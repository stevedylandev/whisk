package ui

import (
	"encoding/json"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stevedylandev/whisk/internal/protocol"
)

// History paging (§4.3) and reconnect recovery (§4.3.5).
//
// Each joined room pages its own log: the newest page when it is first
// opened, older pages as the user scrolls up, and forward pages from its
// checkpoint after a reconnect.
//
// Room-less history: a history request without room_id pages the combined
// log of every room whose deliveries the connection receives (§3.4). whisk
// uses it in two places:
//
//   - On a server with history but no rooms, sign-in loads the newest page of
//     the whole log, which reveals the default room's id and its backlog
//     before any live message arrives (R2). Older pages and reconnect
//     recovery use the same query.
//   - On a server with rooms, reconnect recovery pages one forward stream
//     instead of one per changed room (R4).
//
// A server may reply unsupported (§4.3); whisk then falls back to per-room
// queries and does not ask again on that session.

// historyPageSize is the limit sent with every history request. A page holds
// at most 50 records (§4.3); whisk asks for that cap, so catching up spends
// as few requests as possible, and a server may answer fewer.
const historyPageSize = protocol.HistoryMaxLimit

// historyJob is one pending history request. Only one may be in flight per
// connection (a second is answered "History request already in progress"),
// so further requests wait here until the page in flight is applied.
type historyJob struct {
	// tag of the request and of the handler that applies its page:
	// "history", "history_forward", "log_history" or "log_forward".
	tag    string
	roomID string
	// params sent with the request; re-used verbatim on a read retry.
	params protocol.HistoryParams
	// tries counts how many times this job was retried after a read rate
	// limit; at most historyRetryMax.
	tries int
}

// historyRetryMax bounds self-retries of a history request a read rate limit
// delayed; a third read refusal is left for the user's next scroll.
const historyRetryMax = 2

// queueHistory appends a history request to a session's queue and starts it
// when the connection is free. The returned command is the request, when one
// starts now.
func (m *Model) queueHistory(s *session, tag, roomID string, params protocol.HistoryParams) tea.Cmd {
	job := historyJob{tag: tag, roomID: roomID, params: params}
	s.histQueue = append(s.histQueue, job)
	return m.popHistory(s)
}

// popHistory sends the oldest queued history request, if the connection is
// free. It runs when a page lands or when a queued request first appears on
// an idle connection. A request on a missing connection reports back at once
// through the usual reply path, which frees the slot again.
func (m *Model) popHistory(s *session) tea.Cmd {
	if s.histBusy || len(s.histQueue) == 0 {
		return nil
	}
	job := s.histQueue[0]
	s.histQueue = s.histQueue[1:]
	s.histBusy = true
	s.histInFlight = job
	if job.tag == "history" || job.tag == "history_forward" {
		return sessionRequest(s, job.tag, job.roomID, "history", job.params)
	}
	return requestCmd(s, job.tag, "history", job.params)
}

// takeHistInFlight returns the page whose reply is being handled, and clears
// the slot's record.
func (s *session) takeHistInFlight() historyJob {
	job := s.histInFlight
	s.histInFlight = historyJob{}
	return job
}

// popHistoryCmd drains the queue after any history request settles: the slot
// is free again, so the next queued page is sent.
func (m *Model) popHistoryCmd(s *session) tea.Cmd {
	s.histBusy = false
	return m.popHistory(s)
}

// retryHistory queues a page a read rate limit delayed again, to be sent
// after the delay the server asked for, and only historyRetryMax times: the
// reply must not be demanded sooner than data.retry_after says.
func (m *Model) retryHistory(s *session, job historyJob, delay time.Duration) tea.Cmd {
	if job.tries >= historyRetryMax {
		return nil
	}
	job.tries++
	sid := s.id
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return historyRetryMsg{sid: sid, job: job}
	})
}

// historyRetryMsg delivers a delayed history retry once its wait is over.
type historyRetryMsg struct {
	sid int
	job historyJob
}

// after reports whether log id a is greater than b.
func after(a, b string) bool { return protocol.LogIDAfter(a, b) }

// --- per-room history -------------------------------------------------------

// historyCmd pages a room's log backward from before (the newest page when
// before is empty). It joins a session's history queue: at most one history
// request is in flight per connection, and pages apply in the order they
// were asked for.
func (m *Model) historyCmd(s *session, roomID, before string) tea.Cmd {
	return m.queueHistory(s, "history", roomID, protocol.HistoryParams{
		RoomID: roomID,
		Before: before,
		Limit:  historyPageSize,
	})
}

// historyForwardCmd pages a room's log forward from after up to before, for
// reconnect recovery (§4.3.5).
func (m *Model) historyForwardCmd(s *session, roomID, after, before string) tea.Cmd {
	return m.queueHistory(s, "history_forward", roomID, protocol.HistoryParams{
		RoomID: roomID,
		After:  after,
		Before: before,
		Limit:  historyPageSize,
	})
}

// loadHistoryIfNeeded loads the open room's newest page, if not loaded yet.
func (m *Model) loadHistoryIfNeeded() tea.Cmd {
	return m.loadRoomHistory(m.cur(), m.chat.roomID)
}

// loadRoomHistory requests the newest page of a room's log the first time it
// is opened (§4.3.5, "load the newest page" path).
func (m *Model) loadRoomHistory(s *session, roomID string) tea.Cmd {
	room := s.store.Room(roomID)
	if room == nil || room.Loaded || room.Loading {
		return nil
	}
	if !s.hasCap("history") {
		room.Loaded = true
		return nil
	}
	room.Loading = true
	if m.isCur(s) {
		m.chat.dirty = true
	}
	return m.historyCmd(s, roomID, "")
}

// loadOlder pages the open room (or the combined log) back from its first
// loaded record, keeping the view anchored while the page loads.
func (m *Model) loadOlder() tea.Cmd {
	s := m.cur()
	if s.usesLog() && s.store.Log.Loaded {
		return m.loadOlderLog(s)
	}
	room := s.store.Room(m.chat.roomID)
	if room == nil || room.Loading || !room.MoreBack {
		return nil
	}
	before := protocol.PrevLogID(room.FirstLogID)
	if before == "" {
		return nil
	}
	room.Loading = true
	m.captureScrollAnchor()
	// Do not mark dirty here: the status bar shows "loading…", and the
	// anchor must survive until the history result arrives.
	return m.historyCmd(s, m.chat.roomID, before)
}

// onRoomHistory applies a backward page of a room's log.
func (m *Model) onRoomHistory(s *session, roomID string, res *protocol.HistoryResult) {
	room := s.store.Room(roomID)
	initial := room == nil || !room.Loaded
	s.store.ApplyHistory(roomID, res)
	active := m.isCur(s)
	if initial && active && roomID == m.chat.roomID {
		// The first page is the newest messages; land at the bottom.
		m.chat.stickBottom = true
	}
	// The newest page leaves the room caught up to H (§4.3.5).
	if room := s.store.Room(roomID); room != nil && room.LatestLogID != "" {
		room.Checkpoint = room.LatestLogID
	}
	if active {
		m.chat.dirty = true
	}
}

// onRoomForward applies a forward recovery page of a room's log, continuing
// until the room is caught up to its latest_log_id (§4.3.5).
func (m *Model) onRoomForward(s *session, roomID string, res *protocol.HistoryResult) tea.Cmd {
	room := s.store.Room(roomID)
	if room == nil {
		return nil
	}
	s.store.ApplyHistory(roomID, res)
	if room.BoundPastCheckpoint() {
		// Retention moved past what we still need (§4.3.5): clear the room
		// and start again from the bound.
		s.store.ResetRoomContent(roomID)
		m.chat.dirty = true
		return m.loadRoomHistory(s, roomID)
	}
	if res.More && res.LastLogID != "" {
		room.Loading = true
		return m.historyForwardCmd(s, roomID, protocol.NextLogID(res.LastLogID), room.LatestLogID)
	}
	// Caught up: the checkpoint is now H (§4.3.5).
	room.Checkpoint = room.LatestLogID
	room.Loading = false
	if m.isCur(s) {
		m.chat.dirty = true
	}
	return nil
}

// catchUpRooms starts forward recovery (§4.3.5) for every room whose log
// advanced past its checkpoint, e.g. after a reconnect.
func (m *Model) catchUpRooms(s *session) []tea.Cmd {
	return m.catchUpRoomsVia(s, true)
}

// catchUpRoomsVia is catchUpRooms; viaLog false forces per-room recovery,
// e.g. after a one-stream recovery was abandoned.
func (m *Model) catchUpRoomsVia(s *session, viaLog bool) []tea.Cmd {
	if !s.hasCap("history") {
		return nil
	}
	var cmds []tea.Cmd
	var forward []string
	for _, id := range s.store.Rooms() {
		r := s.store.Room(id)
		if r == nil || !r.NeedsCatchUp() || r.Loading {
			continue
		}
		if r.BoundPastCheckpoint() {
			s.store.ResetRoomContent(id)
			m.chat.dirty = true
			cmds = append(cmds, m.loadRoomHistory(s, id))
			continue
		}
		forward = append(forward, id)
	}
	// Several rooms recover in one room-less stream when the server
	// supports it (R4); otherwise each room pages forward on its own.
	if viaLog {
		if cmd := m.startLogCatchUp(s, forward); cmd != nil {
			return append(cmds, cmd)
		}
	}
	for _, id := range forward {
		r := s.store.Room(id)
		r.Loading = true
		cmds = append(cmds, m.historyForwardCmd(s, id, protocol.NextLogID(r.Checkpoint), r.LatestLogID))
	}
	return cmds
}

// --- room-less history -----------------------------------------------------

// logHistoryCmd pages the combined log backward from before (newest page when
// before is empty). It joins the session's history queue too: the room-less
// query is a history request like any other.
func (m *Model) logHistoryCmd(s *session, before string) tea.Cmd {
	return m.queueHistory(s, "log_history", "", protocol.HistoryParams{
		Before: before,
		Limit:  historyPageSize,
	})
}

// logForwardCmd pages the combined log forward from after (inclusive) up to
// before (unbounded when empty), for recovery (§4.3.5).
func (m *Model) logForwardCmd(s *session, after, before string) tea.Cmd {
	return m.queueHistory(s, "log_forward", "", protocol.HistoryParams{
		After:  after,
		Before: before,
		Limit:  historyPageSize,
	})
}

// usesLog reports whether s pages history through the room-less query: a
// server with history but no rooms that answered it.
func (s *session) usesLog() bool {
	return !s.hasCap("rooms") && s.hasCap("history") && !s.logUnsupported
}

// syncLog loads or recovers the combined log after sign-in on a server
// without rooms (R2): forward from the checkpoint when one is kept across a
// reconnect, otherwise the newest page.
func (m *Model) syncLog(s *session) tea.Cmd {
	l := &s.store.Log
	if !s.usesLog() || l.Loading {
		return nil
	}
	l.Loading = true
	if l.Loaded && l.Checkpoint != "" {
		s.logTo = ""
		return m.logForwardCmd(s, protocol.NextLogID(l.Checkpoint), "")
	}
	return m.logHistoryCmd(s, "")
}

// loadOlderLog pages the combined log backward from its first loaded record.
func (m *Model) loadOlderLog(s *session) tea.Cmd {
	l := &s.store.Log
	if l.Loading || !l.MoreBack {
		return nil
	}
	before := protocol.PrevLogID(l.FirstLogID)
	if before == "" {
		return nil
	}
	l.Loading = true
	if room := s.store.Room(m.chat.roomID); room != nil {
		// Keeps the scroll anchor alive until the page lands.
		room.Loading = true
	}
	m.captureScrollAnchor()
	return m.logHistoryCmd(s, before)
}

// startLogCatchUp recovers several rooms with one forward stream (R4): from
// the smallest checkpoint to the newest latest_log_id. It returns nil when
// per-room recovery should run instead.
func (m *Model) startLogCatchUp(s *session, ids []string) tea.Cmd {
	if len(ids) < 2 || s.logUnsupported || s.logCatchUp != nil || s.store.Log.Loading {
		return nil
	}
	minCP, maxH := "", ""
	for _, id := range ids {
		r := s.store.Room(id)
		if minCP == "" || after(minCP, r.Checkpoint) {
			minCP = r.Checkpoint
		}
		if after(r.LatestLogID, maxH) {
			maxH = r.LatestLogID
		}
	}
	s.logCatchUp = make(map[string]bool, len(ids))
	for _, id := range ids {
		s.logCatchUp[id] = true
		s.store.Room(id).Loading = true
	}
	s.store.Log.Checkpoint = minCP
	s.store.Log.Loading = true
	s.logTo = maxH
	return m.logForwardCmd(s, protocol.NextLogID(minCP), maxH)
}

// abortLogCatchUp drops a one-stream recovery and returns the rooms it was
// recovering to per-room recovery.
func (m *Model) abortLogCatchUp(s *session) []tea.Cmd {
	for id := range s.logCatchUp {
		if r := s.store.Room(id); r != nil {
			r.Loading = false
		}
	}
	s.logCatchUp = nil
	s.logTo = ""
	s.store.Log.Loading = false
	return m.catchUpRoomsVia(s, false)
}

// onLogHistory applies a backward room-less page (newest or older).
func (m *Model) onLogHistory(s *session, raw json.RawMessage) tea.Cmd {
	var res protocol.HistoryResult
	if err := json.Unmarshal(raw, &res); err != nil {
		s.store.Log.Loading = false
		m.sessStatus(s, "bad history result: "+err.Error(), true)
		return nil
	}
	active := m.isCur(s)
	initial := !s.store.Log.Loaded
	touched := s.store.ApplyLogHistory(&res, false)
	if initial {
		// The newest page leaves the whole log caught up to H (§4.3.5).
		s.store.Log.Checkpoint = res.LatestLogID
	}
	if room := s.store.Room(m.chat.roomID); room != nil {
		room.Loading = false
	}
	if active && m.chat.roomID == "" {
		// Open the room of the newest message: on a server without rooms,
		// the default room (§3.4).
		open := ""
		if n := len(res.Messages); n > 0 {
			open = res.Messages[n-1].RoomID
		} else if len(touched) > 0 {
			open = touched[0]
		}
		if open != "" {
			m.chat.roomID = open
			m.chat.stickBottom = true
		}
	}
	m.refreshRoomItems()
	if active {
		m.chat.dirty = true
	}
	return nil
}

// onLogForward applies a forward room-less page of a recovery stream: either
// a server without rooms resuming from its log checkpoint, or a one-stream
// catch-up of several rooms (R4).
func (m *Model) onLogForward(s *session, raw json.RawMessage) tea.Cmd {
	var res protocol.HistoryResult
	if err := json.Unmarshal(raw, &res); err != nil {
		m.sessStatus(s, "bad history result: "+err.Error(), true)
		if s.logCatchUp != nil {
			return tea.Batch(m.abortLogCatchUp(s)...)
		}
		s.store.Log.Loading = false
		return nil
	}
	// Retention can advance between requests: check the server log's bound
	// before applying the page (§4.3.3).
	probe := s.store.Log
	if res.LatestLogID != "" {
		probe.LatestLogID = res.LatestLogID
	}
	probe.HistoryLogID = res.HistoryLogID
	if probe.BoundPastCheckpoint() {
		if s.logCatchUp != nil {
			// Per-room recovery checks each room's own bound.
			return tea.Batch(m.abortLogCatchUp(s)...)
		}
		// The log moved past what we need: clear every room it covers and
		// start again from the newest page (§4.3.5).
		s.store.ResetLogContent()
		m.chat.dirty = true
		s.store.Log.Loading = true
		return m.logHistoryCmd(s, "")
	}

	s.store.ApplyLogHistory(&res, true)
	// A room's checkpoint advances to its H once the stream passes it.
	for id := range s.logCatchUp {
		r := s.store.Room(id)
		if r == nil {
			delete(s.logCatchUp, id)
			continue
		}
		if res.LastLogID != "" && !after(r.LatestLogID, res.LastLogID) {
			r.Checkpoint = r.LatestLogID
			r.Loading = false
			delete(s.logCatchUp, id)
		}
	}
	if res.More && res.LastLogID != "" {
		s.store.Log.Loading = true
		return m.logForwardCmd(s, protocol.NextLogID(res.LastLogID), s.logTo)
	}

	// Caught up.
	if s.logTo != "" {
		s.store.Log.Checkpoint = s.logTo
	} else if res.LatestLogID != "" {
		s.store.Log.Checkpoint = res.LatestLogID
	}
	for id := range s.logCatchUp {
		if r := s.store.Room(id); r != nil {
			r.Checkpoint = r.LatestLogID
			r.Loading = false
		}
	}
	s.logCatchUp = nil
	s.logTo = ""
	m.refreshRoomItems()
	if m.isCur(s) {
		m.chat.dirty = true
	}
	return nil
}

// onLogError is superseded: history error replies are handled by
// onHistoryError in requests.go.
