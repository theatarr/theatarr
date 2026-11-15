package library

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
)

// otherEnv is newEnv with a second movie library, movie-web (its own root, profile webProfile), both on Real-Debrid
// with the indexer idx, and Pause off. The movie is in both libraries, monitored.
func otherEnv(t *testing.T, webProfile string) (*env, string) {
	t.Helper()
	e := newEnv(t, true)
	web := filepath.Join(e.dir, "media", "movies-web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatal(err)
	}
	e.pol.Router = &policy.Router{Enabled: true}
	e.pol.Indexers = map[string]*policy.Indexer{"idx": {Kind: "torznab", URL: "http://idx", Privacy: policy.PrivacyPublic}}
	remux := e.pol.Versions["movie-remux"]
	remux.Storage, remux.Providers = policy.StorageDebrid, []string{"rd"}
	webVP := *remux
	webVP.Library, webVP.Profile, webVP.MinimumAvailability = web, webProfile, "announced"
	e.pol.Versions["movie-web"] = &webVP
	e.pol.Profiles["web-only"] = &policy.Profile{Qualities: []policy.QualityItem{{Name: "WEBDL-1080p"}}, Cutoff: "WEBDL-1080p"}
	e.pol.Library.Versions["movie-web"] = &policy.LibraryVersion{Roots: []string{web}}
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, created_at, updated_at) VALUES (11, 1, 'movie-web', 1, 0, 0)`)
	e.exec(`UPDATE version SET monitored = 1`)
	engine, err := decide.New(e.pol, decide.Options{Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	e.lib.mount.closeHelpers()
	e.lib = New(Options{Store: e.st, Policy: e.pol, Engine: engine, Queue: e.queue, Now: func() time.Time { return e.now },
		Probe: func(p string, _ int64) (*probe.Result, error) {
			if r, ok := e.probes[p]; ok {
				return r, nil
			}
			return nil, probe.ErrUnknownFormat
		},
		Formats: func(context.Context, string, int64, string) []string { return nil }})
	t.Cleanup(e.lib.mount.closeHelpers)
	e.release(otherRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, otherRelease, otherRelease, "[]")
	return e, web
}

// otherRelease is a release name the decision matches to the movie.
const otherRelease = "Amelie.The.Movie.2001.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-GRP"

func (e *env) webLinks() string {
	return e.one(`SELECT COUNT(*) FROM link l JOIN link_version lv ON lv.link_id = l.id
		WHERE lv.version_id = 11 AND l.state = 'active'`)
}

// A release two libraries want is linked into both in one import: the second library gets a download of its own
// for the same torrent (reused, never our add), imported with its naming under its root. No router_offer row exists.
func TestOtherLibraryLinkedAtImport(t *testing.T) {
	t.Parallel()
	e, web := otherEnv(t, "movie-state")
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "imported" {
		t.Fatalf("source %s", got)
	}
	got := e.one(`SELECT group_concat(version_key || ' ' || state || ' ' || source || ' ' || reused || ' ' || own_add ||
		' ' || provider || ' ' || info_hash, '; ') FROM download WHERE id <> 1`)
	if got != "movie-web imported library:movie-remux 1 0 rd "+hashA {
		t.Fatalf("other download: %s", got)
	}
	if got := e.webLinks(); got != "1" {
		t.Fatalf("movie-web links %s", got)
	}
	// Its History rows are written by the ordinary writers: grabbed (the torrent reused) and imported.
	if got := e.one(`SELECT group_concat(event, ' ') FROM (SELECT h.event FROM history h JOIN download d
		ON d.id = h.download_id WHERE d.version_key = 'movie-web' ORDER BY h.ref)`); got != "grabbed imported" {
		t.Errorf("movie-web history %s", got)
	}
	matches, _ := filepath.Glob(filepath.Join(web, "*", "*.mkv"))
	if len(matches) != 1 || !strings.HasPrefix(filepath.Base(matches[0]), "Amelie The Movie (2001)") {
		t.Fatalf("movie-web files %v", matches)
	}
	if target, err := os.Readlink(matches[0]); err != nil || target != filepath.Join(e.mount, "__all__", otherRelease, "movie.mkv") {
		t.Fatalf("movie-web link %q %v", target, err)
	}
	if got := e.one(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'router_offer'`); got != "0" {
		t.Errorf("router_offer exists")
	}
	// Importing the second library's download again tries nothing further (it was linked from another import).
	if got := e.one(`SELECT COUNT(*) FROM download`); got != "2" {
		t.Errorf("downloads %s", got)
	}
}

// A library whose own rules reject the release (its profile wants WEB only) gets nothing, and nothing is stored.
func TestOtherLibraryRejects(t *testing.T) {
	t.Parallel()
	e, _ := otherEnv(t, "web-only")
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT group_concat(id || ' ' || state) FROM download`); got != "1 imported" {
		t.Fatalf("downloads %s", got)
	}
	if got := e.webLinks(); got != "0" {
		t.Errorf("movie-web links %s", got)
	}
}

// Paused, not monitored or removed from the second library: only the first is linked.
func TestOtherLibrarySkipped(t *testing.T) {
	t.Parallel()
	for name, set := range map[string]func(e *env){
		"paused":        func(e *env) { e.pol.Router.Enabled = false },
		"not monitored": func(e *env) { e.exec(`UPDATE version SET monitored = 0 WHERE id = 11`) },
		"removed": func(e *env) {
			e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (11, 'ui', 0)`)
		},
		"other provider": func(e *env) { e.pol.Versions["movie-web"].Providers = []string{"ad"} },
		"holds it": func(e *env) {
			e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
				created_at, updated_at) VALUES (1, 11, 'movie-web', ?, 'x', 'idx', 'imported', 0, 0)`, hashA)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e, _ := otherEnv(t, "movie-state")
			set(e)
			if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
				t.Fatal(err)
			}
			if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "imported" {
				t.Fatalf("source %s", got)
			}
			if got := e.one(`SELECT COUNT(*) FROM download WHERE source LIKE 'library:%'`); got != "0" {
				t.Errorf("other downloads %s", got)
			}
			if got := e.webLinks(); got != "0" {
				t.Errorf("movie-web links %s", got)
			}
		})
	}
}

// The second library's import fails (its root is gone): the first stays imported, and the second library is left
// with no trace: no download, no History.
func TestOtherLibraryFailureKeepsSource(t *testing.T) {
	t.Parallel()
	e, web := otherEnv(t, "movie-state")
	// A file where the movie's folder would go: the link cannot be made.
	if err := os.WriteFile(filepath.Join(web, "Amelie The Movie (2001)"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	e.otherLeftNoTrace(t)
}

// The second library's import is held after probing the file (here a naming collision with another title's link):
// the second library is left with no trace either.
func TestOtherLibraryHeldLeavesNoTrace(t *testing.T) {
	t.Parallel()
	e, web := otherEnv(t, "movie-state")
	p := filepath.Join(web, "Amelie The Movie (2001)",
		"Amelie The Movie (2001) {imdb-tt0211915} - [Remux-2160p][TrueHD Atmos 7.1][HDR10][HEVC]-GRP.mkv")
	e.exec(`INSERT INTO link (id, path, target, kind, title_id, release_title, state, created_at, updated_at)
		VALUES (5, ?, '/elsewhere.mkv', 'symlink', 2, 'Show', 'active', 0, 0)`, p)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	e.otherLeftNoTrace(t)
}

// otherLeftNoTrace: download 1 imported into movie-remux, and movie-web holds nothing from it.
func (e *env) otherLeftNoTrace(t *testing.T) {
	t.Helper()
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "imported" {
		t.Fatalf("source %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM link l JOIN link_version lv ON lv.link_id = l.id WHERE lv.version_id = 10
		AND l.state = 'active'`); got != "1" {
		t.Fatalf("movie-remux links %s", got)
	}
	if got := e.one(`SELECT group_concat(id) FROM download`); got != "1" {
		t.Errorf("downloads %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_import WHERE download_id <> 1`); got != "0" {
		t.Errorf("other imports %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action WHERE download_id <> 1`); got != "0" {
		t.Errorf("other actions %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM history WHERE download_id <> 1 OR version_key = 'movie-web'`); got != "0" {
		t.Errorf("other history %s", got)
	}
	if got := e.webLinks(); got != "0" {
		t.Errorf("movie-web links %s", got)
	}
}

// The second library's own route rules hold: with upgrades off it keeps the file it has (the first library's Remux
// does not replace its WEB file), and a movie not yet at its minimum availability is not filled there.
func TestOtherLibraryRouteRules(t *testing.T) {
	t.Parallel()
	t.Run("upgrades off", func(t *testing.T) {
		t.Parallel()
		e, web := otherEnv(t, "movie-state")
		const webRelease = "Amelie.The.Movie.2001.1080p.WEB-DL.DDP5.1.H.264-GRP"
		const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		e.release(webRelease, map[string]float64{"movie.mkv": 7000})
		e.videoSize(webRelease, 1920, 1080)
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
			score, episodes, provider, name, state, created_at, updated_at, ready_at) VALUES (2, 1, 11, 'movie-web', ?, ?,
			'idx', 'WEBDL-1080p', 10, '[]', 'rd', ?, 'ready', 0, 0, 0)`, hashB, webRelease, webRelease)
		e.pol.Router.Enabled = false // the WEB file is linked into movie-web alone
		if _, err := e.lib.Import(e.ctx, 2, false); err != nil {
			t.Fatal(err)
		}
		e.pol.Router.Enabled = true
		e.pol.Versions["movie-web"].Upgrades = policy.UpgradesOff
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		if got := e.one(`SELECT COUNT(*) FROM download WHERE source LIKE 'library:%'`); got != "0" {
			t.Errorf("other downloads %s", got)
		}
		matches, _ := filepath.Glob(filepath.Join(web, "*", "*.mkv"))
		if len(matches) != 1 || !strings.Contains(matches[0], "WEBDL-1080p") {
			t.Errorf("movie-web files %v", matches)
		}
	})
	t.Run("minimum availability", func(t *testing.T) {
		t.Parallel()
		e, _ := otherEnv(t, "movie-state")
		e.pol.Versions["movie-web"].MinimumAvailability = "released" // the movie has no release date
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		if got := e.one(`SELECT COUNT(*) FROM download WHERE source LIKE 'library:%'`); got != "0" {
			t.Errorf("other downloads %s", got)
		}
		if got := e.webLinks(); got != "0" {
			t.Errorf("movie-web links %s", got)
		}
	})
}

// A prefer-SeaDex library takes a release SeaDex prefers over its own higher-quality file, as its own route would:
// the screening sees the candidate's SeaDex standing, not a bare release.
func TestOtherLibrarySeaDexPreferred(t *testing.T) {
	t.Parallel()
	e, web := otherEnv(t, "movie-state")
	const webRelease = "Amelie.The.Movie.2001.1080p.WEB-DL.DDP5.1.H.264-GRP"
	const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	e.pol.Versions["movie-web"].PreferSeaDex = true
	// movie-web holds the Remux (hashB); movie-remux then imports the WEB release SeaDex prefers (hashA).
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, episodes, provider, name, state, created_at, updated_at, ready_at) VALUES (2, 1, 11, 'movie-web', ?, ?,
		'idx', 'Remux-2160p', 100, '[]', 'rd', ?, 'ready', 0, 0, 0)`, hashB, otherRelease, otherRelease)
	e.pol.Router.Enabled = false
	if _, err := e.lib.Import(e.ctx, 2, false); err != nil {
		t.Fatal(err)
	}
	e.pol.Router.Enabled = true
	e.release(webRelease, map[string]float64{"movie.mkv": 7000})
	e.videoSize(webRelease, 1920, 1080)
	e.exec(`UPDATE download SET release_title = ?, name = ?, quality = 'WEBDL-1080p' WHERE id = 1`, webRelease, webRelease)
	e.exec(`INSERT INTO title_external_id VALUES (1, 'anilist', '1')`)
	e.exec(`INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at) VALUES ('seadex-standing', '1',
		200, ?, 0, 9999999999999)`, `{"anilist":1,"torrents":[{"info_hash":"`+hashA+`","preference":2,"is_best":true,"facts":{"isBest":true}}]}`)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT group_concat(state) FROM download WHERE source = 'library:movie-remux'`); got != "imported" {
		t.Fatalf("other download %s", got)
	}
	matches, _ := filepath.Glob(filepath.Join(web, "*", "*.mkv"))
	if len(matches) != 1 || !strings.Contains(matches[0], "WEBDL-1080p") {
		t.Errorf("movie-web files %v", matches)
	}
}
