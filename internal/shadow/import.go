package shadow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/importarr"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/store"
)

// ImportSummary is one arr.import run.
type ImportSummary struct {
	Instances []ImportInstance `json:"instances"`
	// Grabs is the number of new grabs handed to shadow.grab.
	Grabs  int    `json:"grabs"`
	TookMS int64  `json:"took_ms"`
	Error  string `json:"error,omitempty"`
}

// ImportInstance is one instance's part of an import run.
type ImportInstance struct {
	Instance string `json:"instance"`
	Version  string `json:"version"`
	// Changes is the rows the import changed; History the new history events.
	Changes int `json:"changes"`
	History int `json:"history"`
	// Grabs are the new grabs after the cursor; Skipped the ones of titles or versions not shadowed.
	Grabs   int `json:"grabs"`
	Skipped int `json:"skipped"`
	// Cursor is the last history id looked at; FirstRun says the cursor was only set.
	Cursor   int    `json:"cursor"`
	FirstRun bool   `json:"first_run,omitempty"`
	Error    string `json:"error,omitempty"`
}

// grabPayload is a shadow.grab job. Existing is the version's file at grab time when the import knew it (the
// version_file row before or after the import, whichever was added before the grab); without it the handler
// rebuilds the file from history.
type grabPayload struct {
	Instance  string               `json:"instance"`
	HistoryID int                  `json:"history_id"`
	Existing  *decide.ExistingFile `json:"existing,omitempty"`
	// NoSearch decides the arr's release only.
	NoSearch bool `json:"no_search,omitempty"`
	// Defers counts the times the grab waited for a search.
	Defers int `json:"defers,omitempty"`
}

type datedFile struct {
	file  decide.ExistingFile
	added int64
}

// Import runs arr.import: every import.arr instance whose key is set, then the new grabs after each cursor.
func (r *Runner) Import(ctx context.Context) (*ImportSummary, error) {
	run, err := r.forOperation(ctx)
	if err != nil {
		return nil, err
	}
	return run.doImport(ctx)
}

func (r *Runner) doImport(ctx context.Context) (*ImportSummary, error) {
	ctx = r.pin(ctx)
	start := r.now()
	sum := &ImportSummary{}
	im := &importarr.Importer{Store: r.st, Now: r.now}
	var errs []error
	for _, inst := range sortedKeys(r.pol.Import.Arr) {
		a := r.pol.Import.Arr[inst]
		ii := ImportInstance{Instance: inst, Version: a.Version}
		err := r.importOne(ctx, im, inst, &ii, sum)
		if err != nil {
			ii.Error = err.Error()
			errs = append(errs, fmt.Errorf("%s: %w", inst, err))
			r.log.Warn("arr import", "instance", inst, "err", err)
		}
		sum.Instances = append(sum.Instances, ii)
	}
	if _, err := r.refresh(ctx); err != nil {
		errs = append(errs, err)
	}
	if _, err := r.recoverPending(ctx); err != nil {
		errs = append(errs, fmt.Errorf("pending grabs: %w", err))
	}
	err := errors.Join(errs...)
	if err != nil {
		sum.Error = err.Error()
	}
	sum.TookMS = r.now().Sub(start).Milliseconds()
	r.record(ctx, KindImport, start, err == nil, sum)
	r.log.Info("arr import", "grabs", sum.Grabs, "took_ms", sum.TookMS, "ok", err == nil)
	if mErr := r.RefreshMetrics(ctx); mErr != nil {
		r.log.Warn("shadow metrics", "err", mErr)
	}
	return sum, err
}

func (r *Runner) importOne(ctx context.Context, im *importarr.Importer, inst string, ii *ImportInstance, sum *ImportSummary) error {
	a := r.pol.Import.Arr[inst]
	g, err := r.arr(inst)
	if err != nil {
		return err
	}
	before, err := r.files(ctx, inst)
	if err != nil {
		return err
	}
	followers := r.pol.Followers(a.Version)
	refresh := r.followerRefresh != nil && len(followers) > 0
	var keysBefore map[int64]string
	if refresh {
		if keysBefore, err = fileKeys(ctx, r.st.Reader(), inst); err != nil {
			return err
		}
	}
	v := r.pol.Versions[a.Version]
	s, err := im.Run(ctx, importarr.Source{Instance: inst, Flavour: a.Flavour, Version: a.Version, Anime: v.Anime,
		Library: v.Library, Followers: followers, CollectionTargets: r.pol.CollectionTargets()}, g)
	if err != nil {
		return err
	}
	ii.Changes, ii.History = s.Changes.Total(), s.History.New
	if len(followers) > 0 && r.followerCatchUp.due(a.Version, r.now()) {
		// The catch-up covers every title, the changed ones too.
		r.catchUpFollowers(ctx, inst, a.Version)
	} else if refresh {
		r.refreshFollowers(ctx, inst, a.Version, keysBefore)
	}
	return r.newGrabs(ctx, inst, before, ii, sum)
}

// SetFollowerRefresh connects the library's follower refresh (library.RefreshFollowers): after each instance's
// import, the parallel versions following its version refresh adopted links and adopt new files, for the titles
// whose arr files the import changed only.
func (r *Runner) SetFollowerRefresh(fn func(ctx context.Context, followed string, titles []int64) error) {
	r.followerRefresh = fn
}

// catchUp runs follower refresh and adoption over every title (library.RefreshFollowersAll), picking up new files
// and upgrades whose per-import pass failed or was skipped (a mount outage, a closed gate, a failed link): the per-import
// trigger fires only on the import that sees the arr's file change. It runs on the first import pass of each
// followed version, then every interval; a failed refresh, per-import or catch-up, makes it due at the next pass.
type catchUp struct {
	fn    func(ctx context.Context, followed string) error
	every time.Duration
	mu    sync.Mutex
	next  map[string]time.Time // by followed version; absent: due
}

// SetFollowerCatchUp connects the follower catch-up (library.RefreshFollowersAll), run every interval and after a
// failed follower refresh (docs/parallel.md "Refresh").
func (r *Runner) SetFollowerCatchUp(fn func(ctx context.Context, followed string) error, every time.Duration) {
	r.followerCatchUp = &catchUp{fn: fn, every: every, next: map[string]time.Time{}}
}

func (c *catchUp) due(version string, now time.Time) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	next, ok := c.next[version]
	return !ok || !now.Before(next)
}

// done schedules the next catch-up after a success, or leaves it due after a failure.
func (c *catchUp) done(version string, now time.Time, ok bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ok {
		c.next[version] = now.Add(c.every)
	} else {
		delete(c.next, version)
	}
}

func (r *Runner) catchUpFollowers(ctx context.Context, inst, version string) {
	r.log.Info("follower refresh: catch-up over every title", "instance", inst, "version", version)
	err := r.followerCatchUp.fn(ctx, version)
	if err != nil {
		r.log.Warn("follower refresh: catch-up failed; due again at the next import", "instance", inst,
			"version", version, "err", err)
	}
	r.followerCatchUp.done(version, r.now(), err == nil)
}

// refreshFollowers hands the titles whose arr files changed in this import to the follower refresh. A failure is
// logged and does not fail the import: it makes the catch-up (SetFollowerCatchUp) due at the next import, and
// theatarr library adopt -refresh does the same by hand.
func (r *Runner) refreshFollowers(ctx context.Context, inst, version string, before map[int64]string) {
	after, err := fileKeys(ctx, r.st.Reader(), inst)
	if err != nil {
		r.log.Warn("follower refresh: arr files not read", "instance", inst, "err", err)
		return
	}
	var titles []int64
	for id, k := range after {
		if before[id] != k {
			titles = append(titles, id)
		}
	}
	for id := range before {
		if _, ok := after[id]; !ok {
			titles = append(titles, id)
		}
	}
	if len(titles) == 0 {
		return
	}
	sort.Slice(titles, func(i, j int) bool { return titles[i] < titles[j] })
	r.log.Info("follower refresh", "instance", inst, "version", version, "titles_changed", len(titles))
	if err := r.followerRefresh(ctx, version, titles); err != nil {
		r.log.Warn("follower refresh", "instance", inst, "version", version, "err", err)
		r.followerCatchUp.done(version, r.now(), false)
	}
}

// fileKeys gives each title of an instance a key of its arr files: their ids, paths and episodes. A title whose key
// changes across an import had a file added, replaced, moved or removed.
func fileKeys(ctx context.Context, db *sql.DB, instance string) (map[int64]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT vf.title_id, vf.arr_file_id, vf.path,
		COALESCE((SELECT GROUP_CONCAT(episode_id) FROM (SELECT episode_id FROM version_file_episode
			WHERE version_file_id = vf.id ORDER BY episode_id)), '')
		FROM version_file vf WHERE vf.instance = ? ORDER BY vf.title_id, vf.arr_file_id`, instance)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var title int64
		var arrID int
		var p, eps string
		if err := rows.Scan(&title, &arrID, &p, &eps); err != nil {
			return nil, err
		}
		out[title] += fmt.Sprintf("%d\x00%s\x00%s\n", arrID, p, eps)
	}
	return out, rows.Err()
}

// files reads an instance's version_file rows by version id.
func (r *Runner) files(ctx context.Context, instance string) (map[int64]datedFile, error) {
	rows, err := r.st.Reader().QueryContext(ctx, `SELECT version_id, quality, quality_revision, scene_name,
		original_file_path, relative_path, release_group, edition, languages, size_bytes, indexer_flags,
		COALESCE(added_at, 0) FROM version_file WHERE instance = ?`, instance)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]datedFile{}
	for rows.Next() {
		var id int64
		var f datedFile
		var langs string
		if err := rows.Scan(&id, &f.file.Quality, &f.file.Revision, &f.file.SceneName, &f.file.OriginalFilePath,
			&f.file.RelativePath, &f.file.ReleaseGroup, &f.file.Edition, &langs, &f.file.Size, &f.file.IndexerFlags,
			&f.added); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(langs), &f.file.Languages)
		if cur, ok := out[id]; !ok || f.added > cur.added {
			out[id] = f
		}
	}
	return out, rows.Err()
}

// fileAt picks the newest of the files added at or before the grab; nil when none was.
func fileAt(at int64, files ...*datedFile) *decide.ExistingFile {
	var best *datedFile
	for _, f := range files {
		if f != nil && f.added > 0 && f.added <= at && (best == nil || f.added > best.added) {
			best = f
		}
	}
	if best == nil {
		return nil
	}
	c := best.file
	return &c
}

// newGrabs hands the grabs after the instance's cursor to shadow.grab and moves the cursor. On the first run it
// only sets the cursor.
func (r *Runner) newGrabs(ctx context.Context, inst string, before map[int64]datedFile, ii *ImportInstance, sum *ImportSummary) error {
	db := r.st.Reader()
	var cursor int
	err := db.QueryRowContext(ctx, `SELECT history_id FROM shadow_cursor WHERE instance = ?`, inst).Scan(&cursor)
	first := errors.Is(err, sql.ErrNoRows)
	if err != nil && !first {
		return err
	}
	var top int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(arr_id), 0) FROM arr_history WHERE instance = ?`,
		inst).Scan(&top); err != nil {
		return err
	}
	if first {
		ii.FirstRun, ii.Cursor = true, top
		return r.setCursor(ctx, inst, top)
	}
	ii.Cursor = cursor
	if top <= cursor {
		return nil
	}
	after, err := r.files(ctx, inst)
	if err != nil {
		return err
	}
	q := `SELECT arr_id, date_at, COALESCE(version_id, 0), title_id IS NOT NULL
		FROM arr_history WHERE instance = ? AND arr_id > ? AND event_type = 'grabbed' ORDER BY arr_id`
	series := r.isSeriesInstance(inst)
	if series {
		q = seriesGrabRows
	}
	rows, err := db.QueryContext(ctx, q, inst, cursor)
	if err != nil {
		return err
	}
	type grab struct {
		id      int
		at      int64
		version int64
		held    bool
	}
	var grabs []grab
	for rows.Next() {
		var g grab
		if err := rows.Scan(&g.id, &g.at, &g.version, &g.held); err != nil {
			rows.Close()
			return err
		}
		grabs = append(grabs, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	_, shadowed := r.versionForInstance(inst)
	var queue []grabPayload
	for _, g := range grabs {
		if !shadowed || !g.held {
			ii.Skipped++
			continue
		}
		var b, a *datedFile
		if f, ok := before[g.version]; ok {
			b = &f
		}
		if f, ok := after[g.version]; ok {
			a = &f
		}
		p := grabPayload{Instance: inst, HistoryID: g.id}
		if !series { // a series grab's files are per episode, read by the grab itself
			p.Existing = fileAt(g.at, b, a)
		}
		ii.Grabs++
		sum.Grabs++
		if r.q == nil {
			// Inline (no queue, theatarr shadow run-once): a grab that fails is logged and passed, as a failed job would be.
			if _, err := r.Grab(ctx, p); err != nil {
				r.log.Warn("shadow grab", "instance", inst, "history_id", g.id, "err", err)
			}
			continue
		}
		queue = append(queue, p)
	}
	if r.q == nil {
		ii.Cursor = top
		return r.setCursor(ctx, inst, top)
	}
	// The grab jobs and the cursor past them commit together: a grab is never behind the cursor without its job.
	if err := r.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, p := range queue {
			if _, _, err := r.q.EnqueueTx(ctx, tx, KindGrab, p, jobs.EnqueueOptions{
				DedupeKey: grabKey(inst, p.HistoryID, 0), Priority: priorityGrab, MaxAttempts: 3}); err != nil {
				return err
			}
		}
		return r.setCursorTx(ctx, tx, inst, top)
	}); err != nil {
		return fmt.Errorf("handing off %d grab(s): %w", len(queue), err)
	}
	ii.Cursor = top
	return nil
}

// recoverPending enqueues again the pending grabs (deferred searches) that have no job queued, running or failed:
// their job was lost before the deferral and its job committed together. A failed job is left for people to retry
// (theatarr jobs retry), so a grab that keeps failing is not re-run on every import.
func (r *Runner) recoverPending(ctx context.Context) (int, error) {
	if r.q == nil {
		return 0, nil
	}
	rows, err := r.st.Reader().QueryContext(ctx, `SELECT g.instance, g.history_id FROM shadow_grab g
		WHERE g.state = 'pending' AND NOT EXISTS (SELECT 1 FROM job j WHERE j.kind = ?
			AND j.state IN ('queued', 'running', 'failed') AND json_extract(j.payload, '$.instance') = g.instance
			AND json_extract(j.payload, '$.history_id') = g.history_id)
		ORDER BY g.id`, KindGrab)
	if err != nil {
		return 0, err
	}
	var ps []grabPayload
	for rows.Next() {
		var p grabPayload
		if err := rows.Scan(&p.Instance, &p.HistoryID); err != nil {
			rows.Close()
			return 0, err
		}
		ps = append(ps, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, p := range ps {
		_, created, err := r.q.Enqueue(ctx, KindGrab, p, jobs.EnqueueOptions{DedupeKey: grabKey(p.Instance, p.HistoryID, 0),
			Priority: priorityGrab, MaxAttempts: 3})
		if err != nil {
			return n, err
		}
		if created {
			n++
			r.log.Warn("shadow grab: pending with no job; enqueued again", "instance", p.Instance, "history_id", p.HistoryID)
		}
	}
	return n, nil
}

func grabKey(inst string, id, defers int) string {
	k := fmt.Sprintf("%s:%s:%d", KindGrab, inst, id)
	if defers > 0 {
		k += fmt.Sprintf(":%d", defers)
	}
	return k
}

func (r *Runner) setCursor(ctx context.Context, inst string, id int) error {
	return r.st.WriteTx(ctx, func(tx *sql.Tx) error { return r.setCursorTx(ctx, tx, inst, id) })
}

func (r *Runner) setCursorTx(ctx context.Context, tx *sql.Tx, inst string, id int) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO shadow_cursor (instance, history_id, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (instance) DO UPDATE SET history_id = excluded.history_id, updated_at = excluded.updated_at`,
		inst, id, store.Millis(r.now()))
	return err
}

// Cursors returns each instance's cursor (theatarr shadow status).
func Cursors(ctx context.Context, db *sql.DB) (map[string]Cursor, error) {
	rows, err := db.QueryContext(ctx, `SELECT c.instance, c.history_id, c.updated_at,
		(SELECT COUNT(*) FROM arr_history h WHERE h.instance = c.instance AND h.arr_id > c.history_id AND h.event_type = 'grabbed')
		FROM shadow_cursor c`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Cursor{}
	for rows.Next() {
		var c Cursor
		var at int64
		if err := rows.Scan(&c.Instance, &c.HistoryID, &at, &c.Behind); err != nil {
			return nil, err
		}
		c.UpdatedAt = store.FromMillis(at)
		out[c.Instance] = c
	}
	return out, rows.Err()
}

// Cursor is an instance's place in its history.
type Cursor struct {
	Instance  string    `json:"instance"`
	HistoryID int       `json:"history_id"`
	UpdatedAt time.Time `json:"updated_at"`
	// Behind counts stored grabs after the cursor (0 except between an import and its hand-off).
	Behind int `json:"behind"`
}

func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}
