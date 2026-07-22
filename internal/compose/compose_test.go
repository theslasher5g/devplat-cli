package compose

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetect(t *testing.T) {
	dir := t.TempDir()
	yml := `services:
  db:
    image: postgres:16
    volumes:
      - ./data:/var/lib/postgresql/data
      - pgdata:/var/lib/postgresql/backups
      - /etc/localtime:/etc/localtime:ro
  app:
    image: myapp
    volumes:
      - type: bind
        source: ./src
        target: /app/src
      - type: volume
        source: cache
        target: /cache
volumes:
  pgdata:
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	mounts, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	// Expect: db ./data, db /etc/localtime, app ./src (bind). NOT pgdata or
	// cache (named volumes).
	got := map[string]bool{}
	for _, m := range mounts {
		got[m.Source] = true
	}
	for _, want := range []string{"./data", "/etc/localtime", "./src"} {
		if !got[want] {
			t.Errorf("expected bind mount %q, got %v", want, mounts)
		}
	}
	for _, notWant := range []string{"pgdata", "cache"} {
		if got[notWant] {
			t.Errorf("named volume %q should not be flagged", notWant)
		}
	}
	if len(mounts) != 3 {
		t.Errorf("expected 3 bind mounts, got %d: %v", len(mounts), mounts)
	}
}

func TestDetect_NoComposeFile(t *testing.T) {
	mounts, err := Detect(t.TempDir())
	if err != nil || mounts != nil {
		t.Fatalf("expected (nil,nil) with no compose file, got (%v,%v)", mounts, err)
	}
}
