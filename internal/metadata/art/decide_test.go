package art

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestMissingTMDbRuntimePreservesKnownRuntime(t *testing.T) {
	for _, previousRefresh := range []bool{false, true} {
		t.Run(fmt.Sprint(previousRefresh), func(t *testing.T) {
			e := newTestEnv(t)
			e.seed(t)
			e.exec(t, `UPDATE title SET runtime_minutes=136 WHERE id=1`)
			ctx := context.Background()
			if previousRefresh {
				if err := e.svc.FillTitle(ctx, 1); err != nil {
					t.Fatal(err)
				}
			}
			m := Meta{TitleID: 1, TMDbID: 603, RefreshedAt: &testNow, AttemptedAt: testNow, RefreshAfter: testNow}
			if err := e.svc.write(ctx, &m, &tmdb.Movie{Title: "The Matrix", Runtime: 0}); err != nil {
				t.Fatal(err)
			}
			title, err := decide.LoadTitle(ctx, e.st.Reader(), 1)
			if err != nil || title.Runtime != 136 {
				t.Fatalf("missing metadata erased known runtime: %+v %v", title, err)
			}
		})
	}
}

// The native add path needs no arr import or response cache for decision metadata.
func TestCatalogRefreshDecisionMetadata(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	var id int64
	if err := e.st.WriteTx(ctx, func(tx *sql.Tx) error {
		r, err := catalog.Add(ctx, tx, catalog.Input{Kind: policy.KindMovie, TmdbID: 603,
			Title: "The Matrix", Monitored: true, Versions: func(bool) ([]string, error) {
				return []string{"movie-web"}, nil
			}}, testNow, nil)
		if err == nil {
			id = r.TitleID
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO title_alias (title_id, source, kind, name) VALUES
		(?, 'radarr', 'alternative', 'Imported alias'), (?, 'tmdb', 'alternative', 'Stale alias')`, id, id)
	w := jobs.NewWorker(e.q, jobs.WorkerOptions{})
	e.svc.RegisterJobs(w)
	for range 2 {
		if _, err := e.svc.Refresh(ctx, id); err != nil {
			t.Fatal(err)
		}
		if ran, err := w.RunOne(ctx); err != nil || !ran {
			t.Fatalf("refresh ran=%v: %v", ran, err)
		}
		title, err := decide.LoadTitle(ctx, e.st.Reader(), id)
		if err != nil {
			t.Fatal(err)
		}
		if title.Runtime != 136 || title.OriginalLanguage != "English" || title.OriginalTitle != "The Matrix" ||
			!slices.Contains(title.Aliases, "Matrix Alternative") || !slices.Contains(title.Aliases, "Matrix Traduit") ||
			!slices.Contains(title.Aliases, "Imported alias") || slices.Contains(title.Aliases, "Stale alias") {
			t.Fatalf("decision title: %+v", title)
		}
		movies, err := decide.LoadMovies(ctx, e.st.Reader())
		if err != nil || movies[id].Runtime != 136 || movies[id].OriginalLanguage != "English" ||
			movies[id].OriginalTitle != title.OriginalTitle || !slices.Equal(movies[id].Aliases, title.Aliases) {
			t.Fatalf("LoadMovies: %+v, %v", movies[id], err)
		}
	}
	var cached int
	if err := e.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM metadata_cache WHERE source = 'tmdb'`).Scan(&cached); err != nil || cached != 0 {
		t.Fatalf("unexpected response cache: %d %v", cached, err)
	}
	// A failed refresh preserves the last good metadata and names.
	e.fake.mu.Lock()
	e.fake.fail = true
	e.fake.mu.Unlock()
	if err := e.svc.FillTitle(ctx, id); err == nil {
		t.Fatal("expected TMDb failure")
	}
	title, err := decide.LoadTitle(ctx, e.st.Reader(), id)
	if err != nil || title.Runtime != 136 || !slices.Contains(title.Aliases, "Matrix Traduit") {
		t.Fatalf("after failed refresh: %+v, %v", title, err)
	}
}
