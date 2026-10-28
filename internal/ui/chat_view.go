package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/stevedylandev/whisk/internal/protocol"
	"github.com/stevedylandev/whisk/internal/state"
)

// Chat screen layout and rendering: pane sizes, the message viewport content,
// the header, sidebar, composer and bottom bar.

const (
	sidebarMin = 16
	sidebarMax = 28
	paneGap    = 1 // columns between the sidebar and message panes
)

// sidebarWidth returns the sidebar's inner content width for an available
// width, clamped to a readable range.
func sidebarWidth(w int) int {
	sw := w / 4
	if sw < sidebarMin {
		sw = sidebarMin
	}
	if sw > sidebarMax {
		sw = sidebarMax
	}
	return sw
}

// layout returns the inner content widths of the sidebar (sw) and messages
// (cw) panes at a window width w, accounting for each pane's border and
// padding so the joined panes fit exactly within w columns.
func (c *chatModel) layout(w int) (sw, cw int) {
	avail := w - sidebarStyle.GetHorizontalFrameSize() - paneGap - msgViewStyle.GetHorizontalFrameSize()
	if avail <= 0 {
		return 0, 0
	}
	sw = sidebarWidth(avail)
	if sw > avail {
		sw = avail
	}
	cw = avail - sw
	if cw < 20 {
		// Not enough room for a sidebar at this width: hide it and give the
		// message pane the full width.
		sw = 0
		cw = avail
	}
	return sw, cw
}

// composerContext reports whether a reply/edit context line sits above the
// composer.
func (c *chatModel) composerContext() bool {
	return c.replyTo != "" || c.editingID != ""
}

// composerBoxHeight is the composer's height including its border and any
// context line.
func (c *chatModel) composerBoxHeight() int {
	h := c.composer.Height() + 2 // composer border
	if c.composerContext() {
		h++
	}
	return h
}

// composerMaxHeight caps how many text rows the composer grows to. Past it the
// textarea scrolls internally, so the message view keeps most of the window.
const composerMaxHeight = 8

// syncComposerHeight grows or shrinks the composer to fit its text, returning
// whether its height changed. The textarea wraps at its current width, so the
// width must be set before this is called.
func (c *chatModel) syncComposerHeight() bool {
	h := c.composerTextHeight()
	if h > composerMaxHeight {
		h = composerMaxHeight
	}
	if h < 1 {
		h = 1
	}
	if c.composer.Height() == h {
		return false
	}
	c.composer.SetHeight(h)
	return true
}

// composerTextHeight counts the visual rows the composer's text occupies:
// one per newline, plus extra rows for lines long enough to wrap.
func (c *chatModel) composerTextHeight() int {
	width := c.composer.Width()
	rows := 0
	for _, line := range strings.Split(c.composer.Value(), "\n") {
		rows += composerWrapRows(line, width)
	}
	if rows < 1 {
		rows = 1
	}
	return rows
}

// composerWrapRows mirrors the textarea's word wrapping to count how many
// visual rows one logical line takes at width.
func composerWrapRows(line string, width int) int {
	if width <= 0 {
		return 1
	}
	rows := 1
	rowWidth := 0  // cells used on the current row
	wordWidth := 0 // cells in the word being accumulated
	for _, r := range line {
		if unicode.IsSpace(r) {
			// Flush the pending word and this space onto the row.
			w := wordWidth + 1
			if rowWidth+w > width {
				rows++
				rowWidth = w
			} else {
				rowWidth += w
			}
			wordWidth = 0
			continue
		}
		rw := ansi.StringWidth(string(r))
		// A word wider than the row starts on a row of its own; an unbroken
		// word longer still fills one row per chunk. The textarea also counts
		// the last rune twice, since a double-width one may not fit.
		if wordWidth+2*rw > width {
			if rowWidth > 0 {
				rows++
				rowWidth = 0
			}
			rowWidth += wordWidth + rw
			wordWidth = 0
			continue
		}
		wordWidth += rw
	}
	// A row filled exactly to width wraps, because the textarea appends a
	// trailing space to the last word.
	if rowWidth+wordWidth >= width {
		rows++
	}
	return rows
}

func (c *chatModel) resize(w, h int) {
	_, cw := c.layout(w)
	composerWidth := cw - 2 // textarea prompt columns
	if composerWidth < 0 {
		composerWidth = 0
	}
	c.composer.SetWidth(composerWidth)
	c.syncComposerHeight()
	composerHeight := c.composerBoxHeight()
	// The sidebar/messages panes add a top and bottom border to the row block.
	paneFrameH := sidebarStyle.GetVerticalFrameSize()
	// Fixed rows: header, the bottom bar, and the row block's borders. The
	// composer sits under the message view inside that block, so its height is
	// subtracted separately. Leave two spare rows so the view never fills the
	// terminal: when it does, Bubble Tea drops lines off the top and hides the
	// header. Two rows also absorbs terminals that reserve a row or report a
	// height that is one or two off.
	fixed := 1 + paneFrameH + 1
	vh := h - fixed - 2 - composerHeight
	if vh < 1 {
		vh = 1
	}
	c.viewport.Width = cw - msgViewStyle.GetHorizontalPadding()
	if c.viewport.Width < 0 {
		c.viewport.Width = 0
	}
	c.viewport.Height = vh
}

func (m *Model) renderContent() {
	c := &m.chat
	// The viewport sits inside the message pane's padding, so its width is the
	// width the renderer must wrap at. Keeping these equal means one rendered
	// line is one visual row and selection positions stay accurate.
	// Each row keeps a gutter on the left for the selection bar, so messages
	// wrap at the viewport width minus the gutter.
	cw := c.viewport.Width - gutterWidth
	if cw < 1 {
		cw = 1
	}
	if c.renderer == nil || c.renderer.width != cw {
		c.renderer = newRenderer(cw)
	}

	var sb strings.Builder
	c.msgPos = c.msgPos[:0]
	line := 0
	room := m.cur().store.Room(c.roomID)
	// Default the selection to the newest message when there is no valid
	// selection yet (e.g. a freshly opened room), so the highlight renders on
	// the first pass instead of only after the next re-render.
	if room != nil && len(room.Order) > 0 {
		if _, ok := room.Messages[c.selectedID]; !ok {
			c.selectedID = room.Order[len(room.Order)-1]
		}
	}
	if room != nil {
		var prev *protocol.Message
		for _, id := range room.Order {
			msg := room.Messages[id]
			// A day change gets a separator; a new author group gets a blank
			// line, and a message continuing a group drops its header.
			compact := continuesGroup(prev, msg)
			if t, ok := msgTime(msg); ok {
				if pt, pok := msgTime(prev); prev == nil || !pok || !sameDay(pt, t) {
					if prev != nil {
						sb.WriteString("\n")
						line++
					}
					sb.WriteString(gutter(daySeparator(t, cw), false) + "\n")
					line++
					prev = nil
					compact = false
				}
			}
			if prev != nil && !compact {
				sb.WriteString("\n")
				line++
			}
			prev = msg
			rendered := renderMessageOpts(m.cur().store, c.renderer, msg, cw, compact)
			// Keep one logical line per visual row: headers, embeds and code
			// blocks are not wrapped by the message renderer, and the pane
			// must not wrap them either or selection positions drift. Make
			// emoji width-safe first so the wrapper measures the same cells
			// the terminal will paint.
			if cw > 0 {
				rendered = ansi.Wrap(termWidthSafe(rendered), cw, "")
			}
			if id == c.selectedID && c.focus == focusMessages {
				rendered = gutter(rendered, true)
			} else {
				rendered = gutter(rendered, false)
			}
			c.msgPos = append(c.msgPos, msgPos{id: id, start: line})
			sb.WriteString(rendered)
			sb.WriteString("\n")
			line += strings.Count(rendered, "\n") + 1
		}
	}

	// Transient notices for this room (§3.5.3, A.1): shown inline, never
	// installed as snapshots. They have no log position, so they follow the
	// messages of the session.
	for _, n := range m.cur().store.RoomNotices(c.roomID) {
		rendered := gutter(ansi.Wrap(termWidthSafe(renderNotice(c.renderer, &n, cw)), cw, ""), false)
		sb.WriteString(rendered)
		sb.WriteString("\n")
		line += strings.Count(rendered, "\n") + 1
	}

	c.viewport.SetContent(termWidthSafe(sb.String()))

	if c.stickBottom {
		c.viewport.GotoBottom()
		c.stickBottom = false
		c.anchorID = ""
		// Scrolling to the newest messages also moves the selection there, so
		// the highlight follows instead of lingering on an off-screen message.
		if len(c.msgPos) > 0 {
			c.selectedID = c.msgPos[len(c.msgPos)-1].id
		}
	} else if c.anchorID != "" {
		found := false
		for _, p := range c.msgPos {
			if p.id == c.anchorID {
				found = true
				if p.start != c.anchorStart {
					c.viewport.SetYOffset(p.start - c.anchorOffset)
				}
				break
			}
		}
		// Keep the anchor for the whole load so an unrelated render (e.g. a
		// new message) cannot drop it; release it once the page has landed.
		if !found || room == nil || !room.Loading {
			c.anchorID = ""
		}
	}
	c.dirty = false
}

// gutter prefixes every line of s with the gutter: the selection bar when
// selected, blank otherwise. Only the gutter marks a selection, so selecting
// cannot change a message's line count or width, and its own styling (code
// blocks, colors) stays readable.
func gutter(s string, selected bool) string {
	prefix := strings.Repeat(" ", gutterWidth)
	if selected {
		prefix = selBar(true)
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// clip trims a single rendered line to the window width so it cannot wrap and
// add an extra row. A wrapped fixed row would make the frame taller than the
// terminal, scrolling the alt screen and hiding the header.
func (m Model) clip(s string) string {
	if m.width <= 0 {
		return s
	}
	return ansi.Truncate(s, m.width, "…")
}

// clampHeight keeps at most n lines of s. lipgloss Height() sets a minimum, not
// a maximum, so pane content longer than its pane would otherwise grow the
// frame past the terminal and make Bubble Tea drop lines off the top.
func clampHeight(s string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func (m Model) viewChat() string {
	sw, cw := m.chat.layout(m.width)
	c := m.chat

	header := m.clip(termWidthSafe(m.headerView()))
	msgStyle := msgViewStyle.BorderForeground(paneBorder(c.focus == focusMessages))
	var msgs string
	if c.showNotices {
		msgs = msgStyle.Width(cw).Height(c.viewport.Height).Render(clampHeight(termWidthSafe(m.noticesView()), c.viewport.Height))
	} else {
		msgs = msgStyle.Width(cw).Height(c.viewport.Height).Render(c.viewport.View())
	}

	// The composer sits directly under the message view, in the same column;
	// the sidebar runs alongside both, all the way to the bottom of the chat
	// column. Its placeholder names the open room; c is a copy, so setting it
	// here does not touch the model.
	if r := m.cur().store.Room(c.roomID); r != nil {
		mark := "#"
		if _, direct := directPeer(m.cur(), r); direct {
			mark = ""
		}
		c.composer.Placeholder = "message " + mark + roomName(m.cur(), r) + "…"
	}
	composerBorder := paneBorder(c.focus == focusComposer)
	if c.editingID != "" {
		composerBorder = warn
	}
	inner := c.composer.View()
	if c.composerContext() {
		inner = clipLines(termWidthSafe(m.composerContextLine()), cw-composerStyle.GetHorizontalPadding()) + "\n" + inner
	}
	composer := composerStyle.BorderForeground(composerBorder).Width(cw).Render(inner)
	chatColumn := lipgloss.JoinVertical(lipgloss.Left, msgs, composer)

	sidebarHeight := c.viewport.Height + c.composerBoxHeight()
	sbStyle := sidebarStyle.BorderForeground(paneBorder(c.focus == focusRooms))
	sidebar := sbStyle.Width(sw).Height(sidebarHeight).Render(clampHeight(termWidthSafe(m.sidebarView()), sidebarHeight))

	spacer := lipgloss.NewStyle().Width(paneGap).Render("")
	middle := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, spacer, chatColumn)

	var b strings.Builder
	b.WriteString(header + "\n")
	b.WriteString(middle + "\n")
	b.WriteString(m.bottomBar())
	if c.emoji.open {
		return overlay(b.String(), termWidthSafe(m.emojiPickerView()), m.width)
	}
	if c.showHelp {
		return overlay(b.String(), termWidthSafe(m.helpModal()), m.width)
	}
	return b.String()
}

// composerContextLine describes what the composer is doing besides a plain
// send: replying to a message, or editing one.
func (m Model) composerContextLine() string {
	hint := dimStyle.Render("  esc cancel")
	if m.chat.editingID != "" {
		return warnStyle.Render("✎ editing message") + hint
	}
	store := m.cur().store
	name, text := "?", ""
	if msg := store.MessageByID(m.chat.replyTo); msg != nil {
		if msg.From != nil {
			name = store.DisplayName(msg.From.UserID, msg.From)
		}
		if msg.Body != nil {
			text = strings.ReplaceAll(msg.Body.Text, "\n", " ")
		}
	}
	quote := truncateRunes(text, 50)
	return accentStyle.Render("↳ replying to "+name) + dimStyle.Render(": "+quote) + hint
}

// noticesView renders the transient-notices overlay (N key): the most recent
// notices of this connection, newest last.
func (m Model) noticesView() string {
	notices := m.cur().store.Notices()
	if len(notices) == 0 {
		return dimStyle.Render("(no notices)")
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render("notices") + "\n\n")
	start := 0
	if len(notices) > 12 {
		start = len(notices) - 12
	}
	for _, n := range notices[start:] {
		room := ""
		if n.RoomID != "" {
			room = " · #" + n.RoomID
		}
		b.WriteString(infoStyle.Render("ℹ "+n.At.Format("15:04")) + dimStyle.Render(room) + "\n")
		b.WriteString(dimStyle.Render(renderNoticePlain(n.Body)) + "\n\n")
	}
	return b.String()
}

func (m Model) headerView() string {
	sep := dimStyle.Render(" · ")
	store := m.cur().store
	left := titleStyle.Render("whisk")
	if len(m.sessions) > 1 {
		left += sep + m.cur().label()
	}
	left += sep + m.selfName()
	if store.You != nil && store.You.Status != "" && store.You.Status != "online" {
		left += " " + statusDot(store.You.Status)
	}
	room, desc := m.chat.roomID, ""
	direct := false
	if r := store.Room(m.chat.roomID); r != nil {
		room = roomName(m.cur(), r)
		_, direct = directPeer(m.cur(), r)
		desc = strings.ReplaceAll(r.Record.Description, "\n", " ")
	}
	if room != "" {
		mark := "#"
		if direct {
			mark = ""
		}
		left += sep + roomStyle.Render(mark+room)
		if desc != "" {
			left += "  " + dimStyle.Render(truncateRunes(desc, 60))
		}
	}
	if store.IsMuted(m.chat.roomID) {
		left += sep + dimStyle.Render("muted")
	}
	if m.chat.showNotices {
		left += sep + dimStyle.Render("notices")
	}
	if typists := store.TypingUsers(m.chat.roomID, time.Now()); len(typists) > 0 {
		names := make([]string, 0, len(typists))
		for _, id := range typists {
			names = append(names, store.DisplayName(id, store.User(id)))
		}
		left += sep + dimStyle.Render(strings.Join(names, ", ")+" typing…")
	}
	return left
}

func (m Model) sidebarView() string {
	sw, _ := m.chat.layout(m.width)
	// Width(sw) on sidebarStyle includes its padding, so the text area is
	// narrower than sw; every line is clipped to it, or a long one wraps.
	inner := sw - sidebarStyle.GetHorizontalPadding()
	var b strings.Builder
	title := titleStyle.Render("rooms")
	if n := m.unreadTotal(); n > 0 {
		title += dimStyle.Render(fmt.Sprintf(" · %d new", n))
	}
	b.WriteString(clipLines(title, inner) + "\n\n")
	if len(m.chat.roomItems) == 0 {
		b.WriteString(dimStyle.Render("(none)"))
		return b.String()
	}
	focused := m.chat.focus == focusRooms
	for i, it := range m.chat.roomItems {
		if (it.header || it.subheader) && i > 0 {
			b.WriteString("\n")
		}
		// Every row keeps a gutter for the cursor bar, so rows line up
		// whether or not they are under the cursor.
		width := inner - gutterWidth
		var line string
		switch {
		case it.header:
			line = m.serverLine(it.sid, width)
		case it.subheader:
			line = dimStyle.Render(it.label)
		default:
			// Threads indent under their parent, so the hierarchy survives
			// even when the titles are clipped.
			indent := strings.Repeat("  ", it.depth)
			line = indent + m.roomLine(it, width-lipgloss.Width(indent))
		}
		prefix := strings.Repeat(" ", gutterWidth)
		if i == m.chat.roomCursor {
			prefix = selBar(focused)
			if focused && !it.header {
				line = roomSelStyle.Render(ansi.Strip(line))
			}
		}
		lines := strings.Split(clipLines(line, width), "\n")
		for j := range lines {
			lines[j] = prefix + lines[j]
		}
		b.WriteString(strings.Join(lines, "\n") + "\n")
	}
	return b.String()
}

// unreadTotal counts unread messages across every server.
func (m Model) unreadTotal() int {
	n := 0
	for _, s := range m.sessions {
		for _, u := range s.unread {
			n += u.count
		}
	}
	return n
}

// clipLines truncates each line of s to width cells, with a trailing "…".
func clipLines(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, width, "…")
	}
	return strings.Join(lines, "\n")
}

// serverLine renders a server header: a collapse arrow, a connection dot and
// the server, then on a second line the signed-in name or what the server is
// waiting for, both clipped to the sidebar width.
func (m Model) serverLine(sid, width int) string {
	s := m.session(sid)
	if s == nil {
		return ""
	}
	arrow := "▾"
	if s.collapsed {
		arrow = "▸"
	}
	var dot, note string
	switch {
	case s.authed:
		dot = lipgloss.NewStyle().Foreground(good).Render("●")
		note = s.selfName()
	case s.needsSignIn:
		dot = lipgloss.NewStyle().Foreground(bad).Render("●")
		note = "sign in ↵"
	case s.connecting || s.reconnecting || s.client != nil:
		dot = lipgloss.NewStyle().Foreground(warn).Render(m.spinner())
		note = "connecting…"
	default:
		dot = dimStyle.Render("●")
		note = "offline · r"
	}
	// The name sits on its own line under the server, so neither has to
	// share the narrow sidebar.
	head := arrow + " " + dot + " " + serverStyle.Render(s.label())
	sub := "    " + dimStyle.Render(note)
	if width > 0 {
		head = ansi.Truncate(head, width, "…")
		sub = ansi.Truncate(sub, width, "…")
	}
	return head + "\n" + sub
}

// roomName returns the label for a room. A private room with exactly two
// members is a one-to-one chat, so it shows the other person's name instead of
// the server title, which may name both people.
func roomName(s *session, r *state.Room) string {
	if r == nil {
		return ""
	}
	if id, ok := directPeer(s, r); ok {
		return s.store.DisplayName(id, r.Members[id])
	}
	return r.Title()
}

// directPeer returns the other member of a private one-to-one room, reporting
// false for a room with any other shape.
func directPeer(s *session, r *state.Room) (string, bool) {
	if s == nil || r == nil || !r.Record.Private || s.store.You == nil || len(r.Members) != 2 {
		return "", false
	}
	for id := range r.Members {
		if id != s.store.You.UserID {
			return id, true
		}
	}
	return "", false
}

// roomLine renders a room row: its title, bold when it has unread messages,
// with an unread badge right-aligned in width ("@" when you were mentioned).
func (m Model) roomLine(it roomItem, width int) string {
	s := m.session(it.sid)
	if s == nil {
		return ""
	}
	prefix := ""
	title := it.id
	if !it.joined {
		if r := s.store.NotJoinedRoom(it.id); r != nil {
			title = roomName(s, r)
			if r.Record.ParentRoomID != "" {
				prefix = "↳ "
			}
		}
		return dimStyle.Render(prefix + title + "  +")
	}
	if r := s.store.Room(it.id); r != nil {
		title = roomName(s, r)
		if r.Record.ParentRoomID != "" {
			prefix = "↳ "
		}
	}
	muted := s.store.IsMuted(it.id)
	u := s.unread[it.id]
	var badge string
	switch {
	case u != nil && u.mention:
		badge = accentStyle.Bold(true).Render(fmt.Sprintf("@%d", u.count))
	case u != nil && !muted:
		badge = dimStyle.Render(fmt.Sprintf("%d", u.count))
	}
	name := prefix + title
	switch {
	case badge != "":
		name = unreadStyle.Render(name)
	case muted:
		name = roomDimStyle.Render(name)
	}
	if badge == "" || width <= 0 {
		return name
	}
	bw := lipgloss.Width(badge)
	nameWidth := width - bw - 1
	if nameWidth < 1 {
		return name
	}
	name = ansi.Truncate(name, nameWidth, "…")
	pad := width - lipgloss.Width(name) - bw
	return name + strings.Repeat(" ", pad) + badge
}

func (m Model) helpLine() string {
	switch m.chat.focus {
	case focusRooms:
		return "? help · ↑/↓ select · enter chat/join · space collapse · a add server · X remove · tab messages · N notices"
	case focusMessages:
		return "? help · k/j select · r reply · o open link · e edit · d delete · + react · pgup/pgdn scroll · g/G top/bottom · i compose"
	default:
		if m.chat.composerContext() {
			return "enter send · alt+enter newline · esc cancel · /help commands"
		}
		return "enter send · alt+enter newline · tab rooms · esc messages · /help commands"
	}
}

func (m Model) statusLine() string {
	var parts []string
	if m.status != "" {
		if m.isErr {
			parts = append(parts, statusErr.Render(m.status))
		} else {
			parts = append(parts, statusOk.Render(m.status))
		}
	}
	if r := m.cur().store.Room(m.chat.roomID); r != nil && r.Loading {
		label := "loading older messages…"
		if !r.Loaded {
			label = "loading messages…"
		}
		parts = append(parts, dimStyle.Render(m.spinner()+" "+label))
	}
	return strings.Join(parts, dimStyle.Render(" · "))
}

// bottomBar is the last row: key help on the left, status on the right. The
// status wins the space when both do not fit, and help is cut short.
func (m Model) bottomBar() string {
	status := m.statusLine()
	if m.width <= 0 {
		if status == "" {
			return helpStyle.Render(m.helpLine())
		}
		return helpStyle.Render(m.helpLine()) + "  " + status
	}
	sw := lipgloss.Width(status)
	if sw > m.width {
		return m.clip(status)
	}
	room := m.width - sw
	if status != "" {
		room -= 2
	}
	help := ""
	if room > 0 {
		help = ansi.Truncate(helpStyle.Render(m.helpLine()), room, "…")
	}
	pad := m.width - lipgloss.Width(help) - sw
	if pad < 0 {
		pad = 0
	}
	return help + strings.Repeat(" ", pad) + status
}

// renderSelected marks a rendered message as selected with an accent bar in
// the gutter, keeping its own styling (code blocks, colors) readable. Only the
// gutter changes, so selecting cannot change the line count or the width.
func renderSelected(s string) string {
	return gutter(s, true)
}
