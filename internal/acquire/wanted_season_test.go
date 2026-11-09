package acquire

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// D: a season 2 entry's curated torrent is no cutoff reason for a season 1 episode.
func TestSeasonTwoStandingDoesNotWantSeasonOne(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 100})
	cutoffFixture(t, e)
	e.pol.Versions["series"].PreferSeaDex = true
	if _, err := e.st.Writer().Exec(`UPDATE title SET tvdb_id=123 WHERE id=10`); err != nil {
		t.Fatal(err)
	}
	s := indexer.SeaDexSnapshot{AniList: 2, Targets: []indexer.AniListTarget{{TVDB: 123, Season: 2}}, Torrents: []indexer.SeaDexStanding{{InfoHash: strings.Repeat("b", 40), Preference: 3, Quality: "WEBDL-1080p"}}}
	body, _ := json.Marshal(s)
	if _, err := e.st.Writer().Exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','2',200,?,0,253402214400000)`, body); err != nil {
		t.Fatal(err)
	}
	got, err := e.a.EpisodesBelowQualityCutoff(e.ctx, "series", 201, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got[1002] {
		t.Fatalf("cutoff-quality S01E02 targeted for S02-only alternative: %+v", got)
	}
}

// D: neither the full nor the quality-only cutoff takes another season's alternatives.
func TestWantedDoesNotUseOtherSeason(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 100})
	cutoffFixture(t, e)
	e.pol.Versions["series"].PreferSeaDex = true
	snapshot := indexer.SeaDexSnapshot{AniList: 222, Targets: []indexer.AniListTarget{{TVDB: 99, Season: 2}}, Torrents: []indexer.SeaDexStanding{{InfoHash: strings.Repeat("a", 40), Quality: "WEBDL-2160p", Preference: 3}}}
	body, _ := json.Marshal(snapshot)
	if _, err := e.st.Writer().Exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','222',200,?,0,253402214400000)`, body); err != nil {
		t.Fatal(err)
	}
	e.a.eng.Profile("p", format.Sonarr).Profile.CutoffFormatScore = 0
	full, err := e.a.episodesBelowCutoff(e.ctx, "series", 201, 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if full[1002] {
		t.Errorf("full cutoff used season 2 for season 1: %v", full)
	}
	got, err := e.a.EpisodesBelowQualityCutoff(e.ctx, "series", 201, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got[1002] {
		t.Fatalf("season 1 episode marked curator-upgradable using season 2 snapshot: %v", got)
	}
}
