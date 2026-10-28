package ui

import (
	"hash/fnv"

	"github.com/charmbracelet/lipgloss"
)

func init() {
	// Prefer a fixed dark background so rendering never blocks on a terminal
	// background-color query (OSC 11), which can hang ~5s in tmux and other
	// terminals that do not answer it. SetTheme switches to light.
	lipgloss.SetHasDarkBackground(true)
}

// lightTheme is set by SetTheme("light"); markdown rendering follows it.
var lightTheme bool

// SetTheme picks the color theme: "light" for light terminals, anything else
// for dark. It is explicit rather than detected, since detection needs the
// OSC 11 query that hangs some terminals.
func SetTheme(name string) {
	lightTheme = name == "light"
	lipgloss.SetHasDarkBackground(!lightTheme)
}

var (
	accent     = lipgloss.AdaptiveColor{Light: "5", Dark: "12"}
	accentText = lipgloss.AdaptiveColor{Light: "5", Dark: "13"}
	dim        = lipgloss.AdaptiveColor{Light: "8", Dark: "8"}
	good       = lipgloss.AdaptiveColor{Light: "2", Dark: "2"}
	bad        = lipgloss.AdaptiveColor{Light: "1", Dark: "1"}
	warn       = lipgloss.AdaptiveColor{Light: "3", Dark: "3"}
	info       = lipgloss.AdaptiveColor{Light: "6", Dark: "6"}

	statusOk    = lipgloss.NewStyle().Foreground(good)
	statusErr   = lipgloss.NewStyle().Foreground(bad)
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(accentText)
	promptStyle = lipgloss.NewStyle().Foreground(accentText)
	helpStyle   = lipgloss.NewStyle().Foreground(dim)
	dimStyle    = lipgloss.NewStyle().Foreground(dim)
	infoStyle   = lipgloss.NewStyle().Foreground(info)
	accentStyle = lipgloss.NewStyle().Foreground(accent)
	warnStyle   = lipgloss.NewStyle().Foreground(warn)
	roomStyle   = lipgloss.NewStyle().Bold(true).Foreground(accentText)

	sidebarStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(dim).Padding(0, 1)
	roomSelStyle = lipgloss.NewStyle().Bold(true).Foreground(accentText)
	roomDimStyle = lipgloss.NewStyle().Foreground(dim)
	unreadStyle  = lipgloss.NewStyle().Bold(true)
	serverStyle  = lipgloss.NewStyle().Bold(true)

	msgViewStyle  = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(dim).Padding(0, 1)
	composerStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true).BorderForeground(dim).Padding(0, 1)

	// authCardStyle frames the sign-in screens.
	authCardStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(1, 3)
)

// gutterWidth is the columns reserved left of each message and sidebar row
// for the selection bar.
const gutterWidth = 2

// selBar marks the selected row: accent while its pane has focus, dim
// otherwise.
func selBar(focused bool) string {
	if focused {
		return accentStyle.Render("▌") + " "
	}
	return dimStyle.Render("▌") + " "
}

// paneBorder returns the border color for a pane: accent when it has focus.
func paneBorder(focused bool) lipgloss.TerminalColor {
	if focused {
		return accent
	}
	return dim
}

// authorPalette colors author names so speakers are easy to tell apart. The
// values are 256-color indexes, a darker set for light terminals.
var authorPalette = []lipgloss.AdaptiveColor{
	{Light: "25", Dark: "75"},   // blue
	{Light: "91", Dark: "141"},  // purple
	{Light: "30", Dark: "80"},   // cyan
	{Light: "28", Dark: "114"},  // green
	{Light: "130", Dark: "179"}, // gold
	{Light: "162", Dark: "175"}, // pink
	{Light: "166", Dark: "209"}, // orange
	{Light: "61", Dark: "147"},  // lavender
}

// authorStyle returns the name style for a user, picked by a stable hash of
// the user_id so the same person always gets the same color.
func authorStyle(userID string) lipgloss.Style {
	h := fnv.New32a()
	_, _ = h.Write([]byte(userID))
	c := authorPalette[h.Sum32()%uint32(len(authorPalette))]
	return lipgloss.NewStyle().Bold(true).Foreground(c)
}

// statusDot returns a colored presence indicator for a status value.
func statusDot(status string) string {
	switch status {
	case "online":
		return lipgloss.NewStyle().Foreground(good).Render("●")
	case "idle":
		return lipgloss.NewStyle().Foreground(warn).Render("●")
	case "dnd":
		return lipgloss.NewStyle().Foreground(bad).Render("●")
	default:
		return dimStyle.Render("●")
	}
}

// logo is the wordmark shown on the first sign-in screen.
const logo = "╻ ╻╻ ╻╻┏━┓╻┏ \n┃╻┃┣━┫┃┗━┓┣┻┓\n┗┻┛╹ ╹╹┗━┛╹ ╹"
