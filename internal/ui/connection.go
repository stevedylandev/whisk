package ui

import (
	"encoding/json"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stevedylandev/whisk/internal/client"
	"github.com/stevedylandev/whisk/internal/protocol"
)

// Connection lifecycle of a session: connect, greeting, reconnect with
// backoff, unsolicited server errors (§1.1), and routing of incoming
// notifications into the store.

// isAuthTarget reports whether s is the server the sign-in screen is working
// on. Other sessions sign in on their own in the background.
func (m *Model) isAuthTarget(s *session) bool {
	return m.screen == screenAuth && m.auth.sess == s
}

func (m *Model) onConnected(s *session, c *client.Client) []tea.Cmd {
	s.client = c
	s.connecting = false
	s.reconnecting = false
	s.reconnectAttempts = 0
	// Commands can depend on who signs in, so a new connection asks again.
	s.serverHelp = serverHelp{gen: s.serverHelp.gen + 1}
	if m.isAuthTarget(s) {
		m.auth.connecting = true // until the greeting arrives
		m.auth.errMsg = ""
		m.setStatus("connected, waiting for server…", false)
	} else if s.everAuthed {
		m.sessStatus(s, "reconnected", false)
	}
	return []tea.Cmd{waitForEvent(s.id, c)}
}

func (m *Model) onConnectErr(s *session, err error) []tea.Cmd {
	s.connecting = false
	if m.isAuthTarget(s) && !s.reconnecting {
		m.auth.connecting = false
		m.auth.errMsg = fmt.Sprintf("connection failed: %v", err)
		m.setStatus("connection failed", true)
		return nil
	}
	// A background server keeps retrying with backoff.
	s.reconnectAttempts++
	if s.reconnectAttempts > 6 {
		s.reconnecting = false
		m.sessStatus(s, fmt.Sprintf("reconnect failed: %v (press r to retry)", err), true)
		return nil
	}
	s.reconnecting = true
	delay := time.Duration(1<<uint(s.reconnectAttempts)) * time.Second
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	m.sessStatus(s, fmt.Sprintf("connection failed: %v (retrying in %s)", err, delay.Round(time.Second)), true)
	s.connecting = true
	return []tea.Cmd{connectCmd(s.id, s.cfg.URL, delay)}
}

func (m *Model) onDisconnect(s *session, err error) []tea.Cmd {
	if m.quit {
		return nil
	}
	// Keep the store (records survive reconnects, §2.3); drop only the
	// transient state of the connection.
	s.store.ResetTransient()
	s.logCatchUp = nil
	s.logTo = ""
	s.closeClient()
	m.chat.dirty = true

	if m.isAuthTarget(s) {
		// Dropped mid sign-in: back to the server prompt for the user to
		// retry.
		s.reconnecting = false
		m.auth.phase = phaseServer
		m.auth.connecting = false
		m.auth.busy = false
		if m.auth.errMsg == "" {
			m.auth.errMsg = "disconnected: " + errMessage(err)
		}
		return nil
	}
	if s.noReconnect {
		// A denied error (§1.1): wait for the user to act.
		s.reconnecting = false
		m.sessStatus(s, "disconnected: "+errMessage(err), true)
		return nil
	}
	if s.needsSignIn {
		// Nothing to resume without the user; sign-in reconnects.
		s.reconnecting = false
		return nil
	}
	s.reconnecting = true
	s.reconnectAttempts = 0
	// An unsolicited retry_after (§1.1) sets the wait for the next attempt.
	delay := time.Second
	if s.retryAfter > 0 {
		delay = s.retryAfter
		s.retryAfter = 0
	}
	m.sessStatus(s, fmt.Sprintf("disconnected: %v — reconnecting in %s…", errMessage(err), delay.Round(time.Second)), true)
	s.connecting = true
	return []tea.Cmd{connectCmd(s.id, s.cfg.URL, delay)}
}

// errMessage renders an error for the status line.
func errMessage(err error) string {
	if err == nil {
		return "connection closed"
	}
	return err.Error()
}

func (m *Model) onEvent(s *session, ev client.Event) []tea.Cmd {
	var cmds []tea.Cmd

	// An unsolicited error frame: no id, no method (§1.1).
	if ev.Error != nil {
		cmds = append(cmds, m.onServerError(s, ev.Error)...)
		if s.client != nil {
			cmds = append(cmds, waitForEvent(s.id, s.client))
		}
		return cmds
	}

	active := m.isCur(s)
	switch ev.Method {
	case "server":
		var srv protocol.Server
		if err := json.Unmarshal(ev.Params, &srv); err == nil {
			s.store.ApplyServer(&srv)
			m.startPing(s)
			if !s.authed {
				cmds = append(cmds, m.onGreeting(s)...)
			}
		}
	case "message":
		var pm protocol.Message
		_ = json.Unmarshal(ev.Params, &pm)
		if m.takeServerHelp(s, &pm) {
			break
		}
		if active && pm.RoomID == m.chat.roomID && m.chat.viewport.AtBottom() {
			m.chat.stickBottom = true
		}
		// A new message (not an edit or delete of a known one) from someone
		// else, in a room that is not open, is unread.
		isNew := pm.MessageID != "" && pm.RoomID != "" && !pm.Deleted &&
			(pm.LogID == "" || pm.LogID == pm.MessageID) &&
			s.store.MessageByID(pm.MessageID) == nil
		s.store.ApplyNotification("message", ev.Params)
		if isNew && !(active && pm.RoomID == m.chat.roomID) && !fromSelf(s, &pm) {
			s.markUnread(pm.RoomID, mentionsSelf(s, &pm))
		}
		if active && m.chat.roomID == "" && pm.RoomID != "" && pm.MessageID != "" {
			m.chat.roomID = pm.RoomID
			m.chat.stickBottom = true
			m.refreshRoomItems()
			// The live message revealed the default room (§3.4). Unless
			// the room-less query covers it, page its backlog directly.
			if !s.store.Log.Loading && !s.usesLog() {
				cmds = append(cmds, m.loadRoomHistory(s, pm.RoomID))
			}
		}
		// A transient notice (no message_id, §3.5.3) is shown, never
		// installed: inline in its room, or via the N overlay when it
		// carries no room.
		if pm.MessageID == "" && pm.RoomID == "" {
			text := ""
			if pm.Body != nil {
				text = pm.Body.Text
			}
			m.sessStatus(s, "notice: "+text, false)
		}
		if active {
			m.chat.dirty = true
		}
	case "user", "reactions", "room_update", "status":
		s.store.ApplyNotification(ev.Method, ev.Params)
		if ev.Method == "room_update" {
			m.refreshRoomItems()
		}
		if active {
			m.chat.dirty = true
		}
	case "activity":
		// Typing is transient and shown in the header, which recomputes every
		// frame; no need to rebuild message content.
		s.store.ApplyNotification("activity", ev.Params)
	}

	if s.client != nil {
		cmds = append(cmds, waitForEvent(s.id, s.client))
	}
	return cmds
}

// onGreeting decides how a session signs in once its server frame arrives.
// The server on the sign-in screen resumes with its token or shows the method
// chooser; a background server signs in on its own when it can, and is
// otherwise marked as needing sign-in in the sidebar.
func (m *Model) onGreeting(s *session) []tea.Cmd {
	if m.isAuthTarget(s) {
		m.loadAuthChoices(s)
		if s.cfg.Token != "" && hasScheme(m.auth.schemes, "token") {
			// A saved token resumes automatically (§3.2.1), instead of
			// re-prompting for sign-in.
			return []tea.Cmd{m.sendAuthScheme(s, "token")}
		}
		m.auth.phase = phaseChoose
		m.auth.connecting = false
		m.setStatus("choose a sign-in method", false)
		return nil
	}
	if s.needsSignIn {
		return nil
	}
	if scheme := autoScheme(s); scheme != "" {
		return []tea.Cmd{m.sendAuthScheme(s, scheme)}
	}
	s.needsSignIn = true
	m.sessStatus(s, "sign in needed (select the server and press enter)", true)
	return nil
}

// onServerError applies the §1.1 semantics of an error that is not a reply to
// a request: after retry_after, wait the delay before reconnecting; after
// denied, do not reconnect automatically.
func (m *Model) onServerError(s *session, e *protocol.Error) []tea.Cmd {
	switch e.Code {
	case protocol.ErrDenied:
		// The server rejected the connection. Stop auto-reconnect; the user
		// decides what to do from the sidebar.
		s.noReconnect = true
		s.reconnecting = false
		s.needsSignIn = true
		s.closeClient()
		if m.isAuthTarget(s) {
			m.auth.errMsg = "server rejected the connection: " + e.Message
			m.auth.phase = phaseServer
			m.auth.connecting = false
			m.auth.busy = false
		}
		m.sessStatus(s, "denied: "+e.Message, true)
		m.chat.dirty = true
		return nil
	case protocol.ErrRetryAfter:
		if d := e.RetryAfterSeconds(); d > 0 {
			s.retryAfter = time.Duration(d * float64(time.Second))
			m.sessStatus(s, fmt.Sprintf("%s — waiting %s before reconnecting", e.Message, s.retryAfter.Round(time.Second)), true)
		} else {
			m.sessStatus(s, e.Message, true)
		}
		return nil
	default:
		m.sessStatus(s, "server error: "+e.Message, true)
		return nil
	}
}

func (m *Model) startPing(s *session) {
	if s.client == nil || s.store.Server == nil || s.store.Server.Ping == nil {
		return
	}
	secs := *s.store.Server.Ping
	if secs <= 0 {
		return
	}
	s.client.StartPing(time.Duration(secs * float64(time.Second)))
}
