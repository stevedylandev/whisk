// Package state applies Apron frames to a client-side model of rooms, messages,
// and users, following the replay rules of §2.3 and §3.3.1.
//
// It is pure and unit-testable: it has no I/O or concurrency.
package state

import (
	"bytes"
	"encoding/json"
	"sort"
	"time"

	"github.com/stevedylandev/whisk/internal/protocol"
)

// Store is the client's accumulated protocol state.
type Store struct {
	Server *protocol.Server
	You    *protocol.User
	Token  string

	rooms     map[string]*Room
	roomOrder []string

	notJoined      map[string]*Room
	notJoinedOrder []string

	users     map[string]*protocol.User
	nameUsers map[string]map[string]bool // display name -> set of user_ids

	// messages indexes the winning snapshot of every loaded message by
	// message_id, across rooms. It is the source of truth for replay; the
	// per-room Messages/Order views are kept in sync for the UI.
	messages map[string]*protocol.Message

	// reactions maps message_id -> user_id -> the user's winning reaction set
	// (its complete emoji set at the greatest log_id seen, §4.7).
	reactions map[string]map[string]reactionSet

	// memberships maps room_id -> user_id -> the winning membership record
	// (§4.2.2), kept apart from the room's base member list.
	memberships map[string]map[string]*membershipRec

	// typing maps room_id -> user_id -> typing expiry (transient).
	typing map[string]map[string]time.Time

	// notices are transient messages (§3.5.3): shown, never installed as
	// snapshots. They are dropped at each sign-in.
	notices []Notice

	// mutes tracks the user's own mutes (§4.5.3); "" is the global mute.
	mutes map[string]*Mute

	// Log is the paging cursor for room-less history (§4.3), kept apart
	// from per-room paging: its bounds are the server log's, not a room's.
	Log LogCursor
}

// LogCursor is the session-level position in the combined log of every
// delivered room (§4.3, history without room_id).
type LogCursor struct {
	FirstLogID string // earliest loaded log_id; "" when nothing loaded yet
	MoreBack   bool   // more older records available
	Loaded     bool   // an initial room-less page has been applied
	Loading    bool   // a room-less request is in flight

	// Checkpoint is the latest_log_id through which the whole log has been
	// applied (§4.3.5).
	Checkpoint string

	LatestLogID  string
	HistoryLogID *string
}

// BoundPastCheckpoint reports whether the server log's effective lower bound
// (§4.3.3) is past the next position the client needs (checkpoint+1), so the
// client must clear every room the log covers and start again (§4.3.5).
func (l *LogCursor) BoundPastCheckpoint() bool {
	if l.Checkpoint == "" {
		return false
	}
	if l.HistoryLogID != nil && *l.HistoryLogID != "" {
		return after(*l.HistoryLogID, l.Checkpoint)
	}
	// history_log_id null: nothing before latest_log_id is retrievable, so
	// any change since the checkpoint is gone.
	return after(l.LatestLogID, l.Checkpoint)
}

// Notice is a transient message without a message_id (§3.5.3, Appendix A.1):
// a private system notice such as a command reply, an error, a reminder, or a
// pre-auth welcome. Clients show notices, but never install them as snapshots.
type Notice struct {
	RoomID string // may be empty (§3.5.1)
	From   *protocol.User
	Body   *protocol.Body
	At     time.Time
}

// maxNotices caps the transient-notice buffer.
const maxNotices = 200

// Mute is one of the user's mutes in effect (§4.5.3).
type Mute struct {
	RoomID  string    // "" is the global mute
	Seconds int       // >0 when a timed mute was reported, in seconds
	Since   time.Time // when the mute was learned
}

// Room is one room's state, plus per-room history paging.
type Room struct {
	Record   protocol.Room
	Messages map[string]*protocol.Message
	// Order holds message_ids sorted by creation order (ascending message_id).
	Order   []string
	Members map[string]*protocol.User

	// History paging.
	FirstLogID string // earliest loaded log_id; "" when nothing loaded yet
	MoreBack   bool   // more older records available
	Loaded     bool   // an initial history page has been applied
	Loading    bool   // a history request is in flight

	// Checkpoint is the recovery position (§4.3.5): the latest_log_id
	// through which this room's log has been applied. Empty until an
	// initial page (or a forward catch-up) completes.
	Checkpoint string

	// Delivery fields kept separately from Record, since logged records
	// (e.g. from history) must not overwrite them.
	LatestLogID  string
	HistoryLogID *string

	// Member-list state (§4.2.1): baseMembers is the complete member list the
	// last room_list/room_update joined frame carried, as of memberPos (the
	// frame's latest_log_id; "" when it carried none). membersKnown reports
	// whether anything is known about the room's members. Memberships above
	// memberPos overlay baseMembers; Members is the computed projection.
	baseMembers  map[string]*protocol.User
	memberPos    string
	membersKnown bool
}

// reactionSet is one user's complete emoji set on a message, with the log_id
// of the record that set it (§4.7).
type reactionSet struct {
	logID  string
	emojis []string
}

// membershipRec is one user's membership state in a room, with the log_id of
// the record that set it (§4.2.2).
type membershipRec struct {
	logID  string
	joined bool
	user   *protocol.User
}

// New returns an empty Store.
func New() *Store {
	return &Store{
		rooms:       make(map[string]*Room),
		notJoined:   make(map[string]*Room),
		users:       make(map[string]*protocol.User),
		nameUsers:   make(map[string]map[string]bool),
		messages:    make(map[string]*protocol.Message),
		reactions:   make(map[string]map[string]reactionSet),
		memberships: make(map[string]map[string]*membershipRec),
		typing:      make(map[string]map[string]time.Time),
		mutes:       make(map[string]*Mute),
	}
}

// Rooms returns the joined room ids in a stable order.
func (s *Store) Rooms() []string { return s.roomOrder }

// NotJoined returns visible rooms the user has not joined.
func (s *Store) NotJoined() []string { return s.notJoinedOrder }

// NotJoinedRoom returns the room state for a not-joined room id.
func (s *Store) NotJoinedRoom(id string) *Room { return s.notJoined[id] }

// Room returns the room state for id, or nil.
func (s *Store) Room(id string) *Room { return s.rooms[id] }

// User returns the kept user object for id, or nil.
func (s *Store) User(id string) *protocol.User { return s.users[id] }

// ApplyServer replaces the server frame state.
func (s *Store) ApplyServer(srv *protocol.Server) { s.Server = srv }

// ApplyNotification applies an incoming notification frame by method.
// Notifications the store does not track (ping, pong, …) are ignored, as are
// params that fail to decode.
func (s *Store) ApplyNotification(method string, params json.RawMessage) {
	switch method {
	case "server":
		decode(params, s.ApplyServer)
	case "user":
		decode(params, func(p *protocol.UserParams) { s.applyUser(p, params) })
	case "message":
		decode(params, s.ApplyMessage)
	case "room_update":
		decode(params, s.ApplyRoomUpdate)
	case "reactions":
		decode(params, s.ApplyReactions)
	case "activity":
		decode(params, s.ApplyActivity)
	case "status":
		decode(params, s.ApplyStatus)
	}
}

// decode unmarshals params into a T and passes it to apply, skipping params
// that do not decode.
func decode[T any](params json.RawMessage, apply func(*T)) {
	var v T
	if json.Unmarshal(params, &v) == nil {
		apply(&v)
	}
}

// ApplyAuth installs the authenticated identity and token. `you` is complete.
// A sign-in drops the statuses and mutes kept from earlier connections
// (§4.5.4); the server re-sends the ones in effect.
func (s *Store) ApplyAuth(result *protocol.AuthResult) {
	s.ResetPresence()
	if result.Token != "" {
		s.Token = result.Token
	}
	if result.You != nil {
		s.You = result.You
		s.setUserComplete(result.You)
	}
}

// ResetPresence drops the statuses and mutes kept from earlier connections
// (§4.5.4), plus transient notices and typing, which belong to one connection.
func (s *Store) ResetPresence() {
	s.mutes = make(map[string]*Mute)
	s.notices = nil
	s.typing = make(map[string]map[string]time.Time)
	for _, u := range s.users {
		u.Status = ""
		u.Seen = ""
	}
}

// ResetTransient drops per-connection transient state (typing, in-flight
// history loads) without touching records; used when a connection is lost.
func (s *Store) ResetTransient() {
	s.typing = make(map[string]map[string]time.Time)
	for _, r := range s.rooms {
		r.Loading = false
	}
	for _, r := range s.notJoined {
		r.Loading = false
	}
	s.Log.Loading = false
}

// ApplyMe installs a me result. `you` is complete.
func (s *Store) ApplyMe(result *protocol.MeResult) {
	s.You = &result.You
	s.setUserComplete(&result.You)
}

func (s *Store) applyUser(p *protocol.UserParams, raw json.RawMessage) {
	if p.You != nil {
		s.mergeCurrentUser(p.You, fieldPresence(raw, "you"))
		s.You = s.users[p.You.UserID]
	}
	if p.New != nil {
		s.mergeCurrentUser(p.New, fieldPresence(raw, "new"))
	}
	if p.Old != nil {
		// The same account changed user_id from old to new. Retire old.
		if old, ok := s.users[p.Old.UserID]; ok {
			s.removeNameUser(p.Old.UserID, old.Name)
			delete(s.users, p.Old.UserID)
		}
	}
}

// mergeCurrentUser merges a current user object into the kept object (§3.3.1).
// Each field that the object carries replaces the kept value — an empty value
// clears the field — and fields it does not carry stay the same.
func (s *Store) mergeCurrentUser(u *protocol.User, present map[string]bool) {
	if u == nil || u.UserID == "" {
		return
	}
	prev, ok := s.users[u.UserID]
	if !ok || prev == nil {
		cp := *u
		s.users[u.UserID] = &cp
		s.addNameUser(u.UserID, u.Name)
		return
	}
	if present == nil {
		// Without the raw object, only non-empty values count as carried.
		present = carriedFields(u)
	}
	merged := *prev
	if present["name"] {
		s.removeNameUser(u.UserID, prev.Name)
		merged.Name = u.Name
		s.addNameUser(u.UserID, u.Name)
	}
	if present["avatar"] {
		merged.Avatar = u.Avatar
	}
	if present["roles"] {
		merged.Roles = u.Roles
	}
	if present["status"] {
		merged.Status = u.Status
	}
	if present["seen"] {
		merged.Seen = u.Seen
	}
	if present["ext"] {
		merged.Ext = mergeExt(prev.Ext, u.Ext)
	}
	s.users[u.UserID] = &merged
}

// carriedFields lists the non-empty fields of u, for merges without raw
// presence data.
func carriedFields(u *protocol.User) map[string]bool {
	present := make(map[string]bool)
	if u.Name != "" {
		present["name"] = true
	}
	if u.Avatar != "" {
		present["avatar"] = true
	}
	if u.Roles != nil {
		present["roles"] = true
	}
	if u.Status != "" {
		present["status"] = true
	}
	if u.Seen != "" {
		present["seen"] = true
	}
	if len(u.Ext) > 0 {
		present["ext"] = true
	}
	return present
}

// fieldPresence reports which fields the named sub-object of raw carries, so
// that an empty value ("a cleared field as its empty value", §3.3.1) can be
// told apart from an absent one.
func fieldPresence(raw json.RawMessage, key string) map[string]bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	field, ok := obj[key]
	if !ok {
		return nil
	}
	var sub map[string]json.RawMessage
	if json.Unmarshal(field, &sub) != nil {
		return nil
	}
	present := make(map[string]bool, len(sub))
	for k := range sub {
		present[k] = true
	}
	return present
}

// mergeExt merges ext objects one level down (§4.12): each key the update
// carries replaces the kept value, an empty value clears that key, and keys it
// leaves out stay. An empty update object changes nothing.
func mergeExt(kept, update json.RawMessage) json.RawMessage {
	if len(update) == 0 {
		return kept
	}
	var upd map[string]json.RawMessage
	if json.Unmarshal(update, &upd) != nil || len(upd) == 0 {
		return kept
	}
	base := make(map[string]json.RawMessage)
	if len(kept) > 0 {
		_ = json.Unmarshal(kept, &base)
	}
	for k, v := range upd {
		switch string(bytes.TrimSpace(v)) {
		case `""`, `[]`, `{}`:
			delete(base, k)
		default:
			base[k] = v
		}
	}
	out, err := json.Marshal(base)
	if err != nil {
		return kept
	}
	return out
}

// ApplyMessage installs a message snapshot under the replay rule: keep the
// record with the greatest log_id, tombstone winning ties. A message without
// a message_id is a transient notice (§3.5.3): it is buffered for display,
// never installed as a snapshot. A reply_to object that carries a log_id is an
// embedded snapshot (§3.5.4): it installs in its own room like any other.
func (s *Store) ApplyMessage(m *protocol.Message) {
	if m == nil {
		return
	}
	if m.MessageID == "" {
		s.AddNotice(m)
		return
	}
	if m.RoomID == "" {
		return
	}

	// Install embedded reply_to snapshots first (§3.5.4). A bare reference
	// (no log_id) installs nothing.
	if m.ReplyTo != nil && m.ReplyTo.MessageID != "" && m.ReplyTo.LogID != "" {
		s.ApplyMessage(m.ReplyTo)
	}

	if prev, ok := s.messages[m.MessageID]; ok {
		if after(prev.LogID, m.LogID) {
			return // existing snapshot is newer
		}
		if prev.LogID == m.LogID && prev.Deleted && !m.Deleted {
			return // tombstone wins an equal-log_id tie
		}
	}

	// Re-home the message when its current room changed (§4.4): the winning
	// snapshot's room_id is where the message is now. Drop any copy from the
	// room it used to be in, whether the move is seen through the previous
	// snapshot or announced via prev_room_id.
	if prev, ok := s.messages[m.MessageID]; ok && prev.RoomID != "" && prev.RoomID != m.RoomID {
		s.removeMessageFromRoom(prev.RoomID, m.MessageID)
	} else if m.PrevRoomID != "" && m.PrevRoomID != m.RoomID {
		s.removeMessageFromRoom(m.PrevRoomID, m.MessageID)
	}

	s.messages[m.MessageID] = m

	r := s.ensureRoom(m.RoomID)
	r.Messages[m.MessageID] = m
	// Updating a known snapshot must not add a second ordering entry: a
	// duplicate id would render the message twice and stall selection.
	r.Order = removeOrderID(r.Order, m.MessageID)
	r.Order = insertOrdered(r.Order, m.MessageID)

	// A new message from a user clears their typing indicator.
	if m.From != nil && m.From.UserID != "" {
		if users := s.typing[m.RoomID]; users != nil {
			delete(users, m.From.UserID)
		}
	}
}

// AddNotice buffers a transient notice (§3.5.3, A.1) for display. It is never
// installed as a snapshot.
func (s *Store) AddNotice(m *protocol.Message) {
	if m == nil || m.Body == nil {
		return
	}
	s.notices = append(s.notices, Notice{
		RoomID: m.RoomID,
		From:   m.From,
		Body:   m.Body,
		At:     time.Now(),
	})
	if len(s.notices) > maxNotices {
		s.notices = append([]Notice(nil), s.notices[len(s.notices)-maxNotices:]...)
	}
}

// Notices returns all buffered notices, oldest first.
func (s *Store) Notices() []Notice { return s.notices }

// RoomNotices returns the notices shown in roomID (A.1: room_id is where the
// notice is shown).
func (s *Store) RoomNotices(roomID string) []Notice {
	var out []Notice
	for _, n := range s.notices {
		if n.RoomID == roomID && n.RoomID != "" {
			out = append(out, n)
		}
	}
	return out
}

// LatestRoomlessNotice returns the most recent notice without a room_id, such
// as a pre-auth welcome (B.1), or nil.
func (s *Store) LatestRoomlessNotice() *Notice {
	for i := len(s.notices) - 1; i >= 0; i-- {
		if s.notices[i].RoomID == "" {
			return &s.notices[i]
		}
	}
	return nil
}

// IsMuted reports whether a mute silences roomID (§4.5.3): a global mute
// applies everywhere, a room mute in that room.
func (s *Store) IsMuted(roomID string) bool {
	if _, ok := s.mutes[""]; ok {
		return true
	}
	_, ok := s.mutes[roomID]
	return ok
}

// Mutes returns the mutes in effect, global mute first.
func (s *Store) Mutes() []*Mute {
	out := make([]*Mute, 0, len(s.mutes))
	if g, ok := s.mutes[""]; ok {
		out = append(out, g)
	}
	for id, m := range s.mutes {
		if id != "" {
			out = append(out, m)
		}
	}
	return out
}

// ApplyStatus applies a status notification (§4.5.3): the mutes of the user.
// Absent fields change nothing.
func (s *Store) ApplyStatus(p *protocol.StatusParams) {
	if p == nil || p.Mute == nil {
		return
	}
	// mute is true, false, or a positive number of seconds.
	var b bool
	if err := json.Unmarshal(p.Mute, &b); err == nil {
		if !b {
			delete(s.mutes, p.RoomID)
		} else {
			s.mutes[p.RoomID] = &Mute{RoomID: p.RoomID, Since: time.Now()}
		}
		return
	}
	var secs float64
	if err := json.Unmarshal(p.Mute, &secs); err != nil || secs <= 0 {
		return
	}
	s.mutes[p.RoomID] = &Mute{RoomID: p.RoomID, Seconds: int(secs), Since: time.Now()}
}

// ApplyReactions merges a reactions record: each entry is one user's complete
// set on a message, kept under the replay rule (§4.7). The greatest log_id
// wins per (message_id, user_id); an empty set clears; duplicate emoji in a
// set collapse. A record without log_id always replaces.
func (s *Store) ApplyReactions(p *protocol.ReactionsParams) {
	if p == nil || p.MessageID == "" {
		return
	}
	sets, ok := s.reactions[p.MessageID]
	if !ok {
		sets = make(map[string]reactionSet)
		s.reactions[p.MessageID] = sets
	}
	for i := range p.Reactions {
		e := p.Reactions[i]
		uid := e.From.UserID
		if uid == "" {
			continue
		}
		if cur, ok := sets[uid]; ok && p.LogID != "" && cur.logID != "" && !after(p.LogID, cur.logID) {
			continue // stale set for this user
		}
		sets[uid] = reactionSet{logID: p.LogID, emojis: dedupeEmojis(e.Emojis)}
	}
}

// dedupeEmojis returns the emojis with duplicates removed, keeping first-seen
// order (§4.7: duplicate emoji in a set collapse).
func dedupeEmojis(in []string) []string {
	if len(in) <= 1 {
		return append([]string(nil), in...)
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, e := range in {
		if seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

// ReactionTotal is one emoji and its count on a message.
type ReactionTotal struct {
	Emoji string
	Count int
}

// ReactionTotals returns the summed reaction counts for a message, sorted by
// emoji. Each user's set is counted once per distinct emoji.
func (s *Store) ReactionTotals(messageID string) []ReactionTotal {
	sets := s.reactions[messageID]
	if len(sets) == 0 {
		return nil
	}
	counts := make(map[string]int)
	for _, set := range sets {
		for _, e := range set.emojis {
			counts[e]++
		}
	}
	out := make([]ReactionTotal, 0, len(counts))
	for e, c := range counts {
		out = append(out, ReactionTotal{Emoji: e, Count: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Emoji < out[j].Emoji })
	return out
}

// MyReactions returns the current user's emoji set on a message.
func (s *Store) MyReactions(messageID, userID string) []string {
	return s.reactions[messageID][userID].emojis
}

// ApplyActivity tracks a typing indicator from an activity notification.
func (s *Store) ApplyActivity(p *protocol.ActivityParams) {
	if p == nil || p.RoomID == "" || p.From == nil || p.From.UserID == "" || p.Typing == nil {
		return
	}
	users, ok := s.typing[p.RoomID]
	if !ok {
		users = make(map[string]time.Time)
		s.typing[p.RoomID] = users
	}
	if *p.Typing == 0 {
		delete(users, p.From.UserID)
		return
	}
	users[p.From.UserID] = time.Now().Add(time.Duration(*p.Typing) * time.Second)
}

// TypingUsers returns the user_ids still typing in roomID, pruning expired
// entries. now is injected for testability.
func (s *Store) TypingUsers(roomID string, now time.Time) []string {
	users := s.typing[roomID]
	if len(users) == 0 {
		return nil
	}
	var out []string
	for id, exp := range users {
		if exp.After(now) {
			out = append(out, id)
		} else {
			delete(users, id)
		}
	}
	sort.Strings(out)
	return out
}

// MessageByID finds the winning message snapshot by its message_id.
func (s *Store) MessageByID(messageID string) *protocol.Message {
	return s.messages[messageID]
}

// ApplyRoomList installs a room_list result.
func (s *Store) ApplyRoomList(res *protocol.RoomListResult) {
	for i := range res.Users {
		s.setUserComplete(&res.Users[i])
	}
	for i := range res.Left {
		s.removeRoom(res.Left[i].RoomID)
	}
	for i := range res.Joined {
		s.applyJoinedRoom(&res.Joined[i])
	}
	for i := range res.NotJoined {
		s.applyNotJoinedRoom(&res.NotJoined[i])
	}
}

// ApplyRoomUpdate installs a room_update notification.
func (s *Store) ApplyRoomUpdate(p *protocol.RoomUpdateParams) {
	for i := range p.Users {
		s.setUserComplete(&p.Users[i])
	}
	for i := range p.Left {
		s.removeRoom(p.Left[i].RoomID)
	}
	for i := range p.Joined {
		s.applyJoinedRoom(&p.Joined[i])
	}
	for i := range p.Updated {
		s.applyUpdatedRoom(&p.Updated[i])
	}
	for i := range p.Memberships {
		s.applyMembership(&p.Memberships[i])
	}
}

// ApplyHistory installs a history result for roomID.
func (s *Store) ApplyHistory(roomID string, res *protocol.HistoryResult) {
	r := s.ensureRoom(roomID)
	r.Loaded = true
	r.Loading = false

	if res.LatestLogID != "" {
		r.LatestLogID = res.LatestLogID
	}
	if res.HistoryLogID != nil {
		r.HistoryLogID = res.HistoryLogID
	}
	if res.FirstLogID != "" {
		if r.FirstLogID == "" || !after(res.FirstLogID, r.FirstLogID) {
			r.FirstLogID = res.FirstLogID
		}
	}
	// `more` describes the direction we paged; backward pages set it true when
	// older records remain.
	r.MoreBack = res.More

	for i := range res.Rooms {
		if res.Rooms[i].RoomID == roomID {
			r.applyRecord(&res.Rooms[i])
		}
	}
	for i := range res.Messages {
		s.ApplyMessage(&res.Messages[i])
	}
	for i := range res.Memberships {
		s.applyMembership(&res.Memberships[i])
	}
	for i := range res.Reactions {
		s.ApplyReactions(&res.Reactions[i])
	}
}

// ApplyLogHistory installs a room-less history result (§4.3): the combined
// log of every delivered room. Each record is routed by its own room_id, and
// one page may carry records (including folded memberships, §4.3.4) for
// several rooms. The result's bounds describe the server log, so they update
// s.Log instead of any room. A backward page (forward false) also moves the
// log's paging cursor. It returns the ids of the rooms the page touched.
func (s *Store) ApplyLogHistory(res *protocol.HistoryResult, forward bool) []string {
	l := &s.Log
	l.Loading = false
	if res.LatestLogID != "" {
		l.LatestLogID = res.LatestLogID
	}
	if res.HistoryLogID != nil {
		l.HistoryLogID = res.HistoryLogID
	}
	if !forward {
		l.Loaded = true
		if res.FirstLogID != "" && (l.FirstLogID == "" || !after(res.FirstLogID, l.FirstLogID)) {
			l.FirstLogID = res.FirstLogID
		}
		l.MoreBack = res.More
	}

	touched := make(map[string]bool)
	var order []string
	touch := func(id string) {
		if id == "" || touched[id] {
			return
		}
		touched[id] = true
		order = append(order, id)
	}
	for i := range res.Rooms {
		rec := res.Rooms[i]
		if rec.RoomID == "" {
			continue
		}
		// Room records in a history result are logged records: their
		// delivery fields are ignored (§4.3.3).
		rec.LatestLogID = ""
		rec.HistoryLogID = nil
		s.ensureRoom(rec.RoomID).applyRecord(&rec)
		touch(rec.RoomID)
	}
	for i := range res.Messages {
		s.ApplyMessage(&res.Messages[i])
		touch(res.Messages[i].RoomID)
	}
	for i := range res.Memberships {
		s.applyMembership(&res.Memberships[i])
		touch(res.Memberships[i].RoomID)
	}
	for i := range res.Reactions {
		s.ApplyReactions(&res.Reactions[i])
		touch(res.Reactions[i].RoomID)
	}
	for _, id := range order {
		if r := s.rooms[id]; r != nil {
			r.Loaded = true
			r.Loading = false
		}
	}
	return order
}

// ResetLogContent clears every room the combined log covers, and the log
// cursor, so recovery can start again from the server log's bound (§4.3.5).
func (s *Store) ResetLogContent() {
	for _, id := range s.roomOrder {
		s.ResetRoomContent(id)
	}
	s.Log = LogCursor{LatestLogID: s.Log.LatestLogID, HistoryLogID: s.Log.HistoryLogID}
}

// DisplayName returns the name to show for a user, using the kept object first
// and the recorded object (e.g. a message from) as fallback. An empty or
// unknown name shows as user_id.
func (s *Store) DisplayName(userID string, recorded *protocol.User) string {
	if u := s.users[userID]; u != nil && u.Name != "" {
		return u.Name
	}
	if recorded != nil && recorded.Name != "" {
		return recorded.Name
	}
	if userID == "" {
		return "?"
	}
	return userID
}

// NameIsAmbiguous reports whether another user_id shares the same display name.
func (s *Store) NameIsAmbiguous(userID, name string) bool {
	if name == "" {
		return false
	}
	set, ok := s.nameUsers[name]
	if !ok {
		return false
	}
	for id := range set {
		if id != userID {
			return true
		}
	}
	return false
}

// removeMessageFromRoom drops a message snapshot from roomID, if present, and
// removes its id from the room's ordering. It is used when a move (§4.4)
// relocates a message to another room.
func (s *Store) removeMessageFromRoom(roomID, messageID string) {
	r := s.rooms[roomID]
	if r == nil {
		return
	}
	if _, ok := r.Messages[messageID]; !ok {
		return
	}
	delete(r.Messages, messageID)
	r.Order = removeOrderID(r.Order, messageID)
}

// newRoom returns an empty room state for id.
func newRoom(id string) *Room {
	return &Room{
		Record:   protocol.Room{RoomID: id},
		Messages: make(map[string]*protocol.Message),
		Members:  make(map[string]*protocol.User),
	}
}

// ensureRoom returns the joined room id, creating it when unknown.
func (s *Store) ensureRoom(id string) *Room {
	if r, ok := s.rooms[id]; ok {
		return r
	}
	r := newRoom(id)
	s.rooms[id] = r
	s.roomOrder = append(s.roomOrder, id)
	return r
}

func (s *Store) applyJoinedRoom(rec *protocol.Room) {
	// joined and not_joined are disjoint (§4.2.1): a room that is now joined
	// must leave the not-joined listing, or the sidebar shows it twice.
	if _, ok := s.notJoined[rec.RoomID]; ok {
		delete(s.notJoined, rec.RoomID)
		s.notJoinedOrder = removeOrderID(s.notJoinedOrder, rec.RoomID)
	}
	r := s.ensureRoom(rec.RoomID)
	r.applyRecord(rec)
	if rec.Members != nil {
		r.replaceMembers(rec.Members, rec.LatestLogID)
		s.refreshMembers(r)
	}
}

func (s *Store) applyUpdatedRoom(rec *protocol.Room) {
	r := s.ensureRoom(rec.RoomID)
	r.applyRecord(rec)
	if rec.Members != nil {
		r.replaceMembers(rec.Members, rec.LatestLogID)
		s.refreshMembers(r)
	}
}

func (s *Store) applyNotJoinedRoom(rec *protocol.Room) {
	// The mirror case: the user left the room, so drop the joined copy
	// instead of keeping both entries in the sidebar.
	if _, ok := s.rooms[rec.RoomID]; ok {
		s.removeRoom(rec.RoomID)
	}
	r, ok := s.notJoined[rec.RoomID]
	if !ok {
		r = newRoom(rec.RoomID)
		s.notJoined[rec.RoomID] = r
		s.notJoinedOrder = append(s.notJoinedOrder, rec.RoomID)
	}
	r.applyRecord(rec)
	if rec.Members != nil {
		r.replaceMembers(rec.Members, rec.LatestLogID)
		s.refreshMembers(r)
	}
}

// removeRoom drops a room from the joined or not-joined listing.
func (s *Store) removeRoom(id string) {
	if _, ok := s.rooms[id]; ok {
		delete(s.rooms, id)
		s.roomOrder = removeOrderID(s.roomOrder, id)
		return
	}
	if _, ok := s.notJoined[id]; ok {
		delete(s.notJoined, id)
		s.notJoinedOrder = removeOrderID(s.notJoinedOrder, id)
	}
}

func (s *Store) applyMembership(m *protocol.Membership) {
	if m == nil || m.RoomID == "" {
		return
	}
	recs, ok := s.memberships[m.RoomID]
	if !ok {
		recs = make(map[string]*membershipRec)
		s.memberships[m.RoomID] = recs
	}
	for i := range m.Members {
		e := m.Members[i]
		uid := e.User.UserID
		if uid == "" {
			continue
		}
		if cur, ok := recs[uid]; ok && m.LogID != "" && cur.logID != "" && !after(m.LogID, cur.logID) {
			continue // stale membership for this user
		}
		u := e.User
		recs[uid] = &membershipRec{logID: m.LogID, joined: e.Joined, user: &u}
	}
	r := s.ensureRoom(m.RoomID)
	s.refreshMembers(r)
}

// computeMembers projects a room's member list: the complete base list as of
// memberPos, overlaid with memberships above that position (§4.2.1).
func (s *Store) computeMembers(r *Room) map[string]*protocol.User {
	out := make(map[string]*protocol.User, len(r.baseMembers))
	for id, u := range r.baseMembers {
		out[id] = u
	}
	for id, rec := range s.memberships[r.Record.RoomID] {
		if r.memberPos != "" && !after(rec.logID, r.memberPos) {
			continue // already in the base list
		}
		if rec.joined {
			out[id] = rec.user
		} else {
			delete(out, id)
		}
	}
	return out
}

// refreshMembers recomputes the room's projected member list (Room.Members)
// from its base list and winning memberships.
func (s *Store) refreshMembers(r *Room) {
	r.Members = s.computeMembers(r)
}

func (r *Room) applyRecord(rec *protocol.Room) {
	if rec == nil || rec.RoomID == "" {
		return
	}
	// Delivery fields are not part of the logged record; a room record frame
	// updates them regardless of the replay gate.
	if rec.LatestLogID != "" {
		r.LatestLogID = rec.LatestLogID
	}
	if rec.HistoryLogID != nil {
		r.HistoryLogID = rec.HistoryLogID
	}
	// Replay (§2.3): keep the record with the greatest log_id. A record
	// without log_id (a server without capability history) always replaces.
	if rec.LogID != "" && r.Record.LogID != "" && !after(rec.LogID, r.Record.LogID) {
		return
	}
	r.Record.RoomID = rec.RoomID
	r.Record.LogID = rec.LogID
	r.Record.PrevLogID = rec.PrevLogID
	r.Record.ParentRoomID = rec.ParentRoomID
	r.Record.Private = rec.Private
	r.Record.Title = rec.Title
	r.Record.Description = rec.Description
	r.Record.Ext = rec.Ext
}

func (r *Room) replaceMembers(members []protocol.User, pos string) {
	nm := make(map[string]*protocol.User, len(members))
	for i := range members {
		u := members[i]
		nm[u.UserID] = &u
	}
	r.baseMembers = nm
	r.memberPos = pos
	r.membersKnown = true
}

// Title returns the display title, falling back to room_id.
func (r *Room) Title() string {
	if r == nil {
		return ""
	}
	if r.Record.Title != "" {
		return r.Record.Title
	}
	return r.Record.RoomID
}

// NeedsCatchUp reports whether the room's log advanced past its checkpoint
// (§4.3.5), e.g. after a reconnect: the room needs forward recovery.
func (r *Room) NeedsCatchUp() bool {
	return r.Loaded && r.Checkpoint != "" && r.LatestLogID != "" &&
		after(r.LatestLogID, r.Checkpoint)
}

// BoundPastCheckpoint reports whether the room's effective lower bound
// (history_log_id, or latest_log_id+1 when null, §4.3.3) is past the next
// position the client needs (checkpoint+1). The client must then clear the
// room's state and start again from that bound (§4.3.5).
func (r *Room) BoundPastCheckpoint() bool {
	if !r.NeedsCatchUp() {
		return false
	}
	if r.HistoryLogID != nil && *r.HistoryLogID != "" {
		return after(*r.HistoryLogID, r.Checkpoint)
	}
	// history_log_id null: nothing before latest_log_id is retrievable.
	return true
}

// ResetContent drops the room's loaded content so recovery can start again
// from the log's effective lower bound (§4.3.5).
func (r *Room) ResetContent() {
	r.Messages = make(map[string]*protocol.Message)
	r.Order = nil
	r.FirstLogID = ""
	r.MoreBack = false
	r.Loaded = false
	r.Loading = false
	r.Checkpoint = ""
}

// MinCheckpoint returns the smallest recovery checkpoint across joined rooms
// (§4.3.5), for room_list's latest_log_id filter (§4.2.1). ok is false when
// no room has been loaded yet, or some loaded room has no checkpoint, either
// of which asks for a full listing.
func (s *Store) MinCheckpoint() (string, bool) {
	minCP := ""
	loaded := false
	for _, r := range s.rooms {
		if !r.Loaded {
			continue
		}
		loaded = true
		if r.Checkpoint == "" {
			return "", false
		}
		if minCP == "" || after(minCP, r.Checkpoint) {
			minCP = r.Checkpoint
		}
	}
	return minCP, loaded
}

// ResetRoomContent drops a room's loaded content, including the reactions of
// its messages (§4.3.5), so recovery can start again from the log's bound.
func (s *Store) ResetRoomContent(roomID string) {
	r := s.rooms[roomID]
	if r == nil {
		return
	}
	for _, mid := range r.Order {
		delete(s.reactions, mid)
		delete(s.messages, mid)
	}
	r.ResetContent()
}

func (s *Store) setUserComplete(u *protocol.User) {
	if u == nil || u.UserID == "" {
		return
	}
	if prev := s.users[u.UserID]; prev != nil {
		s.removeNameUser(u.UserID, prev.Name)
	}
	cp := *u
	s.users[u.UserID] = &cp
	s.addNameUser(u.UserID, u.Name)
}

func (s *Store) removeNameUser(userID, name string) {
	if name == "" {
		return
	}
	if set, ok := s.nameUsers[name]; ok {
		delete(set, userID)
		if len(set) == 0 {
			delete(s.nameUsers, name)
		}
	}
}

func (s *Store) addNameUser(userID, name string) {
	if name == "" {
		return
	}
	set, ok := s.nameUsers[name]
	if !ok {
		set = make(map[string]bool)
		s.nameUsers[name] = set
	}
	set[userID] = true
}

// insertOrdered inserts id into the sorted (ascending numeric) slice, keeping
// it unique. Message ids are decimal epoch milliseconds, compared as numbers.
func insertOrdered(order []string, id string) []string {
	pos := sort.Search(len(order), func(i int) bool {
		return !after(id, order[i])
	})
	if pos < len(order) && order[pos] == id {
		return order
	}
	order = append(order, "")
	copy(order[pos+1:], order[pos:])
	order[pos] = id
	return order
}

// removeOrderID drops every ordering entry for id.
func removeOrderID(order []string, id string) []string {
	out := order[:0]
	for _, v := range order {
		if v != id {
			out = append(out, v)
		}
	}
	return out
}

// after reports whether log id a is greater than b.
func after(a, b string) bool { return protocol.LogIDAfter(a, b) }
