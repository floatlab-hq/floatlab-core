package installtui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pelletier/go-toml/v2"
)

func TestInstallerUsesOnlyEverfloat(t *testing.T) {
	m := New("unused", nil)
	m.stage = "disks"
	before := m.View()
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	if m.currentTheme().name != "Everfloat" || m.View() != before || strings.Contains(before, "Change Theme") {
		t.Fatal("installer still offers theme selection")
	}
}

func TestEverfloatMatchesUIPalette(t *testing.T) {
	data, err := os.ReadFile("../../ui/src/themes/palettes/everfloat.toml")
	if err != nil {
		t.Fatal(err)
	}
	var palette map[string]string
	if err := toml.Unmarshal(data, &palette); err != nil {
		t.Fatal(err)
	}
	for key, color := range map[string]lipgloss.Color{
		"darker_background": everfloat.background, "dark_background": everfloat.panel,
		"background": everfloat.buttonBackground, "foreground": everfloat.foreground,
		"accent": everfloat.accent, "selection": everfloat.selection,
		"dark_foreground": everfloat.darkForeground, "red": everfloat.red,
	} {
		if string(color) != palette[key] {
			t.Fatalf("Everfloat %s does not match the UI palette", key)
		}
	}
}
