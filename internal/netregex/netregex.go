// Package netregex compiles the .NET-flavoured patterns that Radarr, Sonarr and the TRaSH formats use.
//
// It wraps regexp2, a port of the .NET engine. Every arr or TRaSH pattern goes through here, not only those with
// lookarounds, because .NET and Go's RE2 also differ on Unicode \d, \w and \b and on case folding.
// Never pass regexp2.RE2: it switches those semantics back to RE2's.
package netregex

import (
	"fmt"
	"time"

	"github.com/dlclark/regexp2"
)

// MatchTimeout bounds a single match. The arrs set none on most patterns, but a pattern from policy or TRaSH must
// not hang a worker. A match that times out returns an error, which callers treat as "no match" and log.
const MatchTimeout = 250 * time.Millisecond

// The .NET RegexOptions the arr code uses. RegexOptions.Compiled has no equivalent and needs none: it only changes
// how .NET executes a pattern, not what it matches.
const (
	None                    = regexp2.None
	IgnoreCase              = regexp2.IgnoreCase
	Multiline               = regexp2.Multiline
	ExplicitCapture         = regexp2.ExplicitCapture
	Singleline              = regexp2.Singleline
	IgnorePatternWhitespace = regexp2.IgnorePatternWhitespace
	RightToLeft             = regexp2.RightToLeft
)

// Compile compiles expr with .NET semantics and the package match timeout.
func Compile(expr string, opts regexp2.RegexOptions) (*regexp2.Regexp, error) {
	if opts&regexp2.RE2 != 0 {
		return nil, fmt.Errorf("netregex: RE2 option not allowed for %q", expr)
	}
	re, err := regexp2.Compile(expr, opts)
	if err != nil {
		return nil, err
	}
	re.MatchTimeout = MatchTimeout
	return re, nil
}

// MustCompile is Compile for the package-level pattern tables ported from the arrs. It panics on a bad pattern, so a
// porting mistake fails at start-up and in every test.
func MustCompile(expr string, opts regexp2.RegexOptions) *regexp2.Regexp {
	re, err := Compile(expr, opts)
	if err != nil {
		panic(fmt.Sprintf("netregex: MustCompile(%q): %v", expr, err))
	}
	return re
}
