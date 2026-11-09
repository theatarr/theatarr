package library

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// The relink sweep asks the registry about each healthy link's folder. That lookup must be an index search, not a
// scan of content or content_provider per link (e2eperf 3).
func TestIdentityLookupUsesIndexes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n := "Release"
	rows, err := st.Reader().QueryContext(ctx, "EXPLAIN QUERY PLAN "+identityQuery,
		n, n, n, n, n+".mkv", n+".mp4", n, n+".mkv", n+".mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, d := range plan {
		if strings.HasPrefix(d, "SCAN ") && !strings.Contains(d, "USING") {
			t.Fatalf("identity lookup scans a table: %q\nplan: %q", d, plan)
		}
	}
}
