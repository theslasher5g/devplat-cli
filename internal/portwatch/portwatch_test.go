package portwatch

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeDocker serves GET /containers/json with one running container that
// publishes the given TCP port, mimicking what the tunneled Docker API
// returns. The Watcher dials it as if it were the local Docker tunnel.
func fakeDocker(t *testing.T, publicPort int) net.Addr {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/json" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `[{"Ports":[{"PublicPort":%d,"Type":"tcp"}]}]`, publicPort)
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr()
}

// waitForListener returns once the watcher has opened a local mirror for
// port, or fails the test after timeout.
func waitForListener(t *testing.T, w *Watcher, port int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		_, ok := w.listeners[port]
		w.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("watcher never mirrored port %d", port)
}

// TestStopWithActiveListenerDoesNotDeadlock is the regression test for the
// Stop() ordering bug: with a live mirrored listener (whose acceptLoop is
// parked in Accept), Stop() must close listeners before waiting on the
// goroutines, or it hangs forever.
func TestStopWithActiveListenerDoesNotDeadlock(t *testing.T) {
	// A free ephemeral port to advertise as "published", so the local mirror
	// bind won't collide with anything.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	dockerAddr := fakeDocker(t, port).String()
	w := New("http://api.example", "dvp_test", "req_test", dockerAddr)
	w.Start()
	waitForListener(t, w, port)

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() deadlocked with an active mirrored listener")
	}

	// After Stop the mirror must be gone (port bindable again).
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("mirror listener still open after Stop: %v", err)
	}
	l.Close()
}

// TestStopWithoutListenersReturns covers the trivial path (no container ever
// published a port) — Stop must still return promptly.
func TestStopWithoutListenersReturns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(srv.Close)

	w := New("http://api.example", "dvp_test", "req_test", srv.Listener.Addr().String())
	w.Start()
	time.Sleep(400 * time.Millisecond) // let a couple of polls happen

	done := make(chan struct{})
	go func() { w.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() hung with no listeners")
	}
}
