package ui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/stevedylandev/whisk/internal/client"
	"github.com/stevedylandev/whisk/internal/protocol"
)

func liveMessage(t *testing.T, m *Model, msg protocol.Message) {
	t.Helper()
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	m.onEvent(m.cur(), client.Event{Method: "message", Params: raw})
}

// TestUnreadMarksOtherRooms: a live message in a room that is not open is
// unread, a mention is flagged, and opening the room clears it.
func TestUnreadMarksOtherRooms(t *testing.T) {
	m := newTestChat(t, 1)
	m.cur().store.ApplyRoomList(&protocol.RoomListResult{Joined: []protocol.Room{
		{RoomID: "general", Title: "General"}, {RoomID: "dev", Title: "dev"},
	}})
	m.refreshRoomItems()

	liveMessage(t, &m, protocol.Message{MessageID: "900", LogID: "900", RoomID: "general",
		From: &protocol.User{UserID: "u2", Name: "Bob"}, Body: &protocol.Body{Text: "in the open room"}})
	liveMessage(t, &m, protocol.Message{MessageID: "901", LogID: "901", RoomID: "dev",
		From: &protocol.User{UserID: "u2", Name: "Bob"}, Body: &protocol.Body{Text: "hi"}})
	liveMessage(t, &m, protocol.Message{MessageID: "902", LogID: "902", RoomID: "dev",
		From: &protocol.User{UserID: "u2", Name: "Bob"}, Body: &protocol.Body{Text: "ping @u1"}})
	liveMessage(t, &m, protocol.Message{MessageID: "903", LogID: "903", RoomID: "dev",
		From: &protocol.User{UserID: "u1", Name: "Ada"}, Body: &protocol.Body{Text: "mine"}})

	if u := m.cur().unread["general"]; u != nil {
		t.Fatalf("open room marked unread: %+v", u)
	}
	u := m.cur().unread["dev"]
	if u == nil || u.count != 2 || !u.mention {
		t.Fatalf("dev unread = %+v, want 2 with mention", u)
	}
	if !strings.Contains(ansi.Strip(m.sidebarView()), "@2") {
		t.Fatalf("sidebar missing mention badge:\n%s", ansi.Strip(m.sidebarView()))
	}

	m.selectRoom(m.cur(), "dev")
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if u := m.cur().unread["dev"]; u != nil {
		t.Fatalf("opening the room did not clear unread: %+v", u)
	}
}

// TestGroupedMessagesShareHeader: consecutive messages from one author show
// the author once.
func TestGroupedMessagesShareHeader(t *testing.T) {
	m := newTestChat(t, 3)
	m.chat.dirty = true
	m.renderContent()
	content := ansi.Strip(m.chat.viewport.View())
	if n := strings.Count(content, "Ada"); n != 1 {
		t.Fatalf("author shown %d times, want 1:\n%s", n, content)
	}
	for _, want := range []string{"message 0", "message 1", "message 2"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q:\n%s", want, content)
		}
	}
}

// TestEscCancelsReply: esc in the composer drops a reply before leaving the
// composer, and the context line comes and goes with it.
func TestEscCancelsReply(t *testing.T) {
	m := newTestChat(t, 2)
	m.chat.dirty = true
	m.renderContent()
	m.setFocus(focusMessages)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if m.chat.replyTo == "" {
		t.Fatal("r did not start a reply")
	}
	if !strings.Contains(m.View(), "↳ replying to Ada") {
		t.Fatal("reply context line missing")
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.chat.replyTo != "" || m.chat.focus != focusComposer {
		t.Fatalf("esc: replyTo=%q focus=%v, want reply cleared and composer kept", m.chat.replyTo, m.chat.focus)
	}
	if strings.Contains(m.View(), "replying to") {
		t.Fatal("reply context line still shown")
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.chat.focus != focusMessages {
		t.Fatalf("second esc: focus=%v, want messages", m.chat.focus)
	}
}

// TestBottomBarKeepsStatus: help and status share the last row, and the
// status is never cut to fit the help.
func TestBottomBarKeepsStatus(t *testing.T) {
	m := newTestChat(t, 1)
	m.setStatus("connected as Ada", false)
	bar := ansi.Strip(m.bottomBar())
	if !strings.HasSuffix(strings.TrimRight(bar, " "), "connected as Ada") {
		t.Fatalf("status not at the right of the bar: %q", bar)
	}
	if w := ansi.StringWidth(m.bottomBar()); w > m.width {
		t.Fatalf("bar width %d exceeds %d", w, m.width)
	}
}

// TestAuthChooserCursor: arrows move the chooser cursor and enter picks the
// method under it.
func TestAuthChooserCursor(t *testing.T) {
	m := NewModel(testCfg(), t.TempDir()+"/cfg.json", "", "", "")
	m.screen = screenAuth
	m.auth.sess = m.cur()
	m.cur().store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest", "email"}})
	m.loadAuthChoices(m.cur())
	m.auth.phase = phaseChoose
	if m.auth.choice != 0 {
		t.Fatalf("cursor starts at %d, want the default (guest, 0)", m.auth.choice)
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.auth.phase != phasePrompt || m.auth.promptKind != promptEmail {
		t.Fatalf("enter on email: phase=%v kind=%v", m.auth.phase, m.auth.promptKind)
	}
}

// TestHelpModal: ? opens the help popup over the chat without changing the
// frame size, esc closes it, and ? typed in the composer stays text.
func TestHelpModal(t *testing.T) {
	m := newTestChat(t, 3)
	m.chat.dirty = true
	m.renderContent()
	m.setFocus(focusMessages)
	rows := len(strings.Split(m.View(), "\n"))

	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if !m.chat.showHelp {
		t.Fatal("? did not open help")
	}
	v := m.View()
	if !strings.Contains(v, "select a room") || !strings.Contains(v, "esc close") {
		t.Fatalf("help content missing:\n%s", ansi.Strip(v))
	}
	lines := strings.Split(v, "\n")
	if len(lines) != rows {
		t.Fatalf("help changed the frame height: %d, want %d", len(lines), rows)
	}
	for i, ln := range lines {
		if w := ansi.StringWidth(ln); w > m.width {
			t.Fatalf("line %d width %d exceeds %d", i, w, m.width)
		}
	}
	// Keys go to the modal, not the panes underneath.
	sel := m.chat.selectedID
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if m.chat.selectedID != sel {
		t.Fatal("key leaked through the help modal")
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.chat.showHelp {
		t.Fatal("esc did not close help")
	}

	m.setFocus(focusComposer)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if m.chat.showHelp || m.composerText() != "?" {
		t.Fatalf("? in the composer: showHelp=%v text=%q", m.chat.showHelp, m.composerText())
	}
}

// TestHelpCommandOpensModal: /help opens the popup with commands first.
func TestHelpCommandOpensModal(t *testing.T) {
	m := newTestChat(t, 1)
	m.dispatchCommand("/help", "")
	if !m.chat.showHelp || !m.chat.helpCommands {
		t.Fatalf("/help: showHelp=%v commands=%v", m.chat.showHelp, m.chat.helpCommands)
	}
	if !strings.HasPrefix(ansi.Strip(m.helpContent()), "commands") {
		t.Fatal("/help should list commands first")
	}
}

// withCommandCap gives the test server the command capability and marks the
// session connected, so /help asks the server.
func withCommandCap(t *testing.T, m *Model) {
	t.Helper()
	m.cur().store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}, Capabilities: []string{"rooms", "history", "command"}})
	m.cur().client = &client.Client{}
}

// TestHelpShowsServerCommands: /help asks the server, claims its ~private
// reply for the modal instead of the chat, and marks commands the client
// handles itself.
func TestHelpShowsServerCommands(t *testing.T) {
	m := newTestChat(t, 1)
	withCommandCap(t, &m)
	if cmd := m.dispatchCommand("/help", ""); cmd == nil {
		t.Fatal("/help did not ask the server")
	}
	if !strings.Contains(ansi.Strip(m.helpContent()), "loading…") {
		t.Fatal("server section should show loading while pending")
	}

	liveMessage(t, &m, protocol.Message{RoomID: "general",
		From: &protocol.User{UserID: "~private", Name: "Only you"},
		Body: &protocol.Body{Text: "- `/kick @user [reason]`: remove someone\n- `/join #ID`: server join", Format: "markdown"}})

	if n := len(m.cur().store.RoomNotices("general")); n != 0 {
		t.Fatalf("claimed help reply also shown in chat (%d notices)", n)
	}
	got := ansi.Strip(m.helpContent())
	if !strings.Contains(got, "server commands") || !strings.Contains(got, "/kick @user [reason]") {
		t.Fatalf("server commands missing:\n%s", got)
	}
	if !strings.Contains(got, "use /server join") || strings.Contains(got, "use /server kick") {
		t.Fatalf("shadow marking wrong:\n%s", got)
	}

	// Later notices are no longer claimed, and the reply is not re-fetched.
	liveMessage(t, &m, protocol.Message{RoomID: "general",
		From: &protocol.User{UserID: "~private", Name: "Only you"}, Body: &protocol.Body{Text: "later"}})
	if n := len(m.cur().store.RoomNotices("general")); n != 1 {
		t.Fatalf("later notice: %d notices, want 1", n)
	}
	m.chat.showHelp = false
	if cmd := m.dispatchCommand("/help", ""); cmd != nil {
		t.Fatal("/help re-fetched a cached reply")
	}
}

// TestServerHelpFailures: an error result or a result with no notice ends
// the fetch with a message in the modal.
func TestServerHelpFailures(t *testing.T) {
	m := newTestChat(t, 1)
	withCommandCap(t, &m)
	m.dispatchCommand("/help", "")
	m.handleRequestDone(m.cur(), requestDoneMsg{sid: m.cur().id, tag: "server_help",
		rpcErr: &protocol.Error{Code: -32601, Message: "unknown command"}})
	if got := ansi.Strip(m.helpContent()); !strings.Contains(got, "unknown command") {
		t.Fatalf("error not shown:\n%s", got)
	}

	m = newTestChat(t, 1)
	withCommandCap(t, &m)
	m.dispatchCommand("/help", "")
	if cmd := m.handleRequestDone(m.cur(), requestDoneMsg{sid: m.cur().id, tag: "server_help", result: []byte("{}")}); cmd == nil {
		t.Fatal("success without notice should start a grace timer")
	}
	m.onServerHelpTimeout(m.cur(), m.cur().serverHelp.gen-1)
	if m.cur().serverHelp.state != helpPending {
		t.Fatal("stale timeout ended the fetch")
	}
	m.onServerHelpTimeout(m.cur(), m.cur().serverHelp.gen)
	if got := ansi.Strip(m.helpContent()); !strings.Contains(got, "no command list") {
		t.Fatalf("timeout not shown:\n%s", got)
	}
}

// TestServerCommandRouting: /server sends the rest as a server command, even
// for a name the client handles.
func TestServerCommandRouting(t *testing.T) {
	m := newTestChat(t, 1)
	if m.dispatchCommand("/server join #x", "") != nil || !m.isErr {
		t.Fatal("/server without the command capability should fail")
	}
	withCommandCap(t, &m)
	for _, in := range []string{"/server", "/s  "} {
		m.status = ""
		if m.dispatchCommand(in, "") != nil || !strings.Contains(m.status, "usage") {
			t.Fatalf("%q: want usage, got %q", in, m.status)
		}
	}
	for _, in := range []string{"/server join #x", "/s /join #x"} {
		if m.dispatchCommand(in, "") == nil {
			t.Fatalf("%q did not send a command", in)
		}
		if got := serverCommandText(in); got != "/join #x" {
			t.Fatalf("%q sends %q, want /join #x", in, got)
		}
	}
	if got := serverCommandText("/server kick @bob  being  rude"); got != "/kick @bob  being  rude" {
		t.Fatalf("arguments not kept as typed: %q", got)
	}
}
