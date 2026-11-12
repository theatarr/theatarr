package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/ceiling"
)

func TestCeilingAPI(t *testing.T) {
	e := newManageEnv(t, nil)
	ctx := context.Background()
	// A held 2160p WEB is below the remux cutoff, but no higher release has been found.
	e.exec(`DELETE FROM journal_decision WHERE title_id=1`)
	at := addNow.AddDate(0, 0, -100)
	if err := ceiling.Sight(ctx, e.st.Writer(), 1, -1, ceiling.Evidence{Tier: "web_2160", Source: "arr_history", Kind: "arr_history", PositiveOnly: true, Example: "Alpha.2160p.WEB-DL", FirstSeen: at, LastSeen: at}); err != nil {
		t.Fatal(err)
	}
	compute := func() {
		t.Helper()
		if err := e.st.WriteTx(ctx, func(tx *sql.Tx) error { return ceiling.Compute(ctx, tx, e.pol, 1, addNow) }); err != nil {
			t.Fatal(err)
		}
	}
	get := func() *UpgradeInfo {
		t.Helper()
		var d TitleDetail
		e.call("GET", APIBase+"/titles/1", nil, 200, &d)
		return d.Versions[0].Upgrade
	}
	compute()
	u := get()
	if u.State != "best_source_seen" || u.Ceiling == nil || u.Ceiling.Status != "likely" || u.EffectiveCutoff != "WEB 2160p" || len(u.Ceiling.Evidence) != 1 {
		t.Fatalf("likely: %+v", u)
	}
	for _, days := range []int{90, 45, 0} {
		if err := ceiling.Record(ctx, e.st.Writer(), e.pol, 1, "movie-remux", "movie", "search", nil, nil, []ceiling.Coverage{{Indexer: "disc-feed", Kind: "torznab", Season: -1, Complete: true}}, addNow.AddDate(0, 0, -days)); err != nil {
			t.Fatal(err)
		}
	}
	u = get()
	if u.State != "best_available" || u.Ceiling.Status != "known" || len(u.Ceiling.Searches) != 3 || u.Ceiling.Searches[0].Indexers[0].Indexer != "disc-feed" {
		t.Fatalf("known: %+v", u)
	}
	before := e.state()
	submits, interactive := e.acq.counts()
	e.call("PUT", APIBase+"/titles/1/ceiling", map[string]any{"tier": "uhd_bd", "note": "Disc checked manually"}, 200, nil)
	u = get()
	if u.State != "higher_source_exists" || u.Ceiling.Override == nil || u.Ceiling.Override.Note != "Disc checked manually" {
		t.Fatalf("override: %+v", u)
	}
	// A release actually seen retains the existing, stronger upgrade explanation.
	e.exec(`INSERT INTO journal_decision(source,title_id,version_key,target,results,accepted,candidates,rejections,notes,created_at)
 VALUES('test',1,'movie-remux','movie',1,1,'[{"release":"Alpha.Remux","accepted":true,"upgrade":true,"score":9000}]','{}','',?)`, addNow.UnixMilli())
	if u = get(); u.State != "upgrade_available" {
		t.Fatalf("release sighting: %+v", u)
	}
	e.exec(`DELETE FROM journal_decision WHERE source='test'`)
	e.call("DELETE", APIBase+"/titles/1/ceiling", nil, 200, nil)
	if u = get(); u.State != "best_available" || u.Ceiling.Override != nil {
		t.Fatalf("cleared: %+v", u)
	}
	for _, req := range []map[string]any{{"tier": "made_up"}, {"tier": "bd", "season": 1}, {"tier": "bd", "expires_at": addNow.Add(-time.Hour)}} {
		e.call("PUT", APIBase+"/titles/1/ceiling", req, 400, nil)
	}
	e.call("PUT", APIBase+"/titles/10/ceiling", map[string]any{"tier": "bd"}, 400, nil)
	e.call("PUT", APIBase+"/titles/10/ceiling", map[string]any{"tier": "bd", "season": 99}, 404, nil)
	if e.state() != before {
		t.Fatal("ceiling changed monitoring or acquisition state")
	}
	s, i := e.acq.counts()
	if s != submits || i != interactive {
		t.Fatal("ceiling caused a search")
	}
}

func TestSeasonCeilingAndRedaction(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	// Preserve the owning arr profile's groups and order, without fetching it again.
	e.exec(`UPDATE arr_quality_profile SET quality_items='[
 {"id":0,"quality":{"id":3,"name":"WEBDL-1080p"},"allowed":true,"items":[]},
 {"id":0,"quality":{"id":21,"name":"Bluray-2160p Remux"},"allowed":true,"items":[]} ]' WHERE instance='sonarr_4k_debrid'`)
	e.exec(`DELETE FROM journal_decision WHERE title_id=10 AND target='rss'`)
	e.call("PUT", APIBase+"/titles/10/ceiling", map[string]any{"tier": "web_1080", "season": 1, "note": "season one only"}, 200, nil)
	var list EpisodeList
	e.call("GET", APIBase+"/titles/10/episodes", nil, 200, &list)
	var found bool
	for _, s := range list.Seasons {
		u := s.Versions["tv-2160p"].Upgrade
		if s.Season == 1 {
			found = true
			if u == nil || u.Ceiling == nil || u.Ceiling.Status != "override" || u.State != "best_available" {
				t.Fatalf("season: %+v", u)
			}
		} else if u != nil && u.Ceiling != nil && u.Ceiling.Override != nil {
			t.Fatal("override crossed seasons")
		}
	}
	if !found {
		t.Fatal("season not returned")
	}
	var ep EpisodeDetail
	e.call("GET", APIBase+"/titles/10/episodes/102", nil, 200, &ep)
	if u := ep.Versions[0].Upgrade; u.State != "best_available" || u.Ceiling == nil || u.EffectiveCutoff != "WEBDL-1080p" {
		t.Fatalf("episode: %+v", u)
	}
	// The same payloads, including nested grid cells, disclose neither evidence nor confidence to users.
	user := e.userMux()
	viewer, err := New(Config{Store: e.st, Auth: e.au, Policy: e.pol, Perms: func(*auth.Identity) Perms { return PermsOf(auth.PermBrowse | auth.PermSeeFiles) }})
	if err != nil {
		t.Fatal(err)
	}
	fileViewer := http.NewServeMux()
	viewer.Mount(fileViewer)
	for _, path := range []string{"/titles/10", "/titles/10/episodes", "/titles/10/episodes/102"} {
		r := httptest.NewRequest("GET", APIBase+path, nil)
		r.Header.Set(auth.APIKeyHeader, e.key)
		rec := httptest.NewRecorder()
		fileViewer.ServeHTTP(rec, r)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "\"ceiling\"") || strings.Contains(rec.Body.String(), "best_available") {
			t.Errorf("file viewer without manage: %s %d %s", path, rec.Code, rec.Body.String())
		}
	}
	for _, path := range []string{"/titles/10", "/titles/10/episodes", "/titles/10/episodes/102"} {
		r := httptest.NewRequest("GET", APIBase+path, nil)
		r.Header.Set(auth.APIKeyHeader, e.key)
		rec := httptest.NewRecorder()
		user.ServeHTTP(rec, r)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		for _, secret := range []string{"\"ceiling\"", "\"effective_cutoff\"", "season one only", "best_available", "\"evidence\"", "\"searches\""} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Errorf("%s leaks %s: %s", path, secret, rec.Body.String())
			}
		}
	}
	for _, method := range []string{"PUT", "DELETE"} {
		r := httptest.NewRequest(method, APIBase+"/titles/10/ceiling?season=1", strings.NewReader(`{"tier":"bd","season":1}`))
		r.Header.Set(auth.APIKeyHeader, e.key)
		rec := httptest.NewRecorder()
		user.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s as user: %d", method, rec.Code)
		}
	}
	e.call("DELETE", APIBase+"/titles/10/ceiling?season=1", nil, 200, nil)
}

func TestCeilingOpenAPIFields(t *testing.T) {
	t.Parallel()
	var spec struct {
		Components struct{ Schemas map[string]json.RawMessage }
	}
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SourceCeiling", "CeilingCoverage", "CeilingEvidence", "CeilingSearch", "CeilingOverride", "CeilingOverrideRequest"} {
		if len(spec.Components.Schemas[name]) == 0 {
			t.Errorf("missing %s", name)
		}
	}
	for _, field := range []string{"best_available", "best_source_seen", "higher_source_exists", "ceiling", "effective_cutoff"} {
		if !strings.Contains(string(spec.Components.Schemas["UpgradeInfo"]), field) {
			t.Errorf("UpgradeInfo missing %s", field)
		}
	}
}

func TestCeilingOverrideWithoutFiles(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`DELETE FROM version_file`)
	e.call("PUT", APIBase+"/titles/1/ceiling", map[string]any{"tier": "bd", "note": "no file yet"}, 200, nil)
	var title TitleDetail
	e.call("GET", APIBase+"/titles/1", nil, 200, &title)
	if u := title.Versions[0].Upgrade; u == nil || u.State != "none" || u.Ceiling == nil || u.Ceiling.Override == nil {
		t.Fatalf("movie override without file: %+v", u)
	}
	e.call("PUT", APIBase+"/titles/10/ceiling", map[string]any{"tier": "bd", "season": 1}, 200, nil)
	var list EpisodeList
	e.call("GET", APIBase+"/titles/10/episodes", nil, 200, &list)
	for _, season := range list.Seasons {
		if season.Season == 1 {
			if u := season.Versions["tv-2160p"].Upgrade; u == nil || u.State != "none" || u.Ceiling == nil || u.Ceiling.Override == nil {
				t.Fatalf("season override without files: %+v", u)
			}
		}
	}
	var episode EpisodeDetail
	e.call("GET", APIBase+"/titles/10/episodes/102", nil, 200, &episode)
	if u := episode.Versions[0].Upgrade; u == nil || u.State != "none" || u.Ceiling == nil || u.Ceiling.Override == nil {
		t.Fatalf("episode override without file: %+v", u)
	}
	e.call("DELETE", APIBase+"/titles/1/ceiling", nil, 200, nil)
	title = TitleDetail{}
	e.call("GET", APIBase+"/titles/1", nil, 200, &title)
	if u := title.Versions[0].Upgrade; u.Ceiling == nil || u.Ceiling.Override != nil {
		t.Fatalf("cleared without file: %+v", u)
	}
}

// A list row carries the title page's upgrade state, so the poster badge agrees with it; without manage the
// source-ceiling states read as below cutoff.
func TestRowUpgradeState(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	ctx := context.Background()
	e.exec(`DELETE FROM journal_decision WHERE title_id=1`)
	at := addNow.AddDate(0, 0, -100)
	if err := ceiling.Sight(ctx, e.st.Writer(), 1, -1, ceiling.Evidence{Tier: "web_2160", Source: "arr_history", Kind: "arr_history", PositiveOnly: true, Example: "Alpha.2160p.WEB-DL", FirstSeen: at, LastSeen: at}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.WriteTx(ctx, func(tx *sql.Tx) error { return ceiling.Compute(ctx, tx, e.pol, 1, addNow) }); err != nil {
		t.Fatal(err)
	}
	row := func(mux http.Handler, path string) TitleRow {
		t.Helper()
		r := httptest.NewRequest("GET", APIBase+path, nil)
		r.Header.Set(auth.APIKeyHeader, e.key)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		var page struct {
			Items []TitleRow `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, it := range page.Items {
			if it.ID == 1 {
				return it
			}
		}
		t.Fatalf("%s: title 1 not listed: %s", path, rec.Body.String())
		return TitleRow{}
	}
	var p TitlePage
	e.call("GET", APIBase+"/titles?limit=500", nil, 200, &p)
	var got TitleRow
	for _, it := range p.Items {
		if it.ID == 1 {
			got = it
		}
	}
	if got.Availability != "upgrade" || got.UpgradeState != "best_source_seen" {
		t.Fatalf("manager row: %+v", got.TitleFacts)
	}
	viewer, err := New(Config{Store: e.st, Auth: e.au, Policy: e.pol, Perms: func(*auth.Identity) Perms { return PermsOf(auth.PermBrowse | auth.PermSeeFiles) }})
	if err != nil {
		t.Fatal(err)
	}
	fileViewer := http.NewServeMux()
	viewer.Mount(fileViewer)
	// A discover row redacts as the grid does; nothing better was found, so the title is not in the upgrades row.
	for _, path := range []string{"/titles?limit=500", "/discover/recent"} {
		if r := row(fileViewer, path); r.UpgradeState != "below_cutoff" {
			t.Errorf("file viewer %s: %+v", path, r.TitleFacts)
		}
	}
	if r := row(e.userMux(), "/titles?limit=500"); r.UpgradeState != "" || r.Availability != "available" {
		t.Errorf("user: %+v", r.TitleFacts)
	}
	var up DiscoverRow
	e.call("GET", APIBase+"/discover/upgrades", nil, 200, &up)
	for _, it := range up.Items {
		if it.ID == 1 || !it.UpgradeSeen {
			t.Errorf("upgrades row: %s (title %d, upgrade_seen %t) has no upgrade found", it.Name, it.ID, it.UpgradeSeen)
		}
	}
}
