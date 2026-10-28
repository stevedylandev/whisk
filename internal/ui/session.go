package ui

import (
	"net/url"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stevedylandev/whisk/internal/client"
	"github.com/stevedylandev/whisk/internal/config"
	"github.com/stevedylandev/whisk/internal/state"
)

// session is one server connection: its saved settings, its websocket, its
// record store, and its reconnect state. Every saved server keeps a session
// open at once, so the sidebar shows live rooms from all of them.
type session struct {
	// id is a stable key that tags this session's messages, so replies from
	// a removed or replaced connection can be recognized and dropped.
	id  int
	cfg config.Server
	// saved marks a session persisted in the config file. A server being
	// added is unsaved until its first successful sign-in.
	saved bool

	client *client.Client
	store  *state.Store

	authed bool
	// everAuthed is set once a sign-in succeeded on this run, so a reconnect
	// may fall back to guest sign-in like before.
	everAuthed bool
	connecting bool
	// needsSignIn marks a server that cannot sign in on its own (no usable
	// token, or the token was rejected). It waits for the user to pick it in
	// the sidebar and go through sign-in.
	needsSignIn bool
	collapsed   bool

	reconnectAttempts int
	reconnecting      bool
	// noReconnect is set by a denied error (§1.1): the client waits for the
	// user to act instead of reconnecting automatically.
	noReconnect bool
	// retryAfter is the delay an unsolicited retry_after error asked for
	// (§1.1); it applies to the next reconnect attempt once.
	retryAfter time.Duration
	// lastAuthScheme is the scheme of the auth request in flight, so a
	// denied reply can drop a stale saved token (§3.2.1).
	lastAuthScheme string

	// idleSent records that this connection reported idle (§4.5.3).
	idleSent bool

	// unread counts live messages that arrived in rooms other than the open
	// one, by room_id; it clears when the room is opened.
	unread map[string]*roomUnread

	// serverHelp is the server's /help reply (§4.1), fetched for the help
	// modal once per connection.
	serverHelp serverHelp

	// pendingRoom is a room just joined, opened once the following room_list
	// makes its record available.
	pendingRoom string
	// openOnRooms opens this server's first room once its room list lands,
	// e.g. right after the server was added.
	openOnRooms bool
	// expectPrivate is set while a room_set with private:true is in flight,
	// for the §4.2.4 post-creation check.
	expectPrivate bool
	// pendingUpload is a file read from disk waiting for a write_url.
	pendingUpload *pendingUpload

	// logUnsupported is set once the server answered a room-less history
	// request with unsupported (§4.3); per-room queries are used instead.
	logUnsupported bool
	// logCatchUp holds the rooms a one-stream recovery (R4) is still
	// catching up; nil when none is running.
	logCatchUp map[string]bool
	// logTo is the before bound of the room-less forward stream in flight
	// ("" for unbounded).
	logTo string
}

func newSession(id int, cfg config.Server, saved bool) *session {
	return &session{id: id, cfg: cfg, saved: saved, store: state.New()}
}

// label is the short server name shown in the sidebar and status line.
func (s *session) label() string {
	return serverLabel(s.cfg.URL)
}

// serverLabel reduces a server URL to its host (and path, when it has one).
func serverLabel(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.TrimSuffix(raw, "/")
	}
	return u.Host + strings.TrimSuffix(u.Path, "/")
}

func (s *session) hasCap(name string) bool {
	return s.store.Server != nil && s.store.Server.HasCapability(name)
}

// selfName returns the authenticated user's display name on this server, or
// "?".
func (s *session) selfName() string {
	if s.store.You == nil {
		return "?"
	}
	return s.store.DisplayName(s.store.You.UserID, s.store.You)
}

// closeClient drops the connection without triggering a reconnect: the
// disconnect that follows no longer matches s.client and is ignored.
func (s *session) closeClient() {
	if s.client != nil {
		_ = s.client.Close()
		s.client = nil
	}
	s.authed = false
}

// --- model helpers ----------------------------------------------------------

// session returns the session with the given id, or nil when it was removed.
func (m *Model) session(id int) *session {
	for _, s := range m.sessions {
		if s.id == id {
			return s
		}
	}
	return nil
}

// cur returns the session of the open room. It is never nil: with no servers
// it is an empty placeholder, so views and commands need no nil checks.
func (m *Model) cur() *session {
	if s := m.session(m.chat.sid); s != nil {
		return s
	}
	if len(m.sessions) > 0 {
		return m.sessions[0]
	}
	return m.none
}

func (m *Model) isCur(s *session) bool {
	return s != nil && s == m.cur()
}

// addSession creates a session for a server and appends it to the list.
func (m *Model) addSession(cfg config.Server, saved bool) *session {
	m.nextID++
	s := newSession(m.nextID, cfg, saved)
	m.sessions = append(m.sessions, s)
	if m.chat.sid == 0 {
		m.chat.sid = s.id
	}
	return s
}

// findSession returns the session for a server URL, or nil.
func (m *Model) findSession(url string) *session {
	url = strings.TrimSpace(url)
	if u, err := config.NormalizeURL(url); err == nil {
		url = u
	}
	for _, s := range m.sessions {
		if s.cfg.URL == url {
			return s
		}
	}
	return nil
}

// dropSession closes a session and removes it from the list and, when it was
// saved, from the config file.
func (m *Model) dropSession(s *session) {
	s.closeClient()
	for i, x := range m.sessions {
		if x == s {
			m.sessions = append(m.sessions[:i], m.sessions[i+1:]...)
			break
		}
	}
	if s.saved {
		m.cfg.Remove(s.cfg.URL)
		m.saveConfig()
	}
	if m.auth.sess == s {
		m.auth.sess = nil
	}
	if m.chat.sid == s.id {
		m.chat.sid = 0
		m.chat.roomID = ""
		m.chat.selectedID = ""
		m.chat.replyTo = ""
		m.chat.editingID = ""
		m.chat.dirty = true
		if len(m.sessions) > 0 {
			m.chat.sid = m.sessions[0].id
		}
	}
	m.refreshRoomItems()
}

// persist writes a saved session's settings back to the config file.
func (m *Model) persist(s *session) {
	if !s.saved {
		return
	}
	m.cfg.Upsert(s.cfg)
	m.saveConfig()
}

// sessStatus sets the status line for an event on a session, naming the
// server when more than one is configured.
func (m *Model) sessStatus(s *session, msg string, isErr bool) {
	if len(m.sessions) > 1 {
		msg = s.label() + ": " + msg
	}
	m.setStatus(msg, isErr)
}

// defaultName is the display name prefilled for a new server: the one used on
// the other servers.
func (m *Model) defaultName() string {
	for _, s := range m.sessions {
		if s.cfg.Name != "" {
			return s.cfg.Name
		}
	}
	return ""
}

// connectSession dials a session's server.
func (m *Model) connectSession(s *session) tea.Cmd {
	s.connecting = true
	return connectCmd(s.id, s.cfg.URL, 0)
}

// roomUnread is a room's unread state for the sidebar.
type roomUnread struct {
	count   int
	mention bool // a message mentioned the signed-in user
}

// markUnread records a new message in a room that is not open.
func (s *session) markUnread(roomID string, mention bool) {
	if s.unread == nil {
		s.unread = make(map[string]*roomUnread)
	}
	u := s.unread[roomID]
	if u == nil {
		u = &roomUnread{}
		s.unread[roomID] = u
	}
	u.count++
	u.mention = u.mention || mention
}
