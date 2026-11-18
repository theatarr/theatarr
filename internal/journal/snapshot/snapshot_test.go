package snapshot

import (
	"encoding/json"
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

	// A public release (DMM, a Prowlarr feed) sends downloadvolumefactor=0 for every torrent: no freeleech from it,
	// while the flags its indexer names itself stay.
	rel.Privacy = policy.PrivacyPublic
	rel.Flags = []string{"seadex"}
	rel.Extras = map[string]string{"downloadvolumefactor": "0", "uploadvolumefactor": "2"}
	got, _ = Candidates([]*decide.Decision{d}, map[*decide.Decision]indexer.Release{d: rel})
	if want := []string{"seadex"}; !slices.Equal(got[0].Flags, want) {
		t.Errorf("public flags = %v, want %v", got[0].Flags, want)
	}
}

// A mixed pack's candidate keeps the episodes whose file it does not upgrade, through the stored JSON.
func TestCandidateKeepsKept(t *testing.T) {
	d := &decide.Decision{Release: "Show.S01.2160p.WEB-DL-GRP", Accepted: true, Upgrade: true,
		Episodes: []string{"S01E01", "S01E02"}, Kept: []string{"S01E02"}}
	got, _ := Candidates([]*decide.Decision{d}, map[*decide.Decision]indexer.Release{d: {Title: d.Release}})
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var back []Candidate
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || !slices.Equal(back[0].Kept, []string{"S01E02"}) {
		t.Fatalf("stored %s", b)
	}
}

func TestDropPublicVolumeFlags(t *testing.T) {
	cs := []Candidate{{Privacy: "public", Flags: []string{"freeleech", "seadex", "DoubleUpload"}},
		{Privacy: "private", Flags: []string{"freeleech", "seadex"}}}
	DropPublicVolumeFlags(cs)
	if !slices.Equal(cs[0].Flags, []string{"seadex"}) || !slices.Equal(cs[1].Flags, []string{"freeleech", "seadex"}) {
		t.Errorf("flags: %v %v", cs[0].Flags, cs[1].Flags)
	}
}
