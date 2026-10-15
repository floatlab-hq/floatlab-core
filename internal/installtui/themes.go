package installtui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

type theme struct {
	name                                             string
	background, panel, foreground, accent, selection lipgloss.Color
}

// Palette colors from floatlab-ui's Storybook (ui/src/themes/palettes).
var themes = []theme{
	{"Catppuccin Latte", "#eff1f5", "#e3e4e8", "#4c4f69", "#1e66f5", "#ccd0da"},
	{"Catppuccin", "#1e1e2e", "#161622", "#cdd6f4", "#89b4fa", "#45475a"},
	{"Ethereal", "#060B1E", "#040816", "#ffcead", "#7d82d9", "#252e56"},
	{"Everforest", "#2d353b", "#21272c", "#d3c6aa", "#7fbbb3", "#3d484d"},
	{"Flexoki Light", "#FFFCF0", "#f2efe4", "#100F0F", "#205EA6", "#CECDC3"},
	{"Gruvbox", "#282828", "#1e1e1e", "#d4be98", "#7daea3", "#504945"},
	{"Hackerman", "#0B0C16", "#080910", "#ddf7ff", "#82FB9C", "#1f253a"},
	{"Kanagawa", "#1f1f28", "#17171e", "#dcd7ba", "#dcd7ba", "#363646"},
	{"Last Horizon", "#0c0b0c", "#090809", "#FAFCFB", "#b59790", "#584e51"},
	{"Lumon", "#16242d", "#101b21", "#d6e2ee", "#8bc9eb", "#243d56"},
	{"Lupine", "#fafafa", "#ececec", "#212121", "#3264eb", "#d0d0d0"},
	{"Matte Black", "#121212", "#0d0d0d", "#bebebe", "#e68e0d", "#2a2a2a"},
	{"Miasma", "#222222", "#191919", "#c2c2b0", "#78824b", "#383838"},
	{"Nord", "#2e3440", "#222730", "#d8dee9", "#81a1c1", "#434c5e"},
	{"Osaka Jade", "#111c18", "#0c1512", "#C1C497", "#509475", "#32473B"},
	{"Retro 82", "#05182e", "#031222", "#f6dcac", "#faa968", "#134e5a"},
	{"Ristretto", "#2c2525", "#211b1b", "#e6d9db", "#f38d70", "#403e41"},
	{"Rose Pine", "#faf4ed", "#ede7e1", "#575279", "#56949f", "#dfdad9"},
	{"Solitude", "#101315", "#0c0e10", "#cacccc", "#798186", "#343d41"},
	{"Starship", "#1c1c1e", "#161618", "#f5f5f7", "#ff9f0a", "#3a3a3c"},
	{"Tokyo Night", "#1a1b26", "#13141c", "#a9b1d6", "#7aa2f7", "#292e42"},
	{"Vantablack", "#000000", "#090909", "#ffffff", "#8d8d8d", "#1a1a1a"},
	{"White", "#ffffff", "#f5f5f5", "#000000", "#6e6e6e", "#c0c0c0"},
}

func (m *Model) currentTheme() theme {
	if m.choosingTheme {
		return themes[m.themeCursor]
	}
	return themes[m.theme]
}

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

func (m *Model) themeBody() string {
	lines := []string{"↑/↓: preview", "Enter: select", "Esc: cancel"}
	if m.height < 16 {
		lines = nil
	}
	rows := max(1, m.height-9-len(lines))
	start := min(max(0, m.themeCursor-rows/2), max(0, len(themes)-rows))
	for i := start; i < min(len(themes), start+rows); i++ {
		mark := "  "
		if i == m.themeCursor {
			mark = "> "
		}
		lines = append(lines, mark+themes[i].name)
	}
	return strings.Join(lines, "\n")
}
