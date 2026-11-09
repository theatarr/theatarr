package decide

import (
	"context"
	"strings"
	"testing"

	animemeta "github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
)

// animeMapped is anime() with franchise mappings: S1 (auto) and S2 (status given) as two entries, and a Director's
// Cut whose episode 1 is S01E01+E02 and episode 2 is S01E03.
func animeMapped(s2 string) Title {
	t := anime()
	var s1eps, s2eps []animemeta.SE
	for n := 1; n <= 12; n++ {
		s1eps = append(s1eps, animemeta.SE{1, n})
	}
	s2eps = []animemeta.SE{{2, 1}, {2, 2}}
	t.AnimeMap = animemap.New(2002, []*animemap.Entry{
		{Franchise: "isekai", Key: "al:1", Title: "Isekai Story", Names: []string{"Isekai Story"}, Status: "auto", Episodes: s1eps},
		{Franchise: "isekai", Key: "al:2", Title: "Isekai Story 2nd Season", Names: []string{"Isekai Story 2nd Season"},
			Status: s2, Reason: "the lists disagree within the margin", Episodes: s2eps},
	}, []*animemeta.AltCutMap{{Name: "Director's Cut", SeriesID: 2002, Entry: "kitsu:9",
		Episodes: map[int][]animemeta.SE{1: {{1, 1}, {1, 2}}, 2: {{1, 3}}}}})
	return t
}

func TestAnimeFranchiseMapping(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ctx := context.Background()
	v := tvVersion(e, "anime-tv")
	cases := []struct {
		name, release, s2 string
		accepted          bool
		reasons, episodes string
		how, entry, held  string
	}{
		{"entry name, conflict range", "[Group] Isekai Story 2nd Season - 02 (1080p)", "conflict", false, "MappingNotReviewed", "S02E02", "franchise", "al:2", "S02E02"},
		{"entry name, reviewed", "[Group] Isekai Story 2nd Season - 02 (1080p)", "override", true, "", "S02E02", "franchise", "al:2", ""},
		{"entry name, heuristic", "[Group] Isekai Story 2nd Season - 01 (1080p)", "heuristic", true, "", "S02E01", "franchise", "al:2", ""},
		{"entry name, rejected", "[Group] Isekai Story 2nd Season - 01 (1080p)", "rejected", false, "MappingNotReviewed", "S02E01", "franchise", "al:2", "S02E01"},
		{"sonarr absolute into a conflict", "[SubsPlease] Isekai Story - 13 (1080p) [ABCD1234].mkv", "conflict", false, "MappingNotReviewed", "S02E01", "sonarr", "", "S02E01"},
		{"entry's own numbering", "[Group] Isekai Story - 01-03 (BD 1080p)", "conflict", true, "", "S01E01,S01E02,S01E03", "franchise", "al:1", ""},
		{"batch partly held", "[Group] Isekai Story - 12-13 (BD 1080p)", "conflict", true, "", "S01E12,S02E01", "sonarr", "", "S02E01"},
		{"season pack in a conflict", "[Group] Isekai Story S02 (BD 1080p)", "conflict", false, "MappingNotReviewed", "S02E01,S02E02", "sonarr", "", "S02E01,S02E02"},
		{"alternate cut", "[Group] Isekai Story Directors Cut - 01 (BD 1080p)", "auto", true, "", "S01E01,S01E02", "alt_cut:Director's Cut", "kitsu:9", ""},
		{"alternate cut, unmapped number", "[Group] Isekai Story Directors Cut - 07 (BD 1080p)", "auto", false, "UnknownEpisode", "", "alt_cut:Director's Cut", "", ""},
		{"beyond the entry", "[Group] Isekai Story 2nd Season - 05 (1080p)", "auto", false, "UnknownEpisode", "", "", "", ""},
	}
	for _, c := range cases {
		d, err := e.Decide(ctx, rel(c.release, "public", 2*gb), animeMapped(c.s2), v, nil)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if d.Accepted != c.accepted || (c.reasons != "" && reasons(d) != c.reasons) {
			t.Errorf("%s: accepted %v, reasons %s (%v)", c.name, d.Accepted, reasons(d), d.Rejections)
		}
		if got := strings.Join(d.Episodes, ","); got != c.episodes {
			t.Errorf("%s: episodes %s, want %s", c.name, got, c.episodes)
		}
		if c.how != "" && d.AnimeMapping != c.how || c.entry != "" && d.AnimeEntry != c.entry {
			t.Errorf("%s: mapped by %q %q, want %q %q", c.name, d.AnimeMapping, d.AnimeEntry, c.how, c.entry)
		}
		if got := strings.Join(d.AnimeHeld, ","); got != c.held {
			t.Errorf("%s: held %s, want %s", c.name, got, c.held)
		}
		if c.reasons == ReasonMappingNotReviewed && !strings.Contains(d.Rejections[0].Message, "not reviewed") {
			t.Errorf("%s: message %q", c.name, d.Rejections[0].Message)
		}
	}
	// Without franchise mappings nothing changes.
	d, _ := e.Decide(ctx, rel("[SubsPlease] Isekai Story - 13 (1080p) [ABCD1234].mkv", "public", 2*gb), anime(), v, nil)
	if !d.Accepted || d.AnimeMapping != "" {
		t.Errorf("no map: %v %q", d.Accepted, d.AnimeMapping)
	}
}
