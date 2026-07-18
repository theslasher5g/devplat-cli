// Package tui is devplat connect's post-tunnel interface: a single bordered
// terminal application (bubbletea) with a header, a scrollable output pane,
// and an input line at the bottom — modeled on Claude Code's own CLI look.
// Whatever the user types is run as a shell command with DOCKER_HOST already
// set, so `docker version`, `mvn verify`, etc. work exactly as they would in
// a normal terminal; only the chrome around them is custom.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	accent    = lipgloss.Color("#7C6CF0")
	green     = lipgloss.Color("#57C99A")
	muted     = lipgloss.Color("#7A7A85")
	dimStyle  = lipgloss.NewStyle().Foreground(muted)
	promptDot = lipgloss.NewStyle().Foreground(accent).Bold(true)
)

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

func Run(sess Session) error {
	ti := textinput.New()
	ti.Placeholder = "docker version, mvn verify, …"
	ti.Focus()
	ti.CharLimit = 4096
	ti.Prompt = ""

	vp := viewport.New(80, 20)
	m := model{sess: sess, vp: vp, input: ti}
	m.lines = []string{
		dimStyle.Render("Connected. DOCKER_HOST is set for every command you run here."),
		dimStyle.Render("Type 'exit' or press Ctrl+D to disconnect and release the environment."),
		"",
	}
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
	title := lipgloss.NewStyle().Foreground(accent).Bold(true).Render("devplat")
	sub := dimStyle.Render(m.sess.Version + " · environment " + shortID(m.sess.RequestID))
	return title + "  " + sub
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
			m.input.Blur()
			m.running = true
			m.lines = append(m.lines, promptDot.Render("❯")+" "+cmdline)
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
		result := string(out)
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
		statusLine = dimStyle.Render("running…")
	}

	body := lipgloss.JoinVertical(lipgloss.Left,
		m.header(),
		"",
		m.vp.View(),
		dimStyle.Render(strings.Repeat("─", max(1, m.vp.Width))),
		statusLine,
	)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
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
