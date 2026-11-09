package indexer

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"
)

func TestCardigannCriteriaKeywords(t *testing.T) {
	for _, tc := range []struct {
		q    Query
		want string
	}{
		{Query{Type: QuerySeries, Title: "Daily Show", Criteria: CriteriaDaily, AirDate: "2024-03-05"}, "Daily Show 2024 03 05"},
		{Query{Type: QuerySeries, Title: "Anime", Criteria: CriteriaAnimeEpisode, Season: 2, Episode: 1, Absolute: 29}, "Anime 29"},
	} {
		if got := cardigannKeywords(tc.q); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
}

func TestCardigannSendsSceneTitleAndAbsoluteKeywords(t *testing.T) {
	n := newNet(t)
	var mu sync.Mutex
	var asked []string
	n.serve("site.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Query().Get("q"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	}))
	h := cardigannHarness(t, n, "public", nil, nil, false)
	res, err := h.svc.Search(context.Background(), "site", Query{Type: QuerySeries, Title: "Frieren", Criteria: CriteriaAnimeEpisode, Season: 2, Episode: 1, Absolute: 29, SceneTitles: []string{"Sousou no Frieren", "Frieren"}})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, q := range []string{"Sousou no Frieren 29", "Frieren 29", "Sousou no Frieren S02E01", "Frieren S02E01"} {
		if !slices.Contains(asked, q) {
			t.Errorf("missing %q in requests %v", q, asked)
		}
	}
	if len(asked) != 4 || res.Requests != 4 {
		t.Fatalf("requests %v, accounting %d", asked, res.Requests)
	}
}
