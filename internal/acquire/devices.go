package acquire

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
)

// Hooks for the arr views' decision endpoints (release, release/push, parse, manualimport) and the native decision
// API: search and decide one target, or decide one release, for a version of a title. Nothing is routed (grabs go to the router, which writes only while Pause is off).

// ErrNoEngine means the acquirer was built without a decision engine.
var ErrNoEngine = errors.New("acquire: no decision engine")

// Interactive searches the version's indexers for a title (a season, or episodes, of a series) and decides every
// release, ranked: the arrs' interactive search. Releases maps every decision to its release.
func (a *Acquirer) Interactive(ctx context.Context, titleID int64, version string, season *int,
	episodes []int64) (*router.Searched, error) {
	got, err := a.InteractiveMany(ctx, titleID, []string{version}, season, episodes)
	if err != nil {
		return nil, err
	}
	return got[version], nil
}

// InteractiveMany searches the union of these versions' indexer queries and returns each version's own ranking.
func (a *Acquirer) InteractiveMany(ctx context.Context, titleID int64, versions []string, season *int,
	episodes []int64) (map[string]*router.Searched, error) {
	ctx = gateway.WithPurpose(decide.WithParseMemo(a.captureRuntime(ctx)), gateway.PurposeInteractive)
	type loaded struct {
		key    string
		title  decide.Title
		target router.SearchTarget
	}
	var ls []loaded
	for _, key := range versions {
		t, v, existing, err := a.load(ctx, titleID, key)
		if err != nil {
			return nil, err
		}
		var ss *decide.SeriesSearch
		whole := false
		if t.Kind == policy.KindSeries && season == nil && len(episodes) == 0 {
			// The whole series: the query without a season for packs of several seasons, over every aired episode
			// above season 0 (monitored or not).
			whole = true
			ss = wholeSeries(&t, a.now())
		} else if t.Kind == policy.KindSeries {
			ss = seriesSearch(&t, target{season: season, episodes: episodes})
			ss.Interactive, ss.MonitoredOnly = true, false
			if season != nil && len(episodes) == 0 {
				// A season named without episodes requests every episode of that season (Sonarr's season search;
				// season 0 included, which would otherwise read as no season).
				ss.Season = *season
				for _, e := range t.Episodes {
					if e.Season == *season {
						ss.Episodes = append(ss.Episodes, e.ID)
					}
				}
			}
		}
		ls = append(ls, loaded{key: key, title: t, target: router.SearchTarget{ID: key, Title: &t, Version: v,
			Existing: existing, Series: ss, WholeOnly: whole}})
	}
	if len(ls) == 0 {
		return map[string]*router.Searched{}, nil
	}
	var found map[string]*router.Searched
	var err error
	if a.searchBatch != nil {
		targets := make([]router.SearchTarget, len(ls))
		for i := range ls {
			targets[i] = ls[i].target
		}
		found, err = a.searchBatch(ctx, ls[0].title, targets)
	} else {
		found = make(map[string]*router.Searched, len(ls))
		for _, item := range ls {
			found[item.key], err = a.search(decide.WithSearch(ctx), item.title, item.target.Version, item.target.Existing, item.target.Series)
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return found, nil
}

// wholeSeries is the whole-series Interactive search's criterion: every aired episode above season 0.
func wholeSeries(t *decide.Title, now time.Time) *decide.SeriesSearch {
	ss := &decide.SeriesSearch{Kind: profile.SearchSeries, Interactive: true}
	for _, e := range t.Episodes {
		if e.Season > 0 && e.AirDateUTC != nil && e.AirDateUTC.Before(now) {
			ss.Episodes = append(ss.Episodes, e.ID)
		}
	}
	return ss
}

// DecideWhole decides one release of the whole-series Interactive search again for a version of a series, with
// that search's criterion (returned), as a grab by hand from that window is decided.
func (a *Acquirer) DecideWhole(ctx context.Context, rel decide.Release, titleID int64, version string) (*decide.Decision,
	*decide.SeriesSearch, error) {
	ctx, err := decide.WithStoredSeaDex(ctx, a.st.Reader())
	if err != nil {
		return nil, nil, err
	}
	ctx = a.captureRuntime(ctx)
	eng := a.eng
	if a.runtimeEngine != nil {
		eng = a.runtimeEngine(ctx)
	}
	if eng == nil {
		return nil, nil, ErrNoEngine
	}
	t, v, _, err := a.load(ctx, titleID, version)
	if err != nil {
		return nil, nil, err
	}
	if t.Kind != policy.KindSeries {
		return nil, nil, errors.New("acquire: a whole-series decision needs a series")
	}
	ss := wholeSeries(&t, a.now())
	d, err := eng.DecideSearch(decide.WithSearch(ctx), rel, t, v, ss)
	return d, ss, err
}

// Decide decides one release for a version of a title, as the RSS path does (no search criteria).
func (a *Acquirer) Decide(ctx context.Context, rel decide.Release, titleID int64, version string) (*decide.Decision,
	error) {
	ctx, err := decide.WithStoredSeaDex(ctx, a.st.Reader())
	if err != nil {
		return nil, err
	}
	ctx = a.captureRuntime(ctx)
	eng := a.eng
	if a.runtimeEngine != nil {
		eng = a.runtimeEngine(ctx)
	}
	if eng == nil {
		return nil, ErrNoEngine
	}
	t, v, existing, err := a.load(ctx, titleID, version)
	if err != nil {
		return nil, err
	}
	if t.Kind == policy.KindSeries {
		return eng.DecideSearch(ctx, rel, t, v, nil)
	}
	d, err := eng.Decide(ctx, rel, t, v, existing)
	if err != nil {
		return nil, err
	}
	available, err := availability.Movie(ctx, a.st.Reader(), a.pol, titleID, v.ID, version, a.now())
	if err != nil {
		return nil, err
	}
	if !available {
		d.Accepted = false
		d.Rejections = append(d.Rejections, decide.Rejection{Reason: "Availability", Message: "Movie has not reached minimum availability", Temporary: true})
	}
	return d, nil
}

func (a *Acquirer) load(ctx context.Context, titleID int64, version string) (decide.Title, decide.Version,
	*decide.ExistingFile, error) {
	db := a.st.Reader()
	t, err := decide.LoadTitle(ctx, db, titleID)
	if err != nil {
		return t, decide.Version{}, nil, err
	}
	v, err := decide.LoadVersion(ctx, db, a.pol, titleID, version)
	if err != nil {
		return t, v, nil, err
	}
	if v.Policy == nil {
		return t, v, nil, fmt.Errorf("acquire: unknown version %q", version)
	}
	var existing *decide.ExistingFile
	if t.Kind == policy.KindSeries {
		err = decide.LoadSeriesVersion(ctx, db, &t, v.ID)
	} else if v.ID != 0 {
		existing, err = decide.LoadExisting(ctx, db, v.ID)
	}
	return t, v, existing, err
}
