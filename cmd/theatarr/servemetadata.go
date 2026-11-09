package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/metadata/art"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/metadata/tvrefresh"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/web"
)

// serveMetadata is one settings generation's metadata services: the TVDB client, the series refresh, title art and
// the image cache, the movie intake, search and add's TMDb client, Plex sign-in and the arr views' TMDb lookups. They
// read the metadata, ui and library.plex sections and the metadata source keys, so a save of those applies at once.
//
// A generation reuses the previous generation's services whose inputs did not change (the *Key fields), so a save of
// an unrelated section keeps their clients, rate limiters and loops (serveliveworkers.go keys the loops on them).
type serveMetadata struct {
	tmdbKey, tvdbKey, artKey, tvKey, plexKey, intakeKey string

	tvc       *tvdb.Client
	tv        *tvrefresh.Service
	art       *art.Service
	refresher *titleRefresher
	intake    *catalog.MovieIntake
	// listInterval is metadata.lists.interval: the movie intake's schedule.
	listInterval time.Duration
	add          web.AddConfig
	// searchBuilt: add and viewTMDb were built for tmdbKey.
	searchBuilt bool
	plex        web.PlexLogin
	// plexBuilt: plex was built for plexKey (it may be nil: sign-in off).
	plexBuilt bool
	viewTMDb  arrview.TMDb
}

// serveMetadataDeps is what every generation's metadata services share. auth and acquirer are set once serve has
// built them, before any settings reload.
type serveMetadataDeps struct {
	st       *store.Store
	q        *jobs.Queue
	dbPath   string
	log      *slog.Logger
	auth     *auth.Service
	acquirer *serveAcquirer
}

// serveMetadataKeys are the inputs of each metadata service in pol. Secrets enter only as hashes.
type serveMetadataKeys struct{ tmdb, tvdb, art, tv, plex, intake string }

func metadataKeys(pol *policy.Policy, dbPath string) serveMetadataKeys {
	k := serveMetadataKeys{tmdb: metadataSourceKey(pol, "tmdb"), tvdb: metadataSourceKey(pol, tvdb.Source)}
	k.art = keyOf(k.tmdb, metadataSourceKey(pol, policy.TMDbImagesSource), pol.ArtSettings(),
		pol.ImageSettings(filepath.Dir(dbPath)))
	var sources map[string]*policy.MetadataSource
	if pol.Metadata != nil {
		sources = pol.Metadata.Sources
	}
	k.tv = keyOf(k.tvdb, k.tmdb, pol.SeriesMetaSettings(), sources, pol.ArrFedInstances())
	var plexURL, plexToken string
	if p := pol.LibrarySettings().Plex; p != nil {
		plexURL, plexToken = p.URL, secretHash(pol.Getenv(p.TokenEnv))
	}
	k.plex = keyOf(metadataSourceKey(pol, policy.PlexTVSource), plexURL, plexToken)
	if k.tmdb != "" {
		k.intake = keyOf(k.tmdb, pol.ListSettings().Interval)
	}
	return k
}

// metadataSourceKey is metadata.sources.<name> with a hash of its key; "" when the source is not set.
func metadataSourceKey(pol *policy.Policy, name string) string {
	src := metadataSource(pol, name)
	if src == nil {
		return ""
	}
	return keyOf(src, secretHash(pol.Getenv(src.APIKeyEnv)))
}

func secretHash(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

func keyOf(parts ...any) string {
	b, err := json.Marshal(parts)
	if err != nil {
		panic(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:12])
}

// prepareServeMetadata builds the metadata services of a generation with policy pol and gateway gw, reusing prev's
// where their inputs are unchanged.
func prepareServeMetadata(prev *serveMetadata, pol *policy.Policy, gw *gateway.Gateway, d *serveMetadataDeps) (*serveMetadata, error) {
	if prev == nil {
		prev = &serveMetadata{}
	}
	k := metadataKeys(pol, d.dbPath)
	m := &serveMetadata{tmdbKey: k.tmdb, tvdbKey: k.tvdb, artKey: k.art, tvKey: k.tv, plexKey: k.plex, intakeKey: k.intake,
		listInterval: pol.ListSettings().Interval}
	var err error
	if prev.tvc != nil && prev.tvdbKey == k.tvdb {
		m.tvc = prev.tvc
	} else if m.tvc, err = newTVDBClient(pol, d.st, gw); err != nil {
		return nil, err
	}
	if prev.art != nil && prev.artKey == k.art {
		m.art = prev.art
	} else if m.art, err = newServeArt(pol, d.st, d.q, gw, d.dbPath, d.log); err != nil {
		return nil, err
	}
	if prev.tv != nil && prev.tvKey == k.tv && prev.tvc == m.tvc {
		m.tv = prev.tv
	} else {
		if m.tv, err = newServeSeriesMeta(pol, d.st, d.q, gw, m.tvc, d.log); err != nil {
			return nil, err
		}
		if m.tv.Enabled() && d.acquirer != nil {
			m.tv.SetSearches(d.acquirer)
		}
	}
	m.refresher = &titleRefresher{Service: m.art, tv: m.tv, st: d.st}
	if prev.searchBuilt && prev.tmdbKey == k.tmdb {
		m.add, m.viewTMDb, m.searchBuilt = prev.add, prev.viewTMDb, true
	} else {
		m.searchBuilt = true
		var ac web.Acquirer
		if d.acquirer != nil {
			ac = d.acquirer
		}
		if m.add, err = newServeAdd(pol, d.st, gw, ac, d.log); err != nil {
			return nil, err
		}
		if m.viewTMDb, err = newServeViewTMDb(pol, d.st, gw); err != nil {
			return nil, err
		}
	}
	// The intake reads the versions too, so each generation gets its own; its TMDb client is kept while the source is.
	if k.intake != "" {
		if prev.intake != nil && prev.tmdbKey == k.tmdb {
			m.intake = &catalog.MovieIntake{Store: d.st, Policy: pol, TMDb: prev.intake.TMDb}
		} else if m.intake, err = newServeMovieIntake(pol, d.st, gw); err != nil {
			return nil, err
		}
	}
	if m.intake != nil {
		m.intake.Now, m.intake.Log = time.Now, d.log
		if d.acquirer != nil {
			m.intake.OnAdd = movieIntakeSearch(d.acquirer)
		}
	}
	if prev.plexBuilt && prev.plexKey == k.plex {
		m.plex, m.plexBuilt = prev.plex, true
	} else if d.auth != nil {
		if m.plex, err = newPlexLogin(pol, d.auth, gw, d.log); err != nil {
			return nil, err
		}
		m.plexBuilt = true
	}
	return m, nil
}

// movieIntakeSearch starts the add-time search of a movie the intake added.
func movieIntakeSearch(ac *serveAcquirer) func(context.Context, int64, []string) error {
	return func(ctx context.Context, titleID int64, versions []string) error {
		_, _, err := ac.Submit(ctx, 0, acquire.Selection{TitleIDs: []int64{titleID}, Versions: versions})
		return err
	}
}

// runMovieIntake refreshes Radarr collections and TMDb import lists every interval with the intake of the settings in
// force at each refresh, and once at start when now is true.
func runMovieIntake(ctx context.Context, interval time.Duration, now bool, current func(context.Context) *catalog.MovieIntake,
	log *slog.Logger) {
	if interval <= 0 {
		interval = policy.DefaultListInterval
	}
	refresh := func() {
		mi := current(ctx)
		if mi == nil {
			return
		}
		if _, err := mi.RefreshCollections(ctx); err != nil && ctx.Err() == nil {
			log.Warn("movie collections: refresh failed", "err", err)
		}
		if _, err := mi.SyncLists(ctx); err != nil && ctx.Err() == nil {
			log.Warn("movie lists: sync failed", "err", err)
		}
	}
	if now {
		refresh()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			refresh()
		}
	}
}

// runTVDBUpdates reads TVDB's /updates every hour through tvc and queues a refresh of the changed series with the
// series refresh in force.
func runTVDBUpdates(ctx context.Context, tvc *tvdb.Client, current func(context.Context) *tvrefresh.Service, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			res, err := tvc.Refresh(ctx)
			if err != nil {
				log.Warn("tvdb: refresh failed", "err", err)
				continue
			}
			// Our series TVDB names as changed are refreshed now rather than at their next sweep.
			if n, err := current(ctx).Changed(ctx, res.SeriesIDs); err != nil {
				log.Warn("series metadata: changed series", "err", err)
			} else if n > 0 {
				log.Info("series metadata: changed series queued", "series", n)
			}
		}
	}
}

// startupServeMetadata is the bundle of the metadata services serve built at start from pol, so the first reload
// reuses those whose inputs are unchanged.
func startupServeMetadata(pol *policy.Policy, dbPath string, m serveMetadata) *serveMetadata {
	k := metadataKeys(pol, dbPath)
	m.tmdbKey, m.tvdbKey, m.artKey, m.tvKey, m.plexKey, m.intakeKey = k.tmdb, k.tvdb, k.art, k.tv, k.plex, k.intake
	m.listInterval, m.plexBuilt, m.searchBuilt = pol.ListSettings().Interval, true, true
	return &m
}
