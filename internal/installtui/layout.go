package installtui

import (
	_ "embed"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

//go:embed logo.txt
var logoFile string

var logo = strings.TrimSuffix(logoFile, "\n")

func (m *Model) contentWidth() int {
	return max(1, min(84, m.width-8))
}

func (m *Model) largeHeader() bool {
	return m.stage != "totp" && m.stage != "review" && m.contentWidth() >= lipgloss.Width(logo) && m.height >= lipgloss.Height(logo)+29
}

func (m *Model) actionSpacing() int { return min(2, max(0, m.height-13)) }

func (m *Model) bodyHeight() int {
	return max(1, m.height-lipgloss.Height(m.header())-7-m.actionSpacing())
}

func (m *Model) canvasLine(line string) string {
	w := m.contentWidth()
	line = ansi.Truncate(line, w, "…")
	style := lipgloss.NewStyle().Foreground(m.currentTheme().foreground).Background(m.currentTheme().background)
	// Inline accents reset terminal colors; restore the canvas between styled runs.
	paint := style.Render(" ")
	if end := strings.IndexByte(paint, ' '); end > 0 {
		line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+paint[:end])
	}
	return style.Width(m.width).Render(strings.Repeat(" ", (m.width-w)/2) + line)
}

func (m *Model) header() string {
	t := m.currentTheme()
	brand := lipgloss.NewStyle().Foreground(t.accent).Background(t.background).Bold(true)
	var lines []string
	if m.largeHeader() {
		lines = []string{"", "", "", ""}
		for _, line := range strings.Split(logo, "\n") {
			lines = append(lines, brand.Bold(false).Render(line))
		}
		lines = append(lines, "")
	}
	divider := lipgloss.NewStyle().Foreground(t.foreground).Background(t.background).Faint(true).Render(strings.Repeat("─", m.contentWidth()))
	lines = append(lines, brand.Render("FloatLab Installer"), "", divider, "")
	for i := range lines {
		lines[i] = m.canvasLine(lines[i])
	}
	return strings.Join(lines, "\n")
}

func (m *Model) tableLayout() bool { return m.contentWidth() >= 60 }

func (m *Model) labelWidth() int { return 32 }

func (m *Model) diskLine() int {
	for i, line := range strings.Split(ansi.Wrap(m.body(), m.contentWidth(), ""), "\n") {
		if strings.HasPrefix(ansi.Strip(line), "> [") {
			return i
		}
	}
	return 3
}

func (m *Model) tableHeader(label, value string) []string {
	t := m.currentTheme()
	style := lipgloss.NewStyle().Foreground(t.foreground).Background(t.background).Faint(true)
	return []string{
		style.Render(m.tableRow(label, value)),
		style.Render(strings.Repeat("─", m.contentWidth())),
	}
}

func (m *Model) tableRow(label, value string) string {
	w := m.labelWidth()
	label = ansi.Truncate(label, w, "…")
	return label + strings.Repeat(" ", max(0, w-ansi.StringWidth(label))) + "  " + value
}
