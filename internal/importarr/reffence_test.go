package importarr

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/torrents/reffence"
)

// The arr import writes its files under the torrent delete fence: refused while another process
// deletes a torrent, written once it is done.
func TestWriteRefusedDuringForeignTorrentDelete(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	im := &Importer{Store: st, Now: fixedNow}
	ctx := context.Background()
	if _, err := st.Writer().Exec(`INSERT INTO library_lease (name, owner, until) VALUES (?, 'pid 1 at 1', ?)`,
		reffence.LeaseName, store.Millis(fixedNow().Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	src := Source{Instance: "a", Version: "source"}
	if _, err := im.Write(ctx, src, &Snapshot{Instance: "a", Flavour: "radarr"}); !errors.Is(err, reffence.ErrDeleting) {
		t.Fatalf("write during another process's delete: %v", err)
	}
	if _, err := st.Writer().Exec(`DELETE FROM library_lease`); err != nil {
		t.Fatal(err)
	}
	if _, err := im.Write(ctx, src, &Snapshot{Instance: "a", Flavour: "radarr"}); err != nil {
		t.Fatal(err)
	}
}
