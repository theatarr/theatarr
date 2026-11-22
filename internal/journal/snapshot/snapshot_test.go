package snapshot

import (
	"encoding/json"
	"slices"
	"strings"
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

// A candidate says whether its name reads as two or more seasons: a span does, a season pack and an absolute batch
// across a season break do not (the field is left out).
func TestCandidateMultiSeason(t *testing.T) {
	for _, c := range []struct {
		name string
		want bool
	}{
		{"Show.S01-S03.2160p.WEB-DL-GRP", true},
		{"Show.Complete.Series.2160p.WEB-DL-GRP", true},
		{"Show.S02.2160p.WEB-DL-GRP", false},
		{"[Group] Show - 12-13 [1080p]", false},
	} {
		d := &decide.Decision{Release: c.name, Accepted: true, Episodes: []string{"S01E12", "S02E01"}, EpisodeIDs: []int64{112, 201}}
		got, _ := Candidates([]*decide.Decision{d}, map[*decide.Decision]indexer.Release{d: {Title: d.Release}})
		b, _ := json.Marshal(got[0])
		if got[0].MultiSeason != c.want || !c.want && strings.Contains(string(b), "multi_season") {
			t.Errorf("%s: multi_season %v, want %v: %s", c.name, got[0].MultiSeason, c.want, b)
		}
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

// TestCandidatesOver: each kept accepted release records why it ranks before the next accepted torrent (the
// comparer's step and that release's facts), skipping another indexer's copy of itself; none for the last one or a
// rejected one.
func TestCandidatesOver(t *testing.T) {
	steps := map[[2]string]string{}
	saved := firstDifference
	firstDifference = func(a, b *decide.Decision) string { return steps[[2]string{a.Release, b.Release}] }
	t.Cleanup(func() { firstDifference = saved })
	remux := &decide.Decision{Release: "A.2160p.REMUX-X", Accepted: true, Quality: "Remux-2160p", Score: 900}
	copyOf := &decide.Decision{Release: "A.2160p.REMUX-X", Accepted: true, Quality: "Remux-2160p", Score: 900}
	web := &decide.Decision{Release: "A.2160p.WEB-Y", Accepted: true, Quality: "WEBDL-2160p", Score: 450,
		Formats: []decide.FormatScore{{Name: "DV", Score: 100}}}
	bad := &decide.Decision{Release: "A.CAM-Z", Accepted: false}
	steps[[2]string{remux.Release, web.Release}] = "quality"
	rels := map[*decide.Decision]indexer.Release{remux: {InfoHash: "AA"}, copyOf: {InfoHash: "aa", Indexer: "other"},
		web: {InfoHash: "bb", Seeders: 30, Size: 9, CachedOn: []string{"rd"}}, bad: {InfoHash: "cc"}}
	got, _ := Candidates([]*decide.Decision{remux, copyOf, web, bad}, rels)
	o := got[0].Over
	if o == nil || o.Release != web.Release || o.Step != "quality" || o.Quality != "WEBDL-2160p" || o.Score != 450 ||
		!slices.Equal(o.Formats, []string{"DV"}) || o.Seeders != 30 || !slices.Equal(o.CachedOn, []string{"rd"}) {
		t.Fatalf("over %+v", o)
	}
	if got[2].Over != nil || got[3].Over != nil {
		t.Errorf("the last accepted one and a rejected one: %+v %+v", got[2].Over, got[3].Over)
	}
	// A season's search: another episode's release is no alternative.
	e1 := &decide.Decision{Release: "S.S01E01.WEB-X", Accepted: true, Episodes: []string{"S01E01"}}
	e2 := &decide.Decision{Release: "S.S01E02.WEB-X", Accepted: true, Episodes: []string{"S01E02"}}
	e1b := &decide.Decision{Release: "S.S01E01.HDTV-Y", Accepted: true, Episodes: []string{"S01E01"}}
	steps[[2]string{e1.Release, e2.Release}] = "episode_number"
	steps[[2]string{e1.Release, e1b.Release}] = "quality"
	got, _ = Candidates([]*decide.Decision{e1, e2, e1b}, map[*decide.Decision]indexer.Release{e1: {InfoHash: "e1"}, e2: {InfoHash: "e2"}, e1b: {InfoHash: "e3"}})
	if o := got[0].Over; o == nil || o.Release != e1b.Release || o.Step != "quality" {
		t.Errorf("season search over %+v", o)
	}
}
