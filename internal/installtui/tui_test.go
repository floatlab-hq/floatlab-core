package installtui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/floatlab/floatlab-core/internal/installer"
)

func TestCtrlRQuitsMockFromAnyScreen(t *testing.T) {
	for _, stage := range []string{"welcome", "connecting", "disks", "command", "confirm", "settings", "account", "review", "totp", "timezone", "waiting", "complete", "failed", "unavailable"} {
		m := New("unused", nil)
		m.mock, m.pending, m.choosingTheme = true, true, true
		m.stage = stage
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
		if cmd == nil {
			t.Fatalf("%s: Ctrl+R did not quit", stage)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok || m.rebooting {
			t.Fatalf("%s: Ctrl+R did not safely quit mock mode", stage)
		}
		if !strings.Contains(m.footer(80), "Ctrl+R Quit") {
			t.Fatal("mock quit shortcut is missing")
		}
	}
}

func TestCtrlRUsesRebootServiceOnlyWhenTerminal(t *testing.T) {
	m := New("unused", nil)
	var output bytes.Buffer
	m.client = &client{encoder: json.NewEncoder(&output)}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if cmd != nil || m.rebooting || m.validation == "" {
		t.Fatal("Ctrl+R allowed reboot before completion or failure")
	}
	for _, stage := range []string{"complete", "failed"} {
		m.stage, m.terminal, m.pending, m.rebooting = stage, true, false, false
		output.Reset()
		_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
		if cmd == nil || !m.rebooting || !m.paused {
			t.Fatal("Ctrl+R did not request reboot")
		}
		if result := cmd().(sent); result.err != nil {
			t.Fatal(result.err)
		}
		var request installer.Request
		if err := json.Unmarshal(output.Bytes(), &request); err != nil || request.Type != "reboot" {
			t.Fatalf("unexpected reboot request: %s, %v", output.String(), err)
		}
		_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
		if cmd != nil {
			t.Fatal("repeated Ctrl+R sent another reboot")
		}
	}
	if !strings.Contains(m.footer(80), "Ctrl+R Reboot") {
		t.Fatal("reboot shortcut is missing")
	}
}

func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestFooterUsesExactlySevenFullWidthLines(t *testing.T) {
	m := New("unused", nil)
	m.stage = "settings"
	m.settings()
	m.status = installer.Status{Phase: "Installing Floatlab", Progress: 0.5, Copied: 2 * installer.GiB, Total: 4 * installer.GiB, BytesPerSecond: 100 * float64(installer.MiB)}
	for _, width := range []int{40, 80, 140} {
		m.width, m.height = width, 24
		m.resize()
		rows := strings.Split(m.footer(width), "\n")
		if len(rows) != 7 {
			t.Fatal(len(rows))
		}
		for _, row := range rows {
			if ansi.StringWidth(row) != width {
				t.Fatalf("footer width %d != %d", ansi.StringWidth(row), width)
			}
		}
		if got := len(strings.Split(m.View(), "\n")); got != m.height {
			t.Fatalf("screen uses %d lines, expected %d", got, m.height)
		}
	}
}

func TestFailuresRemainVisibleAndHaveNoCountdown(t *testing.T) {
	m := New("unused", nil)
	m.stage, m.terminal, m.failure, m.paused = "failed", true, "Image write failed", true
	m.status.Phase = "Installing Floatlab"
	for range 200 {
		_, _ = m.Update(tick{})
	}
	if m.rebooting || !strings.Contains(m.View(), m.failure) {
		t.Fatal("failure disappeared or reboot started")
	}
}

func TestNoQuitKeysAndPausedCountdown(t *testing.T) {
	m := New("unused", nil)
	m.stage, m.countdown, m.terminal = "complete", 120, true
	for _, kind := range []tea.KeyType{tea.KeyCtrlC, tea.KeyCtrlD} {
		_, cmd := m.Update(tea.KeyMsg{Type: kind})
		if cmd != nil {
			t.Fatal("quit key produced command")
		}
	}
	_, _ = m.Update(key("p"))
	for range 200 {
		_, _ = m.Update(tick{})
	}
	if !m.paused || m.countdown != 120 || m.rebooting {
		t.Fatal("paused countdown advanced")
	}
}

func TestDiskCheckboxesGenerateMirror(t *testing.T) {
	m := New("unused", nil)
	m.stage = "disks"
	m.disks = []installer.Disk{{Path: "/dev/disk/by-id/a", Device: "/dev/sda", Eligible: true}, {Path: "/dev/disk/by-id/b", Device: "/dev/sdb", Eligible: true}, {Device: "/dev/sdc", Eligible: false}}
	_, _ = m.Update(key(" "))
	m.cursor = 1
	_, _ = m.Update(key(" "))
	m.cursor = 2
	_, _ = m.Update(key(" "))
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.stage != "command" || !strings.Contains(m.command.Value(), "MIRROR /dev/disk/by-id/a /dev/disk/by-id/b") || m.selected[2] {
		t.Fatal(m.command.Value(), m.selected)
	}
}

func TestEnrollmentCanScrollBackToSecretAndCorrectCode(t *testing.T) {
	m := New("unused", nil)
	m.stage = "totp"
	m.settings()
	m.enrollment = &installer.Enrollment{URI: strings.Repeat("otpauth", 30), Secret: "ENROLLMENT_SECRET", RecoveryCodes: []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}}
	m.manualScroll = true
	m.scroll = 0
	if !strings.Contains(m.View(), "ENROLLMENT_SECRET") {
		t.Fatal("enrollment secret is hidden")
	}
	m.scrollBy(100)
	m.scrollBy(-100)
	if !strings.Contains(m.View(), "ENROLLMENT_SECRET") {
		t.Fatal("cannot scroll back to enrollment secret")
	}
	m.totpCode = "123456"
	m.focus = 1
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.focus != 0 || m.confirmation.Value() != "123456" {
		t.Fatal("cannot correct authenticator code")
	}
}

func TestThreeLinePoolEditorPreservesArguments(t *testing.T) {
	disks := []installer.Disk{{Path: "/dev/disk/by-id/a", Device: "/dev/sda", Eligible: true}, {Path: "/dev/disk/by-id/b", Device: "/dev/sdb", Eligible: true}}
	for _, selected := range [][]installer.Disk{disks[:1], disks} {
		generated := installer.ZPoolCommand(selected)
		formatted := formatPoolCommand(generated)
		lines := strings.Split(formatted, "\n")
		if len(lines) != 3 || lines[0] != "zpool create floatlab" || !strings.HasPrefix(lines[1], "-f ") {
			t.Fatal(formatted)
		}
		if len(selected) == 1 && lines[2] != selected[0].Path {
			t.Fatal(lines[2])
		}
		if len(selected) > 1 && !strings.HasPrefix(lines[2], "MIRROR ") {
			t.Fatal(lines[2])
		}
		original, err := installer.ParseZPool(generated, selected)
		if err != nil {
			t.Fatal(err)
		}
		edited, err := installer.ParseZPool(formatted, selected)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(original, " ") != strings.Join(edited, " ") {
			t.Fatal("editor changed arguments", original, edited)
		}
	}
	m := New("unused", nil)
	m.stage = "command"
	m.command.SetValue(formatPoolCommand(installer.ZPoolCommand(disks)))
	m.resize()
	if !strings.Contains(m.body(), "╭") || !strings.Contains(m.body(), "╰") {
		t.Fatal("missing editor border")
	}
	before := strings.Count(m.command.Value(), "\n")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.pending || strings.Count(m.command.Value(), "\n") != before+1 {
		t.Fatal("Enter did not insert a newline")
	}
}

func TestTimezoneSelectionAndNetworkRadioNavigation(t *testing.T) {
	m := New("unused", nil)
	m.stage = "settings"
	m.settings()
	m.setFocus(1)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.stage != "timezone" {
		t.Fatal("timezone selector did not open")
	}
	_, _ = m.Update(key("Melbourne"))
	matches := m.timezoneMatches()
	if len(matches) != 1 || matches[0] != "Australia/Melbourne" {
		t.Fatal(matches)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.stage != "settings" || m.focus != 2 || m.config().Timezone != "Australia/Melbourne" {
		t.Fatal("timezone was not selected")
	}
	if !strings.Contains(m.body(), "(●) DHCP") || strings.Contains(m.body(), "IPv4 address (static only)") {
		t.Fatal("DHCP settings do not hide static fields")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if !strings.Contains(m.body(), "(●) Static") || !strings.Contains(m.body(), "IPv4 address (static only)") {
		t.Fatal("Static radio did not reveal address fields")
	}
	m.moveFocus(1)
	if m.focus != 3 {
		t.Fatal("static address field was skipped")
	}
	m.setFocus(2)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m.moveFocus(1)
	if m.focus != 6 || m.config().Network.IPv4.Mode != "dhcp" {
		t.Fatal("DHCP did not skip static fields")
	}
	m.stage = "account"
	m.setFocus(7)
	if strings.Contains(m.body(), "IPv4 configuration") || !strings.Contains(m.body(), "Administrator username") {
		t.Fatal("account and network steps are not separated")
	}
}

func TestAllTimezoneChoicesAreLoadable(t *testing.T) {
	for _, zone := range timezones {
		if _, err := time.LoadLocation(zone); err != nil {
			t.Fatal(zone, err)
		}
	}
}

func TestAccountStepWaitsForNetworkValidation(t *testing.T) {
	m := New("unused", nil)
	m.stage = "settings"
	m.settings()
	m.setFocus(6)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !m.pending || m.stage != "settings" {
		t.Fatal("account opened before validation")
	}
	_, _ = m.Update(received{event: installer.Event{ID: m.requestID, Type: "error", Error: "Invalid gateway"}})
	if m.stage != "settings" || m.pending || m.validation != "Invalid gateway" {
		t.Fatal("network rejection was not kept editable")
	}
	_, _ = m.Update(received{event: installer.Event{ID: m.requestID, Type: "validate-host"}})
	if m.stage != "account" || m.focus != 7 {
		t.Fatal("valid network did not open account setup")
	}
}
