package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// router reconcile is a dry run by default: it lists a legacy queued download and writes nothing.
func TestRouterReconcileDryRunByDefault(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "theatarr.db")
	st, err := store.Open(ctx, storetest.Seed(t, db), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Movie', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (10, 1, 'movie', 0, 0)`,
		`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, source,
			created_at, updated_at) VALUES (5, 1, 10, 'movie', 'aa', 'Movie.2024', 'pub', 'queued', 'wanted', 0, 0)`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	var out bytes.Buffer
	if err := runRouter([]string{"reconcile", "-db", db}, &out, nil); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "download  5") || !strings.Contains(s, "would end 1 automatic") ||
		!strings.Contains(s, "dry run: nothing written") {
		t.Fatalf("reconcile:\n%s", s)
	}
	st, err = store.Open(ctx, db, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var state string
	if err := st.Reader().QueryRow(`SELECT state FROM download WHERE id = 5`).Scan(&state); err != nil || state != "queued" {
		t.Fatalf("dry run changed the download: %q %v", state, err)
	}
}
