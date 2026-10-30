package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stevedylandev/whisk/internal/protocol"
)

// Slash commands typed in the composer. Client commands are handled here;
// any other /command goes to the server when it advertises "command" (§4.1).

// dispatchText routes composer text: "//" escapes a literal leading slash,
// "/" starts a command, and anything else is a message.
func (m *Model) dispatchText(text, replyTo string) tea.Cmd {
	switch {
	case strings.HasPrefix(text, "//"):
		return m.sendMessage(text[1:], replyTo)
	case strings.HasPrefix(text, "/"):
		return m.dispatchCommand(text, replyTo)
	default:
		return m.sendMessage(text, replyTo)
	}
}

func (m *Model) dispatchCommand(text, replyTo string) tea.Cmd {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}
	cmd := strings.TrimPrefix(fields[0], "/")
	args := fields[1:]
	switch cmd {
	case "help":
		return m.openHelp(true)
	case "server", "s":
		return m.serverCommand(text, replyTo)
	case "nick":
		name := strings.Join(args, " ")
		if name == "" {
			m.setStatus("usage: /nick NAME", true)
			return nil
		}
		if !m.requireWrite() {
			return nil
		}
		return requestCmd(m.cur(), "me", "me", protocol.MeParams{Name: protocol.StringPtr(name)})
	case "status":
		return m.statusCommand(args)
	case "mute":
		return m.muteCommand(args)
	case "join":
		if len(args) < 1 {
			m.setStatus("usage: /join ROOM_ID", true)
			return nil
		}
		if !m.requireCap("rooms") || !m.requireWrite() {
			return nil
		}
		return m.joinRoom(m.cur(), m.resolveRoom(args[0]))
	case "leave":
		roomID := m.chat.roomID
		if len(args) >= 1 {
			roomID = m.resolveRoom(args[0])
		}
		if roomID == "" {
			m.setStatus("no room to leave", true)
			return nil
		}
		if !m.requireWrite() {
			return nil
		}
		return requestCmd(m.cur(), "room_leave", "room_leave", map[string]string{"room_id": roomID})
	case "new", "create":
		private := false
		if len(args) > 0 && args[0] == "--private" {
			private = true
			args = args[1:]
		}
		title := strings.Join(args, " ")
		if title == "" {
			m.setStatus("usage: /new [--private] TITLE", true)
			return nil
		}
		if !m.requireCap("rooms") || !m.requireWrite() {
			return nil
		}
		m.cur().expectPrivate = private
		return requestCmd(m.cur(), "room_set", "room_set", protocol.RoomSetParams{Title: title, Private: private})
	case "thread":
		title := strings.Join(args, " ")
		if title == "" {
			m.setStatus("usage: /thread TITLE", true)
			return nil
		}
		if !m.requireCap("rooms") || !m.requireWrite() {
			return nil
		}
		if m.chat.roomID == "" {
			m.setStatus("select a room to start a thread in", true)
			return nil
		}
		return requestCmd(m.cur(), "room_set", "room_set", protocol.RoomSetParams{
			ParentRoomID: m.chat.roomID,
			Title:        title,
		})
	case "move":
		if len(args) < 1 {
			m.setStatus("usage: /move ROOM", true)
			return nil
		}
		if !m.requireCap("edit") || !m.requireWrite() {
			return nil
		}
		msg := m.selectedMessage()
		if msg == nil {
			m.setStatus("no message selected", true)
			return nil
		}
		dest := m.resolveRoom(args[0])
		if dest == "" {
			m.setStatus("unknown room "+args[0], true)
			return nil
		}
		text := ""
		if msg.Body != nil {
			text = msg.Body.Text
		}
		save := buildSave(msg, dest, text, nil)
		if save == nil {
			return nil
		}
		return requestCmd(m.cur(), "message", "message", *save)
	case "react":
		if len(args) < 1 {
			m.setStatus("usage: /react EMOJI", true)
			return nil
		}
		return m.toggleReact(args[0])
	case "emoji":
		// Open the picker in the composer; any argument pre-fills its search.
		m.openEmojiInsert(strings.Join(args, " "))
		return nil
	case "attach", "upload":
		if len(args) < 1 {
			m.setStatus("usage: /attach PATH [caption] (tab completes paths)", true)
			return nil
		}
		return m.startUpload(args[0], strings.Join(args[1:], " "))
	case "avatar":
		return m.avatarCommand(args)
	default:
		if m.hasCap("command") {
			return m.sendCommand(text, replyTo)
		}
		m.setStatus(fmt.Sprintf("unknown command %q (server has no command capability)", cmd), true)
		return nil
	}
}

// statusCommand implements /status (§4.5): show or set the chosen status,
// validated against server.status.
func (m *Model) statusCommand(args []string) tea.Cmd {
	if !m.requireCap("status") {
		return nil
	}
	if len(args) == 0 {
		cur := ""
		if m.cur().store.You != nil {
			cur = m.cur().store.You.Status
		}
		if cur == "" {
			cur = "(none)"
		}
		allowed := append([]string{"online", "off"}, m.cur().store.Server.Status...)
		m.setStatus(fmt.Sprintf("status: %s — allowed: %s", cur, strings.Join(allowed, ", ")), false)
		return nil
	}
	value := args[0]
	if value == "off" {
		value = ""
	}
	if err := validateStatus(m.cur().store.Server, value); err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	return requestCmd(m.cur(), "me", "me", protocol.MeParams{Status: protocol.StringPtr(value)})
}

// validateStatus reports whether value is an acceptable chosen status (§4.5.1):
// servers always accept "online" and "", plus server.status.
func validateStatus(srv *protocol.Server, value string) error {
	if srv == nil {
		return fmt.Errorf("server not ready")
	}
	if value == "online" || value == "" {
		return nil
	}
	for _, s := range srv.Status {
		if s == value {
			return nil
		}
	}
	allowed := append([]string{"online", "off"}, srv.Status...)
	return fmt.Errorf("status %q not accepted (allowed: %s)", value, strings.Join(allowed, ", "))
}

// muteCommand implements /mute [ROOM|*] [SECONDS|off] (§4.5.3).
func (m *Model) muteCommand(args []string) tea.Cmd {
	if !m.requireCap("status") {
		return nil
	}
	params, err := parseMuteArgs(args, m.chat.roomID, m)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	return requestCmd(m.cur(), "status", "status", params)
}

// parseMuteArgs turns /mute arguments into a status request. Room "*" is the
// global mute (room_id omitted).
func parseMuteArgs(args []string, currentRoom string, m *Model) (protocol.StatusParams, error) {
	p := protocol.StatusParams{}
	room := currentRoom
	if len(args) > 0 && args[0] != "off" {
		if !isSeconds(args[0]) {
			room = args[0]
			args = args[1:]
		}
	}
	if room == "" {
		return p, fmt.Errorf("no room to mute (pass ROOM or *)")
	}
	if room != "*" {
		p.RoomID = m.resolveRoom(room)
	}
	switch {
	case len(args) == 0:
		p.Mute = protocol.RawBool(true)
	case args[0] == "off":
		p.Mute = protocol.RawBool(false)
	case isSeconds(args[0]):
		secs, _ := strconv.Atoi(args[0])
		if secs <= 0 {
			return p, fmt.Errorf("mute seconds must be positive")
		}
		p.Mute = protocol.RawNumber(secs)
	default:
		return p, fmt.Errorf("usage: /mute [ROOM|*] [SECONDS|off]")
	}
	return p, nil
}

func isSeconds(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func (m *Model) avatarCommand(args []string) tea.Cmd {
	// /avatar with no path clears the avatar (§3.3.2); a path goes through
	// the /avatar command with an upload embed (§4.8.6).
	if len(args) == 0 {
		if !m.requireWrite() {
			return nil
		}
		return requestCmd(m.cur(), "me", "me", protocol.MeParams{Avatar: protocol.StringPtr("")})
	}
	if !m.hasCap("command") || !m.hasCap("embed:upload") {
		m.setStatus("server lacks command/embed:upload (needed for /avatar)", true)
		return nil
	}
	if !m.requireWrite() {
		return nil
	}
	// An avatar image is checked against ext.media's avatar table, whose
	// caps are far below attachments' (the demo: 256 KiB vs 5 MiB).
	up, err := readUploadFile(args[0], protocol.ServerMedia(m.cur().store.Server), mediaAvatar)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	m.cur().pendingUpload = up
	return requestCmd(m.cur(), "command_upload", "command", protocol.Message{
		Body: &protocol.Body{
			Text:   "/avatar",
			Embeds: []protocol.Embed{{Kind: "upload", Title: up.name}},
		},
	})
}

// resolveRoom maps a room_id (or an exact title) to a known room_id, or "".
func (m *Model) resolveRoom(ref string) string {
	if ref == "" {
		return ""
	}
	if m.cur().store.Room(ref) != nil || m.cur().store.NotJoinedRoom(ref) != nil {
		return ref
	}
	for _, id := range m.cur().store.Rooms() {
		if r := m.cur().store.Room(id); r != nil && r.Title() == ref {
			return id
		}
	}
	for _, id := range m.cur().store.NotJoined() {
		if r := m.cur().store.NotJoinedRoom(id); r != nil && r.Title() == ref {
			return id
		}
	}
	return ref
}

// serverCommand implements /server CMD [ARGS]: it sends /CMD ARGS to the
// server as a command (§4.1), whether or not the client handles CMD itself.
func (m *Model) serverCommand(text, replyTo string) tea.Cmd {
	cmd := serverCommandText(text)
	if cmd == "" {
		m.setStatus("usage: /server CMD [ARGS]", true)
		return nil
	}
	if !m.hasCap("command") {
		m.setStatus("server has no command capability", true)
		return nil
	}
	return m.sendCommand(cmd, replyTo)
}

// serverCommandText turns "/server CMD ARGS" into "/CMD ARGS", keeping the
// arguments as typed; "" when no command follows.
func serverCommandText(text string) string {
	rest := strings.TrimSpace(text)
	i := strings.IndexAny(rest, " \t\n")
	if i < 0 {
		return ""
	}
	rest = strings.TrimPrefix(strings.TrimSpace(rest[i:]), "/")
	if rest == "" {
		return ""
	}
	return "/" + rest
}
