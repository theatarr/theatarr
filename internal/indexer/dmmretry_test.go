package indexer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// DMM's newly cached RSS list reopens our own live uncached verdict (router:add, router:check) at the provider it
// names, once: only a listing newer than the verdict, only from the RSS path, never a miss or a 451, never another
// provider's row.
func TestDMMListingReopensOlderUncached(t *testing.T) {
	p := evidencePolicy()
	p.Indexers["dmm-rdad"] = &policy.Indexer{Kind: "dmm", URL: "http://dmm.test/api/torznab/rd/ad/cached",
		APIKeyEnv: dmmKeyEnv, Privacy: policy.PrivacyPublic, Budget: "dmm-key"}
	st := openStore(t)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	e := NewEvidence(EvidenceConfig{Store: st, Policy: p, Now: func() time.Time { return now }})
	checked := now.Add(-30 * time.Minute)
	for _, q := range []struct{ hash, prov, state, src string }{
		{hx("a"), "rd", "uncached", "router:add"},   // newer listing: reopened
		{hx("b"), "rd", "uncached", "router:add"},   // older listing: kept
		{hx("c"), "rd", "uncached", "router:check"}, // newer listing: reopened
		{hx("d"), "rd", "miss", "router:miss"},      // an all-provider miss, newer listing: reopened
		{hx("e"), "rd", "miss", "router:451"},       // a 451: kept
		{hx("f"), "rd", "uncached", "router:add"},   // a search positive: kept
		{hx("1"), "rd", "uncached", "router:add"},   // an AD listing: RD kept
		{hx("2"), "ad", "uncached", "router:add"},   // newer AD listing: reopened
		{hx("3"), "ad", "uncached", "router:add"},   // an RD listing: AD kept
		{hx("4"), "rd", "uncached", "add:rd"},       // not a reopenable source
		{hx("5"), "rd", "uncached", "router:add"},   // a listing without a pubDate: kept
	} {
		if _, err := st.Writer().Exec(`INSERT INTO provider_cache (info_hash, provider, state, source, checked_at,
			expires_at) VALUES (?, ?, ?, ?, ?, ?)`, q.hash, q.prov, q.state, q.src, store.Millis(checked),
			store.Millis(now.Add(5*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	rel := func(c, on string, listed time.Time) Release {
		return Release{Indexer: "dmm-rdad", Kind: "dmm", InfoHash: hx(c), PublishDate: listed,
			Extras: map[string]string{"cached_on": on}}
	}
	newer, older := checked.Add(15*time.Minute), checked.Add(-time.Minute)
	e.recordFeed(context.Background(), []Release{rel("a", "rd", newer), rel("b", "rd", older), rel("c", "rd", newer),
		rel("d", "rd", newer), rel("e", "rd", newer), rel("1", "ad", newer), rel("2", "ad", newer), rel("3", "rd", newer),
		rel("4", "rd", newer), rel("5", "rd", time.Time{})}, true)
	e.recordFeed(context.Background(), []Release{rel("f", "rd", newer)}, false)
	want := map[string]string{
		"aaaa/rd": "cached feed:dmm-rdad", "bbbb/rd": "uncached router:add", "cccc/rd": "cached feed:dmm-rdad",
		"dddd/rd": "cached feed:dmm-rdad", "eeee/rd": "miss router:451", "ffff/rd": "uncached router:add",
		"1111/rd": "uncached router:add", "1111/ad": "cached feed:dmm-rdad",
		"2222/ad": "cached feed:dmm-rdad", "3333/ad": "uncached router:add", "3333/rd": "cached feed:dmm-rdad",
		"4444/rd": "uncached add:rd", "5555/rd": "uncached router:add",
	}
	if got := cacheRows(t, st); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("provider_cache\n%v, want\n%v", got, want)
	}

	// The retry misses again: our newer verdict. The same listing, read again, leaves it.
	now = now.Add(5 * time.Minute)
	if _, err := st.Writer().Exec(`UPDATE provider_cache SET state = 'uncached', source = 'router:add', checked_at = ?
		WHERE info_hash = ? AND provider = 'rd'`, store.Millis(now), hx("a")); err != nil {
		t.Fatal(err)
	}
	e.recordFeed(context.Background(), []Release{rel("a", "rd", newer)}, true)
	if got := cacheRows(t, st)["aaaa/rd"]; got != "uncached router:add" {
		t.Errorf("same listing again: %q", got)
	}
	// A pubDate ahead of our clock (clock skew) never reopens: read every sync, it would reopen each retry.
	now = now.Add(10 * time.Minute)
	ahead := now.Add(time.Hour)
	for range 2 {
		e.recordFeed(context.Background(), []Release{rel("a", "rd", ahead)}, true)
		if got := cacheRows(t, st)["aaaa/rd"]; got != "uncached router:add" {
			t.Fatalf("listing ahead of the clock: %q", got)
		}
		now = now.Add(time.Minute)
	}
	// A past pubDate newer than the verdict still reopens it.
	e.recordFeed(context.Background(), []Release{rel("a", "rd", now.Add(-time.Minute))}, true)
	if got := cacheRows(t, st)["aaaa/rd"]; got != "cached feed:dmm-rdad" {
		t.Errorf("a newer past listing: %q", got)
	}
}

// TorBox's search-time check: an uncached answer lives 30 minutes, at most router.miss_ttl.
func TestTorBoxCheckUncachedLivesThirtyMinutes(t *testing.T) {
	for _, q := range []struct {
		missTTL time.Duration
		want    time.Duration
	}{{6 * time.Hour, 30 * time.Minute}, {10 * time.Minute, 10 * time.Minute}} {
		n := newNet(t)
		tb := &tbServer{cached: map[string]bool{}}
		tb.serve(t, n)
		p := evidencePolicy()
		p.Router = &policy.Router{MissTTL: policy.Duration(q.missTTL)}
		h := newHarness(t, n, p, false)
		st := openStore(t)
		h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: p, Checkers: tbChecker(t, p, h), Now: h.clock}))
		vs := []*policy.VersionPolicy{{Storage: policy.StorageDebrid, Indexers: []string{"torrentio"}, Providers: []string{"tb"}}}
		if n := h.svc.CheckCached(context.Background(), vs, []*Release{{Indexer: "torrentio", InfoHash: hx("1"),
			Privacy: policy.PrivacyPublic}}, nil); n != 1 {
			t.Fatalf("checked %d", n)
		}
		var exp int64
		if err := st.Reader().QueryRow(`SELECT expires_at - checked_at FROM provider_cache WHERE info_hash = ?
			AND provider = 'tb' AND state = 'uncached' AND source = 'check:torbox'`, hx("1")).Scan(&exp); err != nil {
			t.Fatal(err)
		}
		if got := time.Duration(exp) * time.Millisecond; got != q.want {
			t.Errorf("miss_ttl %v: uncached for %v, want %v", q.missTTL, got, q.want)
		}
	}
}
