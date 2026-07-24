// Package commands persists, per project directory, the shell commands run in
// the TUI (history) and the ones the user explicitly stars (saved), so they
// can be recalled and re-run. Stored as a single JSON file under the OS
// config dir — not secret, so plain 0644.
package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const maxHistory = 100

type project struct {
	History []string `json:"history"` // most-recent first, de-duplicated
	Saved   []string `json:"saved"`   // starred, insertion order
}

// Store is safe for concurrent use by the TUI (input goroutine + renders).
type Store struct {
	mu   sync.Mutex
	path string
	data map[string]*project // keyed by project dir
}

func filePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "devplat", "commands.json"), nil
}

// Load reads the store (empty if none/parse error — command history is
// best-effort and must never block a session).
func Load() *Store {
	s := &Store{data: map[string]*project{}}
	p, err := filePath()
	if err != nil {
		return s
	}
	s.path = p
	if raw, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(raw, &s.data)
		if s.data == nil {
			s.data = map[string]*project{}
		}
	}
	return s
}

func (s *Store) proj(dir string) *project {
	if s.data[dir] == nil {
		s.data[dir] = &project{}
	}
	return s.data[dir]
}

func (s *Store) persist() {
	if s.path == "" {
		return
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return
	}
	// 0600 / 0700, not world-readable: command history can contain secrets a
	// user typed inline (e.g. `TOKEN=… mvn verify`), same reasoning as a shell
	// history file — don't expose it to other local users.
	_ = os.MkdirAll(filepath.Dir(s.path), 0o700)
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

// AddHistory records a run command at the front, de-duplicating and capping.
func (s *Store) AddHistory(dir, cmd string) {
	if cmd == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.proj(dir)
	p.History = dedupeFront(p.History, cmd)
	if len(p.History) > maxHistory {
		p.History = p.History[:maxHistory]
	}
	s.persist()
}

func (s *Store) History(dir string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.proj(dir).History...)
}

// Save stars a command (idempotent).
func (s *Store) Save(dir, cmd string) {
	if cmd == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.proj(dir)
	for _, c := range p.Saved {
		if c == cmd {
			return
		}
	}
	p.Saved = append(p.Saved, cmd)
	s.persist()
}

func (s *Store) Unsave(dir, cmd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.proj(dir)
	out := p.Saved[:0]
	for _, c := range p.Saved {
		if c != cmd {
			out = append(out, c)
		}
	}
	p.Saved = out
	s.persist()
}

func (s *Store) Saved(dir string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.proj(dir).Saved...)
}

// dedupeFront moves cmd to the front, removing any earlier occurrence.
func dedupeFront(list []string, cmd string) []string {
	out := make([]string, 0, len(list)+1)
	out = append(out, cmd)
	for _, c := range list {
		if c != cmd {
			out = append(out, c)
		}
	}
	return out
}
