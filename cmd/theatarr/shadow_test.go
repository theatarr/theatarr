package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func TestShadowStatusLiveComparison(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "theatarr.db")
	st, err := store.Open(ctx, storetest.Seed(t, db), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Alpha',0,0)`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ bucket, reason string }{{"agree", "same_release"}, {"we_grab_arr_didnt", "theatarr_only"}} {
		if _, err := st.Writer().Exec(`INSERT INTO shadow_diff(at,title_id,version_key,bucket,reason,detail,compare_instance,compare_key,created_at)
 VALUES(?,1,'movie',?,?,'live: Theatarr grabbed earlier than the arr','arr',?,0)`, time.Now().Add(-time.Hour).UnixMilli(), row.bucket, row.reason, row.reason); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	var out bytes.Buffer
	if err := runShadow(commandSettingsArgs(t, []string{"status", "-db", db}), &out, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"50.0% of 2 grabs", "movie", "theatarr_only", "vs live; live: Theatarr grabbed earlier than the arr"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out.String())
		}
	}
}

func TestShadowStatusAndArgs(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "theatarr.db")
	st, err := store.Open(ctx, storetest.Seed(t, db), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	st.Close()
	var out bytes.Buffer
	if err := runShadow(commandSettingsArgs(t, []string{"status", "-db", db}), &out, nil); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "agreement (14d)  - of 0 grabs (bar 95%)") ||
		!strings.Contains(s, "hand grabs 0 (not in the ratio): 0 accepted, 0 rejected") ||
		!strings.Contains(s, "we_grab_arr_didnt") {
		t.Errorf("status:\n%s", s)
	}
	out.Reset()
	if err := runShadow(commandSettingsArgs(t, []string{"status", "-db", db, "-json"}), &out, nil); err != nil || !strings.Contains(out.String(), `"bar": 0.95`) {
		t.Errorf("json: %v\n%s", err, out.String())
	}
	if err := runShadow(commandSettingsArgs(t, []string{"run-once", "-db", db}), &out, nil); err == nil || !strings.Contains(err.Error(), "one of") {
		t.Errorf("run-once without a step: %v", err)
	}
	if err := runShadow(commandSettingsArgs(t, []string{"bogus"}), &out, nil); err == nil {
		t.Error("unknown subcommand accepted")
	}
}

// theatarr shadow reclassify: a stored RSS grab classified under the old rules (search vs the arr's pick) moves to
// same_rss_pick, and status shows the later search as information.
func TestShadowReclassify(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "theatarr.db")
	st, err := store.Open(ctx, storetest.Seed(t, db), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Hour).UnixMilli()
	arr := `{"release":"Alpha.2020.2160p.WEB-DL-FLUX","arr":true,"accepted":true,"quality":"WEBDL-2160p","score":1700}`
	pick := `{"release":"Alpha.2020.2160p.BluRay.REMUX-FraMeSToR","indexer":"dmm","info_hash":"cc","accepted":true,"quality":"Remux-2160p","score":1950}`
	ours := `{"release":"Alpha.2020.2160p.WEB-DL-FLUX","indexer":"prowlarr","accepted":true,"quality":"WEBDL-2160p","score":1700}`
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES (1, 'movie', 'Alpha', 2020, 0, 0)`,
		`INSERT INTO shadow_grab (id, instance, history_id, title_id, version_key, grabbed_at, release_title, release_source,
			state, searched, arr_decision, pick, created_at, updated_at) VALUES (1, 'radarr_zurg', 10, 1, 'movie-remux', ?,
			'Alpha.2020.2160p.WEB-DL-FLUX', 'Rss', 'done', 1, '` + arr + `', '` + pick + `', 0, 0)`,
		`INSERT INTO shadow_rss_pick (id, indexer, release_title, title_id, version_key, seen_at, quality, score, decision,
			status, grab_id) VALUES (1, 'prowlarr', 'Alpha.2020.2160p.WEB-DL-FLUX', 1, 'movie-remux', ? + 24000, 'WEBDL-2160p',
			1700, '` + ours + `', 'linked', 1)`,
		`INSERT INTO shadow_diff (at, title_id, version_key, bucket, reason, detail, searched, grab_id, rss_pick_id,
			created_at) VALUES (?, 1, 'movie-remux', 'we_grab_another', 'better_quality', 'old', 1, 1, 1, 0)`,
		`INSERT INTO router_plan (id, title_id, version_key, mode, outcome, steps, source, created_at) VALUES (3, 1,
			'movie-remux', 'shadow', 'none', '[{"release":"x","code":"private","outcome":"skipped"}]', 'shadow.grab', ?)`,
		`UPDATE shadow_grab SET router_plan_id = 3 WHERE id = 1 AND ? > 0`,
	} {
		if _, err := st.Writer().Exec(q, now); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()
	pol := filepath.Join("..", "..", "internal", "shadow", "testdata", "policy")
	var out bytes.Buffer
	if err := runShadow(commandSettingsArgs(t, []string{"reclassify", "-db", db, "-policy", pol}), &out, nil); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "reclassified 1 grabs: 1 changed") ||
		!strings.Contains(s, "we_grab_another/better_quality -> agree/same_rss_pick") {
		t.Errorf("reclassify:\n%s", s)
	}
	out.Reset()
	if err := runShadow(commandSettingsArgs(t, []string{"status", "-db", db}), &out, nil); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "same_rss_pick") || !strings.Contains(s, "(search found better: 1)") ||
		!strings.Contains(s, "vs rss; search found better: Remux-2160p 1950; router: would add nothing: private") {
		t.Errorf("status:\n%s", s)
	}
	if err := runShadow(commandSettingsArgs(t, []string{"reclassify", "-db", db, "-policy", pol, "extra"}), &out, nil); err == nil {
		t.Error("extra argument accepted")
	}
}
