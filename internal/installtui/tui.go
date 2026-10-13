package installtui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/floatlab/floatlab-core/internal/hostnetwork"
	"github.com/floatlab/floatlab-core/internal/installer"
)

type client struct {
	cmd     *exec.Cmd
	input   io.WriteCloser
	output  io.ReadCloser
	encoder *json.Encoder
	decoder *json.Decoder
	log     *os.File
}
type connected struct {
	client *client
	err    error
}
type received struct {
	event installer.Event
	err   error
}
type sent struct{ err error }
type tick time.Time

type Model struct {
	service        string
	args           []string
	client         *client
	stage          string
	width, height  int
	disks          []installer.Disk
	selected       map[int]bool
	cursor         int
	fields         []textinput.Model
	labels         []string
	focus          int
	command        textarea.Model
	confirmation   textinput.Model
	preview        installer.Event
	status         installer.Status
	enrollment     *installer.Enrollment
	ntp            bool
	pending        bool
	requestID      int
	failure        string
	validation     string
	configured     bool
	terminal       bool
	countdown      int
	paused         bool
	frame          int
	scroll         int
	rebooting      bool
	totpCode       string
	manualScroll   bool
	imageBytes     int64
	timezoneSearch textinput.Model
	timezoneCursor int
}

func New(service string, args []string) *Model {
	command := textarea.New()
	command.CharLimit = 8192
	command.Prompt = ""
	command.ShowLineNumbers = false
	command.MaxWidth = 0
	command.SetHeight(6)
	command.SetWidth(72)
	command.Focus()
	confirmation := textinput.New()
	confirmation.Prompt = ""
	confirmation.CharLimit = 64
	return &Model{service: service, args: args, stage: "welcome", width: 80, height: 24, selected: map[int]bool{}, command: command, confirmation: confirmation, status: installer.Status{Phase: "Waiting for disk layout"}}
}
func Run(ctx context.Context, service string, args []string) error {
	m := New(service, args)
	defer func() {
		if m.client != nil {
			m.client.input.Close()
			m.client.output.Close()
			m.client.cmd.Wait()
			m.client.log.Close()
		}
	}()
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	return err
}
func (m *Model) Init() tea.Cmd { return ticking() }
func ticking() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg { return tick(t) })
}
func connect(service string, args []string) tea.Cmd {
	return func() tea.Msg {
		log, err := os.CreateTemp("", "install-tui-*.log")
		if err != nil {
			return connected{err: err}
		}
		cmd := exec.Command(service, args...)
		cmd.Stderr = log
		in, err := cmd.StdinPipe()
		if err != nil {
			log.Close()
			return connected{err: err}
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			in.Close()
			log.Close()
			return connected{err: err}
		}
		if err = cmd.Start(); err != nil {
			in.Close()
			out.Close()
			log.Close()
			return connected{err: err}
		}
		return connected{client: &client{cmd: cmd, input: in, output: out, encoder: json.NewEncoder(in), decoder: json.NewDecoder(out), log: log}}
	}
}
func (m *Model) read() tea.Cmd {
	return func() tea.Msg {
		var event installer.Event
		err := m.client.decoder.Decode(&event)
		return received{event, err}
	}
}
func (m *Model) send(req installer.Request) tea.Cmd {
	m.requestID++
	req.ID = m.requestID
	m.pending = true
	m.validation = ""
	return func() tea.Msg { return sent{m.client.encoder.Encode(req)} }
}
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		if v.Width > 0 && v.Height > 0 {
			m.width, m.height = v.Width, v.Height
		}
		m.resize()
		return m, nil
	case connected:
		if v.err != nil {
			m.failure = v.err.Error()
			m.stage = "unavailable"
			return m, nil
		}
		m.client = v.client
		return m, m.read()
	case sent:
		if v.err != nil {
			m.failure = "Installer connection failed: " + v.err.Error()
			m.stage = "unavailable"
			m.pending = false
		}
		return m, nil
	case received:
		if v.err != nil {
			if m.rebooting {
				return m, tea.Quit
			}
			if m.stage != "unavailable" {
				m.failure = "Installer connection closed. Diagnostics: " + m.client.log.Name()
				m.stage = "unavailable"
			}
			return m, nil
		}
		e := v.event
		if e.Status != nil {
			m.status = *e.Status
		}
		if e.ID == m.requestID && e.ID != 0 {
			m.pending = false
		}
		switch e.Type {
		case "inventory":
			m.disks = e.Disks
			m.ntp = e.NTP
			m.imageBytes = installer.ImageSize(e.PayloadBytes)
			m.stage = "disks"
		case "preview":
			m.preview = e
			m.stage = "confirm"
			m.confirmation.SetValue("")
			m.confirmation.Focus()
			m.resize()
		case "confirm":
			m.stage = "settings"
			m.settings()
		case "validate-host":
			m.stage = "account"
			m.setFocus(7)
		case "enroll":
			m.enrollment = e.Enrollment
			m.stage = "totp"
			m.focus = 0
			m.confirmation.SetValue("")
			m.confirmation.Focus()
			m.scroll = 0
			m.manualScroll = true
		case "configure":
			m.configured = true
			m.stage = "waiting"
			for i := range m.fields {
				m.fields[i].SetValue("")
			}
			m.enrollment = nil
			m.totpCode = ""
			m.confirmation.SetValue("")
		case "failed":
			m.terminal = true
			m.stage = "failed"
			m.failure = m.status.Error
			m.paused = true
		case "complete":
			m.terminal = true
			m.stage = "complete"
			m.countdown = 30 * 4
			m.paused = false
		case "fatal":
			m.failure = e.Error
			m.stage = "unavailable"
		case "error":
			m.validation = e.Error
			if m.rebooting {
				m.rebooting = false
				m.paused = true
			}
		case "reboot":
			return m, tea.Quit
		}
		return m, tea.Batch(m.read(), textinput.Blink)
	case tick:
		m.frame++
		if m.stage == "complete" && !m.paused && !m.rebooting {
			m.countdown--
			if m.countdown <= 0 {
				return m, tea.Batch(ticking(), m.reboot())
			}
		}
		return m, ticking()
	case tea.KeyMsg:
		if v.Type == tea.KeyCtrlJ {
			v.Type = tea.KeyEnter
		}
		// Ctrl-C/Ctrl-D are deliberately not quit actions in the installer.
		if v.String() == "ctrl+c" || v.String() == "ctrl+d" {
			return m, nil
		}
		if m.stage == "timezone" && (v.String() == "pgup" || v.String() == "pgdown") {
			delta := max(1, m.height-15)
			if v.String() == "pgup" {
				delta = -delta
			}
			m.timezoneCursor = max(0, min(max(0, len(m.timezoneMatches())-1), m.timezoneCursor+delta))
			return m, nil
		}
		if v.String() == "pgup" {
			m.scrollBy(-max(1, m.height-8))
			return m, nil
		}
		if v.String() == "pgdown" {
			m.scrollBy(max(1, m.height-8))
			return m, nil
		}
		if v.String() == "alt+up" {
			m.scrollBy(-1)
			return m, nil
		}
		if v.String() == "alt+down" {
			m.scrollBy(1)
			return m, nil
		}
		if m.pending {
			return m, nil
		}
		switch m.stage {
		case "welcome":
			if v.String() == "enter" {
				m.stage = "connecting"
				return m, connect(m.service, m.args)
			}
		case "disks":
			switch v.String() {
			case "up", "k":
				m.cursor = max(0, m.cursor-1)
			case "down", "j":
				m.cursor = min(len(m.disks)-1, m.cursor+1)
			case " ":
				if m.cursor >= 0 && m.cursor < len(m.disks) && m.disks[m.cursor].Eligible {
					m.selected[m.cursor] = !m.selected[m.cursor]
				}
			case "enter":
				var selected []installer.Disk
				for i, d := range m.disks {
					if m.selected[i] {
						selected = append(selected, d)
					}
				}
				if len(selected) == 0 {
					m.validation = "Select at least one disk"
					return m, nil
				}
				m.command.SetValue(formatPoolCommand(installer.ZPoolCommand(selected)))
				m.command.Focus()
				m.stage = "command"
				m.resize()
				return m, textinput.Blink
			}
		case "command":
			if v.String() == "esc" {
				m.stage = "disks"
				m.validation = ""
				return m, nil
			}
			if v.String() == "ctrl+s" {
				return m, m.send(installer.Request{Type: "preview", Command: m.command.Value()})
			}
			var cmd tea.Cmd
			m.command, cmd = m.command.Update(v)
			return m, cmd
		case "confirm":
			if v.String() == "esc" {
				m.stage = "command"
				m.validation = ""
				return m, nil
			}
			if v.String() == "enter" {
				return m, m.send(installer.Request{Type: "confirm", Command: m.preview.Command, Confirmation: m.confirmation.Value()})
			}
			var cmd tea.Cmd
			m.confirmation, cmd = m.confirmation.Update(v)
			return m, cmd
		case "timezone":
			matches := m.timezoneMatches()
			switch v.String() {
			case "esc":
				m.stage = "settings"
				m.manualScroll = false
				return m, nil
			case "up":
				m.timezoneCursor = max(0, m.timezoneCursor-1)
				return m, nil
			case "down":
				m.timezoneCursor = min(max(0, len(matches)-1), m.timezoneCursor+1)
				return m, nil
			case "enter":
				if len(matches) > 0 {
					m.fields[1].SetValue(matches[m.timezoneCursor])
					m.stage = "settings"
					m.moveFocus(1)
				}
				return m, textinput.Blink
			}
			old := m.timezoneSearch.Value()
			var cmd tea.Cmd
			m.timezoneSearch, cmd = m.timezoneSearch.Update(v)
			if old != m.timezoneSearch.Value() {
				m.timezoneCursor = 0
			}
			return m, cmd
		case "settings", "account":
			if m.stage == "settings" && m.focus == 1 && (v.String() == "enter" || v.String() == " ") {
				m.openTimezone()
				return m, textinput.Blink
			}
			if m.stage == "settings" && m.focus == 2 {
				switch v.String() {
				case "left":
					m.fields[2].SetValue("dhcp")
					return m, nil
				case "right":
					m.fields[2].SetValue("static")
					return m, nil
				case " ":
					if m.fields[2].Value() == "dhcp" {
						m.fields[2].SetValue("static")
					} else {
						m.fields[2].SetValue("dhcp")
					}
					return m, nil
				}
			}
			switch v.String() {
			case "tab", "down":
				m.moveFocus(1)
				return m, textinput.Blink
			case "shift+tab", "up":
				m.moveFocus(-1)
				return m, textinput.Blink
			case "esc":
				if m.stage == "account" {
					m.stage = "settings"
					m.setFocus(6)
					m.validation = ""
				}
				return m, nil
			case "enter":
				visible := m.visibleFields()
				if m.focus == visible[len(visible)-1] {
					if m.stage == "settings" {
						config := m.config()
						return m, m.send(installer.Request{Type: "validate-host", Config: &config})
					}
					m.stage = "review"
					m.scroll = 0
					m.manualScroll = false
					return m, nil
				}
				m.moveFocus(1)
				return m, textinput.Blink
			}
			if m.stage == "settings" && (m.focus == 1 || m.focus == 2) {
				return m, nil
			}
			var cmd tea.Cmd
			m.fields[m.focus], cmd = m.fields[m.focus].Update(v)
			return m, cmd

		case "review":
			if v.String() == "esc" {
				m.stage = "account"
				return m, nil
			}
			if v.String() == "enter" {
				config := m.config()
				if config.TOTP {
					return m, m.send(installer.Request{Type: "enroll", Config: &config})
				}
				return m, m.send(installer.Request{Type: "configure", Config: &config})
			}
		case "totp":
			m.manualScroll = false
			m.scroll = 0
			if v.String() == "tab" || v.String() == "shift+tab" {
				if m.focus == 0 {
					m.totpCode = m.confirmation.Value()
					m.focus = 1
					m.confirmation.SetValue("")
				} else {
					m.focus = 0
					m.confirmation.SetValue(m.totpCode)
				}
				m.manualScroll = false
				m.scroll = 0
				return m, textinput.Blink
			}
			if v.String() == "esc" {
				m.stage = "account"
				m.setFocus(10)
				m.enrollment = nil
				m.scroll = 0
				m.manualScroll = false
				return m, nil
			}
			if v.String() == "enter" {
				if m.focus == 0 {
					m.totpCode = m.confirmation.Value()
					m.focus = 1
					m.confirmation.SetValue("")
					m.confirmation.Focus()
					return m, textinput.Blink
				}
				config := m.config()
				config.TOTPCode = m.totpCode
				config.RecoverySaved = m.confirmation.Value() == "SAVED"
				return m, m.send(installer.Request{Type: "configure", Config: &config})
			}
			var cmd tea.Cmd
			m.confirmation, cmd = m.confirmation.Update(v)
			return m, cmd
		case "complete":
			if v.String() == "p" || v.String() == " " {
				m.paused = true
				return m, nil
			}
			if v.String() == "r" || v.String() == "enter" {
				return m, m.reboot()
			}
		case "failed":
			if v.String() == "r" || v.String() == "enter" {
				return m, m.reboot()
			}
		}
	}
	var cmd tea.Cmd
	switch m.stage {
	case "command":
		m.command, cmd = m.command.Update(msg)
	case "confirm", "totp":
		m.confirmation, cmd = m.confirmation.Update(msg)
	case "timezone":
		m.timezoneSearch, cmd = m.timezoneSearch.Update(msg)
	case "settings", "account":
		if m.focus != 1 && m.focus != 2 {
			m.fields[m.focus], cmd = m.fields[m.focus].Update(msg)
		}
	}
	return m, cmd
}
func (m *Model) reboot() tea.Cmd {
	m.rebooting = true
	m.paused = true
	return m.send(installer.Request{Type: "reboot"})
}

func (m *Model) settings() {
	m.labels = []string{"Node name", "Timezone", "IPv4 configuration", "IPv4 address (static only)", "Netmask (static only)", "Gateway (static only)", "DNS servers (comma separated)", "Administrator username", "Password", "Confirm password", "Set up TOTP (yes/no)"}
	defaults := []string{"floatlab", "UTC", "dhcp", "", "", "", "1.1.1.1,8.8.8.8", "admin", "", "", "no"}
	m.fields = make([]textinput.Model, len(m.labels))
	for i := range m.fields {
		f := textinput.New()
		f.Prompt = ""
		f.CharLimit = 256
		f.SetValue(defaults[i])
		if i == 8 || i == 9 {
			f.EchoMode = textinput.EchoPassword
			f.EchoCharacter = '•'
		}
		m.fields[i] = f
	}
	m.focus = 0
	m.fields[0].Focus()
	m.resize()
}
func (m *Model) visibleFields() []int {
	if m.stage == "account" {
		return []int{7, 8, 9, 10}
	}
	fields := []int{0, 1, 2}
	if m.fields[2].Value() == "static" {
		fields = append(fields, 3, 4, 5)
	}
	return append(fields, 6)
}
func (m *Model) setFocus(index int) {
	for i := range m.fields {
		m.fields[i].Blur()
	}
	m.focus = index
	if index != 1 && index != 2 {
		m.fields[index].Focus()
	}
	m.manualScroll = false
	m.scroll = 0
}
func (m *Model) moveFocus(delta int) {
	fields := m.visibleFields()
	position := 0
	for i, index := range fields {
		if index == m.focus {
			position = i
			break
		}
	}
	m.setFocus(fields[(position+delta+len(fields))%len(fields)])
}
func (m *Model) focusedLine() int {
	for i, index := range m.visibleFields() {
		if index == m.focus {
			return 3 + i*3
		}
	}
	return 3
}

func (m *Model) resize() {
	w := max(1, m.width-6)
	m.command.SetWidth(max(1, m.width-8))
	m.command.SetHeight(max(3, min(6, m.height-16)))
	m.timezoneSearch.Width = w
	m.confirmation.Width = w
	for i := range m.fields {
		m.fields[i].Width = w
	}
}
func (m *Model) config() installer.InstallConfig {
	value := func(i int) string { return m.fields[i].Value() }
	network := hostnetwork.DefaultConfig()
	network.IPv4.Mode = value(2)
	if network.IPv4.Mode == "static" {
		network.IPv4.Address = value(3)
		network.IPv4.Netmask = value(4)
		network.IPv4.Gateway = value(5)
	}
	network.DNSServers = nil
	for _, v := range strings.Split(value(6), ",") {
		if v = strings.TrimSpace(v); v != "" {
			network.DNSServers = append(network.DNSServers, v)
		}
	}
	return installer.InstallConfig{NodeName: value(0), Timezone: value(1), Network: network, Username: value(7), Password: value(8), PasswordConfirmation: value(9), TOTP: value(10) == "yes"}
}
func size(bytes int64) string { return fmt.Sprintf("%.1f GiB", float64(bytes)/float64(installer.GiB)) }
func partitions(d installer.Disk) string {
	var parts []string
	for _, p := range d.Partitions {
		fs := p.Filesystem
		if fs == "" {
			fs = "unknown"
		}
		parts = append(parts, fmt.Sprintf("%.0fGB %s", float64(p.Size)/1e9, fs))
	}
	if len(parts) == 0 {
		return "No partitions"
	}
	return strings.Join(parts, " + ")
}
func (m *Model) body() string {
	var lines []string
	switch m.stage {
	case "welcome":
		return "Install FloatLab\n\nPress Enter to begin."
	case "connecting":
		return "Starting installation service…"
	case "disks":
		lines = append(lines, "Select installation disks", "Space: toggle   ↑/↓: move   Enter: edit pool command", "")
		for i, d := range m.disks {
			mark := "[ ]"
			if m.selected[i] {
				mark = "[x]"
			}
			if !d.Eligible {
				mark = "[–]"
			}
			prefix := "  "
			if i == m.cursor {
				prefix = "> "
			}
			lines = append(lines, prefix+mark+" "+d.Device+"  "+d.Manufacturer+"  "+d.Model+"  "+size(d.Size), "      "+partitions(d))
			if !d.Eligible {
				lines = append(lines, "      Unavailable: "+d.Reason)
			}
		}
	case "command":
		lines = []string{"Edit the complete pool command", "Enter: new line   Ctrl+S: validate / preview   Esc: back", "", lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#537279")).Padding(0, 1).Render(m.command.View()), "", "Whole-disk references will become ZFS partition references.", "Edit mirror to raidz, raidz2, or another supported topology."}
	case "confirm":
		lines = []string{"Confirm disk layout", "Esc: edit command", ""}
		for _, d := range m.preview.Disks {
			lines = append(lines, d.Device+" — "+d.Manufacturer+" "+d.Model, "  Existing: "+partitions(d), "  Proposed: 512 MiB UEFI + "+size(m.imageBytes)+" boot image + remaining ZFS")
		}
		lines = append(lines, "", "Final command:", formatPoolCommand(m.preview.Command), "", "Whole-disk dry-run:", m.preview.DryRun, "", "All data on these disks will be erased.", "Type ERASE ALL DATA and press Enter:", m.confirmation.View())
	case "timezone":
		return m.timezoneBody()
	case "settings", "account":
		title := "Configure FloatLab while installation runs"
		help := "Tab: next field   Enter: choose / validate network   Alt-↑/↓: scroll"
		if m.stage == "account" {
			title = "Set up administrator"
			help = "Tab: next field   Enter: next / review   Esc: network settings"
		}
		lines = []string{title, help, ""}
		for _, i := range m.visibleFields() {
			label := m.labels[i]
			if i == m.focus {
				label = "> " + label
			}
			value := m.fields[i].View()
			if i == 1 {
				value = m.fields[1].Value() + " ▾  (Enter: choose timezone)"
			}
			if i == 2 {
				value = "(●) DHCP    ( ) Static"
				if m.fields[2].Value() == "static" {
					value = "( ) DHCP    (●) Static"
				}
				if m.focus == 2 {
					value += "   ←/→ or Space to select"
				}
			}
			lines = append(lines, label, value, "")
		}
		if m.stage == "account" && !m.ntp {
			lines = append(lines, "TOTP requires synchronized time; password authentication is available.")
		}

	case "review":
		c := m.config()
		lines = []string{"Review configuration", "Node: " + c.NodeName, "Timezone: " + c.Timezone, "IPv4: " + c.Network.IPv4.Mode, "Administrator: " + c.Username, fmt.Sprintf("TOTP: %t", c.TOTP), "", "Enter: validate and submit   Esc: edit settings"}
		if c.Network.IPv4.Mode == "static" {
			lines = append(lines, "Address: "+c.Network.IPv4.Address, "Netmask: "+c.Network.IPv4.Netmask, "Gateway: "+c.Network.IPv4.Gateway)
		}
		lines = append(lines, "DNS: "+strings.Join(c.Network.DNSServers, ", "))
	case "totp":
		lines = []string{"Enroll your authenticator", "Esc: edit settings   Alt-↑/↓: scroll", ""}
		if m.enrollment != nil {
			lines = append(lines, m.enrollment.URI, "Manual secret: "+m.enrollment.Secret, "", "Record these single-use recovery codes:")
			lines = append(lines, m.enrollment.RecoveryCodes...)
		}
		if m.focus == 0 {
			lines = append(lines, "", "Enter the six-digit authenticator code:", m.confirmation.View())
		} else {
			lines = append(lines, "", "Type SAVED after recording the recovery codes:", m.confirmation.View())
		}
	case "waiting":
		return "Configuration accepted.\n\nInstallation and verification are still running."
	case "complete":
		lines = []string{"Installation complete", "Management: " + m.status.Address, "", "Remove installation media before rebooting.", "R / Enter: reboot now   P / Space: pause countdown"}
		if m.paused {
			lines = append(lines, "Automatic reboot paused.")
		} else {
			lines = append(lines, fmt.Sprintf("Rebooting in %d seconds…", (m.countdown+3)/4))
		}
	case "failed", "unavailable":
		lines = []string{"Installation failed", "Stage: " + m.status.Phase, "", m.failure}
		if m.terminal {
			lines = append(lines, "", "R / Enter: reboot when ready")
		}
	}
	if m.validation != "" {
		lines = append(lines, "", "Validation: "+m.validation)
	}
	if m.pending {
		lines = append(lines, "", "Waiting for installer…")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) View() string {
	if m.width < 20 || m.height < 12 {
		return "Enlarge the terminal to at least 20 columns × 12 rows.\nInstallation continues in the background."
	}
	width := m.width
	step := map[string]string{"welcome": "Welcome", "connecting": "Starting installer", "disks": "1 · Disk selection", "command": "1 · Pool command", "confirm": "1 · Confirm layout", "settings": "2 · Host and network settings", "timezone": "2 · Choose timezone", "account": "3 · Administrator account", "review": "3 · Review configuration", "totp": "3 · Authenticator enrollment", "waiting": "4 · Installing", "complete": "Complete", "failed": "Failed", "unavailable": "Service unavailable"}[m.stage]
	header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7CD4C5")).Width(width).Render(ansi.Truncate("  FloatLab  ·  "+step, width, "…"))
	bodyHeight := m.height - 8
	bodyWidth := width - 4
	// Wrap first so long commands, inventory rows, and errors remain readable.
	lines := strings.Split(ansi.Wrap(m.body(), bodyWidth, ""), "\n")
	offset := m.scroll
	if (m.stage == "settings" || m.stage == "account") && !m.manualScroll {
		offset = max(0, m.focusedLine()-bodyHeight+3)
	}
	if m.stage == "disks" && !m.manualScroll {
		offset = max(0, 3+m.cursor*2-bodyHeight+3)
	}
	if (m.stage == "confirm" || m.stage == "totp") && !m.manualScroll {
		offset = max(offset, len(lines)-bodyHeight)
	}
	offset = min(offset, max(0, len(lines)-bodyHeight))
	var visible []string
	for i := 0; i < bodyHeight; i++ {
		line := ""
		if offset+i < len(lines) {
			line = lines[offset+i]
		}
		visible = append(visible, "  "+line)
	}
	footer := m.footer(width)
	return header + "\n\n" + strings.Join(visible, "\n") + "\n" + footer
}
func (m *Model) footer(width int) string {
	phase := m.status.Phase
	if m.failure != "" {
		phase = "Failed: " + m.status.Phase
	}
	spinner := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}[m.frame%10]
	if m.terminal || m.stage == "welcome" {
		spinner = "●"
	}
	percent := max(0, min(1, m.status.Progress))
	barWidth := max(1, width-13)
	filled := int(percent * float64(barWidth))
	bar := "[" + strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled) + fmt.Sprintf("] %3.0f%%", percent*100)
	copied := "Installer is active"
	speed := ""
	if m.status.Total > 0 {
		copied = "Copied: " + size(m.status.Copied) + " / " + size(m.status.Total)
		speed = fmt.Sprintf("Speed: %.1f MiB/s", m.status.BytesPerSecond/float64(installer.MiB))
	}
	config := "Configuration: awaiting submission"
	if m.configured {
		config = "Configuration: accepted"
	}
	activity := m.status.Activity
	if m.failure != "" {
		activity = m.failure
	} else if m.validation != "" {
		activity = m.validation
	}
	rows := []string{spinner + "  " + phase, bar, copied, speed, config, activity}
	style := lipgloss.NewStyle().Background(lipgloss.Color("#182529")).Foreground(lipgloss.Color("#C9D8DC"))
	for i, row := range rows {
		row = ansi.Truncate(strings.ReplaceAll(row, "\n", " "), width-4, "…")
		rows[i] = style.Render("  " + row + strings.Repeat(" ", max(0, width-2-ansi.StringWidth(row))))
	}
	return strings.Join(rows, "\n")
}

func ServicePath(name string) string {
	exe, err := os.Executable()
	if err == nil {
		path := filepath.Join(filepath.Dir(exe), name)
		if _, err = os.Stat(path); err == nil {
			return path
		}
	}
	return name
}

func (m *Model) scrollBy(delta int) {
	height := max(1, m.height-8)
	lines := len(strings.Split(ansi.Wrap(m.body(), max(1, m.width-4), ""), "\n"))
	if !m.manualScroll {
		switch m.stage {
		case "settings", "account":
			m.scroll = max(0, m.focusedLine()-height+3)
		case "disks":
			m.scroll = max(0, 3+m.cursor*2-height+3)
		case "confirm", "totp":
			m.scroll = max(0, lines-height)
		}
	}
	m.manualScroll = true
	m.scroll = max(0, min(max(0, lines-height), m.scroll+delta))
}
