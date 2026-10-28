package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stevedylandev/whisk/internal/protocol"
	"github.com/stevedylandev/whisk/internal/state"
)

// Request replies. Every request goes out through sessionRequest with a tag;
// its reply comes back as a requestDoneMsg and is dispatched here by tag.

func (m *Model) handleRequestDone(s *session, msg requestDoneMsg) tea.Cmd {
	if msg.tag == "server_help" {
		return m.onServerHelpDone(s, msg)
	}
	isLog := msg.tag == "log_history" || msg.tag == "log_forward"
	if isLog && (msg.err != nil || msg.rpcErr != nil) {
		return m.onLogError(s, msg)
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
	case "history":
		if res, ok := decodeResult[protocol.HistoryResult](m, s, msg, "history"); ok {
			m.onRoomHistory(s, msg.roomID, &res)
		}
	case "history_forward":
		if res, ok := decodeResult[protocol.HistoryResult](m, s, msg, "history"); ok {
			return m.onRoomForward(s, msg.roomID, &res)
		}
	case "log_history":
		return m.onLogHistory(s, msg.result)
	case "log_forward":
		return m.onLogForward(s, msg.result)
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
		m.sessStatus(s, fmt.Sprintf("rate limited: %s", e.Message), true)
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
