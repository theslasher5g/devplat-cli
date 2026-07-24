package versioncheck

import "testing"

func TestOutdated(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v1.0.0", "v1.0.1", true},
		{"v1.0.0", "v1.1.0", true},
		{"v1.0.0", "v2.0.0", true},
		{"1.0.0", "v1.0.1", true},   // missing v prefix on current
		{"v1.0.1", "v1.0.0", false}, // newer than latest
		{"v1.2.3", "v1.2.3", false}, // equal
		{"dev", "v1.2.3", false},    // unversioned local build never nags
		{"v1.0.0", "", false},       // failed lookup
		{"", "v1.0.0", false},
		{"garbage", "v1.0.0", false},
		{"v1.10.0", "v1.9.0", false}, // numeric, not lexical, compare
		{"v1.9.0", "v1.10.0", true},
	}
	for _, c := range cases {
		if got := Outdated(c.current, c.latest); got != c.want {
			t.Errorf("Outdated(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}
