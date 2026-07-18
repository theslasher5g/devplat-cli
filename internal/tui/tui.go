// Package tui is devplat connect's post-tunnel interface: a single bordered
// terminal application (bubbletea) with a header, a scrollable output pane,
// and an input line at the bottom. Styled to match devplat-frontend's own
// design system (see devplat-frontend/src/index.css: --dark/--red/--green/
// --dark-muted) rather than an arbitrary palette, so the CLI reads as the
// same product as the website. Whatever the user types is run as a shell
// command with DOCKER_HOST already set, so `docker version`, `mvn verify`,
// etc. work exactly as they would in a normal terminal; only the chrome
// around them is custom.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Colors lifted directly from devplat-frontend/src/index.css's dark-mode
// tokens, not picked independently — the CLI and the website should read as
// the same product.
var (
	red    = lipgloss.Color("#E63312") // --red
	green  = lipgloss.Color("#23A26D") // --green
	amber  = lipgloss.Color("#D99000") // --amber
	text   = lipgloss.Color("#EDEDE8") // --dark-text
	muted  = lipgloss.Color("#8A8A82") // --dark-muted
	hair   = lipgloss.Color("#262624") // --dark-line

	dimStyle    = lipgloss.NewStyle().Foreground(muted)
	textStyle   = lipgloss.NewStyle().Foreground(text)
	redBold     = lipgloss.NewStyle().Foreground(red).Bold(true)
	promptGlyph = lipgloss.NewStyle().Foreground(red).Bold(true)
	// Mirrors .eyebrow-dot from the website: a small red bullet ahead of a
	// tracked-out uppercase label.
	eyebrowDot = lipgloss.NewStyle().Foreground(red).Render("●")
)

// tracked emulates the website's letter-spacing (.eyebrow's 0.22em) in a
// monospace terminal by inserting hair spaces between characters — the
// closest a terminal can get to that tracked-out dot-matrix look.
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

// ansiEscape strips CSI/OSC control sequences from arbitrary command output
// before it's folded into the viewport's own content. Without this, a
// command like `clear` (or anything else that writes raw terminal control
// codes, e.g. a screen-clear or cursor-repositioning sequence) gets
// embedded directly into the bordered box's rendered frame and corrupts the
// whole layout — the box itself is just another string being printed, it
// has no way to "absorb" a real clear-screen sequence a child process emits.
var ansiEscape = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[a-zA-Z]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)

func stripANSI(s string) string { return ansiEscape.ReplaceAllString(s, "") }

// Session carries everything the TUI needs that comes from outside it —
// the already-established tunnel and the callback to release the
// environment when the user quits. Nothing in here talks to the network
// directly; that stays in main.go/apiclient, same as before.
type Session struct {
	Version    string
	RequestID  string
	DockerHost string
	ShellPath  string // $SHELL, falls back to /bin/sh if empty
	OnQuit     func() // called once, after the TUI has torn down the terminal
}

type cmdDoneMsg struct {
	output string
}

type model struct {
	sess     Session
	vp       viewport.Model
	input    textinput.Model
	lines    []string
	running  bool
	quitting bool
	width    int
	height   int
}

func welcomeLines() []string {
	return []string{
		dimStyle.Render("Connected. DOCKER_HOST is set for every command you run here."),
		dimStyle.Render("Type 'exit' or press Ctrl+D to disconnect · 'clear' to clear this pane."),
		"",
	}
}

func Run(sess Session) error {
	ti := textinput.New()
	ti.Placeholder = "docker version, mvn verify, …"
	ti.Focus()
	ti.CharLimit = 4096
	ti.Prompt = ""
	ti.TextStyle = textStyle
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(green)

	vp := viewport.New(80, 20)
	m := model{sess: sess, vp: vp, input: ti, lines: welcomeLines()}
	m.syncViewport()

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m *model) syncViewport() {
	m.vp.SetContent(strings.Join(m.lines, "\n"))
	m.vp.GotoBottom()
}

// header, footer, chromeHeight together define how much vertical space the
// bordered box's own decoration takes, so the viewport gets exactly what's
// left of the terminal height.
func (m model) header() string {
	title := redBold.Render(tracked("devplat"))
	sub := dimStyle.Render(m.sess.Version + " · env " + shortID(m.sess.RequestID))
	return eyebrowDot + " " + title + "  " + sub
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		innerWidth := m.width - 4 // border + padding
		if innerWidth < 10 {
			innerWidth = 10
		}
		m.input.Width = innerWidth - 2
		chromeHeight := 8 // header + blank + input row + borders/padding
		vpHeight := m.height - chromeHeight
		if vpHeight < 3 {
			vpHeight = 3
		}
		m.vp.Width = innerWidth
		m.vp.Height = vpHeight
		m.syncViewport()
		return m, nil

	case cmdDoneMsg:
		m.running = false
		m.lines = append(m.lines, strings.Split(strings.TrimRight(msg.output, "\n"), "\n")...)
		m.lines = append(m.lines, "")
		m.syncViewport()
		m.input.Focus()
		return m, textinput.Blink

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyCtrlD:
			m.quitting = true
			return m, tea.Quit
		case tea.KeyEnter:
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
			// 'clear'/'cls' reset the pane directly instead of shelling out —
			// running the real `clear` binary would emit a raw screen-clear
			// escape sequence as its "output", which corrupts this box's own
			// layout the instant it's folded into the viewport content.
			if cmdline == "clear" || cmdline == "cls" {
				m.lines = welcomeLines()
				m.syncViewport()
				return m, nil
			}
			m.input.Blur()
			m.running = true
			m.lines = append(m.lines, promptGlyph.Render("❯")+" "+textStyle.Render(cmdline))
			m.syncViewport()
			return m, runShellCmd(m.sess, cmdline)
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	var vpCmd tea.Cmd
	m.vp, vpCmd = m.vp.Update(msg)
	return m, tea.Batch(cmd, vpCmd)
}

func runShellCmd(sess Session, cmdline string) tea.Cmd {
	return func() tea.Msg {
		shellPath := sess.ShellPath
		if shellPath == "" {
			shellPath = "/bin/sh"
		}
		c := exec.Command(shellPath, "-c", cmdline)
		c.Env = append(os.Environ(), "DOCKER_HOST="+sess.DockerHost)
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

func (m model) View() string {
	if m.quitting {
		return ""
	}
	statusLine := m.input.View()
	if m.running {
		statusLine = lipgloss.NewStyle().Foreground(amber).Render("running…")
	}

	body := lipgloss.JoinVertical(lipgloss.Left,
		m.header(),
		"",
		m.vp.View(),
		lipgloss.NewStyle().Foreground(hair).Render(strings.Repeat("─", max(1, m.vp.Width))),
		statusLine,
	)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(red).
		Padding(0, 1).
		Width(m.width - 2).
		Render(body)
	return box
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
