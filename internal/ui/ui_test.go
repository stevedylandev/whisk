package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/stevedylandev/whisk/internal/config"
	"github.com/stevedylandev/whisk/internal/protocol"
)

// testCfg is a config with one saved server, so models start on the chat
// screen with a session to work on.
func testCfg() config.Config {
	return config.Config{Servers: []config.Server{{URL: "wss://test.example/"}}}
}

func update(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	nm, _ := m.Update(msg)
	return nm.(Model)
}

func TestAuthScreenRenders(t *testing.T) {
	lipgloss.SetHasDarkBackground(true)

	m := NewModel(config.Config{}, t.TempDir()+"/cfg.json", "", "", "")
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	v := m.View()

	if !strings.Contains(v, "whisk") {
		t.Fatalf("auth screen missing title: %q", v)
	}
	if !strings.Contains(v, "server:") {
		t.Fatalf("auth screen missing server field: %q", v)
	}
}

func TestAuthEmptyServerShowsError(t *testing.T) {
	lipgloss.SetHasDarkBackground(true)

	m := NewModel(config.Config{}, t.TempDir()+"/cfg.json", "", "", "")
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.auth.errMsg == "" {
		t.Fatal("expected an error for empty server")
	}
	if m.auth.connecting {
		t.Fatal("must not connect with an empty server")
	}
}

func TestToggleEmoji(t *testing.T) {
	set := []string{"👍"}
	set = toggleEmoji(set, "🎉")
	if len(set) != 2 || set[0] != "👍" || set[1] != "🎉" {
		t.Fatalf("after add = %v", set)
	}
	set = toggleEmoji(set, "👍")
	if len(set) != 1 || set[0] != "🎉" {
		t.Fatalf("after remove = %v", set)
	}
}

func TestRenderContentBuildsSelection(t *testing.T) {
	lipgloss.SetHasDarkBackground(true)

	m := NewModel(testCfg(), t.TempDir()+"/cfg.json", "", "", "")
	m.screen = screenChat
	m.cur().authed = true
	m.cur().store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}, Capabilities: []string{"rooms", "history"}})
	m.cur().store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Ada"}})
	m.cur().store.ApplyMessage(&protocol.Message{
		MessageID: "100", LogID: "100", RoomID: "general",
		From: &protocol.User{UserID: "u1", Name: "Ada"},
		Body: &protocol.Body{Text: "hello"},
	})
	m.cur().store.ApplyMessage(&protocol.Message{
		MessageID: "200", LogID: "200", RoomID: "general",
		From: &protocol.User{UserID: "u2", Name: "Bob"},
		Body: &protocol.Body{Text: "world"},
	})
	m.chat.roomID = "general"
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.renderContent()

	if len(m.chat.msgPos) != 2 {
		t.Fatalf("msgPos = %d, want 2", len(m.chat.msgPos))
	}
	// A fresh selection defaults to the last message.
	if m.chat.selectedID != "200" {
		t.Fatalf("selectedID = %q, want 200", m.chat.selectedID)
	}

	// Moving selection up targets the first message.
	m.moveSelection(-1)
	if m.chat.selectedID != "100" {
		t.Fatalf("after moveSelection(-1) selectedID = %q, want 100", m.chat.selectedID)
	}
}

// TestViewFitsWindow guards against fixed rows (header/help/status/composer)
// wrapping: a frame taller or wider than the terminal scrolls the alt screen
// and pushes the header off the top.
func TestViewFitsWindow(t *testing.T) {
	lipgloss.SetHasDarkBackground(true)
	for _, w := range []int{50, 60, 80, 100, 120} {
		for _, h := range []int{15, 20, 24, 40} {
			m := NewModel(testCfg(), t.TempDir()+"/cfg.json", "", "", "")
			m.screen = screenChat
			m.cur().authed = true
			m.cur().store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}, Capabilities: []string{"rooms", "history"}})
			m.cur().store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Ada"}})
			// Many rooms, so the sidebar is taller than the pane at short heights.
			var rooms []protocol.Room
			for i := 0; i < 30; i++ {
				rooms = append(rooms, protocol.Room{RoomID: fmt.Sprintf("r%d", i), Title: fmt.Sprintf("Room %d", i)})
			}
			rooms[0] = protocol.Room{RoomID: "general", Title: "General"}
			m.cur().store.ApplyRoomList(&protocol.RoomListResult{Joined: rooms})
			m = update(t, m, tea.WindowSizeMsg{Width: w, Height: h})
			m.chat.roomID = "general"
			m.cur().store.Room("general").Loaded = true
			m.cur().store.Room("general").Loading = false
			for i := 0; i < 40; i++ {
				id := fmt.Sprintf("%d", 100+i)
				m.cur().store.ApplyMessage(&protocol.Message{
					MessageID: id, LogID: id, RoomID: "general",
					From: &protocol.User{UserID: "u1", Name: "Ada"},
					Body: &protocol.Body{Text: fmt.Sprintf("message %d", i)},
				})
			}
			m.status = "connected as Ada — a deliberately long status line that must be clipped"

			for _, f := range []focusMode{focusRooms, focusMessages, focusComposer} {
				m.chat.focus = f
				m.chat.dirty = true
				m.renderContent()

				// Check both the bottom and the top of the loaded history.
				for _, pos := range []string{"bottom", "top"} {
					if pos == "top" {
						m.chat.viewport.GotoTop()
						m.selectMessageAtTop()
						m.renderContent()
					}
					v := m.View()
					lines := strings.Split(v, "\n")
					// Must leave a spare row: Bubble Tea drops lines from the top
					// when the frame reaches the full terminal height.
					if len(lines) >= h {
						t.Fatalf("w=%d h=%d focus=%v %s: %d lines fill the terminal (need a spare row)", w, h, f, pos, len(lines))
					}
					if !strings.Contains(lines[0], "whisk") {
						t.Fatalf("w=%d h=%d focus=%v %s: header missing from first line: %q", w, h, f, pos, lines[0])
					}
					for i, ln := range lines {
						if lw := lipgloss.Width(ln); lw > w {
							t.Fatalf("w=%d h=%d focus=%v %s: line %d width %d exceeds %d", w, h, f, pos, i, lw, w)
						}
					}
				}
			}
		}
	}
}

// TestComposerKeepsLetterKeys guards against global key bindings stealing
// letters while the user is typing (e.g. "N" toggling notices instead of
// typing "Nice").
func TestComposerKeepsLetterKeys(t *testing.T) {
	lipgloss.SetHasDarkBackground(true)
	m := NewModel(testCfg(), t.TempDir()+"/cfg.json", "", "", "")
	m.screen = screenChat
	m.cur().authed = true
	m.width, m.height = 80, 24
	m.resize()
	m.setFocus(focusComposer)

	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})
	if m.chat.showNotices {
		t.Fatal("typing N in the composer must not toggle the notices overlay")
	}
	if got := m.chat.composer.Value(); got != "N" {
		t.Fatalf("composer value = %q, want N", got)
	}

	// The disconnected-reconnect key must not swallow a typed letter either.
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if got := m.chat.composer.Value(); got != "Nr" {
		t.Fatalf("composer value = %q, want Nr", got)
	}

	// Elsewhere, N still toggles the overlay.
	m.setFocus(focusMessages)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})
	if !m.chat.showNotices {
		t.Fatal("N outside the composer should toggle the notices overlay")
	}
}

// TestComposerSitsUnderMessages checks the chat layout: the composer is in the
// message column, and the sidebar extends down alongside it to the same bottom
// border.
func TestComposerSitsUnderMessages(t *testing.T) {
	lipgloss.SetHasDarkBackground(true)
	m := NewModel(testCfg(), t.TempDir()+"/cfg.json", "", "", "")
	m.screen = screenChat
	m.cur().authed = true
	m.cur().store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}, Capabilities: []string{"rooms"}})
	m.cur().store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Ada"}})
	m.cur().store.ApplyRoomList(&protocol.RoomListResult{
		Joined: []protocol.Room{{RoomID: "general", Title: "General"}},
	})
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.refreshRoomItems()
	m.chat.roomID = "general"
	m.cur().store.Room("general").Loaded = true
	m.chat.dirty = true
	m.renderContent()

	lines := strings.Split(m.View(), "\n")
	composerLine := -1
	for i, ln := range lines {
		if strings.Contains(ln, "message #") {
			composerLine = i
			break
		}
	}
	if composerLine < 0 {
		t.Fatal("composer not found in the view")
	}
	// With a one-line composer, the next row is its bottom border. The
	// sidebar's bottom border must be on that same row.
	if composerLine+1 >= len(lines) {
		t.Fatal("missing composer bottom border")
	}
	bottom := lines[composerLine+1]
	if !strings.Contains(bottom, "╰") {
		t.Fatalf("composer bottom border missing: %q", bottom)
	}
	if !strings.Contains(bottom, "└") {
		t.Fatalf("sidebar does not extend to the composer bottom: %q", bottom)
	}
}

// TestComposerGrowsWithText checks that the composer grows upward as the user
// adds lines or a long line wraps, shrinking the message view to make room.
func TestComposerGrowsWithText(t *testing.T) {
	lipgloss.SetHasDarkBackground(true)
	m := NewModel(testCfg(), t.TempDir()+"/cfg.json", "", "", "")
	m.screen = screenChat
	m.cur().authed = true
	m.cur().store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}, Capabilities: []string{"rooms"}})
	m.cur().store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Ada"}})
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.setFocus(focusComposer)

	if got := m.chat.composer.Height(); got != 1 {
		t.Fatalf("empty composer height = %d, want 1", got)
	}
	base := m.chat.viewport.Height

	// alt+enter starts a second line: the composer takes a row from the view.
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if got := m.chat.composer.Height(); got != 2 {
		t.Fatalf("after alt+enter composer height = %d, want 2", got)
	}
	if got := m.chat.viewport.Height; got != base-1 {
		t.Fatalf("after alt+enter viewport height = %d, want %d", got, base-1)
	}

	// A line long enough to wrap grows the composer again, without newlines.
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("x", 200))})
	if got := m.chat.composer.Height(); got <= 2 {
		t.Fatalf("after long line composer height = %d, want > 2", got)
	}
	if got := m.chat.viewport.Height; got >= base-1 {
		t.Fatalf("after long line viewport height = %d, want < %d", got, base-1)
	}
	if lines := strings.Split(m.View(), "\n"); len(lines) >= 24 {
		t.Fatalf("grown composer overflows the window: %d lines", len(lines))
	}

	// Past the cap the composer scrolls instead of eating the message view.
	for i := 0; i < 20; i++ {
		m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	}
	if got := m.chat.composer.Height(); got != composerMaxHeight {
		t.Fatalf("composer height at cap = %d, want %d", got, composerMaxHeight)
	}
	if got := m.chat.viewport.Height; got < 1 {
		t.Fatalf("viewport height at cap = %d, want >= 1", got)
	}

	// Clearing the text shrinks the composer back and restores the view.
	m.chat.composer.Reset()
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if got := m.chat.composer.Height(); got != 1 {
		t.Fatalf("after clear composer height = %d, want 1", got)
	}
	if got := m.chat.viewport.Height; got != base {
		t.Fatalf("after clear viewport height = %d, want %d", got, base)
	}
}

func TestChatScreenRenders(t *testing.T) {
	lipgloss.SetHasDarkBackground(true)

	m := NewModel(testCfg(), t.TempDir()+"/cfg.json", "", "", "")
	m.screen = screenChat
	m.cur().authed = true
	m.cur().store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}, Capabilities: []string{"rooms", "history"}})
	m.cur().store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Ada"}})
	m.cur().store.ApplyRoomList(&protocol.RoomListResult{
		Joined: []protocol.Room{{RoomID: "general", Title: "General"}},
	})
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.refreshRoomItems()
	m.selectDefaultRoom(m.cur())
	m.chat.dirty = true
	m.renderContent()
	v := m.View()

	if !strings.Contains(v, "whisk") {
		t.Fatalf("chat screen missing header: %q", v)
	}
	if !strings.Contains(v, "rooms") {
		t.Fatalf("chat screen missing rooms sidebar: %q", v)
	}
	if !strings.Contains(v, "General") {
		t.Fatalf("chat screen missing room title: %q", v)
	}
}

// newTestChat builds a signed-in chat model on a loaded room with n messages.
func newTestChat(t *testing.T, n int) Model {
	t.Helper()
	lipgloss.SetHasDarkBackground(true)

	m := NewModel(testCfg(), t.TempDir()+"/cfg.json", "", "", "")
	m.screen = screenChat
	m.cur().authed = true
	m.cur().store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}, Capabilities: []string{"rooms", "history"}})
	m.cur().store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Ada"}})
	m.cur().store.ApplyRoomList(&protocol.RoomListResult{Joined: []protocol.Room{{RoomID: "general", Title: "General"}}})
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.chat.roomID = "general"
	m.cur().store.Room("general").Loaded = true
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%d", 100+i)
		m.cur().store.ApplyMessage(&protocol.Message{
			MessageID: id, LogID: id, RoomID: "general",
			From: &protocol.User{UserID: "u1", Name: "Ada"},
			Body: &protocol.Body{Text: fmt.Sprintf("message %d", i)},
		})
	}
	return m
}

func TestChatStartsOnRoomSidebar(t *testing.T) {
	m := NewModel(testCfg(), t.TempDir()+"/cfg.json", "", "", "")
	if m.chat.focus != focusRooms {
		t.Fatalf("initial focus = %v, want focusRooms", m.chat.focus)
	}
}

// newRoomSidebar builds a signed-in model with two joined rooms and one room
// that still needs joining.
func newRoomSidebar(t *testing.T) Model {
	t.Helper()
	lipgloss.SetHasDarkBackground(true)

	m := NewModel(testCfg(), t.TempDir()+"/cfg.json", "", "", "")
	m.screen = screenChat
	m.cur().authed = true
	m.cur().store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}, Capabilities: []string{"rooms"}})
	m.cur().store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "u1", Name: "Ada"}})
	m.cur().store.ApplyRoomList(&protocol.RoomListResult{
		Joined: []protocol.Room{
			{RoomID: "general", Title: "General"},
			{RoomID: "random", Title: "Random"},
		},
		NotJoined: []protocol.Room{{RoomID: "secret", Title: "Secret"}},
	})
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.refreshRoomItems()
	m.chat.roomID = "general"
	m.cursorTo(m.cur().id, "general", false)
	m.cur().store.Room("general").Loaded = true
	m.cur().store.Room("random").Loaded = true
	m.setFocus(focusRooms)
	return m
}

// TestRoomNavigationOpensJoinedRoom checks that moving through the sidebar
// switches the chat automatically, and that only unjoined rooms wait for enter.
func TestRoomNavigationOpensJoinedRoom(t *testing.T) {
	m := newRoomSidebar(t)

	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if m.chat.roomID != "random" {
		t.Fatalf("room after j = %q, want random", m.chat.roomID)
	}
	if m.chat.focus != focusRooms {
		t.Fatal("navigating rooms must keep focus on the sidebar")
	}

	// Enter on a joined room hands focus to the messages, not the composer.
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.chat.focus != focusMessages {
		t.Fatalf("enter on a joined room focused %v, want focusMessages", m.chat.focus)
	}

	// Moving onto a not-joined room leaves the open chat alone.
	m.setFocus(focusRooms)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if m.chat.roomID != "random" {
		t.Fatalf("room after moving to an unjoined room = %q, want random", m.chat.roomID)
	}
	if m.chat.focus != focusRooms {
		t.Fatal("focus must stay on the sidebar until the room is joined")
	}
}

// TestLeaveHotkeyRemoved: l no longer leaves a room; /leave is the way.
func TestLeaveHotkeyRemoved(t *testing.T) {
	m := newRoomSidebar(t)
	m.status = ""

	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if m.status != "" {
		t.Fatalf("l changed status to %q; the leave hotkey should be gone", m.status)
	}
	if m.chat.roomID != "general" {
		t.Fatalf("l changed the open room to %q", m.chat.roomID)
	}
}

// TestThreadsNestUnderParentRoom checks that a thread is listed directly under
// its parent, even when the server returns it after an unrelated room, and
// that an orphan thread stays reachable as a top-level row.
func TestThreadsNestUnderParentRoom(t *testing.T) {
	m := newRoomSidebar(t)
	m.cur().store.ApplyRoomList(&protocol.RoomListResult{Joined: []protocol.Room{
		{RoomID: "general", Title: "General"},
		{RoomID: "random", Title: "Random"},
		{RoomID: "deploy", Title: "Deploy", ParentRoomID: "general"},
		{RoomID: "lunch", Title: "Lunch", ParentRoomID: "random"},
		{RoomID: "stray", Title: "Stray", ParentRoomID: "missing"},
	}})
	m.refreshRoomItems()

	index := func(id string) int {
		t.Helper()
		for i, it := range m.chat.roomItems {
			if !it.header && it.id == id {
				return i
			}
		}
		t.Fatalf("room %q missing from the sidebar", id)
		return -1
	}
	depth := func(id string) int {
		return m.chat.roomItems[index(id)].depth
	}

	if got, want := index("deploy"), index("general")+1; got != want {
		t.Fatalf("deploy row = %d, want directly under general at %d", got, want)
	}
	if got, want := index("lunch"), index("random")+1; got != want {
		t.Fatalf("lunch row = %d, want directly under random at %d", got, want)
	}
	if depth("general") != 0 || depth("deploy") != 1 || depth("lunch") != 1 {
		t.Fatalf("depths: general=%d deploy=%d lunch=%d, want 0/1/1",
			depth("general"), depth("deploy"), depth("lunch"))
	}
	if depth("stray") != 0 {
		t.Fatalf("orphan thread depth = %d, want 0", depth("stray"))
	}

	// Rendering reflects the nesting: the thread row is indented and still
	// carries the thread marker.
	view := m.sidebarView()
	var deploy string
	for _, ln := range strings.Split(view, "\n") {
		if strings.Contains(ln, "Deploy") {
			deploy = ln
		}
	}
	if !strings.Contains(deploy, "  ↳ Deploy") {
		t.Fatalf("deploy row not indented under general: %q", deploy)
	}
}

// TestPrivateRoomsHaveOwnSection checks that private rooms sit under their own
// subheader, and that a one-to-one private room shows the other person's name
// rather than the server title that names both people.
func TestPrivateRoomsHaveOwnSection(t *testing.T) {
	m := newRoomSidebar(t)
	m.cur().store.ApplyRoomList(&protocol.RoomListResult{
		Joined: []protocol.Room{
			{RoomID: "general", Title: "General"},
			{
				RoomID: "dm-bob", Title: "Ada & Bob", Private: true,
				Members: []protocol.User{
					{UserID: "u1", Name: "Ada"},
					{UserID: "u2", Name: "Bob"},
				},
			},
			{
				RoomID: "team", Title: "Team", Private: true,
				Members: []protocol.User{
					{UserID: "u1", Name: "Ada"},
					{UserID: "u2", Name: "Bob"},
					{UserID: "u3", Name: "Cy"},
				},
			},
		},
	})
	m.refreshRoomItems()

	index := func(id string) int {
		t.Helper()
		for i, it := range m.chat.roomItems {
			if it.subheader && it.label == id {
				return i
			}
			if !it.header && !it.subheader && it.id == id {
				return i
			}
		}
		t.Fatalf("row %q missing from the sidebar", id)
		return -1
	}
	if index("private") >= index("dm-bob") || index("private") >= index("team") {
		t.Fatalf("private subheader should come before its rooms: %+v", m.chat.roomItems)
	}
	if index("general") >= index("private") {
		t.Fatalf("private section should follow the public rooms: %+v", m.chat.roomItems)
	}

	view := m.sidebarView()
	for _, want := range []string{"private", "Bob", "Team"} {
		if !strings.Contains(view, want) {
			t.Fatalf("sidebar missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Ada & Bob") {
		t.Fatalf("one-to-one room should show only the peer's name:\n%s", view)
	}

	// The cursor skips the label, and the header names the peer alone.
	m.cursorTo(m.cur().id, "random", false)
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if it := m.chat.roomItems[m.chat.roomCursor]; it.subheader {
		t.Fatalf("cursor stopped on the private label: %+v", it)
	}
	m.cursorTo(m.cur().id, "dm-bob", false)
	if it := m.chat.roomItems[m.chat.roomCursor]; it.id != "dm-bob" {
		t.Fatalf("cursor on %+v, want dm-bob", it)
	}
	m.chat.roomID = "dm-bob"
	if h := m.headerView(); !strings.Contains(h, "Bob") || strings.Contains(h, "Ada & Bob") {
		t.Fatalf("header should show the peer's name: %q", h)
	}
}

// TestJoinedRoomOpensAfterRoomList checks that a room joined from the sidebar
// is opened once the follow-up room_list arrives.
func TestJoinedRoomOpensAfterRoomList(t *testing.T) {
	m := newRoomSidebar(t)
	m.cur().pendingRoom = "secret"

	res := protocol.RoomListResult{Joined: []protocol.Room{
		{RoomID: "general", Title: "General"},
		{RoomID: "random", Title: "Random"},
		{RoomID: "secret", Title: "Secret"},
	}}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	m = update(t, m, requestDoneMsg{sid: m.cur().id, tag: "room_list", result: raw})
	if m.chat.roomID != "secret" {
		t.Fatalf("room after room_list = %q, want secret", m.chat.roomID)
	}
	if m.cur().pendingRoom != "" {
		t.Fatalf("pendingRoom not cleared: %q", m.cur().pendingRoom)
	}
	// The joined room must not still appear in the sidebar twice.
	seen := 0
	for _, it := range m.chat.roomItems {
		if it.id == "secret" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("secret appears %d times in the sidebar, want 1", seen)
	}
}

// TestSelectionPassesAdjacentSameAuthorMessages reproduces the stall where an
// updated message was inserted twice into the ordering: two identical entries
// rendered next to each other and selection could not move past them.
func TestSelectionPassesAdjacentSameAuthorMessages(t *testing.T) {
	m := newTestChat(t, 4)
	m.cur().store.ApplyMessage(&protocol.Message{
		MessageID: "101", LogID: "201", RoomID: "general",
		From: &protocol.User{UserID: "u1", Name: "Ada"},
		Body: &protocol.Body{Text: "message 1 edited"},
	})
	m.setFocus(focusMessages)
	m.chat.stickBottom = true
	m.renderContent()

	if len(m.chat.msgPos) != 4 {
		t.Fatalf("rendered %d messages, want 4 (duplicate ordering entry)", len(m.chat.msgPos))
	}
	m.chat.selectedID = m.chat.msgPos[0].id
	for i := 1; i < len(m.chat.msgPos); i++ {
		before := m.selectionIndex()
		nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		m = nm.(Model)
		if got := m.selectionIndex(); got != before+1 {
			t.Fatalf("selection stuck at %d, want %d", got, before+1)
		}
	}
}

// TestDeleteAsksForConfirmation: d only arms a y/n prompt, so a stray key
// cannot tombstone a message; y sends the request and anything else cancels.
func TestDeleteAsksForConfirmation(t *testing.T) {
	m := newTestChat(t, 3)
	m.cur().store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}, Capabilities: []string{"rooms", "history", "edit"}})
	m.setFocus(focusMessages)
	m.renderContent()
	m.chat.selectedID = m.chat.msgPos[len(m.chat.msgPos)-1].id

	// d arms the prompt and sends nothing.
	if cmd := m.deleteSelected(); cmd != nil {
		t.Fatal("d sent a command before confirmation")
	}
	if m.chat.confirmDelete != "102" {
		t.Fatalf("confirmDelete = %q, want the selected message 102", m.chat.confirmDelete)
	}
	if !strings.Contains(ansi.Strip(m.statusLine()), "(y/n)") {
		t.Fatalf("status = %q, want a y/n prompt", m.status)
	}

	// Any other key cancels and sends nothing.
	if cmds := m.updateChat(keyMsg("n")); len(cmds) != 0 || m.chat.confirmDelete != "" {
		t.Fatalf("n: %d commands, confirmDelete=%q; want a cancel", len(cmds), m.chat.confirmDelete)
	}

	// y confirms with one message request.
	m.deleteSelected()
	cmds := m.updateChat(keyMsg("y"))
	if len(cmds) != 1 {
		t.Fatalf("y sent %d commands, want 1", len(cmds))
	}
	raw := cmds[0]()
	done, ok := raw.(requestDoneMsg)
	if !ok || done.tag != "message" {
		t.Fatalf("y sent %T, want a message request", raw)
	}
	if m.chat.confirmDelete != "" {
		t.Fatalf("confirmDelete = %q after y, want cleared", m.chat.confirmDelete)
	}
}

func keyMsg(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// TestTextLinks finds the links in a message body, trimming the punctuation
// that belongs to the sentence or markdown wrapper rather than the URL.
func TestTextLinks(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"see https://example.com/a.", []string{"https://example.com/a"}},
		{"[docs](https://example.com/docs)", []string{"https://example.com/docs"}},
		{"one http://a.test/x, two https://b.test/y!", []string{"http://a.test/x", "https://b.test/y"}},
		{"https://en.wikipedia.org/wiki/Foo_(bar)", []string{"https://en.wikipedia.org/wiki/Foo_(bar)"}},
		{`"https://example.com/q"`, []string{"https://example.com/q"}},
		{"no links here", nil},
		{"javascript:alert(1)", nil},
	}
	for _, c := range cases {
		got := textLinks(c.text)
		if len(got) != len(c.want) {
			t.Fatalf("textLinks(%q) = %v, want %v", c.text, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("textLinks(%q)[%d] = %q, want %q", c.text, i, got[i], c.want[i])
			}
		}
	}
}

// TestOpenLinkInSelectedMessage: o opens the first link in the selected
// message, and says so when the message has none.
func TestOpenLinkInSelectedMessage(t *testing.T) {
	m := newTestChat(t, 2)
	m.cur().store.ApplyMessage(&protocol.Message{
		MessageID: "200", LogID: "200", RoomID: "general",
		From: &protocol.User{UserID: "u1", Name: "Ada"},
		Body: &protocol.Body{Text: "see [the docs](https://example.com/docs)."},
	})
	m.setFocus(focusMessages)
	m.chat.selectedID = "200"

	// Do not run the returned command: it would launch a real browser.
	cmds := m.updateChat(keyMsg("o"))
	if len(cmds) != 1 {
		t.Fatalf("o sent %d commands, want 1", len(cmds))
	}
	if !strings.Contains(m.status, "https://example.com/docs") {
		t.Fatalf("status = %q, want the opened link", m.status)
	}

	m.chat.selectedID = "100"
	if cmds := m.updateChat(keyMsg("o")); len(cmds) != 0 {
		t.Fatalf("o on a message without a link sent %d commands, want 0", len(cmds))
	}
	if !m.isErr || !strings.Contains(m.status, "no link") {
		t.Fatalf("status = %q (err=%v), want a no-link error", m.status, m.isErr)
	}
}

// TestFocusChangeRerendersMessages checks that moving focus in or out of the
// message pane rebuilds the content, since the selection highlight is part of
// it.
func TestFocusChangeRerendersMessages(t *testing.T) {
	m := newTestChat(t, 3)
	m.chat.focus = focusComposer
	m.chat.dirty = false

	m.setFocus(focusMessages)
	if !m.chat.dirty {
		t.Fatal("focusing messages did not request a re-render")
	}

	m.chat.dirty = false
	m.setFocus(focusComposer)
	if !m.chat.dirty {
		t.Fatal("leaving messages did not request a re-render")
	}
}

// TestRenderSelectedKeepsLineCount checks that highlighting a multi-line
// message does not change its line count or pad the lines out.
func TestRenderSelectedKeepsLineCount(t *testing.T) {
	in := "first line\n\nthird line"
	out := gutter(in, true)
	if got, want := strings.Count(out, "\n"), strings.Count(in, "\n"); got != want {
		t.Fatalf("line breaks = %d, want %d", got, want)
	}
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > 30 {
			t.Fatalf("line %d width = %d, want <= 30", i, got)
		}
	}
}

// TestLoadingShowsInStatusBar checks that history loading is reported at the
// bottom, not in the header or message content, so it cannot shift the top of
// the screen.
func TestLoadingShowsInStatusBar(t *testing.T) {
	m := newTestChat(t, 5)
	m.renderContent()

	rows := len(strings.Split(m.View(), "\n"))
	m.cur().store.Room("general").Loading = true
	m.renderContent()
	v := m.View()

	header := strings.Split(v, "\n")[0]
	if strings.Contains(header, "loading") {
		t.Fatalf("loading indicator in header: %q", header)
	}
	if strings.Contains(m.chat.viewport.View(), "loading") {
		t.Fatal("loading indicator in message content")
	}
	if !strings.Contains(v, "loading older messages…") {
		t.Fatalf("loading indicator missing from status bar: %q", v)
	}
	if got := len(strings.Split(v, "\n")); got != rows {
		t.Fatalf("view height changed while loading: %d, want %d", got, rows)
	}
}

// TestLoadOlderKeepsViewportAnchor checks that prepending an older page keeps
// the message at the top of the viewport in place.
func TestLoadOlderKeepsViewportAnchor(t *testing.T) {
	m := newTestChat(t, 35)
	room := m.cur().store.Room("general")
	room.FirstLogID = "105"
	room.MoreBack = true

	m.chat.stickBottom = true
	m.renderContent()
	m.chat.viewport.GotoTop()
	m.selectMessageAtTop()
	m.renderContent()

	anchor := m.chat.msgPos[0].id
	before := m.chat.msgPos[0].start - m.chat.viewport.YOffset
	if cmd := m.loadOlder(); cmd == nil {
		t.Fatal("loadOlder returned no command")
	}
	if m.chat.anchorID != anchor {
		t.Fatalf("anchorID = %q, want %q", m.chat.anchorID, anchor)
	}

	var older []protocol.Message
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("%d", 100+i)
		older = append(older, protocol.Message{
			MessageID: id, LogID: id, RoomID: "general",
			From: &protocol.User{UserID: "u1", Name: "Ada"},
			Body: &protocol.Body{Text: fmt.Sprintf("older %d", i)},
		})
	}
	m.cur().store.ApplyHistory("general", &protocol.HistoryResult{
		FirstLogID: "101", LatestLogID: "134", More: false, Messages: older,
	})
	m.chat.dirty = true
	m.renderContent()

	start := -1
	for _, p := range m.chat.msgPos {
		if p.id == anchor {
			start = p.start
			break
		}
	}
	if start < 0 {
		t.Fatal("anchor message missing after loading older history")
	}
	if got := start - m.chat.viewport.YOffset; got != before {
		t.Fatalf("anchor row = %d, want %d (start=%d y=%d)", got, before, start, m.chat.viewport.YOffset)
	}
}

// TestEmojiZWJSequenceKeepsBorders: "😀‍♀️" (U+1F600 U+200D U+2640 U+FE0F)
// measures as one two-cell grapheme to lipgloss, but terminals without
// grapheme clustering paint the face (two cells) then the female sign (one).
// The reaction pill's row was therefore a cell too wide and wrapped, splitting
// the pill across rows. Joiners and selectors are dropped before layout, so
// the pill stays on one row and every pane row is the same width.
func TestEmojiZWJSequenceKeepsBorders(t *testing.T) {
	m := newTestChat(t, 3)
	m.cur().store.ApplyMessage(&protocol.Message{
		MessageID: "300", LogID: "300", RoomID: "general",
		From: &protocol.User{UserID: "u2", Name: "Bo"},
		Body: &protocol.Body{Text: "welcome!"},
	})
	m.cur().store.ApplyReactions(&protocol.ReactionsParams{
		MessageID: "300",
		Reactions: []protocol.ReactionEntry{{From: protocol.User{UserID: "u2"}, Emojis: []string{"😀\u200d♀️"}}},
	})
	m.chat.dirty = true
	m.renderContent()

	view := m.View()
	if strings.Contains(view, "\u200d") {
		t.Fatal("view still carries U+200D")
	}
	lines := strings.Split(view, "\n")
	// Skip the header and bottom bar; the rows between are bordered panes.
	want := ansi.StringWidth(lines[1])
	for i, l := range lines[1 : len(lines)-1] {
		if w := ansi.StringWidth(l); w != want {
			t.Fatalf("row %d width %d, want %d:\n%s", i+1, w, want, l)
		}
	}
}

// TestEmojiVariationSelectorKeepsBorders: "❤️" (U+2764 U+FE0F) measures two
// cells to lipgloss but one in terminals without grapheme clustering, which
// tore the message pane's right border. The selector is dropped before
// layout, so every pane row is the same width either way.
func TestEmojiVariationSelectorKeepsBorders(t *testing.T) {
	m := newTestChat(t, 3)
	m.cur().store.ApplyMessage(&protocol.Message{
		MessageID: "200", LogID: "200", RoomID: "general",
		From: &protocol.User{UserID: "u2", Name: "Bo"},
		Body: &protocol.Body{Text: "love it ❤️ ✔️"},
	})
	m.cur().store.ApplyReactions(&protocol.ReactionsParams{
		MessageID: "200",
		Reactions: []protocol.ReactionEntry{{From: protocol.User{UserID: "u2"}, Emojis: []string{"❤️"}}},
	})
	m.chat.dirty = true
	m.renderContent()

	view := m.View()
	if strings.Contains(view, "\uFE0F") {
		t.Fatal("view still carries U+FE0F")
	}
	lines := strings.Split(view, "\n")
	// Skip the header and bottom bar; the rows between are bordered panes.
	want := ansi.StringWidth(lines[1])
	for i, l := range lines[1 : len(lines)-1] {
		if w := ansi.StringWidth(l); w != want {
			t.Fatalf("row %d width %d, want %d:\n%s", i+1, w, want, l)
		}
	}
}
