package ui

import (
	"net/url"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stevedylandev/whisk/internal/protocol"
	"github.com/stevedylandev/whisk/internal/state"
)

func (m *Model) updateChat(msg tea.Msg) []tea.Cmd {
	if key, ok := msg.(tea.KeyMsg); ok && m.chat.confirmRemove != 0 {
		// Any key answers the remove prompt; only y confirms.
		s := m.session(m.chat.confirmRemove)
		m.chat.confirmRemove = 0
		if s != nil && (key.String() == "y" || key.String() == "Y") {
			return m.removeServer(s)
		}
		m.setStatus("", false)
		return nil
	}

	if key, ok := msg.(tea.KeyMsg); ok && m.chat.confirmDelete != "" {
		// Any key answers the delete prompt; only y confirms.
		id := m.chat.confirmDelete
		m.chat.confirmDelete = ""
		if key.String() == "y" || key.String() == "Y" {
			return []tea.Cmd{m.deleteMessage(id)}
		}
		m.setStatus("", false)
		return nil
	}

	// The emoji picker takes every key while it is open, including the
	// letters that normally drive the panes: the user is typing a name.
	if key, ok := msg.(tea.KeyMsg); ok && m.chat.emoji.open {
		return m.handleEmojiKey(key)
	}

	// The help modal takes every key while it is open.
	if key, ok := msg.(tea.KeyMsg); ok && m.chat.showHelp {
		return m.handleHelpKey(key)
	}

	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "r" && m.chat.focus != focusComposer {
		// r retries the server under the cursor (or the open one) once it
		// has stopped reconnecting.
		s := m.cur()
		if m.chat.focus == focusRooms {
			s = m.cursorSession()
		}
		if s != nil && s.client == nil && !s.connecting && s.cfg.URL != "" {
			// A manual retry is the user action that re-enables reconnect.
			s.noReconnect = false
			s.needsSignIn = false
			s.reconnecting = true
			s.reconnectAttempts = 0
			m.sessStatus(s, "reconnecting…", false)
			return []tea.Cmd{m.connectSession(s)}
		}
	}

	c := &m.chat
	if key, ok := msg.(tea.KeyMsg); ok {
		// The notices overlay (N) works from any focus except the composer,
		// where every letter belongs to the textarea (typing "Nice" must not
		// toggle the overlay).
		if c.focus != focusComposer {
			switch key.String() {
			case "N":
				c.showNotices = !c.showNotices
				return nil
			case "?":
				return []tea.Cmd{m.openHelp(false)}
			case "esc":
				if c.showNotices {
					c.showNotices = false
					return nil
				}
			}
		}
		switch c.focus {
		case focusRooms:
			return m.handleRoomsKey(key)
		case focusMessages:
			return m.handleMessagesKey(key)
		case focusComposer:
			switch key.String() {
			case "enter":
				if len(m.composerText()) > 0 {
					cmds := m.sendTypingStop()
					cmds = append(cmds, m.sendComposer())
					return cmds
				}
				return nil
			case "alt+enter":
				c.composer.InsertString("\n")
				return nil
			case "tab":
				// While a /attach, /upload or /avatar path is being typed,
				// tab completes it; otherwise it cycles focus.
				if m.completePath() {
					return nil
				}
				m.setFocus(focusRooms)
				return nil
			case "esc":
				// esc first drops a reply or edit in progress, then leaves
				// the composer.
				if c.editingID != "" {
					c.editingID = ""
					c.composer.Reset()
					m.setStatus("edit cancelled", false)
					return nil
				}
				if c.replyTo != "" {
					c.replyTo = ""
					m.setStatus("", false)
					return nil
				}
				m.setFocus(focusMessages)
				return nil
			}
		}
	}

	if c.focus == focusComposer {
		before := c.composer.Value()
		var cmd tea.Cmd
		c.composer, cmd = c.composer.Update(msg)
		var cmds []tea.Cmd
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if before != c.composer.Value() {
			cmds = append(cmds, m.maybeSendTyping()...)
		}
		return cmds
	}
	return nil
}

func (m *Model) handleRoomsKey(key tea.KeyMsg) []tea.Cmd {
	var cmds []tea.Cmd
	var it *roomItem
	if m.chat.roomCursor < len(m.chat.roomItems) {
		it = &m.chat.roomItems[m.chat.roomCursor]
	}
	switch key.String() {
	case "a":
		return m.startAddServer()
	case "X":
		if s := m.cursorSession(); s != nil {
			m.chat.confirmRemove = s.id
			m.setStatus("remove "+s.label()+"? (y/n)", true)
		}
		return nil
	case " ":
		if it != nil && it.header {
			m.toggleCollapse(it.sid)
		}
		return nil
	}
	if it != nil && it.header && key.String() == "enter" {
		s := m.session(it.sid)
		if s != nil && !s.authed && s.needsSignIn {
			return m.startSignIn(s)
		}
		m.toggleCollapse(it.sid)
		return nil
	}
	switch key.String() {
	case "up", "k":
		if m.moveRoomCursor(-1) {
			cmds = append(cmds, m.openHighlightedRoom())
		}
	case "down", "j":
		if m.moveRoomCursor(1) {
			cmds = append(cmds, m.openHighlightedRoom())
		}
	case "enter":
		if it != nil {
			s := m.session(it.sid)
			if s == nil {
				break
			}
			if it.joined {
				// Already a member: the room is open (navigation switches
				// automatically), so enter hands focus to the messages.
				cmds = append(cmds, m.selectRoom(s, it.id))
				m.setFocus(focusMessages)
			} else {
				cmds = append(cmds, m.joinRoom(s, it.id))
			}
		}
	case "tab":
		m.setFocus(focusMessages)
	}
	return cmds
}

// moveRoomCursor steps the sidebar cursor in direction delta, skipping group
// labels, and reports whether it moved.
func (m *Model) moveRoomCursor(delta int) bool {
	for i := m.chat.roomCursor + delta; i >= 0 && i < len(m.chat.roomItems); i += delta {
		if m.chat.roomItems[i].selectable() {
			m.chat.roomCursor = i
			return true
		}
	}
	return false
}

// openHighlightedRoom switches the chat to the room under the cursor when it is
// already joined. Rooms that still need joining are left for enter.
func (m *Model) openHighlightedRoom() tea.Cmd {
	if m.chat.roomCursor >= len(m.chat.roomItems) {
		return nil
	}
	it := m.chat.roomItems[m.chat.roomCursor]
	s := m.session(it.sid)
	if it.header || !it.joined || s == nil {
		return nil
	}
	return m.selectRoom(s, it.id)
}

func (m *Model) joinRoom(s *session, id string) tea.Cmd {
	m.sessStatus(s, "joining "+id+"…", false)
	return roomJoinCmd(s, id)
}

// cursorSession returns the session of the sidebar row under the cursor, or
// the current one.
func (m *Model) cursorSession() *session {
	if m.chat.roomCursor < len(m.chat.roomItems) {
		if s := m.session(m.chat.roomItems[m.chat.roomCursor].sid); s != nil {
			return s
		}
	}
	if len(m.sessions) == 0 {
		return nil
	}
	return m.cur()
}

// toggleCollapse hides or shows a server's rooms in the sidebar.
func (m *Model) toggleCollapse(sid int) {
	if s := m.session(sid); s != nil {
		s.collapsed = !s.collapsed
		m.refreshRoomItems()
		m.cursorTo(sid, "", true)
	}
}

// removeServer forgets a server: it disconnects, leaves the sidebar, and is
// dropped from the config file. With no servers left, the add-server prompt
// opens.
func (m *Model) removeServer(s *session) []tea.Cmd {
	label := s.label()
	wasCur := m.isCur(s)
	m.dropSession(s)
	if len(m.sessions) == 0 {
		m.setFocus(focusRooms)
		cmds := m.startAddServer()
		m.setStatus("removed "+label, false)
		return cmds
	}
	m.setStatus("removed "+label, false)
	if wasCur {
		return []tea.Cmd{m.selectDefaultRoom(m.cur())}
	}
	return nil
}

func (m *Model) handleMessagesKey(key tea.KeyMsg) []tea.Cmd {
	var cmds []tea.Cmd
	switch key.String() {
	case "up", "k":
		if m.chat.viewport.AtTop() && m.selectionIsFirst() {
			cmds = append(cmds, m.loadOlder())
		} else {
			m.moveSelection(-1)
		}
	case "down", "j":
		m.moveSelection(1)
	case "pgup":
		if m.chat.viewport.AtTop() {
			cmds = append(cmds, m.loadOlder())
		} else {
			m.chat.viewport.ScrollUp(m.chat.viewport.Height)
			m.selectMessageAtTop()
		}
	case "pgdown":
		m.chat.viewport.ScrollDown(m.chat.viewport.Height)
		m.selectMessageAtTop()
	case "home", "g":
		m.selectFirstMessage()
	case "end", "G":
		m.selectLastMessage()
	case "r", "R":
		m.startReply()
	case "o":
		if cmd := m.openSelectedLink(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case "e":
		m.startEdit()
	case "d", "D":
		cmds = append(cmds, m.deleteSelected())
	case "+", "=":
		m.openEmojiPicker()
	case "tab":
		m.setFocus(focusComposer)
	case "esc":
		m.setFocus(focusRooms)
	case "enter", "i":
		m.setFocus(focusComposer)
	}
	return cmds
}

func (m *Model) setFocus(f focusMode) {
	if m.chat.focus != f {
		// Selection highlighting lives in the rendered message content, so a
		// focus change has to rebuild it.
		m.chat.dirty = true
	}
	m.chat.focus = f
	if f == focusComposer {
		m.chat.composer.Focus()
	} else {
		m.chat.composer.Blur()
	}
}

func (m *Model) selectRoom(s *session, id string) tea.Cmd {
	if m.chat.sid == s.id && m.chat.roomID == id {
		return m.loadHistoryIfNeeded()
	}
	m.chat.sid = s.id
	m.chat.roomID = id
	m.chat.dirty = true
	m.chat.stickBottom = true
	m.chat.replyTo = ""
	m.chat.editingID = ""
	m.chat.confirmDelete = ""
	m.chat.lastReadID = ""
	m.chat.lastTypingSent = time.Time{}
	m.refreshRoomItems()
	m.cursorTo(s.id, id, false)
	return m.loadHistoryIfNeeded()
}

// --- selection --------------------------------------------------------------

func (m *Model) selectionIndex() int {
	for i, p := range m.chat.msgPos {
		if p.id == m.chat.selectedID {
			return i
		}
	}
	return -1
}

func (m *Model) selectionIsFirst() bool { return m.selectionIndex() == 0 }

func (m *Model) moveSelection(delta int) {
	if len(m.chat.msgPos) == 0 {
		return
	}
	idx := m.selectionIndex()
	if idx < 0 {
		idx = len(m.chat.msgPos) - 1
	}
	idx += delta
	if idx < 0 {
		idx = 0
	}
	if idx >= len(m.chat.msgPos) {
		idx = len(m.chat.msgPos) - 1
	}
	m.chat.selectedID = m.chat.msgPos[idx].id
	m.chat.dirty = true
	m.scrollToSelection()
}

func (m *Model) scrollToSelection() {
	idx := m.selectionIndex()
	if idx < 0 {
		return
	}
	pos := m.chat.msgPos[idx]
	y := m.chat.viewport.YOffset
	h := m.chat.viewport.Height
	if pos.start < y {
		m.chat.viewport.SetYOffset(pos.start)
	} else if pos.start >= y+h {
		m.chat.viewport.SetYOffset(pos.start - h + 1)
	}
}

func (m *Model) selectFirstMessage() {
	if len(m.chat.msgPos) == 0 {
		return
	}
	m.chat.selectedID = m.chat.msgPos[0].id
	m.chat.dirty = true
	m.chat.viewport.GotoTop()
}

func (m *Model) selectLastMessage() {
	if len(m.chat.msgPos) == 0 {
		return
	}
	m.chat.selectedID = m.chat.msgPos[len(m.chat.msgPos)-1].id
	m.chat.dirty = true
	m.chat.viewport.GotoBottom()
}

// selectMessageAtTop moves the selection to the message whose header is the
// first one at or below the current viewport offset, so the selection marker
// stays visible while scrolling with pgup/pgdn.
func (m *Model) selectMessageAtTop() {
	if idx := m.chat.topMessageIndex(); idx >= 0 {
		m.chat.selectedID = m.chat.msgPos[idx].id
		m.chat.dirty = true
	}
}

// topMessageIndex returns the index in msgPos of the first message starting
// at or below the viewport's top line (the last message when none does), or
// -1 when nothing is rendered.
func (c *chatModel) topMessageIndex() int {
	if len(c.msgPos) == 0 {
		return -1
	}
	for i, p := range c.msgPos {
		if p.start >= c.viewport.YOffset {
			return i
		}
	}
	return len(c.msgPos) - 1
}

func (m *Model) selectedMessage() *protocol.Message {
	if m.chat.selectedID == "" {
		return nil
	}
	room := m.cur().store.Room(m.chat.roomID)
	if room == nil {
		return nil
	}
	return room.Messages[m.chat.selectedID]
}

// --- message actions --------------------------------------------------------

// linkPattern matches a bare web URL inside message text. Quotes, angle
// brackets, and backticks end the match so quoted or embedded links stay
// intact; trailing sentence punctuation is trimmed in textLinks.
var linkPattern = regexp.MustCompile("https?://[^\\s<>\"'`]+")

// textLinks returns the http(s) URLs in text, in order.
func textLinks(text string) []string {
	var out []string
	for _, raw := range linkPattern.FindAllString(text, -1) {
		if u := trimLink(raw); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// trimLink drops punctuation that ended the sentence rather than the URL, and
// closing brackets that have no matching opener in the link (as in a markdown
// link, "[text](url)"). Brackets inside a URL, such as a Wikipedia title, are
// kept.
func trimLink(raw string) string {
	u := strings.TrimRight(raw, ".,;:!?")
	for len(u) > 0 {
		last := u[len(u)-1]
		var open byte
		switch last {
		case ')':
			open = '('
		case ']':
			open = '['
		case '}':
			open = '{'
		default:
			return u
		}
		if strings.Count(u, string(open)) >= strings.Count(u, string(last)) {
			return u
		}
		u = u[:len(u)-1]
	}
	return u
}

// webURL reports whether raw is an absolute http(s) URL, so a message cannot
// make the client open a javascript: or file: link.
func webURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// openSelectedLink opens the first link in the selected message: a URL in its
// text, or an embed's URL when the text has none.
func (m *Model) openSelectedLink() tea.Cmd {
	msg := m.selectedMessage()
	if msg == nil {
		m.setStatus("no message selected", true)
		return nil
	}
	var links []string
	if msg.Body != nil {
		links = textLinks(msg.Body.Text)
		if len(links) == 0 {
			for _, e := range msg.Body.Embeds {
				if webURL(e.URL) {
					links = append(links, e.URL)
				}
			}
		}
	}
	if len(links) == 0 {
		m.setStatus("no link in the selected message", true)
		return nil
	}
	link := links[0]
	m.setStatus("opening "+link, false)
	return openBrowserCmd(link)
}

func (m *Model) startReply() {
	msg := m.selectedMessage()
	if msg == nil {
		m.setStatus("no message selected", true)
		return
	}
	m.chat.replyTo = msg.MessageID
	m.chat.editingID = ""
	// The composer's context line names who is being replied to.
	m.setFocus(focusComposer)
}

// ownSelection returns the selected message when the user may edit it: the
// server has the edit capability and the message is the user's own. verb
// names the action in the status line otherwise.
func (m *Model) ownSelection(verb string) *protocol.Message {
	msg := m.selectedMessage()
	if msg == nil {
		m.setStatus("no message selected", true)
		return nil
	}
	if !m.requireCap("edit") {
		return nil
	}
	if !fromSelf(m.cur(), msg) {
		m.setStatus("you can only "+verb+" your own messages", true)
		return nil
	}
	return msg
}

func (m *Model) startEdit() {
	msg := m.ownSelection("edit")
	if msg == nil {
		return
	}
	m.chat.editingID = msg.MessageID
	m.chat.replyTo = ""
	text := ""
	if msg.Body != nil {
		text = msg.Body.Text
	}
	m.chat.composer.SetValue(text)
	m.setFocus(focusComposer)
	m.setStatus("editing message (enter to save)", false)
}

// deleteSelected asks before deleting: deletion is irreversible, so a stray d
// only arms the prompt. deleteMessage runs when the user answers y.
func (m *Model) deleteSelected() tea.Cmd {
	msg := m.ownSelection("delete")
	if msg == nil {
		return nil
	}
	m.chat.confirmDelete = msg.MessageID
	m.setStatus("delete message? (y/n)", true)
	return nil
}

// deleteMessage sends the tombstone for a message the user confirmed. It
// re-checks that the confirmed message is still selected and still the user's.
func (m *Model) deleteMessage(id string) tea.Cmd {
	msg := m.ownSelection("delete")
	if msg == nil || msg.MessageID != id {
		m.setStatus("", false)
		return nil
	}
	return requestCmd(m.cur(), "message", "message", protocol.Message{
		MessageID: msg.MessageID,
		RoomID:    msg.RoomID,
		Deleted:   true,
	})
}

// toggleReact adds emoji to, or removes it from, the user's reactions on the
// selected message. A reactions request carries the complete set (§4.7).
func (m *Model) toggleReact(emoji string) tea.Cmd {
	return m.reactTo(m.chat.selectedID, emoji)
}

func toggleEmoji(set []string, emoji string) []string {
	for i, e := range set {
		if e == emoji {
			return append(append([]string{}, set[:i]...), set[i+1:]...)
		}
	}
	return append(append([]string{}, set...), emoji)
}

// refreshRoomItems rebuilds the sidebar: a header per server followed by its
// rooms, unless collapsed. Private rooms get their own "private" subheader so
// one-to-one chats do not mix into the room list. Threads nest under their
// parent room (§4.2), so the row order never makes a thread look like a child
// of an unrelated room. The cursor stays on the same row when it still exists,
// else it lands on the open room.
func (m *Model) refreshRoomItems() {
	var items []roomItem
	for _, s := range m.sessions {
		items = append(items, roomItem{sid: s.id, header: true})
		if s.collapsed {
			continue
		}
		public, private := splitPrivate(s, s.store.Rooms(), true)
		items = append(items, roomRows(s, public, true)...)
		if len(private) > 0 {
			items = append(items, roomItem{sid: s.id, subheader: true, label: "private"})
			items = append(items, roomRows(s, private, true)...)
		}
		items = append(items, roomRows(s, s.store.NotJoined(), false)...)
	}
	var old *roomItem
	if m.chat.roomCursor < len(m.chat.roomItems) {
		o := m.chat.roomItems[m.chat.roomCursor]
		old = &o
	}
	m.chat.roomItems = items
	m.chat.roomCursor = 0
	if old != nil && m.cursorTo(old.sid, old.id, old.header) {
		return
	}
	m.cursorTo(m.chat.sid, m.chat.roomID, m.chat.roomID == "")
}

// splitPrivate divides room ids into public and private lists, keeping their
// original order. Threads follow their own private flag; a thread whose parent
// lands in the other list simply becomes a top-level row there.
func splitPrivate(s *session, ids []string, joined bool) (public, private []string) {
	for _, id := range ids {
		var r *state.Room
		if joined {
			r = s.store.Room(id)
		} else {
			r = s.store.NotJoinedRoom(id)
		}
		if r != nil && r.Record.Private {
			private = append(private, id)
		} else {
			public = append(public, id)
		}
	}
	return public, private
}

// roomRows lists ids as sidebar rows, with each thread following its parent at
// one deeper level. A thread whose parent is not in the list stays a top-level
// row, so a room that is joined on its own (for example a thread of a room the
// user did not join) is still reachable.
func roomRows(s *session, ids []string, joined bool) []roomItem {
	listed := make(map[string]bool, len(ids))
	parentOf := make(map[string]string, len(ids))
	for _, id := range ids {
		listed[id] = true
		if joined {
			if r := s.store.Room(id); r != nil {
				parentOf[id] = r.Record.ParentRoomID
			}
		} else if r := s.store.NotJoinedRoom(id); r != nil {
			parentOf[id] = r.Record.ParentRoomID
		}
	}
	children := make(map[string][]string, len(ids))
	var roots []string
	for _, id := range ids {
		p := parentOf[id]
		if p != "" && p != id && listed[p] {
			children[p] = append(children[p], id)
		} else {
			roots = append(roots, id)
		}
	}
	var items []roomItem
	seen := make(map[string]bool, len(ids))
	var add func(id string, depth int)
	add = func(id string, depth int) {
		if seen[id] {
			return
		}
		seen[id] = true
		items = append(items, roomItem{sid: s.id, id: id, joined: joined, depth: depth})
		for _, child := range children[id] {
			add(child, depth+1)
		}
	}
	for _, id := range roots {
		add(id, 0)
	}
	// Defensive: a malformed parent cycle would otherwise hide its rooms.
	for _, id := range ids {
		add(id, 0)
	}
	return items
}

// cursorTo moves the sidebar cursor to a server header or room row, reporting
// whether the row exists.
func (m *Model) cursorTo(sid int, roomID string, header bool) bool {
	for i, it := range m.chat.roomItems {
		if it.sid == sid && it.header == header && it.selectable() && (header || it.id == roomID) {
			m.chat.roomCursor = i
			return true
		}
	}
	return false
}

// selectDefaultRoom keeps the open room of s when it still exists, or opens
// its first joined room.
func (m *Model) selectDefaultRoom(s *session) tea.Cmd {
	if m.isCur(s) && m.chat.roomID != "" && s.store.Room(m.chat.roomID) != nil {
		return m.loadHistoryIfNeeded()
	}
	joined := s.store.Rooms()
	if len(joined) == 0 {
		if m.isCur(s) {
			m.chat.roomID = ""
			m.chat.dirty = true
		}
		return nil
	}
	return m.selectRoom(s, joined[0])
}

// captureScrollAnchor remembers the message at the top of the viewport and how
// far down the viewport it sits, so prepending older history can keep the same
// content in place instead of making the view jump.
func (m *Model) captureScrollAnchor() {
	c := &m.chat
	c.anchorID = ""
	idx := c.topMessageIndex()
	if idx < 0 {
		return
	}
	p := c.msgPos[idx]
	c.anchorID = p.id
	c.anchorStart = p.start
	c.anchorOffset = p.start - c.viewport.YOffset
}
