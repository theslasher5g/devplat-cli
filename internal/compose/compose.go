// Package compose detects host-path bind mounts in a docker-compose file.
// They're the classic remote-Docker footgun with devplat: `./data:/x` mounts
// a path on the REMOTE VM (where the daemon runs), not the developer's laptop,
// so the files silently aren't there. The TUI warns once when it spots them.
package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

var fileNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// BindMount is one detected host-path mount, for a human-readable warning.
type BindMount struct {
	Service string
	Source  string
	Target  string
}

func (b BindMount) String() string {
	return fmt.Sprintf("%s: %s → %s", b.Service, b.Source, b.Target)
}

type composeFile struct {
	Services map[string]struct {
		Volumes []yaml.Node `yaml:"volumes"`
	} `yaml:"services"`
}

// Detect finds a compose file in dir and returns its host-path bind mounts
// (empty if there's no compose file or only named volumes). A parse error is
// returned so the caller can stay silent rather than warn on bad input.
func Detect(dir string) ([]BindMount, error) {
	var path string
	for _, name := range fileNames {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			path = p
			break
		}
	}
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cf composeFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, err
	}

	var out []BindMount
	for svc, s := range cf.Services {
		for _, v := range s.Volumes {
			if m := bindFromNode(svc, v); m != nil {
				out = append(out, *m)
			}
		}
	}
	return out, nil
}

func bindFromNode(svc string, n yaml.Node) *BindMount {
	switch n.Kind {
	case yaml.ScalarNode: // short syntax: "SRC:DST[:mode]"
		parts := strings.SplitN(n.Value, ":", 3)
		if len(parts) < 2 {
			return nil
		}
		src := parts[0]
		if !isHostPath(src) {
			return nil // a named volume, not a bind mount
		}
		return &BindMount{Service: svc, Source: src, Target: parts[1]}
	case yaml.MappingNode: // long syntax: {type: bind, source: …, target: …}
		var long struct {
			Type   string `yaml:"type"`
			Source string `yaml:"source"`
			Target string `yaml:"target"`
		}
		if err := n.Decode(&long); err != nil {
			return nil
		}
		if long.Type == "bind" || isHostPath(long.Source) {
			return &BindMount{Service: svc, Source: long.Source, Target: long.Target}
		}
	}
	return nil
}

// isHostPath distinguishes a bind-mount source (a path) from a named volume (a
// bare identifier). Paths are absolute, relative (./ or ../), home (~), or
// otherwise contain a slash or a Windows drive.
func isHostPath(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, ".") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") {
		return true
	}
	if strings.Contains(s, "/") {
		return true
	}
	// Windows drive path like C:\… (only reachable via long syntax since the
	// short syntax splits on ":").
	if len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		return true
	}
	return false
}
