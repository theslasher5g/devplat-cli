// Package portwatch makes Testcontainers' published ports actually
// reachable. A test doesn't talk to the Docker API to reach its Postgres —
// it inspects the container, learns the published host port (an ephemeral
// port on the REMOTE VM), and dials <docker host>:<that port>. With
// DOCKER_HOST pointing at the CLI's local tunnel listener, "docker host"
// resolves to 127.0.0.1 — so the connection lands on the developer's own
// machine, where nothing is listening.
//
// This watcher closes that gap: it polls the (already tunneled) Docker API
// for running containers' published TCP ports and mirrors each one as a
// local listener on 127.0.0.1:<same port>, bridging every accepted
// connection through the control plane's per-port tunnel
// (/environments/:id/tunnel/:port) to the same port inside the guest. The
// port numbers match by construction, so whatever the test learned from
// `docker inspect` Just Works. Listeners disappear again when their
// container's port does.
package portwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/theslasher5g/devplat-cli/internal/tunnel"
)

// pollInterval trades chattiness against how quickly a freshly-started
// container's port becomes reachable. Testcontainers connects to a mapped
// port almost immediately after `start` returns (Ryuk especially), so this
// needs to be well under a second; every poll after the first reuses one
// keep-alive tunnel connection, so the steady-state cost is one tiny HTTP
// round-trip per tick, not a new WebSocket.
const pollInterval = 250 * time.Millisecond

type Watcher struct {
	apiURL    string
	token     string
	requestID string

	client *http.Client // Docker API via the local tunnel listener, keep-alive
	stop   chan struct{}
	wg     sync.WaitGroup

	mu        sync.Mutex
	listeners map[int]net.Listener
	warned    map[int]bool // ports we already logged a listen failure for
}

// New builds a watcher for one connected environment. dockerAddr is the
// CLI's own local tunnel listener ("127.0.0.1:<port>") — the same address
// DOCKER_HOST points at.
func New(apiURL, token, requestID, dockerAddr string) *Watcher {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &Watcher{
		apiURL:    apiURL,
		token:     token,
		requestID: requestID,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				// Every request goes to the tunnel listener regardless of the
				// request URL's host; keep-alive means one long-lived tunnel
				// connection serves all polls instead of one WebSocket each.
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, "tcp", dockerAddr)
				},
				MaxIdleConns:    1,
				IdleConnTimeout: 60 * time.Second,
			},
		},
		stop:      make(chan struct{}),
		listeners: map[int]net.Listener{},
		warned:    map[int]bool{},
	}
}

// Start begins polling in the background. Call Stop to end it and close
// every mirrored listener.
func (w *Watcher) Start() {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-ticker.C:
				w.reconcile()
			}
		}
	}()
}

func (w *Watcher) Stop() {
	close(w.stop)
	w.wg.Wait()
	w.mu.Lock()
	defer w.mu.Unlock()
	for port, l := range w.listeners {
		_ = l.Close()
		delete(w.listeners, port)
	}
}

// containerSummary is the slice of GET /containers/json we care about.
type containerSummary struct {
	Ports []struct {
		PublicPort int    `json:"PublicPort"`
		Type       string `json:"Type"`
	} `json:"Ports"`
}

func (w *Watcher) publishedPorts() (map[int]bool, error) {
	// The URL host is a placeholder — the transport dials the tunnel
	// listener no matter what; "docker" just needs to be a valid Host header.
	resp, err := w.client.Get("http://docker/containers/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker API returned %s", resp.Status)
	}
	var containers []containerSummary
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil, err
	}
	wanted := map[int]bool{}
	for _, c := range containers {
		for _, p := range c.Ports {
			// Docker reports one entry per bound address (0.0.0.0 and ::) —
			// the map dedupes. UDP can't ride a TCP tunnel; skip it.
			if p.Type == "tcp" && p.PublicPort > 0 {
				wanted[p.PublicPort] = true
			}
		}
	}
	return wanted, nil
}

func (w *Watcher) reconcile() {
	wanted, err := w.publishedPorts()
	if err != nil {
		// Transient tunnel/API hiccups self-heal on the next tick; existing
		// listeners stay up so in-flight test connections aren't cut.
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	for port, l := range w.listeners {
		if !wanted[port] {
			_ = l.Close() // active bridges drain on their own; only new accepts stop
			delete(w.listeners, port)
		}
	}

	for port := range wanted {
		if _, ok := w.listeners[port]; ok {
			continue
		}
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			// Most likely the port is taken locally. Warn once (stderr — the
			// TUI runs on the alt screen, same as existing tunnel warnings)
			// and retry every tick in case it frees up.
			if !w.warned[port] {
				w.warned[port] = true
				fmt.Fprintf(os.Stderr, "devplat: cannot mirror container port %d locally: %v\n", port, err)
			}
			continue
		}
		delete(w.warned, port)
		w.listeners[port] = l
		w.wg.Add(1)
		go w.acceptLoop(l, port)
	}
}

func (w *Watcher) acceptLoop(l net.Listener, port int) {
	defer w.wg.Done()
	for {
		conn, err := l.Accept()
		if err != nil {
			return // listener closed by reconcile/Stop
		}
		go func() {
			if err := tunnel.BridgePort(w.apiURL, w.token, w.requestID, port, conn); err != nil {
				fmt.Fprintf(os.Stderr, "devplat: port %d tunnel connection ended: %v\n", port, err)
			}
		}()
	}
}
