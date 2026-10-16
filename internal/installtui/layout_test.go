package installtui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func TestCenteredLayoutKeepsFieldsAndActionsVisible(t *testing.T) {
	for _, size := range [][2]int{{40, 16}, {80, 24}, {120, 40}, {160, 50}} {
		for _, stage := range []string{"disks", "command", "confirm", "settings", "account", "review", "timezone", "totp", "waiting", "complete", "failed"} {
			m := New("unused", nil)
			m.width, m.height = size[0], size[1]
			m.settings()
			m.stage = stage
			if stage == "timezone" {
				m.openTimezone()
			}
			rows := strings.Split(ansi.Strip(m.View()), "\n")
			if len(rows) != size[1] {
				t.Fatalf("%s %v: got %d rows", stage, size, len(rows))
			}
			for _, row := range rows {
				if ansi.StringWidth(row) != size[0] {
					t.Fatalf("%s %v: wrong row width: %q", stage, size, row)
				}
			}
			if size[0] >= 120 {
				margin := strings.Repeat(" ", (m.width-m.contentWidth())/2)
				for _, row := range rows {
					if !strings.HasPrefix(row, margin) {
						t.Fatalf("%s: content escaped its centered column: %q", stage, row)
					}
				}
			}
			if stage == "account" || stage == "settings" {
				for _, field := range m.visibleFields() {
					m.setFocus(field)
					for _, row := range strings.Split(ansi.Strip(m.body()), "\n") {
						for _, other := range m.visibleFields() {
							if strings.Contains(row, m.labels[other]) && strings.Index(row, m.labels[other]) != 2 {
								t.Fatalf("%s: field label moved when focus changed: %q", stage, row)
							}
						}
					}
					view := ansi.Strip(m.View())
					if !strings.Contains(view, "> "+m.labels[field]) {
						t.Fatalf("%s %v: focused field %s is hidden", stage, size, m.labels[field])
					}
				}
			}
		}
	}
}

func TestTableLayoutAlignsFieldsAndKeepsPasswordMasked(t *testing.T) {
	m := New("unused", nil)
	m.width, m.height = 120, 40
	m.settings()
	m.stage = "account"
	m.fields[8].SetValue("private-password")
	rows := strings.Split(ansi.Strip(m.View()), "\n")
	username, password := -1, -1
	for i, row := range rows {
		if strings.Contains(row, "Administrator username") {
			username = i
		}
		if strings.Contains(row, "Password") {
			password = i
		}
	}
	if username < 0 || password != username+1 || strings.Contains(strings.Join(rows, "\n"), "private-password") {
		t.Fatal("account table lost its row alignment or exposed the password")
	}
	for value, indicator := range map[string]string{"": "", "different-password": "✗", "private-password": "✓"} {
		m.fields[9].SetValue(value)
		for _, row := range strings.Split(ansi.Strip(m.body()), "\n") {
			if strings.Contains(row, "Confirm password") {
				if indicator == "" && strings.ContainsAny(row, "✓✗") || indicator != "" && (!strings.HasSuffix(row, indicator) || ansi.StringWidth(row) != m.contentWidth()) {
					t.Fatal("password confirmation indicator is incorrect or not right aligned")
				}
			}
		}
	}
}

func TestFlatCanvasRestoresBackgroundAfterInlineAccents(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	m := New("unused", nil)
	accent := lipgloss.NewStyle().Foreground(m.currentTheme().accent).Render("FLOATLAB")
	line := m.canvasLine(accent + "  /  Installer")
	if strings.Contains(line, "\x1b[0m  /  Installer") {
		t.Fatal("inline accent reset left the canvas using the terminal's default background")
	}
	if !strings.Contains(ansi.Strip(line), "FLOATLAB  /  Installer") || ansi.StringWidth(line) != m.width {
		t.Fatal("restoring the background changed the row's content or width")
	}
}

func TestTimezoneCursorUsesAccentColor(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	m := New("unused", nil)
	m.settings()
	m.openTimezone()
	m.timezoneSearch.SetValue("Australia/")
	m.timezoneCursor = 0
	matches := m.timezoneMatches()
	for _, cursor := range []int{0, 1} {
		m.timezoneCursor = cursor
		selected := lipgloss.NewStyle().Foreground(m.currentTheme().accent).Background(m.currentTheme().background).Render("> " + matches[cursor])
		if !strings.Contains(m.timezoneBody(), selected) {
			t.Fatal("current timezone row does not use the accent color")
		}
	}
}
