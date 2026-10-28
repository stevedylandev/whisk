package ui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/stevedylandev/whisk/internal/client"
	"github.com/stevedylandev/whisk/internal/config"
	"github.com/stevedylandev/whisk/internal/protocol"
)

// newTwoServers builds a model with two saved, signed-in servers, each with
// one joined room, and the first server's room open.
func newTwoServers(t *testing.T) (Model, *session, *session) {
	t.Helper()
	lipgloss.SetHasDarkBackground(true)
	cfg := config.Config{Servers: []config.Server{
		{URL: "wss://one.example/", Token: "t1"},
		{URL: "wss://two.example/", Token: "t2"},
	}}
	m := NewModel(cfg, t.TempDir()+"/cfg.json", "", "", "")
	a, b := m.sessions[0], m.sessions[1]
	for i, s := range []*session{a, b} {
		s.authed = true
		s.store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"token"}, Capabilities: []string{"rooms"}})
		name := []string{"Ada", "Bea"}[i]
		s.store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "u", Name: name}})
	}
	a.store.ApplyRoomList(&protocol.RoomListResult{Joined: []protocol.Room{{RoomID: "general", Title: "General"}}})
	b.store.ApplyRoomList(&protocol.RoomListResult{Joined: []protocol.Room{{RoomID: "lobby", Title: "Lobby"}}})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.selectRoom(a, "general")
	return m, a, b
}

func TestSavedServersStartOnChat(t *testing.T) {
	m, _, _ := newTwoServers(t)
	if m.screen != screenChat {
		t.Fatalf("screen = %v, want chat (saved servers must not walk through sign-in)", m.screen)
	}
	if len(m.sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(m.sessions))
	}
	for _, s := range m.sessions {
		if !s.connecting {
			t.Fatalf("%s should connect in the background", s.label())
		}
	}
}

func TestSidebarHasServerHeaders(t *testing.T) {
	m, _, _ := newTwoServers(t)
	m.renderContent()
	v := m.View()
	for _, want := range []string{"one.example", "two.example", "General", "Lobby", "Ada", "Bea"} {
		if !strings.Contains(v, want) {
			t.Fatalf("view missing %q:\n%s", want, v)
		}
	}
}

func TestNavigatingAcrossServersSwitchesSession(t *testing.T) {
	m, _, b := newTwoServers(t)
	// Cursor is on General; next rows are server two's header, then Lobby.
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if !m.chat.roomItems[m.chat.roomCursor].header {
		t.Fatal("expected the cursor on the second server header")
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if m.chat.sid != b.id || m.chat.roomID != "lobby" {
		t.Fatalf("open room = %d/%q, want server two/lobby", m.chat.sid, m.chat.roomID)
	}
	if !strings.Contains(m.headerView(), "two.example") {
		t.Fatalf("header should name the server: %q", m.headerView())
	}
}

func TestEventsRouteToTheirSession(t *testing.T) {
	m, a, b := newTwoServers(t)
	msg := `{"message_id":"9","log_id":"9","room_id":"lobby","from":{"user_id":"x","name":"X"},"body":{"text":"hi"}}`
	m.onEvent(b, client.Event{Method: "message", Params: json.RawMessage(msg)})
	if r := b.store.Room("lobby"); r == nil || len(r.Order) != 1 {
		t.Fatal("message not applied to server two")
	}
	if a.store.Room("lobby") != nil {
		t.Fatal("message leaked into server one")
	}
	if m.chat.roomID != "general" {
		t.Fatalf("a background message changed the open room to %q", m.chat.roomID)
	}
}

func TestBackgroundRoomListKeepsOpenRoom(t *testing.T) {
	m, _, b := newTwoServers(t)
	raw, _ := json.Marshal(protocol.RoomListResult{Joined: []protocol.Room{{RoomID: "lobby"}, {RoomID: "dev"}}})
	m = update(t, m, requestDoneMsg{sid: b.id, tag: "room_list", result: raw})
	if m.chat.roomID != "general" {
		t.Fatalf("open room = %q, want general", m.chat.roomID)
	}
	if b.store.Room("dev") == nil {
		t.Fatal("room list not applied to server two")
	}
}

func TestCollapseHidesRooms(t *testing.T) {
	m, a, _ := newTwoServers(t)
	m.cursorTo(a.id, "", true)
	m = update(t, m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	if !a.collapsed {
		t.Fatal("space on a header should collapse it")
	}
	for _, it := range m.chat.roomItems {
		if it.sid == a.id && !it.header {
			t.Fatal("collapsed server still lists rooms")
		}
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if a.collapsed {
		t.Fatal("enter on a signed-in header should expand it")
	}
}

func TestAddServerShortcutAndCancel(t *testing.T) {
	m, _, _ := newTwoServers(t)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.screen != screenAuth || m.auth.phase != phaseServer {
		t.Fatalf("a should open the server prompt: screen=%v phase=%v", m.screen, m.auth.phase)
	}
	if !strings.Contains(m.View(), "esc: cancel") {
		t.Fatal("add-server prompt should offer esc")
	}
	m.auth.server.SetValue("wss://three.example/")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.sessions) != 3 || m.auth.sess == nil || m.auth.sess.saved {
		t.Fatalf("enter should create an unsaved session: %d sessions", len(m.sessions))
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenChat || len(m.sessions) != 2 {
		t.Fatalf("esc should drop the new server and return: screen=%v sessions=%d", m.screen, len(m.sessions))
	}
}

func TestAddedServerSavedAfterSignIn(t *testing.T) {
	lipgloss.SetHasDarkBackground(true)
	path := t.TempDir() + "/cfg.json"
	m := NewModel(config.Config{}, path, "", "", "")
	if m.screen != screenAuth {
		t.Fatal("with no servers the app should open the add-server prompt")
	}
	m.auth.server.SetValue("wss://new.example/")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	s := m.auth.sess
	if s == nil {
		t.Fatal("no session for the new server")
	}
	if saved, _ := config.Load(path); len(saved.Servers) != 0 {
		t.Fatal("server saved before sign-in")
	}
	m.finishAuth(s, protocol.AuthResult{You: &protocol.User{UserID: "u", Name: "Ada"}, Token: "st_new"})
	if m.screen != screenChat || !s.saved || m.chat.sid != s.id {
		t.Fatalf("sign-in should open the chat on the new server: screen=%v saved=%v", m.screen, s.saved)
	}
	saved, _ := config.Load(path)
	if len(saved.Servers) != 1 || saved.Servers[0].Token != "st_new" {
		t.Fatalf("saved = %+v", saved.Servers)
	}
}

func TestRemoveServerAsksFirst(t *testing.T) {
	m, a, b := newTwoServers(t)
	m.cursorTo(b.id, "", true)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if len(m.sessions) != 2 {
		t.Fatal("n must not remove the server")
	}
	m.cursorTo(b.id, "", true)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if len(m.sessions) != 1 || m.sessions[0] != a {
		t.Fatalf("sessions after remove = %d", len(m.sessions))
	}
	if m.cfg.Find("wss://two.example/") >= 0 {
		t.Fatal("removed server still in config")
	}
}

func TestNeedsSignInHeaderOpensChooser(t *testing.T) {
	m, _, b := newTwoServers(t)
	b.authed = false
	b.cfg.Token = ""
	b.store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"email"}})
	b.client = &client.Client{}
	m.onGreeting(b)
	if !b.needsSignIn || m.screen != screenChat {
		t.Fatalf("no usable scheme should mark the server, not jump to sign-in: needs=%v screen=%v", b.needsSignIn, m.screen)
	}
	m.refreshRoomItems()
	m.cursorTo(b.id, "", true)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenAuth || m.auth.phase != phaseChoose || m.auth.sess != b {
		t.Fatalf("enter should open the chooser for that server: screen=%v phase=%v", m.screen, m.auth.phase)
	}
	b.client = nil
}

func TestAddServerAcceptsHTTPSAndBareHost(t *testing.T) {
	m, _, _ := newTwoServers(t)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m.auth.server.SetValue("https://Three.example")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.auth.sess == nil || m.auth.sess.cfg.URL != "wss://three.example/" {
		t.Fatalf("https URL not normalized: %+v", m.auth.sess)
	}

	// The same server typed another way is recognized as already added.
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m.auth.server.SetValue("one.example")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.auth.errMsg, "already signed in") {
		t.Fatalf("bare host should match the saved wss server, errMsg=%q", m.auth.errMsg)
	}

	m.auth.server.SetValue("ftp://nope.example/")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.auth.errMsg, "unsupported scheme") {
		t.Fatalf("errMsg = %q, want unsupported scheme", m.auth.errMsg)
	}
}
