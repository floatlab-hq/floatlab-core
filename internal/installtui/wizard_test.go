package installtui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/floatlab/floatlab-core/internal/installer"
)

func TestInstallerStartsConnectingWithoutAWelcomeScreen(t *testing.T) {
	m := New("/nonexistent/floatlab-install-service", nil)
	if m.stage != "connecting" || m.canBack() || m.canNext() {
		t.Fatal("installer requires a welcome-screen action")
	}
	commands, ok := m.Init()().(tea.BatchMsg)
	if !ok || len(commands) != 2 {
		t.Fatal("installer did not start connecting automatically")
	}
	if _, ok := commands[1]().(connected); !ok {
		t.Fatal("startup command did not attempt the service connection")
	}
	m.Update(received{event: installer.Event{Type: "inventory"}})
	if m.stage != "disks" || m.canBack() {
		t.Fatal("inventory did not open disk selection as the first screen")
	}
}

func TestWizardArrowNavigationPreservesEdits(t *testing.T) {
	for _, stage := range []string{"disks", "command", "confirm", "settings", "account", "review", "totp", "timezone"} {
		t.Run(stage, func(t *testing.T) {
			m := New("unused", nil)
			m.settings()
			m.fields[0].SetValue("edited-node")
			m.fields[8].SetValue("edited-password")
			m.stage = stage
			m.selected[0] = true
			m.command.SetValue("custom pool command")
			m.focusAction(2)
			m.Update(tea.KeyMsg{Type: tea.KeyLeft})
			if stage == "settings" || stage == "disks" {
				if m.actionFocus != 2 || m.canBack() {
					t.Fatal("Back enabled across the active storage boundary")
				}
				return
			}
			if m.actionFocus != 1 {
				t.Fatal("left arrow did not reach Back")
			}
			m.Update(tea.KeyMsg{Type: tea.KeyRight})
			if m.actionFocus != 2 {
				t.Fatal("right arrow did not reach Next")
			}
			m.Update(tea.KeyMsg{Type: tea.KeyLeft})
			m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if m.stage == stage || m.fields[0].Value() != "edited-node" || m.fields[8].Value() != "edited-password" || !m.selected[0] || m.command.Value() != "custom pool command" {
				t.Fatal("Back failed or discarded installer edits")
			}
		})
	}
}

func TestWizardNextUsesServiceValidation(t *testing.T) {
	for stage, request := range map[string]string{"command": "preview", "confirm": "confirm", "settings": "validate-host", "account": "validate-account", "review": "configure"} {
		t.Run(stage, func(t *testing.T) {
			m := New("unused", nil)
			m.settings()
			m.stage = stage
			var output bytes.Buffer
			m.client = &client{encoder: json.NewEncoder(&output)}
			m.focusAction(2)
			_, cmd := m.Update(key(" "))
			if cmd == nil || !m.pending {
				t.Fatal("Next bypassed the service")
			}
			if result := cmd().(sent); result.err != nil {
				t.Fatal(result.err)
			}
			var got installer.Request
			if err := json.Unmarshal(output.Bytes(), &got); err != nil || got.Type != request {
				t.Fatalf("got %s, want %s: %v", output.String(), request, err)
			}
			if stage == "confirm" && (got.Confirmation != "ERASE ALL DATA" || strings.Contains(m.body(), "Type ERASE")) {
				t.Fatal("erase action did not confirm the preview without typed input")
			}
		})
	}
}

func TestAccountAndTOTPStayOnTheirScreensUntilValidated(t *testing.T) {
	m := New("unused", nil)
	m.settings()
	m.stage = "account"
	m.setFocus(10)
	var output bytes.Buffer
	m.client = &client{encoder: json.NewEncoder(&output)}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || m.stage != "account" {
		t.Fatal("account advanced before validation")
	}
	m.Update(received{event: installer.Event{ID: m.requestID, Type: "error", Error: "passwords do not match"}})
	m.Update(struct{}{}) // Cursor and other non-edit messages must retain errors.
	if m.stage != "account" || !strings.Contains(m.View(), "passwords do not match") {
		t.Fatal("account error did not stay on its screen")
	}
	if len(m.visibleFields()) != 4 || m.visibleFields()[3] != 10 {
		t.Fatal("account screen does not include the TOTP choice")
	}
	m.fields[10].SetValue("yes")
	_, cmd = m.Update(received{event: installer.Event{ID: m.requestID, Type: "validate-account"}})
	commands, ok := cmd().(tea.BatchMsg)
	if !ok || len(commands) != 2 || !m.pending || m.stage != "account" {
		t.Fatal("validated account did not request TOTP setup")
	}
	output.Reset()
	commands[0]()
	var request installer.Request
	if err := json.Unmarshal(output.Bytes(), &request); err != nil || request.Type != "enroll" {
		t.Fatal("account did not request enrollment")
	}
	m.Update(received{event: installer.Event{ID: m.requestID, Type: "enroll", Enrollment: &installer.Enrollment{Secret: "TEST"}}})
	m.confirmation.SetValue("123456")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.stage != "totp" || m.focus != 0 {
		t.Fatal("authenticator advanced before verification")
	}
	m.Update(received{event: installer.Event{ID: m.requestID, Type: "error", Error: "invalid or expired authenticator code"}})
	if m.stage != "totp" || m.focus != 0 {
		t.Fatal("invalid authenticator code left its screen")
	}
	m.Update(received{event: installer.Event{Type: "verify-totp"}})
	if m.focus != 1 || m.confirmation.Value() != "" {
		t.Fatal("verified authenticator did not open recovery codes")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.stage != "totp" || m.validation == "" {
		t.Fatal("unsaved recovery codes reached Review")
	}
	m.confirmation.SetValue("SAVED")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.stage != "review" || m.config().TOTPCode != "123456" || !m.config().RecoverySaved {
		t.Fatal("Review lost the verified authenticator state")
	}
}

func TestStorageNextPreviewsAndCustomiseOpensEditor(t *testing.T) {
	for _, action := range []int{2, 3} {
		m := New("unused", nil)
		m.stage = "disks"
		m.disks = []installer.Disk{{Path: "/dev/disk/by-id/a", Device: "/dev/sda", Eligible: true}}
		m.selected[0] = true
		var output bytes.Buffer
		m.client = &client{encoder: json.NewEncoder(&output)}
		if action == 3 {
			m.stage = "confirm"
			m.command.SetValue("zpool create floatlab /dev/disk/by-id/a")
		}
		m.focusAction(action)
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if action == 3 {
			if m.stage != "command" || m.pending || cmd == nil || m.command.Value() == "" {
				t.Fatal("Customise zpool did not open the editor")
			}
			continue
		}
		if cmd == nil || !m.pending {
			t.Fatal("Next did not request a preview")
		}
		cmd()
		var request installer.Request
		if err := json.Unmarshal(output.Bytes(), &request); err != nil || request.Type != "preview" {
			t.Fatalf("Next sent an unexpected request: %s", output.String())
		}
	}
}

func TestWizardBackDoesNotReplayStorageOrResetTheSession(t *testing.T) {
	m := New("unused", nil)
	m.client = &client{}
	m.disks = []installer.Disk{{Path: "/dev/disk/by-id/a", Eligible: true}}
	m.selected[0] = true
	m.command.SetValue("custom pool command")
	m.stage = "command"
	m.back()
	m.stage = "confirm"
	m.focusAction(3)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.stage != "command" || m.command.Value() != "custom pool command" {
		t.Fatal("returning to the editor discarded the custom command")
	}
	m.back()
	m.back()
	if m.stage != "disks" || m.canBack() {
		t.Fatal("disk selection allowed navigating back to a removed welcome screen")
	}
	for _, stage := range []string{"settings", "waiting", "complete", "failed"} {
		m.stage = stage
		m.back()
		if m.stage != stage {
			t.Fatalf("Back crossed an irreversible boundary from %s", stage)
		}
	}
}

func TestWizardActionsAreReachableWithoutTab(t *testing.T) {
	for _, stage := range []string{"disks", "command", "confirm", "settings", "account", "review", "totp", "timezone", "complete", "failed"} {
		t.Run(stage, func(t *testing.T) {
			m := New("unused", nil)
			m.settings()
			m.stage, m.terminal = stage, stage == "complete" || stage == "failed"
			m.disks = []installer.Disk{{Eligible: true}}
			if stage == "settings" || stage == "account" {
				fields := m.visibleFields()
				m.setFocus(fields[len(fields)-1])
			}
			if stage == "timezone" {
				m.openTimezone()
				m.timezoneCursor = len(m.timezoneMatches()) - 1
			}
			if stage == "totp" {
				m.focus = 1
			}
			m.Update(tea.KeyMsg{Type: tea.KeyDown})
			want := 2
			if stage == "confirm" {
				want = 3
			}
			if m.actionFocus != want {
				t.Fatal("down arrow did not reach the next available action")
			}
			m.Update(key("x"))
			m.Update(tea.KeyMsg{Type: tea.KeyUp})
			if m.actionFocus != 0 {
				t.Fatal("up arrow did not return to content")
			}
		})
	}
}

func TestWizardScreenBoundsAndContextualHelp(t *testing.T) {
	for _, stage := range []string{"connecting", "disks", "command", "confirm", "settings", "account", "timezone", "review", "totp", "waiting", "complete", "failed", "unavailable"} {
		for _, width := range []int{20, 40, 80, 140} {
			m := New("unused", nil)
			m.settings()
			m.stage, m.width, m.height = stage, width, 24
			if stage == "timezone" {
				m.openTimezone()
			}
			m.resize()
			rows := strings.Split(m.View(), "\n")
			if len(rows) != m.height {
				t.Fatalf("%s: got %d rows", stage, len(rows))
			}
			for _, row := range rows {
				if ansi.StringWidth(row) != width {
					t.Fatalf("%s: width %d: %q", stage, width, row)
				}
			}
			if strings.Contains(ansi.Strip(rows[len(rows)-2]), "Change Theme") {
				t.Fatalf("%s: removed theme shortcut remains visible", stage)
			}
			if width >= 80 && (strings.Contains(ansi.Strip(m.actionBar()), "Back") != m.canBack() || !strings.Contains(ansi.Strip(m.actionBar()), "Next") && !strings.Contains(ansi.Strip(m.actionBar()), "Reboot") && stage != "confirm" && stage != "review") {
				t.Fatalf("%s: wizard navigation is missing", stage)
			}
		}
	}
	m := New("unused", nil)
	m.stage = "disks"
	if !strings.Contains(ansi.Strip(m.helpBar(100)), "Space Select") || !strings.Contains(ansi.Strip(m.helpBar(100)), "↑↓ Move") {
		t.Fatal("disk selection shortcuts are missing")
	}
}

func TestAccountWithoutTOTPAdvancesDirectlyToReview(t *testing.T) {
	m := New("unused", nil)
	m.settings()
	m.stage = "account"
	m.fields[10].SetValue("no")
	m.Update(received{event: installer.Event{Type: "validate-account"}})
	if m.stage != "review" || m.pending {
		t.Fatal("TOTP disabled did not skip enrollment")
	}
	m.back()
	if m.stage != "account" || m.focus != 10 {
		t.Fatal("Review Back did not return to account TOTP choice")
	}
}

func TestReviewIncludesEveryStepWithoutExposingSecrets(t *testing.T) {
	m := New("unused", nil)
	m.settings()
	m.stage = "review"
	m.width, m.height = 160, 47
	for index, value := range map[int]string{0: "review-node", 1: "Australia/Melbourne", 2: "static", 3: "192.0.2.10", 4: "255.255.255.0", 5: "192.0.2.1", 6: "1.1.1.1", 7: "review-admin", 8: "private-password", 9: "private-password", 10: "yes"} {
		m.fields[index].SetValue(value)
	}
	m.preview = installer.Event{Disks: []installer.Disk{{Device: "/dev/sda", Model: "Review SSD"}}, Command: "zpool create floatlab /dev/sda2"}
	m.enrollment = &installer.Enrollment{Secret: "PRIVATE-TOTP-SECRET", RecoveryCodes: []string{"private-recovery-code"}}
	m.totpCode = "123456"
	m.confirmation.SetValue("SAVED")
	view := ansi.Strip(m.View())
	for _, want := range []string{"Review SSD", "review-node", "Australia/Melbourne", "STATIC", "192.0.2.10", "255.255.255.0", "192.0.2.1", "1.1.1.1", "review-admin", "Set and confirmed", "Enabled and verified", "Recovery codes", "Saved", "zpool create floatlab", "/dev/sda2", "Finish →", "Enter Finish"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Review is missing %q", want)
		}
	}
	for _, secret := range []string{"private-password", "PRIVATE-TOTP-SECRET", "private-recovery-code", "123456"} {
		if strings.Contains(view, secret) {
			t.Fatalf("Review exposed %q", secret)
		}
	}
}
