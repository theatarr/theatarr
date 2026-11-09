package router

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestInFlight(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	check := func(title, version int64, eps []int64, want bool) {
		t.Helper()
		got, err := InFlight(e.ctx, e.st.Reader(), title, version, eps)
		if err != nil || got != want {
			t.Errorf("InFlight(%d, %d, %v) = %v, %v; want %v", title, version, eps, got, err, want)
		}
	}
	check(1, 10, nil, false)
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (1, 10, 'movie', ?, 'x', 'pub', 'failed', 0, 0)`, h("a"))
	check(1, 10, nil, false)
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (1, 10, 'movie', ?, 'x', 'pub', 'downloading', 0, 0)`, h("b"))
	check(1, 10, nil, true)
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, episodes, state,
		created_at, updated_at) VALUES (2, 20, 'series', ?, 'x', 'pub', '[5,6]', 'queued', 0, 0)`, h("c"))
	check(2, 20, []int64{7}, false)
	check(2, 20, []int64{6, 7}, true)
	check(2, 20, nil, true)
}

func TestTitleOnlyBlocklist(t *testing.T) {
	t.Parallel()
	for _, dry := range []bool{false, true} {
		e := newEnv(t, true, policy.ModeActive)
		if err := e.st.WriteTx(e.ctx, func(tx *sql.Tx) error {
			return Blocklist(e.ctx, tx, "movie", "", 1, 0, "Blocked.Release", "arr blocklist (radarr)", "arr:radarr", 0)
		}); err != nil {
			t.Fatal(err)
		}
		var res *Result
		var err error
		req := movieReq(cand(h("b"), "blocked.release"))
		if dry {
			res, err = e.r.Plan(e.ctx, req)
		} else {
			res, err = e.r.Route(e.ctx, req)
		}
		if err != nil || res.Outcome != OutcomeExhausted || len(res.Steps) != 1 || res.Steps[0].Code != "blocklisted" {
			t.Fatalf("dry=%v: %+v, %v", dry, res, err)
		}
		if e.writes() != 0 {
			t.Error("blocklisted title reached a provider")
		}
	}
}

func TestPlanSummary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		outcome, release, provider string
		steps                      []Step
		want                       string
	}{
		{"would_add", "R", "rd", []Step{{Provider: "rd", Code: "add"}}, "would add R on rd"},
		{"would_add", "R", "rd", []Step{{Provider: "rd", Code: "reused"}}, "would add R on rd (the provider holds it already)"},
		{"deferred", "R", "", nil, "would wait to add R: every usable provider is paced or backing off"},
		{"cancelled", "", "", []Step{{Code: "route_cancelled"}}, "route cancelled: no release is still wanted"},
		{"none", "", "", nil, "would add nothing: no accepted release"},
		{"none", "", "", []Step{{Code: "private"}, {Code: "no_hash"}, {Code: "private"}}, "would add nothing: private, no_hash"},
		{"none", "", "", []Step{{Provider: "rd", Code: "rd_name_rule"}, {Code: "miss", Outcome: "miss"}},
			"would add nothing: rd rd_name_rule, miss"},
	} {
		if got := PlanSummary(tc.outcome, tc.release, tc.provider, tc.steps); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.outcome, got, tc.want)
		}
	}
}

// The routed decision's formats land on the download (for the library's names); a blocklisted hash is skipped by
// Route and Plan alike, and listed in the status.
func TestFormatsAndBlocklist(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	d := &decide.Decision{Accepted: true, Quality: "Remux-2160p", Score: 2400,
		Formats: []decide.FormatScore{{Name: "HDR", Score: 500}, {Name: "Remux Tier 01", Score: 1900}}}
	cs := Candidates([]*decide.Decision{d}, map[*decide.Decision]indexer.Release{d: cand(h("a"), "Movie.2024.2160p.UHD.BluRay.REMUX-GRP").Release})
	cs[0].Release.Languages = []string{"French"}
	cs[0].Release.Flags = []string{"internal"}
	res, err := e.r.Route(e.ctx, grabReq(cs...))
	if err != nil || res.Outcome != OutcomeAdded {
		t.Fatalf("route %+v %v", res, err)
	}
	var formats string
	if err := e.st.Reader().QueryRow(`SELECT formats FROM download WHERE id = ?`, res.DownloadID).Scan(&formats); err != nil ||
		formats != `[{"name":"HDR","score":500},{"name":"Remux Tier 01","score":1900}]` {
		t.Errorf("formats %q %v", formats, err)
	}
	var raw string
	if err := e.st.Reader().QueryRow(`SELECT release_data FROM download WHERE id = ?`, res.DownloadID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved indexer.Release
	if err := json.Unmarshal([]byte(raw), &saved); err != nil || saved.Title != cs[0].Release.Title ||
		len(saved.Languages) != 1 || saved.Languages[0] != "French" || len(saved.Flags) != 1 || saved.Flags[0] != "internal" {
		t.Fatalf("saved release inputs: %+v, %v", saved, err)
	}
	if err := e.st.WriteTx(e.ctx, func(tx *sql.Tx) error {
		return Blocklist(e.ctx, tx, "movie", strings.ToUpper(h("b")), 1, 0, "x", "sample", BlocklistLibrary, 0)
	}); err != nil {
		t.Fatal(err)
	}
	before := e.writes()
	for _, dry := range []bool{false, true} {
		req := grabReq(cand(h("b"), "Movie.2024.2160p.UHD.BluRay.REMUX-OTHER"))
		var res *Result
		if dry {
			res, err = e.r.Plan(e.ctx, req)
		} else {
			res, err = e.r.Route(e.ctx, req)
		}
		if err != nil || res.Outcome != OutcomeExhausted || len(res.Steps) != 1 || res.Steps[0].Code != "blocklisted" {
			t.Errorf("dry %v: %+v %v", dry, res, err)
		}
	}
	if e.writes() != before {
		t.Error("a blocklisted hash was added")
	}
	s, err := ReadStatus(e.ctx, e.st, e.pol, nil, e.clock, 10)
	if err != nil || s.BlocklistTotal != 1 || s.Blocklist[0].Hash != h("b") || s.Blocklist[0].Source != "library" {
		t.Errorf("status %+v %v", s, err)
	}
}
