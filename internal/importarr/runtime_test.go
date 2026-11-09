package importarr

import (
	"net/http/httptest"
	"testing"
)

func TestRadarrRuntimeImportedAndRefreshed(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	fake := newFake("/mnt/movies-a")
	fake.movies[0]["runtime"] = 87
	srv := httptest.NewServer(fake)
	defer srv.Close()
	im := &Importer{Store: st, Now: fixedNow}
	src := Source{Instance: "radarr_a", Flavour: "radarr", Version: "movie-remux", Library: "/mnt/movies-a"}
	for _, runtime := range []int{87, 90, 0} {
		fake.mu.Lock()
		fake.movies[0]["runtime"] = runtime
		fake.mu.Unlock()
		runImport(t, im, src, srv)
		want := runtime
		if want == 0 {
			want = 90 // an unknown response must not erase the last known runtime
		}
		if got := one[int](t, st, `SELECT runtime_minutes FROM title WHERE tmdb_id=100`); got != want {
			t.Fatalf("imported runtime %d, want %d", got, want)
		}
	}
}
