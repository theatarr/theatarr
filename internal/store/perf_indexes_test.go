package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// 00132: cached provider_cache rows last 14 days from their check; rows expired more than a day ago then go once;
// live, recently expired and unexpiring rows stay.
// journal_decision gains its per-title and accepted indexes.
func TestMigrationPerfIndexes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.OpenAt(t, 131)
	db := st.Writer()
	now := time.Now()
	at := func(d time.Duration) int64 { return store.Millis(now.Add(d)) }
	if _, err := db.ExecContext(ctx, `INSERT INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at) VALUES
		('old', 'rd', 'miss', 'router:miss', 0, ?), ('recent', 'rd', 'miss', 'router:miss', 0, ?),
		('live', 'rd', 'miss', 'router:451', 0, ?), ('forever', 'rd', 'cached', 'add:rd', 0, NULL),
		('kept', 'rd', 'cached', 'feed:dmm', ?, ?), ('stale', 'ad', 'cached', 'feed:dmm', ?, ?)`,
		at(-48*time.Hour), at(-12*time.Hour), at(time.Hour), at(-3*24*time.Hour), at(-2*24*time.Hour),
		at(-16*24*time.Hour), at(-15*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.UpTo(ctx, 132); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		`SELECT group_concat(info_hash, ' ') FROM (SELECT info_hash FROM provider_cache ORDER BY info_hash)`: "forever kept live recent",
		// A cached row is kept to 14 days after its check.
		`SELECT CAST(expires_at - checked_at AS TEXT) FROM provider_cache WHERE info_hash = 'kept'`: "1209600000",
		`SELECT group_concat(name, ' ') FROM (SELECT name FROM sqlite_master WHERE type = 'index'
			AND name IN ('journal_decision_title', 'journal_decision_accepted') ORDER BY name)`: "journal_decision_accepted journal_decision_title",
	} {
		var got string
		if err := db.QueryRowContext(ctx, q).Scan(&got); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", q, got, err, want)
		}
	}
}
