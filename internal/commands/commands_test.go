package commands

import (
	"runtime"
	"testing"
)

func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	switch runtime.GOOS {
	case "darwin":
		t.Setenv("HOME", dir)
	case "windows":
		t.Setenv("AppData", dir)
	default:
		t.Setenv("XDG_CONFIG_HOME", dir)
	}
}

func TestHistoryDedupeAndPersist(t *testing.T) {
	isolate(t)
	proj := "/work/repo"

	s := Load()
	s.AddHistory(proj, "mvn verify")
	s.AddHistory(proj, "pytest")
	s.AddHistory(proj, "mvn verify") // re-run → moves to front, no dupe

	got := s.History(proj)
	if len(got) != 2 || got[0] != "mvn verify" || got[1] != "pytest" {
		t.Fatalf("history = %v, want [mvn verify pytest]", got)
	}

	// Reload from disk — persistence round-trips.
	s2 := Load()
	if h := s2.History(proj); len(h) != 2 || h[0] != "mvn verify" {
		t.Fatalf("reloaded history = %v", h)
	}
}

func TestSaveUnsavePerProject(t *testing.T) {
	isolate(t)
	s := Load()
	s.Save("/a", "cmd-a")
	s.Save("/a", "cmd-a") // idempotent
	s.Save("/b", "cmd-b")

	if a := s.Saved("/a"); len(a) != 1 || a[0] != "cmd-a" {
		t.Fatalf("saved /a = %v", a)
	}
	if b := s.Saved("/b"); len(b) != 1 || b[0] != "cmd-b" {
		t.Fatalf("saved /b = %v", b)
	}
	s.Unsave("/a", "cmd-a")
	if a := s.Saved("/a"); len(a) != 0 {
		t.Fatalf("after unsave, /a = %v", a)
	}
}
