package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/stevedylandev/whisk/internal/client"
	"github.com/stevedylandev/whisk/internal/config"
	"github.com/stevedylandev/whisk/internal/protocol"
)

func newTestModel(t *testing.T) Model {
	lipgloss.SetHasDarkBackground(true)
	return NewModel(testCfg(), t.TempDir()+"/cfg.json", "", "", "")
}

// --- M1 mentions ------------------------------------------------------------

func TestScanMentions(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"hello @guest_1234 world", []string{"guest_1234"}},
		{"foo@bar.com tag#ops", nil},
		{"https://example.com/@bob?", []string{"bob"}},
		{"@.foo", nil},
		{"@a.b.c:", []string{"a.b.c"}},
		{"@carol@b.example hi", []string{"carol@b.example"}},
		{"mention @alice_2.0 please", []string{"alice_2.0"}},
		{"", nil},
	}
	for _, c := range cases {
		got := scanMentions(c.text)
		var ids []string
		for _, t := range got {
			ids = append(ids, t.ID)
		}
		if len(ids) != len(c.want) {
			t.Errorf("scanMentions(%q) = %v, want %v", c.text, ids, c.want)
			continue
		}
		for i := range ids {
			if ids[i] != c.want[i] {
				t.Errorf("scanMentions(%q) = %v, want %v", c.text, ids, c.want)
				break
			}
		}
	}
}

func TestExtractMentionsKnownUsersOnly(t *testing.T) {
	m := newTestModel(t)
	m.cur().store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Me"}})
	m.cur().store.ApplyNotification("user", []byte(`{"new":{"user_id":"u2","name":"Bob"}}`))
	m.cur().store.ApplyRoomList(&protocol.RoomListResult{
		Joined: []protocol.Room{{RoomID: "general", Members: []protocol.User{{UserID: "u3"}, {UserID: "u1"}}}},
	})

	got := extractMentions(m.cur().store, "general", "hi @u2 @u3 @unknown and @u1")
	want := []string{"u2", "u3"}
	if len(got) != len(want) {
		t.Fatalf("extractMentions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("extractMentions = %v, want %v", got, want)
		}
	}
}

func TestApplyMentionNames(t *testing.T) {
	m := newTestModel(t)
	m.cur().store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Me"}})
	m.cur().store.ApplyNotification("user", []byte(`{"new":{"user_id":"u2","name":"Bob"}}`))

	got := applyMentionNames(m.cur().store, "general", "hi @u2 and @unknown")
	if !strings.Contains(got, "@Bob") {
		t.Fatalf("applyMentionNames = %q, want @Bob", got)
	}
	if !strings.Contains(got, "@unknown") {
		t.Fatalf("applyMentionNames = %q, want unknown left as-is", got)
	}
}

// --- E1 save semantics ------------------------------------------------------

func TestBuildSaveResendsClientFields(t *testing.T) {
	existing := &protocol.Message{
		MessageID: "m1",
		RoomID:    "general",
		ReplyTo:   &protocol.Message{MessageID: "m0", Body: &protocol.Body{Text: "orig"}},
		Body: &protocol.Body{
			Text:     "old",
			Format:   "markdown",
			Mentions: []string{"alice"},
			Embeds: []protocol.Embed{
				{EmbedID: "e1", Kind: "upload", Title: "a.png", URL: "https://x/a"},
				{Kind: "upload", Title: "pending.png"},
			},
		},
	}

	save := buildSave(existing, "general", "new", []string{"bob"})

	if save.MessageID != "m1" || save.RoomID != "general" {
		t.Fatalf("save identity = %+v", save)
	}
	if save.ReplyTo == nil || save.ReplyTo.MessageID != "m0" {
		t.Fatalf("save must keep reply_to, got %+v", save.ReplyTo)
	}
	if save.Body.Format != "markdown" {
		t.Fatalf("save format = %q, want markdown", save.Body.Format)
	}
	// Existing mentions are kept and new ones merged, deduplicated.
	if len(save.Body.Mentions) != 2 || save.Body.Mentions[0] != "bob" || save.Body.Mentions[1] != "alice" {
		t.Fatalf("save mentions = %v", save.Body.Mentions)
	}
	// Only embeds with an embed_id are kept (§4.8.2).
	if len(save.Body.Embeds) != 1 || save.Body.Embeds[0].EmbedID != "e1" {
		t.Fatalf("save embeds = %+v, want only e1", save.Body.Embeds)
	}
}

// --- D1 default-room posting -------------------------------------------------

func TestComposeMessageDefaultRoom(t *testing.T) {
	m := newTestModel(t)
	m.cur().authed = true
	m.cur().store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}}) // no rooms capability
	if !m.canPostWithoutRoom() {
		t.Fatal("server without rooms should allow posting with no room_id")
	}
	if p := m.composeMessage("hi"); p == nil || p.RoomID != "" {
		t.Fatalf("composeMessage with no room = %+v", p)
	}

	// With rooms capability and a joined room known, a room must be selected.
	m.cur().store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}, Capabilities: []string{"rooms"}})
	m.cur().store.ApplyRoomList(&protocol.RoomListResult{Joined: []protocol.Room{{RoomID: "general"}}})
	if m.canPostWithoutRoom() {
		t.Fatal("a room must be selected once joined rooms are known")
	}
	if p := m.composeMessage("hi"); p != nil {
		t.Fatalf("composeMessage should require a room, got %+v", p)
	}
	m.chat.roomID = "general"
	if p := m.composeMessage("hi"); p == nil || p.RoomID != "general" {
		t.Fatalf("composeMessage with room = %+v", p)
	}
}

// --- C6 signup schemes ------------------------------------------------------

func TestSignupSchemesListedInChooser(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenAuth
	m.auth.sess = m.cur()
	m.onEvent(m.cur(), client.Event{Method: "server", Params: json.RawMessage(
		`{"apron":9,"auth":["token"],"signup":["guest"]}`)})

	if !hasScheme(m.auth.signups, "guest") {
		t.Fatalf("signups = %v, want guest", m.auth.signups)
	}
	v := m.viewAuthChoose()
	if !strings.Contains(v, "create an account with") {
		t.Fatalf("chooser missing signup section: %q", v)
	}

	// The uppercase key starts a sign-up flow.
	m.updateAuthChoose(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	if m.auth.phase != phasePrompt || m.auth.promptKind != promptName || !m.auth.signupMode {
		t.Fatalf("signup guest flow: phase=%v kind=%v signup=%v", m.auth.phase, m.auth.promptKind, m.auth.signupMode)
	}
}

// --- C4 / C3 reconnect semantics --------------------------------------------

// TestDeniedAuthMarksServerForSignIn: a background token resume that is
// denied drops the token, stops reconnecting, and leaves the app on the chat
// screen with the server marked as needing sign-in.
func TestDeniedAuthMarksServerForSignIn(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenChat
	m.cur().authed = false
	m.cur().cfg.Token = "stale-token"
	m.cur().lastAuthScheme = "token"
	m.cur().reconnecting = true
	m.cur().store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"token", "guest"}})

	m.handleRPCError(m.cur(), requestDoneMsg{sid: m.cur().id, tag: "auth", rpcErr: &protocol.Error{Code: protocol.ErrDenied, Message: "Session expired"}})

	if m.screen != screenChat {
		t.Fatalf("denied background auth must not leave the chat: screen=%v", m.screen)
	}
	if !m.cur().needsSignIn {
		t.Fatal("server should be marked as needing sign-in")
	}
	if m.cur().cfg.Token != "" {
		t.Fatalf("stale token should be dropped, got %q", m.cur().cfg.Token)
	}
	if m.cur().reconnecting || !m.cur().noReconnect {
		t.Fatalf("reconnecting=%v noReconnect=%v, want false/true", m.cur().reconnecting, m.cur().noReconnect)
	}
}

func TestUnsolicitedDeniedStopsReconnect(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenChat
	m.cur().authed = true

	m.onEvent(m.cur(), client.Event{Error: &protocol.Error{Code: protocol.ErrDenied, Message: "banned"}})

	if !m.cur().noReconnect || m.cur().reconnecting {
		t.Fatalf("noReconnect=%v reconnecting=%v", m.cur().noReconnect, m.cur().reconnecting)
	}
	if m.screen != screenChat || !m.cur().needsSignIn || m.cur().client != nil {
		t.Fatalf("screen=%v needsSignIn=%v, want chat/true and closed", m.screen, m.cur().needsSignIn)
	}
}

func TestUnsolicitedRetryAfterSetsDelay(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenChat
	m.cur().authed = true

	m.onEvent(m.cur(), client.Event{Error: &protocol.Error{
		Code:    protocol.ErrRetryAfter,
		Message: "capacity",
		Data:    json.RawMessage(`{"retry_after":25}`),
	}})
	if m.cur().retryAfter != 25*time.Second {
		t.Fatalf("retryAfter = %v, want 25s", m.cur().retryAfter)
	}

	// The delay applies to the next reconnect attempt, once.
	cmds := m.onDisconnect(m.cur(), nil)
	if len(cmds) != 1 {
		t.Fatalf("onDisconnect cmds = %d, want 1", len(cmds))
	}
	if m.cur().retryAfter != 0 {
		t.Fatalf("retryAfter should be consumed, got %v", m.cur().retryAfter)
	}
	if !m.cur().reconnecting {
		t.Fatal("should still be reconnecting after retry_after")
	}
}

// --- S1 status / mute -------------------------------------------------------

func TestValidateStatus(t *testing.T) {
	srv := &protocol.Server{Apron: 9, Status: []string{"dnd", "invisible"}}
	if err := validateStatus(srv, "online"); err != nil {
		t.Fatalf("online should always be accepted: %v", err)
	}
	if err := validateStatus(srv, ""); err != nil {
		t.Fatalf("empty (opt-out) should always be accepted: %v", err)
	}
	if err := validateStatus(srv, "dnd"); err != nil {
		t.Fatalf("dnd should be accepted: %v", err)
	}
	if err := validateStatus(srv, "away"); err == nil {
		t.Fatal("unknown status should be rejected")
	}
}

func TestParseMuteArgs(t *testing.T) {
	m := newTestModel(t)
	m.cur().store.ApplyRoomList(&protocol.RoomListResult{Joined: []protocol.Room{{RoomID: "general"}}})

	cases := []struct {
		args     []string
		roomID   string
		wantRoom string
		wantMute string
		wantErr  bool
	}{
		{[]string{}, "general", "general", "true", false},
		{[]string{"off"}, "general", "general", "false", false},
		{[]string{"300"}, "general", "general", "300", false},
		{[]string{"*"}, "general", "", "true", false},
		{[]string{"*", "120"}, "general", "", "120", false},
		{[]string{"general", "off"}, "", "general", "false", false},
		{[]string{}, "", "", "", true},
	}
	for _, c := range cases {
		p, err := parseMuteArgs(c.args, c.roomID, &m)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseMuteArgs(%v) expected error", c.args)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseMuteArgs(%v) = %v", c.args, err)
			continue
		}
		if p.RoomID != c.wantRoom || string(p.Mute) != c.wantMute {
			t.Errorf("parseMuteArgs(%v) = {%q %s}, want {%q %s}", c.args, p.RoomID, p.Mute, c.wantRoom, c.wantMute)
		}
	}
}

// --- rendering: seen, roles, notices ----------------------------------------

func TestRenderMessageSeenAndRoles(t *testing.T) {
	m := newTestModel(t)
	m.cur().store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Me"}})
	m.cur().store.ApplyNotification("user", []byte(`{"new":{"user_id":"u2","name":"Bob","status":"offline","seen":"1724803600000"}}`))
	msg := &protocol.Message{
		MessageID: "100", LogID: "100", RoomID: "general",
		From: &protocol.User{UserID: "u2", Name: "Bob", Roles: []string{"bot"}},
		Body: &protocol.Body{Text: "hello"},
	}
	out := renderMessage(m.cur().store, newRenderer(80), msg, 80)
	if !strings.Contains(out, "[bot]") {
		t.Fatalf("roles badge missing: %q", out)
	}
	if !strings.Contains(out, "last seen") {
		t.Fatalf("last seen missing: %q", out)
	}
}

func TestRenderMessageMovedFromSource(t *testing.T) {
	m := newTestModel(t)
	m.cur().store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Me"}})
	msg := &protocol.Message{
		MessageID: "100", LogID: "200", RoomID: "thread-1", PrevRoomID: "general",
		From: &protocol.User{UserID: "u2", Name: "Bob"},
		Body: &protocol.Body{Text: "hello"},
	}
	out := renderMessage(m.cur().store, newRenderer(80), msg, 80)
	if !strings.Contains(out, "moved from #general") {
		t.Fatalf("moved-from note missing: %q", out)
	}
}

func TestRenderMessageMentionNames(t *testing.T) {
	m := newTestModel(t)
	m.cur().store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Me"}})
	m.cur().store.ApplyNotification("user", []byte(`{"new":{"user_id":"u2","name":"Bob"}}`))
	msg := &protocol.Message{
		MessageID: "100", LogID: "100", RoomID: "general",
		From: &protocol.User{UserID: "u2", Name: "Bob"},
		Body: &protocol.Body{Text: "thanks @u2"},
	}
	out := renderMessage(m.cur().store, newRenderer(80), msg, 80)
	if !strings.Contains(out, "@Bob") {
		t.Fatalf("mention should render the display name: %q", out)
	}
}

func TestNoticesOverlayRenders(t *testing.T) {
	m := newTestModel(t)
	m.cur().store.ApplyMessage(&protocol.Message{
		From: &protocol.User{UserID: "~private", Name: "Only you"},
		Body: &protocol.Body{Text: "command ran"},
	})
	v := m.noticesView()
	if !strings.Contains(v, "notices") || !strings.Contains(v, "command ran") {
		t.Fatalf("notices view = %q", v)
	}
}

func TestRoomNoticeRenderedInline(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenChat
	m.cur().authed = true
	m.cur().store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}, Capabilities: []string{"history"}})
	m.cur().store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Ada"}})
	m.cur().store.ApplyMessage(&protocol.Message{
		MessageID: "100", LogID: "100", RoomID: "general",
		From: &protocol.User{UserID: "u1", Name: "Ada"},
		Body: &protocol.Body{Text: "hello"},
	})
	m.cur().store.ApplyMessage(&protocol.Message{
		RoomID: "general",
		From:   &protocol.User{UserID: "~private", Name: "Only you"},
		Body:   &protocol.Body{Text: "only you see this"},
	})
	m.chat.roomID = "general"
	m.width, m.height = 80, 24
	m.resize()
	m.renderContent()
	if !strings.Contains(m.chat.viewport.View(), "only you see this") {
		t.Fatalf("room notice not rendered inline: %q", m.chat.viewport.View())
	}
}

// --- config persistence & resume --------------------------------------------

func TestFinishAuthKeepsSavedToken(t *testing.T) {
	lipgloss.SetHasDarkBackground(true)
	path := t.TempDir() + "/config.json"
	m := NewModel(config.Config{Servers: []config.Server{{URL: "wss://example/", Token: "st_keepme"}}}, path, "", "", "")

	// A token sign-in whose result carries no token (§3.2.1: the server only
	// sends one when it rotates) must not erase the saved token.
	m.finishAuth(m.cur(), protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Ada"}})

	if m.cur().cfg.Token != "st_keepme" {
		t.Fatalf("in-memory token overwritten: %q", m.cur().cfg.Token)
	}
	saved, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(saved.Servers) != 1 || saved.Servers[0].Token != "st_keepme" {
		t.Fatalf("saved servers = %+v, want token st_keepme", saved.Servers)
	}

	// A rotated token does replace the saved one.
	m.finishAuth(m.cur(), protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Ada"}, Token: "st_rotated"})
	if m.cur().cfg.Token != "st_rotated" {
		t.Fatalf("rotated token not saved: %q", m.cur().cfg.Token)
	}
}

func TestSavedTokenResumesAutomatically(t *testing.T) {
	lipgloss.SetHasDarkBackground(true)
	m := NewModel(config.Config{Servers: []config.Server{{URL: "wss://example/", Token: "st_x"}}}, t.TempDir()+"/c.json", "", "", "")
	m.onEvent(m.cur(), client.Event{Method: "server", Params: json.RawMessage(`{"apron":9,"auth":["token","guest"]}`)})

	if m.auth.phase == phaseChoose {
		t.Fatal("a saved token should resume automatically, not show the chooser")
	}
	if m.cur().lastAuthScheme != "token" {
		t.Fatalf("lastAuthScheme = %q, want token", m.cur().lastAuthScheme)
	}
}

func TestMoveSelectionMarksDirty(t *testing.T) {
	lipgloss.SetHasDarkBackground(true)
	m := NewModel(testCfg(), t.TempDir()+"/c.json", "", "", "")
	m.screen = screenChat
	m.cur().authed = true
	m.cur().store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}, Capabilities: []string{"rooms", "history"}})
	m.cur().store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Ada"}})
	m.cur().store.ApplyMessage(&protocol.Message{MessageID: "100", LogID: "100", RoomID: "general", From: &protocol.User{UserID: "u1", Name: "Ada"}, Body: &protocol.Body{Text: "a"}})
	m.cur().store.ApplyMessage(&protocol.Message{MessageID: "200", LogID: "200", RoomID: "general", From: &protocol.User{UserID: "u2", Name: "Bob"}, Body: &protocol.Body{Text: "b"}})
	m.chat.roomID = "general"
	m.width, m.height = 80, 24
	m.resize()
	m.renderContent()
	m.chat.dirty = false

	m.moveSelection(-1)
	if !m.chat.dirty {
		t.Fatal("moveSelection must mark the viewport dirty so the marker re-renders")
	}
}

func TestLayoutFitsWithinWidth(t *testing.T) {
	lipgloss.SetHasDarkBackground(true)
	m := NewModel(testCfg(), t.TempDir()+"/c.json", "", "", "")
	sw, cw := m.chat.layout(80)
	total := sw + sidebarStyle.GetHorizontalFrameSize() + paneGap + cw + msgViewStyle.GetHorizontalFrameSize()
	if total != 80 {
		t.Fatalf("layout total = %d, want 80 (sw=%d cw=%d)", total, sw, cw)
	}
	if cw < 20 {
		t.Fatalf("message pane too narrow: %d", cw)
	}
}

// --- C5 not_joined listing --------------------------------------------------

func TestRoomListParamsFilterAll(t *testing.T) {
	m := newTestModel(t)
	p := roomListParams(m.cur().store)
	if p.Filter != "all" || !p.Members {
		t.Fatalf("roomListParams = %+v, want filter=all members=true", p)
	}
	if p.LatestLogID != "" {
		t.Fatalf("empty store should not set latest_log_id, got %q", p.LatestLogID)
	}

	// After loading rooms with checkpoints, the smallest checkpoint filters
	// the listing (§4.3.5).
	m.cur().store.ApplyHistory("general", &protocol.HistoryResult{LatestLogID: "200"})
	m.cur().store.Room("general").Checkpoint = "200"
	m.cur().store.ApplyHistory("ops", &protocol.HistoryResult{LatestLogID: "100"})
	m.cur().store.Room("ops").Checkpoint = "100"
	p = roomListParams(m.cur().store)
	if p.LatestLogID != "100" {
		t.Fatalf("latest_log_id = %q, want 100", p.LatestLogID)
	}
}

// --- R2 / R4 room-less history ----------------------------------------------

// cmdTag runs a request command (with no connection) and returns its tag.
func cmdTag(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a request command, got nil")
	}
	done, ok := cmd().(requestDoneMsg)
	if !ok {
		t.Fatalf("command did not produce a request")
	}
	return done.tag
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestRoomlessHistoryOnServerWithoutRooms: on a history-without-rooms server,
// sign-in sends a room-less history query; its page opens the default room.
func TestRoomlessHistoryOnServerWithoutRooms(t *testing.T) {
	m := newTestModel(t)
	s := m.cur()
	m.screen = screenChat
	s.store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}, Capabilities: []string{"history"}})

	if tag := cmdTag(t, m.finishAuth(s, protocol.AuthResult{You: &protocol.User{UserID: "me"}})); tag != "log_history" {
		t.Fatalf("sign-in request = %q, want log_history", tag)
	}
	if !s.store.Log.Loading {
		t.Fatal("log should be loading")
	}

	m.handleRequestDone(s, requestDoneMsg{sid: s.id, tag: "log_history", result: mustJSON(t, protocol.HistoryResult{
		Messages: []protocol.Message{
			{MessageID: "10", LogID: "10", RoomID: "lobby", Body: &protocol.Body{Text: "hi"}},
		},
		FirstLogID: "10", LastLogID: "10", More: true, LatestLogID: "10", HistoryLogID: strPtr("1"),
	})})
	if m.chat.roomID != "lobby" {
		t.Fatalf("roomID = %q, want lobby (learned from the page)", m.chat.roomID)
	}
	if r := s.store.Room("lobby"); r == nil || r.Messages["10"] == nil {
		t.Fatal("backlog not installed")
	}
	if s.store.Log.Checkpoint != "10" {
		t.Fatalf("log checkpoint = %q, want 10", s.store.Log.Checkpoint)
	}

	// Older pages use the same room-less query.
	if tag := cmdTag(t, m.loadOlder()); tag != "log_history" {
		t.Fatalf("older page = %q, want log_history", tag)
	}
	m.handleRequestDone(s, requestDoneMsg{sid: s.id, tag: "log_history", result: mustJSON(t, protocol.HistoryResult{
		LatestLogID: "10", HistoryLogID: strPtr("1"),
	})})

	// A reconnect resumes forward from the checkpoint.
	if tag := cmdTag(t, m.finishAuth(s, protocol.AuthResult{You: &protocol.User{UserID: "me"}})); tag != "log_forward" {
		t.Fatalf("resume request = %q, want log_forward", tag)
	}
	// Retention moved past the checkpoint: clear and reload the newest page.
	cmd := m.handleRequestDone(s, requestDoneMsg{sid: s.id, tag: "log_forward", result: mustJSON(t, protocol.HistoryResult{
		LatestLogID: "90", HistoryLogID: strPtr("50"),
	})})
	if tag := cmdTag(t, cmd); tag != "log_history" {
		t.Fatalf("after bound reset = %q, want log_history", tag)
	}
	if len(s.store.Room("lobby").Messages) != 0 {
		t.Fatal("bound past checkpoint should clear the room")
	}
}

// TestRoomlessHistoryUnsupportedFallsBack: an unsupported reply turns the
// room-less query off; a live message then pages its room directly.
func TestRoomlessHistoryUnsupportedFallsBack(t *testing.T) {
	m := newTestModel(t)
	s := m.cur()
	m.screen = screenChat
	s.store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}, Capabilities: []string{"history"}})
	s.store.Log.Loading = true

	m.handleRequestDone(s, requestDoneMsg{sid: s.id, tag: "log_history",
		rpcErr: &protocol.Error{Code: protocol.ErrUnsupported, Message: "unsupported"}})
	if !s.logUnsupported || s.store.Log.Loading {
		t.Fatalf("logUnsupported=%v loading=%v", s.logUnsupported, s.store.Log.Loading)
	}
	if m.finishAuth(s, protocol.AuthResult{You: &protocol.User{UserID: "me"}}) != nil {
		t.Fatal("no room-less query after unsupported")
	}

	cmds := m.onEvent(s, client.Event{Method: "message", Params: json.RawMessage(
		`{"message_id":"5","log_id":"5","room_id":"lobby","body":{"text":"x"}}`)})
	found := false
	for _, c := range cmds {
		if c == nil {
			continue
		}
		if d, ok := c().(requestDoneMsg); ok && d.tag == "history" && d.roomID == "lobby" {
			found = true
		}
	}
	if !found {
		t.Fatal("live message should page the revealed room's history")
	}
}

// TestCatchUpOneStream: several rooms behind their checkpoints recover with
// one room-less forward stream (R4), and fall back per room on unsupported.
func TestCatchUpOneStream(t *testing.T) {
	m := newTestModel(t)
	s := m.cur()
	s.store.ApplyServer(&protocol.Server{Apron: 9, Capabilities: []string{"rooms", "history"}})
	for _, id := range []string{"a", "b"} {
		s.store.ApplyHistory(id, &protocol.HistoryResult{LatestLogID: "100", HistoryLogID: strPtr("1")})
		s.store.Room(id).Checkpoint = "100"
	}
	s.store.Room("a").LatestLogID = "120"
	s.store.Room("b").LatestLogID = "150"

	cmds := m.catchUpRooms(s)
	if len(cmds) != 1 || cmdTag(t, cmds[0]) != "log_forward" {
		t.Fatalf("want one log_forward, got %d cmds", len(cmds))
	}
	if s.logTo != "150" || s.store.Log.Checkpoint != "100" {
		t.Fatalf("stream bounds: to=%q from=%q", s.logTo, s.store.Log.Checkpoint)
	}

	// First page passes a's H only.
	cmd := m.handleRequestDone(s, requestDoneMsg{sid: s.id, tag: "log_forward", result: mustJSON(t, protocol.HistoryResult{
		Messages:   []protocol.Message{{MessageID: "110", LogID: "110", RoomID: "a"}},
		FirstLogID: "101", LastLogID: "130", More: true, LatestLogID: "150", HistoryLogID: strPtr("1"),
	})})
	if cmdTag(t, cmd) != "log_forward" {
		t.Fatal("stream should continue")
	}
	if s.store.Room("a").Checkpoint != "120" || s.store.Room("b").Checkpoint != "100" {
		t.Fatalf("checkpoints a=%q b=%q", s.store.Room("a").Checkpoint, s.store.Room("b").Checkpoint)
	}
	m.handleRequestDone(s, requestDoneMsg{sid: s.id, tag: "log_forward", result: mustJSON(t, protocol.HistoryResult{
		Messages:   []protocol.Message{{MessageID: "140", LogID: "140", RoomID: "b"}},
		FirstLogID: "131", LastLogID: "150", LatestLogID: "150", HistoryLogID: strPtr("1"),
	})})
	if s.store.Room("b").Checkpoint != "150" || s.logCatchUp != nil || s.store.Room("b").Loading {
		t.Fatalf("stream did not finish: b=%q", s.store.Room("b").Checkpoint)
	}

	// The log bound passed the stream's start: per-room recovery takes
	// over, without restarting the stream. Only one history request may be
	// in flight: room a's page goes out first; room b's waits in the queue
	// until the slot is free again.
	s.store.Room("a").LatestLogID = "180"
	s.store.Room("b").LatestLogID = "180"
	m.catchUpRooms(s)
	cmd = m.handleRequestDone(s, requestDoneMsg{sid: s.id, tag: "log_forward", result: mustJSON(t, protocol.HistoryResult{
		LatestLogID: "180", HistoryLogID: strPtr("170"),
	})})
	if s.logCatchUp != nil || s.logUnsupported {
		t.Fatal("bound past start should abandon the stream, not the query")
	}
	first, ok := cmd().(requestDoneMsg)
	if !ok || first.tag != "history_forward" || first.roomID != "a" {
		t.Fatalf("room a's page should go out first, got %v", cmd())
	}
	// A reply for a frees the connection's one history slot, and the
	// queued page for b goes out.
	next := m.handleRequestDone(s, requestDoneMsg{sid: s.id, tag: "history_forward", roomID: "a",
		result: mustJSON(t, protocol.HistoryResult{LatestLogID: "180", HistoryLogID: strPtr("170")})})
	second, ok := next().(requestDoneMsg)
	if !ok || second.tag != "history_forward" || second.roomID != "b" {
		t.Fatalf("room b's queued page should follow a's, got %v", next())
	}
	for _, id := range []string{"a", "b"} {
		r := s.store.Room(id)
		r.Loading = false
		r.Checkpoint = "180"
	}

	// Unsupported: fall back to one forward page per room.
	s.store.Room("a").LatestLogID = "200"
	s.store.Room("b").LatestLogID = "200"
	if cmds := m.catchUpRooms(s); len(cmds) != 1 {
		t.Fatalf("want one log_forward, got %d", len(cmds))
	}
	cmd = m.handleRequestDone(s, requestDoneMsg{sid: s.id, tag: "log_forward",
		rpcErr: &protocol.Error{Code: protocol.ErrUnsupported}})
	if cmd == nil {
		t.Fatal("unsupported should fall back to per-room recovery")
	}
	if !s.logUnsupported || s.logCatchUp != nil {
		t.Fatal("room-less query should be off")
	}
	if cmds := m.catchUpRooms(s); len(cmds) != 0 {
		t.Fatalf("rooms already recovering per room, got %d new cmds", len(cmds))
	}
}

func strPtr(s string) *string { return &s }

// TestSidebarLongServerFits: a long server name, selected or not, is clipped
// to the sidebar's text area (its width minus padding) instead of wrapping.
func TestSidebarLongServerFits(t *testing.T) {
	lipgloss.SetHasDarkBackground(true)
	cfg := config.Config{Servers: []config.Server{{URL: "wss://apronchat.averyveryverylongdomain.example.me/"}}}
	m := NewModel(cfg, t.TempDir()+"/c.json", "", "", "")
	m.width, m.height = 100, 30
	m.screen = screenChat
	m.cur().authed = true
	m.refreshRoomItems()
	sw, _ := m.chat.layout(m.width)
	inner := sw - sidebarStyle.GetHorizontalPadding()

	for _, focus := range []focusMode{focusRooms, focusMessages} {
		m.chat.focus = focus
		m.chat.roomCursor = 0
		for _, l := range strings.Split(m.sidebarView(), "\n") {
			if w := lipgloss.Width(l); w > inner {
				t.Fatalf("focus %v: line %q is %d wide, sidebar text area is %d", focus, l, w, inner)
			}
		}
	}
}
