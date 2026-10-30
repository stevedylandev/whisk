package ui

import (
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/stevedylandev/whisk/internal/protocol"
)

// helpEntry is one row of the help modal: a key or command and what it does.
type helpEntry struct{ key, desc string }

type helpSection struct {
	title   string
	entries []helpEntry
}

var helpKeys = []helpSection{
	{"rooms", []helpEntry{
		{"↑/↓ k/j", "select a room (opens joined rooms)"},
		{"enter", "focus messages, or join"},
		{"space", "collapse/expand a server"},
		{"a", "add a server"},
		{"X", "remove the selected server"},
		{"r", "reconnect a server that gave up"},
	}},
	{"messages", []helpEntry{
		{"k/j ↑/↓", "select previous / next message"},
		{"pgup/pgdn", "page (loads older history)"},
		{"g / G", "top / bottom"},
		{"r", "reply to the selected message"},
		{"o", "open a link in the selected message"},
		{"e", "edit your selected message"},
		{"d", "delete your selected message (asks y/n)"},
		{"+", "pick an emoji reaction (type to search)"},
		{"i / enter", "focus the composer"},
	}},
	{"composer", []helpEntry{
		{"enter", "send"},
		{"alt+enter", "newline"},
		{"tab", "complete a path (/attach, /avatar), else focus rooms"},
		{"esc", "cancel a reply or edit, else focus messages"},
	}},
	{"anywhere", []helpEntry{
		{"tab", "cycle rooms → messages → composer"},
		{"?", "this help"},
		{"N", "toggle the notices overlay"},
		{"ctrl+c", "quit"},
	}},
}

var helpCommands = helpSection{"commands", []helpEntry{
	{"/nick NAME", "change your display name"},
	{"/status [VALUE|off]", "show or set presence"},
	{"/mute [ROOM|*] [SECONDS|off]", "mute a room, or * for all"},
	{"/join ID", "join a room"},
	{"/leave [ID]", "leave this (or the named) room"},
	{"/new [--private] TITLE", "create a room"},
	{"/thread TITLE", "start a thread in this room"},
	{"/move ROOM", "move the selected message"},
	{"/react EMOJI", "react to the selected message"},
	{"/emoji [QUERY]", "insert an emoji into your message"},
	{"/attach PATH [caption]", "attach a file (~/ expands; tab completes)"},
	{"/avatar [PATH]", "set (or clear) your avatar"},
	{"/server CMD [ARGS]", "send /CMD to the server, even one the client handles"},
	{"//text", "send a message starting with /"},
}}

// clientCommands are the commands dispatchCommand handles itself. A server
// command with one of these names is reachable only through /server. /avatar
// is absent: the client sends it on to the server (§4.8.6).
var clientCommands = map[string]bool{
	"help": true, "nick": true, "status": true, "mute": true, "join": true,
	"leave": true, "new": true, "thread": true, "move": true, "react": true,
	"emoji": true, "attach": true, "upload": true, "server": true, "s": true,
}

// serverHelp tracks a session's fetch of the server's /help reply (§4.1).
// The reply is a ~private notice, not a result, so the client claims the
// first one that arrives in the asked room while the fetch is open. The
// notice and the result travel separate paths, so either may land first; a
// result without a notice waits a short grace period before giving up.
type serverHelp struct {
	state helpState
	room  string
	text  string
	err   string
	// gen tells this fetch's timeout apart from an older one's.
	gen int
}

type helpState int

const (
	helpIdle helpState = iota
	helpPending
	helpDone
	helpFailed
)

// serverHelpGrace is how long a successful /help result waits for its notice.
const serverHelpGrace = 3 * time.Second

type serverHelpTimeoutMsg struct{ sid, gen int }

// fetchServerHelp sends /help to the current server unless it already has an
// answer or one in flight. A read-only user cannot run commands (§3.2 guest
// read-only), so the local help has to do there.
func (m *Model) fetchServerHelp() tea.Cmd {
	s := m.cur()
	if !m.hasCap("command") || !s.canWrite() || s.client == nil || s.serverHelp.state != helpIdle {
		return nil
	}
	s.serverHelp = serverHelp{state: helpPending, room: m.chat.roomID, gen: s.serverHelp.gen + 1}
	return sessionRequest(s, "server_help", m.chat.roomID, "command", protocol.Message{
		RoomID: m.chat.roomID,
		Body:   &protocol.Body{Text: "/help"},
	})
}

// takeServerHelp claims pm as the server's /help reply when a fetch is open
// and pm is a ~private notice in the asked room. A claimed notice is shown in
// the help modal instead of the chat.
func (m *Model) takeServerHelp(s *session, pm *protocol.Message) bool {
	h := &s.serverHelp
	if h.state != helpPending || pm.MessageID != "" || pm.RoomID != h.room ||
		pm.From == nil || pm.From.UserID != "~private" || pm.Body == nil {
		return false
	}
	h.state = helpDone
	h.text = pm.Body.Text
	return true
}

// onServerHelpDone handles the /help result: an error ends the fetch, and a
// success gives the notice a grace period if it has not arrived yet.
func (m *Model) onServerHelpDone(s *session, msg requestDoneMsg) tea.Cmd {
	h := &s.serverHelp
	if h.state != helpPending {
		return nil
	}
	switch {
	case msg.err != nil:
		h.state, h.err = helpFailed, msg.err.Error()
	case msg.rpcErr != nil:
		h.state, h.err = helpFailed, msg.rpcErr.Message
	default:
		sid, gen := s.id, h.gen
		return tea.Tick(serverHelpGrace, func(time.Time) tea.Msg {
			return serverHelpTimeoutMsg{sid: sid, gen: gen}
		})
	}
	return nil
}

func (m *Model) onServerHelpTimeout(s *session, gen int) {
	if h := &s.serverHelp; h.state == helpPending && h.gen == gen {
		h.state, h.err = helpFailed, "the server sent no command list"
	}
}

// serverCommandName finds the command a server help line describes: the
// first /name in it.
var serverCommandName = regexp.MustCompile("(?:^|[\\s`*_(\\[])/([A-Za-z][\\w-]*)")

// serverHelpSection renders the server's /help reply for the modal. Lines
// whose command the client handles itself are marked, since typing that
// command runs the client's version.
func (m Model) serverHelpSection() []string {
	h := m.cur().serverHelp
	switch h.state {
	case helpIdle:
		return []string{dimStyle.Render("not connected")}
	case helpPending:
		return []string{dimStyle.Render("loading…")}
	case helpFailed:
		return []string{dimStyle.Render(h.err)}
	}
	var out []string
	for _, ln := range strings.Split(strings.TrimSpace(h.text), "\n") {
		ln = strings.TrimRight(ln, " ")
		if ln == "" {
			continue
		}
		plain := strings.NewReplacer("`", "", "**", "").Replace(ln)
		line := plain
		if mm := serverCommandName.FindStringSubmatch(plain); mm != nil && clientCommands[strings.ToLower(mm[1])] {
			line += dimStyle.Render("  (client's /" + mm[1] + " wins; use /server " + mm[1] + ")")
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return []string{dimStyle.Render("the server listed no commands")}
	}
	return out
}

// helpContent renders the modal body: commands first when opened from /help,
// key bindings first when opened with ?.
func (m Model) helpContent() string {
	sections := append([]helpSection{}, helpKeys...)
	if m.chat.helpCommands {
		sections = append([]helpSection{helpCommands}, sections...)
	} else {
		sections = append(sections, helpCommands)
	}
	keyWidth := 0
	for _, s := range sections {
		for _, e := range s.entries {
			if w := ansi.StringWidth(e.key); w > keyWidth {
				keyWidth = w
			}
		}
	}
	var b strings.Builder
	for i, s := range sections {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(titleStyle.Render(s.title) + "\n")
		for _, e := range s.entries {
			pad := strings.Repeat(" ", keyWidth-ansi.StringWidth(e.key))
			b.WriteString(accentStyle.Render(e.key) + pad + "  " + dimStyle.Render(e.desc) + "\n")
		}
		if s.title == "commands" && m.hasCap("command") {
			b.WriteString(dimStyle.Render("other /commands go to the server") + "\n")
			b.WriteString("\n" + titleStyle.Render("server commands") + "\n")
			for _, ln := range m.serverHelpSection() {
				b.WriteString(ln + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

var helpModalStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 2)

// helpBodyRows is how many content rows the modal shows at this height. It
// leaves the header and bottom bar of the chat frame (two rows short of the
// terminal) uncovered.
func (m Model) helpBodyRows() int {
	frameH := helpModalStyle.GetVerticalFrameSize() + 2 // footer and its gap
	rows := m.height - 2 - 4 - frameH
	if rows < 3 {
		rows = 3
	}
	return rows
}

// helpMaxScroll is the largest useful scroll offset for the modal.
func (m Model) helpMaxScroll() int {
	n := strings.Count(m.helpContent(), "\n") + 1 - m.helpBodyRows()
	if n < 0 {
		return 0
	}
	return n
}

// helpModal renders the help popup sized to fit the screen, scrolled by
// helpScroll.
func (m Model) helpModal() string {
	lines := strings.Split(m.helpContent(), "\n")
	rows := m.helpBodyRows()
	start := m.chat.helpScroll
	if max := m.helpMaxScroll(); start > max {
		start = max
	}
	end := start + rows
	if end > len(lines) {
		end = len(lines)
	}
	body := strings.Join(lines[start:end], "\n")
	// Size the box to the widest line of all the content, so it does not
	// change width as it scrolls.
	contentW := 0
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > contentW {
			contentW = w
		}
	}
	footer := helpStyle.Render("esc close")
	if m.helpMaxScroll() > 0 {
		footer = helpStyle.Render("j/k scroll · esc close")
	}
	if maxW := m.width - 4 - helpModalStyle.GetHorizontalFrameSize(); maxW > 0 {
		body = clipLines(body, maxW)
		if contentW > maxW {
			contentW = maxW
		}
	}
	return helpModalStyle.Width(contentW + helpModalStyle.GetHorizontalPadding()).Render(body + "\n\n" + footer)
}

// overlay draws fg centered over bg, replacing the cells it covers. bg is
// width columns wide.
func overlay(bg, fg string, width int) string {
	bgLines := strings.Split(bg, "\n")
	fgLines := strings.Split(fg, "\n")
	height := len(bgLines)
	fw := lipgloss.Width(fg)
	x := (width - fw) / 2
	y := (height - len(fgLines)) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	for i, fl := range fgLines {
		row := y + i
		if row >= len(bgLines) {
			break
		}
		bl := bgLines[row]
		if pad := x - ansi.StringWidth(bl); pad > 0 {
			bl += strings.Repeat(" ", pad)
		}
		left := ansi.Truncate(bl, x, "")
		right := ansi.TruncateLeft(bl, x+ansi.StringWidth(fl), "")
		bgLines[row] = left + "\x1b[0m" + fl + "\x1b[0m" + right
	}
	return strings.Join(bgLines, "\n")
}

// openHelp shows the help modal; commands puts the command list first. It
// asks the server for its commands the first time on each connection.
func (m *Model) openHelp(commands bool) tea.Cmd {
	m.chat.showHelp = true
	m.chat.helpCommands = commands
	m.chat.helpScroll = 0
	return m.fetchServerHelp()
}

// handleHelpKey handles keys while the help modal is open; it takes every
// key so nothing reaches the panes underneath.
func (m *Model) handleHelpKey(key tea.KeyMsg) []tea.Cmd {
	switch key.String() {
	case "esc", "?", "q", "enter":
		m.chat.showHelp = false
	case "j", "down":
		m.chat.helpScroll++
	case "k", "up":
		m.chat.helpScroll--
	case "pgdown", " ":
		m.chat.helpScroll += 10
	case "pgup":
		m.chat.helpScroll -= 10
	case "g", "home":
		m.chat.helpScroll = 0
	case "G", "end":
		m.chat.helpScroll = m.helpMaxScroll()
	}
	if max := m.helpMaxScroll(); m.chat.helpScroll > max {
		m.chat.helpScroll = max
	}
	if m.chat.helpScroll < 0 {
		m.chat.helpScroll = 0
	}
	return nil
}
