// Package buildinfo reports the version a binary was built from.
//
// Release builds set Version, Commit, and Date with -ldflags "-X ...".
// Development builds fall back to the VCS information that the go command
// embeds, so `go run` and `go build` still report the commit.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

// Set by the linker in release builds.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// Info is the build identity of the running binary.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	Modified  bool   `json:"modified,omitempty"`
	GoVersion string `json:"go"`
	Platform  string `json:"platform"`
}

// Get returns the build identity, filling gaps from the embedded VCS stamp.
func Get() Info {
	info := Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = s.Value
				}
			case "vcs.time":
				if info.Date == "" {
					info.Date = s.Value
				}
			case "vcs.modified":
				info.Modified = s.Value == "true"
			}
		}
	}
	return info
}

// String is a one-line form for --version output and logs.
func (i Info) String() string {
	s := i.Version
	if i.Commit != "" {
		c := i.Commit
		if len(c) > 12 {
			c = c[:12]
		}
		s += " (" + c
		if i.Modified {
			s += "-dirty"
		}
		s += ")"
	}
	return s + " " + i.GoVersion + " " + i.Platform
}
