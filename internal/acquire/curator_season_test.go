package acquire

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/store"
)

// An episode a curated season pack makes wanted is searched by season, even when it is the only wanted episode
// of its season: the curator's release is the season pack, which an episode search rejects.
func TestCuratorWantedEpisodeSearchesSeason(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	cutoffFixture(t, e)
	e.pol.Versions["series"].PreferSeaDex = true
	for _, q := range []string{
		`UPDATE title SET runtime_minutes = 24 WHERE id = 10`,
		`UPDATE episode SET runtime_minutes = 24 WHERE title_id = 10`,
		`UPDATE version_file SET quality = 'WEBDL-2160p' WHERE id = 4`, // only the curator makes anything wanted
		`INSERT INTO title_external_id VALUES (10, 'anilist', '1535')`,
	} {
		e.exec(q)
	}
	sel := Selection{Versions: []string{"series"}, Only: OnlyCutoff, QualityCutoff: true}
	targets := func() []string {
		t.Helper()
		ts, err := e.a.seriesTargets(e.ctx, e.st.Reader(), 201, 10, "series", sel, store.Millis(e.now))
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, x := range ts {
			names = append(names, x.name)
		}
		return names
	}
	if got := targets(); len(got) != 0 {
		t.Fatalf("control: no curated alternative, searches %v", got)
	}
	snapshot := indexer.SeaDexSnapshot{AniList: 1535, Torrents: []indexer.SeaDexStanding{{InfoHash: strings.Repeat("f", 40),
		Quality: "WEBDL-2160p", Preference: 3, IsBest: true, RecordID: "abcdef012345678", Size: 4 << 30}}}
	if err := json.Unmarshal([]byte(`{"facts":{"id":"abcdef012345678","tracker":"Nyaa","isBest":true,"files":[
		{"name":"Show.S01E01.2160p.WEB-DL.mkv","length":2147483648},{"name":"Show.S01E02.2160p.WEB-DL.mkv","length":2147483648}]}}`),
		&snapshot.Torrents[0]); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(snapshot)
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','1535',200,?,0,253402214400000)`, body)
	if got := strings.Join(targets(), " "); got != "s01" {
		t.Fatalf("curator-wanted S01E02 searched as %q, want the season search s01", got)
	}
}

// The season target a curated pack makes reaches the search criteria: a season search for season 1
// with no episode, both from seriesSearch and on the search the queued run actually sends.
func TestCuratorWantedEpisodeSeasonCriteria(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	cutoffFixture(t, e)
	e.pol.Versions["series"].PreferSeaDex = true
	for _, q := range []string{
		`UPDATE title SET runtime_minutes = 24 WHERE id = 10`,
		`UPDATE episode SET runtime_minutes = 24 WHERE title_id = 10`,
		`UPDATE version_file SET quality = 'WEBDL-2160p' WHERE id = 4`,
		`INSERT INTO title_external_id VALUES (10, 'anilist', '1535')`,
	} {
		e.exec(q)
	}
	snapshot := indexer.SeaDexSnapshot{AniList: 1535, Torrents: []indexer.SeaDexStanding{{InfoHash: strings.Repeat("f", 40),
		Quality: "WEBDL-2160p", Preference: 3, IsBest: true, RecordID: "abcdef012345678", Size: 4 << 30}}}
	if err := json.Unmarshal([]byte(`{"facts":{"id":"abcdef012345678","tracker":"Nyaa","isBest":true,"files":[
		{"name":"Show.S01E01.2160p.WEB-DL.mkv","length":2147483648},{"name":"Show.S01E02.2160p.WEB-DL.mkv","length":2147483648}]}}`),
		&snapshot.Torrents[0]); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(snapshot)
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','1535',200,?,0,253402214400000)`, body)
	ts, err := e.a.seriesTargets(e.ctx, e.st.Reader(), 201, 10, "series", Selection{Versions: []string{"series"},
		Only: OnlyCutoff, QualityCutoff: true}, store.Millis(e.now))
	if err != nil || len(ts) != 1 || len(ts[0].episodes) != 1 {
		t.Fatalf("fixture: targets %+v %v", ts, err)
	}
	title, err := decide.LoadTitle(e.ctx, e.st.Reader(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := decide.LoadSeriesVersion(e.ctx, e.st.Reader(), &title, 201); err != nil {
		t.Fatal(err)
	}
	if ss := seriesSearch(&title, ts[0]); ss.Kind != profile.SearchSeason || ss.Season != 1 || ss.Episode != 0 {
		t.Fatalf("curated pack target %s %v: criteria %+v, want a season search for season 1", ts[0].name, ts[0].episodes, ss)
	}
	if err := e.a.startRuns(e.ctx, SourceWanted, 0, ts[:1]); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if len(e.calls) != 1 || e.calls[0].Season != 1 || e.calls[0].Ep != 0 {
		t.Fatalf("queued run searched %+v, want season 1 with no episode", e.calls)
	}
}
