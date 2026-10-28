package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/stevedylandev/whisk/internal/state"
)

// Sign-in screen rendering: the server prompt, the method chooser, and the
// per-scheme prompts, drawn as a card centered on the screen.

func (m Model) viewAuth() string {
	switch m.auth.phase {
	case phaseChoose:
		return m.viewAuthChoose()
	case phasePrompt:
		return m.viewAuthPrompt()
	default:
		return m.viewAuthServer()
	}
}

func (m Model) authWidth() int {
	width := m.width
	if width <= 0 {
		width = 80
	}
	if width > 100 {
		width = 100
	}
	return width
}

func (m Model) viewAuthServer() string {
	f := m.auth
	var b strings.Builder
	subtitle := "Apron chat client"
	if m.canCancelAuth() {
		subtitle = "add a server"
	}
	// The wordmark needs room; a short terminal gets the one-line title.
	if m.height >= 20 {
		b.WriteString(titleStyle.Render(logo) + "\n\n" + helpStyle.Render("whisk · "+subtitle) + "\n\n")
	} else {
		b.WriteString(titleStyle.Render("whisk") + " — " + helpStyle.Render(subtitle) + "\n\n")
	}

	if f.connecting {
		b.WriteString(promptStyle.Render(m.spinner()+" connecting to "+f.server.Value()+"…") + "\n\n")
		b.WriteString(dimStyle.Render("waiting for the server greeting…"))
		b.WriteString("\n\n" + helpStyle.Render(strings.TrimPrefix(m.cancelHint()+" · ctrl+c: quit", " · ")))
		return m.authFrame(b.String())
	}

	b.WriteString(f.server.View() + "\n")
	if f.errMsg != "" {
		b.WriteString("\n" + statusErr.Render(f.errMsg))
	}
	b.WriteString("\n\n" + helpStyle.Render("enter: connect"+m.cancelHint()+" · ctrl+c: quit"))

	return m.authFrame(b.String())
}

func (m Model) viewAuthChoose() string {
	width := m.authWidth() - authCardStyle.GetHorizontalFrameSize()
	var b strings.Builder
	b.WriteString(titleStyle.Render("whisk") + " — sign in to " + m.authSess().label() + "\n\n")

	if m.auth.welcome != "" {
		b.WriteString(m.renderWelcome(width) + "\n\n")
	}

	// A pre-auth ~private notice (B.1) is shown while signing in.
	if n := m.authSess().store.LatestRoomlessNotice(); n != nil {
		b.WriteString(m.renderAuthNotice(m.auth.md, n, width) + "\n\n")
	}

	b.WriteString(promptStyle.Render("sign in with:") + "\n")
	for i, c := range m.authChoices() {
		// server.signup lists the schemes that create an account (§3.2): a
		// fresh user has no other way in when auth has no sign-up path.
		if c.signup && (i == 0 || !m.authChoices()[i-1].signup) {
			b.WriteString("\n" + promptStyle.Render("create an account with:") + "\n")
		}
		row := schemeChoice(c.scheme, c.signup)
		if i == m.auth.choice {
			b.WriteString(selBar(true) + roomSelStyle.Render(row) + "\n")
		} else {
			b.WriteString("  " + row + "\n")
		}
	}
	if !hasScheme(m.auth.schemes, "email") && !hasScheme(m.auth.schemes, "external") {
		note := "This server only offers the methods above; email/external sign-in " +
			"requires a server that advertises those schemes."
		b.WriteString("\n" + dimStyle.Render(note))
	}
	b.WriteString("\n" + helpStyle.Render("↑/↓ select · enter: continue · or press a key"+m.cancelHint()+" · ctrl+c: quit"))

	if m.auth.errMsg != "" {
		b.WriteString("\n" + statusErr.Render(m.auth.errMsg))
	}
	if m.auth.busy {
		b.WriteString("\n" + dimStyle.Render(m.spinner()+" working…"))
	}

	return m.authFrame(b.String())
}

// authFrame draws sign-in content in a card centered on the screen. A
// terminal too small for the card gets the content alone.
func (m Model) authFrame(content string) string {
	content = termWidthSafe(content)
	card := authCardStyle.Render(content)
	if m.width <= 0 || m.height <= 0 ||
		lipgloss.Width(card) > m.width || lipgloss.Height(card) >= m.height {
		return lipgloss.NewStyle().Padding(1).Render(content)
	}
	return lipgloss.Place(m.width, m.height-1, lipgloss.Center, lipgloss.Center, card)
}

// cancelHint is the esc entry of the help line, when there is a chat to
// return to.
func (m Model) cancelHint() string {
	if m.canCancelAuth() {
		return " · esc: cancel"
	}
	return ""
}

// schemeChoice describes one auth scheme for the chooser, with the key to
// press (uppercase in the "create an account" section).
func schemeChoice(s string, signup bool) string {
	key := map[string]string{"guest": "g", "token": "t", "email": "e", "external": "x"}[s]
	desc := map[string]string{
		"guest":    "guest — no account",
		"token":    "token — bearer token",
		"email":    "email — code sent to your inbox",
		"external": "external — sign in on a web page",
	}[s]
	if key == "" {
		key = "?"
		desc = s
	}
	if signup {
		key = strings.ToUpper(key)
	}
	return key + "  " + desc
}

// renderAuthNotice renders a notice (B.1 welcome) as markdown at the given
// width.
func (m *Model) renderAuthNotice(r *renderer, n *state.Notice, width int) string {
	if n == nil || n.Body == nil {
		return ""
	}
	if n.Body.Format == "markdown" {
		if r == nil {
			r = newRenderer(width)
		}
		if out := r.MarkdownMuted(n.Body.Text); out != "" {
			return out
		}
	}
	return dimStyle.Render(n.Body.Text)
}

func (m Model) viewAuthPrompt() string {
	var b strings.Builder
	switch m.auth.promptKind {
	case promptName:
		if m.auth.signupMode {
			b.WriteString(titleStyle.Render("whisk") + " — create account\n\n")
		} else {
			b.WriteString(titleStyle.Render("whisk") + " — guest sign-in\n\n")
		}
		b.WriteString(dimStyle.Render("Pick a display name.\n\n"))
	case promptToken:
		if m.auth.signupMode {
			b.WriteString(titleStyle.Render("whisk") + " — create account\n\n")
			b.WriteString(dimStyle.Render("Paste an invite token, or a bearer token.\n\n"))
		} else {
			b.WriteString(titleStyle.Render("whisk") + " — token sign-in\n\n")
			b.WriteString(dimStyle.Render("Paste your bearer token.\n\n"))
		}
	case promptEmail:
		if m.auth.signupMode {
			b.WriteString(titleStyle.Render("whisk") + " — create account\n\n")
			b.WriteString(dimStyle.Render("We'll send a code to this address.\n\n"))
		} else {
			b.WriteString(titleStyle.Render("whisk") + " — email sign-in\n\n")
			b.WriteString(dimStyle.Render("We'll send a code to this address.\n\n"))
		}
	case promptCode:
		if m.auth.codeScheme == "external" {
			b.WriteString(titleStyle.Render("whisk") + " — external sign-in\n\n")
			if m.auth.externalURL != "" {
				b.WriteString(dimStyle.Render("Sign in in your browser, then paste the code back here:\n"))
				b.WriteString(promptStyle.Render(m.auth.externalURL) + "\n\n")
			}
		} else {
			b.WriteString(titleStyle.Render("whisk") + " — email sign-in\n\n")
			b.WriteString(dimStyle.Render("Check your email and enter the code.\n\n"))
		}
	}

	b.WriteString(m.auth.prompt.View() + "\n")
	if m.auth.errMsg != "" {
		b.WriteString("\n" + statusErr.Render(m.auth.errMsg))
	}
	if m.auth.busy {
		b.WriteString("\n" + dimStyle.Render(m.spinner()+" working…"))
	}
	b.WriteString("\n\n" + helpStyle.Render("enter: continue · esc: back"))
	return m.authFrame(b.String())
}

func (m *Model) renderWelcome(width int) string {
	if m.auth.md == nil || m.auth.mdWidth != width {
		m.auth.md = newRenderer(width)
		m.auth.mdWidth = width
	}
	out := m.auth.md.Markdown(m.auth.welcome)
	if out == "" {
		return m.auth.welcome
	}
	return out
}
