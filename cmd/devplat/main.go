// devplat is the client CLI described on the marketing site's Download page:
// a static binary that requests a remote microVM from the control plane,
// tunnels its Docker API back to a local TCP port, and prints the
// DOCKER_HOST export for whatever test command runs next.
package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/theslasher5g/devplat-cli/internal/apiclient"
	"github.com/theslasher5g/devplat-cli/internal/config"
	"github.com/theslasher5g/devplat-cli/internal/tunnel"
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
      Requests a microVM, tunnels it to a local port, and prints the
      DOCKER_HOST export to use for the rest of the session. Runs until
      interrupted (Ctrl+C), then releases the environment.

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

	fmt.Println("Requesting an environment…")
	env, err := client.RequestEnvironment()
	if err != nil {
		fmt.Fprintln(os.Stderr, "devplat: "+err.Error())
		os.Exit(1)
	}
	env, err = awaitAssignment(client, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devplat: "+err.Error())
		os.Exit(1)
	}
	fmt.Printf("✓ Assigned (request %s)\n", env.RequestID)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "devplat: failed to open a local port: "+err.Error())
		release(client, env.RequestID)
		os.Exit(1)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	fmt.Printf("✓ Tunnel active\n\n")
	fmt.Printf("  export DOCKER_HOST=tcp://127.0.0.1:%d\n\n", port)
	fmt.Println("Run your test command in this shell, or eval the line above. Ctrl+C to disconnect.")

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigc
		fmt.Println("\nReleasing environment…")
		// Release before closing the listener: closing it unblocks the main
		// goroutine's Accept() loop below, which returns and lets main()
		// finish — and the whole process exits the instant main() returns,
		// regardless of what this goroutine is still doing. Releasing first
		// means the DELETE call is guaranteed to complete before that happens.
		release(client, env.RequestID)
		listener.Close()
		os.Exit(0)
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			return // listener closed (Ctrl+C path above)
		}
		go func() {
			if err := tunnel.Bridge(cfg.APIURL, cfg.Token, env.RequestID, conn); err != nil {
				fmt.Fprintln(os.Stderr, "devplat: tunnel connection ended: "+err.Error())
			}
		}()
	}
}

// awaitAssignment polls GET /environments/:id until the scheduler has
// placed the request on a host (or it fails) — POST /environments can
// return "queued" immediately when the team is at its parallelism limit.
func awaitAssignment(client *apiclient.Client, env *apiclient.Environment) (*apiclient.Environment, error) {
	deadline := time.Now().Add(2 * time.Minute)
	for env.Status == "queued" {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for capacity (request %s is still queued)", env.RequestID)
		}
		fmt.Println("… queued, waiting for capacity")
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
