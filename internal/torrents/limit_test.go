package torrents

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

// TestReportTorrentLimit: an AllDebrid account is reported with its magnet_limit (default 5000) and flagged from
// 90% of it; the other kinds have no limit. Only reported.
func TestReportTorrentLimit(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	limit := 10
	e.pol.Providers["ad"] = &policy.Provider{Kind: provider.KindAllDebrid, Order: 3, MagnetLimit: &limit}
	ad := &fakeProvider{key: "ad", kind: provider.KindAllDebrid}
	e.m.Providers["ad"] = ad
	for i := range 8 {
		ad.list = append(ad.list, tor(string(rune('a'+i)), "", "Some.Movie.2001.1080p", 1, e.now, provider.StateDownloaded))
	}
	limits := func() map[string]ProviderReport {
		r, err := e.report(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]ProviderReport{}
		for _, p := range r.Providers {
			out[p.Provider] = p
		}
		return out
	}
	got := limits()
	if p := got["ad"]; p.AccountTorrents != 8 || p.Limit != 10 || p.NearLimit {
		t.Errorf("8 of 10: %+v", p)
	}
	if p := got["rd"]; p.Limit != 0 || p.NearLimit {
		t.Errorf("rd has no limit: limit %d near %v", p.Limit, p.NearLimit)
	}
	ad.list = append(ad.list, tor("z", "", "Other.Movie.2002.1080p", 1, e.now, provider.StateDownloaded))
	if p := limits()["ad"]; p.AccountTorrents != 9 || !p.NearLimit {
		t.Errorf("9 of 10 is 90%%: %+v", p)
	}
	e.pol.Providers["ad"].MagnetLimit = nil
	if p := limits()["ad"]; p.Limit != 5000 || p.NearLimit {
		t.Errorf("default limit: %+v", p)
	}
}
