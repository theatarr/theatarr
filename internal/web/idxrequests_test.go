package web

import (
	"context"
	"encoding/json"
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
