package decide

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	animemeta "github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestSeaDexEntryCoversOnlyItsCour(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ti := animeMapped("auto")
	first := *ti.AnimeMap.Entries[0]
	second := first
	first.Episodes = first.Episodes[:6]
	second.Key = "al:9"
	second.Episodes = second.Episodes[6:]
	ti.AnimeMap = animemap.New(2002, []*animemap.Entry{&first, &second}, nil)
	r := rel("Isekai Story S01 Bluray 1080p remux (entry)", "public", 12*gb)
	r.Kind = "seadex"
	r.Extras = map[string]string{"anilist": "9"}
	d, err := e.Decide(context.Background(), r, ti, tvVersion(e, "anime-tv"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.EpisodeIDs) != 6 || d.EpisodeIDs[0] != 307 || d.EpisodeIDs[5] != 312 {
		t.Fatalf("one cour claimed the whole season: %v (%v)", d.EpisodeIDs, d.Rejections)
	}
}

// A SeaDex pack whose entry's mapping is not reviewed is not narrowed to the entry's (candidate) cour: Sonarr's
// numbering takes the season, and the episodes are listed as fallbacks.
func TestSeaDexUnreviewedEntryUsesSonarrNumbering(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ti := animeMapped("auto")
	first := *ti.AnimeMap.Entries[0]
	second := first
	first.Episodes = first.Episodes[:6]
	second.Key = "al:9"
	second.Status = "conflict"
	second.Episodes = second.Episodes[6:]
	ti.AnimeMap = animemap.New(2002, []*animemap.Entry{&first, &second}, nil)
	r := rel("Isekai Story S01 Bluray 1080p remux (entry)", "public", 12*gb)
	r.Kind = "seadex"
	r.Extras = map[string]string{"anilist": "9"}
	d, err := e.Decide(context.Background(), r, ti, tvVersion(e, "anime-tv"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.EpisodeIDs) != 12 || d.AnimeMapping != AnimeMapSonarr || len(d.AnimeFallback) != 12 ||
		d.AnimeFallback[0].Entry != "al:9" || d.AnimeFallback[0].Status != "conflict" {
		t.Fatalf("unreviewed cour: %v %q %+v (%v)", d.EpisodeIDs, d.AnimeMapping, d.AnimeFallback, d.Rejections)
	}
}

// drStone is the live shape of Dr. STONE S04: one TVDB season of 37 episodes and three AniList cours mapped into it
// (12, 12 and 13 episodes).
func drStone() Title {
	t := Title{ID: 578, Kind: policy.KindSeries, Name: "Dr. STONE", Year: 2019, TVDB: 3003, SeriesType: "anime",
		Runtime: 24, Monitored: true, OriginalLanguage: "Japanese"}
	var cours [3][]animemeta.SE
	for n := 1; n <= 37; n++ {
		d := time.Date(2025, 1, 9, 15, 0, 0, 0, time.UTC).AddDate(0, 0, 7*n)
		t.Episodes = append(t.Episodes, Episode{ID: int64(400 + n), Season: 4, Number: n, Absolute: 57 + n, Title: "Episode",
			Runtime: 24, Monitored: true, AirDate: d.Format("2006-01-02"), AirDateUTC: &d})
		c := min((n-1)/12, 2)
		cours[c] = append(cours[c], animemeta.SE{4, n})
	}
	t.AnimeMap = animemap.New(3003, []*animemap.Entry{
		{Franchise: "drstone", Key: "al:172019", Names: []string{"Dr. STONE: SCIENCE FUTURE"}, Status: "auto", Episodes: cours[0]},
		{Franchise: "drstone", Key: "al:189117", Names: []string{"Dr. STONE: SCIENCE FUTURE Part 2"}, Status: "auto", Episodes: cours[1]},
		{Franchise: "drstone", Key: "al:199221", Names: []string{"Dr. STONE: SCIENCE FUTURE Part 3"}, Status: "auto", Episodes: cours[2]},
	}, nil)
	return t
}

// The seadex listing of a whole-season batch SeaDex lists under each of the season's cours covers their union: it
// passes the size check for 37 episodes, and its files ("… - NN") import to S04E01..E37. Listed under one cour only,
// the same pack still covers that cour and is too large for it.
func TestSeaDexBatchUnderSeveralCoursCoversTheSeason(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ti := drStone()
	v := tvVersion(e, "anime-tv")
	r := rel("Dr. STONE S04 Bluray 1080p (sam1)", "public", 105*gb)
	r.Kind = "seadex"
	r.Extras = map[string]string{"anilist": "172019"}
	one, err := e.Decide(context.Background(), r, ti, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(one.EpisodeIDs) != 12 || one.Accepted || reasons(one) != "AboveMaximumSize" {
		t.Fatalf("one cour: %d episodes, %s", len(one.EpisodeIDs), reasons(one))
	}
	// Without the file-list evidence (numbered 1..37) the union is not made: as one cour.
	r.Extras = map[string]string{"anilist": "172019", indexer.ExtraSeaDexEntries: "172019,189117,199221"}
	if d, err := e.Decide(context.Background(), r, ti, v, nil); err != nil || len(d.EpisodeIDs) != 12 || d.AnimeMapping != AnimeMapFranchise {
		t.Fatalf("no evidence: %v %d episodes %q", err, len(d.EpisodeIDs), d.AnimeMapping)
	}
	// Cours 2 and 3 only (episodes 13..37): files numbered 1..25 are no season numbering.
	r.Extras = map[string]string{"anilist": "189117", indexer.ExtraSeaDexEntries: "189117,199221", indexer.ExtraSeaDexNumbered: "25"}
	if d, err := e.Decide(context.Background(), r, ti, v, nil); err != nil || len(d.EpisodeIDs) != 12 || d.AnimeMapping != AnimeMapFranchise {
		t.Fatalf("later cours: %v %d episodes %q", err, len(d.EpisodeIDs), d.AnimeMapping)
	}
	r.Extras = map[string]string{"anilist": "172019", indexer.ExtraSeaDexEntries: "172019,189117,199221", indexer.ExtraSeaDexNumbered: "37"}
	d, err := e.Decide(context.Background(), r, ti, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Accepted || len(d.EpisodeIDs) != 37 || d.EpisodeIDs[0] != 401 || d.EpisodeIDs[36] != 437 || d.AnimeMapping != "season:4" {
		t.Fatalf("batch: accepted %v, %d episodes, %q (%s)", d.Accepted, len(d.EpisodeIDs), d.AnimeMapping, reasons(d))
	}
	m := d.EpisodeMapping()
	for n := 1; n <= 37; n++ {
		res, err := ResolveFile(&ti, FileInput{Path: fmt.Sprintf("[sam] Dr. STONE - Science Future - %02d (BD 1080p).mkv", n),
			Release: d.Release, OtherVideoFiles: true, Mapping: m})
		if err != nil || len(res.EpisodeIDs) != 1 || res.EpisodeIDs[0] != int64(400+n) {
			t.Errorf("file %02d: %v %s %v", n, res.EpisodeIDs, res.How, err)
		}
	}
	// The season's numbering comes before an entry the file name matches (Part 3 holds 13 episodes) and takes vN.
	res, err := ResolveFile(&ti, FileInput{Path: "[sam] Dr. STONE - Science Future Part 3 - 13v2 (BD 1080p).mkv",
		Release: d.Release, OtherVideoFiles: true, Mapping: m})
	if err != nil || len(res.EpisodeIDs) != 1 || res.EpisodeIDs[0] != 413 {
		t.Errorf("Part 3 - 13v2: %v %s %v", res.EpisodeIDs, res.How, err)
	}
}

// Reviewers' shapes: a season of two cours whose batch is numbered series-wide (13..36) or from 01 in each cour's
// folder has no 1..N evidence, so the listing is decided as before (one cour) and its files resolve as before.
func TestSeaDexBatchWithoutSeasonNumberingAsBefore(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	v := tvVersion(e, "anime-tv")
	ti := Title{ID: 579, Kind: policy.KindSeries, Name: "Dr. STONE", Year: 2019, TVDB: 3004, SeriesType: "anime",
		Runtime: 24, Monitored: true, OriginalLanguage: "Japanese"}
	var cours [3][]animemeta.SE
	for n := 1; n <= 36; n++ {
		s, num, c := 1, n, 0
		if n > 12 {
			s, num, c = 2, n-12, 1+(n-13)/12
		}
		d := time.Date(2024, 1, 4, 15, 0, 0, 0, time.UTC).AddDate(0, 0, 7*n)
		ti.Episodes = append(ti.Episodes, Episode{ID: int64(500 + n), Season: s, Number: num, Absolute: n, Title: "Episode",
			Runtime: 24, Monitored: true, AirDate: d.Format("2006-01-02"), AirDateUTC: &d})
		cours[c] = append(cours[c], animemeta.SE{s, num})
	}
	ti.AnimeMap = animemap.New(3004, []*animemap.Entry{
		{Franchise: "drstone", Key: "al:1", Names: []string{"Dr. STONE"}, Status: "auto", Episodes: cours[0]},
		{Franchise: "drstone", Key: "al:2", Names: []string{"Dr. STONE: NEW WORLD"}, Status: "auto", Episodes: cours[1]},
		{Franchise: "drstone", Key: "al:3", Names: []string{"Dr. STONE: NEW WORLD Part 2"}, Status: "auto", Episodes: cours[2]},
	}, nil)
	r := rel("Dr. STONE S02 Bluray 1080p (sam2)", "public", 20*gb)
	r.Kind = "seadex"
	r.Extras = map[string]string{"anilist": "2"}
	before, err := e.Decide(context.Background(), r, ti, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Extras = map[string]string{"anilist": "2", indexer.ExtraSeaDexEntries: "2,3"} // the indexer sets no numbering
	d, err := e.Decide(context.Background(), r, ti, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.AnimeMapping != before.AnimeMapping || d.AnimeEntry != before.AnimeEntry || len(d.EpisodeIDs) != len(before.EpisodeIDs) {
		t.Fatalf("changed: %q %q %v, before %q %q %v", d.AnimeMapping, d.AnimeEntry, d.EpisodeIDs, before.AnimeMapping,
			before.AnimeEntry, before.EpisodeIDs)
	}
	res, err := ResolveFile(&ti, FileInput{Path: "[sam] Dr. STONE - 13 (BD 1080p).mkv", Release: d.Release,
		OtherVideoFiles: true, Mapping: d.EpisodeMapping()})
	if err != nil || len(res.EpisodeIDs) != 1 || res.EpisodeIDs[0] != 513 {
		t.Errorf("series-wide 13: %v %s %v (S02E01 is 513)", res.EpisodeIDs, res.How, err)
	}
}

// With Prefer SeaDex the entries come from the stored standing too: a listing that names one entry covers the union
// of the entries the standing lists its hash under.
func TestSeaDexBatchEntriesFromStanding(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	v := tvVersion(e, "anime-tv")
	cfg := *v.Policy
	cfg.PreferSeaDex = true
	v.Policy = &cfg
	const hash = "5bf61ad8aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var snaps []indexer.SeaDexSnapshot
	for _, al := range []int{172019, 189117, 199221} {
		snaps = append(snaps, factsSnapshot(indexer.SeaDexSnapshot{AniList: al, Torrents: []indexer.SeaDexStanding{
			{RecordID: "sam1", InfoHash: hash, IsBest: true, Preference: 2, Quality: "Bluray-1080p"}}}))
	}
	r := rel("Dr. STONE S04 Bluray 1080p (sam1)", "public", 105*gb)
	r.Kind, r.InfoHash = "seadex", hash
	r.Flags = []string{indexer.FlagSeaDex, indexer.FlagSeaDexBest}
	r.Extras = map[string]string{"anilist": "172019", indexer.ExtraSeaDexNumbered: "37"}
	d, err := e.Decide(WithSeaDex(context.Background(), snaps), r, drStone(), v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Accepted || len(d.EpisodeIDs) != 37 || d.AnimeMapping != "season:4" {
		t.Fatalf("batch from standing: accepted %v, %d episodes, %q (%s)", d.Accepted, len(d.EpisodeIDs), d.AnimeMapping, reasons(d))
	}
}
