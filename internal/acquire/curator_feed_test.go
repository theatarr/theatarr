package acquire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/router"
)

type loopbackGateway struct{}

func (loopbackGateway) Do(ctx context.Context, _ gateway.Target, req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req.WithContext(ctx))
}
func (loopbackGateway) PrivateFor(context.Context, gateway.Target) bool { return false }

// The season search for a curated pack target stays a season query through the Torznab planner:
// the feed is not asked for S01E02, and the curated pack it returns is not rejected as FullSeason.
func TestCuratorPackSeasonReachesFeed(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	cutoffFixture(t, e)
	e.pol.Versions["series"].PreferSeaDex = true
	for _, q := range []string{
		`UPDATE title SET runtime_minutes=24 WHERE id=10`,
		`UPDATE episode SET runtime_minutes=24 WHERE title_id=10`,
		`UPDATE version_file SET quality='WEBDL-2160p' WHERE id=4`,
		`INSERT INTO title_external_id VALUES(10,'anilist','1535')`,
	} {
		e.exec(q)
	}
	snap := indexer.SeaDexSnapshot{AniList: 1535, Torrents: []indexer.SeaDexStanding{{InfoHash: strings.Repeat("f", 40), Quality: "WEBDL-2160p", Preference: 3, IsBest: true, RecordID: "abcdef012345678", Size: 4 << 30}}}
	if err := json.Unmarshal([]byte(`{"facts":{"id":"abcdef012345678","tracker":"Nyaa","isBest":true,"files":[{"name":"Show.S01E01.2160p.WEB-DL.mkv","length":2147483648},{"name":"Show.S01E02.2160p.WEB-DL.mkv","length":2147483648}]}}`), &snap.Torrents[0]); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(snap)
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','1535',200,?,0,253402214400000)`, body)
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		q := r.URL.Query()
		if q.Get("t") == "caps" {
			fmt.Fprint(w, `<caps><limits default="100" max="100"/><searching><search available="yes" supportedParams="q"/><tv-search available="yes" supportedParams="q,season,ep,tvdbid,imdbid"/></searching></caps>`)
			return
		}
		mu.Lock()
		queries = append(queries, q.Encode())
		mu.Unlock()
		fmt.Fprint(w, `<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><item><title>Show.S01.2160p.WEB-DL-GRP</title><guid>pack</guid><size>4294967296</size><torznab:attr name="infohash" value="ffffffffffffffffffffffffffffffffffffffff"/><torznab:attr name="seeders" value="50"/></item></channel></rss>`)
	}))
	defer srv.Close()
	e.pol.Indexers["pub"].URL = srv.URL + "/api"
	idx, err := indexer.New(e.pol, loopbackGateway{}, indexer.Options{CacheTTL: -1})
	if err != nil {
		t.Fatal(err)
	}
	idx.SetEvidence(indexer.NewEvidence(indexer.EvidenceConfig{Store: e.st, Policy: e.pol}))
	var got *router.Searched
	var criteria *decide.SeriesSearch
	e.a.search = func(ctx context.Context, title decide.Title, v decide.Version, existing *decide.ExistingFile, ss *decide.SeriesSearch) (*router.Searched, error) {
		criteria = ss
		var err error
		got, err = router.SearchRequest(decide.WithSearch(ctx), e.a.eng, idx, title, v, existing, ss)
		return got, err
	}
	n, _, err := e.a.Submit(e.ctx, 0, Selection{Versions: []string{"series"}, Only: OnlyCutoff, QualityCutoff: true})
	if err != nil || n != 1 {
		t.Fatalf("submit n=%d err=%v", n, err)
	}
	e.drain()
	mu.Lock()
	t.Logf("target=%s criteria=%+v requests=%q", e.runs(), criteria, queries)
	mu.Unlock()
	if got == nil {
		t.Fatal("search did not run")
	}
	for _, d := range got.Ranked {
		t.Logf("release=%s accepted=%v rejections=%+v", d.Release, d.Accepted, d.Rejections)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, q := range queries {
		if strings.Contains(q, "ep=2") {
			t.Errorf("season query was turned into an episode query: %s", q)
		}
	}
	for _, d := range got.Ranked {
		for _, rejection := range d.Rejections {
			if rejection.Reason == "FullSeason" {
				t.Errorf("season pack rejected after season target: %+v", d.Rejections)
			}
		}
	}
}
