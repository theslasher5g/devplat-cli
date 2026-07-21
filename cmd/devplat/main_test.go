package main

import "testing"

// TestRunExecCommand covers the --exec contract CI depends on: the child's
// exit code becomes devplat's, and DOCKER_HOST is exported to the command.
func TestRunExecCommand(t *testing.T) {
	if code := runExecCommand("exit 0", "/bin/sh", "tcp://127.0.0.1:12345"); code != 0 {
		t.Errorf("exit 0: got exit code %d, want 0", code)
	}

	// A failing test command must fail the CI step — exit code propagates.
	if code := runExecCommand("exit 7", "/bin/sh", "tcp://127.0.0.1:12345"); code != 7 {
		t.Errorf("exit 7: got exit code %d, want 7", code)
	}

	// The command sees DOCKER_HOST — this is the whole point of --exec, and
	// exactly what a backgrounded interactive `connect` failed to provide.
	script := `[ "$DOCKER_HOST" = "tcp://127.0.0.1:52731" ] && exit 0 || exit 9`
	if code := runExecCommand(script, "/bin/sh", "tcp://127.0.0.1:52731"); code != 0 {
		t.Errorf("DOCKER_HOST not visible to command: got exit code %d, want 0", code)
	}
}
