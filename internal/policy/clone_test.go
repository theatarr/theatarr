package policy

import (
	"reflect"
	"testing"
)

func TestClone(t *testing.T) {
	t.Parallel()
	p, err := Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c, p) {
		t.Fatal("clone changed policy or source paths")
	}
	c.Providers["rd"].Pacing.AddsPerMinute++
	c.Versions["movie-web"].Indexers[0] = "different"
	c.origin["providers[rd]"] = "different"
	if c.Providers["rd"].Pacing == p.Providers["rd"].Pacing || c.Versions["movie-web"].Indexers[0] == p.Versions["movie-web"].Indexers[0] || c.origin["providers[rd]"] == p.origin["providers[rd]"] {
		t.Fatal("clone shares mutable fields")
	}
}

func TestCloneCarriesForcedOff(t *testing.T) {
	t.Parallel()
	p := &Policy{ForcedOff: []string{"router", "notify"}}
	copy, err := p.Clone()
	if err != nil {
		t.Fatal(err)
	}
	copy.ForcedOff[0] = "library"
	if p.ForcedOff[0] != "router" || copy.ForcedOff[1] != "notify" {
		t.Fatal("forced-off state was lost or shared")
	}
}
