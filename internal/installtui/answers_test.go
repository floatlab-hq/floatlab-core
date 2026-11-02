package installtui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/floatlab/floatlab-core/internal/installer"
	"golang.org/x/crypto/bcrypt"
)

func TestReviewExportsHashedTOMLWithoutSubmittingConfiguration(t *testing.T) {
	m := New("unused", nil)
	m.settings()
	m.stage = "review"
	m.preview.Command = installer.ZPoolCommand([]installer.Disk{{Path: "/dev/disk/by-id/test"}})
	m.fields[1].SetValue("UTC")
	for _, field := range []int{8, 9} {
		m.fields[field].SetValue("admin-secret")
	}
	for _, field := range []int{11, 12} {
		m.fields[field].SetValue("recovery-secret")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	if m.stage != "export-answers" || !strings.Contains(m.body(), "bcrypt") {
		t.Fatal("Review did not offer the private TOML export")
	}
	path := filepath.Join(t.TempDir(), "answers.toml")
	m.answersPath.SetValue(path)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.stage != "review" || m.pending || !strings.Contains(m.body(), path) {
		t.Fatal("export submitted installation settings or did not show its destination")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	answers, err := installer.ReadAnswers(file)
	if err != nil || answers.Command != m.preview.Command || bcrypt.CompareHashAndPassword([]byte(answers.Config.PasswordHash), []byte("admin-secret")) != nil {
		t.Fatal("UI export cannot be used by the unattended installer", err)
	}
	if m.fields[8].Value() != "admin-secret" || m.fields[11].Value() != "recovery-secret" {
		t.Fatal("export modified the interactive configuration")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m.fields[10].SetValue("yes")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.stage != "export-answers" || !strings.Contains(m.body(), "authenticator") {
		t.Fatal("export silently removed TOTP or hid the validation error")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.stage != "review" {
		t.Fatal("cancel did not return to Review")
	}
}
