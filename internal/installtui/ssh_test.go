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

func TestSSHSourceSelectorAndSubmission(t *testing.T) {
	m := New("unused", nil)
	m.settings()
	m.stage = "account"
	m.setFocus(13)
	if strings.Contains(ansi.Strip(m.body()), "SSH key username") {
		t.Fatal("None shows reference")
	}
	for _, source := range []string{"github", "gitlab", "launchpad", "web", "none"} {
		m.Update(tea.KeyMsg{Type: tea.KeyRight})
		if m.fields[13].Value() != source {
			t.Fatalf("want %s", source)
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if m.fields[13].Value() != "web" || !strings.Contains(ansi.Strip(m.body()), "SSH key URL") {
		t.Fatal("Web did not show URL")
	}
	m.fields[14].SetValue("http://localhost:8080/keys")
	m.stage = "review"
	if !strings.Contains(ansi.Strip(m.body()), "Root SSH access") || !strings.Contains(ansi.Strip(m.body()), "http://localhost:8080/keys") {
		t.Fatal("missing SSH review")
	}
	m.back()
	if m.fields[14].Value() != "http://localhost:8080/keys" {
		t.Fatal("Back discarded URL")
	}
	m.stage = "review"
	var output bytes.Buffer
	m.client = &client{encoder: json.NewEncoder(&output)}
	m.focusAction(2)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("no submission")
	}
	cmd()
	var req installer.Request
	if err := json.Unmarshal(output.Bytes(), &req); err != nil {
		t.Fatal(err)
	}
	if req.Config.SSH.Source != "web" || req.Config.SSH.Reference != "http://localhost:8080/keys" {
		t.Fatal("lost SSH config")
	}
	m.pending = false
	m.Update(received{event: installer.Event{ID: m.requestID, Type: "error", Error: "could not fetch SSH keys"}})
	if m.stage != "review" || !m.canBack() || !strings.Contains(m.body(), "could not fetch SSH keys") {
		t.Fatal("failed import is not editable")
	}
}

func TestSSHSourceNarrowLayout(t *testing.T) {
	m := New("unused", nil)
	m.settings()
	m.stage = "account"
	m.fields[13].SetValue("web")
	m.fields[14].SetValue(strings.Repeat("a", 200))
	for _, width := range []int{40, 60, 80} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m.setFocus(14)
		for _, line := range strings.Split(m.View(), "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("width %d overflow: %q", width, line)
			}
		}
	}
}
