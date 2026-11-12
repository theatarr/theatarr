package rss

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/indexer/lookup"
	"github.com/theatarr/theatarr/internal/policy"
)

// Through the RSS sync and its decisions: an expanded record entry 5001 does not list is
// never filed under The Show nor decided or picked, for prefer-on and prefer-off versions alike; the listed one is.
func TestRSSUnlistedSeaDexRecordNotPicked(t *testing.T) {
	t.Parallel()
	const listed, unlisted = "5555555555555555555555555555555555555555", "7777777777777777777777777777777777777777"
	feed := `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[
 {"alID":5001,"trs":["sdx0000000001ab"],"expand":{"trs":[
  {"id":"sdx0000000001ab","url":"https://nyaa.si/view/501","infoHash":"` + listed + `",
   "isBest":true,"dualAudio":false,"tags":[],"tracker":"Nyaa","releaseGroup":"Grp","created":"2026-10-26 11:00:00.000Z",
   "updated":"2026-10-26 11:00:00.000Z","files":[{"length":9000000000,"name":"[Grp] The Show - 01 (BD 1080p).mkv"},
   {"length":9000000000,"name":"[Grp] The Show - 02 (BD 1080p).mkv"},{"length":9000000000,"name":"[Grp] The Show - 03 (BD 1080p).mkv"}]},
  {"id":"sdx0000000003ef","url":"https://nyaa.si/view/503","infoHash":"` + unlisted + `",
   "isBest":true,"dualAudio":true,"tags":[],"tracker":"Nyaa","releaseGroup":"Other","created":"2026-10-26 11:30:00.000Z",
   "updated":"2026-10-26 11:30:00.000Z","files":[{"length":9500000000,"name":"[Other] Another Show - 01 (BD 1080p).mkv"},
   {"length":9500000000,"name":"[Other] Another Show - 02 (BD 1080p).mkv"},{"length":9500000000,"name":"[Other] Another Show - 03 (BD 1080p).mkv"}]}]}}
]}`
	for _, prefer := range []bool{false, true} {
		t.Run(fmt.Sprintf("prefer=%v", prefer), func(t *testing.T) {
			e := newEnv(t, false)
			e.seed(t)
			e.seedShow(t)
			e.net.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("sort") != "-updated" {
					fmt.Fprint(w, `{"items":[]}`)
					return
				}
				fmt.Fprint(w, feed)
			}))
			e.pol.Versions["tv"].Indexers = append(e.pol.Versions["tv"].Indexers, "seadex")
			e.pol.Versions["tv"].PreferSeaDex = prefer
			tvp := e.pol.Profiles["tv-2160p"]
			tvp.Qualities = append(tvp.Qualities, policy.QualityItem{Name: "Bluray-1080p"})
			tvp.Sizes = map[string]policy.SizeLimit{"Bluray-1080p": {Min: 4}}
			eng, err := decide.New(e.pol, decide.Options{Now: e.clock})
			if err != nil {
				t.Fatal(err)
			}
			e.eng = eng
			db := lookup.DB{DB: e.st.Reader()}
			e.svc.SetMetadata(indexer.Metadata{Names: db, Targets: indexer.FirstAniListTargets(db,
				lookupTargets{5001: {{TVDB: 1001, Season: 1}}})})
			e.build(nil)
			e.prow.setRSS()
			rs, err := e.r.RSS(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if sd := seadexInfo(t, rs); sd.Items != 1 {
				t.Fatalf("seadex feed items %+v, want only the listed record", sd)
			}
			if e.rec.routed(unlisted) {
				t.Fatalf("the unlisted record was routed for The Show")
			}
			if !e.rec.routed(listed) {
				t.Fatalf("the listed record was not routed (rss %+v)", rs)
			}
		})
	}
}

// A SeaDex feed whose standing cannot be stored (a prefer-on version reads it, so the search
// fails) still counts the upstream request it made.
func TestFailedStandingWriteCountedInRSS(t *testing.T) {
	t.Parallel()
	e := seadexRSSEnv(t, true, nil)
	e.net.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":500,"trs":["pick1"],"expand":{"trs":[{"id":"pick1","infoHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","tracker":"Nyaa","isBest":true,"files":[{"name":"Show BD 720p.mkv","length":3000000000}]}]}}]}`)
	}))
	for _, q := range []string{`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(900,'movie','Film',0,0)`,
		`INSERT INTO title_external_id VALUES(900,'anilist','500')`,
		`CREATE TRIGGER fail_standing BEFORE INSERT ON metadata_cache WHEN NEW.source='seadex-standing' BEGIN SELECT RAISE(ABORT,'injected persistence failure'); END`} {
		if _, err := e.st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	e.svc.SetEvidence(indexer.NewEvidence(indexer.EvidenceConfig{Store: e.st, Policy: e.pol, Now: e.clock}))
	rs, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := seadexInfo(t, rs); got.Error == "" || got.Requests != 1 {
		t.Fatalf("failed standing write hid the feed's upstream request: %+v (sent %d)", got, len(e.net.requests("seadex.test")))
	}
	if rs.Requests < 1 {
		t.Fatalf("sync request total %d leaves out the failed feed's request", rs.Requests)
	}
}
