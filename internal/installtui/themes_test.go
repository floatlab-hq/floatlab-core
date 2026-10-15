package installtui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/floatlab/floatlab-core/internal/installer"
	"github.com/muesli/termenv"
	"github.com/pelletier/go-toml/v2"
)

func TestThemeChooserPreservesInstallerState(t *testing.T) {
	m := New("unused", nil)
	if m.currentTheme().name != "Everforest" {
		t.Fatal("default theme is not Everforest")
	}
	m.stage = "settings"
	m.settings()
	m.fields[0].SetValue("my-node")
	m.pending = true
	m.scroll, m.manualScroll = 2, true
	before := m.theme
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if !m.choosingTheme || m.currentTheme() != themes[before+1] {
		t.Fatal("theme preview failed while request was pending")
	}
	m.Update(key("x"))
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.choosingTheme || m.theme != before || m.currentTheme() != themes[before] {
		t.Fatal("cancel did not restore the theme")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.choosingTheme || m.theme != before+1 || m.fields[0].TextStyle.GetForeground() != m.currentTheme().foreground {
		t.Fatal("theme selection did not update the inputs")
	}
	if m.stage != "settings" || !m.pending || m.fields[0].Value() != "my-node" || m.focus != 0 || m.scroll != 2 || !m.manualScroll {
		t.Fatal("theme chooser changed installer state")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m.Update(received{event: installer.Event{Type: "complete"}})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.stage != "complete" || !m.terminal {
		t.Fatal("theme chooser lost a background installer update")
	}
}

func TestThemeCancelRestoresRenderedColors(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	for _, stage := range []string{"welcome", "command", "confirm", "settings", "account", "timezone"} {
		t.Run(stage, func(t *testing.T) {
			m := New("unused", nil)
			m.settings()
			if stage == "timezone" {
				m.openTimezone()
			}
			m.stage = stage
			before := m.View()
			m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
			m.Update(tea.KeyMsg{Type: tea.KeyDown})
			m.View()
			m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if got := m.View(); got != before {
				t.Fatalf("cancel changed rendered colors\nbefore: %q\nafter: %q", before, got)
			}
		})
	}
}

func TestThemesMatchStorybookPalettes(t *testing.T) {
	files, err := filepath.Glob("../../ui/src/themes/palettes/*.toml")
	if err != nil || len(files) != len(themes) {
		t.Fatalf("palette count mismatch: %d files, %d themes: %v", len(files), len(themes), err)
	}
	for _, theme := range themes {
		path := "../../ui/src/themes/palettes/" + strings.ReplaceAll(strings.ToLower(theme.name), " ", "-") + ".toml"
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var palette map[string]string
		if err := toml.Unmarshal(data, &palette); err != nil {
			t.Fatal(err)
		}
		for key, color := range map[string]lipgloss.Color{"background": theme.background, "dark_background": theme.panel, "foreground": theme.foreground, "accent": theme.accent, "selection": theme.selection} {
			if string(color) != palette[key] {
				t.Fatalf("%s %s does not match Storybook", theme.name, key)
			}
		}
	}
}

func TestThemeChooserRendering(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	m := New("unused", nil)
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	for _, width := range []int{20, 40, 80, 140} {
		for _, height := range []int{12, 24, 40} {
			m.Update(tea.WindowSizeMsg{Width: width, Height: height})
			for i := range themes {
				m.themeCursor = i
				view := m.View()
				lines := strings.Split(view, "\n")
				if len(lines) != height {
					t.Fatalf("%s: got %d rows, want %d", themes[i].name, len(lines), height)
				}
				for _, line := range lines {
					if ansi.StringWidth(line) != width {
						t.Fatalf("%s: got width %d, want %d", themes[i].name, ansi.StringWidth(line), width)
					}
				}
				if !strings.Contains(ansi.Strip(view), "> "+themes[i].name) && width >= 40 {
					t.Fatalf("selected theme %s is hidden", themes[i].name)
				}
				if width >= 40 && !strings.Contains(ansi.Strip(lines[len(lines)-1]), "Ctrl+T Change Theme") {
					t.Fatal("theme shortcut is missing from the bottom row")
				}
				if !strings.Contains(view, "\x1b[") {
					t.Fatal("theme colors were not rendered")
				}
			}
		}
	}
}
