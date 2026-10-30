// Package ui implements the Bubble Tea interface for whisk.
//
// Model is the root model. It keeps one session per server (session.go), each
// with its own connection and state.Store, and shows one of two screens:
// sign-in (auth.go, auth_view.go) or chat (chat.go, chat_view.go). Network I/O
// runs in tea.Cmds (cmds.go); their results come back as messages that Update
// routes to connection.go (events, reconnects) and requests.go (replies).
// DEVELOPMENT.md at the repository root maps out the rest.
package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/stevedylandev/whisk/internal/config"
	"github.com/stevedylandev/whisk/internal/protocol"
)

// DefaultAgent is the implementation string sent with auth requests.
const DefaultAgent = "whisk/0.1"

type screen int

const (
	screenAuth screen = iota
	screenChat
)

type focusMode int

const (
	focusRooms focusMode = iota
	focusMessages
	focusComposer
)

// Model is the root Bubble Tea model.
type Model struct {
	screen screen

	// sessions holds one connection per saved server, in sidebar order.
	sessions []*session
	nextID   int
	// none is the placeholder returned by cur() when no server is configured.
	none *session

	cfg     config.Config
	cfgPath string
	agent   string

	quit bool

	auth authForm
	chat chatModel

	width, height int

	status   string
	isErr    bool
	statusAt time.Time
	// statusTickPending marks a scheduled check that clears a stale status.
	statusTickPending bool

	// spinFrame animates busy indicators while spinTickPending keeps a frame
	// tick scheduled; the tick stops once nothing is busy.
	spinFrame       int
	spinTickPending bool

	// Idle reporting (§4.5.3). Attention is the user's, so it is shared by
	// every connection; each session records whether it reported idle.
	lastInput       time.Time
	idleTickPending bool
}

type authForm struct {
	// sess is the server being added or signed in to; nil while the user is
	// still typing a new server URL.
	sess *session

	server textinput.Model
	prompt textinput.Model

	phase       authPhase
	promptKind  promptKind
	codeScheme  string // "email" or "external"
	state       string
	externalURL string
	// signupMode marks a flow started from the "create an account" section
	// of the chooser (server.signup, §3.2); the wire shape is unchanged.
	signupMode bool

	welcome string
	schemes []string
	signups []string
	// choice is the cursor in the method chooser, an index into
	// authChoices().
	choice     int
	errMsg     string
	connecting bool
	busy       bool

	md      *renderer
	mdWidth int
}

type chatModel struct {
	// sid is the session of the open room.
	sid      int
	roomID   string
	viewport viewport.Model
	composer textarea.Model
	focus    focusMode

	roomCursor int
	roomItems  []roomItem

	// confirmRemove is the session awaiting a y/n before it is removed.
	confirmRemove int
	// confirmDelete is the message awaiting a y/n before it is deleted.
	confirmDelete string

	renderer    *renderer
	dirty       bool
	stickBottom bool

	// Scroll anchor: the message at the top of the viewport and its offset
	// within the viewport. It is used to keep the view steady when older
	// history is prepended.
	anchorID     string
	anchorStart  int
	anchorOffset int

	// Message selection and actions.
	selectedID string
	msgPos     []msgPos
	replyTo    string
	editingID  string

	// emoji is the emoji picker, opened with + to react or /emoji to insert.
	emoji emojiPicker

	// showNotices toggles the transient-notices overlay (N key).
	showNotices bool

	// showHelp shows the help modal (? key, /help); helpCommands lists the
	// commands first, and helpScroll is its scroll offset.
	showHelp     bool
	helpCommands bool
	helpScroll   int

	// contextShown is whether the layout last made room for the composer's
	// reply/edit context line.
	contextShown bool

	// Activity (typing / read) throttle.
	lastTypingSent time.Time
	lastReadID     string
}

// roomItem is one sidebar row: a server header, a section label, or a room of
// that server. depth is a thread's nesting level under its parent room;
// top-level rooms have depth 0.
type roomItem struct {
	sid       int
	header    bool
	subheader bool   // a group label, such as "private"
	label     string // the subheader text
	id        string
	joined    bool
	depth     int
}

// selectable reports whether the cursor may rest on the row. Group labels are
// decoration, not rows the user can open.
func (it roomItem) selectable() bool {
	return !it.subheader
}

// msgPos maps a message_id to its starting line in the rendered viewport.
type msgPos struct {
	id    string
	start int
}

// NewModel builds the root model. Every saved server gets a session that
// connects in the background; flags override the matching server's settings,
// and a --server not in the config starts the add-server flow for it.
func NewModel(cfg config.Config, cfgPath, serverFlag, nameFlag, tokenFlag string) Model {
	m := Model{
		cfg:     cfg,
		cfgPath: cfgPath,
		agent:   DefaultAgent,
		none:    newSession(0, config.Server{}, false),
	}

	server := textinput.New()
	server.Placeholder = "wss://server.apron.chat/"
	server.Prompt = "server: "
	server.Focus()

	prompt := textinput.New()
	prompt.Prompt = "> "

	m.auth = authForm{server: server, prompt: prompt}

	composer := textarea.New()
	composer.Placeholder = "message…"
	composer.SetHeight(1)
	composer.ShowLineNumbers = false
	composer.CharLimit = 8000

	m.chat = chatModel{
		viewport: viewport.New(0, 0),
		composer: composer,
		// Start on the channel sidebar; tab moves into the messages and
		// composer.
		focus:    focusRooms,
		renderer: newRenderer(80),
	}

	for _, sc := range cfg.Servers {
		m.addSession(sc, true).connecting = true
	}

	var target *session
	if serverFlag != "" {
		target = m.findSession(serverFlag)
		if target == nil {
			url := strings.TrimSpace(serverFlag)
			if u, err := config.NormalizeURL(url); err == nil {
				url = u
			}
			target = m.addSession(config.Server{URL: url, Name: m.defaultName()}, false)
			target.connecting = true
			m.auth.sess = target
		}
	} else if len(m.sessions) > 0 {
		target = m.sessions[0]
	}
	if target != nil {
		if nameFlag != "" {
			target.cfg.Name = nameFlag
		}
		if tokenFlag != "" {
			target.cfg.Token = tokenFlag
		}
	}

	switch {
	case m.auth.sess != nil:
		m.screen = screenAuth
		m.auth.server.SetValue(m.auth.sess.cfg.URL)
		m.auth.connecting = true
	case len(m.sessions) == 0:
		m.screen = screenAuth
	default:
		m.screen = screenChat
	}
	m.refreshRoomItems()
	return m
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink}
	for _, s := range m.sessions {
		if s.connecting {
			cmds = append(cmds, connectCmd(s.id, s.cfg.URL, 0))
		}
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		m.chat.dirty = true

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quit = true
			for _, s := range m.sessions {
				s.closeClient()
			}
			return m, tea.Quit
		}
		if m.screen == screenChat {
			// Any key is attention: resume an idle connection (§4.5.3).
			cmds = append(cmds, m.noteInput()...)
		}
		cmds = append(cmds, m.routeMsg(msg)...)

	case tea.MouseMsg:
		if m.screen == screenChat {
			cmds = append(cmds, m.noteInput()...)
		}

	case idleTickMsg:
		cmds = append(cmds, m.onIdleTick()...)

	case statusTickMsg:
		m.statusTickPending = false
		ttl := statusTTL
		if m.isErr {
			ttl = statusErrTTL
		}
		if m.status != "" && m.chat.confirmRemove == 0 && m.chat.confirmDelete == "" && time.Since(m.statusAt) >= ttl {
			m.status = ""
			m.isErr = false
		}

	case spinTickMsg:
		m.spinTickPending = false
		m.spinFrame++

	case connectedMsg:
		if s := m.session(msg.sid); s != nil {
			cmds = append(cmds, m.onConnected(s, msg.c)...)
		} else {
			_ = msg.c.Close()
		}

	case connectErrMsg:
		if s := m.session(msg.sid); s != nil {
			cmds = append(cmds, m.onConnectErr(s, msg.err)...)
		}

	case eventMsg:
		if s := m.session(msg.sid); s != nil && s.client == msg.c {
			cmds = append(cmds, m.onEvent(s, msg.ev)...)
		}

	case disconnectMsg:
		if s := m.session(msg.sid); s != nil && s.client == msg.c {
			cmds = append(cmds, m.onDisconnect(s, msg.err)...)
		}

	case requestDoneMsg:
		if s := m.session(msg.sid); s != nil {
			cmds = append(cmds, m.handleRequestDone(s, msg))
		}

	case historyRetryMsg:
		if s := m.session(msg.sid); s != nil {
			// The read rate limit's wait is over: re-queue the page and
			// send it when the connection is free.
			s.histQueue = append([]historyJob{msg.job}, s.histQueue...)
			cmds = append(cmds, m.popHistory(s))
		}

	case serverHelpTimeoutMsg:
		if s := m.session(msg.sid); s != nil {
			m.onServerHelpTimeout(s, msg.gen)
		}

	case uploadDoneMsg:
		if msg.err != nil {
			m.setStatus("upload "+msg.path+": "+msg.err.Error(), true)
		} else {
			m.setStatus("uploaded "+msg.path, false)
		}

	default:
		cmds = append(cmds, m.routeMsg(msg)...)
	}

	if m.screen == screenChat {
		// Opening a room reads it.
		delete(m.cur().unread, m.chat.roomID)
		// A reply/edit context line above the composer, or a composer that
		// grew or shrank with its text, changes how many rows the message view
		// gets; keep the layout in step with it.
		if m.chat.viewport.Height > 0 {
			grew := m.chat.syncComposerHeight()
			contextChanged := m.chat.contextShown != m.chat.composerContext()
			if grew || contextChanged {
				if grew && m.chat.viewport.AtBottom() {
					// Keep the newest messages in view as the composer takes
					// rows from the message pane.
					m.chat.stickBottom = true
				}
				m.chat.contextShown = m.chat.composerContext()
				m.resize()
				m.chat.dirty = true
			}
		}
	}

	if m.screen == screenChat && m.chat.dirty {
		m.renderContent()
		if m.chat.viewport.AtBottom() && m.chat.roomID != "" {
			cmds = append(cmds, m.maybeSendRead())
		}
	}

	// Idle reporting needs a periodic check while signed in (§4.5.3).
	if m.screen == screenChat && !m.idleTickPending && m.anyReportsIdle() {
		m.idleTickPending = true
		cmds = append(cmds, tea.Tick(30*time.Second, func(t time.Time) tea.Msg { return idleTickMsg(t) }))
	}

	// Clear a status message once it has been shown for a while.
	if m.status != "" && !m.statusTickPending {
		m.statusTickPending = true
		cmds = append(cmds, tea.Tick(time.Second, func(time.Time) tea.Msg { return statusTickMsg{} }))
	}

	// Animate busy indicators while something is in flight.
	if !m.spinTickPending && m.busy() {
		m.spinTickPending = true
		cmds = append(cmds, tea.Tick(spinInterval, func(time.Time) tea.Msg { return spinTickMsg{} }))
	}

	// Re-render once a second while someone is typing so the indicator clears.
	if m.screen == screenChat && m.chat.roomID != "" &&
		len(m.cur().store.TypingUsers(m.chat.roomID, time.Now())) > 0 {
		cmds = append(cmds, tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} }))
	}

	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	if m.screen == screenAuth {
		return m.viewAuth()
	}
	return m.viewChat()
}

func (m *Model) routeMsg(msg tea.Msg) []tea.Cmd {
	switch m.screen {
	case screenAuth:
		return m.updateAuth(msg)
	case screenChat:
		return m.updateChat(msg)
	}
	return nil
}

func (m *Model) resize() {
	m.chat.resize(m.width, m.height)
}

func (m *Model) setStatus(s string, isErr bool) {
	m.status = s
	m.isErr = isErr
	m.statusAt = time.Now()
}

// How long a status message stays before it clears; errors stay longer.
const (
	statusTTL    = 5 * time.Second
	statusErrTTL = 10 * time.Second
)

// spinFrames animate busy indicators, advancing every spinInterval.
var spinFrames = spinner.MiniDot.Frames

const spinInterval = 120 * time.Millisecond

// spinner returns the current busy-indicator frame.
func (m Model) spinner() string {
	return spinFrames[m.spinFrame%len(spinFrames)]
}

// busy reports whether anything shown on screen is waiting: a sign-in step,
// a connection, or a history page for the open room.
func (m Model) busy() bool {
	if m.screen == screenAuth {
		return m.auth.busy || m.auth.connecting
	}
	for _, s := range m.sessions {
		if s.connecting || s.reconnecting || (s.client != nil && !s.authed && !s.needsSignIn) {
			return true
		}
	}
	r := m.cur().store.Room(m.chat.roomID)
	return r != nil && r.Loading
}

func (m *Model) saveConfig() {
	_ = config.Save(m.cfgPath, m.cfg)
}

// selfName returns the authenticated user's display name on the current
// server, or "?".
func (m *Model) selfName() string {
	return m.cur().selfName()
}

// composerText returns the trimmed composer value.
func (m *Model) composerText() string {
	return strings.TrimSpace(m.chat.composer.Value())
}

func (m *Model) hasCap(name string) bool {
	return m.cur().hasCap(name)
}

// requireCap reports whether the current server has a capability, and says
// so on the status line when it does not.
func (m *Model) requireCap(name string) bool {
	if m.hasCap(name) {
		return true
	}
	m.setStatus("server lacks the "+name+" capability", true)
	return false
}

// fromSelf reports whether a message was sent by the signed-in user.
func fromSelf(s *session, msg *protocol.Message) bool {
	return msg.From != nil && s.store.You != nil && msg.From.UserID == s.store.You.UserID
}

// mentionsSelf reports whether a message's text @mentions the signed-in user.
func mentionsSelf(s *session, msg *protocol.Message) bool {
	if s.store.You == nil || msg.Body == nil {
		return false
	}
	for _, t := range scanMentions(msg.Body.Text) {
		if t.ID == s.store.You.UserID {
			return true
		}
	}
	return false
}
