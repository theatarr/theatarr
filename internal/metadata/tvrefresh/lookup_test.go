package tvrefresh

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/metadata/tvdb"
)

type lookupTVDB struct {
	TVDB
	series func(context.Context, int) (*tvdb.Series, error)
}

func (f lookupTVDB) Series(ctx context.Context, id int) (*tvdb.Series, error) {
	return f.series(ctx, id)
}

func TestLookupSeriesSourcesAndCache(t *testing.T) {
	t.Parallel()
	e := setup(t)
	clock := now
	e.svc.now = func() time.Time { return clock }
	ctx := context.Background()
	got, err := e.svc.LookupSeries(ctx, 371980)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Severance" || got.Year != 2022 || got.Runtime != 49 || got.IMDbID != "tt11280740" ||
		got.TMDbID != 95396 || got.Status != StatusContinuing || !reflect.DeepEqual(got.Seasons, []int{0, 1, 2}) {
		t.Fatalf("TVDB lookup: %+v", got)
	}
	if len(e.tvdb.reqs) != 1 {
		t.Fatalf("lookup fetched more than the series record: %v", e.tvdb.reqs)
	}
	got.Seasons[0] = 99
	again, err := e.svc.LookupSeries(ctx, 371980)
	if err != nil || again.Seasons[0] != 0 || len(e.tvdb.reqs) != 1 {
		t.Fatalf("cache: %+v, %v", again, err)
	}
	clock = clock.Add(5 * time.Minute)
	if _, err := e.svc.LookupSeries(ctx, 371980); err != nil {
		t.Fatal(err)
	}
	if len(e.tvdb.reqs) != 2 {
		t.Fatalf("cache did not expire: %v", e.tvdb.reqs)
	}
	// TVDB 404 falls back to the existing TMDb client, reading its series record only, including empty seasons.
	got, err = e.svc.LookupSeries(ctx, 800800)
	if err != nil || got.Title != "Only On TMDb" || got.Year != 2019 || got.Runtime != 30 ||
		got.TMDbID != 5000 || got.Status != StatusEnded || !reflect.DeepEqual(got.Seasons, []int{0, 1}) || e.tmdb.calls != 2 {
		t.Fatalf("TMDb lookup: %+v, %v; calls %d", got, err, e.tmdb.calls)
	}
	var n int
	if err := e.st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM title`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("lookup created %d titles", n)
	}
}

func TestLookupSeriesTimeoutAndFailure(t *testing.T) {
	e := setup(t)
	calls := 0
	e.svc.tvdb = lookupTVDB{series: func(ctx context.Context, _ int) (*tvdb.Series, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Fatal("lookup has no five-second timeout")
		}
		return nil, errors.New("source unavailable")
	}}
	for i := 0; i < 2; i++ {
		if _, err := e.svc.LookupSeries(context.Background(), 1); err == nil {
			t.Fatal("failed source returned success")
		}
	}
	if calls != 1 || e.tmdb.calls != 0 {
		t.Fatalf("failure cache or fallback: TVDB %d, TMDb %d calls", calls, e.tmdb.calls)
	}
	e.svc.tvdb = lookupTVDB{series: func(ctx context.Context, _ int) (*tvdb.Series, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Millisecond)
	defer cancel()
	if _, err := e.svc.LookupSeries(ctx, 2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	if _, ok := e.svc.lookups[2]; ok {
		t.Fatal("cached a caller's timeout")
	}
}
