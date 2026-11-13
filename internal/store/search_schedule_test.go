package store_test

import (
	"context"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// 00126: a pending Search now request stays due and asked, the queued drains and retry chains of new content go,
// other jobs stay, and the settings are not touched.
func TestMigrationSearchSchedule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.OpenAt(t, 125)
	db := st.Writer()
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'One', 0, 0)`,
		`INSERT INTO search_request (title_id, version_key, reason, season, created_at) VALUES (1, 'movie', 'missing', -1, 5000)`,
		`INSERT INTO setting_section (key, config, created_at, updated_at) VALUES ('router',
			'{"wanted":{"again_after":"24h0m0s","cutoff_unmet":"off","enabled":null,"interval":"15m0s","missing":false,"per_hour":10}}', 1, 1)`,
		`INSERT INTO job (kind, payload, state, dedupe_key, run_at, created_at, updated_at) VALUES
			('acquire.searchnow', '{}', 'queued', 'a', 0, 0, 0),
			('acquire.searchnow', '{}', 'done', 'b', 0, 0, 0),
			('acquire.research', '{"reason":"fresh","retry_new":true}', 'queued', 'c', 0, 0, 0),
			('acquire.research', '{"reason":"stalled"}', 'queued', 'd', 0, 0, 0),
			('acquire.search', '{"run_id":1,"retry_new":true}', 'queued', 'e', 0, 0, 0)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var before string
	if err := db.QueryRowContext(ctx, `SELECT config FROM setting_section WHERE key = 'router'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := st.UpTo(ctx, 126); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		`SELECT next_at || ' ' || tries || ' ' || why FROM search_request`:                           "5000 0 asked",
		`SELECT group_concat(dedupe_key, ' ') FROM (SELECT dedupe_key FROM job ORDER BY dedupe_key)`: "b d e",
		`SELECT config FROM setting_section WHERE key = 'router'`:                                    before,
	} {
		var got string
		if err := db.QueryRowContext(ctx, q).Scan(&got); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", q, got, err, want)
		}
	}
}

// 00127: router.wanted loses enabled, interval and retry_new. A stored enabled false turns both backlog switches off;
// a stored null (the owner's shape) or no enabled keeps the switches as stored. The section then decodes strictly.
func TestMigrationWantedSettings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, stored, want string }{
		{"owner", `{"wanted":{"again_after":"24h0m0s","cutoff_unmet":"off","enabled":null,"interval":"15m0s","missing":false,"per_hour":10}}`,
			`{"wanted":{"again_after":"24h0m0s","cutoff_unmet":"off","missing":false,"per_hour":10}}`},
		{"null keeps on", `{"wanted":{"enabled":null,"missing":true,"cutoff_unmet":"quality","retry_new":false}}`,
			`{"wanted":{"missing":true,"cutoff_unmet":"quality"}}`},
		{"off", `{"enabled":true,"wanted":{"enabled":false,"interval":"30m0s","missing":true,"cutoff_unmet":"score"}}`,
			`{"enabled":true,"wanted":{"missing":false,"cutoff_unmet":"off"}}`},
		{"on", `{"wanted":{"enabled":true,"per_hour":5}}`, `{"wanted":{"per_hour":5}}`},
		{"no wanted", `{"enabled":true}`, `{"enabled":true}`},
		{"wanted null", `{"wanted":null}`, `{"wanted":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			st := store.OpenAt(t, 126)
			db := st.Writer()
			if _, err := db.ExecContext(ctx, `INSERT INTO setting_section (key, config, created_at, updated_at)
				VALUES ('router', ?, 1, 1)`, tc.stored); err != nil {
				t.Fatal(err)
			}
			if err := st.UpTo(ctx, 127); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := db.QueryRowContext(ctx, `SELECT json(config) FROM setting_section WHERE key = 'router'`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			var want string
			if err := db.QueryRowContext(ctx, `SELECT json(?)`, tc.want).Scan(&want); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("got %s, want %s", got, want)
			}
			var r policy.Router
			dec := yaml.NewDecoder(strings.NewReader(got))
			dec.KnownFields(true)
			if err := dec.Decode(&r); err != nil {
				t.Fatalf("strict decode of %s: %v", got, err)
			}
		})
	}
}
