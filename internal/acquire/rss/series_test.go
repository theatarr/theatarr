package rss

import (
	"fmt"
	"strings"
	"testing"
)

func containsReq(reqs []string, part string) bool {
	for _, r := range reqs {
		if strings.Contains(r, part) {
			return true
		}
	}
	return false
}

// An RSS item in a TV category is matched to a series by a scene mapping name and routed with its episodes; the feed
// is asked for movies and TV.
func TestRSSSeriesBySceneName(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.seed(t)
	e.seedShow(t)
	e.prow.setRSS(item{title: "Show.Alt.S01E03.2160p.WEB-DL-GRP", hash: hShowE03, size: 8e9, pub: t0, tv: true})
	rs, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Routed != 1 || len(e.rec.reqs) != 1 {
		t.Fatalf("rss %+v", rs)
	}
	if req := e.rec.reqs[0]; req.Version != "tv" || len(req.Candidates[0].Episodes) != 1 {
		t.Errorf("route %+v", req)
	}
	if reqs := e.net.requests("prowlarr.test/1/api"); !containsReq(reqs, "cat=2000%2C5000") {
		t.Errorf("rss categories %v", reqs)
	}
}

func TestRSSCategoriesIncludeAnimeForConfiguredAnimeVersion(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	anime := true
	e.r.pol.Versions["tv"].Anime = &anime
	if got := e.r.rssCategories("prowlarr"); fmt.Sprint(got) != "[2000 5000 5070]" {
		t.Fatalf("RSS categories %v", got)
	}
}
