package dockerapi

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestContainers_ParsesAndDedupesPorts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/json" {
			http.NotFound(w, r)
			return
		}
		// One container, its 5432 port bound on both 0.0.0.0 and :: (Docker
		// lists it twice), plus a UDP port that must be ignored.
		w.Write([]byte(`[{"Id":"abc","Names":["/pg"],"Image":"postgres:16","State":"running",
			"Ports":[{"PrivatePort":5432,"PublicPort":54321,"Type":"tcp"},
			         {"PrivatePort":5432,"PublicPort":54321,"Type":"tcp"},
			         {"PrivatePort":9999,"PublicPort":40000,"Type":"udp"}]}]`))
	}))
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	cs, err := c.Containers(context.Background())
	if err != nil {
		t.Fatalf("Containers: %v", err)
	}
	if len(cs) != 1 || cs[0].Name != "pg" || cs[0].Image != "postgres:16" || cs[0].State != "running" {
		t.Fatalf("unexpected container: %+v", cs)
	}
	if len(cs[0].Ports) != 1 || cs[0].Ports[0].Public != 54321 {
		t.Fatalf("expected one deduped tcp port 54321, got %+v", cs[0].Ports)
	}
}

func frame(stream byte, payload string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, []byte(payload)...)
}

func TestDemuxLogs(t *testing.T) {
	// Two framed chunks (stdout then stderr) → concatenated payloads.
	raw := append(frame(1, "hello\n"), frame(2, "warn\n")...)
	if got := DemuxLogs(raw); got != "hello\nwarn\n" {
		t.Fatalf("framed: got %q", got)
	}
	// TTY output (no valid framing) is returned verbatim.
	if got := DemuxLogs([]byte("plain tty line\n")); got != "plain tty line\n" {
		t.Fatalf("tty: got %q", got)
	}
}
