// Package dockerapi is a tiny read-only Docker Engine API client the TUI uses
// to show what's running in the remote VM. It talks to the same local tunnel
// listener DOCKER_HOST points at (see internal/portwatch for the same
// transport trick), so it rides the existing tunnel — no new connection type.
package dockerapi

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	http *http.Client
}

// New builds a client that always dials dockerAddr ("127.0.0.1:<port>", the
// CLI's local tunnel listener) regardless of the request URL host, reusing one
// keep-alive connection.
func New(dockerAddr string) *Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &Client{http: &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp", dockerAddr)
			},
			MaxIdleConns: 2, IdleConnTimeout: 60 * time.Second,
		},
	}}
}

// Container is the slice of GET /containers/json the TUI renders.
type Container struct {
	ID    string
	Name  string
	Image string
	State string // "running", "exited", …
	Ports []Port // published TCP ports (mirrored locally on the same number)
}

type Port struct {
	Private int
	Public  int
	Type    string
}

type rawContainer struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
	Image string   `json:"Image"`
	State string   `json:"State"`
	Ports []struct {
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
}

// Containers lists all containers (running and stopped) in the remote VM.
func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/json?all=1", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker API returned %s", resp.Status)
	}
	var raw []rawContainer
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(raw))
	for _, r := range raw {
		c := Container{ID: r.ID, Image: r.Image, State: r.State, Name: trimName(r.Names)}
		// De-dupe published ports (Docker lists one per bound address).
		seen := map[int]bool{}
		for _, p := range r.Ports {
			if p.Type == "tcp" && p.PublicPort > 0 && !seen[p.PublicPort] {
				seen[p.PublicPort] = true
				c.Ports = append(c.Ports, Port{Private: p.PrivatePort, Public: p.PublicPort, Type: p.Type})
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func trimName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

// Logs returns the last `tail` lines of a container's combined stdout/stderr,
// de-multiplexed from Docker's stream framing.
func (c *Client) Logs(ctx context.Context, id string, tail int) (string, error) {
	url := "http://docker/containers/" + id + "/logs?stdout=1&stderr=1&tail=" + strconv.Itoa(tail)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("docker API returned %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // cap at 1 MB
	if err != nil {
		return "", err
	}
	return DemuxLogs(raw), nil
}

// DemuxLogs strips Docker's multiplexed-stream framing. For a non-TTY
// container each chunk is an 8-byte header [stream(1), 0,0,0, size(4 BE)]
// followed by `size` payload bytes. A TTY container has no framing — detected
// by a first "header" that isn't a valid frame — and is returned as-is.
// Exported and header-only so it's unit-testable without a live daemon.
func DemuxLogs(raw []byte) string {
	var b strings.Builder
	i := 0
	for i+8 <= len(raw) {
		streamType := raw[i]
		size := int(binary.BigEndian.Uint32(raw[i+4 : i+8]))
		// A valid frame has stream type 0/1/2 and the payload fits. If not,
		// this isn't framed output (TTY mode) — return the remainder verbatim.
		if streamType > 2 || i+8+size > len(raw) {
			b.Write(raw[i:])
			return b.String()
		}
		b.Write(raw[i+8 : i+8+size])
		i += 8 + size
	}
	if i < len(raw) {
		b.Write(raw[i:])
	}
	return b.String()
}
