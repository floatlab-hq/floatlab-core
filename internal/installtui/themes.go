package installtui

import (
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

type theme struct {
	name                                                                                    string
	background, panel, foreground, accent, selection, darkForeground, red, buttonBackground lipgloss.Color
}

// Everfloat colors from ui/src/themes/palettes/everfloat.toml.
var everfloat = theme{"Everfloat", "#181d20", "#21272c", "#d3c6aa", "#49D49B", "#3d484d", "#4f585e", "#e67e80", "#2d353b"}

func (m *Model) currentTheme() theme { return everfloat }

func (m *Model) styleInputs() {
	t := m.currentTheme()
	text := lipgloss.NewStyle().Foreground(t.foreground).Background(t.background)
	cursor := lipgloss.NewStyle().Foreground(t.accent).Background(t.background)
	for _, input := range []*textinput.Model{&m.confirmation, &m.timezoneSearch} {
		input.TextStyle = text
		input.PromptStyle = cursor
		input.PlaceholderStyle = text
		input.Cursor.Style = cursor
	}
	for i := range m.fields {
		m.fields[i].TextStyle = text
		m.fields[i].PlaceholderStyle = text
		m.fields[i].Cursor.Style = cursor
	}
	for _, style := range []*textarea.Style{&m.command.FocusedStyle, &m.command.BlurredStyle} {
		style.Base = text
		style.Text = text
		style.CursorLine = lipgloss.NewStyle().Background(t.selection).Foreground(t.foreground)
		style.Placeholder = text
		style.EndOfBuffer = text
	}
	m.command.Cursor.Style = cursor
}
