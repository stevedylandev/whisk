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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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

	// lastRecvMu guards lastRecv, the time of the last frame of any kind
	// received, for the ping watchdog.
	lastRecvMu sync.Mutex
	lastRecv   time.Time

	pingMu     sync.Mutex
	pingCancel context.CancelFunc
}

// Dial opens a WebSocket connection to url and starts the read loop. The server
// sends its server frame immediately on accept; it arrives on Events.
//
// A server that serves its web client at the root and the socket at /ws (the
// web client's convention) answers a root dial with a plain HTTP page; Dial
// then retries at /ws, so the address people open in a browser also works.
//
// A server that refuses the upgrade with an HTTP error status (429, 503, 403,
// 400 - the limits doc's "connecting" table) returns an *HTTPError carrying
// the response's message and Retry-After, over the opaque dial error.
func Dial(ctx context.Context, url string) (*Client, error) {
	conn, resp, err := dialWS(ctx, url)
	if err != nil && isPlainHTTP(resp) {
		if alt, ok := wsFallbackURL(url); ok {
			if c2, _, err2 := dialWS(ctx, alt); err2 == nil {
				conn, err, url = c2, nil, alt
			} else {
				resp, _ = alt2(ctx, alt) // keep the alt's answer, if any, for the error
			}
		}
	}
	if err != nil {
		if h := newHTTPError(resp); h != nil {
			return nil, h
		}
		return nil, err
	}
	c := &Client{
		url:     url,
		conn:    conn,
		pending: make(map[string]chan protocol.Response),
		events:  make(chan Event, 256),
		done:    make(chan struct{}),
	}
	c.markRecv()
	go c.readLoop()
	return c, nil
}

// alt2 dials alt and returns the handshake response even on failure.
func alt2(ctx context.Context, alt string) (*http.Response, error) {
	_, resp, err := dialWS(ctx, alt)
	return resp, err
}

// HTTPError is a failed WebSocket handshake answered with an HTTP error
// status (429, 503, 403, 400) instead of the upgrade: a limit or condition
// refused before any socket existed. RetryAfter is the Retry-After header of
// a temporary refusal.
type HTTPError struct {
	Status     int
	StatusText string
	Message    string        // the body's {"error": "…"}
	RetryAfter time.Duration // 0 when the answer carried none
}

func (e *HTTPError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.StatusText != "" {
		return "connection refused: " + e.StatusText
	}
	return "connection refused"
}

// IsTemporary reports whether the answer suggested trying again: a 429 or
// 503, possibly with a Retry-After.
func (e *HTTPError) IsTemporary() bool {
	return e.Status == http.StatusTooManyRequests || e.Status == http.StatusServiceUnavailable
}

// newHTTPError turns a failed handshake's response, when it is an HTTP error
// the server answered (rather than a network failure), into an *HTTPError.
func newHTTPError(resp *http.Response) *HTTPError {
	if resp == nil || resp.StatusCode < 400 {
		return nil
	}
	e := &HTTPError{
		Status:     resp.StatusCode,
		StatusText: http.StatusText(resp.StatusCode),
	}
	if resp.Header.Get("Retry-After") != "" {
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	// The body is {"error": "<message>"}; the dialer keeps only its first
	// kilobyte, which the body stays within.
	if resp.Body != nil {
		if b, err := io.ReadAll(io.LimitReader(resp.Body, 1024)); err == nil {
			var body struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(b, &body) == nil {
				e.Message = body.Error
			}
		}
	}
	return e
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

// CloseCode returns the WebSocket status code the peer closed with, or -1
// when the connection ended otherwise. The limits doc's closed-connections
// table tells what each code means.
func (c *Client) CloseCode() int {
	var ci CloseInfo
	if !AsClose(c.Err(), &ci) {
		return -1
	}
	return ci.Code
}

// CloseReason returns the reason the peer gave for a clean close, if any.
func (c *Client) CloseReason() string {
	var ci CloseInfo
	if !AsClose(c.Err(), &ci) {
		return ""
	}
	return ci.Reason
}

// markRecv records a frame arriving, for the ping watchdog: a peer answered,
// so the connection is alive.
func (c *Client) markRecv() {
	c.lastRecvMu.Lock()
	c.lastRecv = time.Now()
	c.lastRecvMu.Unlock()
}

// silentFor reports how long the connection has received no frame of any
// kind.
func (c *Client) silentFor() time.Duration {
	c.lastRecvMu.Lock()
	defer c.lastRecvMu.Unlock()
	return time.Since(c.lastRecv)
}

// Request sends a request and blocks until its reply arrives, ctx is done, or
// the connection drops. It returns the result payload, a protocol error reply,
// or a transport error.
func (c *Client) Request(ctx context.Context, method string, params any) (json.RawMessage, *protocol.Error, error) {
	id := protocol.NewID()
	if err := protocol.CheckID(id); err != nil {
		return nil, nil, err
	}
	data, err := protocol.MarshalRequest(method, id, params)
	if err != nil {
		return nil, nil, err
	}
	return c.sendRequest(ctx, data, id)
}

// RequestWithID sends a request with an explicit id and blocks until its reply
// arrives, ctx is done, or the connection drops. Retrying a mutation through
// a burst limit must reuse the same id, so the server deduplicates and never
// posts twice. It returns the result payload, a protocol error reply, or a
// transport error.
func (c *Client) RequestWithID(ctx context.Context, method, id string, params any) (json.RawMessage, *protocol.Error, error) {
	if err := protocol.CheckID(id); err != nil {
		return nil, nil, err
	}
	data, err := protocol.MarshalRequest(method, id, params)
	if err != nil {
		return nil, nil, err
	}
	return c.sendRequest(ctx, data, id)
}

// RequestRaw is RequestWithID with the params already marshaled, so a retry
// can send the exact frame again.
func (c *Client) RequestRaw(ctx context.Context, method, id string, rawParams []byte) (json.RawMessage, *protocol.Error, error) {
	if err := protocol.CheckID(id); err != nil {
		return nil, nil, err
	}
	data, err := protocol.MarshalRequest(method, id, rawParams)
	if err != nil {
		return nil, nil, err
	}
	return c.sendRequest(ctx, data, id)
}

func (c *Client) sendRequest(ctx context.Context, data []byte, id string) (json.RawMessage, *protocol.Error, error) {
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

	// The server closes a connection with more frames sent and not yet
	// processed ("Too many pending frames"); a request that cannot even be
	// written is no better, so an oversized one is refused here.
	if err := protocol.CheckFrame(data); err != nil {
		return nil, nil, err
	}

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

// Notify sends a one-way notification (a frame with no id). An oversized one
// is refused here rather than sent, which would close the connection (1009).
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	data, err := protocol.MarshalNotification(method, params)
	if err != nil {
		return err
	}
	if err := protocol.CheckFrame(data); err != nil {
		return err
	}
	return c.write(ctx, data)
}

// Ping sends a bare ping notification. The server answers with a pong.
func (c *Client) Ping(ctx context.Context) error {
	return c.Notify(ctx, "ping", nil)
}

// StartPing begins sending the server-advertised keepalive at interval: the
// exact text {"method":"ping"}, which the runtime answers without waking the
// server and without counting against any frame limit. Calling it again
// replaces the previous interval. A non-positive interval stops the pinger.
//
// The interval is also the liveness watch: when a connection has received no
// frame of any kind for about two intervals, the connection is presumed dead
// (its peer vanished without a close frame - sleep, a network change) and is
// closed here, so pending requests fail and the caller reconnects.
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
				// About two intervals without a pong or any other frame is
				// the checklist's rule for a dead connection.
				if silence := c.silentFor(); silence > 2*interval {
					c.setErr(fmt.Errorf("no frame for %v; connection presumed dead", silence.Round(time.Second)))
					_ = c.conn.CloseNow()
					return
				}
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
			var ce websocket.CloseError
			if errors.As(err, &ce) {
				d := &disconnectedError{
					cause: err,
					CloseInfo: CloseInfo{
						Code:   int(ce.Code),
						Reason: ce.Reason,
					},
				}
				c.setErr(d)
			} else {
				c.setErr(err)
			}
			return
		}
		if len(data) > 0 {
			c.markRecv()
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
