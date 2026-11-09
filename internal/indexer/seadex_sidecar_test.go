package indexer

import (
	"context"
	"os"
	"testing"
)

// A prefer-off SeaDex search now fails outright when the sidecar cannot be opened (read-only dir, sidecar owned by
// another user, ...). Before this range a prefer-off answer was still returned when standing could not be stored.
func TestPreferOffFailsWhenSidecarUnavailable(t *testing.T) {
	st := lockMappedStore(t)
	if err := os.Mkdir(st.Path()+".seadex-lock", 0o700); err != nil { // stands in for EACCES/EROFS
		t.Fatal(err)
	}
	svc := lockService(st, "seadex", func(context.Context, Query) (*Result, error) { return lockAnswer("a"), nil })
	if svc.ev.prefersSeaDex() {
		t.Fatal("fixture policy is not prefer-off")
	}
	res, err := svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1})
	if err != nil {
		t.Fatalf("prefer-off SeaDex search failed because the lock sidecar is unavailable: %v", err)
	}
	if res.Cached || res.Requests != 1 {
		t.Fatalf("result: %+v", res)
	}
	var count int
	if err := st.Reader().QueryRow(`SELECT count(*) FROM metadata_cache WHERE source='seadex-standing'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("standing written without lock: %d %v", count, err)
	}
}
