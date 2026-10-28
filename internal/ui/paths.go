package ui

import (
	"fmt"
	"os"
	"strings"
)

// Local path arguments: "/attach ~/pic.png" reads the file after expanding a
// leading ~, and tab completes the path being typed.

// pathCommands are the commands whose argument is a local path. While one of
// them is being typed, tab completes the path instead of moving focus.
var pathCommands = map[string]bool{"attach": true, "upload": true, "avatar": true}

// resolveLocalPath expands a leading "~" or "~/" to the user's home directory.
// Any other path is returned unchanged.
func resolveLocalPath(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return home + strings.TrimPrefix(path, "~")
}

// splitTypedPath splits a path being typed into the directory as typed (which
// keeps a leading "~"), the directory to read, and the partial base name.
func splitTypedPath(p string) (typedDir, readDir, base string) {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		// "~" alone names the home directory, not a file in the cwd.
		if p == "~" {
			return "~/", resolveLocalPath("~/"), ""
		}
		return "", ".", p
	}
	typedDir = p[:i+1]
	return typedDir, resolveLocalPath(typedDir), p[i+1:]
}

// completePathToken completes a partial path against the local filesystem. The
// returned path keeps the "~" prefix as typed and appends "/" to directories.
// candidates lists the entries that matched, so an ambiguous completion can be
// reported.
func completePathToken(partial string) (string, []string) {
	typedDir, readDir, base := splitTypedPath(partial)
	entries, err := os.ReadDir(readDir)
	if err != nil {
		return partial, nil
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base) {
			continue
		}
		// Hidden entries match only when the user asks for them.
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	switch {
	case len(names) == 0:
		return partial, nil
	case len(names) > 1:
		common := commonPrefix(names)
		if len(common) <= len(base) {
			// Nothing to add without choosing between the matches.
			return partial, names
		}
		return typedDir + common, names
	default:
		return typedDir + names[0], names
	}
}

// commonPrefix returns the longest shared prefix of names, in runes so a
// multi-byte name is never cut in half.
func commonPrefix(names []string) string {
	prefix := []rune(names[0])
	for _, n := range names[1:] {
		r := []rune(n)
		i := 0
		for i < len(prefix) && i < len(r) && prefix[i] == r[i] {
			i++
		}
		prefix = prefix[:i]
	}
	return string(prefix)
}

// composerPathToken splits composer text into the part before the path argument
// and the partial path. ok is false unless the text ends in a path argument of
// one of pathCommands.
func composerPathToken(value string) (head, partial string, ok bool) {
	if value == "" || strings.HasSuffix(value, " ") || strings.HasSuffix(value, "\n") {
		return "", "", false
	}
	sp := strings.IndexAny(value, " \t\n")
	if sp < 0 {
		return "", "", false
	}
	if !pathCommands[strings.TrimPrefix(value[:sp], "/")] {
		return "", "", false
	}
	partial = value[sp+1:]
	if strings.ContainsAny(partial, " \t\n") {
		// Already past the path, in the caption.
		return "", "", false
	}
	return value[:sp+1], partial, true
}

// completePath completes a path argument in the composer. It reports whether
// tab was consumed; when it was not, tab keeps its focus-cycling behaviour.
func (m *Model) completePath() bool {
	value := m.chat.composer.Value()
	head, partial, ok := composerPathToken(value)
	if !ok {
		return false
	}
	completed, matches := completePathToken(partial)
	switch {
	case len(matches) == 0:
		return false
	case len(matches) == 1:
		// A directory keeps its trailing slash so the next tab descends; a
		// file gets a space so a caption can follow.
		if !strings.HasSuffix(completed, "/") {
			completed += " "
		}
		m.setStatus("", false)
	case completed == partial:
		m.setStatus("matches: "+strings.Join(fewNames(matches, 8), "  "), false)
		return true
	default:
		m.setStatus("", false)
	}
	m.chat.composer.SetValue(head + completed)
	return true
}

// fewNames returns at most n names, noting how many were left out.
func fewNames(names []string, n int) []string {
	if len(names) <= n {
		return names
	}
	out := append([]string{}, names[:n]...)
	return append(out, fmt.Sprintf("… (%d more)", len(names)-n))
}
