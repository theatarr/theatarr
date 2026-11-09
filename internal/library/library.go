// Package library turns ready downloads into library links and keeps them working (docs/design/library.md): it finds
// the release on the zurg mount, probes the video files (header only), applies the sample and fake guard, links each
// file into the version's root with the arr's naming, records the link and registry rows, and asks Plex for a
// partial scan. It also repoints dangling links by info hash and verifies a version's root.
//
// # Gating
//
// Every filesystem change goes through one writer (fs.go) that asks the [Gate] with the exact path first:
// library.enabled, the version's mode: active, and the version's roots under library.versions. Otherwise the same
// work runs as a dry run and records what it would do (library_action mode dry_run). Plex calls need
// library.enabled too. Nothing here deletes debrid content: links are symlinks into a read-only mount, replaced
// links are copied into the trash before they go, and the content they point at is never touched.
package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// RenameFormats returns, for a version and a release title, the custom format names the arr would put in the file
// name ({Custom Formats}): the formats the release matches that have includeCustomFormatWhenRenaming, in the
// profile's order. nil means none.
type RenameFormats func(ctx context.Context, version string, titleID int64, releaseTitle string) []string

// MatchedFormats returns the {Custom Formats} names for formats the router's decision matched (stored on the
// download): those with includeCustomFormatWhenRenaming, in the profile's order. No decision is made again.
type MatchedFormats func(ctx context.Context, version string, matched []string) []string

// Options configure New.
type Options struct {
	Store  *store.Store
	Policy *policy.Policy
	Engine *decide.Engine
	// Probe reads a file's headers, in this process. Default probe.File, in a mount helper when there is one.
	Probe func(path string, maxBytes int64) (*probe.Result, error)
	// MountHelper runs the mount's calls in helper processes (mountpool.go; theatarr serve). Nil: in this process.
	MountHelper *MountHelper
	// Formats gives the {Custom Formats} names. Optional.
	Formats RenameFormats
	// Matched gives the {Custom Formats} names from the formats stored on the download; preferred over Formats
	// when the download has them. Optional.
	Matched MatchedFormats
	// Queue receives plex.scan jobs. Nil: no scans.
	Queue *jobs.Queue
	// OnImported is called after a download is imported (links made), e.g. to offer it to the title's other
	// versions (router.Offerer). Optional.
	OnImported func(ctx context.Context, downloadID int64)
	Logger     *slog.Logger
	Now        func() time.Time
	// Registerer, if set, receives the mount guard's metrics (breaker.go).
	Registerer prometheus.Registerer
	// Zurg is zurg's read-only API (library.mount.zurg_api; zurgapi.go). Nil: the mount alone.
	Zurg *ZurgAPI
}

// Library is the import, relink and verify service.
type Library struct {
	runtime func(context.Context) (context.Context, *Library)
	st      *store.Store
	pol     *policy.Policy
	engine  *decide.Engine
	set     policy.LibrarySettings
	gate    *Gate
	w       *writer
	mount   *Mount
	// probe is Options.Probe; nil probes with probe.File (Mount.ProbeFile).
	probe   func(string, int64) (*probe.Result, error)
	formats RenameFormats
	matched MatchedFormats
	queue   *jobs.Queue
	log     *slog.Logger
	zurg    *ZurgAPI
	// onImported runs after an import (Options.OnImported).
	onImported func(ctx context.Context, downloadID int64)
	now        func() time.Time
	// recomputedRev is the policy revision RecomputeLinkStates last succeeded under in this process (Run does it
	// first, and again after a settings change); recomputed whether it has.
	recomputeMu   sync.Mutex
	recomputed    bool
	recomputedRev string
	// recomputedPrints are the versionPrints of that pass: a settings change recomputes only the versions whose
	// print changed.
	recomputedPrints map[string]string
	// beforeSweepWrite, when set (tests), runs just before a relink sweep records its outcome: a concurrent writer.
	beforeSweepWrite func()
}

// New builds the library service.
func New(o Options) *Library {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := o.Policy.LibrarySettings()
	g := NewGate(o.Policy)
	l := &Library{st: o.Store, pol: o.Policy, engine: o.Engine, set: s, gate: g, w: &writer{gate: g, now: o.Now},
		mount: &Mount{Path: s.MountPath, LinkPath: s.LinkPath, AllDir: s.AllDir, StatTimeout: s.StatTimeout,
			Now: o.Now},
		probe: o.Probe, formats: o.Formats, matched: o.Matched, queue: o.Queue, log: o.Logger, now: o.Now,
		onImported: o.OnImported, zurg: o.Zurg}
	if o.MountHelper == nil {
		o.MountHelper = testMountHelper
	}
	if o.MountHelper != nil && s.MountPath != "" && filepath.IsAbs(s.MountPath) {
		l.mount.pool = newHelperPool(s.MountPath, *o.MountHelper)
	}
	if l.zurg != nil && l.zurg.AllDir == "" {
		l.zurg.AllDir = s.AllDir
	}
	if o.Registerer != nil {
		RegisterMountMetrics(o.Registerer, func() *Library { return l })
	}
	return l
}

// MountBreaker returns the mount guard's state: whether mount calls are refused now (the circuit is open after
// repeated timeouts), how many calls are outstanding, and the timeouts seen. Health checks read it.
func (l *Library) MountBreaker() BreakerState { return l.mount.Breaker() }

// InheritMount keeps in-flight mount calls, the circuit breaker, the largest listings and the helper processes
// across settings generations while the mount path stays the same (Mount.inherit); the new generation's mount
// settings (release directory, stat timeout, link path) apply. A library section save that moves the mount starts
// a fresh guard. previous is the generation being replaced, not the startup library.
func (l *Library) InheritMount(previous *Library) {
	if previous == nil || previous == l {
		return
	}
	l.mount.inherit(previous.mount)
	// zurg's cached list, backoff and repair grace carry over too: a save must not restart the grace.
	l.zurg.inherit(previous.zurg)
}

// MountHealthy reports whether the mount's release root answers (Mount.Healthy); nil without a mount.
func (l *Library) MountHealthy(ctx context.Context) error {
	if l.mount == nil {
		return nil
	}
	return l.mountHealthy(ctx)
}

// Prewarm reads the head of a link target on the mount: one sequential read from offset 0, at most max bytes, and
// the file closed at once (Mount.ReadHead: under the mount guard, in a helper when the mount has them). zurg
// resolves the file's link and rclone caches its head before Plex asks for it, and a dead copy fails now. The now
// playing page's next-episode pre-warm.
func (l *Library) Prewarm(ctx context.Context, target string, max int64) error {
	if l.mount == nil {
		return ErrMountDown
	}
	p, ok := l.mount.FromLink(target)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotOnMount, target)
	}
	n, err := l.mount.ReadHead(ctx, p, max)
	if err == nil && n == 0 {
		return errors.New("empty file")
	}
	return err
}

// MountFileSize stats a link target on the mount under the mount guard (in a helper when the mount has them).
func (l *Library) MountFileSize(ctx context.Context, target string) (int64, error) {
	if l.mount == nil {
		return 0, ErrMountDown
	}
	p, ok := l.mount.FromLink(target)
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrNotOnMount, target)
	}
	v, err := l.mount.withTimeout(ctx, p, func(f mountFS) (any, error) {
		fi, err := f.Stat(p)
		if err != nil {
			return int64(0), err
		}
		return fi.Size(), nil
	})
	if err != nil {
		return 0, err
	}
	return v.(int64), nil
}

// CacheFile reads a whole link target through the mount, in order, at most rate bytes per second, dropping the
// bytes (Mount.ReadAll): rclone's VFS cache keeps the file, so it plays from local disk. The now playing page's
// next-episode caching read. It returns the bytes read.
func (l *Library) CacheFile(ctx context.Context, target string, rate int64, progress func(done, size int64)) (int64, error) {
	if l.mount == nil {
		return 0, ErrMountDown
	}
	p, ok := l.mount.FromLink(target)
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrNotOnMount, target)
	}
	return l.mount.ReadAll(ctx, p, rate, progress)
}

// MountAnswers stats the release root and reads one name from it under the mount guard (in a helper process when
// the mount has them): the serve health check. It returns how many names it read (0: empty, likely unmounted).
func (l *Library) MountAnswers(ctx context.Context) (int, error) {
	if l.mount == nil {
		return 0, nil
	}
	return l.mount.answers(ctx, l.mount.AllPath())
}

// MountListed is MountHealthy plus the listing check relink uses (newMountIndex, Mount.checkListing): an empty or
// half-loaded release root counts as ErrMountDown too. The router's ready deadline waits on it. nil without a mount.
func (l *Library) MountListed(ctx context.Context) error {
	if l.mount == nil {
		return nil
	}
	if err := l.mountHealthy(ctx); err != nil {
		return err
	}
	_, err := l.newMountIndex(ctx)
	return err
}

// Gate returns the write gate.
func (l *Library) Gate() *Gate { return l.gate }

// Locate places a path a client named on the mount (Mount.Locate); ErrNotOnMount when it is not there.
func (l *Library) Locate(ctx context.Context, p string) (Located, error) {
	return l.mount.Locate(ctx, p)
}

// ReadDir lists a directory a client named (a manual import preview) under the mount guard.
func (l *Library) ReadDir(ctx context.Context, dir string) ([]os.DirEntry, error) {
	return l.mount.ReadDir(ctx, dir)
}

// Stat stats a path a client named under the mount guard, following symlinks.
func (l *Library) Stat(ctx context.Context, p string) (os.FileInfo, error) {
	return l.mount.Stat(ctx, p)
}

// action is one library_action row.
type action struct {
	Kind, Mode, Version, Path, Target, Detail string
	DownloadID, LinkID                        int64
}

func (l *Library) record(ctx context.Context, tx *sql.Tx, a action) error {
	q := `INSERT INTO library_action (at, kind, mode, version_key, download_id, link_id, path, target, detail)
		VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, 0), NULLIF(?, 0), NULLIF(?, ''), NULLIF(?, ''), ?)`
	args := []any{store.Millis(l.now()), a.Kind, a.Mode, a.Version, a.DownloadID, a.LinkID, a.Path, a.Target, a.Detail}
	if tx != nil {
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	}
	return l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	})
}

func modeOf(err error) string {
	if errors.Is(err, ErrRefused) {
		return "refused"
	}
	return "failed"
}

// errf wraps an error with a download id.
func errf(id int64, format string, args ...any) error {
	return fmt.Errorf("library: download %d: "+format, append([]any{id}, args...)...)
}
