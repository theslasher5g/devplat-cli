// Package tui is devplat connect's interactive interface: a bordered terminal
// app (bubbletea) styled to match devplat-frontend's design system. Beyond the
// command input + output pane, it shows a live view of the remote environment
// — running containers and their locally-mirrored ports, the env's resource
// caps and TTL countdown, platform status, and parallel-usage — plus a
// command history/saved-commands picker and a docker-compose bind-mount
// warning. Everything the user types runs as a shell command with DOCKER_HOST
// set, exactly as in a normal terminal; only the chrome is custom.
package tui

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/theslasher5g/devplat-cli/internal/apiclient"
	"github.com/theslasher5g/devplat-cli/internal/commands"
	"github.com/theslasher5g/devplat-cli/internal/compose"
	"github.com/theslasher5g/devplat-cli/internal/dockerapi"
)

// Colors lifted from devplat-frontend/src/index.css's dark-mode tokens.
var (
	red   = lipgloss.Color("#E63312")
	green = lipgloss.Color("#23A26D")
	amber = lipgloss.Color("#D99000")
	text  = lipgloss.Color("#EDEDE8")
	muted = lipgloss.Color("#8A8A82")
	hair  = lipgloss.Color("#262624")

	dimStyle    = lipgloss.NewStyle().Foreground(muted)
	textStyle   = lipgloss.NewStyle().Foreground(text)
	redBold     = lipgloss.NewStyle().Foreground(red).Bold(true)
	greenStyle  = lipgloss.NewStyle().Foreground(green)
	amberStyle  = lipgloss.NewStyle().Foreground(amber)
	promptGlyph = lipgloss.NewStyle().Foreground(red).Bold(true)
	selStyle    = lipgloss.NewStyle().Foreground(text).Background(lipgloss.Color("#3A1D1A"))
	eyebrowDot  = lipgloss.NewStyle().Foreground(red).Render("●")
)

func tracked(s string) string {
	var b strings.Builder
	for i, r := range strings.ToUpper(s) {
		if i > 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

var ansiEscape = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[a-zA-Z]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)

func stripANSI(s string) string { return ansiEscape.ReplaceAllString(s, "") }

// Session carries everything the TUI needs from outside — the established
// tunnel, the control-plane client, and local helpers. Nothing here talks to
// the network directly beyond the passed-in client/docker address.
type Session struct {
	Version    string
	RequestID  string
	DockerHost string // tcp://127.0.0.1:<port> for child commands
	DockerAddr string // 127.0.0.1:<port> for the docker API client
	ShellPath  string
	ProjectDir string
	Client     *apiclient.Client
	Commands   *commands.Store
	BindMounts []compose.BindMount
}

// --- messages ---

type cmdDoneMsg struct{ output string }
type containersMsg struct {
	list []dockerapi.Container
	rtt  time.Duration
}
type envMsg struct{ env *apiclient.Environment }
type statusMsg struct{ status, label string }
type logsMsg struct {
	id, body string
}
type tickMsg time.Time

// --- model ---

type focus int

const (
	focusInput focus = iota
	focusSidebar
)

type overlay int

const (
	overlayNone overlay = iota
	overlayPicker
	overlayLogs
)

type model struct {
	sess  Session
	vp    viewport.Model
	input textinput.Model
	lines []string

	containers []dockerapi.Container
	selected   int
	env        *apiclient.Environment
	rtt        time.Duration
	platform   string // status level
	platLabel  string

	focus   focus
	overlay overlay

	// picker (saved + history) state
	pickerItems []string
	pickerIdx   int
	pickerSaved map[string]bool

	// command-history navigation in the input (↑/↓). histIdx == -1 means
	// "editing the live input"; 0.. indexes into History (most-recent first).
	histIdx   int
	histDraft string
	// tab-completion cycle: acMatches is the candidate set built on the first
	// Tab from the then-current prefix; repeated Tab advances acIdx through it.
	acMatches []string
	acIdx     int

	// logs overlay state. logsID/logsFollow drive tail-follow: while the logs
	// overlay is open the tick loop re-fetches this container's logs so they
	// stream in like `docker logs -f`.
	logsBody   string
	logsID     string
	logsFollow bool

	running  bool
	quitting bool
	ticks    int
	width    int
	height   int
}

func Run(sess Session) error {
	ti := textinput.New()
	ti.Placeholder = "docker version, mvn verify, …"
	ti.Focus()
	ti.CharLimit = 4096
	ti.Prompt = ""
	ti.TextStyle = textStyle
	ti.Cursor.Style = greenStyle

	m := model{
		sess:    sess,
		vp:      viewport.New(80, 20),
		input:   ti,
		lines:   welcomeLines(sess),
		histIdx: -1,
	}
	m.syncViewport()
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func welcomeLines(sess Session) []string {
	out := []string{
		dimStyle.Render("Connected. DOCKER_HOST is set for every command you run here."),
		dimStyle.Render("↑ history · Tab complete · ⇧Tab panes · ^l logs · ^y copy port · ^r commands · ^s save · exit to quit."),
		"",
	}
	// #8: one-time docker-compose bind-mount warning.
	if len(sess.BindMounts) > 0 {
		out = append(out, amberStyle.Render("⚠ docker-compose bind mounts won't work — containers run remotely, so host paths point at the VM, not your machine:"))
		for _, b := range sess.BindMounts {
			out = append(out, dimStyle.Render("    "+b.String()))
		}
		out = append(out, "")
	}
	return out
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, pollContainers(m.sess), pollEnv(m.sess), pollStatus(m.sess), tick())
}

func (m *model) syncViewport() {
	m.vp.SetContent(strings.Join(m.lines, "\n"))
	m.vp.GotoBottom()
}

// --- tea.Cmd producers ---

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func pollContainers(s Session) tea.Cmd {
	return func() tea.Msg {
		client := dockerapi.New(s.DockerAddr)
		start := time.Now()
		// A quick dial to the tunnel listener doubles as the RTT probe.
		if c, err := net.DialTimeout("tcp", s.DockerAddr, 2*time.Second); err == nil {
			_ = c.Close()
		}
		rtt := time.Since(start)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		list, err := client.Containers(ctx)
		if err != nil {
			return containersMsg{list: nil, rtt: rtt}
		}
		return containersMsg{list: list, rtt: rtt}
	}
}

func pollEnv(s Session) tea.Cmd {
	return func() tea.Msg {
		if s.Client == nil {
			return envMsg{}
		}
		env, err := s.Client.GetEnvironment(s.RequestID)
		if err != nil {
			return envMsg{}
		}
		return envMsg{env: env}
	}
}

func pollStatus(s Session) tea.Cmd {
	return func() tea.Msg {
		if s.Client == nil {
			return statusMsg{}
		}
		st, err := s.Client.PlatformStatus()
		if err != nil {
			return statusMsg{}
		}
		return statusMsg{status: st.Overall.Status, label: st.Overall.Label}
	}
}

func fetchLogs(s Session, c dockerapi.Container) tea.Cmd {
	return fetchLogsByID(s, c.ID)
}

func fetchLogsByID(s Session, id string) tea.Cmd {
	return func() tea.Msg {
		client := dockerapi.New(s.DockerAddr)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		body, err := client.Logs(ctx, id, 400)
		if err != nil {
			body = "failed to fetch logs: " + err.Error()
		}
		return logsMsg{id: id, body: stripANSI(body)}
	}
}

// commonCommands seed tab-completion alongside the project's own history and
// saved commands — the things people actually run against a Testcontainers
// environment, so the very first Tab has something useful to offer even before
// any history exists.
var commonCommands = []string{
	"mvn verify", "mvn test", "mvn clean verify",
	"gradle test", "gradle build", "gradle check",
	"pytest", "pytest -v", "go test ./...", "npm test", "npm run test",
	"docker ps", "docker images", "docker compose up -d", "docker compose logs -f",
	"docker version", "docker info",
}

// autocomplete completes the input against history + saved + common commands.
// The first Tab from a given prefix builds the candidate set; each further Tab
// cycles through it. Reset (acMatches=nil) happens on any live edit.
func (m *model) autocomplete() {
	prefix := m.input.Value()
	if strings.TrimSpace(prefix) == "" {
		return
	}
	if m.acMatches == nil {
		lower := strings.ToLower(prefix)
		seen := map[string]bool{}
		var cand []string
		add := func(list []string) {
			for _, c := range list {
				if c != "" && c != prefix && strings.HasPrefix(strings.ToLower(c), lower) && !seen[c] {
					seen[c] = true
					cand = append(cand, c)
				}
			}
		}
		add(m.sess.Commands.History(m.sess.ProjectDir))
		add(m.sess.Commands.Saved(m.sess.ProjectDir))
		add(commonCommands)
		if len(cand) == 0 {
			return
		}
		m.acMatches = cand
		m.acIdx = 0
	} else {
		m.acIdx = (m.acIdx + 1) % len(m.acMatches)
	}
	m.input.SetValue(m.acMatches[m.acIdx])
	m.input.CursorEnd()
}

// historyBack steps to an older command (↑). histIdx == -1 means we're editing
// the live input, whose text is stashed in histDraft so ↓ can restore it.
func (m *model) historyBack() {
	hist := m.sess.Commands.History(m.sess.ProjectDir)
	if len(hist) == 0 {
		return
	}
	switch {
	case m.histIdx == -1:
		m.histDraft = m.input.Value()
		m.histIdx = 0
	case m.histIdx < len(hist)-1:
		m.histIdx++
	default:
		return // already at the oldest
	}
	m.input.SetValue(hist[m.histIdx])
	m.input.CursorEnd()
}

// historyForward steps back toward the live input (↓).
func (m *model) historyForward() {
	if m.histIdx == -1 {
		return
	}
	hist := m.sess.Commands.History(m.sess.ProjectDir)
	if m.histIdx > 0 {
		m.histIdx--
		m.input.SetValue(hist[m.histIdx])
	} else {
		m.histIdx = -1
		m.input.SetValue(m.histDraft)
	}
	m.input.CursorEnd()
}

func runShellCmd(sess Session, cmdline string) tea.Cmd {
	return func() tea.Msg {
		shellPath := sess.ShellPath
		if shellPath == "" {
			shellPath = "/bin/sh"
		}
		c := exec.Command(shellPath, "-c", cmdline)
		c.Env = append(os.Environ(), "DOCKER_HOST="+sess.DockerHost)
		c.Dir = sess.ProjectDir
		out, err := c.CombinedOutput()
		result := stripANSI(string(out))
		if err != nil {
			if result != "" && !strings.HasSuffix(result, "\n") {
				result += "\n"
			}
			result += dimStyle.Render(fmt.Sprintf("(exit: %v)", err))
		}
		return cmdDoneMsg{output: result}
	}
}

// --- Update ---

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.relayout()
		return m, nil

	case tickMsg:
		m.ticks++
		cmds := []tea.Cmd{tick(), pollContainers(m.sess)} // containers every second
		if m.ticks%5 == 0 {
			cmds = append(cmds, pollEnv(m.sess))
		}
		if m.ticks%30 == 0 {
			cmds = append(cmds, pollStatus(m.sess))
		}
		// Tail-follow: while the logs overlay is open, keep pulling the
		// container's logs so they stream in.
		if m.overlay == overlayLogs && m.logsFollow && m.logsID != "" {
			cmds = append(cmds, fetchLogsByID(m.sess, m.logsID))
		}
		return m, tea.Batch(cmds...)

	case containersMsg:
		m.containers = msg.list
		m.rtt = msg.rtt
		if m.selected >= len(m.containers) {
			m.selected = max(0, len(m.containers)-1)
		}
		return m, nil

	case envMsg:
		if msg.env != nil {
			m.env = msg.env
		}
		return m, nil

	case statusMsg:
		m.platform, m.platLabel = msg.status, msg.label
		return m, nil

	case logsMsg:
		// Ignore a stale follow-refresh for a container we're no longer viewing.
		if m.overlay == overlayLogs && m.logsID != "" && msg.id != m.logsID {
			return m, nil
		}
		m.logsBody = msg.body
		if strings.TrimSpace(m.logsBody) == "" {
			m.logsBody = dimStyle.Render("(no output)")
		}
		wasFollowing := m.overlay == overlayLogs && m.logsFollow
		m.overlay = overlayLogs
		m.logsID = msg.id
		m.vp.SetContent(m.logsBody)
		// On the first open, start at the top; while following, ride the bottom
		// like `docker logs -f`.
		if wasFollowing {
			m.vp.GotoBottom()
		} else {
			m.logsFollow = true
			m.vp.GotoBottom()
		}
		return m, nil

	case cmdDoneMsg:
		m.running = false
		m.lines = append(m.lines, strings.Split(strings.TrimRight(msg.output, "\n"), "\n")...)
		m.lines = append(m.lines, "")
		if m.overlay == overlayNone {
			m.syncViewport()
		}
		m.input.Focus()
		return m, textinput.Blink

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	var vpCmd tea.Cmd
	m.vp, vpCmd = m.vp.Update(msg)
	return m, tea.Batch(cmd, vpCmd)
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global quit.
	if msg.Type == tea.KeyCtrlC || msg.Type == tea.KeyCtrlD {
		m.quitting = true
		return m, tea.Quit
	}

	// Picker overlay owns the keyboard while open.
	if m.overlay == overlayPicker {
		switch msg.Type {
		case tea.KeyEsc:
			m.overlay = overlayNone
		case tea.KeyUp:
			m.pickerIdx = max(0, m.pickerIdx-1)
		case tea.KeyDown:
			m.pickerIdx = min(len(m.pickerItems)-1, m.pickerIdx+1)
		case tea.KeyEnter:
			if m.pickerIdx < len(m.pickerItems) {
				m.input.SetValue(m.pickerItems[m.pickerIdx])
			}
			m.overlay = overlayNone
			m.input.Focus()
		}
		return m, nil
	}

	// Logs overlay: Esc closes, arrows scroll the viewport.
	if m.overlay == overlayLogs {
		if msg.Type == tea.KeyEsc {
			m.overlay = overlayNone
			m.logsFollow = false
			m.logsID = ""
			m.syncViewport()
			return m, nil
		}
		var vpCmd tea.Cmd
		m.vp, vpCmd = m.vp.Update(msg)
		return m, vpCmd
	}

	switch msg.Type {
	case tea.KeyShiftTab:
		// Switch panes (input ↔ container sidebar). Tab is reserved for
		// autocomplete now, so pane-switching moved here.
		if m.focus == focusInput {
			m.focus = focusSidebar
			m.input.Blur()
		} else {
			m.focus = focusInput
			m.input.Focus()
		}
		return m, nil
	case tea.KeyCtrlR:
		m.openPicker()
		return m, nil
	case tea.KeyCtrlS:
		if v := strings.TrimSpace(m.input.Value()); v != "" {
			m.sess.Commands.Save(m.sess.ProjectDir, v)
			m.flash("saved: " + v)
		}
		return m, nil
	case tea.KeyCtrlL:
		if c, ok := m.selectedContainer(); ok {
			m.logsID = c.ID
			m.logsFollow = true
			return m, fetchLogs(m.sess, c)
		}
		return m, nil
	case tea.KeyCtrlY:
		m.copyPort()
		return m, nil
	}

	// Sidebar focus: navigate containers.
	if m.focus == focusSidebar {
		switch msg.Type {
		case tea.KeyUp:
			m.selected = max(0, m.selected-1)
		case tea.KeyDown:
			m.selected = min(len(m.containers)-1, m.selected+1)
		case tea.KeyEnter:
			if c, ok := m.selectedContainer(); ok {
				m.logsID = c.ID
				m.logsFollow = true
				return m, fetchLogs(m.sess, c)
			}
		}
		return m, nil
	}

	// Input focus.
	// Tab cycles autocomplete candidates; ↑/↓ walk command history.
	switch msg.Type {
	case tea.KeyTab:
		m.autocomplete()
		return m, nil
	case tea.KeyUp:
		m.historyBack()
		return m, nil
	case tea.KeyDown:
		m.historyForward()
		return m, nil
	}

	if msg.Type == tea.KeyEnter {
		if m.running {
			return m, nil
		}
		cmdline := strings.TrimSpace(m.input.Value())
		if cmdline == "" {
			return m, nil
		}
		if cmdline == "exit" || cmdline == "quit" {
			m.quitting = true
			return m, tea.Quit
		}
		m.input.SetValue("")
		if cmdline == "clear" || cmdline == "cls" {
			m.lines = welcomeLines(m.sess)
			m.syncViewport()
			return m, nil
		}
		m.sess.Commands.AddHistory(m.sess.ProjectDir, cmdline)
		m.input.Blur()
		m.running = true
		m.histIdx = -1
		m.acMatches = nil
		m.lines = append(m.lines, promptGlyph.Render("❯")+" "+textStyle.Render(cmdline))
		m.syncViewport()
		return m, runShellCmd(m.sess, cmdline)
	}

	// Any other key is live editing — cancel history navigation and reset the
	// autocomplete cycle so the next Tab recomputes from the new text.
	m.histIdx = -1
	m.acMatches = nil
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) openPicker() {
	saved := m.sess.Commands.Saved(m.sess.ProjectDir)
	hist := m.sess.Commands.History(m.sess.ProjectDir)
	m.pickerSaved = map[string]bool{}
	seen := map[string]bool{}
	items := []string{}
	for _, c := range saved {
		if !seen[c] {
			seen[c] = true
			m.pickerSaved[c] = true
			items = append(items, c)
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return false }) // keep saved-first order
	for _, c := range hist {
		if !seen[c] {
			seen[c] = true
			items = append(items, c)
		}
	}
	m.pickerItems = items
	m.pickerIdx = 0
	if len(items) > 0 {
		m.overlay = overlayPicker
	} else {
		m.flash("no saved commands or history yet")
	}
}

func (m *model) flash(s string) {
	m.lines = append(m.lines, dimStyle.Render("· "+s), "")
	m.syncViewport()
}

func (m model) selectedContainer() (dockerapi.Container, bool) {
	if m.selected >= 0 && m.selected < len(m.containers) {
		return m.containers[m.selected], true
	}
	return dockerapi.Container{}, false
}

func (m *model) copyPort() {
	c, ok := m.selectedContainer()
	if !ok || len(c.Ports) == 0 {
		m.flash("no published port on the selected container to copy")
		return
	}
	addr := fmt.Sprintf("localhost:%d", c.Ports[0].Public)
	if err := clipboard.WriteAll(addr); err != nil {
		m.flash("copy failed (" + addr + ")")
		return
	}
	m.flash("copied " + addr + " to clipboard")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- layout + View ---

const sidebarWidth = 28

func (m *model) relayout() {
	innerWidth := m.width - 4
	if innerWidth < 20 {
		innerWidth = 20
	}
	m.input.Width = innerWidth - 2
	// header + hud + hairline + hairline + input + footer + box border/padding
	chromeHeight := 10
	vpHeight := m.height - chromeHeight
	if vpHeight < 3 {
		vpHeight = 3
	}
	mainWidth := innerWidth - sidebarWidth - 1
	if mainWidth < 10 {
		mainWidth = 10
	}
	m.vp.Width = mainWidth
	m.vp.Height = vpHeight
	if m.overlay == overlayNone {
		m.syncViewport()
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (m model) header() string {
	title := redBold.Render(tracked("devplat"))
	sub := dimStyle.Render(m.sess.Version + " · env " + shortID(m.sess.RequestID))
	left := eyebrowDot + " " + title + "  " + sub

	// #6 platform status + #7 parallel usage, right-aligned.
	var right string
	if m.platform != "" {
		dot := statusDot(m.platform)
		right += dot + " " + dimStyle.Render(m.platLabel)
	}
	if m.env != nil && m.env.Usage.Limit > 0 {
		if right != "" {
			right += dimStyle.Render("  ·  ")
		}
		right += dimStyle.Render(fmt.Sprintf("%d/%d envs", m.env.Usage.Running, m.env.Usage.Limit))
	}
	return padBetween(left, right, m.width-4)
}

// hud is the resource/TTL/RTT line (#2).
func (m model) hud() string {
	parts := []string{}
	if m.env != nil {
		if m.env.Vcpu > 0 {
			parts = append(parts, fmt.Sprintf("%d vCPU", m.env.Vcpu))
		}
		if m.env.RamMb > 0 {
			parts = append(parts, fmt.Sprintf("%d GB", m.env.RamMb/1024))
		}
		if m.env.Region != "" {
			parts = append(parts, m.env.Region)
		}
		if ttl := m.ttlRemaining(); ttl != "" {
			parts = append(parts, "TTL "+ttl)
		}
	}
	if m.rtt > 0 {
		parts = append(parts, fmt.Sprintf("RTT %dms", m.rtt.Milliseconds()))
	}
	if len(parts) == 0 {
		return dimStyle.Render("gathering environment info…")
	}
	return dimStyle.Render(strings.Join(parts, "  ·  "))
}

func (m model) ttlRemaining() string {
	if m.env == nil || m.env.ExpiresAt == "" {
		return ""
	}
	exp, err := time.Parse(time.RFC3339, m.env.ExpiresAt)
	if err != nil {
		return ""
	}
	d := time.Until(exp)
	if d <= 0 {
		return "expired"
	}
	h := int(d.Hours())
	mm := int(d.Minutes()) % 60
	ss := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, mm)
	}
	return fmt.Sprintf("%02d:%02d", mm, ss)
}

func statusDot(level string) string {
	switch level {
	case "operational":
		return greenStyle.Render("●")
	case "partial_outage", "major_outage":
		return lipgloss.NewStyle().Foreground(red).Render("●")
	default:
		return amberStyle.Render("●")
	}
}

// sidebar is the live container list (#1).
func (m model) sidebar() string {
	var b strings.Builder
	b.WriteString(dimStyle.Render(tracked("containers")) + "\n")
	if len(m.containers) == 0 {
		b.WriteString(dimStyle.Render("none running"))
	}
	for i, c := range m.containers {
		dot := dimStyle.Render("○")
		if c.State == "running" {
			dot = greenStyle.Render("●")
		}
		name := c.Name
		if name == "" {
			name = shortID(c.ID)
		}
		line := dot + " " + clip(name, sidebarWidth-3)
		if m.focus == focusSidebar && i == m.selected {
			line = selStyle.Render(clip(dot+" "+name, sidebarWidth-1))
		}
		b.WriteString(line + "\n")
		b.WriteString(dimStyle.Render("  "+clip(c.Image, sidebarWidth-3)) + "\n")
		for _, p := range c.Ports {
			b.WriteString(greenStyle.Render(fmt.Sprintf("  → localhost:%d", p.Public)) + "\n")
		}
	}
	return lipgloss.NewStyle().Width(sidebarWidth).Height(m.vp.Height).Render(b.String())
}

// mainPane is the output/logs viewport, or the command picker overlay.
func (m model) mainPane() string {
	if m.overlay == overlayPicker {
		return m.pickerView()
	}
	title := ""
	if m.overlay == overlayLogs {
		follow := ""
		if m.logsFollow {
			follow = greenStyle.Render(" ● live") + dimStyle.Render(" (following)")
		}
		title = amberStyle.Render(tracked("logs")) + follow + dimStyle.Render("  esc to close") + "\n"
	}
	return lipgloss.NewStyle().Width(m.vp.Width).Height(m.vp.Height).Render(title + m.vp.View())
}

func (m model) pickerView() string {
	var b strings.Builder
	b.WriteString(redBold.Render(tracked("commands")) + dimStyle.Render("  ↑↓ enter · esc") + "\n\n")
	for i, it := range m.pickerItems {
		star := " "
		if m.pickerSaved[it] {
			star = amberStyle.Render("★")
		}
		row := star + " " + clip(it, m.vp.Width-3)
		if i == m.pickerIdx {
			row = selStyle.Render(clip(star+" "+it, m.vp.Width-1))
		}
		b.WriteString(row + "\n")
	}
	return lipgloss.NewStyle().Width(m.vp.Width).Height(m.vp.Height).Render(b.String())
}

func (m model) View() string {
	if m.quitting {
		return ""
	}
	hairline := lipgloss.NewStyle().Foreground(hair).Render(strings.Repeat("─", max(1, m.width-4)))

	statusLine := m.input.View()
	if m.running {
		statusLine = amberStyle.Render("running…")
	} else if m.focus == focusSidebar {
		statusLine = dimStyle.Render("containers focused — ↑↓ select · enter/^l logs · ^y copy port · ⇧tab back to input")
	}

	body := lipgloss.JoinVertical(lipgloss.Left,
		m.header(),
		m.hud(),
		hairline,
		lipgloss.JoinHorizontal(lipgloss.Top,
			m.sidebar(),
			lipgloss.NewStyle().Foreground(hair).Render(strings.Repeat("│\n", max(1, m.vp.Height))),
			m.mainPane(),
		),
		hairline,
		statusLine,
		dimStyle.Render("↑ history · Tab complete · ⇧Tab panes · ^l logs · ^y copy port · ^r commands · exit"),
	)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(red).
		Padding(0, 1).
		Width(m.width - 2).
		Render(body)
}

// clip truncates s to n display columns (rough — good enough for names/images).
func clip(s string, n int) string {
	if n < 1 {
		n = 1
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// padBetween left-justifies l and right-justifies r within width.
func padBetween(l, r string, width int) string {
	lw := lipgloss.Width(l)
	rw := lipgloss.Width(r)
	gap := width - lw - rw
	if gap < 1 {
		gap = 1
	}
	return l + strings.Repeat(" ", gap) + r
}
