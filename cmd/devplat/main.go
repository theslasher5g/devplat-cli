// devplat is the client CLI described on the marketing site's Download page:
// a static binary that requests a remote microVM from the control plane,
// tunnels its Docker API back to a local TCP port, and opens a small
// bordered terminal UI (see internal/tui) where every command runs with
// DOCKER_HOST already set.
package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/theslasher5g/devplat-cli/internal/apiclient"
	"github.com/theslasher5g/devplat-cli/internal/config"
	"github.com/theslasher5g/devplat-cli/internal/portwatch"
	"github.com/theslasher5g/devplat-cli/internal/tui"
	"github.com/theslasher5g/devplat-cli/internal/tunnel"
	"github.com/theslasher5g/devplat-cli/internal/ui"
)

var version = "dev" // set via -ldflags at release build time

func main() {
	// lipgloss's default color detection reads TERM/COLORTERM and falls back
	// conservatively (sometimes to no color at all) when a terminal doesn't
	// advertise truecolor support explicitly, even though almost every
	// terminal in real use renders 24-bit color fine. We ship one exact
	// brand red (#E63312), so force the profile instead of trusting that
	// env-var detection — otherwise the border silently renders uncolored
	// on terminals that just don't bother setting COLORTERM.
	lipgloss.SetColorProfile(termenv.TrueColor)

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "connect":
		runConnect(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("devplat " + version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "devplat: unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`devplat — remote Testcontainers backend client

Usage:
  devplat connect [--token TOKEN] [--api-url URL]
      Requests a microVM, tunnels it to a local port, and opens an
      interactive terminal where DOCKER_HOST is already set. Type 'exit'
      or press Ctrl+D/Ctrl+C to disconnect and release the environment.

  devplat version
      Print the CLI version.

Token resolution: --token flag, then the DEVPLAT_TOKEN environment
variable. Create a scoped token (ci:run) in the dashboard under
Tokens. Control-plane URL defaults to https://api.devplat.ch, override
with --api-url or DEVPLAT_API_URL (mainly for local development).`)
}

func runConnect(args []string) {
	var tokenFlag, apiURLFlag string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--token":
			i++
			if i < len(args) {
				tokenFlag = args[i]
			}
		case "--api-url":
			i++
			if i < len(args) {
				apiURLFlag = args[i]
			}
		}
	}

	cfg := config.Resolve(tokenFlag, apiURLFlag)
	if cfg.Token == "" {
		fmt.Fprintln(os.Stderr, "devplat: no API token — pass --token or set DEVPLAT_TOKEN")
		os.Exit(1)
	}
	client := apiclient.New(cfg.APIURL, cfg.Token)

	ui.Banner(version)

	spin := ui.NewSpinner()
	spin.Start("requesting an environment…")
	env, err := client.RequestEnvironment()
	if err != nil {
		spin.Stop(false, "failed to request an environment")
		ui.Fatal("%s", err.Error())
		os.Exit(1)
	}
	env, err = awaitAssignment(client, env, spin)
	if err != nil {
		spin.Stop(false, "environment never became ready")
		ui.Fatal("%s", err.Error())
		os.Exit(1)
	}
	spin.Stop(true, fmt.Sprintf("assigned (request %s)", env.RequestID))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		ui.Fatal("failed to open a local port: %s", err.Error())
		release(client, env.RequestID)
		os.Exit(1)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	dockerHost := fmt.Sprintf("tcp://127.0.0.1:%d", port)
	ui.Line(true, "tunnel active")

	// Mirror container ports the VM's Docker publishes onto the same local
	// ports, so Testcontainers' mapped-port connections (which resolve the
	// docker host to 127.0.0.1) actually land somewhere — see
	// internal/portwatch.
	watcher := portwatch.New(cfg.APIURL, cfg.Token, env.RequestID, fmt.Sprintf("127.0.0.1:%d", port))
	watcher.Start()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // listener closed once the child shell exits
			}
			go func() {
				if err := tunnel.Bridge(cfg.APIURL, cfg.Token, env.RequestID, conn); err != nil {
					fmt.Fprintln(os.Stderr, "devplat: tunnel connection ended: "+err.Error())
				}
			}()
		}
	}()

	// SIGTERM (an explicit, programmatic kill of devplat itself — not the
	// user quitting the TUI, which is handled as a plain keypress inside
	// it) still needs to release the environment on its way out.
	termCh := make(chan os.Signal, 1)
	signal.Notify(termCh, syscall.SIGTERM)
	programDone := make(chan struct{})
	go func() {
		select {
		case <-termCh:
			release(client, env.RequestID)
			os.Exit(0)
		case <-programDone:
		}
	}()

	shellPath := os.Getenv("SHELL")
	if shellPath == "" {
		shellPath = "/bin/sh"
	}
	tuiErr := tui.Run(tui.Session{
		Version:    version,
		RequestID:  env.RequestID,
		DockerHost: dockerHost,
		ShellPath:  shellPath,
	})
	close(programDone)

	watcher.Stop()
	listener.Close()
	release(client, env.RequestID)
	if tuiErr != nil {
		fmt.Fprintln(os.Stderr, "devplat: "+tuiErr.Error())
	}
	ui.Farewell()
}

// awaitAssignment polls GET /environments/:id until the scheduler has
// placed the request on a host (or it fails) — POST /environments can
// return "queued" immediately when the team is at its parallelism limit.
func awaitAssignment(client *apiclient.Client, env *apiclient.Environment, spin *ui.Spinner) (*apiclient.Environment, error) {
	deadline := time.Now().Add(2 * time.Minute)
	// "assigning" is a brief in-progress claim the scheduler holds while it's
	// actually booting a VM (see devplat-backend's allocator.ts) — it's not
	// "queued" and not "failed", so without handling it explicitly here this
	// loop would exit early and report success before docker_endpoint even
	// exists.
	for env.Status == "queued" || env.Status == "assigning" {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for capacity (request %s is still %s)", env.RequestID, env.Status)
		}
		if env.Status == "assigning" {
			spin.Update(ui.Amber("assigning a host…"))
		} else {
			spin.Update(ui.Amber("queued, waiting for capacity…"))
		}
		time.Sleep(2 * time.Second)
		next, err := client.GetEnvironment(env.RequestID)
		if err != nil {
			return nil, err
		}
		env = next
	}
	if env.Status == "failed" {
		return nil, fmt.Errorf("environment request failed: %s", env.Error)
	}
	return env, nil
}

func release(client *apiclient.Client, requestID string) {
	if err := client.ReleaseEnvironment(requestID); err != nil {
		fmt.Fprintln(os.Stderr, "devplat: failed to release environment: "+err.Error())
	}
}
