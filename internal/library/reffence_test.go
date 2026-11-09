package library

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/torrents/reffence"
)

// A link write is refused as ErrLinkChanged while another process deletes a torrent: the import
// retries once the delete's lease is gone.
func TestRefWriteTxRefusedDuringForeignDelete(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	if _, err := e.st.Writer().Exec(`INSERT INTO library_lease (name, owner, until) VALUES (?, 'pid 1 at 1', ?)`,
		reffence.LeaseName, store.Millis(e.now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	ran := false
	err := e.lib.refWriteTx(e.ctx, func(*sql.Tx) error { ran = true; return nil })
	if !errors.Is(err, ErrLinkChanged) || !errors.Is(err, reffence.ErrDeleting) || ran {
		t.Fatalf("refWriteTx during another process's delete: %v (ran %v)", err, ran)
	}
	if _, err := e.st.Writer().Exec(`DELETE FROM library_lease`); err != nil {
		t.Fatal(err)
	}
	if err := e.lib.refWriteTx(e.ctx, func(*sql.Tx) error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("after the delete: %v (ran %v)", err, ran)
	}
}
