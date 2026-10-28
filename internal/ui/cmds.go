package ui

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"runtime"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stevedylandev/whisk/internal/client"
	"github.com/stevedylandev/whisk/internal/protocol"
)

// Bubble Tea commands that do I/O off the update loop (dialing, waiting for
// events, requests, notifications), and the messages they report back with.
// Every connection message carries its session id, so Update can drop one
// from a removed session or a replaced connection.

var errNotConnected = errors.New("not connected")

// connectCmd dials a session's server after delay.
func connectCmd(sid int, url string, delay time.Duration) tea.Cmd {
	return func() tea.Msg {
		if delay > 0 {
			time.Sleep(delay)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		c, err := client.Dial(ctx, url)
		if err != nil {
			return connectErrMsg{sid: sid, err: err}
		}
		return connectedMsg{sid: sid, c: c}
	}
}

func waitForEvent(sid int, c *client.Client) tea.Cmd {
	return func() tea.Msg {
		select {
		case ev := <-c.Events():
			return eventMsg{sid: sid, c: c, ev: ev}
		case <-c.Done():
			return disconnectMsg{sid: sid, c: c, err: c.Err()}
		}
	}
}

// sessionRequest runs one request on a session's connection and tags the reply
// with the session.
func sessionRequest(s *session, tag, roomID, method string, params any) tea.Cmd {
	sid, c := s.id, s.client
	return func() tea.Msg {
		if c == nil {
			return requestDoneMsg{sid: sid, tag: tag, roomID: roomID, err: errNotConnected}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, rpcErr, err := c.Request(ctx, method, params)
		return requestDoneMsg{sid: sid, tag: tag, roomID: roomID, result: result, rpcErr: rpcErr, err: err}
	}
}

func requestCmd(s *session, tag, method string, params any) tea.Cmd {
	return sessionRequest(s, tag, "", method, params)
}

// roomJoinCmd joins a room and reports its id, so the room can be opened once
// the room_list refresh lands.
func roomJoinCmd(s *session, roomID string) tea.Cmd {
	return sessionRequest(s, "room_join", roomID, "room_join", map[string]string{"room_id": roomID})
}

func notifyCmd(s *session, method string, params any) tea.Cmd {
	c := s.client
	return func() tea.Msg {
		if c == nil {
			return noopMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.Notify(ctx, method, params)
		return noopMsg{}
	}
}

// openBrowserCmd opens url in the system browser.
func openBrowserCmd(url string) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", url)
		case "windows":
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
		default:
			cmd = exec.Command("xdg-open", url)
		}
		_ = cmd.Start()
		return noopMsg{}
	}
}

// Connection messages carry the session id and client they came from, so a
// message from a removed session or a replaced connection is dropped.
type connectedMsg struct {
	sid int
	c   *client.Client
}

type connectErrMsg struct {
	sid int
	err error
}

type eventMsg struct {
	sid int
	c   *client.Client
	ev  client.Event
}

type disconnectMsg struct {
	sid int
	c   *client.Client
	err error
}

type noopMsg struct{}

type tickMsg struct{}

type statusTickMsg struct{}

type spinTickMsg struct{}

// idleTickMsg drives idle reporting (§4.5.3): a connection starts attended,
// and reports idle once nobody has given it input for a while.
type idleTickMsg time.Time

type requestDoneMsg struct {
	sid    int
	tag    string
	roomID string
	result json.RawMessage
	rpcErr *protocol.Error
	err    error
}
