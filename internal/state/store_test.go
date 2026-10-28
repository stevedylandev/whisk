package state

import (
	"testing"
	"time"

	"github.com/stevedylandev/whisk/internal/protocol"
)

func msg(id, logID, room, text string) *protocol.Message {
	return &protocol.Message{
		MessageID: id,
		LogID:     logID,
		RoomID:    room,
		From:      &protocol.User{UserID: "u1", Name: "Ada"},
		Body:      &protocol.Body{Text: text},
	}
}

func TestApplyMessageReplayKeepsGreatestLogID(t *testing.T) {
	s := New()

	s.ApplyMessage(msg("m1", "100", "general", "one"))
	s.ApplyMessage(msg("m1", "200", "general", "one edited"))

	r := s.Room("general")
	if r == nil || len(r.Messages) != 1 {
		t.Fatalf("expected 1 message, got room=%v", r)
	}
	if got := r.Messages["m1"].Body.Text; got != "one edited" {
		t.Fatalf("expected edited snapshot, got %q", got)
	}
	if len(r.Order) != 1 {
		t.Fatalf("update duplicated the ordering entry: %v", r.Order)
	}

	// An older snapshot must not overwrite the newer one.
	s.ApplyMessage(msg("m1", "150", "general", "stale"))
	if got := r.Messages["m1"].Body.Text; got != "one edited" {
		t.Fatalf("stale snapshot overwrote newer one: %q", got)
	}
	if len(r.Order) != 1 {
		t.Fatalf("stale update duplicated the ordering entry: %v", r.Order)
	}
}

func TestInsertOrderedKeepsUnique(t *testing.T) {
	var order []string
	for _, id := range []string{"100", "200", "300", "200", "100", "250"} {
		order = insertOrdered(order, id)
	}
	want := []string{"100", "200", "250", "300"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestApplyMessageTombstoneWinsTie(t *testing.T) {
	s := New()
	s.ApplyMessage(msg("m1", "100", "general", "hello"))
	s.ApplyMessage(&protocol.Message{
		MessageID: "m1",
		LogID:     "100",
		RoomID:    "general",
		From:      &protocol.User{UserID: "u1", Name: "Ada"},
		Deleted:   true,
	})

	r := s.Room("general")
	if got := r.Messages["m1"].Deleted; !got {
		t.Fatalf("expected tombstone to win equal-log_id tie")
	}
}

func TestApplyMessageMoveRemovesFromSourceRoom(t *testing.T) {
	s := New()
	s.ApplyMessage(msg("m1", "100", "general", "hello"))

	// A snapshot that moved the message to a thread carries prev_room_id (§4.4).
	moved := msg("m1", "200", "thread-1", "hello")
	moved.PrevRoomID = "general"
	s.ApplyMessage(moved)

	if got := s.Room("general"); got == nil || len(got.Messages) != 0 || len(got.Order) != 0 {
		t.Fatalf("source room should be empty after a move: %+v", got)
	}
	r := s.Room("thread-1")
	if r == nil || len(r.Messages) != 1 || r.Messages["m1"].PrevRoomID != "general" {
		t.Fatalf("destination room should hold the moved message: %+v", r)
	}
	if s.MessageByID("m1") == nil {
		t.Fatal("moved message should still be findable")
	}
}

func TestApplyMessageTransientNoticeIgnored(t *testing.T) {
	s := New()
	s.ApplyMessage(&protocol.Message{
		RoomID: "general",
		From:   &protocol.User{UserID: "~private", Name: "Only you"},
		Body:   &protocol.Body{Text: "notice"},
	})
	r := s.Room("general")
	if r != nil && len(r.Messages) != 0 {
		t.Fatalf("transient notice should not be installed, got %d messages", len(r.Messages))
	}
}

func TestTransientNoticeBuffered(t *testing.T) {
	s := New()
	s.ApplyMessage(&protocol.Message{
		RoomID: "general",
		From:   &protocol.User{UserID: "~private", Name: "Only you"},
		Body:   &protocol.Body{Text: "room notice"},
	})
	s.ApplyMessage(&protocol.Message{
		From: &protocol.User{UserID: "~private", Name: "Only you"},
		Body: &protocol.Body{Text: "welcome", Format: "markdown"},
	})

	if got := len(s.Notices()); got != 2 {
		t.Fatalf("notices = %d, want 2", got)
	}
	if got := s.RoomNotices("general"); len(got) != 1 || got[0].Body.Text != "room notice" {
		t.Fatalf("RoomNotices(general) = %+v", got)
	}
	n := s.LatestRoomlessNotice()
	if n == nil || n.Body.Text != "welcome" {
		t.Fatalf("LatestRoomlessNotice = %+v", n)
	}
	// The notice must never become a snapshot, in any room.
	if s.Room("general") != nil && len(s.Room("general").Messages) != 0 {
		t.Fatalf("notice installed as a snapshot")
	}
}

func TestUserMergeClearsCarriedEmptyValues(t *testing.T) {
	s := New()
	s.ApplyAuth(&protocol.AuthResult{
		You: &protocol.User{UserID: "u1", Name: "Ada", Avatar: "https://x/a.png", Status: "dnd"},
	})

	// A current object that carries an empty avatar and status clears them
	// (§3.3.1); an absent field stays.
	s.ApplyNotification("user", []byte(`{"new":{"user_id":"u1","avatar":"","status":""}}`))
	u := s.User("u1")
	if u.Avatar != "" {
		t.Fatalf("avatar should be cleared, got %q", u.Avatar)
	}
	if u.Status != "" {
		t.Fatalf("status should be cleared (opt-out), got %q", u.Status)
	}
	if u.Name != "Ada" {
		t.Fatalf("name should stay, got %q", u.Name)
	}

	// A carried name "" clears the name too.
	s.ApplyNotification("user", []byte(`{"new":{"user_id":"u1","name":""}}`))
	if s.User("u1").Name != "" {
		t.Fatalf("name should be cleared, got %q", s.User("u1").Name)
	}

	// seen (§4.5.2) is carried and merged.
	s.ApplyNotification("user", []byte(`{"new":{"user_id":"u1","status":"offline","seen":"1724803600000"}}`))
	u = s.User("u1")
	if u.Status != "offline" || u.Seen != "1724803600000" {
		t.Fatalf("status/seen not merged: %+v", u)
	}
}

func TestApplyAuthDropsPresenceState(t *testing.T) {
	s := New()
	s.ApplyNotification("user", []byte(`{"new":{"user_id":"u2","status":"offline","seen":"123"}}`))
	s.ApplyStatus(&protocol.StatusParams{RoomID: "general", Mute: protocol.RawBool(true)})
	if !s.IsMuted("general") {
		t.Fatal("mute not applied")
	}

	// A sign-in drops statuses and mutes (§4.5.4).
	s.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Ada"}})
	if s.IsMuted("general") {
		t.Fatal("mute should be dropped at sign-in")
	}
	if u := s.User("u2"); u.Status != "" || u.Seen != "" {
		t.Fatalf("status/seen should be dropped at sign-in: %+v", u)
	}
}

func TestStatusMuteApplyAndClear(t *testing.T) {
	s := New()

	s.ApplyStatus(&protocol.StatusParams{Mute: protocol.RawBool(true)})
	if !s.IsMuted("general") || !s.IsMuted("other") {
		t.Fatal("global mute should silence every room")
	}

	// Clearing the global mute and adding a room mute scopes it to one room.
	s.ApplyStatus(&protocol.StatusParams{Mute: protocol.RawBool(false)})
	s.ApplyStatus(&protocol.StatusParams{RoomID: "general", Mute: protocol.RawNumber(300)})
	if !s.IsMuted("general") {
		t.Fatal("room mute should silence that room")
	}
	if s.IsMuted("other") {
		t.Fatal("room mute should not silence other rooms")
	}

	s.ApplyStatus(&protocol.StatusParams{RoomID: "general", Mute: protocol.RawBool(false)})
	if s.IsMuted("general") {
		t.Fatal("room mute should be cleared")
	}

	// Absent fields change nothing.
	s.ApplyStatus(&protocol.StatusParams{})
	if len(s.Mutes()) != 0 {
		t.Fatalf("empty status should change nothing, got %v", s.Mutes())
	}
}

func TestCheckpointAndCatchUp(t *testing.T) {
	s := New()
	s.ApplyHistory("general", &protocol.HistoryResult{
		Messages:     []protocol.Message{*msg("100", "100", "general", "a")},
		LatestLogID:  "100",
		HistoryLogID: strPtr("0"),
	})
	r := s.Room("general")
	r.Checkpoint = "100" // the model sets this on the initial page

	if r.NeedsCatchUp() {
		t.Fatal("room should be caught up")
	}
	r.LatestLogID = "150"
	if !r.NeedsCatchUp() {
		t.Fatal("room with advanced log should need catch-up")
	}
	if r.BoundPastCheckpoint() {
		t.Fatal("bound 0 is not past checkpoint 100")
	}

	// Retention advanced past the next position needed (checkpoint+1).
	r.HistoryLogID = strPtr("120")
	if !r.BoundPastCheckpoint() {
		t.Fatal("bound 120 should be past checkpoint 100")
	}

	// A null history_log_id means nothing before latest is retrievable.
	r.HistoryLogID = nil
	if !r.BoundPastCheckpoint() {
		t.Fatal("null history_log_id should be past the checkpoint")
	}
}

func TestMinCheckpoint(t *testing.T) {
	s := New()
	if _, ok := s.MinCheckpoint(); ok {
		t.Fatal("empty store should report no checkpoint")
	}
	s.ApplyHistory("general", &protocol.HistoryResult{LatestLogID: "200"})
	s.Room("general").Checkpoint = "200"
	s.ApplyHistory("ops", &protocol.HistoryResult{LatestLogID: "100"})
	s.Room("ops").Checkpoint = "100"

	cp, ok := s.MinCheckpoint()
	if !ok || cp != "100" {
		t.Fatalf("MinCheckpoint = %q, %v; want 100", cp, ok)
	}

	// A loaded room without a checkpoint asks for a full listing.
	s.ApplyHistory("new", &protocol.HistoryResult{LatestLogID: "300"})
	if _, ok := s.MinCheckpoint(); ok {
		t.Fatal("a room without a checkpoint should ask for a full listing")
	}
}

func TestResetRoomContent(t *testing.T) {
	s := New()
	s.ApplyMessage(msg("m1", "100", "general", "hello"))
	s.ApplyReactions(&protocol.ReactionsParams{
		MessageID: "m1",
		Reactions: []protocol.ReactionEntry{{From: protocol.User{UserID: "u1"}, Emojis: []string{"👍"}}},
	})
	if len(s.ReactionTotals("m1")) != 1 {
		t.Fatal("reaction should be present")
	}
	s.ResetRoomContent("general")
	r := s.Room("general")
	if len(r.Messages) != 0 || len(r.Order) != 0 || r.Loaded {
		t.Fatalf("room content not reset: %+v", r)
	}
	if len(s.ReactionTotals("m1")) != 0 {
		t.Fatal("reactions should be dropped with the room's content")
	}
}

func strPtr(s string) *string { return &s }

func TestOrderSortedByMessageID(t *testing.T) {
	s := New()
	s.ApplyMessage(msg("300", "300", "general", "c"))
	s.ApplyMessage(msg("100", "100", "general", "a"))
	s.ApplyMessage(msg("200", "200", "general", "b"))

	r := s.Room("general")
	want := []string{"100", "200", "300"}
	if len(r.Order) != len(want) {
		t.Fatalf("order = %v, want %v", r.Order, want)
	}
	for i := range want {
		if r.Order[i] != want[i] {
			t.Fatalf("order = %v, want %v", r.Order, want)
		}
	}
}

func TestUserCompleteReplacesAndCurrentMerges(t *testing.T) {
	s := New()

	s.ApplyAuth(&protocol.AuthResult{
		You: &protocol.User{UserID: "u1", Name: "Ada", Roles: []string{"bot"}},
	})
	if s.User("u1").Name != "Ada" || len(s.User("u1").Roles) != 1 {
		t.Fatalf("auth you not applied: %+v", s.User("u1"))
	}

	// A current (merge) object changes the name only.
	s.ApplyNotification("user", []byte(`{"new":{"user_id":"u1","name":"Ada Lovelace"}}`))
	u := s.User("u1")
	if u.Name != "Ada Lovelace" {
		t.Fatalf("merge name = %q", u.Name)
	}
	if len(u.Roles) != 1 {
		t.Fatalf("roles should be preserved on merge, got %v", u.Roles)
	}
}

func TestDisplayNameAndAmbiguity(t *testing.T) {
	s := New()
	s.ApplyNotification("user", []byte(`{"new":{"user_id":"u1","name":"Alice"}}`))
	s.ApplyNotification("user", []byte(`{"new":{"user_id":"u2","name":"Alice"}}`))

	if s.DisplayName("u1", nil) != "Alice" {
		t.Fatalf("display name = %q", s.DisplayName("u1", nil))
	}
	if !s.NameIsAmbiguous("u1", "Alice") {
		t.Fatalf("Alice should be ambiguous across u1/u2")
	}
	if s.DisplayName("u3", &protocol.User{UserID: "u3", Name: "Bob"}) != "Bob" {
		t.Fatalf("recorded fallback name should be used")
	}
	if s.DisplayName("u9", nil) != "u9" {
		t.Fatalf("unknown user should show as user_id")
	}
}

// TestJoinedAndNotJoinedStayDisjoint checks that moving a room between the
// joined and not_joined listings never leaves it in both.
func TestJoinedAndNotJoinedStayDisjoint(t *testing.T) {
	s := New()
	s.ApplyRoomList(&protocol.RoomListResult{
		NotJoined: []protocol.Room{{RoomID: "secret", Title: "Secret"}},
	})
	if got := s.NotJoined(); len(got) != 1 || got[0] != "secret" {
		t.Fatalf("not_joined = %v, want [secret]", got)
	}

	// Joining removes it from not_joined.
	s.ApplyRoomList(&protocol.RoomListResult{
		Joined: []protocol.Room{{RoomID: "secret", Title: "Secret"}},
	})
	if s.Room("secret") == nil {
		t.Fatal("secret should be joined")
	}
	if got := s.NotJoined(); len(got) != 0 {
		t.Fatalf("not_joined after join = %v, want empty", got)
	}
	if got := s.Rooms(); len(got) != 1 || got[0] != "secret" {
		t.Fatalf("joined after join = %v, want [secret]", got)
	}

	// Leaving moves it back, without a stale joined copy.
	s.ApplyRoomUpdate(&protocol.RoomUpdateParams{
		Left: []protocol.RoomRef{{RoomID: "secret"}},
	})
	s.ApplyRoomList(&protocol.RoomListResult{
		NotJoined: []protocol.Room{{RoomID: "secret", Title: "Secret"}},
	})
	if s.Room("secret") != nil {
		t.Fatal("secret should no longer be joined")
	}
	if got := s.Rooms(); len(got) != 0 {
		t.Fatalf("joined after leave = %v, want empty", got)
	}
	if got := s.NotJoined(); len(got) != 1 || got[0] != "secret" {
		t.Fatalf("not_joined after leave = %v, want [secret]", got)
	}
}

func TestRoomUpdateMembership(t *testing.T) {
	s := New()
	s.ApplyRoomList(&protocol.RoomListResult{
		Joined: []protocol.Room{
			{RoomID: "general", Members: []protocol.User{{UserID: "u1"}, {UserID: "u2"}}},
		},
	})
	r := s.Room("general")
	if len(r.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(r.Members))
	}

	// A membership record removes u2 and adds u3.
	s.ApplyRoomUpdate(&protocol.RoomUpdateParams{
		Memberships: []protocol.Membership{{
			LogID:  "500",
			RoomID: "general",
			Members: []protocol.MemberEntry{
				{User: protocol.User{UserID: "u2"}, Joined: false},
				{User: protocol.User{UserID: "u3"}, Joined: true},
			},
		}},
	})
	if _, ok := r.Members["u2"]; ok {
		t.Fatalf("u2 should have left")
	}
	if _, ok := r.Members["u3"]; !ok {
		t.Fatalf("u3 should have joined")
	}
}

func TestApplyHistoryPagesAndMoreBack(t *testing.T) {
	s := New()
	s.ApplyHistory("general", &protocol.HistoryResult{
		Messages: []protocol.Message{
			*msg("100", "100", "general", "a"),
			*msg("200", "200", "general", "b"),
		},
		FirstLogID: "100",
		LastLogID:  "200",
		More:       true,
	})

	r := s.Room("general")
	if !r.Loaded || !r.MoreBack {
		t.Fatalf("Loaded=%v MoreBack=%v", r.Loaded, r.MoreBack)
	}
	if r.FirstLogID != "100" {
		t.Fatalf("FirstLogID = %q", r.FirstLogID)
	}
	if len(r.Order) != 2 {
		t.Fatalf("order len = %d, want 2", len(r.Order))
	}
}

func TestApplyReactionsReplaceSetAndTotals(t *testing.T) {
	s := New()
	s.ApplyReactions(&protocol.ReactionsParams{
		MessageID: "m1",
		Reactions: []protocol.ReactionEntry{
			{From: protocol.User{UserID: "u1"}, Emojis: []string{"👍"}},
			{From: protocol.User{UserID: "u2"}, Emojis: []string{"👍"}},
		},
	})
	tot := s.ReactionTotals("m1")
	if len(tot) != 1 || tot[0].Emoji != "👍" || tot[0].Count != 2 {
		t.Fatalf("totals = %+v", tot)
	}

	// u1 replaces their set; an empty set clears.
	s.ApplyReactions(&protocol.ReactionsParams{
		MessageID: "m1",
		Reactions: []protocol.ReactionEntry{
			{From: protocol.User{UserID: "u1"}, Emojis: []string{"🎉"}},
			{From: protocol.User{UserID: "u2"}, Emojis: []string{}},
		},
	})
	tot = s.ReactionTotals("m1")
	if len(tot) != 1 || tot[0].Emoji != "🎉" || tot[0].Count != 1 {
		t.Fatalf("totals after replace = %+v", tot)
	}
	if got := s.MyReactions("m1", "u1"); len(got) != 1 || got[0] != "🎉" {
		t.Fatalf("my reactions = %v", got)
	}
}

func TestTypingTracksAndExpires(t *testing.T) {
	s := New()
	now := time.Now()

	typing := func(secs int) *int { return &secs }

	s.ApplyActivity(&protocol.ActivityParams{
		RoomID: "general", From: &protocol.User{UserID: "u1"}, Typing: typing(5),
	})
	got := s.TypingUsers("general", now)
	if len(got) != 1 || got[0] != "u1" {
		t.Fatalf("typing users = %v", got)
	}

	// Expired after 6 seconds.
	got = s.TypingUsers("general", now.Add(6*time.Second))
	if len(got) != 0 {
		t.Fatalf("typing should expire, got %v", got)
	}

	// A message from the user clears typing immediately.
	s.ApplyActivity(&protocol.ActivityParams{
		RoomID: "general", From: &protocol.User{UserID: "u2"}, Typing: typing(30),
	})
	s.ApplyMessage(msg("m1", "100", "general", "hi"))
	// (the msg has From u1; that only clears u1, not u2)
	if got := s.TypingUsers("general", now); len(got) != 1 || got[0] != "u2" {
		t.Fatalf("after message typing users = %v", got)
	}
}

func TestMessageByID(t *testing.T) {
	s := New()
	s.ApplyMessage(msg("100", "100", "general", "hello"))
	if got := s.MessageByID("100"); got == nil || got.Body.Text != "hello" {
		t.Fatalf("MessageByID = %+v", got)
	}
	if got := s.MessageByID("nope"); got != nil {
		t.Fatalf("MessageByID(nope) = %+v, want nil", got)
	}
}

func TestAfterComparesNumerically(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"100", "99", true},
		{"2", "10", false}, // numeric, not lexicographic
		{"100", "100", false},
		{"", "1", false},
		{"1", "", true},
	}
	for _, c := range cases {
		if got := after(c.a, c.b); got != c.want {
			t.Errorf("after(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// --- R3 room-less history -----------------------------------------------------

// TestApplyLogHistoryRoutesByRoom: a room-less page (§4.3) spans rooms; each
// record lands in its own room, and the bounds move the log cursor, not a
// room's.
func TestApplyLogHistoryRoutesByRoom(t *testing.T) {
	s := New()
	res := &protocol.HistoryResult{
		Rooms: []protocol.Room{{RoomID: "ops", LogID: "5", Title: "Ops", LatestLogID: "999"}},
		Messages: []protocol.Message{
			{MessageID: "10", LogID: "10", RoomID: "general", Body: &protocol.Body{Text: "a"}},
			{MessageID: "11", LogID: "11", RoomID: "ops", Body: &protocol.Body{Text: "b"}},
		},
		// Folded memberships (§4.3.4) for two rooms in one page.
		Memberships: []protocol.Membership{
			{LogID: "7", RoomID: "general", Members: []protocol.MemberEntry{{User: protocol.User{UserID: "dave"}, Joined: true}}},
			{LogID: "8", RoomID: "ops", Members: []protocol.MemberEntry{{User: protocol.User{UserID: "erin"}, Joined: true}}},
		},
		Reactions: []protocol.ReactionsParams{{LogID: "12", MessageID: "11", RoomID: "ops",
			Reactions: []protocol.ReactionEntry{{From: protocol.User{UserID: "dave"}, Emojis: []string{"👍"}}}}},
		FirstLogID: "5", LastLogID: "12", More: true,
		LatestLogID: "12", HistoryLogID: strPtr("1"),
	}
	touched := s.ApplyLogHistory(res, false)
	if len(touched) != 2 {
		t.Fatalf("touched = %v, want general and ops", touched)
	}

	g, o := s.Room("general"), s.Room("ops")
	if g == nil || o == nil {
		t.Fatal("both rooms should exist")
	}
	if g.Messages["10"] == nil || o.Messages["11"] == nil || g.Messages["11"] != nil {
		t.Fatal("messages not routed by room_id")
	}
	if g.Members["dave"] == nil || o.Members["erin"] == nil || g.Members["erin"] != nil {
		t.Fatal("memberships not routed by room_id")
	}
	if o.Title() != "Ops" {
		t.Fatalf("room record not applied: %q", o.Title())
	}
	if o.LatestLogID != "" {
		t.Fatalf("logged room record's delivery fields must be ignored, got latest %q", o.LatestLogID)
	}
	if len(s.ReactionTotals("11")) != 1 {
		t.Fatal("reaction set not applied")
	}
	if !g.Loaded || !o.Loaded {
		t.Fatal("touched rooms should be marked loaded")
	}
	if g.FirstLogID != "" || g.MoreBack {
		t.Fatal("room-less bounds must not move a room's paging cursor")
	}

	l := s.Log
	if !l.Loaded || l.FirstLogID != "5" || !l.MoreBack || l.LatestLogID != "12" {
		t.Fatalf("log cursor = %+v", l)
	}

	// An older page lowers first_log_id; a forward page leaves it alone.
	s.ApplyLogHistory(&protocol.HistoryResult{FirstLogID: "2", LastLogID: "4", LatestLogID: "12", HistoryLogID: strPtr("1")}, false)
	if s.Log.FirstLogID != "2" || s.Log.MoreBack {
		t.Fatalf("after older page: %+v", s.Log)
	}
	s.ApplyLogHistory(&protocol.HistoryResult{FirstLogID: "13", LastLogID: "14", More: true, LatestLogID: "14", HistoryLogID: strPtr("1")}, true)
	if s.Log.FirstLogID != "2" || s.Log.MoreBack || s.Log.LatestLogID != "14" {
		t.Fatalf("forward page moved the backward cursor: %+v", s.Log)
	}
}

// TestLogBoundPastCheckpoint: at the log level, a bound past the next needed
// position clears every room (§4.3.5).
func TestLogBoundPastCheckpoint(t *testing.T) {
	s := New()
	s.ApplyLogHistory(&protocol.HistoryResult{
		Messages: []protocol.Message{
			{MessageID: "10", LogID: "10", RoomID: "general"},
			{MessageID: "11", LogID: "11", RoomID: "ops"},
		},
		FirstLogID: "10", LastLogID: "11", LatestLogID: "11", HistoryLogID: strPtr("1"),
	}, false)
	s.Log.Checkpoint = "11"
	if s.Log.BoundPastCheckpoint() {
		t.Fatal("bound 1 is not past checkpoint 11")
	}

	s.Log.HistoryLogID = strPtr("50")
	s.Log.LatestLogID = "60"
	if !s.Log.BoundPastCheckpoint() {
		t.Fatal("bound 50 should be past checkpoint 11")
	}
	s.Log.HistoryLogID = nil
	if !s.Log.BoundPastCheckpoint() {
		t.Fatal("null history_log_id with newer changes should be past the checkpoint")
	}

	s.ResetLogContent()
	if len(s.Room("general").Messages) != 0 || len(s.Room("ops").Messages) != 0 {
		t.Fatal("reset should clear every room")
	}
	if s.Log.Loaded || s.Log.Checkpoint != "" || s.Log.FirstLogID != "" {
		t.Fatalf("reset should clear the cursor: %+v", s.Log)
	}
}
