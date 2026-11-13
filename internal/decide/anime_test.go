package decide

import (
	"context"
	"strconv"
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
		how, entry        string
		fallback          string
	}{
		{"entry name, conflict range", "[Group] Isekai Story 2nd Season - 02 (1080p)", "conflict", true, "", "S01E02", "sonarr", "al:2", "S01E02 al:2 conflict"},
		{"entry name, reviewed", "[Group] Isekai Story 2nd Season - 02 (1080p)", "override", true, "", "S02E02", "franchise", "al:2", ""},
		{"entry name, heuristic", "[Group] Isekai Story 2nd Season - 01 (1080p)", "heuristic", true, "", "S02E01", "franchise", "al:2", ""},
		{"entry name, rejected", "[Group] Isekai Story 2nd Season - 01 (1080p)", "rejected", true, "", "S01E01", "sonarr", "al:2", "S01E01 al:2 rejected"},
		{"sonarr absolute into a conflict", "[SubsPlease] Isekai Story - 13 (1080p) [ABCD1234].mkv", "conflict", true, "", "S02E01", "sonarr", "", "S02E01 al:2 conflict"},
		{"entry's own numbering", "[Group] Isekai Story - 01-03 (BD 1080p)", "conflict", true, "", "S01E01,S01E02,S01E03", "franchise", "al:1", ""},
		{"batch partly in a conflict", "[Group] Isekai Story - 12-13 (BD 1080p)", "conflict", true, "", "S01E12,S02E01", "sonarr", "", "S02E01 al:2 conflict"},
		{"season pack in a conflict", "[Group] Isekai Story S02 (BD 1080p)", "conflict", true, "", "S02E01,S02E02", "sonarr", "", "S02E01 al:2 conflict,S02E02 al:2 conflict"},
		{"season pack in a rejected range", "[Group] Isekai Story S02 (BD 1080p)", "rejected", true, "", "S02E01,S02E02", "sonarr", "", "S02E01 al:2 rejected,S02E02 al:2 rejected"},
		{"alternate cut", "[Group] Isekai Story Directors Cut - 01 (BD 1080p)", "auto", true, "", "S01E01,S01E02", "alt_cut:Director's Cut", "kitsu:9", ""},
		{"alternate cut, unmapped number", "[Group] Isekai Story Directors Cut - 07 (BD 1080p)", "auto", false, "UnknownEpisode", "", "alt_cut:Director's Cut", "kitsu:9", ""},
		// An entry that cannot hold the number leaves it to Sonarr, which places the known series' absolute 5.
		{"beyond the entry", "[Group] Isekai Story 2nd Season - 05 (1080p)", "auto", true, "", "S01E05", "sonarr", "", ""},
		{"beyond a conflicted entry", "[Group] Isekai Story 2nd Season - 05 (1080p)", "conflict", true, "", "S01E05", "sonarr", "al:2", "S01E05 al:2 conflict"},
		{"beyond a rejected entry", "[Group] Isekai Story 2nd Season - 05 (1080p)", "rejected", true, "", "S01E05", "sonarr", "al:2", "S01E05 al:2 rejected"},
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
		if c.how != "" && d.AnimeMapping != c.how || d.AnimeEntry != c.entry {
			t.Errorf("%s: mapped by %q %q, want %q %q", c.name, d.AnimeMapping, d.AnimeEntry, c.how, c.entry)
		}
		var fb []string
		for _, f := range d.AnimeFallback {
			fb = append(fb, f.Episode+" "+f.Entry+" "+f.Status)
		}
		if got := strings.Join(fb, ","); got != c.fallback {
			t.Errorf("%s: fallback %q, want %q", c.name, got, c.fallback)
		}
	}
	// Without franchise mappings nothing changes.
	d, _ := e.Decide(ctx, rel("[SubsPlease] Isekai Story - 13 (1080p) [ABCD1234].mkv", "public", 2*gb), anime(), v, nil)
	if !d.Accepted || d.AnimeMapping != "" {
		t.Errorf("no map: %v %q", d.Accepted, d.AnimeMapping)
	}
}

// A release named after an entry whose mapping is not importable takes Sonarr's numbering, never the entry's
// (best-scoring) candidate; the decision lists each episode as a fallback with the entry and its status.
func TestAnimeUnreviewedEntryUsesSonarrNumbering(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ctx := context.Background()
	v := tvVersion(e, "anime-tv")
	var candidate []animemeta.SE
	for n := 4; n <= 12; n++ {
		candidate = append(candidate, animemeta.SE{1, n}) // the candidate puts the entry's episode 1 at S01E04
	}
	for _, c := range []struct {
		status, episodes, how, fallback string
	}{
		{"conflict", "S01E01,S01E02,S01E03", "sonarr", "S01E01 al:1 conflict,S01E02 al:1 conflict,S01E03 al:1 conflict"},
		{"rejected", "S01E01,S01E02,S01E03", "sonarr", "S01E01 al:1 rejected,S01E02 al:1 rejected,S01E03 al:1 rejected"},
		{"auto", "S01E04,S01E05,S01E06", "franchise", ""},
	} {
		ti := anime()
		ti.AnimeMap = animemap.New(2002, []*animemap.Entry{{Franchise: "isekai", Key: "al:1", Title: "Isekai Story",
			Names: []string{"Isekai Story"}, Status: c.status, Episodes: candidate}}, nil)
		d, err := e.Decide(ctx, rel("[Group] Isekai Story - 01-03 (BD 1080p)", "public", 2*gb), ti, v, nil)
		if err != nil {
			t.Fatal(err)
		}
		var fb []string
		for _, f := range d.AnimeFallback {
			fb = append(fb, f.Episode+" "+f.Entry+" "+f.Status)
		}
		if !d.Accepted || strings.Join(d.Episodes, ",") != c.episodes || d.AnimeMapping != c.how || strings.Join(fb, ",") != c.fallback {
			t.Errorf("%s: accepted %v %v, episodes %v, mapped by %q, fallback %v", c.status, d.Accepted, d.Rejections,
				d.Episodes, d.AnimeMapping, fb)
		}
	}
}

// A release named after an entry whose mapping gives no placement (not importable, or the number is beyond its
// range) imports through Sonarr's numbering, so the decision must place it where the import's file resolution does.
// A fallback keeps the entry it named on the download's mapping, also when the entry's candidate range cannot hold
// the number (review may pick another). It is never stale: the import reconciles it once its entry is importable
// and places its files (library/resolve.go).
func TestAnimeFallbackMatchesFileResolution(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ctx := context.Background()
	v := tvVersion(e, "anime-tv")
	for _, c := range []struct {
		status, release, episodes string
	}{
		{"conflict", "[Group] Isekai Story 2nd Season - 05 (1080p)", "S01E05"},
		{"rejected", "[Group] Isekai Story 2nd Season - 05 (1080p)", "S01E05"},
		{"conflict", "[Group] Isekai Story 2nd Season - 01-02 (BD 1080p)", "S01E01,S01E02"},
		{"rejected", "[Group] Isekai Story 2nd Season - 02 (1080p)", "S01E02"},
	} {
		ti := animeMapped(c.status)
		d, err := e.Decide(ctx, rel(c.release, "public", 2*gb), ti, v, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !d.Accepted || strings.Join(d.Episodes, ",") != c.episodes {
			t.Errorf("%s %s: accepted %v %v, episodes %v, want %s", c.status, c.release, d.Accepted, d.Rejections,
				d.Episodes, c.episodes)
		}
		file, err := ResolveFile(&ti, FileInput{Path: c.release + ".mkv", Release: c.release})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(int64s(file.EpisodeIDs), ",") != strings.Join(int64s(d.EpisodeIDs), ",") {
			t.Errorf("%s %s: decision %v, file resolution %v", c.status, c.release, d.EpisodeIDs, file.EpisodeIDs)
		}
		m := d.EpisodeMapping()
		if m == nil || *m != (EpisodeMap{How: AnimeMapFranchise, Entry: "al:2", Fallback: true}) {
			t.Errorf("%s %s: mapping %+v, want the named entry as a fallback", c.status, c.release, m)
			continue
		}
		if m.Stale(ti.AnimeMap) || m.Stale(animeMapped("override").AnimeMap) {
			t.Errorf("%s %s: a fallback is stale", c.status, c.release)
		}
	}
}

func int64s(ids []int64) []string {
	var out []string
	for _, id := range ids {
		out = append(out, strconv.FormatInt(id, 10))
	}
	return out
}
