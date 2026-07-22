package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/theslasher5g/devplat-cli/internal/apiclient"
	"github.com/theslasher5g/devplat-cli/internal/commands"
	"github.com/theslasher5g/devplat-cli/internal/compose"
	"github.com/theslasher5g/devplat-cli/internal/dockerapi"
)

func newTestModel(t *testing.T) model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ti := textinput.New()
	ti.Focus()
	sess := Session{
		Version: "1.0", RequestID: "req_abcdef1234", ProjectDir: t.TempDir(),
		Commands: commands.Load(),
		BindMounts: []compose.BindMount{{Service: "db", Source: "./data", Target: "/var/lib/postgresql"}},
	}
	m := model{sess: sess, vp: viewport.New(80, 20), input: ti, lines: welcomeLines(sess)}
	return m
}

func step(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(model)
}

func TestView_RendersAllPanels(t *testing.T) {
	m := newTestModel(t)
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = step(t, m, containersMsg{
		list: []dockerapi.Container{{Name: "pg", Image: "postgres:16", State: "running", Ports: []dockerapi.Port{{Public: 54321}}}},
		rtt:  8 * time.Millisecond,
	})
	m = step(t, m, envMsg{env: &apiclient.Environment{
		Vcpu: 2, RamMb: 4096, Region: "CH-BSL-1",
		ExpiresAt: time.Now().Add(30 * time.Minute).Format(time.RFC3339),
		Usage:     struct{ Running int `json:"running"`; Limit int `json:"limit"` }{Running: 1, Limit: 2},
	}})
	m = step(t, m, statusMsg{status: "operational", label: "All systems operational"})

	view := m.View()
	for _, want := range []string{
		"req_abcd",                    // short env id
		"postgres:16",                 // container image (#1)
		"localhost:54321",             // mirrored port (#1)
		"2 vCPU", "4 GB", "CH-BSL-1",  // resource HUD (#2)
		"TTL",                          // TTL countdown (#2)
		"1/2 envs",                     // parallel usage (#7)
		"All systems operational",      // platform status (#6)
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q", want)
		}
	}
	// #8 bind-mount warning is in the output pane content.
	if !strings.Contains(strings.Join(m.lines, "\n"), "bind mounts won't work") {
		t.Error("expected bind-mount warning in output")
	}
}

func TestKeys_NoPanic(t *testing.T) {
	m := newTestModel(t)
	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = step(t, m, containersMsg{list: []dockerapi.Container{{Name: "pg", State: "running", Ports: []dockerapi.Port{{Public: 5432}}}}})

	// Tab into the sidebar, navigate, request logs (returns a cmd).
	m = step(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if m.focus != focusSidebar {
		t.Fatal("Tab should focus the sidebar")
	}
	m = step(t, m, tea.KeyMsg{Type: tea.KeyDown})
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	m = next.(model)
	if cmd == nil {
		t.Error("^l on a selected container should produce a fetch command")
	}

	// Command picker with history present.
	m = step(t, m, tea.KeyMsg{Type: tea.KeyTab}) // back to input
	m.sess.Commands.AddHistory(m.sess.ProjectDir, "mvn verify")
	m = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlR})
	if m.overlay != overlayPicker {
		t.Fatal("^r should open the picker when history exists")
	}
	m = step(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // pick → fills input
	if m.input.Value() != "mvn verify" {
		t.Errorf("picker enter should fill input, got %q", m.input.Value())
	}
	_ = m.View() // must not panic in any state
}
