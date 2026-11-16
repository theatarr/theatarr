package router

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// A release missed on every provider opens one failed download (and one re-search) when it is first
// missed; routing it again within miss_ttl (RSS does every sync) opens no new row and asks for no new search.
func TestKnownMissOpensNoDownload(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
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

// MissUntil answers from the stored cache evidence alone: known while every provider has a live miss (or an uncached
// row under cached_only), until the first of those rows lapses.
func TestMissUntil(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	db, now, hash := e.r.st.Writer(), e.clock, h("m")
	at := func(d time.Duration) time.Time { return now.Add(d).Truncate(time.Millisecond) }
	put := func(pk, state string, exp time.Time) {
		if _, err := db.ExecContext(e.ctx, `INSERT OR REPLACE INTO provider_cache (info_hash, provider, state, source,
			checked_at, expires_at) VALUES (?, ?, ?, 'test', ?, ?)`, hash, pk, state, store.Millis(now), store.Millis(exp)); err != nil {
			t.Fatal(err)
		}
	}
	ask := func(cachedOnly bool, when time.Time, providers ...string) (time.Time, bool) {
		until, known, err := MissUntil(e.ctx, db, providers, hash, cachedOnly, when)
		if err != nil {
			t.Fatal(err)
		}
		return until, known
	}
	// The owner's case: rd and ad missed until late, tb uncached (a cache check) until early.
	put("rd", "miss", at(6*time.Hour))
	put("ad", "miss", at(6*time.Hour))
	put("tb", "uncached", at(29*time.Minute))
	if until, known := ask(true, now, "rd", "ad", "tb"); !known || !until.Equal(at(29*time.Minute)) {
		t.Errorf("owner's case: %v %v", until, known)
	}
	if _, known := ask(true, at(30*time.Minute), "rd", "ad", "tb"); known {
		t.Error("known after the uncached row lapsed")
	}
	if _, known := ask(false, now, "rd", "ad", "tb"); known {
		t.Error("an uncached row counted with cached_only off")
	}
	if _, known := ask(true, now, "rd", "ad", "tb", "pm"); known {
		t.Error("known with a provider that has no row")
	}
	if _, known := ask(true, at(7*time.Hour), "rd", "ad"); known {
		t.Error("known with every row expired")
	}
	if _, known := ask(true, now); known {
		t.Error("known without providers")
	}
}
