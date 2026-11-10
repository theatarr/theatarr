package settings

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// A version's grab switch is a confirmed change when it lets an active version grab (turned on, or a
// version activated with it on); turning it off, or setting it on a shadow version, needs nothing. It is stored
// with the version's DB settings.
func TestVersionGrabNeedsConfirmation(t *testing.T) {
	t.Parallel()
	p, err := policy.Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	p.Versions["movie-web"].Mode = policy.ModeActive
	grabs := func(before, after *policy.Policy) bool {
		t.Helper()
		effects, err := versionConfirmationEffects(context.Background(), nil, before, after)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range effects {
			if e.Version == "movie-web" && strings.Contains(e.Effect, "Allow downloads for this library (grab) is on for active library movie-web") {
				return true
			}
		}
		return false
	}
	on, _ := p.Clone()
	on.Versions["movie-web"].Grab = true
	if !grabs(p, on) {
		t.Fatal("grab turned on for an active version without confirmation")
	}
	if grabs(on, p) {
		t.Fatal("grab turned off asked for confirmation")
	}
	shadow, _ := p.Clone()
	shadow.Versions["movie-web"].Mode = policy.ModeShadow
	shadowOn, _ := shadow.Clone()
	shadowOn.Versions["movie-web"].Grab = true
	if grabs(shadow, shadowOn) {
		t.Fatal("grab on a shadow version asked for confirmation")
	}
	if !grabs(shadowOn, on) {
		t.Fatal("activating a version with grab on did not name the grab")
	}

	raw, err := versionConfigJSON(*on.Versions["movie-web"], nil)
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := versionConfigFromJSON(raw)
	if err != nil || !back.Grab {
		t.Fatalf("grab not stored: %s %v", raw, err)
	}
	raw, _ = versionConfigJSON(*p.Versions["movie-web"], nil)
	if strings.Contains(string(raw), "grab") {
		t.Fatalf("grab off stored: %s", raw)
	}
}
