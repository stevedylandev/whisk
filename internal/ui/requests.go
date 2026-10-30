package ui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stevedylandev/whisk/internal/protocol"
	"github.com/stevedylandev/whisk/internal/state"
)

// Request replies. Every request goes out through sessionRequest with a tag;
// its reply comes back as a requestDoneMsg and is dispatched here by tag.

// historyTag reports whether a request tag is a history page reply, which is
// always drained through the session's one-page-at-a-time history queue.
func historyTag(tag string) bool {
	switch tag {
	case "history", "history_forward", "log_history", "log_forward":
		return true
	}
	return false
}

func (m *Model) handleRequestDone(s *session, msg requestDoneMsg) tea.Cmd {
	if msg.tag == "server_help" {
		return m.onServerHelpDone(s, msg)
	}
	if historyTag(msg.tag) {
		return m.handleHistoryDone(s, msg)
	}
	if msg.err != nil {
		if strings.HasPrefix(msg.tag, "auth") && m.isAuthTarget(s) {
			m.auth.busy = false
			m.auth.errMsg = msg.err.Error()
		}
		m.sessStatus(s, fmt.Sprintf("%s: %v", msg.tag, msg.err), true)
		return nil
	}
	if msg.rpcErr != nil {
		m.handleRPCError(s, msg)
		if cmd := m.retryWrite(s, msg); cmd != nil {
			return cmd
		}
		return nil
	}

	switch msg.tag {
	case "auth", "auth_code":
		if res, ok := decodeResult[protocol.AuthResult](m, s, msg, "auth"); ok {
			return m.finishAuth(s, res)
		}
	case "auth_email":
		m.onAuthEmailSent(s)
	case "auth_external":
		return m.onAuthExternal(s, msg.result)
	case "room_list":
		if res, ok := decodeResult[protocol.RoomListResult](m, s, msg, "room_list"); ok {
			return m.onRoomList(s, &res)
		}
	case "room_join":
		// Open the joined room once room_list makes its record available.
		s.pendingRoom = msg.roomID
		return roomListCmd(s)
	case "room_leave":
		return roomListCmd(s)
	case "room_set":
		m.checkPrivateRoom(s, msg.result)
		return roomListCmd(s)
	case "me":
		if res, ok := decodeResult[protocol.MeResult](m, s, msg, "me"); ok {
			s.store.ApplyMe(&res)
			m.chat.dirty = true
		}
	case "message_upload", "command_upload":
		if res, ok := decodeResult[protocol.MessageResult](m, s, msg, "upload"); ok {
			return m.onUploadCreated(s, &res)
		}
	case "message", "command", "reactions", "status":
		// The broadcast carries the snapshot; nothing else to do here.
	}
	// A write the server finally accepted is through with its retry count.
	if writeTag(msg.tag) {
		delete(s.writeRetries, msg.id)
	}
	return nil
}

// writeRetryMax bounds how many times a mutation delayed by a posting limit
// is resent on its own with the same id.
const writeRetryMax = 2

// writeTag reports whether a request tag is one the server charges as
// posting: message creates, edits, moves and deletes, reaction changes,
// commands (which are messages), room creation, joins and leaves, and name
// changes (a `me`). A retry limit covers all of these.
func writeTag(tag string) bool {
	switch tag {
	case "message", "command", "message_upload", "command_upload",
		"reactions", "room_join", "room_leave", "room_set", "me":
		return true
	}
	return false
}

// retryWrite re-sends a mutation that a posting burst limit refused, with
// the same request id, after data.retry_after: the server deduplicates ids
// it accepted in the last 24 hours, so a retry never applies the request
// twice, whether it was refused or was accepted but lost. The retry goes
// only to the connection that took the request; when that one is gone, the
// user resends from the composer instead.
func (m *Model) retryWrite(s *session, msg requestDoneMsg) tea.Cmd {
	e := msg.rpcErr
	if e == nil || e.Code != protocol.ErrRetryAfter {
		return nil
	}
	if !writeTag(msg.tag) || msg.id == "" || msg.method == "" ||
		msg.c == nil || msg.c != s.client {
		return nil
	}
	// Retry what the limit names a write, or a refusal with no category at
	// all, which a `me` write and a server-wide budget carry.
	if cats := e.Limits(); len(cats) > 0 && !slices.Contains(cats, "write") {
		return nil
	}
	// The frame must also fit: a retry of an oversized request cannot work.
	if err := protocol.CheckID(msg.id); err != nil {
		return nil
	}
	if s.writeRetries[msg.id] >= writeRetryMax {
		return nil
	}
	if s.writeRetries == nil {
		s.writeRetries = map[string]int{}
	}
	s.writeRetries[msg.id]++
	delay := 5 * time.Second
	if d := e.RetryAfterSeconds(); d > 0 {
		delay = time.Duration(d * float64(time.Second))
	}
	sid, replying := msg.sid, msg.c
	m.sessStatus(s, fmt.Sprintf("%s rate limited; retrying in %s (same request id)", msg.tag, delay.Round(time.Second)), true)
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return requestRawMsg(sid, replying, msg.tag, msg.roomID, msg.method, msg.id, msg.rawParams)()
	})
}

// handleHistoryDone applies one history request's reply, then frees the
// connection's single in-flight history slot and starts the next queued
// page. Every history request passes through here.
func (m *Model) handleHistoryDone(s *session, msg requestDoneMsg) tea.Cmd {
	isLog := msg.tag == "log_history" || msg.tag == "log_forward"
	var cmds []tea.Cmd

	switch {
	case msg.err != nil:
		// A transport failure: the page is dropped, its waiting room (or
		// the log) stops waiting, and a later scroll or reconnect re-issues
		// it.
		m.sessStatus(s, msg.tag+": "+msg.err.Error(), true)
		if isLog {
			s.store.Log.Loading = false
		}
		if msg.roomID != "" {
			if r := s.store.Room(msg.roomID); r != nil && s.logCatchUp == nil {
				r.Loading = false
			}
		}
	case msg.rpcErr != nil:
		cmds = m.onHistoryError(s, msg, isLog)
	default:
		var res protocol.HistoryResult
		if err := json.Unmarshal(msg.result, &res); err != nil {
			m.sessStatus(s, "bad history result: "+err.Error(), true)
			if isLog {
				s.store.Log.Loading = false
				// An unreadable page stops a one-stream recovery; its
				// rooms fall back to per-room pages (§4.3.5).
				if s.logCatchUp != nil {
					if c := tea.Batch(m.abortLogCatchUp(s)...); c != nil {
						cmds = append(cmds, c)
					}
				}
			} else if r := s.store.Room(msg.roomID); r != nil {
				r.Loading = false
			}
		} else {
			switch msg.tag {
			case "history":
				m.onRoomHistory(s, msg.roomID, &res)
			case "history_forward":
				if c := m.onRoomForward(s, msg.roomID, &res); c != nil {
					cmds = append(cmds, c)
				}
			case "log_history":
				m.onLogHistory(s, msg.result)
			case "log_forward":
				if c := m.onLogForward(s, msg.result); c != nil {
					cmds = append(cmds, c)
				}
			}
		}
	}

	// Whatever the reply was, the connection's one history slot is free:
	// the next queued page may go out.
	if c := m.popHistoryCmd(s); c != nil {
		cmds = append(cmds, c)
	}
	switch len(cmds) {
	case 0:
		return nil
	case 1:
		return cmds[0]
	default:
		return tea.Batch(cmds...)
	}
}

// onHistoryError handles an error reply to a history request: an unsupported
// room-less query falls back to per-room pages, and a read rate limit is
// retried on a timer that honours data.retry_after.
func (m *Model) onHistoryError(s *session, msg requestDoneMsg, isLog bool) []tea.Cmd {
	e := msg.rpcErr
	var cmds []tea.Cmd

	if e.Code == protocol.ErrUnsupported {
		if isLog {
			// (§4.3) The room-less history query is handled unsupported:
			// turn it off for the session and fall back to per-room pages.
			cmds = m.onLogUnsupported(s)
		} else {
			m.handleRPCError(s, msg)
		}
		return cmds
	}
	if e.Code == protocol.ErrRetryAfter {
		return m.onHistoryRateLimited(s, msg)
	}

	// server_help's command path never lands here; a plain failure keeps
	// the old shape: catch-up abandoned, loading flags cleared.
	m.handleRPCError(s, msg)
	if isLog {
		if s.logCatchUp != nil {
			cmds = m.abortLogCatchUp(s)
		}
		s.store.Log.Loading = false
		if r := s.store.Room(m.chat.roomID); r != nil && s.logCatchUp == nil {
			r.Loading = false
		}
	} else if r := s.store.Room(msg.roomID); r != nil {
		r.Loading = false
	}
	return cmds
}

// onLogUnsupported carries what the old error handler did for an
// unsupported room-less reply (§4.3): the room-less query is off for the
// session, catch-up falls back to per rooms, and its loading state clears.
func (m *Model) onLogUnsupported(s *session) []tea.Cmd {
	s.logUnsupported = true
	s.store.Log.Loading = false
	if r := s.store.Room(m.chat.roomID); r != nil {
		r.Loading = false
	}
	var cmds []tea.Cmd
	if s.logCatchUp != nil {
		cmds = m.abortLogCatchUp(s)
	} else if m.isCur(s) {
		// A server without rooms: page the room a live message revealed,
		// if any (otherwise wait for one, §3.4).
		if c := m.loadRoomHistory(s, m.chat.roomID); c != nil {
			cmds = append(cmds, c)
		}
	}
	return cmds
}

// onHistoryRateLimited schedules a page's retry after data.retry_after
// (never sooner), pausing only this read and leaving the rest of the client
// working (extension ext:limits).
func (m *Model) onHistoryRateLimited(s *session, msg requestDoneMsg) []tea.Cmd {
	delay := 5 * time.Second
	if d := msg.rpcErr.RetryAfterSeconds(); d > 0 {
		delay = time.Duration(d * float64(time.Second))
	}
	m.sessStatus(s, fmt.Sprintf("history rate limited: %s — retrying in %s", msg.rpcErr.Message, delay.Round(time.Second)), true)

	// The in-flight page is retried with its exact params; queued pages
	// never go out sooner anyway.
	if job := s.takeHistInFlight(); job.tag != "" {
		return []tea.Cmd{m.retryHistory(s, job, delay)}
	}
	return nil
}

// decodeResult unmarshals a reply's result, reporting a malformed one on the
// status line as "bad <what> result".
func decodeResult[T any](m *Model, s *session, msg requestDoneMsg, what string) (T, bool) {
	var res T
	if err := json.Unmarshal(msg.result, &res); err != nil {
		m.sessStatus(s, "bad "+what+" result: "+err.Error(), true)
		return res, false
	}
	return res, true
}

// roomListCmd refreshes a session's room list.
func roomListCmd(s *session) tea.Cmd {
	return requestCmd(s, "room_list", "room_list", roomListParams(s.store))
}

// onRoomList applies a room_list result, opens a room when one is due, and
// starts forward recovery for rooms whose log moved while we were away.
func (m *Model) onRoomList(s *session, res *protocol.RoomListResult) tea.Cmd {
	s.store.ApplyRoomList(res)
	m.refreshRoomItems()
	var cmds []tea.Cmd
	if id := s.pendingRoom; id != "" {
		s.pendingRoom = ""
		if s.store.Room(id) != nil {
			cmds = append(cmds, m.selectRoom(s, id))
		} else if m.isCur(s) {
			cmds = append(cmds, m.selectDefaultRoom(s))
		}
	} else if m.isCur(s) || (m.chat.roomID == "" && !m.cur().authed) {
		// Open a room on the current server, or on the first server that has
		// one when the current one has nothing to show.
		cmds = append(cmds, m.selectDefaultRoom(s))
	}
	// Forward recovery (§4.3.5), e.g. after a reconnect.
	cmds = append(cmds, m.catchUpRooms(s)...)
	return tea.Batch(cmds...)
}

// checkPrivateRoom does the §4.2.4 check after creating a room with
// private:true: the room_update joined record (already applied) must carry
// private:true before the client posts in it.
func (m *Model) checkPrivateRoom(s *session, result json.RawMessage) {
	if !s.expectPrivate {
		return
	}
	s.expectPrivate = false
	var res protocol.RoomSetResult
	if json.Unmarshal(result, &res) != nil {
		return
	}
	if r := s.store.Room(res.RoomID); r == nil || !r.Record.Private {
		m.sessStatus(s, "server did not keep the room private; not using it", true)
	}
}

// onUploadCreated PUTs the pending file to the write_url the server issued
// for its upload embed (§4.8.3).
func (m *Model) onUploadCreated(s *session, res *protocol.MessageResult) tea.Cmd {
	up := s.pendingUpload
	s.pendingUpload = nil
	if up == nil {
		return nil
	}
	for _, e := range res.Embeds {
		if e.WriteURL != "" {
			return uploadCmd(e.WriteURL, up.mimeType, up.data, up.path)
		}
	}
	m.sessStatus(s, "upload: server returned no write URL", true)
	return nil
}

func (m *Model) handleRPCError(s *session, msg requestDoneMsg) {
	e := msg.rpcErr
	switch e.Code {
	case protocol.ErrDenied:
		m.sessStatus(s, fmt.Sprintf("%s denied: %s", msg.tag, e.Message), true)
	case protocol.ErrRetryAfter:
		if limits := e.Limits(); len(limits) > 0 {
			m.sessStatus(s, fmt.Sprintf("rate limited (%s): %s", strings.Join(limits, ", "), e.Message), true)
		} else {
			m.sessStatus(s, fmt.Sprintf("rate limited: %s", e.Message), true)
		}
	default:
		m.sessStatus(s, fmt.Sprintf("%s error (%d): %s", msg.tag, e.Code, e.Message), true)
	}
	if !strings.HasPrefix(msg.tag, "auth") {
		return
	}
	// A failed auth step clears any in-flight state and surfaces the message
	// on the sign-in screen for retry.
	if e.Code == protocol.ErrDenied && s.lastAuthScheme == "token" && s.cfg.Token != "" {
		// The saved token was rejected (§3.2.1): drop it so the next
		// sign-in asks for a method again.
		s.cfg.Token = ""
		m.persist(s)
	}
	if m.isAuthTarget(s) {
		m.auth.busy = false
		m.auth.errMsg = e.Message
		if m.auth.phase == phaseServer && s.store.Server != nil {
			// The automatic token resume failed: offer the chooser.
			m.loadAuthChoices(s)
			m.auth.phase = phaseChoose
			m.auth.connecting = false
		}
		return
	}
	if !s.authed {
		// A background sign-in failed. A denied one would strand the
		// session in a reconnect loop (§1.1, §3.2.1): stop reconnecting
		// and wait for the user to sign in from the sidebar.
		s.needsSignIn = true
		if e.Code == protocol.ErrDenied {
			s.noReconnect = true
			s.reconnecting = false
			m.sessStatus(s, e.Message+" — select the server to sign in again", true)
		}
		m.chat.dirty = true
	}
}

// roomListParams builds the room_list request (§4.2.1): all rooms with their
// members, so not_joined rooms appear in the sidebar; on a resume, the
// smallest checkpoint filters the listing to rooms that changed (§4.3.5).
func roomListParams(s *state.Store) protocol.RoomListParams {
	p := protocol.RoomListParams{Filter: "all", Members: true}
	if cp, ok := s.MinCheckpoint(); ok {
		p.LatestLogID = cp
	}
	return p
}
