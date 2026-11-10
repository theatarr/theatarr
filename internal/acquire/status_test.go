package acquire

import (
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/router"
)

// A download added through a download client keeps provider NULL; the status text and the router's download list
// name the client instead.
func TestStatusNamesClientOfClientDownload(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	if _, _, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"movie"}, TitleIDs: []int64{1}}); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if _, err := e.st.Writer().ExecContext(e.ctx, `UPDATE download SET provider = NULL, client = 'decypharr' WHERE source = ?`,
		Source(1)); err != nil {
		t.Fatal(err)
	}
	st, err := LoadCommandStatus(e.ctx, e.st.Reader(), 1)
	if err != nil || !strings.Contains(st.Message, "added One.2024.2160p.UHD.BluRay.REMUX.HEVC-GRP on decypharr (") {
		t.Fatalf("status %+v %v", st, err)
	}
	rows, err := router.Downloads(e.ctx, e.st.Reader(), "", 0)
	if err != nil || len(rows) != 1 || rows[0].Provider != "decypharr" {
		t.Fatalf("downloads %+v %v", rows, err)
	}
}
