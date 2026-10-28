package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stevedylandev/whisk/internal/protocol"
)

// Activity and idle reporting: typing indicators and read cursors (§4.6),
// and the idle/attended status a connection reports (§4.5.3).

func (m *Model) maybeSendTyping() []tea.Cmd {
	if !m.hasCap("activity") || m.chat.roomID == "" || m.composerText() == "" {
		return nil
	}
	if time.Since(m.chat.lastTypingSent) < 3*time.Second {
		return nil
	}
	m.chat.lastTypingSent = time.Now()
	return m.sendActivity(protocol.ActivityParams{RoomID: m.chat.roomID, Typing: intPtr(5)})
}

func (m *Model) sendTypingStop() []tea.Cmd {
	if !m.hasCap("activity") || m.chat.roomID == "" {
		return nil
	}
	m.chat.lastTypingSent = time.Time{}
	return m.sendActivity(protocol.ActivityParams{RoomID: m.chat.roomID, Typing: intPtr(0)})
}

func (m *Model) maybeSendRead() tea.Cmd {
	s := m.cur()
	room := s.store.Room(m.chat.roomID)
	if room == nil || len(room.Order) == 0 {
		return nil
	}
	last := room.Order[len(room.Order)-1]
	if last == m.chat.lastReadID {
		return nil
	}
	m.chat.lastReadID = last
	return notifyCmd(s, "activity", protocol.ActivityParams{
		RoomID:        m.chat.roomID,
		ReadMessageID: last,
	})
}

func (m *Model) sendActivity(p protocol.ActivityParams) []tea.Cmd {
	return []tea.Cmd{notifyCmd(m.cur(), "activity", p)}
}

// anyReportsIdle reports whether some signed-in connection supports idle
// reporting.
func (m *Model) anyReportsIdle() bool {
	for _, s := range m.sessions {
		if s.authed && s.client != nil && s.hasCap("status") {
			return true
		}
	}
	return false
}

// noteInput records user attention and reports each connection attended
// again after it had gone idle (§4.5.3).
func (m *Model) noteInput() []tea.Cmd {
	m.lastInput = time.Now()
	var cmds []tea.Cmd
	for _, s := range m.sessions {
		if !s.idleSent {
			continue
		}
		s.idleSent = false
		if s.client == nil || !s.hasCap("status") {
			continue
		}
		cmds = append(cmds, requestCmd(s, "status", "status", protocol.StatusParams{
			Idle: protocol.RawBool(false),
		}))
	}
	return cmds
}

// idleThreshold is how long without input before the connection reports idle
// (§4.5.3: clients MAY wait a few minutes).
const idleThreshold = 5 * time.Minute

func (m *Model) onIdleTick() []tea.Cmd {
	m.idleTickPending = false
	if m.lastInput.IsZero() {
		return nil
	}
	idle := time.Since(m.lastInput)
	if idle < idleThreshold {
		return nil
	}
	var cmds []tea.Cmd
	for _, s := range m.sessions {
		if s.client == nil || !s.authed || !s.hasCap("status") || s.idleSent {
			continue
		}
		s.idleSent = true
		cmds = append(cmds, requestCmd(s, "status", "status", protocol.StatusParams{
			Idle: protocol.RawNumber(int(idle.Seconds())),
		}))
	}
	return cmds
}

func intPtr(v int) *int { return &v }
