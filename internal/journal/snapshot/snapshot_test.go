package snapshot

import (
	"slices"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestCandidatesCarrySourceFlagsPeers(t *testing.T) {
	d := &decide.Decision{Release: "Movie.2024.1080p.WEB-DL-GRP", Accepted: true, ProfileRank: 2}
	rel := indexer.Release{Indexer: "tl", Kind: "cardigann", Title: d.Release, Privacy: policy.PrivacyPrivate,
		Flags: []string{"freeleech"}, Seeders: 10, Peers: 12}
	got, _ := Candidates([]*decide.Decision{d}, map[*decide.Decision]indexer.Release{d: rel})
	if len(got) != 1 {
		t.Fatalf("candidates = %d, want 1", len(got))
	}
	c := got[0]
	if c.Source != "cardigann" || c.Privacy != "private" || !slices.Equal(c.Flags, []string{"freeleech"}) ||
		c.QualityRank != 2 || c.Leechers == nil || *c.Leechers != 2 {
		t.Fatalf("candidate = %+v (leechers %v)", c, c.Leechers)
	}

	// No peer count: no leechers. Volume factors give the flags the decision engine reads from them.
	rel.Peers = -1
	rel.Flags = []string{"internal"}
	rel.Extras = map[string]string{"downloadvolumefactor": "0", "uploadvolumefactor": "2"}
	got, _ = Candidates([]*decide.Decision{d}, map[*decide.Decision]indexer.Release{d: rel})
	c = got[0]
	if c.Leechers != nil {
		t.Errorf("leechers = %d, want none", *c.Leechers)
	}
	if want := []string{"internal", "freeleech", "doubleupload"}; !slices.Equal(c.Flags, want) {
		t.Errorf("flags = %v, want %v", c.Flags, want)
	}
	if !slices.Equal(rel.Flags, []string{"internal"}) {
		t.Errorf("release flags changed: %v", rel.Flags)
	}

	// A freeleech factor with the freeleech tag already there is not doubled; a half-leech factor gives halfleech.
	rel.Flags = []string{"Freeleech"}
	rel.Extras = map[string]string{"downloadvolumefactor": "0"}
	got, _ = Candidates([]*decide.Decision{d}, map[*decide.Decision]indexer.Release{d: rel})
	if want := []string{"Freeleech"}; !slices.Equal(got[0].Flags, want) {
		t.Errorf("flags = %v, want %v", got[0].Flags, want)
	}
	rel.Flags = nil
	rel.Extras = map[string]string{"downloadvolumefactor": "0.5"}
	got, _ = Candidates([]*decide.Decision{d}, map[*decide.Decision]indexer.Release{d: rel})
	if want := []string{"halfleech"}; !slices.Equal(got[0].Flags, want) {
		t.Errorf("flags = %v, want %v", got[0].Flags, want)
	}
}
