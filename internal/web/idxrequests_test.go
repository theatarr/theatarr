package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/store"
)

// The settings overview carries each indexer's requests by purpose from the gateway: today and the last 7 UTC days,
// totals, and the purposes with a request in display order (docs/design/gateway.md).
func TestSettingsIndexerRequests(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	today := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, r := range []struct {
		indexer, purpose string
		day              time.Time
		n                int
	}{
		{"dmm", "rss", today, 40}, {"dmm", "interactive", today, 2}, {"dmm", "rss", today.AddDate(0, 0, -6), 100},
		{"dmm", "upgrade", today.AddDate(0, 0, -3), 5}, {"dmm", "wanted", today.AddDate(0, 0, -7), 9},
	} {
		se.exec(`INSERT INTO gateway_indexer_request (indexer, purpose, day_start_at, count) VALUES (?, ?, ?, ?)`,
			r.indexer, r.purpose, store.Millis(r.day), r.n)
	}
	gw, err := gateway.New(context.Background(), se.web.pol, gateway.Options{Store: se.st,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	se.eg.gw = gw
	rec := se.call("GET", "", nil)
	se.want(rec, 200, "overview")
	var o struct {
		Indexers []struct {
			Key      string          `json:"key"`
			Requests json.RawMessage `json:"requests"`
		} `json:"indexers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for _, ix := range o.Indexers {
		var v any
		if err := json.Unmarshal(ix.Requests, &v); err != nil {
			t.Fatalf("%s: %s: %v", ix.Key, ix.Requests, err)
		}
		got[ix.Key] = v
	}
	var want any
	json.Unmarshal([]byte(`{"today": 42, "week": 147, "purposes": [
		{"purpose": "interactive", "today": 2, "week": 2},
		{"purpose": "upgrade", "today": 0, "week": 5},
		{"purpose": "rss", "today": 40, "week": 140}]}`), &want)
	if !reflect.DeepEqual(got["dmm"], want) {
		t.Fatalf("dmm requests: %v", got["dmm"])
	}
	var none any
	json.Unmarshal([]byte(`{"today": 0, "week": 0, "purposes": []}`), &none)
	if !reflect.DeepEqual(got["zilean"], none) {
		t.Fatalf("zilean requests: %v", got["zilean"])
	}
}

// GET /indexers/stats: per configured indexer over the range, settings tests left out; failed and average response
// over the measured requests only (rows from before the measurement have a count only, and read as none measured);
// downloads from History by distinct download, manual adds left out.
func TestIndexerStats(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	today := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, r := range []struct {
		indexer, purpose string
		day              time.Time
		n, measured, f   int
		took             int64
	}{
		{"dmm", "rss", today, 10, 10, 2, 5000},
		{"dmm", "test", today, 3, 3, 3, 900},                        // settings tests: left out
		{"dmm", "interactive", today.AddDate(0, 0, -6), 5, 0, 0, 0}, // before the measurement
		{"dmm", "rss", today.AddDate(0, 0, -7), 50, 50, 50, 1},      // outside 7 days, inside 30
		{"zilean", "wanted", today.AddDate(0, 0, -1), 4, 0, 0, 0},
		{"torrentio", "wanted", today, 2, 2, 5, 400}, // failures recorded without a measured request (a solver fetch)
	} {
		se.exec(`INSERT INTO gateway_indexer_request (indexer, purpose, day_start_at, count, measured, failures, took_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, r.indexer, r.purpose, store.Millis(r.day), r.n, r.measured, r.f, r.took)
	}
	at := store.Millis(today.Add(time.Hour))
	se.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, error,
		created_at, updated_at) VALUES (5, 1, 1, 'movie', 'h5', 'x', 'dmm', 'failed', 'miss on every provider', ?, ?)`, at, at)
	for i, h := range []struct {
		event, indexer string
		dl             int64
		at             int64
	}{
		{"grabbed", "dmm", 1, at}, {"grabbed", "dmm", 2, at}, {"imported", "dmm", 1, at},
		{"failed", "dmm", 2, at}, {"failed", "dmm", 2, at + 1}, // one download, two failed rows
		{"grabbed", "manual add", 3, at}, {"grabbed", "dmm", 4, store.Millis(today.AddDate(0, 0, -8))},
		{"failed", "dmm", 5, at}, // a candidate every provider missed: not a failed download
	} {
		se.exec(`INSERT INTO history (ref, at, event, kind, download_id, indexer) VALUES (?, ?, ?, '', ?, ?)`,
			fmt.Sprint("t", i), h.at, h.event, h.dl, h.indexer)
	}
	stats := func(q string) map[string]IndexerStatsItem {
		t.Helper()
		rec := se.get(APIBase+"/indexers/stats"+q, se.cookie)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body)
		}
		var s IndexerStats
		if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		if s.MeasuredSince != "2026-10-19" {
			t.Fatalf("measured since %q", s.MeasuredSince)
		}
		out := map[string]IndexerStatsItem{}
		for _, it := range s.Indexers {
			out[it.Key] = it
		}
		return out
	}
	ptr := func(n int) *int { return &n }
	ms := func(n int64) *int64 { return &n }
	week := stats("")
	if want := (IndexerStatsItem{Key: "dmm", Requests: 15, Measured: 10, Failed: ptr(2), AverageMS: ms(500), Grabbed: 2,
		Imported: 1, FailedDownloads: 1}); !reflect.DeepEqual(week["dmm"], want) {
		t.Fatalf("dmm, 7 days: %+v", week["dmm"])
	}
	if want := (IndexerStatsItem{Key: "zilean", Requests: 4}); !reflect.DeepEqual(week["zilean"], want) {
		t.Fatalf("zilean, 7 days: %+v (no measured request: no failed or average)", week["zilean"])
	}
	// Failed never reads above the measured requests (100%).
	if want := (IndexerStatsItem{Key: "torrentio", Requests: 2, Measured: 2, Failed: ptr(2), AverageMS: ms(200)}); !reflect.DeepEqual(week["torrentio"], want) {
		t.Fatalf("torrentio, 7 days: %+v", week["torrentio"])
	}
	month := stats("?days=30")["dmm"]
	if month.Requests != 65 || *month.Failed != 52 || *month.AverageMS != 83 || month.Grabbed != 3 {
		t.Fatalf("dmm, 30 days: %+v", month)
	}
	if rec := se.get(APIBase+"/indexers/stats?days=14", se.cookie); rec.Code != 400 {
		t.Fatalf("days=14: %d", rec.Code)
	}
}
