package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	emojiPickerWidth   = 48
	emojiPickerMaxRows = 9
)

var emojiPickerStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(accent).
	Padding(0, 2)

// emojiPickerRows is how many results the picker shows. Short terminals get
// fewer rows so the modal never covers the whole frame.
func (m Model) emojiPickerRows() int {
	rows := emojiPickerMaxRows
	if max := m.height - 8; max < rows {
		rows = max
	}
	if rows < 1 {
		rows = 1
	}
	return rows
}

// emojiPickerView renders the reaction picker: a search field, the matching
// emoji, and the keys that drive it.
func (m Model) emojiPickerView() string {
	p := m.chat.emoji
	results := emojiMatches(p.query)
	rows := m.emojiPickerRows()

	cursor := p.cursor
	if cursor >= len(results) {
		cursor = len(results) - 1
	}
	if cursor < 0 {
		cursor = 0
	}
	start := 0
	if cursor >= rows {
		start = cursor - rows + 1
	}
	end := start + rows
	if end > len(results) {
		end = len(results)
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("react with emoji") + "\n")
	b.WriteString(accentStyle.Render("> ") + p.query + accentStyle.Render("▏") + "\n")

	shown := 0
	if len(results) == 0 {
		b.WriteString(dimStyle.Render("  no matching emoji") + "\n")
		shown = 1
	} else {
		for i := start; i < end; i++ {
			e := results[i]
			line := e.display + "  " + e.label
			if i == cursor {
				b.WriteString(accentStyle.Render("› ") + roomSelStyle.Render(line) + "\n")
			} else {
				b.WriteString("  " + dimStyle.Render(line) + "\n")
			}
			shown++
		}
	}
	// Keep the box's height steady as the result count changes.
	for ; shown < rows; shown++ {
		b.WriteString("\n")
	}

	b.WriteString("\n" + helpStyle.Render("↑/↓ choose · enter react · esc cancel"))

	// Size the box to a stable width rather than the longest result, so it
	// does not jump while the query changes.
	width := emojiPickerWidth
	if max := m.width - 4 - emojiPickerStyle.GetHorizontalFrameSize(); max < width {
		width = max
	}
	if width < 1 {
		width = 1
	}
	body := clipLines(b.String(), width)
	return emojiPickerStyle.Width(width + emojiPickerStyle.GetHorizontalPadding()).Render(body)
}
