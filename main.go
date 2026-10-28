// Command whisk is a terminal client for the Apron Chat Protocol.
package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stevedylandev/whisk/internal/config"
	"github.com/stevedylandev/whisk/internal/ui"
)

func main() {
	server := flag.String("server", "", "Apron server URL (ws://, wss://, http://, https://, or a bare host)")
	name := flag.String("name", "", "display name for guest sign-in")
	token := flag.String("token", "", "bearer token for token sign-in")
	cfgPath := flag.String("config", config.DefaultPath(), "path to the config file")
	theme := flag.String("theme", "", "color theme: dark or light (default from config, else dark)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load config: %v\n", err)
		cfg = config.Config{}
	}

	// The flag applies to this run only; it is not saved to the config.
	if *theme != "" {
		ui.SetTheme(*theme)
	} else {
		ui.SetTheme(cfg.Theme)
	}

	model := ui.NewModel(cfg, *cfgPath, *server, *name, *token)
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
