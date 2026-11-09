package shadow

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
)

// seadexRSSEnv is the RSS environment with the SeaDex feed ready: its recent feed is empty, every entry answers empty.
func seadexRSSEnv(t *testing.T, prefer bool, entry http.HandlerFunc) *env {
	e := newEnv(t, false)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.net.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if entry != nil && r.URL.Query().Get("sort") != "-updated" {
			entry(w, r)
			return
		}
		fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":0,"totalPages":0,"items":[]}`)
	}))
	e.pol.Versions["tv"].Indexers = append(e.pol.Versions["tv"].Indexers, "seadex")
	e.pol.Versions["tv"].PreferSeaDex = prefer
	e.build(nil)
	e.prow.setRSS()
	return e
}

func seadexInfo(t *testing.T, rs *RSSSummary) RSSIndexer {
	t.Helper()
	for _, ix := range rs.Indexers {
		if ix.Indexer == "seadex" {
			return ix
		}
	}
	t.Fatalf("no seadex feed in %+v", rs.Indexers)
	return RSSIndexer{}
}

type stubPlan struct{}

func (stubPlan) Route(_ context.Context, r router.Request) (*router.Result, error) {
	c := r.Candidates[0]
	return &router.Result{PlanID: 1, Steps: []router.Step{{Hash: indexer.HashFromMagnet(c.Release.Magnet.URL()), Release: c.Release.Title, Outcome: "would_add"}}}, nil
}
func (stubPlan) Enqueue(context.Context, router.Request, time.Time) (int64, error) { return 0, nil }

// Shadow matches the router's plan by the release's normalised identity: a hashless release is matched
// by its magnet's hash; a release whose reported hash conflicts with its magnet is never a pick.
func TestMagnetIdentityShadowPick(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reported string
		pick     bool
	}{{"", true}, {strings.Repeat("a", 40), false}} {
		e := newEnv(t, false)
		e.r.rt = stubPlan{}
		for _, q := range []string{`INSERT INTO title(id,kind,name,year,created_at,updated_at) VALUES(1,'movie','Film',2024,0,0)`,
			`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(1,1,'movie-remux',0,0)`} {
			if _, err := e.st.Writer().Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		rel := indexer.Release{Title: "Film.2024.1080p.BluRay-GRP", InfoHash: tc.reported, Magnet: indexer.BareMagnet(strings.Repeat("b", 40))}
		d := &decide.Decision{Accepted: true}
		c := candidate{title: decide.Title{ID: 1, Kind: policy.KindMovie}, version: decide.Version{Key: "movie-remux", ID: 1}}
		g := &activeGroup{title: c.title, version: c.version, rels: map[*decide.Decision]indexer.Release{d: rel}, picks: map[*decide.Decision]candidate{d: c}}
		sum := &RSSSummary{}
		if err := e.r.planRSS(e.ctx, sum, g, []*decide.Decision{d}, router.Request{Candidates: []router.Candidate{{Release: rel}}, Version: "movie-remux"}); err != nil {
			t.Fatal(err)
		}
		if got := sum.Picks > 0; got != tc.pick {
			t.Fatalf("reported %q: pick %v, want %v", tc.reported, got, tc.pick)
		}
	}
}

// A shadow grab replayed without a search decides the arr's release against stored standing.
func TestShadowGrabWithoutSearchUsesStanding(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.pol.Versions["movie-remux"].PreferSeaDex = true
	p := e.eng.Profile(e.pol.Versions["movie-remux"].Profile, format.Radarr)
	cf, err := format.ParseTrashFormat(format.Radarr, []byte(`{"trash_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"Best","specifications":[{"name":"Best","implementation":"SeaDexSpecification","required":true,"fields":{"value":"best"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	cf.ID = 77
	p.Formats = append(p.Formats, cf)
	p.Profile.FormatItems = append(p.Profile.FormatItems, format.FormatItem{Format: 77, Score: 5000})
	e.zurg.add(grabEvent(2, dune, t0.Add(-time.Hour), duneRemux, hDuneRemux, "Remux-2160p", 31, 1950, "Rss"))
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	var tid int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM title WHERE name = ?`, dune.title).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`INSERT INTO title_external_id VALUES(?,'anilist','1')`, tid); err != nil {
		t.Fatal(err)
	}
	body := `{"anilist":1,"torrents":[{"info_hash":"` + hDuneRemux + `","preference":2,"is_best":true,"facts":{"isBest":true}}]}`
	if _, err := e.st.Writer().Exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','1',200,?,1,9999999999999)`, body); err != nil {
		t.Fatal(err)
	}
	res, err := e.r.GrabByID(e.ctx, "radarr_zurg", 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Searched {
		t.Fatal("bad setup: the replay searched")
	}
	best := false
	for _, f := range res.Arr.Formats {
		best = best || f.Name == "Best"
	}
	if !best {
		t.Fatalf("the arr's release was decided without stored standing: %+v", res.Arr)
	}
}
