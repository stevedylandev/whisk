package ui

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/stevedylandev/whisk/internal/config"
	"github.com/stevedylandev/whisk/internal/protocol"
)

type authPhase int

const (
	phaseServer authPhase = iota
	phaseChoose
	phasePrompt
)

type promptKind int

const (
	promptName promptKind = iota
	promptToken
	promptEmail
	promptCode
)

func (m *Model) updateAuth(msg tea.Msg) []tea.Cmd {
	// esc backs out of adding a server (or signing in to one) to the chat,
	// unless there is nothing to go back to.
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" &&
		m.auth.phase != phasePrompt && m.canCancelAuth() {
		m.cancelAuth()
		return nil
	}
	switch m.auth.phase {
	case phaseServer:
		return m.updateAuthServer(msg)
	case phaseChoose:
		return m.updateAuthChoose(msg)
	case phasePrompt:
		return m.updateAuthPrompt(msg)
	}
	return nil
}

func (m *Model) updateAuthServer(msg tea.Msg) []tea.Cmd {
	var cmds []tea.Cmd
	if key, ok := msg.(tea.KeyMsg); ok && !m.auth.connecting {
		if key.String() == "enter" {
			url := strings.TrimSpace(m.auth.server.Value())
			if url == "" {
				m.auth.errMsg = "enter a server URL (e.g. wss://server.apron.chat/ or https://…)"
				return nil
			}
			return m.submitServer(url)
		}
	}
	var cmd tea.Cmd
	m.auth.server, cmd = m.auth.server.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	return cmds
}

// submitServer starts signing in to the server typed on the server prompt. A
// new server gets an unsaved session, kept only once sign-in succeeds.
func (m *Model) submitServer(raw string) []tea.Cmd {
	url, err := config.NormalizeURL(raw)
	if err != nil {
		m.auth.errMsg = err.Error()
		return nil
	}
	m.auth.server.SetValue(url)
	if prev := m.auth.sess; prev != nil && prev.cfg.URL != url && !prev.saved {
		m.dropSession(prev)
	}
	s := m.findSession(url)
	if s != nil && s.authed {
		m.auth.errMsg = "already signed in to " + s.label()
		return nil
	}
	if s == nil {
		s = m.addSession(config.Server{URL: url, Name: m.defaultName()}, false)
	}
	m.auth.sess = s
	m.auth.errMsg = ""
	s.noReconnect = false // the user acted: reconnect is allowed again
	s.reconnecting = false
	if s.client != nil && s.store.Server != nil {
		// Already connected and greeted: go straight to the chooser.
		m.loadAuthChoices(s)
		m.auth.phase = phaseChoose
		m.auth.connecting = false
		return nil
	}
	m.auth.connecting = true
	m.setStatus("connecting…", false)
	if s.connecting {
		return nil
	}
	return []tea.Cmd{m.connectSession(s)}
}

// startAddServer opens the server prompt to add a new server.
func (m *Model) startAddServer() []tea.Cmd {
	m.resetAuthForm()
	m.auth.server.SetValue("")
	m.screen = screenAuth
	m.setStatus("", false)
	return []tea.Cmd{textinput.Blink}
}

// startSignIn opens sign-in for a saved server that could not sign in on its
// own. A live connection goes straight to the method chooser.
func (m *Model) startSignIn(s *session) []tea.Cmd {
	m.resetAuthForm()
	m.auth.sess = s
	m.auth.server.SetValue(s.cfg.URL)
	m.screen = screenAuth
	s.noReconnect = false
	s.reconnecting = false
	m.setStatus("", false)
	if s.client != nil && s.store.Server != nil {
		m.loadAuthChoices(s)
		m.auth.phase = phaseChoose
		return nil
	}
	m.auth.connecting = true
	if s.connecting {
		return nil
	}
	return []tea.Cmd{m.connectSession(s)}
}

// canCancelAuth reports whether the sign-in screen has a chat to return to.
func (m *Model) canCancelAuth() bool {
	for _, s := range m.sessions {
		if s != m.auth.sess || s.saved {
			return true
		}
	}
	return false
}

// cancelAuth leaves the sign-in screen. A server that was being added is
// dropped; a saved one stays in the sidebar, still needing sign-in.
func (m *Model) cancelAuth() {
	if s := m.auth.sess; s != nil && !s.saved {
		m.dropSession(s)
	}
	m.resetAuthForm()
	m.setStatus("", false)
	if len(m.sessions) > 0 {
		m.screen = screenChat
		m.refreshRoomItems()
	}
}

// resetAuthForm clears the sign-in flow back to the server prompt.
func (m *Model) resetAuthForm() {
	m.auth.sess = nil
	m.auth.phase = phaseServer
	m.auth.errMsg = ""
	m.auth.busy = false
	m.auth.connecting = false
	m.auth.signupMode = false
	m.auth.externalURL = ""
	m.auth.codeScheme = ""
	m.auth.schemes = nil
	m.auth.signups = nil
	m.auth.welcome = ""
	m.auth.prompt.Blur()
	m.auth.prompt.SetValue("")
	m.auth.server.Focus()
}

func (m *Model) updateAuthChoose(msg tea.Msg) []tea.Cmd {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	if m.auth.busy {
		return nil
	}
	choices := m.authChoices()
	switch key.String() {
	case "g", "G":
		return m.pickScheme("guest", key.String() == "G")
	case "t", "T":
		return m.pickScheme("token", key.String() == "T")
	case "e", "E":
		return m.pickScheme("email", key.String() == "E")
	case "x", "X":
		return m.pickScheme("external", key.String() == "X")
	case "up", "k":
		if m.auth.choice > 0 {
			m.auth.choice--
		}
	case "down", "j":
		if m.auth.choice < len(choices)-1 {
			m.auth.choice++
		}
	case "enter":
		if m.auth.choice >= 0 && m.auth.choice < len(choices) {
			c := choices[m.auth.choice]
			return m.pickScheme(c.scheme, c.signup)
		}
	}
	return nil
}

// authChoice is one row of the method chooser.
type authChoice struct {
	scheme string
	signup bool // from server.signup: creates an account (§3.2)
}

// authChoices lists the chooser's rows: sign-in methods, then sign-up ones.
func (m Model) authChoices() []authChoice {
	out := make([]authChoice, 0, len(m.auth.schemes)+len(m.auth.signups))
	for _, s := range m.auth.schemes {
		out = append(out, authChoice{scheme: s})
	}
	for _, s := range m.auth.signups {
		out = append(out, authChoice{scheme: s, signup: true})
	}
	return out
}

// pickScheme starts signing in (or up) with a scheme, if the server offers
// it.
func (m *Model) pickScheme(scheme string, signup bool) []tea.Cmd {
	if !m.chooseScheme(scheme, signup) {
		return nil
	}
	switch scheme {
	case "guest":
		m.startPrompt(promptName)
	case "token":
		m.startPrompt(promptToken)
	case "email":
		m.startPrompt(promptEmail)
	case "external":
		m.auth.codeScheme = "external"
		m.auth.busy = true
		m.auth.errMsg = ""
		m.setStatus("requesting sign-in URL…", false)
		return []tea.Cmd{m.sendExternalProposal()}
	}
	return nil
}

// chooseScheme validates and selects an auth scheme, distinguishing sign-in
// (server.auth) from account creation (server.signup, §3.2). It reports
// whether the scheme is available.
func (m *Model) chooseScheme(scheme string, signup bool) bool {
	if signup {
		if !hasScheme(m.auth.signups, scheme) {
			return false
		}
		m.auth.signupMode = true
		return true
	}
	if !hasScheme(m.auth.schemes, scheme) {
		return false
	}
	m.auth.signupMode = false
	return true
}

func (m *Model) updateAuthPrompt(msg tea.Msg) []tea.Cmd {
	var cmds []tea.Cmd
	if key, ok := msg.(tea.KeyMsg); ok && !m.auth.busy {
		switch key.String() {
		case "enter":
			return m.submitPrompt()
		case "esc":
			m.auth.phase = phaseChoose
			m.auth.errMsg = ""
			m.auth.busy = false
			m.auth.externalURL = ""
			return nil
		}
	}
	var cmd tea.Cmd
	m.auth.prompt, cmd = m.auth.prompt.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	return cmds
}

// startPrompt moves to the prompt phase for a sign-in method.
func (m *Model) startPrompt(kind promptKind) {
	m.auth.promptKind = kind
	m.auth.phase = phasePrompt
	m.auth.errMsg = ""
	m.auth.busy = false
	switch kind {
	case promptName:
		m.auth.prompt.SetValue(m.authSess().cfg.Name)
		m.auth.prompt.EchoMode = textinput.EchoNormal
	case promptToken:
		m.auth.prompt.SetValue(m.authSess().cfg.Token)
		m.auth.prompt.EchoMode = textinput.EchoPassword
	default:
		m.auth.prompt.SetValue("")
		m.auth.prompt.EchoMode = textinput.EchoNormal
	}
	m.auth.prompt.Placeholder = placeholderFor(kind)
	m.focusPrompt()
}

func (m *Model) submitPrompt() []tea.Cmd {
	val := strings.TrimSpace(m.auth.prompt.Value())
	switch m.auth.promptKind {
	case promptName:
		if val == "" {
			m.auth.errMsg = "enter a display name"
			return nil
		}
		s := m.authSess()
		s.cfg.Name = val
		return []tea.Cmd{m.sendAuthScheme(s, "guest")}
	case promptToken:
		if val == "" {
			m.auth.errMsg = "enter a token"
			return nil
		}
		s := m.authSess()
		s.cfg.Token = val
		return []tea.Cmd{m.sendAuthScheme(s, "token")}
	case promptEmail:
		if val == "" {
			m.auth.errMsg = "enter your email address"
			return nil
		}
		m.auth.busy = true
		m.setStatus("sending…", false)
		return []tea.Cmd{m.sendEmailProposal(val)}
	case promptCode:
		if val == "" {
			m.auth.errMsg = "enter the code or token"
			return nil
		}
		m.auth.busy = true
		m.setStatus("verifying…", false)
		return []tea.Cmd{m.sendCodeApproval(val)}
	}
	return nil
}

func (m *Model) defaultScheme() string {
	s := m.auth.schemes
	if m.authSess().cfg.Token != "" && hasScheme(s, "token") {
		return "token"
	}
	if hasScheme(s, "guest") {
		return "guest"
	}
	if hasScheme(s, "email") {
		return "email"
	}
	if hasScheme(s, "external") {
		return "external"
	}
	return ""
}

func (m *Model) focusPrompt() {
	m.auth.server.Blur()
	m.auth.prompt.Focus()
}

// validateExternalURL rejects an external sign-in url that is not https, or
// http on a loopback host (§4.11.5, §5.4). A server must not be able to make
// the client open a javascript:, file:, or other scheme.
func validateExternalURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid sign-in URL: %v", err)
	}
	switch u.Scheme {
	case "https":
		if u.Host == "" {
			return fmt.Errorf("invalid sign-in URL (no host)")
		}
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("external sign-in URL must be https")
	default:
		return fmt.Errorf("external sign-in URL must be https")
	}
}

// isLoopbackHost reports whether host is localhost or a loopback address.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func placeholderFor(k promptKind) string {
	switch k {
	case promptName:
		return "display name"
	case promptToken:
		return "bearer token"
	case promptEmail:
		return "you@example.com"
	case promptCode:
		return "code or link token"
	}
	return ""
}

// hasScheme reports whether the server offers an auth scheme.
func hasScheme(schemes []string, want string) bool { return slices.Contains(schemes, want) }

// autoScheme picks the scheme a session can sign in with on its own: the
// saved token, else guest when the user signed in as a guest before. Other
// schemes need the user and are chosen on the sign-in screen instead.
func autoScheme(s *session) string {
	srv := s.store.Server
	if srv == nil {
		return ""
	}
	if s.cfg.Token != "" && hasScheme(srv.Auth, "token") {
		return "token"
	}
	if hasScheme(srv.Auth, "guest") && (s.everAuthed || s.cfg.Name != "") {
		return "guest"
	}
	return ""
}

// sendAuthScheme sends a direct sign-in for a token/guest scheme.
func (m *Model) sendAuthScheme(s *session, scheme string) tea.Cmd {
	params := protocol.AuthParams{Scheme: scheme, Name: s.cfg.Name, Agent: m.agent}
	if scheme == "token" {
		params.Token = s.cfg.Token
	}
	s.lastAuthScheme = scheme
	return requestCmd(s, "auth", "auth", params)
}

// sendEmailProposal proposes an email sign-in (§4.11.4).
func (m *Model) sendEmailProposal(email string) tea.Cmd {
	m.auth.codeScheme = "email"
	m.auth.state = protocol.NewID()
	return requestCmd(m.authSess(), "auth_email", "auth", protocol.AuthParams{
		Scheme: "email",
		Email:  email,
		State:  m.auth.state,
	})
}

// sendExternalProposal proposes an external (web page) sign-in (§4.11.5).
func (m *Model) sendExternalProposal() tea.Cmd {
	m.auth.codeScheme = "external"
	m.auth.state = protocol.NewID()
	return requestCmd(m.authSess(), "auth_external", "auth", protocol.AuthParams{
		Scheme: "external",
		State:  m.auth.state,
	})
}

// sendCodeApproval approves a pending email/external proposal with a token.
func (m *Model) sendCodeApproval(code string) tea.Cmd {
	s := m.authSess()
	s.lastAuthScheme = m.auth.codeScheme
	return requestCmd(s, "auth_code", "auth", protocol.AuthParams{
		Scheme: m.auth.codeScheme,
		Token:  code,
	})
}

// finishAuth applies a successful sign-in. A server signed in from the
// sign-in screen is saved and opened; a background one just comes online.
func (m *Model) finishAuth(s *session, res protocol.AuthResult) tea.Cmd {
	s.store.ApplyAuth(&res)
	s.authed = true
	s.everAuthed = true
	s.needsSignIn = false
	s.noReconnect = false
	// §3.2.1: a successful auth result MAY carry a token. Only persist it when
	// the server issued (or rotated) one; otherwise keep the saved token, so a
	// token sign-in that returns no token does not erase it.
	if res.Token != "" {
		s.cfg.Token = res.Token
	}
	if m.isAuthTarget(s) {
		s.saved = true
		m.auth.sess = nil
		m.auth.signupMode = false
		m.auth.busy = false
		m.screen = screenChat
		m.setFocus(focusRooms)
		m.openSession(s)
	}
	m.persist(s)
	m.lastInput = time.Now() // a connection starts attended (§4.5.3)
	s.idleSent = false
	m.sessStatus(s, fmt.Sprintf("connected as %s", s.selfName()), false)
	m.refreshRoomItems()
	if s.hasCap("rooms") {
		return roomListCmd(s)
	}
	// No rooms: the room-less history query loads the default room's
	// backlog before any live message reveals its id (R2).
	return m.syncLog(s)
}

// openSession makes s the current server with no room open yet; its first
// room opens once the room list lands.
func (m *Model) openSession(s *session) {
	m.chat.sid = s.id
	m.chat.roomID = ""
	m.chat.selectedID = ""
	m.chat.replyTo = ""
	m.chat.editingID = ""
	m.chat.lastReadID = ""
	m.chat.dirty = true
	m.cursorTo(s.id, "", true)
}

// loadAuthChoices copies a session's offered sign-in methods to the form.
func (m *Model) loadAuthChoices(s *session) {
	srv := s.store.Server
	if srv == nil {
		return
	}
	m.auth.schemes = srv.Auth
	m.auth.signups = srv.Signup
	m.auth.welcome = srv.Welcome
	// Start the cursor on the method enter used to pick by default.
	m.auth.choice = 0
	def := m.defaultScheme()
	for i, c := range m.authChoices() {
		if !c.signup && c.scheme == def {
			m.auth.choice = i
			break
		}
	}
}

// authSess returns the session the sign-in screen works on, or the empty
// placeholder.
func (m *Model) authSess() *session {
	if m.auth.sess != nil {
		return m.auth.sess
	}
	return m.none
}

// onAuthEmailSent moves to the code prompt once the server accepted an email
// sign-in proposal (§4.11.4).
func (m *Model) onAuthEmailSent(s *session) {
	if !m.isAuthTarget(s) {
		return
	}
	m.auth.busy = false
	m.auth.codeScheme = "email"
	m.startPrompt(promptCode)
	m.setStatus("check your email and enter the code", false)
}

// onAuthExternal opens the server's sign-in page, after checking its URL is
// safe to open, and moves to the code prompt (§4.11.5).
func (m *Model) onAuthExternal(s *session, result json.RawMessage) tea.Cmd {
	if !m.isAuthTarget(s) {
		return nil
	}
	m.auth.busy = false
	var res protocol.AuthResult
	if err := json.Unmarshal(result, &res); err != nil {
		m.setStatus("bad auth result: "+err.Error(), true)
		return nil
	}
	var cmd tea.Cmd
	if res.URL != "" {
		if err := validateExternalURL(res.URL); err != nil {
			m.auth.errMsg = err.Error()
			m.setStatus(err.Error(), true)
			m.auth.phase = phaseChoose
			return nil
		}
		m.auth.externalURL = res.URL
		cmd = openBrowserCmd(res.URL)
	}
	m.auth.codeScheme = "external"
	m.startPrompt(promptCode)
	m.setStatus("sign in in your browser, then enter the code", false)
	return cmd
}
