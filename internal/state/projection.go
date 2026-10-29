package state

import (
	"encoding/json"
	"sort"

	"github.com/stevedylandev/whisk/internal/protocol"
)

// Projection returns the store's logical projection as JSON: {"rooms": [...]},
// matching the shared wire fixtures' expected state (§2, "Records, replay, and
// the logical projection"). It is used to check conformance against the
// official replay fixtures.
//
// Rooms appear sorted by room_id, messages by numeric message_id, members by
// user_id, and reaction entries by emoji (all in Unicode code point order,
// equivalently UTF-8 byte order for valid UTF-8).
func (s *Store) Projection() json.RawMessage {
	ids := make(map[string]*Room, len(s.rooms)+len(s.notJoined))
	for id, r := range s.rooms {
		ids[id] = r
	}
	for id, r := range s.notJoined {
		if _, ok := ids[id]; !ok {
			ids[id] = r
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)

	rooms := make([]any, 0, len(ordered))
	for _, id := range ordered {
		if p := s.projectRoom(ids[id]); p != nil {
			rooms = append(rooms, p)
		}
	}
	out, err := json.Marshal(map[string]any{"rooms": rooms})
	if err != nil {
		return json.RawMessage(`{"rooms":[]}`)
	}
	return out
}

// projectRoom builds one room's projection, or nil when the room has no stored
// record, no messages, and no member state.
func (s *Store) projectRoom(r *Room) map[string]any {
	hasRecord := r.Record.LogID != "" || r.Record.Title != "" ||
		r.Record.ParentRoomID != "" || r.Record.Description != "" ||
		len(r.Record.Ext) > 0
	membersKnown := r.membersKnown || len(s.memberships[r.Record.RoomID]) > 0
	if !hasRecord && len(r.Messages) == 0 && !membersKnown {
		return nil
	}

	p := map[string]any{"room_id": r.Record.RoomID}
	if r.Record.LogID != "" {
		p["log_id"] = r.Record.LogID
	}
	if r.Record.ParentRoomID != "" {
		p["parent_room_id"] = r.Record.ParentRoomID
	}
	if r.Record.Title != "" {
		p["title"] = r.Record.Title
	}
	if r.Record.Description != "" {
		p["description"] = r.Record.Description
	}
	if len(r.Record.Ext) > 0 {
		p["ext"] = r.Record.Ext
	}
	if membersKnown {
		p["members"] = s.projectMembers(r)
	}

	ids := make([]string, 0, len(r.Messages))
	for id := range r.Messages {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return after(ids[j], ids[i]) })
	msgs := make([]any, 0, len(ids))
	for _, id := range ids {
		msgs = append(msgs, s.projectMessage(r.Messages[id]))
	}
	p["messages"] = msgs
	return p
}

// projectMembers projects a room's member list, sorted by user_id.
func (s *Store) projectMembers(r *Room) []any {
	members := s.computeMembers(r)
	ids := make([]string, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		raw, err := json.Marshal(members[id])
		if err != nil {
			continue
		}
		out = append(out, json.RawMessage(raw))
	}
	return out
}

// projectMessage projects one message snapshot: the known fields copied
// exactly, reply_to always bare, and reactions folded in (hidden for
// tombstones).
func (s *Store) projectMessage(m *protocol.Message) map[string]any {
	if m == nil {
		return nil
	}
	p := map[string]any{
		"message_id": m.MessageID,
		"log_id":     m.LogID,
		"room_id":    m.RoomID,
	}
	if m.FromRaw != nil {
		p["from"] = m.FromRaw
	} else if m.From != nil {
		if raw, err := json.Marshal(m.From); err == nil {
			p["from"] = json.RawMessage(raw)
		}
	}
	if m.BodyRaw != nil {
		p["body"] = m.BodyRaw
	} else if m.Body != nil {
		if raw, err := json.Marshal(m.Body); err == nil {
			p["body"] = json.RawMessage(raw)
		}
	}
	if m.ReplyTo != nil && m.ReplyTo.MessageID != "" {
		p["reply_to"] = map[string]any{"message_id": m.ReplyTo.MessageID}
	}
	if m.Deleted {
		p["deleted"] = true
	}
	if len(m.Ext) > 0 {
		p["ext"] = m.Ext
	}
	if !m.Deleted {
		if rx := s.projectReactions(m.MessageID); rx != nil {
			p["reactions"] = rx
		}
	}
	return p
}

// projectReactions folds a message's reaction sets into
// [{"emoji", "user_ids": [...]}] sorted by emoji, or nil when the message has
// no non-empty sets.
func (s *Store) projectReactions(messageID string) []any {
	sets := s.reactions[messageID]
	if len(sets) == 0 {
		return nil
	}
	emojiUsers := make(map[string][]string)
	for uid, set := range sets {
		for _, e := range set.emojis {
			emojiUsers[e] = append(emojiUsers[e], uid)
		}
	}
	if len(emojiUsers) == 0 {
		return nil
	}
	emojis := make([]string, 0, len(emojiUsers))
	for e := range emojiUsers {
		emojis = append(emojis, e)
	}
	sort.Strings(emojis)
	out := make([]any, 0, len(emojis))
	for _, e := range emojis {
		uids := emojiUsers[e]
		sort.Strings(uids)
		out = append(out, map[string]any{"emoji": e, "user_ids": uids})
	}
	return out
}
