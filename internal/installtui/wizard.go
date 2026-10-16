package installtui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/floatlab/floatlab-core/internal/installer"
)

func (m *Model) canBack() bool {
	if m.pending || m.rebooting {
		return false
	}
	switch m.stage {
	case "command", "confirm", "timezone", "account", "review", "totp":
		return true
	}
	return false
}

func (m *Model) canNext() bool {
	if m.pending || m.rebooting {
		return false
	}
	switch m.stage {
	case "connecting", "waiting", "unavailable":
		return false
	case "failed":
		return m.terminal
	}
	return true
}

func (m *Model) back() {
	if !m.canBack() {
		return
	}
	switch m.stage {
	case "command":
		m.stage = "disks"
		if m.preview.Command != "" {
			m.stage = "confirm"
		}
	case "confirm":
		m.stage = "disks"
	case "timezone":
		m.stage = "settings"
		m.setFocus(1)
	case "account":
		m.stage = "settings"
		m.setFocus(6)
	case "review":
		m.stage = "account"
		m.setFocus(10)
	case "totp":
		if m.focus == 1 {
			m.focus = 0
			m.confirmation.SetValue(m.totpCode)
		} else {
			m.stage = "account"
			m.setFocus(10)
			m.enrollment = nil
		}
	}
	m.actionFocus = 0
	m.validation = ""
	m.scroll, m.manualScroll = 0, false
	if m.stage == "command" {
		m.command.Focus()
	}
}

func (m *Model) focusAction(action int) {
	m.actionFocus = action
	m.command.Blur()
	m.confirmation.Blur()
	m.timezoneSearch.Blur()
	for i := range m.fields {
		m.fields[i].Blur()
	}
}

func (m *Model) focusContent() tea.Cmd {
	m.actionFocus = 0
	switch m.stage {
	case "command":
		return m.command.Focus()
	case "totp":
		return m.confirmation.Focus()
	case "timezone":
		return m.timezoneSearch.Focus()
	case "settings", "account":
		if m.focus >= len(m.fields) {
			fields := m.visibleFields()
			m.setFocus(fields[len(fields)-1])
		} else {
			m.setFocus(m.focus)
		}
	}
	return textinput.Blink
}

// The action row has its own focus so arrow keys in editors retain their meaning.
func (m *Model) wizardKey(v tea.KeyMsg) (bool, tea.Cmd) {
	k := v.String()
	if k == "esc" && m.canBack() {
		m.back()
		return true, m.focusContent()
	}
	if m.actionFocus != 0 {
		if m.stage == "confirm" && m.actionFocus == 3 && (k == "enter" || k == " ") {
			m.stage = "command"
			m.resize()
			return true, m.focusContent()
		}
		switch k {
		case "left":
			if m.actionFocus == 3 {
				m.actionFocus = 2
			} else if m.canBack() {
				m.actionFocus = 1
			}
		case "right":
			if m.canNext() {
				m.actionFocus = 2
			}
		case "tab":
			if m.stage == "confirm" && m.actionFocus == 2 {
				m.actionFocus = 3
			} else if m.canBack() && m.actionFocus != 1 {
				m.actionFocus = 1
			} else if m.canNext() {
				m.actionFocus = 2
			}
		case "down":
			if m.stage == "confirm" && m.actionFocus == 3 {
				m.focusAction(2)
			}
		case "up", "shift+tab":
			if m.stage == "confirm" && m.actionFocus != 3 {
				m.focusAction(3)
				return true, nil
			}
			return true, m.focusContent()
		case "enter", " ":
			if m.actionFocus == 1 {
				m.back()
				return true, m.focusContent()
			}
			if m.canNext() {
				m.focusContent()
				if m.stage == "settings" || m.stage == "account" {
					fields := m.visibleFields()
					m.setFocus(fields[len(fields)-1])
				}
				if m.stage == "command" {
					v = tea.KeyMsg{Type: tea.KeyCtrlS}
				} else {
					v = tea.KeyMsg{Type: tea.KeyEnter}
				}
				_, cmd := m.Update(v)
				return true, cmd
			}
		}
		return true, nil
	}
	toActions := k == "tab" || k == "shift+tab"
	switch m.stage {
	case "settings", "account":
		return false, nil // Form focus joins the action row in moveFocus.
	case "totp":
		toActions = toActions || k == "down" || k == "up"
	case "disks":
		toActions = toActions || k == "down" && m.cursor == len(m.disks)-1 || k == "up" && m.cursor == 0
	case "command":
		toActions = toActions || k == "down" && m.command.Line() == m.command.LineCount()-1 || k == "up" && m.command.Line() == 0
	case "timezone":
		toActions = toActions || k == "down" && m.timezoneCursor == len(m.timezoneMatches())-1
	default:
		toActions = toActions || k == "up" || k == "down" || k == "left" || k == "right"
	}
	if toActions {
		action := 2
		if (k == "shift+tab" || k == "up" || k == "left") && m.canBack() {
			action = 1
		}
		if m.stage == "confirm" && (k == "down" || k == "tab") {
			action = 3
		}
		if action == 1 || m.canNext() {
			m.focusAction(action)
		}
		return true, nil
	}
	return false, nil
}

func (m *Model) button(label string, action int, enabled bool) string {
	t := m.currentTheme()
	style := lipgloss.NewStyle().Foreground(t.foreground).Background(t.buttonBackground)
	if !enabled {
		style = style.Faint(true)
	} else if m.actionFocus == action {
		style = style.Foreground(t.background).Background(t.accent).Bold(true)
	}
	return style.Render("  " + label + "  ")
}

func (m *Model) actionBar() string {
	next := "Next →"
	if m.stage == "confirm" {
		next = "Erase All and continue →"
	}
	if m.stage == "review" {
		next = "Finish →"
	}
	if m.stage == "complete" || m.stage == "failed" && m.terminal {
		next = "Reboot →"
	}
	row := m.button(next, 2, m.canNext())
	if m.canBack() {
		row = m.button("← Back", 1, true) + "    " + row
	}

	return row
}

func (m *Model) helpBar(width int) string {
	help := "↑↓ Move  Enter Next"
	switch {
	case m.actionFocus != 0:
		help = "←→ Choose  ↑ Edit  Enter Select"
	case m.stage == "disks":
		help = "↑↓ Move  Space Select  Tab Actions"
	case m.stage == "command":
		help = "↑↓←→ Edit  Tab Actions  Ctrl+S Next"
	case m.stage == "settings" || m.stage == "account":
		help = "↑↓ Move  ←→/Space Choose  Enter Next"
	case m.stage == "timezone":
		help = "Type Filter  ↑↓ Move  Enter Select"
	case m.stage == "confirm":
		help = "←→ Actions  Enter Erase All and continue"
	case m.stage == "review":
		help = "PgUp/PgDn Scroll  Enter Finish"
	case m.stage == "totp":
		help = "Type Confirm  ↓ Actions  Enter Next"
	case m.stage == "complete" || m.stage == "failed" && m.terminal:
		help = "←→ Actions  Enter Reboot"
	case m.stage == "waiting" || m.stage == "connecting":
		help = "Installing…"
	}
	if m.terminal {
		help += "  Ctrl+R Reboot"
	}
	help = ansi.Truncate(help, max(1, width-4), "…")
	t := m.currentTheme()
	keyStyle := lipgloss.NewStyle().Foreground(t.foreground).Background(t.background)
	labelStyle := lipgloss.NewStyle().Foreground(t.darkForeground).Background(t.background)
	parts := strings.Split(help, "  ")
	for i, part := range parts {
		key, label, found := strings.Cut(strings.TrimLeft(part, " "), " ")
		padding := strings.Repeat(" ", len(part)-len(strings.TrimLeft(part, " ")))
		parts[i] = padding + keyStyle.Render(key)
		if found {
			parts[i] += " " + labelStyle.Render(label)
		}
	}
	return strings.Join(parts, "  ")
}

func (m *Model) preparePoolCommand() bool {
	var selected []installer.Disk
	for i, d := range m.disks {
		if m.selected[i] && d.Eligible {
			selected = append(selected, d)
		}
	}
	if len(selected) == 0 {
		m.validation = "Select at least one disk"
		return false
	}
	if m.command.Value() == "" {
		m.command.SetValue(formatPoolCommand(installer.ZPoolCommand(selected)))
	}
	return true
}
