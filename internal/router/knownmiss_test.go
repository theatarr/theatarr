package router

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

// A release missed on every provider opens one failed download (and one re-search) when it is first
// missed; routing it again within miss_ttl (RSS does every sync) opens no new row and asks for no new search.
func TestKnownMissOpensNoDownload(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	for k := range e.ad {
		e.ad[k].add = refuse(adder.CodeNotCached)
	}
	for i := 0; i < 4; i++ {
		e.clock = e.clock.Add(15 * time.Minute)
		res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && (res.DownloadID != 0 || !hasStep(res, "known_miss")) {
			t.Fatalf("route %d: %+v", i, res)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE info_hash = ?`, h("a")); n != 1 {
		t.Errorf("%d download rows for a known miss", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ?`, JobResearch); n > 1 {
		t.Errorf("%d re-searches for one miss", n)
	}
	// After miss_ttl it is tried again.
	e.clock = e.clock.Add(e.r.set.MissTTL + time.Minute)
	if _, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE info_hash = ?`, h("a")); n != 2 {
		t.Errorf("%d download rows after miss_ttl", n)
	}
}

func hasStep(res *Result, code string) bool {
	for _, s := range res.Steps {
		if s.Code == code {
			return true
		}
	}
	return false
}
