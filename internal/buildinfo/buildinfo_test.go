package buildinfo

import (
	"strings"
	"testing"
)

func TestGetDefaults(t *testing.T) {
	info := Get()
	if info.Version != "dev" {
		t.Errorf("Version = %q, want dev", info.Version)
	}
	if !strings.HasPrefix(info.GoVersion, "go") {
		t.Errorf("GoVersion = %q", info.GoVersion)
	}
	if !strings.Contains(info.Platform, "/") {
		t.Errorf("Platform = %q", info.Platform)
	}
}

func TestStringShortensCommit(t *testing.T) {
	s := Info{Version: "v1.2.3", Commit: "0123456789abcdef", Modified: true, GoVersion: "go1.26", Platform: "linux/amd64"}.String()
	want := "v1.2.3 (0123456789ab-dirty) go1.26 linux/amd64"
	if s != want {
		t.Errorf("String() = %q, want %q", s, want)
	}
}
