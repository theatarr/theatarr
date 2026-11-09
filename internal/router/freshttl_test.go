package router

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// A miss on new content (an episode aired within the fresh window) lives an hour: the uncached add and the
// all-provider miss. Old content keeps miss_ttl, and so does a 451.
func TestFreshContentMissLivesAnHour(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	for id, aired := range map[int64]time.Time{501: e.clock.Add(-48 * time.Hour), 502: e.clock.Add(-10 * 24 * time.Hour),
		503: e.clock.Add(-48 * time.Hour), 504: e.clock.Add(-48 * time.Hour)} {
		e.exec(`INSERT INTO episode (id, title_id, season, number, air_date_utc_at, created_at, updated_at)
			VALUES (?, 2, 1, ?, ?, 0, 0)`, id, id-500, store.Millis(aired))
	}
	epCand := func(hash string, ep int64) Candidate {
		c := cand(hash, "Show.S01E01.2160p.WEB.H265-GRP")
		c.Episodes = []int64{ep}
		return c
	}
	series := func(c Candidate) Request {
		return Request{TitleID: 2, VersionID: 20, Version: "series", Candidates: []Candidate{c}, Source: "ui:grab:test"}
	}
	expires := func(hash, pk string) time.Duration {
		t.Helper()
		var exp, checked int64
		if err := e.st.Reader().QueryRow(`SELECT expires_at, checked_at FROM provider_cache WHERE info_hash = ? AND provider = ?`,
			hash, pk).Scan(&exp, &checked); err != nil {
			t.Fatalf("%s/%s: %v", hash[:4], pk, err)
		}
		return time.Duration(exp-checked) * time.Millisecond
	}
	e.ad["rd"].add = refuse(adder.CodeNotCached)

	// RD only: the all-provider miss.
	e.pol.Versions["series"].Providers = []string{"rd"}
	for _, q := range []struct {
		hash string
		ep   int64
		want time.Duration
	}{{h("a"), 501, time.Hour}, {h("b"), 502, 6 * time.Hour}} {
		e.clock = e.clock.Add(time.Minute)
		if _, err := e.r.Route(e.ctx, series(epCand(q.hash, q.ep))); err != nil {
			t.Fatal(err)
		}
		if st, _ := e.r.cacheState(e.ctx, q.hash, "rd", e.clock); st != "miss" {
			t.Fatalf("rd %q", st)
		}
		if got := expires(q.hash, "rd"); got != q.want {
			t.Errorf("episode %d: miss for %v, want %v", q.ep, got, q.want)
		}
	}
	// RD then AD: RD's uncached add.
	e.pol.Versions["series"].Providers = []string{"rd", "ad"}
	for _, q := range []struct {
		hash string
		ep   int64
		want time.Duration
	}{{h("c"), 501, time.Hour}, {h("d"), 502, 6 * time.Hour}} {
		e.clock = e.clock.Add(time.Minute)
		if res, err := e.r.Route(e.ctx, series(epCand(q.hash, q.ep))); err != nil || res.Provider != "ad" {
			t.Fatalf("%v %s", err, steps(res))
		}
		if got := expires(q.hash, "rd"); got != q.want {
			t.Errorf("episode %d: uncached for %v, want %v", q.ep, got, q.want)
		}
	}
	// A 451 on new content (another episode: 501 is on AD now) keeps miss_ttl.
	e.ad["rd"].add = func(adder.Magnet, adder.AddOptions) (*adder.Added, error) {
		return nil, &adder.Error{Provider: "rd", Code: adder.CodeAccountHold, Status: 451}
	}
	e.clock = e.clock.Add(time.Minute)
	if _, err := e.r.Route(e.ctx, series(epCand(h("e"), 503))); err != nil {
		t.Fatal(err)
	}
	if got := expires(h("e"), "rd"); got != 6*time.Hour {
		t.Errorf("451 for %v, want miss_ttl", got)
	}
	// RD only: the all-provider miss after a 451 leaves the 451 row as it was.
	e.pol.Versions["series"].Providers = []string{"rd"}
	e.clock = e.clock.Add(time.Minute)
	if _, err := e.r.Route(e.ctx, series(epCand(h("f"), 504))); err != nil {
		t.Fatal(err)
	}
	if got := expires(h("f"), "rd"); got != 6*time.Hour {
		t.Errorf("RD-only 451 for %v, want miss_ttl", got)
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'rd' AND state = 'miss'
		AND source = 'router:451'`, h("f")); n != 1 {
		t.Error("RD-only 451 source not kept")
	}
}
