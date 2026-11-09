// Package buildinfo reports the version of the running binary.
package buildinfo

import (
	"fmt"
	"runtime/debug"
)

// Version is stamped at build time:
//
//	go build -ldflags "-X github.com/theatarr/theatarr/internal/buildinfo.Version=v0.1.0" ./cmd/theatarr
var Version = "dev"

// String returns the version plus the VCS revision Go recorded, e.g. "dev (3f2a9c1, modified)".
func String() string {
	rev, modified := "", false
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
	}
	if rev == "" {
		return Version
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if modified {
		return fmt.Sprintf("%s (%s, modified)", Version, rev)
	}
	return fmt.Sprintf("%s (%s)", Version, rev)
}
