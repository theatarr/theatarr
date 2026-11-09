package importarr

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/theatarr/theatarr/internal/catalog"
)

// The arr's monitorNewItems and season flags become the version's monitoring intent, so a season that appears after
// the cutover is monitored only when the arr would have monitored it. A later import follows the arr.
func TestImportSeriesMonitor(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	fake := newFakeSonarr("/mnt/shows")
	fake.series[0]["monitorNewItems"] = "none"
	fake.series[0]["seasons"] = []map[string]any{{"seasonNumber": 0, "monitored": false},
		{"seasonNumber": 1, "monitored": true}, {"seasonNumber": 2, "monitored": false}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	im := &Importer{Store: st, Now: fixedNow}
	src := Source{Instance: "sonarr_a", Flavour: "sonarr", Version: "tv-2160p", Library: "/mnt/shows"}
	runImport(t, im, src, srv)

	intent := func(tvdb int) catalog.Monitor {
		t.Helper()
		var vid int64
		if err := st.Reader().QueryRow(`SELECT v.id FROM version v JOIN title t ON t.id = v.title_id
			WHERE t.tvdb_id = ?`, tvdb).Scan(&vid); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM series_monitor WHERE version_id = ?`, vid).Scan(&n); err != nil || n != 1 {
			t.Fatalf("tvdb %d: %d series_monitor rows %v", tvdb, n, err)
		}
		m, err := catalog.LoadMonitor(context.Background(), st.Reader(), vid)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := intent(1001); m.New != catalog.MonitorNone || len(m.Seasons) != 3 || m.Seasons[0] || !m.Seasons[1] || m.Seasons[2] {
		t.Errorf("Show intent %+v", m)
	}
	// An arr without the field (or the seasons) takes new seasons, as Sonarr's default.
	if m := intent(2002); m.New != catalog.MonitorAll || len(m.Seasons) != 0 {
		t.Errorf("Anime intent %+v", m)
	}

	fake.mu.Lock()
	fake.series[0]["monitorNewItems"] = "all"
	fake.series[0]["seasons"] = []map[string]any{{"seasonNumber": 1, "monitored": true},
		{"seasonNumber": 2, "monitored": true}}
	fake.mu.Unlock()
	runImport(t, im, src, srv)
	if m := intent(1001); m.New != catalog.MonitorAll || len(m.Seasons) != 2 || !m.Seasons[2] {
		t.Errorf("Show intent after re-import %+v", m)
	}
}
