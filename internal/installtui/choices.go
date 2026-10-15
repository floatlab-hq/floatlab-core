package installtui

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
)

//go:embed timezones.txt
var timezoneData string

var timezones = strings.Fields(timezoneData)

// The editor groups the command into pool, options, and topology. The service
// validates and converts it into canonical argv before invoking zpool.
func formatPoolCommand(command string) string {
	prefix, devices, ok := strings.Cut(command, " floatlab ")
	if !ok {
		return command
	}
	options := strings.TrimSpace(strings.TrimPrefix(prefix, "zpool create"))
	words := strings.Fields(devices)
	for i, word := range words {
		if !strings.HasPrefix(word, "/") {
			words[i] = strings.ToUpper(word)
		}
	}
	return "zpool create floatlab\n" + options + "\n" + strings.Join(words, " ")
}

func (m *Model) timezoneMatches() []string {
	query := strings.ToLower(strings.TrimSpace(m.timezoneSearch.Value()))
	var matches []string
	for _, zone := range timezones {
		if strings.Contains(strings.ToLower(zone), query) {
			matches = append(matches, zone)
		}
	}
	return matches
}
func (m *Model) openTimezone() {
	m.stage = "timezone"
	m.scroll = 0
	m.manualScroll = false
	m.timezoneSearch = textinput.New()
	m.timezoneSearch.Prompt = "Search: "
	m.timezoneSearch.CharLimit = 128
	m.timezoneSearch.Focus()
	m.styleInputs()
	m.resize()
	m.timezoneCursor = 0
	for i, zone := range timezones {
		if zone == m.fields[1].Value() {
			m.timezoneCursor = i
			break
		}
	}
}
func (m *Model) timezoneBody() string {
	matches := m.timezoneMatches()
	rows := max(1, m.height-16)
	start := max(0, m.timezoneCursor-rows/2)
	start = min(start, max(0, len(matches)-rows))
	lines := []string{"Choose timezone", "Type to filter   ↑/↓: select   Enter: confirm   Esc: back", "", m.timezoneSearch.View(), ""}
	if len(matches) == 0 {
		lines = append(lines, "No matching timezones. Clear or change the search.")
	}
	for i := start; i < min(len(matches), start+rows); i++ {
		mark := "  "
		if i == m.timezoneCursor {
			mark = "> "
		}
		lines = append(lines, mark+matches[i])
	}
	lines = append(lines, "", fmt.Sprintf("%d matching timezones", len(matches)))
	return strings.Join(lines, "\n")
}
