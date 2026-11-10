package health

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/uipages"
)

func fail(format string, args ...any) Result {
	return Result{Type: Warning, Message: fmt.Sprintf(format, args...)}
}

// short cuts a stored error for a message.
func short(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

// age formats a duration for messages: 45s, 12m, 5h, 3d.
func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// Mount checks that a FUSE mount answers: a stat of dir and its first name, within timeout. An empty directory is an
// error too: an unmounted mount point is an empty directory. A hung FUSE call cannot be cancelled, so while an
// earlier probe is still blocked no new one starts (the goroutines would pile up); the check reports the hang until
// the call returns.
func Mount(dir string, timeout time.Duration) Func {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	var busy atomic.Bool
	return func(ctx context.Context) Result {
		detail := map[string]any{"path": dir}
		if !busy.CompareAndSwap(false, true) {
			return Result{Type: Error, Detail: detail,
				Message: "The mount at " + dir + " is not answering: an earlier check is still blocked on it"}
		}
		type answer struct {
			names int
			err   error
		}
		ch := make(chan answer, 1)
		go func() {
			defer busy.Store(false)
			n, err := probe(dir)
			ch <- answer{n, err}
		}()
		t := time.NewTimer(timeout)
		defer t.Stop()
		select {
		case a := <-ch:
			switch {
			case a.err != nil:
				return Result{Type: Error, Detail: detail, Message: "The mount at " + dir + " is not readable: " + short(a.err.Error())}
			case a.names == 0:
				return Result{Type: Error, Detail: detail, Message: "The mount at " + dir + " is empty: is it mounted?"}
			}
			return Result{Type: OK, Detail: detail, Message: "The mount at " + dir + " answers"}
		case <-t.C:
		case <-ctx.Done():
		}
		return Result{Type: Error, Detail: detail,
			Message: fmt.Sprintf("The mount at %s did not answer in %v", dir, timeout)}
	}
}

// MountAnswering is Mount with the stat and the first name read by answer, which bounds its own calls (theatarr serve's
// mount guard, in its helper processes): no goroutine of the check stays blocked on a dead mount.
func MountAnswering(dir string, answer func(ctx context.Context) (int, error)) Func {
	return func(ctx context.Context) Result {
		detail := map[string]any{"path": dir}
		n, err := answer(ctx)
		switch {
		case err != nil:
			return Result{Type: Error, Detail: detail, Message: "The mount at " + dir + " is not answering: " + short(err.Error())}
		case n == 0:
			return Result{Type: Error, Detail: detail, Message: "The mount at " + dir + " is empty: is it mounted?"}
		}
		return Result{Type: OK, Detail: detail, Message: "The mount at " + dir + " answers"}
	}
}

// MountHelperState is what MountHelper reads of the mount helper processes.
type MountHelperState struct {
	RecentKills int // kills and crashes in the last 15 minutes
	Lingering   int // killed helpers still there
	LastError   string
	RetryAt     time.Time // no helper starts before this
}

// MountHelper warns when the mount helper processes keep getting killed (calls that did not answer in time, or
// helpers that ended): from 3 in 15 minutes, the mount is hanging. nil state: no helpers (the check is OK).
func MountHelper(state func() *MountHelperState) Func {
	return func(context.Context) Result {
		s := state()
		if s == nil {
			return Result{Type: OK, Message: "Mount calls run in Theatarr (no helper processes)"}
		}
		detail := map[string]any{"recent_kills": s.RecentKills, "lingering": s.Lingering}
		if s.RecentKills < 3 {
			return Result{Type: OK, Detail: detail, Message: "Mount helpers answer"}
		}
		msg := fmt.Sprintf("Mount helpers were killed %d times in the last 15 minutes (calls on the mount that did "+
			"not answer): check rclone and zurg", s.RecentKills)
		if s.LastError != "" {
			msg += "; last: " + short(s.LastError)
		}
		if s.Lingering > 0 {
			msg += fmt.Sprintf("; %d killed helpers still stuck in the kernel", s.Lingering)
		}
		if !s.RetryAt.IsZero() {
			detail["retry_at"] = s.RetryAt
		}
		return Result{Type: Warning, Detail: detail, Message: msg}
	}
}

// probe is probeDir; tests replace it to simulate a hung mount.
var probe = probeDir

// probeDir stats dir and reads one name from it.
func probeDir(dir string) (int, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return 0, err
	}
	if !fi.IsDir() {
		return 0, errors.New("not a directory")
	}
	f, err := os.Open(dir)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	return len(names), nil
}

// MetadataOptions say which metadata refreshes run.
type MetadataOptions struct {
	// TMDb: the meta.art job runs (metadata.sources.tmdb and its key are set).
	TMDb bool
	// TMDbInterval is how often meta.art runs (metadata.art.interval).
	TMDbInterval time.Duration
	// TVDB: the hourly TVDB /updates refresh runs.
	TVDB bool
	Now  func() time.Time
}

// Metadata checks title metadata freshness: titles with no TMDb row an hour after they were added, rows overdue for
// refresh by more than a day, the share of failed tries in the last day, the newest good fetch, and the TVDB refresh's
// last run.
func Metadata(st *store.Store, o MetadataOptions) Func {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.TMDbInterval <= 0 {
		o.TMDbInterval = 6 * time.Hour
	}
	return func(ctx context.Context) Result {
		now := o.Now()
		db := st.Reader()
		var titles, missing, overdue, tried, failed int
		var newest, worstDue sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT
				(SELECT COUNT(*) FROM title),
				(SELECT COUNT(*) FROM title t WHERE t.created_at < ?1
					AND NOT EXISTS (SELECT 1 FROM title_meta m WHERE m.title_id = t.id)),
				(SELECT COUNT(*) FROM title_meta WHERE refresh_after < ?2),
				(SELECT MIN(refresh_after) FROM title_meta WHERE refresh_after < ?2),
				(SELECT COUNT(*) FROM title_meta WHERE attempted_at >= ?3),
				(SELECT COUNT(*) FROM title_meta WHERE attempted_at >= ?3 AND error <> ''),
				(SELECT MAX(refreshed_at) FROM title_meta)`,
			store.Millis(now.Add(-time.Hour)), store.Millis(now.Add(-24*time.Hour)),
			store.Millis(now.Add(-24*time.Hour))).Scan(&titles, &missing, &overdue, &worstDue, &tried, &failed,
			&newest); err != nil {
			return fail("Metadata state not readable: %v", err)
		}
		detail := map[string]any{"titles": titles, "missing": missing, "overdue": overdue, "tried_24h": tried,
			"failed_24h": failed, "tmdb": o.TMDb, "tvdb": o.TVDB}
		var problems []string
		typ := OK
		raise := func(t, msg string) {
			problems = append(problems, msg)
			if Rank(t) > Rank(typ) {
				typ = t
			}
		}
		if newest.Valid {
			a := now.Sub(store.FromMillis(newest.Int64))
			detail["last_refresh_age_s"] = int64(a.Seconds())
			if o.TMDb && overdue > 0 && a > 2*o.TMDbInterval+time.Hour {
				raise(Warning, "no TMDb refresh succeeded in "+age(a))
			}
		}
		if worstDue.Valid {
			detail["worst_overdue_s"] = int64(now.Sub(store.FromMillis(worstDue.Int64)).Seconds())
		}
		switch {
		case !o.TMDb && titles > 0:
			raise(Warning, "TMDb refresh is off (no metadata.sources.tmdb key): titles get no new metadata")
		case missing > 0:
			raise(Warning, fmt.Sprintf("%d title(s) have no metadata an hour after they were added", missing))
		}
		if o.TMDb && overdue > 0 {
			raise(Notice, fmt.Sprintf("%d title(s) are more than a day overdue for a refresh", overdue))
		}
		if failed >= 3 && failed*2 >= tried {
			raise(Warning, fmt.Sprintf("%d of %d TMDb refreshes in the last day failed", failed, tried))
		}
		if o.TVDB {
			var runAt sql.NullInt64
			err := db.QueryRowContext(ctx, `SELECT run_at FROM tvdb_sync WHERE id = 1`).Scan(&runAt)
			switch {
			case err != nil && !errors.Is(err, sql.ErrNoRows):
				raise(Warning, "TVDB refresh state not readable")
			case !runAt.Valid:
				detail["tvdb_last_run_s"] = nil
			default:
				a := now.Sub(store.FromMillis(runAt.Int64))
				detail["tvdb_last_run_s"] = int64(a.Seconds())
				if a > 3*time.Hour {
					raise(Warning, "the TVDB refresh last ran "+age(a)+" ago (hourly)")
				}
			}
		}
		if len(problems) == 0 {
			msg := fmt.Sprintf("Metadata is current for %d title(s)", titles)
			if failed > 0 {
				msg += fmt.Sprintf("; %d refresh(es) failed in the last day", failed)
			}
			return Result{Type: OK, Message: msg, Detail: detail}
		}
		return Result{Type: typ, Message: "Metadata: " + strings.Join(problems, "; "), Detail: detail}
	}
}

// BacklogOptions set how long a ready job may wait.
type BacklogOptions struct {
	// Urgent are the acquisition and import kinds (jobs.WorkerOptions.Urgent).
	Urgent []string
	// UrgentWarn and UrgentError: how long the oldest ready urgent job may wait. Defaults 15m and 1h.
	UrgentWarn, UrgentError time.Duration
	// BackgroundWarn: how long the oldest ready job of another kind may wait. Default 6h.
	BackgroundWarn time.Duration
	Now            func() time.Time
}

// Backlog checks the job queue per kind: how long the oldest ready job of each kind has waited for a worker slot.
// Acquisition and import kinds have tighter limits than background work. The detail lists every kind with its ready,
// later and running counts and its oldest wait.
func Backlog(q *jobs.Queue, o BacklogOptions) Func {
	if o.UrgentWarn <= 0 {
		o.UrgentWarn = 15 * time.Minute
	}
	if o.UrgentError <= 0 {
		o.UrgentError = time.Hour
	}
	if o.BackgroundWarn <= 0 {
		o.BackgroundWarn = 6 * time.Hour
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return func(ctx context.Context) Result {
		bl, err := q.Backlog(ctx, o.Now().Add(-24*time.Hour))
		if err != nil {
			return fail("Job queue not readable: %v", err)
		}
		typ, ready := OK, 0
		var late []string
		for _, k := range bl {
			ready += k.Ready
			warn, errAt := o.BackgroundWarn, time.Duration(0)
			if slices.Contains(o.Urgent, k.Kind) {
				warn, errAt = o.UrgentWarn, o.UrgentError
			}
			if k.OldestReady < warn {
				continue
			}
			t := Warning
			if errAt > 0 && k.OldestReady >= errAt {
				t = Error
			}
			if Rank(t) > Rank(typ) {
				typ = t
			}
			late = append(late, fmt.Sprintf("%s (%d waiting, oldest %s)", k.Kind, k.Ready, age(k.OldestReady)))
		}
		detail := map[string]any{"kinds": bl, "urgent": o.Urgent}
		if len(late) > 0 {
			return Result{Type: typ, Detail: detail, Message: "Jobs are waiting for a worker: " + strings.Join(late, ", ")}
		}
		return Result{Type: OK, Detail: detail, Message: fmt.Sprintf("Job queue keeping up (%d ready)", ready)}
	}
}

// FailedJobs checks for failed jobs that still need attention (internal/jobs manage.go: not resolved by a later
// run, retried or dismissed).
func FailedJobs(q *jobs.Queue) Func {
	return func(ctx context.Context) Result {
		n, err := q.AttentionCount(ctx)
		if err != nil {
			return fail("Failed jobs not readable: %v", err)
		}
		detail := map[string]any{"attention": n}
		if n > 0 {
			return Result{Type: Warning, Detail: detail,
				Message: fmt.Sprintf("%d failed job(s) need attention ("+uipages.Tasks+")", n)}
		}
		return Result{Type: OK, Detail: detail, Message: "No failed jobs need attention"}
	}
}

// UnsettledAdds checks for downloads left in adding for more than after (default 10m): a provider add whose
// outcome the router could not settle from the account yet (internal/router reconcile.go). They are never failed by
// time alone, since adding again could put the torrent on the account twice; this is how they are seen.
func UnsettledAdds(st *store.Store, after time.Duration, now func() time.Time) Func {
	if after <= 0 {
		after = 10 * time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return func(ctx context.Context) Result {
		var n int
		var oldest sql.NullInt64
		var why sql.NullString
		err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*), MIN(updated_at), (SELECT error FROM download
			WHERE state = 'adding' AND updated_at < ? ORDER BY updated_at LIMIT 1) FROM download
			WHERE state = 'adding' AND updated_at < ?`, store.Millis(now().Add(-after)),
			store.Millis(now().Add(-after))).Scan(&n, &oldest, &why)
		if err != nil {
			return fail("Downloads not readable: %v", err)
		}
		detail := map[string]any{"unsettled": n}
		if n == 0 {
			return Result{Type: OK, Detail: detail, Message: "No provider add is waiting to be settled"}
		}
		msg := fmt.Sprintf("%d download(s) in adding for up to %s: the provider add may or may not have landed and "+
			"nothing is added again until the account shows which", n, age(now().Sub(store.NullMillis(oldest))))
		// The add's error can name torrent ids: it stays in Detail (API); Message is what the Health webhook sends.
		if why.String != "" {
			detail["error"] = short(why.String)
		}
		return Result{Type: Warning, Detail: detail, Message: msg}
	}
}

// UnfinishedCacheChecks warns about downloads whose cached-only check (internal/router cachedcheck.go) has been
// unfinished for more than after (default 30m) since the add: no verdict yet, or an uncached add of ours whose
// delete waits (the stop switch closed, the delete failing). The poller finishes them; this is how a long wait is
// seen.
func UnfinishedCacheChecks(st *store.Store, after time.Duration, now func() time.Time) Func {
	if after <= 0 {
		after = 30 * time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return func(ctx context.Context) Result {
		var n int
		var oldest sql.NullInt64
		err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*), MIN(added_at) FROM download
			WHERE cached_check IS NOT NULL AND state IN ('added', 'downloading') AND added_at < ?`,
			store.Millis(now().Add(-after))).Scan(&n, &oldest)
		if err != nil {
			return fail("Downloads not readable: %v", err)
		}
		detail := map[string]any{"unfinished": n}
		if n == 0 {
			return Result{Type: OK, Detail: detail, Message: "No cached-only check is left unfinished"}
		}
		return Result{Type: Warning, Detail: detail, Message: fmt.Sprintf("%d download(s) added up to %s ago still "+
			"wait for their cached-only check (an uncached add of ours may be downloading on the provider; see "+
			"Activity)", n, age(now().Sub(store.NullMillis(oldest))))}
	}
}

// Dangling reports the latest dangling-link report (library_dangling_scan): links into the zurg mount whose target
// is missing. A notice while there are any, naming how many zurg's API says lost their video upstream (when
// library.mount.zurg_api is set) and whether zurg was repairing; the links themselves are on System › Status (GET /health/dangling) and in theatarr library dangling.
func Dangling(st *store.Store) Func {
	return func(ctx context.Context) Result {
		var links, dangling int
		var at int64
		var walkErr, zurgErr sql.NullString
		var noVideo, noVideoTorrents sql.NullInt64
		var repairing sql.NullBool
		err := st.Reader().QueryRowContext(ctx, `SELECT at, links, dangling, error, no_video, no_video_torrents,
			repairing, zurg_error FROM library_dangling_scan ORDER BY id DESC LIMIT 1`).Scan(&at, &links, &dangling,
			&walkErr, &noVideo, &noVideoTorrents, &repairing, &zurgErr)
		if errors.Is(err, sql.ErrNoRows) {
			return Result{Type: OK, Message: "No dangling-link report yet"}
		}
		if err != nil {
			return fail("Dangling-link report not readable: %v", err)
		}
		detail := map[string]any{"links": links, "dangling": dangling, "at": store.FromMillis(at)}
		if noVideo.Valid {
			detail["no_video"], detail["no_video_torrents"] = noVideo.Int64, noVideoTorrents.Int64
			detail["zurg_repairing"] = repairing.Bool
			if zurgErr.String != "" {
				detail["zurg_error"] = short(zurgErr.String)
			}
		}
		if walkErr.String != "" {
			detail["error"] = short(walkErr.String)
		}
		if dangling == 0 {
			return Result{Type: OK, Detail: detail, Message: fmt.Sprintf("No dangling links (%d links into the mount)", links)}
		}
		msg := fmt.Sprintf("%d of %d links into the mount are dangling", dangling, links)
		if noVideo.Valid && zurgErr.String == "" {
			msg += fmt.Sprintf("; %d of them: torrent has no video upstream", noVideo.Int64)
		}
		if repairing.Bool {
			msg += "; zurg was repairing"
		}
		return Result{Type: Notice, Detail: detail, Message: msg + " (listed on System › Status; or theatarr library dangling)"}
	}
}

// vanishFailsafeWindow is how long a reconcile failsafe stays reported after its last alert.
const vanishFailsafeWindow = 24 * time.Hour

// VanishFailsafe reports a library reconcile that refused to mark links missing because too many of a root's links
// were gone at once (library.VanishFailsafeDetail alert actions), a warning while the latest is under
// a day old: most likely a share below a library root is not mounted, and its links were left as they are.
func VanishFailsafe(st *store.Store, now func() time.Time) Func {
	return func(ctx context.Context) Result {
		var at int64
		var root, detail sql.NullString
		err := st.Reader().QueryRowContext(ctx, `SELECT at, path, detail FROM library_action WHERE kind = 'alert'
			AND mode = 'refused' AND detail LIKE 'vanish failsafe: %' AND at >= ? ORDER BY id DESC LIMIT 1`,
			store.Millis(now().Add(-vanishFailsafeWindow))).Scan(&at, &root, &detail)
		if errors.Is(err, sql.ErrNoRows) {
			return Result{Type: OK, Message: "No library root lost a large share of its links at once"}
		}
		if err != nil {
			return fail("Library actions not readable: %v", err)
		}
		return Result{Type: Warning, Detail: map[string]any{"root": root.String, "at": store.FromMillis(at),
			"detail": short(detail.String)}, Message: fmt.Sprintf("Library root %s: too many links missing at once, "+
			"left as they are (is a share below it not mounted?)", root.String)}
	}
}

// BackupOptions describe the nightly backup.
type BackupOptions struct {
	// Dir is the backup directory; empty means backups are off.
	Dir string
	// Warn and Error: the newest backup's age that raises each. Defaults 36h and 72h (it runs nightly).
	Warn, Error time.Duration
	// Started is when the service started: without any backup, the check stays ok for a day after it.
	Started time.Time
	Now     func() time.Time
}

// Backup checks that a recent database backup exists, with its policy archive and manifest beside it. A set whose
// manifest records no policy directory (a database-only install) needs no archive while that directory still does
// not exist.
func Backup(o BackupOptions) Func {
	if o.Warn <= 0 {
		o.Warn = 36 * time.Hour
	}
	if o.Error <= 0 {
		o.Error = 72 * time.Hour
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return func(ctx context.Context) Result {
		if o.Dir == "" {
			return Result{Type: Warning, Message: "Backups are off (--backup-dir off): nothing protects the database"}
		}
		detail := map[string]any{"dir": o.Dir}
		files, err := filepath.Glob(filepath.Join(o.Dir, "theatarr-*.db"))
		if err != nil {
			return fail("Backups not readable: %v", err)
		}
		sort.Strings(files)
		now := o.Now()
		if len(files) == 0 {
			if now.Sub(o.Started) < 26*time.Hour {
				return Result{Type: OK, Detail: detail, Message: "No backup yet; the first one runs tonight"}
			}
			return Result{Type: Error, Detail: detail, Message: "No database backup in " + o.Dir}
		}
		newest := files[len(files)-1]
		fi, err := os.Stat(newest)
		if err != nil {
			return fail("Backup %s not readable: %v", filepath.Base(newest), err)
		}
		a := now.Sub(fi.ModTime())
		base := strings.TrimSuffix(newest, ".db")
		_, perr := os.Stat(base + ".policy.tar")
		_, merr := os.Stat(base + ".manifest.json")
		if perr != nil && merr == nil && databaseOnlySet(base+".manifest.json") {
			perr = nil
		}
		detail["newest"], detail["age_s"], detail["count"] = filepath.Base(newest), int64(a.Seconds()), len(files)
		detail["policy"], detail["manifest"] = perr == nil, merr == nil
		switch {
		case a >= o.Error:
			return Result{Type: Error, Detail: detail, Message: "The newest backup is " + age(a) + " old"}
		case a >= o.Warn:
			return Result{Type: Warning, Detail: detail, Message: "The newest backup is " + age(a) + " old"}
		case perr != nil || merr != nil:
			return Result{Type: Warning, Detail: detail,
				Message: "The newest backup has no policy archive or manifest beside it"}
		}
		return Result{Type: OK, Detail: detail, Message: fmt.Sprintf("Last backup %s ago (%d kept)", age(a), len(files))}
	}
}

// databaseOnlySet reports whether a backup manifest records that the install had no policy directory, and that
// directory still does not exist.
func databaseOnlySet(manifest string) bool {
	b, err := os.ReadFile(manifest)
	if err != nil {
		return false
	}
	var m struct {
		Policy struct {
			State string `json:"state"`
			Dir   string `json:"dir"`
		} `json:"policy"`
	}
	if json.Unmarshal(b, &m) != nil || m.Policy.State != "absent" {
		return false
	}
	if m.Policy.Dir == "" {
		return true
	}
	_, err = os.Stat(m.Policy.Dir)
	return errors.Is(err, os.ErrNotExist)
}

// ProviderOptions describe the provider registry sync.
type ProviderOptions struct {
	// Providers are the providers with a key set, which the sync reads.
	Providers []string
	// Interval is the sync interval; a provider whose last good sync is older than three of them warns.
	Interval time.Duration
	Now      func() time.Time
}

// Providers checks provider account reachability from the registry sync's own log (provider_sync_run): a provider
// whose latest sync failed, or that has not synced completely for three intervals. Every provider failing is an
// error.
func Providers(st *store.Store, o ProviderOptions) Func {
	if o.Interval <= 0 {
		o.Interval = 6 * time.Hour
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return func(ctx context.Context) Result {
		if len(o.Providers) == 0 {
			return Result{Type: OK, Message: "No debrid provider keys set; nothing to sync"}
		}
		now := o.Now()
		var bad []string
		detail := map[string]any{}
		for _, p := range o.Providers {
			var ok int
			var finished int64
			var errText string
			err := st.Reader().QueryRowContext(ctx, `SELECT ok, finished_at, COALESCE(error, '') FROM provider_sync_run
				WHERE provider = ? ORDER BY finished_at DESC, id DESC LIMIT 1`, p).Scan(&ok, &finished, &errText)
			if errors.Is(err, sql.ErrNoRows) {
				detail[p] = map[string]any{"synced": false}
				continue
			}
			if err != nil {
				return fail("Provider sync state not readable: %v", err)
			}
			var lastOK sql.NullInt64
			if err := st.Reader().QueryRowContext(ctx, `SELECT MAX(finished_at) FROM provider_sync_run
				WHERE provider = ? AND ok = 1`, p).Scan(&lastOK); err != nil {
				return fail("Provider sync state not readable: %v", err)
			}
			d := map[string]any{"synced": true, "last_ok": ok == 1, "age_s": int64(now.Sub(store.FromMillis(finished)).Seconds())}
			detail[p] = d
			switch {
			case ok == 0:
				d["error"] = short(errText)
				bad = append(bad, p+": "+short(errText))
			case now.Sub(store.NullMillis(lastOK)) > 3*o.Interval:
				bad = append(bad, p+": no complete sync in "+age(now.Sub(store.NullMillis(lastOK))))
			}
		}
		switch {
		case len(bad) == len(o.Providers):
			return Result{Type: Error, Detail: detail, Message: "Debrid providers unreachable: " + strings.Join(bad, "; ")}
		case len(bad) > 0:
			return Result{Type: Warning, Detail: detail, Message: "Debrid provider sync failing: " + strings.Join(bad, "; ")}
		}
		return Result{Type: OK, Detail: detail, Message: "Debrid providers reachable: " + strings.Join(o.Providers, ", ")}
	}
}

// TorrentLimits warns when a debrid account holds at least TorrentLimitWarnPercent of the torrents it may hold
// (AllDebrid's providers.<key>.magnet_limit; docs/design/torrents.md "Account limit"). limits gives each provider's
// limit (0 or missing: none); the count is the provider's latest complete registry sync (provider_sync_run summary),
// so the check asks no provider anything. Only a warning: nothing is deleted.
func TorrentLimits(st *store.Store, limits func(ctx context.Context) map[string]int, warnPercent int) Func {
	return func(ctx context.Context) Result {
		lim := limits(ctx)
		keys := make([]string, 0, len(lim))
		for k, n := range lim {
			if n > 0 {
				keys = append(keys, k)
			}
		}
		if len(keys) == 0 {
			return Result{Type: OK, Message: "No debrid account with a torrent limit"}
		}
		sort.Strings(keys)
		detail := map[string]any{}
		var near, counted []string
		for _, k := range keys {
			var summary string
			err := st.Reader().QueryRowContext(ctx, `SELECT summary FROM provider_sync_run WHERE provider = ? AND ok = 1
				ORDER BY finished_at DESC, id DESC LIMIT 1`, k).Scan(&summary)
			if errors.Is(err, sql.ErrNoRows) {
				detail[k] = map[string]any{"limit": lim[k], "synced": false}
				continue
			}
			if err != nil {
				return fail("Provider sync state not readable: %v", err)
			}
			var sum struct {
				Torrents *int `json:"torrents"`
			}
			if json.Unmarshal([]byte(summary), &sum) != nil || sum.Torrents == nil {
				detail[k] = map[string]any{"limit": lim[k], "synced": false}
				continue
			}
			n := *sum.Torrents
			detail[k] = map[string]any{"limit": lim[k], "count": n}
			line := fmt.Sprintf("%s %d of %d (%d%%)", k, n, lim[k], n*100/lim[k])
			counted = append(counted, line)
			if n*100 >= lim[k]*warnPercent {
				near = append(near, line)
			}
		}
		if len(near) > 0 {
			return Result{Type: Warning, Detail: detail, Message: "Debrid account near its torrent limit: " +
				strings.Join(near, "; ") + ". Nothing is removed automatically; see " + uipages.Torrents}
		}
		if len(counted) == 0 {
			return Result{Type: OK, Detail: detail, Message: "Torrent limits: no complete provider sync yet"}
		}
		return Result{Type: OK, Detail: detail, Message: "Torrent limits: " + strings.Join(counted, "; ")}
	}
}

// AccountReader reads a debrid account (provider.Provider's Account).
type AccountReader interface {
	Account(ctx context.Context) (*provider.Account, error)
}

// AccountSource is one provider's client for Accounts, with a fingerprint of its credential (a hash, never the
// credential itself): a read is reused while the provider and fingerprint stay the same.
type AccountSource struct {
	Reader      AccountReader
	Fingerprint string
}

// Fingerprint is the AccountSource fingerprint of a credential: its SHA-256, in hex.
func Fingerprint(credential string) string {
	h := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(h[:])
}

// AccountWarnWithin is how long before a premium plan ends Accounts starts to warn.
const AccountWarnWithin = 7 * 24 * time.Hour

// AccountOptions tune Accounts. The zero value is fine.
type AccountOptions struct {
	// Interval is how long a good read is reused. Default 6h.
	Interval time.Duration
	// Retry is how long a failed read is reused. Default 30m.
	Retry time.Duration
	// Timeout bounds each read, inside the check's own. Default 8s.
	Timeout time.Duration
	Now     func() time.Time
}

type accountRead struct {
	at      time.Time
	account *provider.Account
	err     error
}

// Accounts checks each enabled debrid account's plan (providers.<key>, with a key set): an error when it is not
// premium, its premium has ended or the provider refuses the key; a warning when premium ends within
// AccountWarnWithin, or when a provider does not answer. accounts gives the running clients, so reads go through the
// gateway and the provider's pacing. A read is reused for Interval (a failed one for Retry), keyed on the provider
// and its credential's fingerprint: a settings save rebuilds the clients but keeps an unchanged account's read, and a
// changed key is read at once. The expiry is judged against the clock at every run.
func Accounts(accounts func(ctx context.Context) map[string]AccountSource, o AccountOptions) Func {
	if o.Interval <= 0 {
		o.Interval = 6 * time.Hour
	}
	if o.Retry <= 0 {
		o.Retry = 30 * time.Minute
	}
	if o.Timeout <= 0 {
		o.Timeout = 8 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	type cacheKey struct{ provider, fingerprint string }
	var mu sync.Mutex
	cache := map[cacheKey]accountRead{}
	return func(ctx context.Context) Result {
		clients := accounts(ctx)
		if len(clients) == 0 {
			return Result{Type: OK, Message: "No debrid provider keys set; no account to read"}
		}
		keys := slices.Sorted(maps.Keys(clients))
		mu.Lock()
		defer mu.Unlock()
		reads := make([]accountRead, len(keys))
		var wg sync.WaitGroup
		for i, k := range keys {
			c := clients[k].Reader
			r, ok := cache[cacheKey{k, clients[k].Fingerprint}]
			if ok && (r.err == nil && o.Now().Sub(r.at) < o.Interval || r.err != nil && o.Now().Sub(r.at) < o.Retry) {
				reads[i] = r
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				rctx, cancel := context.WithTimeout(ctx, o.Timeout)
				defer cancel()
				a, err := c.Account(rctx)
				if err == nil && a == nil {
					err = errors.New("no account in the answer")
				}
				reads[i] = accountRead{at: o.Now(), account: a, err: err}
			}()
		}
		wg.Wait()
		// A caller that gave up cut the reads short: keep them out of the cache. Providers removed or with a changed
		// key are dropped.
		if ctx.Err() == nil {
			next := map[cacheKey]accountRead{}
			for i, k := range keys {
				next[cacheKey{k, clients[k].Fingerprint}] = reads[i]
			}
			cache = next
		}
		now := o.Now()
		typ := OK
		var issues, fine []string
		var refused bool
		detail := map[string]any{}
		for i, k := range keys {
			r := reads[i]
			d := map[string]any{"read_at": r.at.UTC()}
			detail[k] = d
			t, line := OK, ""
			switch a := r.account; {
			case errors.Is(r.err, provider.ErrAuth):
				t, line, refused = Error, k+": key refused ("+short(r.err.Error())+")", true
			case r.err != nil:
				t, line = Warning, k+": account not readable ("+short(r.err.Error())+")"
			case !a.Expires.IsZero() && !a.Expires.After(now):
				t, line = Error, k+": premium ended "+a.Expires.UTC().Format(time.DateOnly)
			case !a.Premium:
				t, line = Error, k+": not premium"
			case !a.Expires.IsZero() && a.Expires.Sub(now) < AccountWarnWithin:
				t, line = Warning, k+": premium ends "+a.Expires.UTC().Format(time.DateOnly)+" (in "+age(a.Expires.Sub(now))+")"
			case a.Expires.IsZero():
				line = k + ": premium"
			default:
				line = k + ": premium until " + a.Expires.UTC().Format(time.DateOnly)
			}
			if r.err != nil {
				d["error"] = short(r.err.Error())
			} else {
				d["premium"] = r.account.Premium
				if !r.account.Expires.IsZero() {
					d["expires"] = r.account.Expires.UTC()
				}
			}
			if t == OK {
				fine = append(fine, line)
				continue
			}
			issues = append(issues, line)
			if Rank(t) > Rank(typ) {
				typ = t
			}
		}
		if typ == OK {
			return Result{Type: OK, Detail: detail, Message: "Debrid accounts: " + strings.Join(fine, "; ")}
		}
		msg := "Debrid account needs attention: " + strings.Join(issues, "; ")
		if refused {
			msg += ". Check the key in " + uipages.Providers
		}
		return Result{Type: typ, Detail: detail, Message: msg}
	}
}

// Indexers checks the gateway's view of the policy's indexers (the arrs' IndexerStatusCheck): indexers in a back-off
// after failures, and indexers whose budget is spent for the hour or day. All of them unavailable is an error.
func Indexers(state func() gateway.State, indexers []string) Func {
	return func(ctx context.Context) Result {
		if len(indexers) == 0 {
			return Result{Type: OK, Message: "No indexers configured"}
		}
		s := state()
		down := map[string]string{}
		for _, b := range s.Backoffs {
			if k, ok := strings.CutPrefix(b.Subject, string(gateway.ClassIndexer)+":"); ok && b.Active && slices.Contains(indexers, k) {
				down[k] = "backed off: " + short(b.Reason)
			}
		}
		for _, b := range s.Budgets {
			spent := b.HourCapped || b.DayCapped || b.Backoff != nil && b.Backoff.Active
			if !spent {
				continue
			}
			for _, u := range b.UsedBy {
				if k, ok := strings.CutPrefix(u, string(gateway.ClassIndexer)+":"); ok && slices.Contains(indexers, k) && down[k] == "" {
					down[k] = "budget " + b.Key + " spent"
				}
			}
		}
		if len(down) == 0 {
			return Result{Type: OK, Message: fmt.Sprintf("%d indexer(s) available", len(indexers))}
		}
		// Which indexers and why stay in Detail (API, System › Status); Message is what the Health webhook sends, so it
		// gives counts only.
		detail := map[string]any{"unavailable": down}
		if len(down) == len(indexers) {
			return Result{Type: Error, Detail: detail, Message: fmt.Sprintf("All %d indexer(s) are unavailable "+
				"(backed off or budget spent): see "+uipages.Status+" for which", len(indexers))}
		}
		return Result{Type: Warning, Detail: detail, Message: fmt.Sprintf("%d of %d indexer(s) unavailable "+
			"(backed off or budget spent): see "+uipages.Status+" for which", len(down), len(indexers))}
	}
}

// Egress checks the gateway's VPN egress check (policy egress): every path fresh and on a VPN exit. Without an egress
// policy it reports a warning that the guard is off.
func Egress(summary func() map[string]any, configured bool) Func {
	return func(ctx context.Context) Result {
		if !configured {
			return Result{Type: Warning, Message: "VPN guard off"}
		}
		s := summary()
		if s["mode"] == "off" {
			return Result{Type: Warning, Detail: s, Message: "VPN guard off"}
		}
		if s["mode"] == "monitor-only" {
			if ok, _ := s["vpn_ok"].(bool); ok {
				return Result{Type: OK, Detail: s, Message: "VPN egress confirmed (monitor-only; no requests require VPN)"}
			}
			return Result{Type: Warning, Detail: s, Message: "VPN egress not confirmed (monitor-only; no requests require VPN)"}
		}
		if ok, _ := s["vpn_ok"].(bool); ok {
			return Result{Type: OK, Detail: s, Message: "VPN egress confirmed"}
		}
		why := "no check has passed yet"
		if paths, _ := s["paths"].([]map[string]any); len(paths) > 0 {
			var errs []string
			for _, p := range paths {
				if e, _ := p["error"].(string); e != "" {
					errs = append(errs, fmt.Sprint(p["path"])+": "+short(e))
				} else if st, _ := p["stale"].(bool); st {
					errs = append(errs, fmt.Sprint(p["path"])+": stale")
				}
			}
			if len(errs) > 0 {
				why = strings.Join(errs, "; ")
			}
		}
		classes, _ := s["require_vpn_for"].([]string)
		return Result{Type: Error, Detail: s, Message: "VPN egress not confirmed; requests requiring VPN wait (" + strings.Join(classes, ", ") + "): " + why}
	}
}

// DiskOptions set the free-space limits for a directory.
type DiskOptions struct {
	// WarnBytes and ErrorBytes: free space that raises each. Defaults 2 GiB and 256 MiB.
	WarnBytes, ErrorBytes uint64
	// WarnPercent: a free share that warns too. Default 5. A share alone never makes an error: on a large disk a
	// small share is still plenty for a database.
	WarnPercent float64
}

// Disk checks free space on the filesystem holding dir (the data directory: database, WAL, backups, image cache).
func Disk(dir string, o DiskOptions) Func {
	if o.WarnBytes == 0 {
		o.WarnBytes = 2 << 30
	}
	if o.ErrorBytes == 0 {
		o.ErrorBytes = 256 << 20
	}
	if o.WarnPercent == 0 {
		o.WarnPercent = 5
	}
	return func(ctx context.Context) Result {
		free, total, err := diskFree(dir)
		if err != nil {
			return fail("Free space of %s not readable: %v", dir, err)
		}
		pct := 0.0
		if total > 0 {
			pct = float64(free) / float64(total) * 100
		}
		detail := map[string]any{"path": dir, "free_bytes": free, "total_bytes": total}
		msg := fmt.Sprintf("%s free on %s (%.1f%%)", bytesText(free), dir, pct)
		switch {
		case free < o.ErrorBytes:
			return Result{Type: Error, Detail: detail, Message: "Disk space low: " + msg}
		case free < o.WarnBytes || pct < o.WarnPercent:
			return Result{Type: Warning, Detail: detail, Message: "Disk space getting low: " + msg}
		}
		return Result{Type: OK, Detail: detail, Message: msg}
	}
}

func bytesText(n uint64) string {
	const gib = 1 << 30
	if n >= gib {
		return fmt.Sprintf("%.1f GiB", float64(n)/gib)
	}
	return fmt.Sprintf("%d MiB", n>>20)
}
