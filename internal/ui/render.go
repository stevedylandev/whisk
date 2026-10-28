package ui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/reflow/wordwrap"

	"github.com/stevedylandev/whisk/internal/protocol"
	"github.com/stevedylandev/whisk/internal/state"
)

// renderer renders markdown with glamour (raw HTML disabled by goldmark's
// default). It is recreated lazily because word-wrap depends on width.
type renderer struct {
	width   int
	md      *glamour.TermRenderer
	mdMuted *glamour.TermRenderer
	cache   map[string]string
}

func newRenderer(width int) *renderer {
	if width < 20 {
		width = 20
	}
	// Use a fixed style (not WithAutoStyle) to avoid querying the terminal's
	// background color, which blocks ~5s on terminals that don't answer OSC 11.
	style, base := "dark", styles.DarkStyleConfig
	if lightTheme {
		style, base = "light", styles.LightStyleConfig
	}
	md, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		md = nil
	}
	// System messages use the same layout with the body text muted.
	muted := base
	mutedColor := "8"
	muted.Document.Color = &mutedColor
	mdMuted, err := glamour.NewTermRenderer(
		glamour.WithStyles(muted),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		mdMuted = nil
	}
	return &renderer{width: width, md: md, mdMuted: mdMuted, cache: make(map[string]string)}
}

// Markdown renders a markdown string to styled terminal text, caching the
// result. The same input at the same width always yields the same output.
func (r *renderer) Markdown(s string) string {
	if r == nil || r.md == nil {
		return s
	}
	if v, ok := r.cache[s]; ok {
		return v
	}
	out, err := r.md.Render(s)
	if err != nil {
		return s
	}
	// glamour pads the document with blank lines; trim for inline placement.
	out = trimBlankLines(out)
	if len(r.cache) > 4096 {
		r.cache = make(map[string]string)
	}
	r.cache[s] = out
	return out
}

// MarkdownMuted renders markdown with muted body text for system messages
// (§A.1), caching the result separately from the normal rendering.
func (r *renderer) MarkdownMuted(s string) string {
	if r == nil || r.mdMuted == nil {
		return s
	}
	key := "muted\x00" + s
	if v, ok := r.cache[key]; ok {
		return v
	}
	out, err := r.mdMuted.Render(s)
	if err != nil {
		return s
	}
	out = trimBlankLines(out)
	if len(r.cache) > 4096 {
		r.cache = make(map[string]string)
	}
	r.cache[key] = out
	return out
}

// trimBlankLines drops leading and trailing lines that render as blank
// (glamour's document margin), so a message body starts right under its
// header.
func trimBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	blank := func(l string) bool { return strings.TrimSpace(ansi.Strip(l)) == "" }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// isSystemSender reports whether a message comes from a server-controlled
// system identity (§A.1): user_ids beginning with "~".
func isSystemSender(u *protocol.User) bool {
	return u != nil && strings.HasPrefix(u.UserID, "~")
}

// renderBodyText renders a message body, using the muted variant for system
// messages and notices.
func renderBodyText(r *renderer, text, format string, width int, muted bool) string {
	// Drop invisible joiners before wrapping so the wrapper measures the same
	// cells terminals paint (see termWidthSafe).
	text = termWidthSafe(text)
	if format == "markdown" {
		if muted {
			return r.MarkdownMuted(text)
		}
		return r.Markdown(text)
	}
	// Plain text gets the same left margin glamour gives markdown, so every
	// body lines up under its header.
	out := indent(wordwrap.String(text, width-bodyIndent))
	if muted {
		return dimStyle.Render(out)
	}
	return out
}

// bodyIndent is the left margin of message bodies, matching glamour's.
const bodyIndent = 2

// indent prefixes each line of s with the body margin.
func indent(s string) string {
	pad := strings.Repeat(" ", bodyIndent)
	return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
}

// renderMessage renders one message snapshot for display at the given content
// width.
func renderMessage(s *state.Store, r *renderer, m *protocol.Message, contentWidth int) string {
	return renderMessageOpts(s, r, m, contentWidth, false)
}

// groupWindow is how close in time two messages from the same author must be
// to share one header.
const groupWindow = 5 * time.Minute

// continuesGroup reports whether m can render without a header under prev:
// same author, same day, close in time, and nothing the header would have to
// say (a reply, an edit, a move, a deletion, or a system sender).
func continuesGroup(prev, m *protocol.Message) bool {
	if prev == nil || m == nil || prev.From == nil || m.From == nil {
		return false
	}
	if prev.From.UserID != m.From.UserID || isSystemSender(m.From) {
		return false
	}
	if prev.Deleted || m.Deleted || m.ReplyTo != nil {
		return false
	}
	if m.PrevRoomID != "" && m.PrevRoomID != m.RoomID {
		return false
	}
	if m.MessageID != "" && m.LogID != "" && m.LogID != m.MessageID {
		return false
	}
	pt, ok1 := msgTime(prev)
	mt, ok2 := msgTime(m)
	if !ok1 || !ok2 {
		return false
	}
	return sameDay(pt, mt) && mt.Sub(pt) < groupWindow
}

// msgTime returns when a message was created: its message_id, which is a
// log position in milliseconds, falling back to its log_id.
func msgTime(m *protocol.Message) (time.Time, bool) {
	if m == nil {
		return time.Time{}, false
	}
	id := m.MessageID
	if id == "" {
		id = m.LogID
	}
	ms, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.UnixMilli(ms), true
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// daySeparator renders a centered rule naming a day, e.g. "── Tue Oct 27 ──".
func daySeparator(t time.Time, width int) string {
	label := " " + t.Format("Mon Jan 2") + " "
	if t.Year() != time.Now().Year() {
		label = " " + t.Format("Mon Jan 2, 2006") + " "
	}
	fill := width - len([]rune(label))
	if fill < 4 {
		return dimStyle.Render(strings.TrimSpace(label))
	}
	left := fill / 2
	return dimStyle.Render(strings.Repeat("─", left) + label + strings.Repeat("─", fill-left))
}

// renderMessageOpts renders a message; compact leaves out the header, for a
// message that continues the previous one's group.
func renderMessageOpts(s *state.Store, r *renderer, m *protocol.Message, contentWidth int, compact bool) string {
	if m == nil {
		return ""
	}
	author := s.DisplayName(m.From.UserID, m.From)
	if s.NameIsAmbiguous(m.From.UserID, author) {
		author = fmt.Sprintf("%s (@%s)", author, m.From.UserID)
	}

	author = truncateRunes(author, 28)

	system := isSystemSender(m.From)
	var header string
	if m.From != nil {
		if system {
			header = dimStyle.Render(author)
		} else {
			header = authorStyle(m.From.UserID).Render(author)
		}
		for _, role := range m.From.Roles {
			header += " " + dimStyle.Render("["+role+"]")
		}
		if ts := logTime(m.LogID); ts != "" {
			header += " " + dimStyle.Render(ts)
		}
		if m.PrevRoomID != "" && m.PrevRoomID != m.RoomID {
			header += " " + dimStyle.Render("moved from #"+m.PrevRoomID)
		}
		if m.MessageID != "" && m.LogID != "" && m.LogID != m.MessageID {
			header += " " + dimStyle.Render("(edited)")
		}
		if m.Deleted {
			header += " " + dimStyle.Render("(deleted)")
		}
		// Show "last seen …" while the author's shown status is offline
		// (§4.5.2).
		if kept := s.User(m.From.UserID); kept != nil && kept.Status == "offline" && kept.Seen != "" {
			header += " " + dimStyle.Render("last seen "+logTime(kept.Seen))
		}
	}

	if compact {
		header = ""
	}

	if m.Deleted {
		if header != "" {
			return header + " " + dimStyle.Render("— message deleted —")
		}
		return dimStyle.Render("— message deleted —")
	}

	var body strings.Builder
	if m.ReplyTo != nil && m.ReplyTo.MessageID != "" {
		body.WriteString(indent(renderReply(s, m.ReplyTo, contentWidth)) + "\n")
	}
	if m.Body != nil {
		if text := m.Body.Text; text != "" {
			// Show @id that refers to a known user with the latest display
			// name (Appendix A.3).
			text = applyMentionNames(s, m.RoomID, text)
			body.WriteString(renderBodyText(r, text, m.Body.Format, contentWidth, system))
		}
		for _, e := range m.Body.Embeds {
			body.WriteString("\n" + renderEmbed(r, e))
		}
	}

	if m.MessageID != "" {
		if totals := s.ReactionTotals(m.MessageID); len(totals) > 0 {
			mine := make(map[string]bool)
			if s.You != nil {
				for _, e := range s.MyReactions(m.MessageID, s.You.UserID) {
					mine[e] = true
				}
			}
			parts := make([]string, 0, len(totals))
			for _, t := range totals {
				pill := fmt.Sprintf("[%s %d]", t.Emoji, t.Count)
				if mine[t.Emoji] {
					parts = append(parts, accentStyle.Render(pill))
				} else {
					parts = append(parts, dimStyle.Render(pill))
				}
			}
			body.WriteString("\n" + indent(strings.Join(parts, " ")))
		}
	}

	if header == "" {
		return body.String()
	}
	if body.Len() == 0 {
		return header
	}
	return header + "\n" + body.String()
}

// renderNotice renders a transient notice (§3.5.3) inline in a room.
func renderNotice(r *renderer, n *state.Notice, contentWidth int) string {
	name := "?"
	if n.From != nil && n.From.Name != "" {
		name = n.From.Name
	}
	header := infoStyle.Render("ℹ " + name)
	if !n.At.IsZero() {
		header += " " + dimStyle.Render(n.At.Format("15:04"))
	}
	var body string
	if n.Body != nil {
		body = renderBodyText(r, n.Body.Text, n.Body.Format, contentWidth, true)
	}
	if body == "" {
		return header
	}
	return header + "\n" + body
}

// renderNoticePlain renders a notice's text for the overlay, unwrapped.
func renderNoticePlain(body *protocol.Body) string {
	if body == nil {
		return ""
	}
	return body.Text
}

// termWidthSafe drops emoji variation selectors (U+FE0F) and zero-width
// joiners (U+200D) from text about to be laid out. lipgloss measures a narrow
// symbol plus VS16, such as "❤️", as two cells, but terminals without grapheme
// clustering (mode 2027, which Bubble Tea does not enable) advance one. Joined
// sequences are worse: "😀‍♀️" measures as one two-cell grapheme, while such a
// terminal paints the grinning face (two cells) followed by the female sign
// (one). The padding then comes up short and the pane border tears. Without
// the invisible joiners both sides agree on the visible cells; emoji that are
// wide on their own are unaffected.
func termWidthSafe(s string) string {
	s = strings.ReplaceAll(s, "\uFE0F", "")
	return strings.ReplaceAll(s, "\u200D", "")
}

// truncateRunes truncates s to at most max runes, adding an ellipsis.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func renderEmbed(r *renderer, e protocol.Embed) string {
	title := e.Title
	var ogTitle, ogDesc string
	if len(e.OG) > 0 {
		var og struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		}
		if json.Unmarshal(e.OG, &og) == nil {
			ogTitle = og.Title
			ogDesc = og.Description
		}
	}
	if title == "" {
		title = ogTitle
	}
	if title == "" {
		title = e.Kind
	}
	line := indent(fmt.Sprintf("[%s] %s", e.Kind, title))
	if ogDesc != "" {
		line += " — " + ogDesc
	}
	if e.URL != "" {
		line += " — " + e.URL
	}
	// A finished stream carries its kept text in place of url (§4.8.5).
	if e.Kind == "stream" && e.Text != "" {
		if e.Format == "markdown" {
			return line + "\n" + r.Markdown(e.Text)
		}
		return line + "\n" + e.Text
	}
	return dimStyle.Render(line)
}

// renderReply renders a one-line "↳ replying to" context for a reply target.
func renderReply(s *state.Store, replyTo *protocol.Message, contentWidth int) string {
	if replyTo.From != nil && replyTo.Body != nil {
		return replyLine(s, replyTo.From, replyTo.Body.Text)
	}
	if m := s.MessageByID(replyTo.MessageID); m != nil {
		text := ""
		if m.Body != nil {
			text = m.Body.Text
		}
		return replyLine(s, m.From, text)
	}
	return dimStyle.Render("↳ " + replyTo.MessageID)
}

func replyLine(s *state.Store, from *protocol.User, text string) string {
	name := "?"
	if from != nil {
		name = s.DisplayName(from.UserID, from)
	}
	quote := strings.ReplaceAll(text, "\n", " ")
	if quote == "" {
		quote = "…"
	}
	quote = truncateRunes(quote, 60)
	return dimStyle.Render("↳ " + name + ": " + quote)
}

func logTime(logID string) string {
	if logID == "" {
		return ""
	}
	ms, err := strconv.ParseInt(logID, 10, 64)
	if err != nil {
		return ""
	}
	return time.UnixMilli(ms).Format("15:04")
}

// --- mentions (§3.5.5, Appendix A.3) ---------------------------------------

// mentionToken is one @id token found in text.
type mentionToken struct {
	Start int // index of '@' in text
	End   int // index just past the id
	ID    string
}

func isAlnum(b byte) bool {
	return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9'
}

func isMentionStart(b byte) bool {
	return isAlnum(b) || b == '_'
}

func isMentionChar(b byte) bool {
	return isAlnum(b) || b == '_' || b == '.' || b == '+' || b == '@' || b == ':' || b == '-'
}

// scanMentions finds @id tokens in text per Appendix A.3: after '@', the
// longest run of [A-Za-z0-9_.+@:-] that starts and ends with a letter, digit,
// or '_', where the '@' is not preceded by a letter, digit, or '_'.
func scanMentions(text string) []mentionToken {
	var out []mentionToken
	for i := 0; i < len(text); i++ {
		if text[i] != '@' {
			continue
		}
		if i > 0 {
			if prev := text[i-1]; isMentionStart(prev) {
				continue
			}
		}
		j := i + 1
		if j >= len(text) || !isMentionStart(text[j]) {
			continue
		}
		for j < len(text) && isMentionChar(text[j]) {
			j++
		}
		for j > i+1 && !isMentionStart(text[j-1]) {
			j--
		}
		out = append(out, mentionToken{Start: i, End: j, ID: text[i+1 : j]})
		i = j - 1
	}
	return out
}

// extractMentions returns the user_ids mentioned by @id tokens in text,
// limited to users the store knows (room members or any user seen so far).
// The authenticated user is not mentioned.
func extractMentions(s *state.Store, roomID, text string) []string {
	room := s.Room(roomID)
	seen := make(map[string]bool)
	var out []string
	for _, t := range scanMentions(text) {
		if seen[t.ID] {
			continue
		}
		known := s.User(t.ID) != nil
		if !known && room != nil {
			_, known = room.Members[t.ID]
		}
		if !known {
			continue
		}
		if s.You != nil && t.ID == s.You.UserID {
			continue
		}
		seen[t.ID] = true
		out = append(out, t.ID)
	}
	return out
}

// applyMentionNames replaces @id tokens that refer to a known user with the
// user's latest display name (Appendix A.3). Unknown ids render as written.
func applyMentionNames(s *state.Store, roomID, text string) string {
	toks := scanMentions(text)
	if len(toks) == 0 {
		return text
	}
	room := s.Room(roomID)
	var b strings.Builder
	last := 0
	for _, t := range toks {
		name := ""
		if u := s.User(t.ID); u != nil {
			name = u.Name
		}
		if name == "" && room != nil {
			if mu := room.Members[t.ID]; mu != nil {
				name = mu.Name
			}
		}
		b.WriteString(text[last:t.Start])
		if name == "" {
			b.WriteString(text[t.Start:t.End])
		} else {
			b.WriteString("@" + name)
		}
		last = t.End
	}
	b.WriteString(text[last:])
	return b.String()
}
