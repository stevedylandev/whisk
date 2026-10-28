// Package client implements the Apron WebSocket transport: dialing, framing,
// request/reply matching by id, notifications, ping, and disconnect signaling.
//
// It has no UI dependencies. The UI consumes incoming notifications from the
// channel returned by Events, issues requests with Request, and is told about
// a lost connection through Done.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/stevedylandev/whisk/internal/protocol"
)

// Event is an incoming notification. Method is the frame method; Params is its
// raw params. A frame with no id and no method but an error is an unsolicited
// error (§1.1), delivered with Method empty and Error set.
type Event struct {
	Method string
	Params json.RawMessage
	Error  *protocol.Error
}

// Client is a thread-safe Apron client bound to one WebSocket connection.
type Client struct {
	url string

	conn    *websocket.Conn
	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]chan protocol.Response

	events chan Event

	doneOnce sync.Once
	done     chan struct{}
	errMu    sync.Mutex
	err      error

	pingMu     sync.Mutex
	pingCancel context.CancelFunc
}

// Dial opens a WebSocket connection to url and starts the read loop. The server
// sends its server frame immediately on accept; it arrives on Events.
//
// A server that serves its web client at the root and the socket at /ws (the
// web client's convention) answers a root dial with a plain HTTP page; Dial
// then retries at /ws, so the address people open in a browser also works.
func Dial(ctx context.Context, url string) (*Client, error) {
	conn, resp, err := dialWS(ctx, url)
	if err != nil && isPlainHTTP(resp) {
		if alt, ok := wsFallbackURL(url); ok {
			if c2, _, err2 := dialWS(ctx, alt); err2 == nil {
				conn, err, url = c2, nil, alt
			}
		}
	}
	if err != nil {
		return nil, err
	}
	c := &Client{
		url:     url,
		conn:    conn,
		pending: make(map[string]chan protocol.Response),
		events:  make(chan Event, 256),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func dialWS(ctx context.Context, url string) (*websocket.Conn, *http.Response, error) {
	return websocket.Dial(ctx, url, &websocket.DialOptions{
		CompressionMode: websocket.CompressionContextTakeover,
	})
}

// isPlainHTTP reports whether a failed dial reached an HTTP server that did
// not upgrade, as opposed to a network or TLS failure.
func isPlainHTTP(resp *http.Response) bool {
	return resp != nil && resp.StatusCode != http.StatusSwitchingProtocols
}

// wsFallbackURL returns raw with its path set to /ws when it points at the
// root, the path a web client conventionally serves its socket on.
func wsFallbackURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || strings.Trim(u.Path, "/") != "" {
		return "", false
	}
	u.Path = "/ws"
	return u.String(), true
}

// Events returns the channel of incoming notifications (frames with no id).
func (c *Client) Events() <-chan Event { return c.events }

// Done is closed when the connection is lost.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err returns the error that ended the connection, or nil while connected.
func (c *Client) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

// Request sends a request and blocks until its reply arrives, ctx is done, or
// the connection drops. It returns the result payload, a protocol error reply,
// or a transport error.
func (c *Client) Request(ctx context.Context, method string, params any) (json.RawMessage, *protocol.Error, error) {
	id := protocol.NewID()
	data, err := protocol.MarshalRequest(method, id, params)
	if err != nil {
		return nil, nil, err
	}

	ch := make(chan protocol.Response, 1)
	c.pendingMu.Lock()
	if c.pending == nil {
		c.pending = make(map[string]chan protocol.Response)
	}
	c.pending[id] = ch
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	if err := c.write(ctx, data); err != nil {
		return nil, nil, err
	}

	select {
	case resp := <-ch:
		return resp.Result, resp.Error, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-c.done:
		if err := c.Err(); err != nil {
			return nil, nil, err
		}
		return nil, nil, ErrDisconnected
	}
}

// Notify sends a one-way notification (a frame with no id).
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	data, err := protocol.MarshalNotification(method, params)
	if err != nil {
		return err
	}
	return c.write(ctx, data)
}

// Ping sends a bare ping notification. The server answers with a pong.
func (c *Client) Ping(ctx context.Context) error {
	return c.Notify(ctx, "ping", nil)
}

// StartPing begins sending ping notifications every interval. Calling it again
// replaces the previous interval. A non-positive interval stops the pinger.
func (c *Client) StartPing(interval time.Duration) {
	c.pingMu.Lock()
	defer c.pingMu.Unlock()
	c.stopPingLocked()
	if interval <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.pingCancel = cancel
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-c.done:
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, interval/2)
				_ = c.Ping(pctx)
				pcancel()
			}
		}
	}()
}

// Close closes the connection gracefully. Done is closed after the read loop
// notices the close.
func (c *Client) Close() error {
	c.stopPing()
	return c.conn.Close(websocket.StatusNormalClosure, "")
}

// stopPing stops the pinger, if one is running.
func (c *Client) stopPing() {
	c.pingMu.Lock()
	defer c.pingMu.Unlock()
	c.stopPingLocked()
}

// stopPingLocked is stopPing for a caller that holds pingMu.
func (c *Client) stopPingLocked() {
	if c.pingCancel != nil {
		c.pingCancel()
		c.pingCancel = nil
	}
}

func (c *Client) write(ctx context.Context, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.conn.Write(wctx, websocket.MessageText, data)
}

func (c *Client) readLoop() {
	defer func() {
		c.stopPing()
		c.doneOnce.Do(func() { close(c.done) })
	}()

	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			c.setErr(err)
			return
		}
		c.dispatch(data)
	}
}

func (c *Client) dispatch(data []byte) {
	var f protocol.Frame
	if err := json.Unmarshal(data, &f); err != nil {
		// A malformed frame is not actionable by the client; ignore it.
		return
	}
	if f.ID != "" {
		c.pendingMu.Lock()
		ch, ok := c.pending[f.ID]
		c.pendingMu.Unlock()
		if ok {
			ch <- protocol.Response{ID: f.ID, Result: f.Result, Error: f.Error}
		}
		// A reply with an unknown id is dropped.
		return
	}
	// An error that is not a reply to a request has no id and no method
	// (§1.1), such as a server-at-capacity retry_after. Deliver it as an
	// event so the caller can apply the §1.1 reconnect semantics.
	if f.Error != nil && f.Method == "" {
		c.events <- Event{Error: f.Error}
		return
	}
	c.events <- Event{Method: f.Method, Params: f.Params}
}

func (c *Client) setErr(err error) {
	c.errMu.Lock()
	c.err = err
	c.errMu.Unlock()
}

// ErrDisconnected is the error returned to pending requests after a connection
// loss when no more specific error is available.
var ErrDisconnected = errors.New("disconnected")
