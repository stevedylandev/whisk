package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestResolveLocalPathExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := map[string]string{
		"~":            home,
		"~/":           home + "/",
		"~/pic.png":    home + "/pic.png",
		"/tmp/pic":     "/tmp/pic",
		"relative.txt": "relative.txt",
		"~steve/x":     "~steve/x",
	}
	for in, want := range cases {
		if got := resolveLocalPath(in); got != want {
			t.Errorf("resolveLocalPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompletePathToken(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha.txt", "alpine.txt", ".secret"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		partial   string
		want      string
		wantMatch int
	}{
		// Two matches share "alp", so only the prefix is added.
		{dir + "/al", dir + "/alp", 2},
		// A single file completes in full.
		{dir + "/alph", dir + "/alpha.txt", 1},
		// A directory completes with a trailing slash.
		{dir + "/doc", dir + "/docs/", 1},
		// Nothing matches, so the text is left alone.
		{dir + "/nope", dir + "/nope", 0},
		// Hidden entries are listed only when asked for.
		{dir + "/.", dir + "/.secret", 1},
	}
	for _, c := range cases {
		got, matches := completePathToken(c.partial)
		if got != c.want || len(matches) != c.wantMatch {
			t.Errorf("completePathToken(%q) = %q, %d matches; want %q, %d",
				c.partial, got, len(matches), c.want, c.wantMatch)
		}
	}
}

func TestCompletePathTokenKeepsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, matches := completePathToken("~/no")
	if got != "~/notes.txt" || len(matches) != 1 {
		t.Fatalf("completePathToken(~/no) = %q, %d matches; want ~/notes.txt, 1", got, len(matches))
	}
	// "~" alone lists the home directory rather than completing a user name.
	got, matches = completePathToken("~")
	if got != "~/notes.txt" || len(matches) != 1 {
		t.Fatalf("completePathToken(~) = %q, %d matches; want ~/notes.txt, 1", got, len(matches))
	}
}

func TestComposerPathToken(t *testing.T) {
	cases := []struct {
		value      string
		head, part string
		ok         bool
	}{
		{"/attach ~/pi", "/attach ", "~/pi", true},
		{"/upload /tmp/a", "/upload ", "/tmp/a", true},
		{"/avatar ~/a.png", "/avatar ", "~/a.png", true},
		// Not a path command, no argument yet, or already in the caption.
		{"/nick Ada", "", "", false},
		{"/attach", "", "", false},
		{"/attach ", "", "", false},
		{"/attach ~/a.png caption", "", "", false},
		{"hello world", "", "", false},
	}
	for _, c := range cases {
		head, part, ok := composerPathToken(c.value)
		if head != c.head || part != c.part || ok != c.ok {
			t.Errorf("composerPathToken(%q) = %q, %q, %v; want %q, %q, %v",
				c.value, head, part, ok, c.head, c.part, c.ok)
		}
	}
}

func TestCommonPrefix(t *testing.T) {
	if got := commonPrefix([]string{"alpha.txt", "alpine.txt"}); got != "alp" {
		t.Fatalf("commonPrefix = %q, want alp", got)
	}
	if got := commonPrefix([]string{"héllo", "hélice"}); got != "hél" {
		t.Fatalf("commonPrefix = %q, want hé", got)
	}
}

// TestTabCompletesAttachPath checks that tab fills in the path of a pending
// /attach instead of moving focus to the sidebar.
func TestTabCompletesAttachPath(t *testing.T) {
	m := newTestChat(t, 1)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.setFocus(focusComposer)
	m.chat.composer.SetValue("/attach " + dir + "/alph")

	m = update(t, m, tea.KeyMsg{Type: tea.KeyTab})

	if got, want := m.chat.composer.Value(), "/attach "+dir+"/alpha.txt "; got != want {
		t.Fatalf("composer = %q, want %q", got, want)
	}
	if m.chat.focus != focusComposer {
		t.Fatalf("focus = %v, want focusComposer", m.chat.focus)
	}
}

func TestTabCompletesTildePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newTestChat(t, 1)
	m.setFocus(focusComposer)
	m.chat.composer.SetValue("/attach ~/no")

	m = update(t, m, tea.KeyMsg{Type: tea.KeyTab})

	if got, want := m.chat.composer.Value(), "/attach ~/notes.txt "; got != want {
		t.Fatalf("composer = %q, want %q", got, want)
	}
}

// TestTabWithoutPathMovesFocus keeps tab's focus cycling when the composer is
// not typing a path.
func TestTabWithoutPathMovesFocus(t *testing.T) {
	m := newTestChat(t, 1)
	m.setFocus(focusComposer)
	m.chat.composer.SetValue("hello")

	m = update(t, m, tea.KeyMsg{Type: tea.KeyTab})

	if m.chat.focus != focusRooms {
		t.Fatalf("focus = %v, want focusRooms", m.chat.focus)
	}
	if got := m.chat.composer.Value(); got != "hello" {
		t.Fatalf("composer = %q, want hello", got)
	}
}

func TestReadUploadFileExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "note.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	up, err := readUploadFile("~/note.txt", nil, mediaUpload)
	if err != nil {
		t.Fatal(err)
	}
	if up.name != "note.txt" || string(up.data) != "hi" {
		t.Fatalf("got name %q data %q, want note.txt hi", up.name, up.data)
	}
	if !strings.HasPrefix(up.path, "~/") {
		t.Fatalf("path = %q, want the typed ~/ path", up.path)
	}
}
