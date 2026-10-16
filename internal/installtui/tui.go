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
	"github.com/nbutton23/zxcvbn-go"
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
	passwordScore  int
	labels         []string
	focus          int
	actionFocus    int // 0: content, 1: Back, 2: Next, 3: Customise zpool.
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
	terminal       bool
	countdown      int
	paused         bool
	frame          int
	scroll         int
	rebooting      bool
	totpCode       string
	manualScroll   bool
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
	m := &Model{service: service, args: args, stage: "connecting", width: 80, height: 24, selected: map[int]bool{}, command: command, confirmation: confirmation, status: installer.Status{Phase: "Waiting for disk layout"}}
	m.styleInputs()
	return m
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
func (m *Model) Init() tea.Cmd { return tea.Batch(ticking(), connect(m.service, m.args)) }
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
		if m.rebooting {
			return m, tea.Batch(m.read(), m.reboot())
		}
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
		if m.rebooting && e.Type != "reboot" && !(e.Type == "error" && e.ID == m.requestID) {
			return m, m.read()
		}
		previousStage := m.stage
		switch e.Type {
		case "inventory":
			m.disks = e.Disks
			m.ntp = e.NTP
			m.stage = "disks"
		case "preview":
			m.preview = e
			m.stage = "confirm"
			m.resize()
		case "confirm":
			m.stage = "settings"
			m.settings()
		case "validate-host":
			m.stage = "account"
			m.setFocus(7)
		case "validate-account":
			m.enrollment = nil
			m.totpCode = ""
			m.confirmation.SetValue("")
			config := m.config()
			if config.TOTP {
				return m, tea.Batch(m.send(installer.Request{Type: "enroll", Config: &config}), m.read())
			}
			m.stage = "review"
			m.scroll, m.manualScroll = 0, false
		case "verify-totp":
			m.focus = 1
			m.confirmation.SetValue("")
			m.confirmation.Focus()
			m.validation = ""
			m.scroll, m.manualScroll = 0, false
		case "enroll":
			m.totpCode = ""
			m.enrollment = e.Enrollment
			m.stage = "totp"
			m.focus = 0
			m.confirmation.SetValue("")
			m.confirmation.Focus()
			m.scroll = 0
			m.manualScroll = true
		case "configure":
			m.stage = "waiting"
			for i := range m.fields {
				m.fields[i].SetValue("")
			}
			m.enrollment = nil
			m.totpCode = ""
			m.passwordScore = 0
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
		if m.stage != previousStage {
			m.actionFocus = 0
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
		if v.String() == "ctrl+r" {
			if m.rebooting {
				return m, nil
			}
			if m.client == nil {
				m.rebooting = true
				return m, nil
			}
			return m, m.reboot()
		}
		if m.stage == "timezone" && (v.String() == "pgup" || v.String() == "pgdown") {
			delta := max(1, m.height-16)
			if v.String() == "pgup" {
				delta = -delta
			}
			m.timezoneCursor = max(0, min(max(0, len(m.timezoneMatches())-1), m.timezoneCursor+delta))
			return m, nil
		}
		if v.String() == "pgup" {
			m.scrollBy(-max(1, m.height-9))
			return m, nil
		}
		if v.String() == "pgdown" {
			m.scrollBy(max(1, m.height-9))
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
		if handled, cmd := m.wizardKey(v); handled {
			return m, cmd
		}
		switch m.stage {
		case "disks":
			switch v.String() {
			case "up", "k":
				m.cursor = max(0, m.cursor-1)
			case "down", "j":
				m.cursor = min(len(m.disks)-1, m.cursor+1)
			case " ":
				if m.cursor >= 0 && m.cursor < len(m.disks) && m.disks[m.cursor].Eligible {
					m.selected[m.cursor] = !m.selected[m.cursor]
					m.command.SetValue("")
				}
			case "enter":
				if !m.preparePoolCommand() {
					return m, nil
				}
				return m, m.send(installer.Request{Type: "preview", Command: m.command.Value()})
			}
		case "command":
			if v.String() == "ctrl+s" {
				return m, m.send(installer.Request{Type: "preview", Command: m.command.Value()})
			}
			var cmd tea.Cmd
			m.command, cmd = m.command.Update(v)
			return m, cmd
		case "confirm":
			if v.String() == "enter" {
				return m, m.send(installer.Request{Type: "confirm", Command: m.preview.Command, Confirmation: "ERASE ALL DATA"})
			}
			return m, nil
		case "timezone":
			matches := m.timezoneMatches()
			switch v.String() {
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
			if m.focus == len(m.fields) && v.String() == " " {
				v = tea.KeyMsg{Type: tea.KeyEnter}
			}
			if m.stage == "settings" && m.focus == 1 && (v.String() == "enter" || v.String() == " ") {
				m.openTimezone()
				return m, textinput.Blink
			}
			if m.stage == "settings" && m.focus == 2 || m.stage == "account" && m.focus == 10 {
				left, right := "dhcp", "static"
				if m.focus == 10 {
					left, right = "yes", "no"
				}
				switch v.String() {
				case "left":
					m.validation = ""
					m.fields[m.focus].SetValue(left)
					return m, nil
				case "right":
					m.validation = ""
					m.fields[m.focus].SetValue(right)
					return m, nil
				case " ":
					m.validation = ""
					if m.fields[m.focus].Value() == left {
						m.fields[m.focus].SetValue(right)
					} else {
						m.fields[m.focus].SetValue(left)
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
			case "enter":
				visible := m.visibleFields()
				if m.focus == len(m.fields) || m.focus == visible[len(visible)-1] {
					if m.stage == "settings" {
						config := m.config()
						return m, m.send(installer.Request{Type: "validate-host", Config: &config})
					}
					config := m.config()
					return m, m.send(installer.Request{Type: "validate-account", Config: &config})
				}
				m.moveFocus(1)
				return m, textinput.Blink
			}
			if m.focus == len(m.fields) || m.focus == 10 || (m.stage == "settings" && (m.focus == 1 || m.focus == 2)) {
				return m, nil
			}
			return m, m.updateField(v)

		case "review":
			if v.String() == "enter" {
				config := m.config()
				return m, m.send(installer.Request{Type: "configure", Config: &config})
			}
		case "totp":
			m.manualScroll = false
			m.scroll = 0
			if v.String() == "enter" {
				if m.focus == 0 {
					m.totpCode = m.confirmation.Value()
					config := m.config()
					return m, m.send(installer.Request{Type: "verify-totp", Config: &config})
				}
				if m.confirmation.Value() != "SAVED" {
					m.validation = "Type SAVED after recording your recovery codes."
					return m, nil
				}
				m.stage = "review"
				m.validation = ""
				m.scroll, m.manualScroll = 0, false
				return m, nil
			}
			old := m.confirmation.Value()
			var cmd tea.Cmd
			m.confirmation, cmd = m.confirmation.Update(v)
			if old != m.confirmation.Value() {
				m.validation = ""
			}
			return m, cmd
		case "complete":
			if v.String() == "enter" {
				return m, m.reboot()
			}
		case "failed":
			if v.String() == "enter" {
				return m, m.reboot()
			}
		}
	}
	var cmd tea.Cmd
	switch m.stage {
	case "command":
		m.command, cmd = m.command.Update(msg)
	case "totp":
		m.confirmation, cmd = m.confirmation.Update(msg)
	case "timezone":
		m.timezoneSearch, cmd = m.timezoneSearch.Update(msg)
	case "settings", "account":
		if m.focus < len(m.fields) && m.focus != 1 && m.focus != 2 && m.focus != 10 {
			cmd = m.updateField(msg)
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
	m.labels = []string{"Node name", "Timezone", "IPv4 configuration", "IPv4 address (static only)", "Netmask (static only)", "Gateway (static only)", "DNS servers (comma separated)", "Administrator username", "Password", "Confirm password", "Set up TOTP"}
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
	m.styleInputs()
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
	m.actionFocus = 0
	if index == len(m.fields) {
		m.focusAction(2)
		return
	}
	if index < len(m.fields) && index != 1 && index != 2 && index != 10 {
		m.fields[index].Focus()
	}
	m.manualScroll = false
	m.scroll = 0
}
func (m *Model) moveFocus(delta int) {
	if m.focus == m.visibleFields()[0] && delta < 0 && m.canBack() {
		m.focusAction(1)
		return
	}
	fields := append(m.visibleFields(), len(m.fields))
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
	for i, line := range strings.Split(ansi.Wrap(m.body(), m.contentWidth(), ""), "\n") {
		if strings.HasPrefix(ansi.Strip(line), "> ") {
			return i
		}
	}
	return 3
}

func (m *Model) updateField(msg tea.Msg) tea.Cmd {
	old := m.fields[m.focus].Value()
	var cmd tea.Cmd
	m.fields[m.focus], cmd = m.fields[m.focus].Update(msg)
	if old != m.fields[m.focus].Value() {
		m.validation = ""
	}
	if old != m.fields[m.focus].Value() && (m.focus == 0 || m.focus == 7 || m.focus == 8) {
		m.passwordScore = 0
		if m.fields[8].Value() != "" {
			m.passwordScore = zxcvbn.PasswordStrength(m.fields[8].Value(), []string{m.fields[0].Value(), m.fields[7].Value(), "floatlab"}).Score
		}
	}
	return cmd
}

func (m *Model) passwordStrength() string {
	if m.fields[8].Value() == "" {
		return ""
	}
	labels := []string{"Very weak", "Weak", "Fair", "Strong", "Very strong"}
	if m.inputColumnWidth() < 40 {
		return labels[m.passwordScore]
	}
	return fmt.Sprintf("[%s%s] %s", strings.Repeat("█", m.passwordScore+1), strings.Repeat("·", 4-m.passwordScore), labels[m.passwordScore])
}

func (m *Model) inputColumnWidth() int {
	if m.tableLayout() {
		return max(1, m.contentWidth()-m.labelWidth()-2)
	}
	return m.contentWidth()
}

func (m *Model) resize() {
	w := m.contentWidth()
	m.command.SetWidth(max(1, w-4))
	m.command.SetHeight(max(3, min(6, m.bodyHeight()-7)))
	m.timezoneSearch.Width = max(1, w-ansi.StringWidth(m.timezoneSearch.Prompt))
	m.confirmation.Width = w
	for i := range m.fields {
		fieldWidth := m.inputColumnWidth()
		switch i {
		case 8:
			reserve := 19
			if fieldWidth < 40 {
				reserve = min(11, max(1, fieldWidth-4))
			}
			fieldWidth -= reserve + 3
		case 9:
			fieldWidth -= 4
		}
		m.fields[i].Width = max(1, fieldWidth)
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
	return installer.InstallConfig{NodeName: value(0), Timezone: value(1), Network: network, Username: value(7), Password: value(8), PasswordConfirmation: value(9), TOTP: value(10) == "yes", TOTPCode: m.totpCode, RecoverySaved: m.enrollment != nil && m.confirmation.Value() == "SAVED"}
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

func diskSummary(d installer.Disk) string {
	return d.Device + " - " + strings.TrimSpace(d.Manufacturer+" "+d.Model) + " - " + partitions(d)
}
func (m *Model) body() string {
	var lines []string
	switch m.stage {
	case "connecting":
		return "Starting installation service…"
	case "disks":
		lines = append(lines, "Select installation disks", "Choose one disk, or several disks for a mirror.", "")
		for i, d := range m.disks {
			if !d.Eligible && i > 0 {
				lines = append(lines, "")
			}
			mark := "[ ]"
			if m.selected[i] {
				mark = "[x]"
			}
			if !d.Eligible {
				mark = "[–]"
			}
			prefix := "  "
			if i == m.cursor && m.actionFocus == 0 {
				prefix = "> "
			}
			row := prefix + mark + " " + diskSummary(d)
			if d.Eligible && i == m.cursor && m.actionFocus == 0 {
				row = lipgloss.NewStyle().Foreground(m.currentTheme().accent).Background(m.currentTheme().background).Render(row)
			}
			muted := lipgloss.NewStyle().Foreground(m.currentTheme().darkForeground).Background(m.currentTheme().background)
			if !d.Eligible {
				row = muted.Render(row)
			}
			lines = append(lines, row)
			if !d.Eligible {
				lines = append(lines, muted.Render("      Unavailable: "+d.Reason))
			}
		}
	case "command":
		lines = []string{"Edit the complete pool command", "Choose Next to validate and preview the layout.", "", lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(m.currentTheme().accent).BorderBackground(m.currentTheme().background).Background(m.currentTheme().background).Padding(0, 1).Render(m.command.View()), "", "Whole-disk references will become ZFS partition references.", "Edit mirror to raidz, raidz2, or another supported topology."}
	case "confirm":
		lines = []string{"Confirm disk layout", "All data on the selected disks will be erased. This cannot be undone.", ""}
		for _, d := range m.preview.Disks {
			lines = append(lines, diskSummary(d))
		}
		command := ansi.Wrap(formatPoolCommand(m.preview.Command), max(1, m.contentWidth()-4), "")
		lines = append(lines, "", "Final zpool command:", command, "", m.button("Customise zpool", 3, !m.pending))
	case "timezone":
		return m.timezoneBody()
	case "settings", "account":
		title := "Configure FloatLab while installation runs"
		help := "Storage installation is running. Network settings remain editable."
		if m.stage == "account" {
			title = "Set up administrator"
			help = "Create the account used to manage this appliance."
		}
		lines = []string{title, help, ""}
		if m.tableLayout() {
			lines = append(lines, m.tableHeader("FIELD", "VALUE")...)
		}
		for _, i := range m.visibleFields() {
			label := "  " + m.labels[i]
			if i == m.focus && m.actionFocus == 0 {
				label = lipgloss.NewStyle().Foreground(m.currentTheme().accent).Background(m.currentTheme().background).Render("> " + m.labels[i])
			}
			value := m.fields[i].View()
			indicator := ""
			if i == 8 {
				indicator = m.passwordStrength()
			} else if i == 9 && m.fields[9].Value() != "" {
				indicator = "✗"
				if m.fields[8].Value() == m.fields[9].Value() {
					indicator = "✓"
				}
			}
			if indicator != "" {
				width := m.inputColumnWidth()
				indicator = ansi.Truncate(indicator, max(1, width-4), "…")
				value = ansi.Truncate(value, max(1, width-ansi.StringWidth(indicator)-2), "…")
				value += strings.Repeat(" ", max(1, width-ansi.StringWidth(value)-ansi.StringWidth(indicator))) + indicator
			}
			if i == 1 {
				value = m.fields[1].Value() + " ▾"
			}
			if i == 2 {
				value = "(●) DHCP    ( ) Static"
				if m.fields[2].Value() == "static" {
					value = "( ) DHCP    (●) Static"
				}
			}
			if i == 10 {
				value = "( ) Yes    (●) No"
				if m.fields[10].Value() == "yes" {
					value = "(●) Yes    ( ) No"
				}
			}
			if m.tableLayout() {
				lines = append(lines, m.tableRow(label, value))
			} else {
				lines = append(lines, label, value)
			}
			if !m.tableLayout() {
				lines = append(lines, "")
			}
		}
		if m.stage == "account" && !m.ntp {
			lines = append(lines, "TOTP requires synchronized time; password authentication is available.")
		}

	case "review":
		c := m.config()
		lines = []string{"Review configuration", "Confirm the settings for your FloatLab appliance.", ""}
		var rows [][2]string
		for _, disk := range m.preview.Disks {
			rows = append(rows, [2]string{"Installation disk", diskSummary(disk)})
		}
		rows = append(rows, [2]string{"Node", c.NodeName}, [2]string{"Timezone", c.Timezone}, [2]string{"IPv4", strings.ToUpper(c.Network.IPv4.Mode)})
		if c.Network.IPv4.Mode == "static" {
			rows = append(rows, [2]string{"Address", c.Network.IPv4.Address}, [2]string{"Netmask", c.Network.IPv4.Netmask}, [2]string{"Gateway", c.Network.IPv4.Gateway})
		}
		password := "Not set"
		if c.Password != "" && c.Password == c.PasswordConfirmation {
			password = "Set and confirmed"
		}
		totp := "Disabled"
		if c.TOTP {
			totp = "Enabled and verified"
		}
		rows = append(rows, [2]string{"DNS", strings.Join(c.Network.DNSServers, ", ")},
			[2]string{"Administrator", c.Username}, [2]string{"Password", password}, [2]string{"TOTP", totp})
		if c.TOTP {
			recovery := "Not saved"
			if c.RecoverySaved {
				recovery = "Saved"
			}
			rows = append(rows, [2]string{"Recovery codes", recovery})
		}
		if m.tableLayout() {
			lines = append(lines, m.tableHeader("FIELD", "VALUE")...)
		}
		for _, row := range rows {
			if m.tableLayout() {
				lines = append(lines, m.tableRow(row[0], row[1]))
			} else {
				lines = append(lines, row[0]+": "+row[1])
			}
		}
		if m.preview.Command != "" {
			lines = append(lines, "", "Final zpool command:", formatPoolCommand(m.preview.Command))
		}
		lines = append(lines, "", "Choose Finish to apply this configuration.")
	case "totp":
		if m.focus == 0 {
			lines = []string{"Enroll your authenticator"}
			if m.enrollment != nil {
				lines = append(lines, "Manual secret: "+m.enrollment.Secret, "", m.enrollmentQR())
			}
			lines = append(lines, "", "Enter the six-digit authenticator code:", m.confirmation.View())
		} else {
			lines = []string{"Save your recovery codes", "Keep these single-use codes somewhere safe before continuing.", ""}
			if m.enrollment != nil {
				lines = append(lines, m.enrollment.RecoveryCodes...)
			}
			lines = append(lines, "", "Type SAVED after recording the recovery codes:", m.confirmation.View())
		}
	case "waiting":
		return "Configuration accepted.\n\nInstallation and verification are still running."
	case "complete":
		lines = []string{"Installation complete", "Management: " + m.status.Address, "", "Remove installation media before rebooting."}
		if m.paused {
			lines = append(lines, "Automatic reboot paused.")
		} else {
			lines = append(lines, fmt.Sprintf("Rebooting in %d seconds…", (m.countdown+3)/4))
		}
	case "failed", "unavailable":
		lines = []string{"Installation failed", "Stage: " + m.status.Phase, "", m.failure}
	}
	if m.validation != "" {
		t := m.currentTheme()
		box := lipgloss.NewStyle().
			Foreground(t.red).Background(t.background).
			Border(lipgloss.NormalBorder()).BorderForeground(t.red).BorderBackground(t.background).
			Padding(0, 1).Width(m.contentWidth() - 2).
			Render(m.validation)
		lines = append(lines, "", box)
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
	t := m.currentTheme()
	bodyHeight := m.bodyHeight()
	bodyWidth := m.contentWidth()
	// Wrap first so long commands, inventory rows, and errors remain readable.
	body := m.body()
	lines := strings.Split(ansi.Wrap(body, bodyWidth, ""), "\n")
	if len(lines) > 0 {
		lines[0] = lipgloss.NewStyle().Foreground(t.foreground).Background(t.background).Bold(true).Render(lines[0])
	}
	offset := m.scroll
	if (m.stage == "settings" || m.stage == "account") && !m.manualScroll {
		offset = max(0, m.focusedLine()-bodyHeight+3)
		if m.actionFocus != 0 {
			offset = max(0, len(lines)-bodyHeight)
		}
	}
	if m.stage == "disks" && !m.manualScroll {
		offset = max(0, m.diskLine()-bodyHeight+3)
	}
	if m.stage == "confirm" && m.actionFocus == 3 {
		offset = max(0, len(lines)-bodyHeight)
	}
	if m.stage == "totp" && !m.manualScroll {
		offset = max(offset, len(lines)-bodyHeight)
	}
	if m.validation != "" && !m.manualScroll {
		offset = max(0, len(lines)-bodyHeight)
	}
	offset = min(offset, max(0, len(lines)-bodyHeight))
	var visible []string
	for i := offset; i < min(len(lines), offset+bodyHeight); i++ {
		visible = append(visible, m.canvasLine(lines[i]))
	}
	for range m.actionSpacing() {
		visible = append(visible, m.canvasLine(""))
	}
	visible = append(visible, m.canvasLine(m.actionBar()))
	for len(visible) < bodyHeight+m.actionSpacing()+1 {
		visible = append(visible, m.canvasLine(""))
	}
	view := m.header() + "\n" + strings.Join(visible, "\n") + "\n" + m.footer(m.width)
	return view
}
func (m *Model) footer(width int) string {
	phase := m.status.Phase
	if m.failure != "" {
		phase = "Failed: " + m.status.Phase
	}
	spinner := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}[m.frame%10]
	if m.terminal {
		spinner = "●"
	}
	percent := max(0, min(1, m.status.Progress))
	barWidth := max(1, m.contentWidth()-6)
	filled := int(percent * float64(barWidth))
	t := m.currentTheme()
	bar := lipgloss.NewStyle().Foreground(t.accent).Background(t.background).Render(strings.Repeat("█", filled)) + lipgloss.NewStyle().Foreground(t.selection).Background(t.background).Render(strings.Repeat("░", barWidth-filled)) + fmt.Sprintf(" %3.0f%%", percent*100)
	metrics := ""
	if m.status.Total > 0 {
		metrics = fmt.Sprintf("  %s / %s · %.0f MiB/s", size(m.status.Copied), size(m.status.Total), m.status.BytesPerSecond/float64(installer.MiB))
	}
	activity := m.status.Activity
	if m.failure != "" {
		activity = m.failure
	}
	if activity != "" {
		phase = activity
	}
	status := spinner + "  " + phase + metrics
	if m.status.Total == 0 && m.failure == "" {
		status, bar = "", ""
	}
	divider := lipgloss.NewStyle().Foreground(t.foreground).Background(t.background).Faint(true).Render(strings.Repeat("─", m.contentWidth()))
	rows := []string{status, bar, "", divider, m.helpBar(m.contentWidth() + 4), ""}
	for i, row := range rows {
		row = strings.ReplaceAll(row, "\n", " ")
		rows[i] = m.canvasLine(row)
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
	height := max(1, m.bodyHeight())
	lines := len(strings.Split(ansi.Wrap(m.body(), m.contentWidth(), ""), "\n"))
	if !m.manualScroll {
		switch m.stage {
		case "settings", "account":
			m.scroll = max(0, m.focusedLine()-height+3)
			if m.focus == len(m.fields) {
				m.scroll = max(0, lines-height)
			}
		case "disks":
			m.scroll = max(0, m.diskLine()-height+3)
		case "confirm", "totp":
			m.scroll = max(0, lines-height)
		}
	}
	m.manualScroll = true
	m.scroll = max(0, min(max(0, lines-height), m.scroll+delta))
}
