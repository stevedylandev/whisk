package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
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
	s.connectedAt = time.Now()
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
	var temp *client.HTTPError
	if !errors.As(err, &temp) {
		temp = nil
	}
	if m.isAuthTarget(s) && !s.reconnecting {
		m.auth.connecting = false
		m.auth.errMsg = fmt.Sprintf("connection failed: %v", err)
		m.setStatus("connection failed", true)
		if temp != nil && temp.RetryAfter > 0 {
			s.retryAfter = temp.RetryAfter
		}
		return nil
	}
	// A temporary refusal carries a Retry-After (the limits doc's
	// "connecting" table) that is never waited less than.
	if temp != nil && temp.RetryAfter > s.retryAfter {
		s.retryAfter = temp.RetryAfter
	}
	// A background server keeps retrying with compliant backoff.
	delay := m.backoff(s)
	m.sessStatus(s, fmt.Sprintf("connection failed: %v (retrying in %s)", err, delay.Round(time.Second)), true)
	s.connecting = true
	return []tea.Cmd{connectCmd(s.id, s.cfg.URL, delay)}
}

// onDisconnect reconnects after a broken connection, honouring the limits
// doc's closed-connections table: exponential backoff with jitter, minutes
// after a server-rate close, and none at all when the server replaced the
// credentials.
func (m *Model) onDisconnect(s *session, err error) []tea.Cmd {
	if m.quit {
		return nil
	}
	// The store keeps its records across reconnects (§2.3); only the
	// transient state of the connection is dropped. The history queue
	// belongs to the lost connection, as do its loading flags.
	s.store.ResetTransient()
	s.histQueue = nil
	s.histBusy = false
	s.histInFlight = historyJob{}
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

	// A close with an explicit policy message decides between a plain
	// backoff, a longer wait, and waiting for the user.
	policyDelay, waitUser, note := classifyClose(err)
	if waitUser {
		s.noReconnect = true
		s.reconnecting = false
		s.retryAfter = 0
		m.sessStatus(s, note, true)
		return nil
	}
	if policyDelay > s.retryAfter {
		s.retryAfter = policyDelay
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
	// The next attempt waits at least what a rate refusal asked for, and
	// otherwise climbs the backoff (see backoff, which consumes it via the
	// attempts counter only up to the refusal's wait).
	delay := m.backoff(s)
	m.sessStatus(s, fmt.Sprintf("disconnected: %v — reconnecting in %s…", errMessage(err), delay.Round(time.Second)), true)
	s.connecting = true
	return []tea.Cmd{connectCmd(s.id, s.cfg.URL, delay)}
}

// backoffDelay is the reconnect delay of attempt n: 1 s doubling to at most
// backoffMax, each scaled ±50% jitter as the limits doc's checklist suggests.
func backoffDelay(n int) time.Duration {
	if n < 0 {
		n = 0
	}
	delay := time.Duration(1<<uint(n)) * time.Second
	if delay > backoffMax {
		delay = backoffMax
	}
	// Full-second jitter within ±50%: half as long at least, half again at
	// most.
	delay = time.Duration(float64(delay) * (0.5 + rand.Float64()))
	if delay < time.Second {
		delay = time.Second
	}
	return delay
}

// backoffMax is the reconnect backoff's ceiling.
const backoffMax = 60 * time.Second

// stableAfter is how long a connection must stay up before the backoff
// resets: about three ping intervals, so a server that cuts every burst
// cannot reset the climb by letting one attempt through.
const stableAfter = 90 * time.Second

// backoff reports the session's next reconnect delay, consuming any retry
// refusal the server asked for, and resetting the climb only after a
// connection that stayed up.
func (m *Model) backoff(s *session) time.Duration {
	delay := s.retryAfter
	s.retryAfter = 0
	if delay > 0 && s.reconnectAttempts == 0 {
		// A fresh refusal on a not-yet-climbing backoff: wait exactly what
		// the server asked for, once.
		return delay
	}
	if time.Since(s.connectedAt) >= stableAfter {
		// The connection had stayed up: start the climb over.
		s.reconnectAttempts = 0
	}
	delay2 := backoffDelay(s.reconnectAttempts)
	s.reconnectAttempts++
	if delay2 > delay {
		return delay2
	}
	return delay
}

// closeState classifies a broken connection per the limits doc's
// closed-connections table:
//
//   - A credential close ("Device token replaced…", "Removed by an admin…",
//     "Signed out…") must not reconnect automatically; the user is asked.
//   - An address frame-rate or budget close (1013) is waited out in minutes,
//     not seconds.
//   - Any other close climbs the usual backoff.
//
// The returned delay is a minimum for the next attempt when positive, and 0
// for the ordinary climb; a code that demands a user makes waitUser true.
func classifyClose(err error) (delay time.Duration, waitUser bool, note string) {
	if err == nil {
		return 0, false, ""
	}
	// The client records a clean peer close's status code and reason.
	var ce client.CloseInfo
	var code int
	var reason string
	if client.AsClose(err, &ce) {
		code, reason = ce.Code, ce.Reason
	}
	switch code {
	case 0: // network or transport failure, no close frame
		return 0, false, ""
	case 1001:
		// "Connection idle; reconnect to recover" (the stale sweep): an
		// ordinary disconnect; whisk pings, so a live tab reopens well
		// before its own socket turns stale.
		return 0, false, ""
	case 1008:
		reason = strings.ToLower(reason)
		switch {
		case strings.Contains(reason, "replaced"),
			strings.Contains(reason, "removed"),
			strings.Contains(reason, "signed out"):
			// Credentials were revoked or rotated: reconnecting with them
			// in a loop would spend authentication attempts for nothing.
			return 0, true, "connection closed by the server: " + reason
		case strings.Contains(reason, "Authentication timed out"):
			// whisk authenticates right after the server frame; reaching
			// the timeout means the network stalled: an ordinary retry.
			return 0, false, ""
		case strings.Contains(reason, "Frame rate limit"),
			strings.Contains(reason, "Too many pending frames"):
			// This connection went over its frame or pending-frames rate:
			// a quiet minute at least, per the table (the frame bucket
			// refills over a minute).
			return 90 * time.Second, false, ""
		default:
			// Repeated policy violations or another close with a reason:
			// some request whisk sends may be at fault.
			return 30 * time.Second, false, ""
		}
	case 1009:
		// A frame over the size bound: whisk refuses oversized frames
		// before sending, so this is unexpected; wait a while.
		return 30 * time.Second, false, ""
	case 1011:
		// "Delivery failed/interrupted; reconnect to recover": ordinary.
		return 0, false, ""
	case 1013:
		// Address frame rate (over about 120 frames a minute from one
		// address, all connections together) or a server-wide budget:
		// the table says minutes of backoff, not an immediate reconnect.
		wait := 2 * time.Minute
		if strings.Contains(reason, "daily") {
			wait = 5 * time.Minute
		}
		return wait, false, ""
	default:
		return 0, false, ""
	}
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
		// A retry_after the server sent on its own: the connection usually
		// stays open (a server-wide frame budget drops notifications
		// instead), so this only raises the floor of the next reconnect's
		// wait. Never retry sooner than it says.
		if d := e.RetryAfterSeconds(); d > 0 {
			delay := time.Duration(d * float64(time.Second))
			if delay > s.retryAfter {
				s.retryAfter = delay
			}
			m.sessStatus(s, fmt.Sprintf("%s — at least %s before reconnecting", e.Message, s.retryAfter.Round(time.Second)), true)
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
	// The server frame advertises ping in seconds; the pinger sends the
	// exact text {"method":"ping"} at that interval, which the runtime
	// answers without counting any frame limit, and watches for silence
	// about two intervals long to cut a connection whose peer has vanished.
	s.client.StartPing(time.Duration(secs * float64(time.Second)))
}
