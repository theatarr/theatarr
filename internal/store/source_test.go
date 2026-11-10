package store

import (
	"context"
	"testing"
)

func TestSourceSettings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openTest(t, Options{})
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'series', 'Show', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at)
		 VALUES (1, 1, 'source', 0, 0), (2, 1, 'follower', 0, 0), (3, 1, 'native', 0, 0)`,
		`INSERT INTO arr_instance (name, flavour, policy_key, created_at, updated_at)
		 VALUES ('a', 'sonarr', 'source', 0, 0), ('b', 'sonarr', 'source', 0, 0)`,
		`INSERT INTO arr_title (instance, arr_id, kind, title_id, version_id, monitored, series_type, minimum_availability)
		 VALUES ('a', 1, 'series', 1, 1, 1, 'standard', 'released'), ('b', 2, 'series', 1, 1, 1, 'anime', 'announced')`,
		`INSERT INTO version_source (version_id, instance, arr_id) VALUES (1, 'b', 2)`,
		`INSERT INTO version_follow (version_id, follows_id) VALUES (2, 1)`,
	} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		id   int64
		want SourceSettings
	}{{1, SourceSettings{"announced"}}, {2, SourceSettings{"announced"}}, {3, SourceSettings{}}, {0, SourceSettings{}}} {
		got, err := LoadSourceSettings(ctx, st.Reader(), tc.id)
		if err != nil || got != tc.want {
			t.Errorf("version %d: %+v, %v; want %+v", tc.id, got, err, tc.want)
		}
	}
	if _, err := st.Writer().Exec(`DELETE FROM version_source`); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadSourceSettings(ctx, st.Reader(), 2); err != nil || got.MinimumAvailability != "released" {
		t.Errorf("fallback imported view: %+v, %v", got, err)
	}
}

func TestFollowfixMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openAt(t, 51)
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 50); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`SELECT version_id, follows_id FROM version_follow`); err == nil {
		t.Fatal("version_follow survived Down")
	}
	if _, err := p.UpTo(ctx, 51); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Movie', 0, 0);
		INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'source', 0, 0), (2, 1, 'theatarr', 0, 0);
		INSERT INTO version_follow VALUES (2, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO version_follow VALUES (1, 1)`); err == nil {
		t.Error("self-follow accepted")
	}
	if _, err := st.Writer().Exec(`DELETE FROM version WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM version_follow`).Scan(&n); err != nil || n != 0 {
		t.Errorf("relationship did not cascade: %d, %v", n, err)
	}
}
