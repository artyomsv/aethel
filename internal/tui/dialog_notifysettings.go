package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/artyomsv/quil/internal/config"
)

// notifyToggle is one row of F1 -> Settings -> Notifications.
//
// Every row is a plain on/off switch, so there is no edit mode and set takes no
// value — unlike settingsField, which also has to serve text and number rows. A
// heading row is inert: it carries a label and nothing else, and the cursor
// skips over it in both directions so Enter can never land on one.
type notifyToggle struct {
	label   string
	hint    string
	get     func(m *Model) string
	set     func(m *Model)
	heading bool
}

// notifySettingsRows returns the screen's rows, headings included.
//
// Sidebar-event toggles apply LIVE: each setter re-projects the config onto the
// notification center's filter, because a visible control that did nothing
// until relaunch reads as a broken dialog — the same rule the Sidebar width row
// states. The toast toggles apply live for free, since raiseAttentionToast
// reads m.cfg on every edge.
func notifySettingsRows() []notifyToggle {
	// applyGroups re-projects the config onto the live filter. Factored out so
	// a group added later cannot be wired up without it.
	applyGroups := func(m *Model) {
		m.notifications.SetGroups(groupFilterFrom(m.cfg.Notification.Events))
		m.configChanged = true
	}
	// group builds one event-group row. The accessor returns a POINTER into
	// the config so one closure serves every group; m.cfg is a value field on
	// Model, so &m.cfg.Notification.Events is addressable through *Model.
	group := func(label, hint string, read func(*config.EventGroupsConfig) *bool) notifyToggle {
		return notifyToggle{
			label: label,
			hint:  hint,
			get:   func(m *Model) string { return boolStr(*read(&m.cfg.Notification.Events)) },
			set: func(m *Model) {
				p := read(&m.cfg.Notification.Events)
				*p = !*p
				applyGroups(m)
			},
		}
	}

	return []notifyToggle{
		{label: "Desktop toasts", heading: true},
		{
			// Reports STATE, not the flag. With Enabled defaulting true,
			// enabled-but-unregistered is the DEFAULT on a fresh Windows
			// install rather than an edge case, and a bare "on" there would
			// claim toasts are working when no toast can be displayed at all.
			//
			// The row deliberately does NOT perform registration: writing a
			// Start Menu shortcut and an HKCU key as a side effect of a config
			// toggle is the auto-register behaviour this design rejected. It
			// names the command instead.
			label: "Enabled",
			hint:  "quil notify setup registers them",
			get:   func(m *Model) string { return m.desktopState().label() },
			set: func(m *Model) {
				m.cfg.Notification.Desktop.Enabled = !m.cfg.Notification.Desktop.Enabled
				m.configChanged = true
			},
		},
		{
			label: "On blocked",
			hint:  "a pane is waiting for you",
			get:   func(m *Model) string { return boolStr(m.cfg.Notification.Desktop.Blocked) },
			set: func(m *Model) {
				m.cfg.Notification.Desktop.Blocked = !m.cfg.Notification.Desktop.Blocked
				m.configChanged = true
			},
		},
		{
			label: "On done",
			hint:  "a turn finished while you were away",
			get:   func(m *Model) string { return boolStr(m.cfg.Notification.Desktop.Done) },
			set: func(m *Model) {
				m.cfg.Notification.Desktop.Done = !m.cfg.Notification.Desktop.Done
				m.configChanged = true
			},
		},
		{label: "Sidebar events", heading: true},
		group("Agent turns", "Working on… / Reply ready",
			func(c *config.EventGroupsConfig) *bool { return &c.AgentTurn }),
		group("Agent blocked", "permission, waiting for you",
			func(c *config.EventGroupsConfig) *bool { return &c.AgentBlocked }),
		group("Agent subagents", "subagent + task start/stop",
			func(c *config.EventGroupsConfig) *bool { return &c.AgentSubagent }),
		group("Agent session", "session end, compaction",
			func(c *config.EventGroupsConfig) *bool { return &c.AgentSession }),
		group("Process", "exited / failed",
			func(c *config.EventGroupsConfig) *bool { return &c.Process }),
		group("Pane", "closed, pinned, marked",
			func(c *config.EventGroupsConfig) *bool { return &c.Pane }),
		group("MCP", "an agent drove a pane",
			func(c *config.EventGroupsConfig) *bool { return &c.MCP }),
		group("System", "input blocked, worktree, unknown",
			func(c *config.EventGroupsConfig) *bool { return &c.System }),
		group("Commands", "every shell command",
			func(c *config.EventGroupsConfig) *bool { return &c.Commands }),
		group("Idle", "output idle",
			func(c *config.EventGroupsConfig) *bool { return &c.Idle }),
	}
}

// firstNotifyRow is the index the cursor starts on: the first non-heading row.
func firstNotifyRow(rows []notifyToggle) int {
	for i, r := range rows {
		if !r.heading {
			return i
		}
	}
	return 0
}

// handleNotifySettingsKey drives the screen.
func (m Model) handleNotifySettingsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rows := notifySettingsRows()
	switch msg.String() {
	case "esc":
		m.dialog = dialogSettings
		m.dialogCursor = 0
	case "up", "k":
		for i := m.dialogCursor - 1; i >= 0; i-- {
			if !rows[i].heading {
				m.dialogCursor = i
				break
			}
		}
	case "down", "j":
		for i := m.dialogCursor + 1; i < len(rows); i++ {
			if !rows[i].heading {
				m.dialogCursor = i
				break
			}
		}
	case "enter", " ":
		if m.dialogCursor >= 0 && m.dialogCursor < len(rows) && !rows[m.dialogCursor].heading {
			rows[m.dialogCursor].set(&m)
		}
	}
	return m, nil
}

// renderNotifySettingsDialog paints the screen at the standard dialog width.
func (m Model) renderNotifySettingsDialog() string {
	var b strings.Builder
	b.WriteString(dialogTitle.Render("Notifications"))
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  changes persist to config.toml"))
	b.WriteString("\n")

	for i, r := range notifySettingsRows() {
		if r.heading {
			b.WriteString("\n  " + dialogTitle.Render(r.label) + "\n")
			continue
		}
		cursor := "    "
		labelStyle := dialogLabelStyle
		if i == m.dialogCursor {
			cursor = "  > "
			labelStyle = labelStyle.Foreground(lipgloss.Color("230")).Bold(true)
		}
		b.WriteString(cursor + labelStyle.Render(r.label) + dialogValStyle.Render(r.get(&m)) + "\n")
		if r.hint != "" {
			b.WriteString(dialogSubtle.Render("      " + r.hint))
			b.WriteByte('\n')
		}
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  Hidden events still reach MCP agents"))
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  ↑↓ navigate  Enter toggle  Esc back"))
	return b.String()
}
