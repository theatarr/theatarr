package retention

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// keptByDesign are the tables retention leaves alone: configuration, catalog and library state, per-key state that
// is overwritten in place, and the memories that must not be forgotten (see the package comment). A new table must be
// pruned by a rule, cascade-deleted with a pruned row, or listed here with its reason, so that no table grows without
// bound unnoticed.
var keptByDesign = map[string]string{}

func init() {
	for _, tb := range strings.Fields(`api_key arr_instance arr_policy_snapshot arr_quality_profile arr_tag arrview_notification arrview_profile arrview_tag library_naming import_list plex_playlist web_setting web_user setting_budget setting_custom_format setting_format_set setting_global_switches setting_indexer setting_profile_default setting_provider setting_quality_profile setting_quality_sync setting_secret setting_section setting_version setting_version_naming setting_view_binding settings_e3_bootstrap settings_generation settings_quality_state settings_runtime_state settings_sections_bootstrap settings_switches_bootstrap`) {
		keptByDesign[tb] = "configuration and settings, changed by hand"
	}
	for _, tb := range strings.Fields(`anime_alt_cut anime_discovery anime_entry anime_franchise anime_mapping anime_node anime_order anime_review anime_stage arr_alias arr_title arrview_removed_version arrview_title arrview_title_tag content content_file content_provider content_version episode episode_number episode_version_state link link_episode link_trash link_version movie_collection movie_exclusion series_meta series_monitor source_ceiling title title_alias title_external_id title_meta title_release_date title_scene_mapping version version_ceiling version_file version_file_episode version_source request request_user arr_blocklist`) {
		keptByDesign[tb] = "catalog and library state: grows with the library, not with runs"
	}
	for _, tb := range strings.Fields(`router_pacing gateway_backoff gateway_budget_window shadow_rss_cursor notify_state tvdb_sync scene_refresh library_lease library_dangling_scan cardigann_session search_request series_search_wait error_inbox_state`) {
		keptByDesign[tb] = "state per key, overwritten in place"
	}
	for _, tb := range strings.Fields(`job journal_decision import_run shadow_run ceiling_search ceiling_search_indexer web_session source_sighting library_dangling router_refusal_451 arrview_command shadow_rss_seen notify_rename torrent_restore_pending gateway_indexer_request`) {
		keptByDesign[tb] = "pruned by the package that owns it"
	}
	for _, tb := range strings.Fields(`router_offer notify_event arr_history router_blocklist journal_action journal_verdict torrent_journal`) {
		keptByDesign[tb] = "kept by design (package comment, journal): a memory or record that must not be forgotten"
	}
	for _, tb := range strings.Fields(`release decision`) {
		keptByDesign[tb] = "from the first schema; nothing writes it"
	}
}

// metadata_cache has a selective rule: query rows are pruned; detail bodies and validators stay for offline reads.
//
// TestRetentionCoversEveryTable: every table the migrations create is pruned by a rule, goes with a pruned
// row (ON DELETE CASCADE, or deleted by the rule's statement), or is kept by design.
func TestRetentionCoversEveryTable(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	ctx := context.Background()
	rows, err := st.Reader().QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'
		AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'goose_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	pruned := map[string]bool{}
	for _, r := range rules {
		pruned[r.table] = true
	}
	// A table whose rows cascade from a pruned table's goes with it.
	for changed := true; changed; {
		changed = false
		for _, tb := range tables {
			if pruned[tb] {
				continue
			}
			fks, err := st.Reader().QueryContext(ctx, `SELECT "table", on_delete FROM pragma_foreign_key_list(?)`, tb)
			if err != nil {
				t.Fatal(err)
			}
			for fks.Next() {
				var parent, onDelete string
				if err := fks.Scan(&parent, &onDelete); err != nil {
					t.Fatal(err)
				}
				if pruned[parent] && onDelete == "CASCADE" {
					pruned[tb], changed = true, true
				}
			}
			fks.Close()
		}
	}
	var missing []string
	for _, tb := range tables {
		if !pruned[tb] && keptByDesign[tb] == "" {
			missing = append(missing, tb)
		}
	}
	if len(missing) > 0 {
		t.Errorf("tables neither pruned nor kept by design (add a retention rule or a keptByDesign reason): %v", missing)
	}
	for tb := range keptByDesign {
		if !slices.Contains(tables, tb) {
			t.Errorf("keptByDesign names %s, which no migration creates", tb)
		}
		if pruned[tb] {
			t.Errorf("%s is pruned and also listed as kept by design", tb)
		}
	}
}

// TestPruneProviderCache: provider_cache rows expired for longer than the runs window go; live
// rows, recently expired ones and rows without an expiry stay.
func TestPruneProviderCache(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	now := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	at := func(d time.Duration) int64 { return store.Millis(now.Add(d)) }
	exec(t, st, `INSERT INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at) VALUES
		('old', 'rd', 'cached', 'feed:dmm', 0, ?), ('recent', 'rd', 'miss', 'router:miss', 0, ?),
		('live', 'rd', 'miss', 'router:451', 0, ?), ('forever', 'rd', 'cached', 'add:rd', 0, NULL)`,
		at(-60*24*time.Hour), at(-24*time.Hour), at(time.Hour))
	sum, err := Prune(context.Background(), st, now, Defaults)
	if err != nil {
		t.Fatal(err)
	}
	if sum["provider_cache"] != 1 || count(t, st, `SELECT COUNT(*) FROM provider_cache WHERE info_hash = 'old'`) != 0 {
		t.Fatalf("pruned %v", sum)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM provider_cache`); n != 3 {
		t.Fatalf("%d rows kept, want 3", n)
	}
}

// Query strings create new search/discovery keys; expired ones must drain in bounded batches.
// Detail responses and other sources keep their offline/conditional-refresh evidence.
func TestPruneMetadataQueries(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	now := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	at := store.Millis(now)
	for i := range batchSize + 3 {
		exec(t, st, `INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at)
   VALUES ('tmdb', ?, 200, x'00', ?, ?)`, fmt.Sprintf("/search/multi?query=arbitrary%d", i), at-2, at-1)
	}
	for _, key := range []string{"/search/movie?query=one&page=1", "/search/tv?query=two&page=2"} {
		exec(t, st, `INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at)
   VALUES ('tmdb', ?, 200, x'00', ?, ?)`, key, at-2, at-1)
	}
	for _, row := range []struct {
		source, key string
		expires     int64
	}{
		{"tmdb", "/search/multi?query=fresh", at + 1},
		{"tmdb", "/movie/12?append_to_response=external_ids", at - 1},
		{"tmdb", "/tv/34", 0},
		{"tvdb", "GET https://tvdb.invalid/series/12/extended", 0},
		{"anilist", "POST https://anilist.invalid abc", at - 1},
	} {
		exec(t, st, `INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at)
   VALUES (?, ?, 200, x'00', ?, ?)`, row.source, row.key, at-2, row.expires)
	}
	cfg := Defaults
	cfg.MaxBatches = 1
	sum, err := Prune(context.Background(), st, now, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if sum["metadata_cache"] != batchSize {
		t.Fatalf("first bounded prune: %v", sum)
	}
	sum, err = Prune(context.Background(), st, now, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if sum["metadata_cache"] != 5 {
		t.Fatalf("remaining expired queries: %v", sum)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM metadata_cache`); n != 5 {
		t.Fatalf("kept %d rows, want 5", n)
	}
}

// TestPruneErrorInbox: resolved and ignored error inbox rows go after the runs window; open rows stay whatever their
// age, and so does a resolved row seen again recently.
func TestPruneErrorInbox(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	now := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	old, recent := store.Millis(now.Add(-40*24*time.Hour)), store.Millis(now.Add(-24*time.Hour))
	for _, r := range []struct {
		fp, status string
		last       int64
	}{{"old-resolved", "resolved", old}, {"old-ignored", "ignored", old}, {"old-new", "new", old},
		{"old-seen", "seen", old}, {"recent-resolved", "resolved", recent}} {
		exec(t, st, `INSERT INTO error_event (fingerprint, source, message, first_seen, last_seen, status, resolved_at,
			updated_at) VALUES (?, 'log', 'm', ?, ?, ?, ?, ?)`, r.fp, r.last, r.last, r.status, r.last, r.last)
	}
	sum, err := Prune(context.Background(), st, now, Defaults)
	if err != nil {
		t.Fatal(err)
	}
	if sum["error_event"] != 2 {
		t.Errorf("pruned %d error_event rows, want 2", sum["error_event"])
	}
	var left []string
	rows, err := st.Reader().Query(`SELECT fingerprint FROM error_event ORDER BY fingerprint`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var fp string
		rows.Scan(&fp)
		left = append(left, fp)
	}
	if strings.Join(left, ",") != "old-new,old-seen,recent-resolved" {
		t.Errorf("left %v", left)
	}
}
