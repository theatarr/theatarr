package router

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// TestUnblock: the row goes and comes back as it was; the other version's entry for the same hash stays; an unknown
// entry is sql.ErrNoRows.
func TestUnblock(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	ctx := context.Background()
	hash := strings.Repeat("ab", 20)
	err := e.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := Blocklist(ctx, tx, "movie-web", hash, 0, 0, "Rel.A", "sample", BlocklistLibrary, 1); err != nil {
			return err
		}
		return Blocklist(ctx, tx, "movie-remux", hash, 0, 0, "Rel.A", "sample", BlocklistLibrary, 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	var b *Blocked
	err = e.st.WriteTx(ctx, func(tx *sql.Tx) error {
		b, err = Unblock(ctx, tx, "movie-web", strings.ToUpper(hash))
		return err
	})
	if err != nil || b.Release != "Rel.A" || b.Source != BlocklistLibrary || b.Hash != hash {
		t.Fatalf("unblock: %+v %v", b, err)
	}
	var n int
	if err := e.st.Reader().QueryRow(`SELECT COUNT(*) FROM router_blocklist WHERE info_hash = ?`, hash).Scan(&n); err != nil || n != 1 {
		t.Errorf("rows left %d %v", n, err)
	}
	err = e.st.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := Unblock(ctx, tx, "movie-web", hash)
		return err
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("second unblock: %v", err)
	}
}
