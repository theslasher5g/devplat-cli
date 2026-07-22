// devplat is the client CLI described on the marketing site's Download page:
// a static binary that requests a remote microVM from the control plane,
// tunnels its Docker API back to a local TCP port, and opens a small
// bordered terminal UI (see internal/tui) where every command runs with
// DOCKER_HOST already set.
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/theslasher5g/devplat-cli/internal/apiclient"
	"github.com/theslasher5g/devplat-cli/internal/commands"
	"github.com/theslasher5g/devplat-cli/internal/compose"
	"github.com/theslasher5g/devplat-cli/internal/config"
	"github.com/theslasher5g/devplat-cli/internal/credentials"
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
	case "login":
		runLogin(os.Args[2:])
	case "logout":
		runLogout(os.Args[2:])
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
  devplat login [--token TOKEN] [--api-url URL]
      Sign in so 'devplat connect' works without a token each time. With no
      flags, opens a browser sign-in (device flow) and stores the resulting
      token. With --token, stores a token you already created in the
      dashboard. Saved to your user config dir.

  devplat logout
      Revoke the stored token and remove it from this machine.

  devplat connect [--token TOKEN] [--api-url URL] [--exec "CMD"]
      Requests a microVM and tunnels it to a local port with DOCKER_HOST set.
      Without --exec, opens an interactive terminal (type 'exit' or press
      Ctrl+D/Ctrl+C to disconnect). With --exec, runs CMD headless — for CI:
      the command inherits DOCKER_HOST, its exit code becomes devplat's, and
      the environment is released when it finishes. Either way the
      environment is torn down on exit.

  devplat version
      Print the CLI version.

Token resolution: --token flag, then the DEVPLAT_TOKEN environment
variable, then the token saved by 'devplat login'. Control-plane URL
defaults to https://api.devplat.ch, override with --api-url or
DEVPLAT_API_URL (mainly for local development).`)
}

// parseConnFlags pulls --token/--api-url/--exec out of an arg slice, shared by
// connect/login/logout. login/logout ignore execCmd (they have no --exec).
func parseConnFlags(args []string) (token, apiURL, execCmd string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--token":
			if i++; i < len(args) {
				token = args[i]
			}
		case "--api-url":
			if i++; i < len(args) {
				apiURL = args[i]
			}
		case "--exec":
			if i++; i < len(args) {
				execCmd = args[i]
			}
		}
	}
	return token, apiURL, execCmd
}

func runLogin(args []string) {
	tokenFlag, apiURLFlag, _ := parseConnFlags(args)

	// Resolve the control-plane URL the same way connect does, but WITHOUT the
	// stored token feeding back in (we're about to write it): flag, env, then
	// default. The api-url is what gets saved alongside the token.
	apiURL := apiURLFlag
	if apiURL == "" {
		apiURL = os.Getenv("DEVPLAT_API_URL")
	}
	if apiURL == "" {
		apiURL = config.DefaultAPIURL
	}

	ui.Banner(version)

	// --token: store a token the user already created in the dashboard. The
	// cheap path — no browser, no polling.
	if tokenFlag != "" {
		if !strings.HasPrefix(tokenFlag, "dvp_") {
			ui.Fatal("that doesn't look like a devplat token (expected a dvp_… value)")
			os.Exit(1)
		}
		if err := credentials.Save(credentials.Credentials{Token: tokenFlag, APIURL: apiURL}); err != nil {
			ui.Fatal("could not save credentials: %s", err.Error())
			os.Exit(1)
		}
		ui.Line(true, "token saved to "+credentials.Path())
		ui.Note("You can now run 'devplat connect' without --token.")
		return
	}

	// Device flow: start a request, show the code, poll until approved.
	client := apiclient.New(apiURL, "")
	da, err := client.StartDeviceAuth()
	if err != nil {
		ui.Fatal("could not start login: %s", err.Error())
		os.Exit(1)
	}

	fmt.Println()
	ui.Note("To sign in, open this URL in your browser:")
	fmt.Println("  " + ui.Highlight(da.VerificationURI))
	ui.Note("and enter the code:")
	fmt.Println("  " + ui.Highlight(da.UserCode))
	fmt.Println()
	if openBrowser(da.VerificationURIComplete) {
		ui.Note("(opened your browser automatically — approve there, or use the URL above)")
	}

	spin := ui.NewSpinner()
	spin.Start("waiting for you to authorize in the browser…")

	interval := time.Duration(da.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(da.ExpiresIn) * time.Second)
	for {
		if time.Now().After(deadline) {
			spin.Stop(false, "login timed out — run 'devplat login' again")
			os.Exit(1)
		}
		time.Sleep(interval)
		res, err := client.PollDeviceToken(da.DeviceCode)
		if err != nil {
			// Terminal server states end the loop; anything else (a transient
			// network blip) is retried until the deadline.
			switch err.Error() {
			case "expired_token":
				spin.Stop(false, "login expired — run 'devplat login' again")
				os.Exit(1)
			case "invalid_device_code", "already_completed":
				spin.Stop(false, "login could not be completed — run 'devplat login' again")
				os.Exit(1)
			}
			continue
		}
		switch res.Status {
		case "pending":
			continue
		case "denied":
			spin.Stop(false, "login was denied in the browser")
			os.Exit(1)
		case "complete":
			// Store the URL the CLI actually reached the server on — that's
			// definitionally the working endpoint for future `connect` calls.
			// The server also reports its own canonical apiUrl, but trusting it
			// would store the production default for a dev/self-hosted backend
			// that never set API_URL, breaking later connects.
			if err := credentials.Save(credentials.Credentials{Token: res.Token, APIURL: apiURL}); err != nil {
				spin.Stop(false, "authorized, but could not save credentials")
				ui.Fatal("%s", err.Error())
				os.Exit(1)
			}
			spin.Stop(true, "logged in — token saved to "+credentials.Path())
			ui.Note("You can now run 'devplat connect'.")
			return
		}
	}
}

func runLogout(args []string) {
	stored, _ := credentials.Load()
	if stored == nil || stored.Token == "" {
		ui.Line(true, "not logged in — nothing to do")
		return
	}
	// Best-effort server-side revoke so the token can't be reused, then remove
	// it locally regardless of whether the revoke call reached the server.
	apiURL := stored.APIURL
	if apiURL == "" {
		apiURL = config.DefaultAPIURL
	}
	if err := apiclient.New(apiURL, stored.Token).RevokeToken(); err != nil {
		ui.Note("(could not reach the server to revoke the token: %s — removing it locally anyway)", err.Error())
	}
	if err := credentials.Delete(); err != nil {
		ui.Fatal("could not remove stored credentials: %s", err.Error())
		os.Exit(1)
	}
	ui.Line(true, "logged out — token revoked and removed")
}

// openBrowser tries to open url in the user's default browser, returning
// whether the launch command started. Best-effort: on a headless/CI box there
// may be no browser, in which case the printed URL + code is the fallback.
func openBrowser(url string) bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start() == nil
}

func runConnect(args []string) {
	tokenFlag, apiURLFlag, execCmd := parseConnFlags(args)

	cfg := config.Resolve(tokenFlag, apiURLFlag)
	if cfg.Token == "" {
		fmt.Fprintln(os.Stderr, "devplat: no API token — run 'devplat login', pass --token, or set DEVPLAT_TOKEN")
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
				return // listener closed once the session ends
			}
			go func() {
				if err := tunnel.Bridge(cfg.APIURL, cfg.Token, env.RequestID, conn); err != nil {
					fmt.Fprintln(os.Stderr, "devplat: tunnel connection ended: "+err.Error())
				}
			}()
		}
	}()

	shellPath := os.Getenv("SHELL")
	if shellPath == "" {
		shellPath = "/bin/sh"
	}

	// --exec: headless mode for CI. Instead of the interactive TUI, run one
	// command with DOCKER_HOST set, propagate its exit code, and release the
	// environment on the way out. This is what makes the documented CI
	// workflow real — a backgrounded interactive `connect` never handed the
	// caller's shell a DOCKER_HOST.
	if execCmd != "" {
		ui.Line(true, "running: "+execCmd)
		code := runExecCommand(execCmd, shellPath, dockerHost)
		watcher.Stop()
		listener.Close()
		release(client, env.RequestID)
		os.Exit(code)
	}

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

	// Project context for the TUI: cwd drives per-project command history and
	// the docker-compose bind-mount warning.
	projectDir, _ := os.Getwd()
	bindMounts, _ := compose.Detect(projectDir)
	tuiErr := tui.Run(tui.Session{
		Version:    version,
		RequestID:  env.RequestID,
		DockerHost: dockerHost,
		DockerAddr: fmt.Sprintf("127.0.0.1:%d", port),
		ShellPath:  shellPath,
		ProjectDir: projectDir,
		Client:     client,
		Commands:   commands.Load(),
		BindMounts: bindMounts,
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

// runExecCommand runs one command with DOCKER_HOST set (headless --exec mode),
// wiring the child's stdio straight through so its output and exit code are
// the CLI's own. Interrupt/terminate signals are forwarded to the child so it
// can abort cleanly; the caller releases the environment after this returns
// regardless of how the command ended.
func runExecCommand(cmdline, shellPath, dockerHost string) int {
	c := exec.Command(shellPath, "-c", cmdline)
	c.Env = append(os.Environ(), "DOCKER_HOST="+dockerHost)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Start(); err != nil {
		ui.Fatal("failed to start command: %s", err.Error())
		return 1
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		for s := range sigCh {
			if c.Process != nil {
				_ = c.Process.Signal(s)
			}
		}
	}()

	err := c.Wait()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	ui.Fatal("command error: %s", err.Error())
	return 1
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
