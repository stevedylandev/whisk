package ui

import (
	"encoding/json"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/stevedylandev/whisk/internal/client"
	"github.com/stevedylandev/whisk/internal/config"
)

func serverEvent(t *testing.T, authJSON string) *Model {
	t.Helper()
	lipgloss.SetHasDarkBackground(true)
	m := NewModel(testCfg(), t.TempDir()+"/cfg.json", "", "", "")
	m.screen = screenAuth
	m.auth.sess = m.cur()
	ev := client.Event{Method: "server", Params: json.RawMessage(authJSON)}
	m.onEvent(m.cur(), ev)
	return &m
}

func TestServerFrameShowsChooser(t *testing.T) {
	m := serverEvent(t, `{"apron":9,"auth":["email","token"],"welcome":"hi"}`)
	if m.auth.phase != phaseChoose {
		t.Fatalf("phase = %v, want choose", m.auth.phase)
	}
	if !hasScheme(m.auth.schemes, "email") || !hasScheme(m.auth.schemes, "token") {
		t.Fatalf("schemes = %v", m.auth.schemes)
	}
}

func TestGuestOnlyServerAlsoShowsChooser(t *testing.T) {
	m := serverEvent(t, `{"apron":9,"auth":["token","guest"]}`)
	if m.auth.phase != phaseChoose {
		t.Fatalf("phase = %v, want choose", m.auth.phase)
	}
}

func TestChooseEmailMovesToPrompt(t *testing.T) {
	m := serverEvent(t, `{"apron":9,"auth":["email","token"]}`)
	m.updateAuthChoose(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if m.auth.phase != phasePrompt || m.auth.promptKind != promptEmail {
		t.Fatalf("phase=%v kind=%v, want prompt/email", m.auth.phase, m.auth.promptKind)
	}
}

func TestChooseExternalSendsProposal(t *testing.T) {
	m := serverEvent(t, `{"apron":9,"auth":["external","token"]}`)
	cmds := m.updateAuthChoose(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if !m.auth.busy {
		t.Fatal("expected busy after external proposal")
	}
	if len(cmds) != 1 {
		t.Fatalf("cmds = %d, want 1", len(cmds))
	}
	if m.auth.codeScheme != "external" {
		t.Fatalf("codeScheme = %q", m.auth.codeScheme)
	}
}

func TestEmailProposalResultMovesToCodePrompt(t *testing.T) {
	m := serverEvent(t, `{"apron":9,"auth":["email","token"]}`)
	m.auth.busy = true
	m.handleRequestDone(m.cur(), requestDoneMsg{sid: m.cur().id, tag: "auth_email", result: json.RawMessage(`{}`)})
	if m.auth.phase != phasePrompt || m.auth.promptKind != promptCode {
		t.Fatalf("phase=%v kind=%v, want prompt/code", m.auth.phase, m.auth.promptKind)
	}
	if m.auth.codeScheme != "email" {
		t.Fatalf("codeScheme = %q", m.auth.codeScheme)
	}
}

func TestValidateExternalURL(t *testing.T) {
	cases := []struct {
		raw   string
		valid bool
	}{
		{"https://chat.example/sso?p=Qm7x", true},
		{"http://localhost:8080/sso", true},
		{"http://127.0.0.1/sso", true},
		{"http://chat.example/sso", false},
		{"javascript:alert(1)", false},
		{"file:///etc/passwd", false},
		{"ftp://chat.example/sso", false},
		{"", false},
	}
	for _, c := range cases {
		err := validateExternalURL(c.raw)
		if c.valid && err != nil {
			t.Errorf("validateExternalURL(%q) = %v, want nil", c.raw, err)
		}
		if !c.valid && err == nil {
			t.Errorf("validateExternalURL(%q) = nil, want error", c.raw)
		}
	}
}

func TestGuestPromptPrefillsName(t *testing.T) {
	m := NewModel(config.Config{Servers: []config.Server{{URL: "wss://test.example/", Name: "Ada"}}}, t.TempDir()+"/cfg.json", "", "", "")
	m.screen = screenAuth
	m.auth.sess = m.cur()
	m.auth.schemes = []string{"guest"}
	m.startPrompt(promptName)
	if m.auth.prompt.Value() != "Ada" {
		t.Fatalf("prompt value = %q, want Ada", m.auth.prompt.Value())
	}
}
