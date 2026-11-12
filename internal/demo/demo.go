// Package demo builds a demo database: an invented library with every screen of the UI filled, for people trying Theatarr,
// screenshots, docs, and reviewers who must never see a real database (docs/demo.md).
//
// Nothing in it is real. The titles, people, networks, studios and release groups are made up (catalog.go); the
// external ids sit in ranges no real TMDb, TVDB, IMDb, AniList, AniDB, Kitsu or MyAnimeList id reaches (the Id
// constants below); hashes, sizes and dates come from the seed. Seed uses no network and reads no live data.
//
// Seed is deterministic for a given Options.Seed and Options.Now: the same pair gives the same rows. Password hashes
// and the web signing secret differ (random salts and secrets, by design). Now defaults to the current minute, so
// without it two runs differ only in where their dates fall.
//
// The schema comes from the normal migrations (store.Migrate). Most rows are plain inserts in one transaction; the
// journal's search snapshots go through journal/snapshot.Record and the anime franchise through animestore.Save,
// so those two are shaped exactly as the services write them.
package demo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Password is shared by every local account printed by theatarr demo seed and documented in docs/demo.md. It is for demo
// databases only and must never be used anywhere else.
const Password = "demo-only-not-a-secret"

// Username is the demo admin account.
const Username = auth.AdminUsername

// Demo account usernames.
const (
	ManagerUsername     = "demo-manager"
	AutoApproveUsername = "demo-auto"
	LimitedUsername     = "demo-limited"
)

// Id ranges. Real ids are far below these (TMDb about 1.6 million, TVDB under 500,000, AniList under 200,000, IMDb
// tt numbers under 40 million in 2026), so a demo id never names a real title.
const (
	TMDbMovieBase = 900_000_000 // TMDb movie ids: 900000001...
	TMDbTVBase    = 900_100_000 // TMDb TV ids: 900100001...
	TVDBBase      = 900_200_000 // TVDB series ids: 900200001...
	AniListBase   = 900_300_000 // AniList ids: 900300001...
	AniDBBase     = 900_400_000 // AniDB ids
	KitsuBase     = 900_500_000 // Kitsu ids
	MALBase       = 900_600_000 // MyAnimeList ids
	IMDbBase      = 99_000_000  // IMDb ids tt99000001...
)

// Options tune the seed.
type Options struct {
	// Policy is the policy the demo runs with (examples/policy): its versions, roots, arr instances and providers.
	Policy *policy.Policy
	// Seed picks the generated details (dates, sizes, hashes, states within a scenario). Default 1.
	Seed uint64
	// Now anchors every date: titles added before it, releases and air dates around it, downloads minutes before
	// it. Default: the current minute, so a database served right after seeding looks live.
	Now time.Time
	// ImagesDir is the /img cache directory (ui.images.dir) to write generated poster and backdrop PNGs into, so
	// the UI shows art with no network. Empty: no files, and titles get no art paths (the generated SVG is served).
	ImagesDir string
	Logger    *slog.Logger
}

// Result says what was seeded.
type Result struct {
	// Now is the anchor the data was built around (pass it as -now to build the same data again).
	Now    time.Time
	Counts map[string]int
	// Art is the number of image files written; ArtError says why none were, when writing failed.
	Art      int
	ArtError string
}

// Seed fills st, a freshly migrated empty database, with the demo dataset.
func Seed(ctx context.Context, st *store.Store, o Options) (*Result, error) {
	if o.Policy == nil {
		return nil, errors.New("demo: a policy is required")
	}
	if o.Seed == 0 {
		o.Seed = 1
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	o.Now = o.Now.UTC().Truncate(time.Minute)
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	var n int
	if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM title`).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, errors.New("demo: the database already has titles; seed a new file")
	}
	s := &seeder{ctx: ctx, st: st, o: o, now: o.Now, r: rand.New(rand.NewPCG(o.Seed, 0x6d6d64656d6f)), pol: o.Policy}
	if err := s.plan(); err != nil {
		return nil, err
	}
	res := &Result{Now: o.Now, Counts: map[string]int{}}
	// Art first: the art paths go into title_meta only when the files are there.
	if o.ImagesDir != "" {
		n, err := writeArt(o.ImagesDir, s.titles)
		if err != nil {
			res.ArtError = err.Error()
			for _, t := range s.titles {
				t.poster, t.backdrop = "", ""
			}
		}
		res.Art = n
	} else {
		for _, t := range s.titles {
			t.poster, t.backdrop = "", ""
		}
	}
	err := st.WriteTx(ctx, func(tx *sql.Tx) error {
		s.tx = tx
		s.err = nil
		s.library()
		s.registry()
		s.arrHistory()
		s.downloads()
		s.upgradesFound()
		s.system()
		if s.err != nil {
			return s.err
		}
		return s.ceilings()
	})
	if err != nil {
		return nil, err
	}
	if err := s.anime(); err != nil {
		return nil, err
	}
	if err := s.users(); err != nil {
		return nil, err
	}
	if err := s.settings(); err != nil {
		return nil, err
	}
	for _, t := range []string{"title", "episode", "version", "version_file", "link", "content", "download", "request",
		"request_user", "journal_decision", "job", "anime_entry", "title_meta", "settings_audit"} {
		var c int
		if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+t).Scan(&c); err != nil {
			return nil, err
		}
		res.Counts[t] = c
	}
	return res, nil
}

// seeder holds the plan and the open transaction.
type seeder struct {
	ctx context.Context
	st  *store.Store
	o   Options
	pol *policy.Policy
	now time.Time
	r   *rand.Rand
	tx  *sql.Tx
	err error

	titles  []*title
	byName  map[string]*title
	grabs   []*arrGrab // arr grab events, for the shadow runner's rows
	runID   int64
	content map[string]int64 // info hash -> content id
	// arr ids given so far per instance
	arrTitles, arrFileIDs map[string]int64
}

// exec runs one statement in the transaction and keeps the first error.
func (s *seeder) exec(q string, args ...any) int64 {
	if s.err != nil {
		return 0
	}
	res, err := s.tx.ExecContext(s.ctx, q, args...)
	if err != nil {
		s.err = fmt.Errorf("demo: %w\n%s", err, strings.TrimSpace(q))
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

// ms is t as stored.
func ms(t time.Time) int64 { return store.Millis(t) }

// ago is now minus d.
func (s *seeder) ago(d time.Duration) time.Time { return s.now.Add(-d) }

func days(n float64) time.Duration { return time.Duration(n * float64(24*time.Hour)) }

// between is a random time between now-from and now-to (from > to).
func (s *seeder) between(from, to time.Duration) time.Time {
	if from <= to {
		return s.ago(from)
	}
	return s.ago(to + time.Duration(s.r.Int64N(int64(from-to))))
}

func (s *seeder) pick(xs []string) string { return xs[s.r.IntN(len(xs))] }

func (s *seeder) hash() string {
	return fmt.Sprintf("%016x%016x%08x", s.r.Uint64(), s.r.Uint64(), s.r.Uint32())
}

func date(t time.Time) string { return t.Format("2006-01-02") }

func jsonOf(v any) string {
	b, err := jsonMarshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// versionKeys returns the policy's version keys for a kind and anime flag, sorted.
func (s *seeder) versionKeys(kind policy.Kind, anime bool) []string {
	var out []string
	for k, v := range s.pol.Versions {
		a := v.Anime != nil && *v.Anime
		if v.Kind == kind && a == anime {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// instanceFor is the arr instance the policy imports a version from ("" when none).
func (s *seeder) instanceFor(key string) (name, flavour string) {
	if s.pol.Import == nil {
		return "", ""
	}
	names := make([]string, 0, len(s.pol.Import.Arr))
	for n := range s.pol.Import.Arr {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if a := s.pol.Import.Arr[n]; a.Version == key {
			return n, a.Flavour
		}
	}
	return "", ""
}

func sortStrings(xs []string) { sort.Strings(xs) }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
