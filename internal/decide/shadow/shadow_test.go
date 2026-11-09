package shadow

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

var instances = []struct{ name, version string }{{"radarr_4k_debrid", "movie-remux"}, {"radarr_4k_web", "movie-web"}}

// seriesInstances are replayed as series; THEATARR_SERIES_ALIASES may name a directory with <instance>.series.json (a saved
// GET series answer) for the scene mappings the corpus lacks.
var seriesInstances = []struct{ name, version string }{{"sonarr_4k_debrid", "tv-2160p"}, {"sonarr_anime_debrid", "anime-tv"}}

func seriesAliases(t *testing.T, instance string) map[int][]decide.SceneMapping {
	dir := os.Getenv("THEATARR_SERIES_ALIASES")
	if dir == "" {
		return nil
	}
	a, err := SeriesAliases(filepath.Join(dir, instance+".series.json"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func engine(t *testing.T, corpus string) *decide.Engine {
	t.Helper()
	pol, err := policy.Load("testdata/policy")
	if err != nil {
		t.Fatal(err)
	}
	e, err := decide.New(pol, decide.Options{Arr: decide.SnapshotDir(corpus)})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// TestCorpusParity replays the corpus grabs (THEATARR_CORPUS). It reports; it does not fail on disagreement.
func TestCorpusParity(t *testing.T) {
	corpus := os.Getenv("THEATARR_CORPUS")
	if corpus == "" {
		t.Skip("THEATARR_CORPUS not set")
	}
	e := engine(t, corpus)
	for _, in := range instances {
		grabs, err := LoadCorpus(corpus, in.name)
		if err != nil {
			t.Fatal(err)
		}
		rep, _ := Run(context.Background(), e, in.version, grabs)
		t.Logf("corpus\n%s", rep.Format())
		if rep.Grabs == 0 || rep.Errors > 0 {
			t.Errorf("%s: %d grabs, %d errors", in.name, rep.Grabs, rep.Errors)
		}
	}
	for _, in := range seriesInstances {
		grabs, err := LoadCorpusSeries(corpus, in.name, seriesAliases(t, in.name))
		if err != nil {
			t.Fatal(err)
		}
		rep, _ := Run(context.Background(), e, in.version, grabs)
		t.Logf("corpus\n%s", rep.Format())
		if rep.Grabs == 0 || rep.Errors > 0 {
			t.Errorf("%s: %d grabs, %d errors", in.name, rep.Grabs, rep.Errors)
		}
	}
}

// TestDBParity replays the imported history (THEATARR_DECIDE_DB, a copy of an theatarr import-arr database; THEATARR_CORPUS for the
// arr snapshots and alternative titles).
func TestDBParity(t *testing.T) {
	dbPath, corpus := os.Getenv("THEATARR_DECIDE_DB"), os.Getenv("THEATARR_CORPUS")
	if dbPath == "" || corpus == "" {
		t.Skip("THEATARR_DECIDE_DB and THEATARR_CORPUS not set")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dbPath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine(t, corpus)
	lib, err := LoadLibrary(ctx, st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	e.SetLibrary(lib)
	for _, in := range instances {
		aliases, err := CorpusAliases(corpus, in.name)
		if err != nil {
			t.Fatal(err)
		}
		grabs, err := LoadDB(ctx, st.Reader(), in.name, aliases)
		if err != nil {
			t.Fatal(err)
		}
		rep, _ := Run(ctx, e, in.version, grabs)
		t.Logf("database\n%s", rep.Format())
		if rep.Grabs == 0 || rep.Errors > 0 {
			t.Errorf("%s: %d grabs, %d errors", in.name, rep.Grabs, rep.Errors)
		}
	}
}
