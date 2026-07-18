// Package ui is the small styling/spinner layer behind `devplat connect`'s
// terminal output — kept deliberately separate from main.go so the actual
// connect/tunnel logic doesn't get lost in ANSI-escape bookkeeping.
package ui

import (
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Palette matches devplat-frontend/src/index.css's dark-mode tokens
// (--red/--green/--amber/--dark-muted) rather than an arbitrary one, so the
// CLI reads as the same product as the website.
var (
	accent  = lipgloss.Color("#E63312") // --red
	green   = lipgloss.Color("#23A26D") // --green
	amber   = lipgloss.Color("#D99000") // --amber
	red     = lipgloss.Color("#E63312") // --red
	muted   = lipgloss.Color("#8A8A82") // --dark-muted
	dim     = lipgloss.NewStyle().Foreground(muted)
	okMark  = lipgloss.NewStyle().Foreground(green).Bold(true)
	badMark = lipgloss.NewStyle().Foreground(red).Bold(true)

	spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
)

// Banner prints the small header shown once at the start of `devplat connect`.
// The red bullet mirrors the website's .eyebrow-dot treatment.
func Banner(version string) {
	dot := lipgloss.NewStyle().Foreground(accent).Render("●")
	title := lipgloss.NewStyle().Foreground(accent).Bold(true).Render("devplat")
	fmt.Printf("%s %s %s\n", dot, title, dim.Render(version))
}

// Spinner renders a single status line that overwrites itself in place
// (like `... assigning a host`) until Stop is called. Safe to use in a
// linear, single-goroutine flow — this isn't a general-purpose progress
// bar, just enough polish for a handful of sequential connect phases.
type Spinner struct {
	stop   chan struct{}
	done   chan struct{}
	label  string
	frameI int
}

func NewSpinner() *Spinner {
	return &Spinner{stop: make(chan struct{}), done: make(chan struct{})}
}

// Start begins rendering label with an animated frame in front of it.
// Call Update to change the label without restarting the animation, and
// Stop (with a final glyph + message) when the phase ends.
func (s *Spinner) Start(label string) {
	s.label = label
	go func() {
		ticker := time.NewTicker(90 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				close(s.done)
				return
			case <-ticker.C:
				frame := spinnerFrames[s.frameI%len(spinnerFrames)]
				s.frameI++
				fmt.Printf("\r\033[K%s %s", lipgloss.NewStyle().Foreground(accent).Render(frame), s.label)
			}
		}
	}()
}

// Update swaps the label shown next to the spinner (e.g. "queued" -> "assigning").
func (s *Spinner) Update(label string) { s.label = label }

// Stop halts the animation and replaces the line with a final ✓/✗ + message.
func (s *Spinner) Stop(ok bool, message string) {
	close(s.stop)
	<-s.done
	mark := okMark.Render("✓")
	if !ok {
		mark = badMark.Render("✗")
	}
	fmt.Printf("\r\033[K%s %s\n", mark, message)
}

// Line prints a single, non-spinning status line (for phases too short to
// bother animating, or plain informational output).
func Line(ok bool, message string) {
	mark := okMark.Render("✓")
	if !ok {
		mark = badMark.Render("✗")
	}
	fmt.Printf("%s %s\n", mark, message)
}

// Farewell prints the closing line once the child shell exits and the
// environment has been released.
func Farewell() {
	fmt.Println()
	fmt.Println(dim.Render("Environment released. Goodbye."))
}

// Fatal prints a red error line to stderr. Doesn't exit itself — callers
// decide the exit code.
func Fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", badMark.Render("✗"), fmt.Sprintf(format, args...))
}

var amberStyle = lipgloss.NewStyle().Foreground(amber)

// Amber renders a warning-toned line (used for the queued/assigning wait).
func Amber(message string) string { return amberStyle.Render(message) }
