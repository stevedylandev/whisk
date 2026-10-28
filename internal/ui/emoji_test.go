package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/stevedylandev/whisk/internal/protocol"
)

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestEmojiMatches(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{"thumbs", "👍"},
		{"thumbsup", "👍"},
		{"thumbs-up", "👍"},
		{"+1", "👍"},
		{"sungla", "😎"},
		{"dark_sunglasses", "🕶️"},
		{":tada:", "🎉"},
		{"tada", "🎉"},
		{"party popper", "🎉"},
		{"heart", "❤️"},
		{"rocket", "🚀"},
		{"THUMBS", "👍"},
	}
	for _, c := range cases {
		got := emojiMatches(c.query)
		if len(got) == 0 {
			t.Errorf("emojiMatches(%q) found nothing", c.query)
			continue
		}
		if got[0].char != c.want {
			t.Errorf("emojiMatches(%q)[0] = %q (%s), want %q", c.query, got[0].char, got[0].name, c.want)
		}
	}
}

func TestEmojiMatchesFailsCleanly(t *testing.T) {
	if got := emojiMatches("zzzzzz-no-such-emoji"); len(got) != 0 {
		t.Fatalf("emojiMatches nonsense = %d results, want 0", len(got))
	}
}

// TestEmojiDisplayIsWidthSafe keeps the picker aligned: the drawn copy drops
// VS16, which lipgloss and some terminals measure differently, but reactions
// still send the full sequence.
func TestEmojiDisplayIsWidthSafe(t *testing.T) {
	got := emojiMatches("heart")
	if len(got) == 0 {
		t.Fatal("heart found nothing")
	}
	if got[0].char != "❤️" {
		t.Fatalf("reaction char = %q, want the full sequence", got[0].char)
	}
	if got[0].display != "❤" {
		t.Fatalf("display char = %q, want width-safe ❤", got[0].display)
	}
}

// TestEmojiSearchDoesNotSpanWords guards against loose subsequence matches:
// "sungla" must not find "spouting whale" or "classical building".
// TestFlagDisplayIsWidthSafe: regional-indicator flags are drawn as a stable
// two-cell marker because terminals advance them differently.
func TestFlagDisplayIsWidthSafe(t *testing.T) {
	got := emojiMatches("jp")
	for _, e := range got {
		if e.char != "🇯🇵" {
			continue
		}
		if e.display != "⚑ " {
			t.Fatalf("flag display = %q, want the flag marker", e.display)
		}
		if w := lipgloss.Width(e.display); w != 2 {
			t.Fatalf("flag display width = %d, want 2", w)
		}
		return
	}
	t.Fatal("jp did not find the Japan flag")
}

func TestEmojiSearchDoesNotSpanWords(t *testing.T) {
	got := emojiMatches("sungla")
	if len(got) == 0 {
		t.Fatal("sungla found nothing")
	}
	for _, e := range got {
		if !strings.Contains(e.label, "sunglasses") {
			t.Errorf("sungla matched %q (%s), want only sunglasses names", e.label, e.name)
		}
	}
	if got[0].label != "sunglasses" {
		t.Errorf("sungla first = %q, want sunglasses", got[0].label)
	}
}

func withReactions(t *testing.T, m *Model) {
	t.Helper()
	m.cur().store.ApplyServer(&protocol.Server{
		Apron:        9,
		Auth:         []string{"guest"},
		Capabilities: []string{"rooms", "history", "reactions"},
	})
}

func TestEmojiPickerSearchesAndReacts(t *testing.T) {
	m := newTestChat(t, 1)
	withReactions(t, &m)
	m.setFocus(focusMessages)
	m.renderContent()

	m = update(t, m, keyRunes("+"))
	if !m.chat.emoji.open {
		t.Fatal("+ did not open the emoji picker")
	}
	if m.chat.emoji.msgID != "100" {
		t.Fatalf("picker target = %q, want 100", m.chat.emoji.msgID)
	}

	for _, r := range "tada" {
		m = update(t, m, keyRunes(string(r)))
	}
	if m.chat.emoji.query != "tada" {
		t.Fatalf("query = %q, want tada", m.chat.emoji.query)
	}
	results := emojiMatches(m.chat.emoji.query)
	if len(results) == 0 || results[0].char != "🎉" {
		t.Fatalf("tada results = %v, want 🎉 first", results)
	}

	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if m.chat.emoji.open {
		t.Fatal("enter did not close the picker")
	}
	if cmd == nil {
		t.Fatal("enter did not send a reactions request")
	}
}

func TestEmojiPickerTakesLetterKeys(t *testing.T) {
	m := newTestChat(t, 1)
	withReactions(t, &m)
	m.setFocus(focusMessages)
	m.renderContent()
	m = update(t, m, keyRunes("+"))

	// "j" and "k" must be search text here, not selection movement.
	m = update(t, m, keyRunes("j"))
	m = update(t, m, keyRunes("k"))
	if m.chat.emoji.query != "jk" {
		t.Fatalf("query = %q, want jk", m.chat.emoji.query)
	}
	if !m.chat.emoji.open {
		t.Fatal("typing closed the picker")
	}

	m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.chat.emoji.query != "j" {
		t.Fatalf("query after backspace = %q, want j", m.chat.emoji.query)
	}

	// Arrows do move the picker cursor without touching the query.
	m = update(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.chat.emoji.cursor != 1 {
		t.Fatalf("cursor after down = %d, want 1", m.chat.emoji.cursor)
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.chat.emoji.cursor != 0 {
		t.Fatalf("cursor after up = %d, want 0", m.chat.emoji.cursor)
	}
}

func TestEmojiPickerEscCancels(t *testing.T) {
	m := newTestChat(t, 1)
	withReactions(t, &m)
	m.setFocus(focusMessages)
	m.renderContent()
	before := m.chat.selectedID
	m = update(t, m, keyRunes("+"))
	m = update(t, m, keyRunes("smile"))
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEsc})

	if m.chat.emoji.open {
		t.Fatal("esc did not close the picker")
	}
	if m.chat.emoji.query != "" {
		t.Fatalf("query after esc = %q, want empty", m.chat.emoji.query)
	}
	if m.chat.selectedID != before {
		t.Fatalf("selection changed to %q, want %q", m.chat.selectedID, before)
	}
}

// TestEmojiCommandInsertsIntoComposer: /emoji opens the picker from the
// composer and enter puts the chosen emoji in the message instead of reacting.
// It needs no reactions capability and no selected message.
func TestEmojiCommandInsertsIntoComposer(t *testing.T) {
	m := newTestChat(t, 1)
	m.setFocus(focusComposer)
	m.chat.composer.SetValue("/emoji tada")

	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.chat.emoji.open || !m.chat.emoji.insert {
		t.Fatalf("picker open=%v insert=%v, want an insert picker", m.chat.emoji.open, m.chat.emoji.insert)
	}
	if m.chat.emoji.query != "tada" {
		t.Fatalf("query = %q, want tada", m.chat.emoji.query)
	}
	if v := m.View(); !strings.Contains(v, "insert emoji") {
		t.Fatalf("picker title missing: %q", v)
	}

	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if cmd != nil {
		t.Fatal("inserting an emoji must not send a reactions request")
	}
	if m.chat.emoji.open {
		t.Fatal("enter did not close the picker")
	}
	if got := m.chat.composer.Value(); got != "🎉" {
		t.Fatalf("composer = %q, want 🎉", got)
	}
	if m.chat.focus != focusComposer {
		t.Fatalf("focus = %v, want the composer", m.chat.focus)
	}
}

// TestEmojiCommandWithoutQuery inserts from the default list, and esc leaves
// the composer alone.
func TestEmojiCommandWithoutQuery(t *testing.T) {
	m := newTestChat(t, 1)
	m.setFocus(focusComposer)
	m.chat.composer.SetValue("/emoji")

	m = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.chat.emoji.open || !m.chat.emoji.insert {
		t.Fatalf("picker open=%v insert=%v, want an insert picker", m.chat.emoji.open, m.chat.emoji.insert)
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.chat.emoji.open {
		t.Fatal("esc did not close the picker")
	}
	if got := m.chat.composer.Value(); got != "" {
		t.Fatalf("esc changed the composer to %q", got)
	}
}

func TestEmojiPickerRendersInWindow(t *testing.T) {
	for _, size := range []struct{ w, h int }{{50, 15}, {80, 24}, {120, 40}} {
		m := newTestChat(t, 1)
		withReactions(t, &m)
		m.setFocus(focusMessages)
		m = update(t, m, tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m.renderContent()
		m = update(t, m, keyRunes("+"))
		m = update(t, m, keyRunes("party"))

		v := m.View()
		if !strings.Contains(v, "react with emoji") {
			t.Fatalf("w=%d h=%d: picker missing: %q", size.w, size.h, v)
		}
		if !strings.Contains(v, "party") {
			t.Fatalf("w=%d h=%d: query missing: %q", size.w, size.h, v)
		}
		for i, line := range strings.Split(v, "\n") {
			if got := lipgloss.Width(line); got > size.w {
				t.Fatalf("w=%d h=%d: line %d width %d exceeds %d", size.w, size.h, i, got, size.w)
			}
		}

		// The popup boxes every result row to the same width, so a border
		// that tears would show up as unequal line widths here.
		lines := strings.Split(m.emojiPickerView(), "\n")
		want := lipgloss.Width(lines[0])
		for i, line := range lines {
			if got := lipgloss.Width(line); got != want {
				t.Fatalf("w=%d h=%d: picker line %d width %d, want %d", size.w, size.h, i, got, want)
			}
		}
	}
}
