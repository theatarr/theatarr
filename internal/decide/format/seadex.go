package format

// SeaDexSpecification: our own condition, not an arr port. The arrs score SeaDex picks through a Torznab
// proxy such as seadexerr that tags its feed; we know SeaDex directly (internal/indexer, kind seadex), so the release carries its
// SeaDex standing and a format condition reads it.

import (
	"fmt"
	"strings"
)

// SeaDexRank is a release's standing on SeaDex (releases.moe) for the title it is decided for.
type SeaDexRank int

const (
	// SeaDexNone: SeaDex does not list the release (or it is unknown).
	SeaDexNone SeaDexRank = iota
	// SeaDexAlt: SeaDex lists the release as an alternative, not marked best.
	SeaDexAlt
	// SeaDexBest: SeaDex marks the release best.
	SeaDexBest
)

func (r SeaDexRank) String() string {
	switch r {
	case SeaDexAlt:
		return "alt"
	case SeaDexBest:
		return "best"
	}
	return "none"
}

// SeaDex values: which standings the condition matches.
const (
	SeaDexValueBest = "best" // marked best
	SeaDexValueAlt  = "alt"  // listed, not marked best
	SeaDexValueAny  = "any"  // listed, best or not
)

// SeaDex matches the release's SeaDex standing (Input.SeaDex). Value is best, alt or any. Negate works as for every
// condition: a negated "any" matches releases SeaDex does not list.
type SeaDex struct{ Value string }

// NewSeaDex checks the value.
func NewSeaDex(value string) (*SeaDex, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	switch v {
	case SeaDexValueBest, SeaDexValueAlt, SeaDexValueAny:
		return &SeaDex{Value: v}, nil
	}
	return nil, fmt.Errorf("SeaDexSpecification value %q: want best, alt or any", value)
}

func (*SeaDex) Implementation() string { return "SeaDexSpecification" }
func (c *SeaDex) satisfied(in *Input) (bool, error) {
	switch c.Value {
	case SeaDexValueBest:
		return in.SeaDex == SeaDexBest, nil
	case SeaDexValueAlt:
		return in.SeaDex == SeaDexAlt, nil
	case SeaDexValueAny:
		return in.SeaDex != SeaDexNone, nil
	}
	return false, nil
}
