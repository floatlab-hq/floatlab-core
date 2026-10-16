package installtui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/floatlab/floatlab-core/internal/installer"
)

func TestRemovedShortcutsDoNotQuitPauseOrReboot(t *testing.T) {
	for _, stage := range []string{"disks", "complete", "failed"} {
		m := New("unused", nil)
		m.stage, m.terminal, m.countdown = stage, stage != "disks", 120
		for _, key := range []tea.KeyMsg{{Type: tea.KeyCtrlC}, {Type: tea.KeyCtrlD}, {Type: tea.KeyRunes, Runes: []rune("p")}, {Type: tea.KeyRunes, Runes: []rune("r")}} {
			_, cmd := m.Update(key)
			if cmd != nil || m.rebooting || m.paused {
				t.Fatalf("%s: removed shortcut still acts", stage)
			}
		}
		footer := ansi.Strip(m.footer(80))
		if strings.Contains(footer, "Quit") || strings.Contains(footer, "Pause") {
			t.Fatal("removed shortcuts remain in footer")
		}
	}
}

func TestRebootButtonUsesService(t *testing.T) {
	for _, stage := range []string{"complete", "failed"} {
		m := New("unused", nil)
		m.stage, m.terminal = stage, true
		var output bytes.Buffer
		m.client = &client{encoder: json.NewEncoder(&output)}
		m.focusAction(2)
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil || !m.rebooting {
			t.Fatal("Reboot button did not request reboot")
		}
		cmd()
		var request installer.Request
		if err := json.Unmarshal(output.Bytes(), &request); err != nil || request.Type != "reboot" {
			t.Fatal("Reboot button sent wrong request")
		}
	}
}

func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestTOTPRadioUpdatesConfiguration(t *testing.T) {
	m := New("unused", nil)
	m.settings()
	m.stage = "account"
	m.setFocus(10)
	for _, choice := range []struct {
		key  tea.KeyMsg
		want bool
	}{{tea.KeyMsg{Type: tea.KeyLeft}, true}, {tea.KeyMsg{Type: tea.KeyRight}, false}, {key(" "), true}, {key("no"), true}} {
		m.Update(choice.key)
		if m.config().TOTP != choice.want {
			t.Fatal("TOTP radio did not preserve the chosen configuration")
		}
	}
	if !strings.Contains(m.body(), "(●) Yes    ( ) No") {
		t.Fatal("TOTP radio selection is not visible")
	}
}

func TestPasswordStrengthUpdatesWithoutExposingPassword(t *testing.T) {
	m := New("unused", nil)
	m.settings()
	m.stage = "account"
	m.setFocus(8)
	if m.passwordStrength() != "" {
		t.Fatal("empty password has a strength indicator")
	}
	m.Update(key("password"))
	weak := m.passwordScore
	if !strings.Contains(m.View(), "Very weak") || m.fields[8].EchoMode != textinput.EchoPassword || m.fields[9].EchoMode != textinput.EchoPassword {
		t.Fatal("weak password meter is missing")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	password := "vN7!qZ2@bL9#sR4$xT6"
	m.Update(key(password))
	if m.passwordScore <= weak || !strings.Contains(m.View(), "Very strong") {
		t.Fatal("meter did not update for a strong password")
	}
	if strings.Contains(m.View(), password) || m.fields[8].Value() != password {
		t.Fatal("password was exposed or changed")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if m.passwordStrength() != "" {
		t.Fatal("meter was not cleared")
	}
}

func TestNextButtonsNavigateAndValidate(t *testing.T) {
	for _, stage := range []string{"settings", "account"} {
		m := New("unused", nil)
		m.settings()
		m.stage = stage
		fields := m.visibleFields()
		m.setFocus(fields[len(fields)-1])
		if !strings.Contains(ansi.Strip(m.actionBar()), "Next →") {
			t.Fatalf("%s: Next button is missing from the action bar", stage)
		}
		if !strings.Contains(ansi.Strip(m.View()), "Next →") {
			t.Fatalf("%s: Next button is hidden", stage)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
		if m.focus != len(m.fields) || m.actionFocus != 2 || strings.Contains(ansi.Strip(m.actionBar()), ">") {
			t.Fatalf("%s: Next button is not focusable", stage)
		}
		m.Update(key("x"))
		m.Update(tick{})
		m.Update(tea.KeyMsg{Type: tea.KeyUp})
		if m.focus != fields[len(fields)-1] {
			t.Fatal("cannot navigate back from Next")
		}
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
		_, cmd := m.Update(key(" "))
		if stage == "settings" {
			if cmd == nil || !m.pending || m.stage != "settings" {
				t.Fatal("Next bypassed network validation")
			}
			m.Update(received{event: installer.Event{ID: m.requestID, Type: "error", Error: "Invalid network"}})
			if m.pending || m.stage != "settings" || !strings.Contains(m.View(), "Next") {
				t.Fatal("rejected network did not remain editable")
			}
		} else if cmd == nil || !m.pending || m.stage != "account" {
			t.Fatal("Next bypassed account validation")
		}
	}
}

func TestFooterUsesExactlySixFullWidthLines(t *testing.T) {
	m := New("unused", nil)
	m.stage = "settings"
	m.settings()
	m.status = installer.Status{Phase: "Installing Floatlab", Progress: 0.5, Copied: 2 * installer.GiB, Total: 4 * installer.GiB, BytesPerSecond: 100 * float64(installer.MiB)}
	for _, width := range []int{40, 80, 140} {
		m.width, m.height = width, 24
		m.resize()
		rows := strings.Split(m.footer(width), "\n")
		if len(rows) != 6 {
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
	if !strings.Contains(ansi.Strip(m.footer(80)), "Enter Reboot") {
		t.Fatal("failure footer does not describe its Reboot action")
	}
}

func TestCountdownContinuesWithoutPauseShortcut(t *testing.T) {
	m := New("unused", nil)
	m.stage, m.countdown, m.terminal = "complete", 120, true
	m.Update(key("p"))
	m.Update(tick{})
	if m.paused || m.countdown != 119 || m.rebooting {
		t.Fatal("removed pause shortcut stopped countdown")
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
	if !m.pending || m.stage != "disks" || !strings.Contains(m.command.Value(), "MIRROR /dev/disk/by-id/a /dev/disk/by-id/b") || m.selected[2] {
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
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
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
	if !strings.Contains(m.body(), "┌") || !strings.Contains(m.body(), "└") {
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

func TestCtrlRRebootsFromEveryScreen(t *testing.T) {
	for _, stage := range []string{"connecting", "disks", "command", "confirm", "timezone", "settings", "account", "totp", "review", "waiting", "complete", "failed", "unavailable"} {
		m := New("unused", nil)
		m.stage, m.terminal = stage, stage == "complete" || stage == "failed"
		var output bytes.Buffer
		m.client = &client{encoder: json.NewEncoder(&output)}
		m.pending = true // Reboot must also interrupt a pending form request.
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
		if cmd == nil || !m.rebooting || !strings.Contains(ansi.Strip(m.helpBar(88)), "Ctrl+R Reboot") {
			t.Fatal("reboot shortcut is missing")
		}
		cmd()
		var request installer.Request
		if err := json.Unmarshal(output.Bytes(), &request); err != nil || request.Type != "reboot" {
			t.Fatal("Ctrl+R did not send a reboot request")
		}
		_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
		if cmd != nil {
			t.Fatal("repeated Ctrl+R sent duplicate reboot request")
		}
	}
}

func TestCtrlRQueuesUntilServiceConnects(t *testing.T) {
	m := New("unused", nil)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if cmd != nil || !m.rebooting {
		t.Fatal("startup reboot was not queued")
	}
	var output bytes.Buffer
	_, cmd = m.Update(connected{client: &client{encoder: json.NewEncoder(&output)}})
	commands := cmd().(tea.BatchMsg)
	commands[1]()
	var request installer.Request
	if err := json.Unmarshal(output.Bytes(), &request); err != nil || request.Type != "reboot" {
		t.Fatal("startup reboot was not sent after connection")
	}
}
