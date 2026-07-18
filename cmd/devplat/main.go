// devplat is the client CLI described on the marketing site's Download page:
// a static binary that requests a remote microVM from the control plane,
// tunnels its Docker API back to a local TCP port, and drops the user into
// their own shell with DOCKER_HOST already set for whatever test command
// runs next.
package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/theslasher5g/devplat-cli/internal/apiclient"
	"github.com/theslasher5g/devplat-cli/internal/config"
	"github.com/theslasher5g/devplat-cli/internal/tunnel"
	"github.com/theslasher5g/devplat-cli/internal/ui"
)

var version = "dev" // set via -ldflags at release build time

func main() {
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
      Requests a microVM, tunnels it to a local port, and drops you into
      your own shell with DOCKER_HOST already set. Exit that shell (or
      Ctrl+D) to disconnect and release the environment.

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

	// Ctrl+C inside the child shell should behave like it does in any other
	// shell (interrupt whatever's running in the foreground, not the shell
	// itself) — but since the child inherits our terminal and process
	// group, the same SIGINT reaches us too. Drain and ignore it here so it
	// doesn't kill devplat out from under the shell; SIGTERM (an explicit,
	// programmatic kill of devplat itself) still tears everything down below.
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt)
	go func() {
		for range sigc {
		}
	}()

	ui.SessionBox(env.RequestID, dockerHost)

	shellPath := os.Getenv("SHELL")
	if shellPath == "" {
		shellPath = "/bin/sh"
	}
	shell := exec.Command(shellPath)
	shell.Env = append(os.Environ(), "DOCKER_HOST="+dockerHost)
	shell.Stdin = os.Stdin
	shell.Stdout = os.Stdout
	shell.Stderr = os.Stderr

	termCh := make(chan os.Signal, 1)
	signal.Notify(termCh, syscall.SIGTERM)
	shellDone := make(chan struct{})
	go func() {
		select {
		case <-termCh:
			if shell.Process != nil {
				_ = shell.Process.Signal(syscall.SIGTERM)
			}
		case <-shellDone:
		}
	}()

	_ = shell.Run() // exit status of the user's shell isn't devplat's to report
	close(shellDone)

	listener.Close()
	release(client, env.RequestID)
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
