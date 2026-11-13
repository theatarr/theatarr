package indexer

import (
	"context"
	"encoding/json"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestSuccessfulSnapshotReplacesRetiredStanding(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Test',0,0); INSERT INTO title_external_id VALUES(1,'anilist','1535')`); err != nil {
		t.Fatal(err)
	}
	e := NewEvidence(EvidenceConfig{Store: st, Policy: testPolicy()})
	now := time.Unix(100000, 0)
	e.now = func() time.Time { return now }
	first := SeaDexSnapshot{AniList: 1535, Torrents: []SeaDexStanding{{RecordID: "6vdej49pr26gca0", InfoHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Preference: 3}}}
	read := func() SeaDexSnapshot {
		t.Helper()
		var raw []byte
		if err := st.Reader().QueryRow(`SELECT body FROM metadata_cache WHERE source='seadex-standing' AND key='1535'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var got SeaDexSnapshot
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	e.recordSeaDex(ctx, []SeaDexSnapshot{first})
	var fetched int64
	if err := st.Reader().QueryRow(`SELECT fetched_at FROM metadata_cache WHERE source='seadex-standing' AND key='1535'`).Scan(&fetched); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	e.recordSeaDex(ctx, []SeaDexSnapshot{first})
	var unchanged int64
	if err := st.Reader().QueryRow(`SELECT fetched_at FROM metadata_cache WHERE source='seadex-standing' AND key='1535'`).Scan(&unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged != fetched {
		t.Fatal("unchanged snapshot rewritten")
	}
	e.recordSeaDex(ctx, []SeaDexSnapshot{{AniList: 1535}})
	if got := read(); got.Torrents[0].Preference != 3 || got.Torrents[0].MissingSince == 0 {
		t.Fatalf("first omission retired standing: %+v", got)
	}
	now = now.Add(SeaDexRetirementDelay - time.Millisecond)
	e.recordSeaDex(ctx, []SeaDexSnapshot{{AniList: 1535}})
	if read().Torrents[0].Preference != 3 {
		t.Fatal("retired before confirmation interval")
	}
	now = now.Add(time.Millisecond)
	e.recordSeaDex(ctx, []SeaDexSnapshot{{AniList: 1535}})
	if read().Torrents[0].Preference != 0 {
		t.Fatal("confirmed omission not retired")
	}
	e.recordSeaDex(ctx, []SeaDexSnapshot{first})
	if got := read(); got.Torrents[0].MissingSince != 0 || got.Torrents[0].Preference != 3 {
		t.Fatal("reappearance did not clear retirement")
	}
	e.recordSeaDex(ctx, []SeaDexSnapshot{{AniList: 999}})
	var n int
	_ = st.Reader().QueryRow(`SELECT COUNT(*) FROM metadata_cache WHERE source='seadex-standing'`).Scan(&n)
	if n != 1 {
		t.Fatal("unmapped snapshot persisted")
	}

}

func TestSeaDexDoesNotInventMissingQualityDimensions(t *testing.T) {
	for _, name := range []string{"Show - 01 [BD].mkv", "Show - 01 [1080p].mkv"} {
		var tor sdTorrent
		body, _ := json.Marshal(map[string]any{"files": []any{map[string]any{"name": name, "length": 1000}}})
		if err := json.Unmarshal(body, &tor); err != nil {
			t.Fatal(err)
		}
		parsed := sonarr.ParseQualityName(name)
		got := seadexQuality(&tor)
		t.Logf("%s: %+v source_detection=%v resolution_detection=%v", name, got, parsed.SourceDetectionSource, parsed.ResolutionDetectionSource)
		if got != sonarr.QualityUnknown {
			t.Errorf("incomplete feature evidence is treated as known: %s", got.Name)
		}
	}
}

// SeaDex reads a file's quality with the series parser (v4plus): a BD file is Bluray, and WEB with EAC3 is WEB-DL by
// name, where the v4 base found no source in the name and the quality stayed unknown.
func TestSeaDexQualityReadsV4Plus(t *testing.T) {
	for name, want := range map[string]sonarr.Quality{
		"Show S01E01 BD 1080p.mkv":                          sonarr.QualityBluray1080p,
		"Show.S01E01.2160p.HDR.WEB.EAC3.Atmos.H265-GRP.mkv": sonarr.QualityWEBDL2160p,
	} {
		var tor sdTorrent
		body, _ := json.Marshal(map[string]any{"files": []any{map[string]any{"name": name, "length": 1000}}})
		if err := json.Unmarshal(body, &tor); err != nil {
			t.Fatal(err)
		}
		if got := seadexQuality(&tor); got != want {
			t.Errorf("%s: %s, want %s", name, got.Name, want.Name)
		}
	}
}

func TestSeaDexRSSAndSearchUseSamePreference(t *testing.T) {
	n := newNet(t)
	// Best is a 1080p single-audio encode. The scoring table deliberately prefers the 720p dual-audio alternative.
	body := `{"items":[{"alID":113415,"trs":["best","dual"],"expand":{"trs":[{"id":"best","infoHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","tracker":"Nyaa","isBest":true,"files":[{"name":"Show S01E01 BD 1080p.mkv","length":3000000000},{"name":"Show S01E02 BD 1080p.mkv","length":3000000000}]},{"id":"dual","infoHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","tracker":"Nyaa","dualAudio":true,"files":[{"name":"Show S01E01 BD 720p.mkv","length":2000000000},{"name":"Show S01E02 BD 720p.mkv","length":2000000000}]}]}}]}`
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	p := testPolicy()
	best := 1
	p.Indexers["seadex"].SeaDex = &policy.SeaDexScoring{Best: &best, DualAudio: 100}
	h := newHarness(t, n, p, false)
	h.svc.SetMetadata(Metadata{Targets: fakeTargets, Names: fakeNames{}})
	searchResult := search(t, h, "seadex", Query{Type: QuerySeries, AniList: 113415, TVDB: 377543, Title: "JUJUTSU KAISEN", Season: 1})
	rssResult := search(t, h, "seadex", Query{Type: QueryRSS})
	if len(searchResult.Releases) != 2 || len(rssResult.Releases) != 2 {
		t.Fatalf("bad setup: search=%+v rss=%+v", searchResult, rssResult)
	}
	prefs := map[string]int{}
	for _, r := range searchResult.Releases {
		prefs[r.InfoHash] = SeaDexPreference(&r)
		wantSeeders := 10
		if r.Extras["seadex_id"] == "dual" {
			wantSeeders = 1000
		}
		if r.Seeders != wantSeeders {
			t.Errorf("search seeder signal changed: %+v", r)
		}
	}
	for _, r := range rssResult.Releases {
		wantSeeders := 10
		if r.Extras["seadex_id"] == "best" {
			wantSeeders = 1000
		}
		if r.Seeders != wantSeeders {
			t.Errorf("RSS seeder signal changed: %+v", r)
		}
		if prefs[r.InfoHash] != SeaDexPreference(&r) {
			t.Errorf("%s preference changes between search (%d) and RSS (%d)", r.Extras["seadex_id"], prefs[r.InfoHash], SeaDexPreference(&r))
		}
	}
	_ = context.Background()
}

func TestSeaDexBatchDoesNotMutateCachedMetadata(t *testing.T) {
	source := Release{InfoHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Flags: []string{FlagSeaDex, FlagSeaDexPreferred}, Extras: map[string]string{"anilist": "1535", "seadex_id": "6vdej49pr26gca0"}}
	ordinary := Release{InfoHash: source.InfoHash, Extras: map[string]string{"original": "kept"}, Flags: []string{"scene"}}
	cached := &Result{Releases: []Release{source, ordinary}}
	one := cached.clone()
	MarkSeaDex(one.Releases)
	if cached.Releases[1].Extras["anilist"] != "" || len(cached.Releases[1].Flags) != 1 {
		t.Fatal("batch preference propagation changed cached input")
	}
	if one.Releases[1].Extras["anilist"] != "1535" || SeaDexPreference(&one.Releases[1]) != 3 {
		t.Fatal("copy lost verified provenance")
	}
}
