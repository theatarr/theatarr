package nowplaying

import (
	"context"
	"errors"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/outcome"
	"github.com/theatarr/theatarr/internal/plex"
	"github.com/theatarr/theatarr/internal/router"
)

// A file that stops working while it plays (docs/design/now-playing.md). A sign of trouble on a file someone plays
// (Plex says buffering, the playback ended early, or zurg refuses its reads or calls it broken) starts a check of that
// file, in one worker beside the poll: Theatarr reads a piece of it through the mount, the same on every mount. Two
// failed reads 20 s apart, a mount that still lists, a control read of another release and a third failed read
// confirm it. Then the same release is taken from another account when one has it cached (the link does not change),
// else the file is marked dead at once and one search for it is queued (internal/library MarkBrokenPlaying), and the
// release's other files are read the same way. Nothing is ever read from a provider directly.

const (
	// pieceSize is how much a check reads, pieceLimit its time limit, pieceAhead how far past the play position.
	pieceSize  = library.PlayingPiece
	pieceLimit = library.PlayingPieceLimit
	pieceAhead = 64 << 20
	// retryGap is the wait between the first two reads.
	retryGap = 20 * time.Second
	// readFineFor: a file that read fine (or gave no verdict) is not checked again this soon.
	readFineFor = 2 * time.Minute
	// earlyStop: a playback that ends before this share of its duration is a sign.
	earlyStop = 0.9
	// After another account took the release, the same range is read every copyEvery for copyFor; the viewer is
	// told after copyQuiet without a readable file.
	copyEvery = 10 * time.Second
	copyFor   = 60 * time.Second
	copyQuiet = 30 * time.Second
	// settleEvery is how often the worker looks whether an incident's search has ended (SettlePlayingRuns).
	settleEvery = 30 * time.Second
)

// Rescuer is what the check uses of the library (internal/library.Library): the piece reads, the mount's listing, the
// incident's record and the dead mark. Options.Mount's library provides it; without it nothing is checked.
type Rescuer interface {
	ReadPiece(ctx context.Context, target string, off, n int64, limit time.Duration) (int64, error)
	MountListed(ctx context.Context) error
	MountFileSize(ctx context.Context, target string) (int64, error)
	PlayingWritable(version string) bool
	PlayingBoundReached(ctx context.Context, titleID int64, version string) (bool, error)
	PlayingHash(ctx context.Context, linkID int64) (string, error)
	PlayingRoute(version string) (client string, providers []string)
	OpenPlayingIncident(ctx context.Context, inc library.PlayingIncident, f library.PlayingFile) (int64, error)
	SettlePlayingIncident(ctx context.Context, incident int64, settled string) error
	PlayingMiss(ctx context.Context, incident int64, keys []string) error
	MarkBrokenPlaying(ctx context.Context, incident int64, files []library.PlayingFile, search bool,
		missKeys []string) (int, error)
	QueuePlayingSearch(ctx context.Context, incident int64) (int, error)
	ReportPlaying(ctx context.Context, incident int64, kind string) error
	SettlePlayingRuns(ctx context.Context) error
	ForgetRelease(ctx context.Context, target string) error
	LastSweep() time.Time
	ComeBack(ctx context.Context) error
}

// The library is the check's Rescuer.
var _ Rescuer = (*library.Library)(nil)

// sign is a reason to check a playing file now.
type sign struct {
	key  string       // the session
	live bool         // the session is still open (a stop's check comes after a live one's)
	s    plex.Session // as at the sign
	src  Source
	// servedBy is the account zurg last served the file from.
	servedBy string
	why      string
}

// rescueStart is the worker's next job: a check, or the incident settling.
type rescueStart struct {
	check  *sign
	settle bool
}

// eligible reports whether a playing file may be checked: a certain Theatarr symlink into the mount (Source.certain).
func eligible(se *session) bool {
	return se.src != nil && se.src.LinkID != 0 && se.src.certain
}

// noteSign queues a check of se's file for why (t.mu held): one per link, a live session's before a stopped one's,
// none for a file that read fine within readFineFor or whose check concluded.
func (t *Tracker) noteSign(se *session, live bool, why string, now time.Time) {
	if !eligible(se) || t.rescuer() == nil {
		return
	}
	id := se.src.LinkID
	if at, ok := t.quiet[id]; ok && now.Before(at) {
		return
	}
	for i, q := range t.signs {
		if q.src.LinkID == id {
			if live && !q.live {
				t.signs[i] = sign{key: se.s.SessionKey, live: true, s: se.s, src: *se.src, servedBy: se.servedBy, why: why}
			}
			return
		}
	}
	t.signs = append(t.signs, sign{key: se.s.SessionKey, live: live, s: se.s, src: *se.src, servedBy: se.servedBy,
		why: why})
}

// liveSigns notes the signs of the open sessions (t.mu held): Plex's buffering verdict, and with zurg a blocked read
// of the file in /stats or a broken-file or broken-link line about it in its log.
func (t *Tracker) liveSigns(now time.Time) {
	for _, se := range t.sess {
		switch {
		case se.health.Verdict == "buffering":
			t.noteSign(se, true, "buffering", now)
		case t.blockedRead(se):
			t.noteSign(se, true, "zurg refuses its reads", now)
		case t.zurgBroken(se):
			t.noteSign(se, true, "zurg says it is broken", now)
		}
	}
}

// blockedRead reports whether zurg's /stats lists a blocked read of se's file.
func (t *Tracker) blockedRead(se *session) bool {
	if t.stats == nil {
		return false
	}
	base, target := path.Base(se.s.File), base0(se.src)
	for _, b := range t.stats.BlockedReads {
		f := path.Base(b.Filename)
		if b.Filename == "" {
			f = path.Base(b.Path)
		}
		if f == base || f == target {
			return true
		}
	}
	return false
}

// zurgBroken reports whether zurg's log called se's file broken, or its download link broken, recently.
func (t *Tracker) zurgBroken(se *session) bool {
	base, target := path.Base(se.s.File), base0(se.src)
	return slices.ContainsFunc(t.events, func(e zurgEvent) bool {
		return (e.Kind == kindBroken || e.Kind == kindLink) && (e.File == base || e.File == target)
	})
}

// stopSign notes the sign of a session Plex no longer lists (t.mu held): it ended below earlyStop of its duration.
func (t *Tracker) stopSign(se *session, now time.Time) {
	if se.s.Duration > 0 && float64(se.s.ViewOffset) < earlyStop*float64(se.s.Duration) {
		t.noteSign(se, false, "stopped early", now)
	}
}

// nextRescue picks the worker's next job (t.mu held): the first live check, else the first stopped one, else the
// incidents' settling when it is due. One job runs at a time.
func (t *Tracker) nextRescue(now time.Time) *rescueStart {
	if t.rescuing || t.rescuer() == nil {
		return nil
	}
	i := slices.IndexFunc(t.signs, func(s sign) bool { return s.live })
	if i < 0 && len(t.signs) > 0 {
		i = 0
	}
	if i >= 0 {
		s := t.signs[i]
		t.signs = slices.Delete(t.signs, i, i+1)
		t.rescuing = true
		return &rescueStart{check: &s}
	}
	if now.Sub(t.settledAt) >= settleEvery {
		t.rescuing, t.settledAt = true, now
		return &rescueStart{settle: true}
	}
	return nil
}

// rescuer is the library as the check uses it; nil without one.
func (t *Tracker) rescuer() Rescuer {
	if t.o.Mount == nil {
		return nil
	}
	r, _ := t.o.Mount().(Rescuer)
	return r
}

// runRescue runs one job of the worker.
func (t *Tracker) runRescue(ctx context.Context, j *rescueStart) {
	defer func() {
		t.mu.Lock()
		t.rescuing = false
		t.mu.Unlock()
	}()
	lib := t.rescuer()
	if lib == nil {
		return
	}
	if j.settle {
		if err := lib.SettlePlayingRuns(ctx); err != nil && ctx.Err() == nil {
			t.o.Logger.Warn("nowplaying: settling broken files' searches", "err", err)
		}
		// After each hourly sweep the old copies are read here, one at a time and off the sweep.
		if swept := lib.LastSweep(); swept.After(t.comeBackAt) {
			t.comeBackAt = swept
			if err := lib.ComeBack(ctx); err != nil && ctx.Err() == nil {
				t.o.Logger.Warn("nowplaying: reading returned copies", "err", err)
			}
		}
		return
	}
	t.check(ctx, lib, j.check)
}

// readResult is a piece read's answer.
type readResult int

const (
	readOK readResult = iota
	readFailed
	readNoVerdict
)

func (t *Tracker) read(ctx context.Context, lib Rescuer, target string, off int64) readResult {
	_, err := lib.ReadPiece(ctx, target, off, pieceSize, pieceLimit)
	switch {
	case err == nil:
		return readOK
	case errors.Is(err, library.ErrPieceUnreadable):
		return readFailed
	}
	return readNoVerdict
}

// pieceOffset is where the check reads: past the play position by pieceAhead (past what the mount's cache likely
// holds, where the viewer is going), never into the second half of what is left (players read the tail on open, so
// it is cached).
func pieceOffset(viewMs, durationMs, size int64) int64 {
	pos := int64(0)
	if durationMs > 0 && viewMs > 0 {
		pos = int64(float64(size) * min(float64(viewMs)/float64(durationMs), 1))
	}
	return min(pos+pieceAhead, (pos+size)/2)
}

// quietFor keeps a file from being checked again until d from now.
func (t *Tracker) quietFor(id int64, d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.quiet[id] = t.o.Now().Add(d)
	for k, at := range t.quiet {
		if t.o.Now().After(at) {
			delete(t.quiet, k)
		}
	}
}

// check runs the read sequence on a signed file and, when it is confirmed broken, the repair.
func (t *Tracker) check(ctx context.Context, lib Rescuer, j *sign) {
	src := &j.src
	log := t.o.Logger.With("title", src.Title, "version", src.version, "link_id", src.LinkID, "sign", j.why)
	fine := func() { t.quietFor(src.LinkID, readFineFor) }
	var state string
	if err := t.o.Store.Reader().QueryRowContext(ctx, `SELECT state FROM link WHERE id = ?`, src.LinkID).
		Scan(&state); err != nil || state != "active" || !lib.PlayingWritable(src.version) {
		fine() // a link the sweep marked dead meanwhile is the hourly path's
		return
	}
	size := j.s.Size
	if size <= 0 {
		var err error
		if size, err = lib.MountFileSize(ctx, src.Target); err != nil {
			fine()
			return
		}
	}
	if size < 2*pieceSize { // too small for a piece: no check
		fine()
		return
	}
	off := pieceOffset(j.s.ViewOffset, j.s.Duration, size)
	if t.read(ctx, lib, src.Target, off) != readFailed {
		fine()
		return
	}
	log.Info("nowplaying: a playing file did not read; reading it again", "offset", off)
	if t.sleep(ctx, retryGap) != nil || t.read(ctx, lib, src.Target, off) != readFailed {
		fine()
		return
	}
	if err := lib.MountListed(ctx); err != nil {
		log.Info("nowplaying: a playing file did not read, but the mount does not list: not judged", "err", err)
		fine()
		return
	}
	hash, err := lib.PlayingHash(ctx, src.LinkID)
	if err != nil {
		fine()
		return
	}
	verdict := t.control(ctx, lib, j, hash)
	if verdict.where == whereMount || verdict.where == whereNone {
		log.Info("nowplaying: a playing file did not read, nor did the control read: not judged",
			"control", verdict.where)
		fine()
		return
	}
	if t.read(ctx, lib, src.Target, off) != readFailed {
		fine()
		return
	}
	log.Info("nowplaying: a playing file stopped working", "where", verdict.where, "serving", verdict.serving,
		"hash", hash)
	t.quietFor(src.LinkID, 24*time.Hour)
	t.repair(ctx, lib, j, hash, off, verdict)
}

// Where a confirmed break lies.
const (
	whereRelease  = "release"  // only this release: another release on its account reads
	whereProvider = "provider" // its account: another release on it fails too, one on another account reads
	whereMount    = "mount"    // the mount: the control reads fail too, or there is nothing to compare
	whereNone     = ""         // no verdict
)

type controlVerdict struct {
	where   string
	serving string   // the account serving the file, "" when unknown
	holders []string // the accounts holding the release complete per the record
}

// control decides where the break lies by reading another release through the mount (section 3 of the plan): one
// held only by the serving account (zurg's servedBy, else the release's only complete holder), at mid-file. It reads:
// only this release is broken. It fails: one held only by another account is read; it reads: the serving account is
// out; it fails, or there is none: the mount. zurg saying the serving account cools down means it is out too. With the
// serving account unknown any other release is read; it must pass.
func (t *Tracker) control(ctx context.Context, lib Rescuer, j *sign, hash string) controlVerdict {
	v := controlVerdict{serving: j.servedBy}
	if hash != "" {
		v.holders = t.strings(ctx, `SELECT DISTINCT provider FROM provider_torrent WHERE info_hash = ? AND progress >= 100
			AND state <> 'error' ORDER BY provider`, hash)
	}
	if v.serving == "" && len(v.holders) == 1 {
		v.serving = v.holders[0]
	}
	if v.serving != "" && t.coolingDown(v.serving) {
		v.where = whereProvider
		return v
	}
	folder := releaseFolder(j.src.Target, t.o.AllDir)
	target, ok := t.otherRelease(ctx, j.src.LinkID, hash, folder, v.serving, true)
	if !ok {
		target, ok = t.otherRelease(ctx, j.src.LinkID, hash, folder, "", false)
		if !ok {
			v.where = whereMount
			return v
		}
		v.serving = "" // nothing held by it alone to tell its account from this release
	}
	switch t.readMid(ctx, lib, target) {
	case readOK:
		v.where = whereRelease
		return v
	case readNoVerdict:
		return v
	}
	if v.serving == "" {
		v.where = whereMount
		return v
	}
	target, ok = t.otherRelease(ctx, j.src.LinkID, hash, folder, v.serving, false)
	if !ok {
		v.where = whereMount
		return v
	}
	switch t.readMid(ctx, lib, target) {
	case readOK:
		v.where = whereProvider
	case readFailed:
		v.where = whereMount
	}
	return v
}

// coolingDown reports whether zurg's log says account cools down (t.mu taken).
func (t *Tracker) coolingDown(account string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.ContainsFunc(t.events, func(e zurgEvent) bool { return e.Kind == kindCooldown && e.Account == account })
}

// readMid reads a piece at the middle of a link target.
func (t *Tracker) readMid(ctx context.Context, lib Rescuer, target string) readResult {
	size, err := lib.MountFileSize(ctx, target)
	if err != nil || size < 2*pieceSize {
		return readNoVerdict
	}
	return t.read(ctx, lib, target, size/2)
}

// releaseFolder is a target's release folder under the release root, "" when it has none.
func releaseFolder(target, allDir string) string {
	folder, _, _ := strings.Cut(mountRel(target, allDir), "/")
	return folder
}

// otherRelease picks at random the target of an active Theatarr symlink of another release (another stored hash and
// another release folder than the checked file's): with account set, one whose release only that account holds
// (only true) or only another single account holds (only false); with account "", any.
func (t *Tracker) otherRelease(ctx context.Context, linkID int64, hash, folder, account string, only bool) (string, bool) {
	q := `SELECT target FROM (SELECT l.target, ` + router.LinkStoredHashSQL + ` AS h FROM link l
		WHERE l.state = 'active' AND l.kind = 'symlink' AND l.id <> ? AND instr(l.target, ?) = 0) WHERE COALESCE(h, '') <> ?`
	args := []any{linkID, "/" + t.o.AllDir + "/" + folder + "/", hash}
	if folder == "" {
		args[1] = "\x00" // no folder to leave out
	}
	if account != "" {
		cmp := "="
		if !only {
			cmp = "<>"
		}
		q += ` AND h IN (SELECT info_hash FROM provider_torrent WHERE progress >= 100 AND state <> 'error'
			GROUP BY info_hash HAVING COUNT(DISTINCT provider) = 1 AND MIN(provider) ` + cmp + ` ?)`
		args = append(args, account)
	} else {
		q += ` AND COALESCE(h, '') <> ''`
	}
	var target string
	err := t.o.Store.Reader().QueryRowContext(ctx, q+` ORDER BY random() LIMIT 1`, args...).Scan(&target)
	return target, err == nil
}

// repair acts on a confirmed break: the bound, the incident, the same release from another account (step 1), else the
// dead mark and its search (step 2), then the release's other files.
func (t *Tracker) repair(ctx context.Context, lib Rescuer, j *sign, hash string, off int64, v controlVerdict) {
	src := &j.src
	log := t.o.Logger.With("title", src.Title, "version", src.version, "link_id", src.LinkID)
	if hit, err := lib.PlayingBoundReached(ctx, src.TitleID, src.version); err != nil {
		return
	} else if hit {
		log.Info("nowplaying: a playing file stopped working; one was replaced for this title today already: left to the hourly sweep")
		t.tell(ctx, j, msgLater)
		return
	}
	if !lib.PlayingWritable(src.version) {
		return
	}
	inc := library.PlayingIncident{TitleID: src.TitleID, Version: src.version, Viewer: j.s.UserName, Player: j.s.Player}
	client, providers := lib.PlayingRoute(src.version)
	failed := v.holders
	if v.serving != "" {
		failed = []string{v.serving}
	}
	if v.where == whereProvider {
		inc.OutAccount = v.serving
	}
	if client != "" {
		failed = []string{client}
	}
	id, err := lib.OpenPlayingIncident(ctx, inc, library.PlayingFile{LinkID: src.LinkID, Offset: off})
	if err != nil {
		log.Warn("nowplaying: incident not recorded", "err", err)
		return
	}
	log = log.With("incident", id)
	others := slices.DeleteFunc(slices.Clone(providers), func(p string) bool { return slices.Contains(failed, p) })
	if v.where == whereProvider && (client != "" || len(others) == 0) {
		// The account is out and no other can be chosen (a client picks its own account): nothing is replaced.
		log.Info("nowplaying: its account is out and the library has no other: nothing replaced")
		t.settleNone(ctx, lib, id)
		t.tell(ctx, j, msgLater)
		return
	}
	told := false
	if client == "" && len(hash) == 40 && t.o.Copy != nil {
		switch t.sameRelease(ctx, lib, j, id, hash, off, others, &told) {
		case copyReadable:
			if err := lib.SettlePlayingIncident(ctx, id, "copy"); err != nil {
				log.Warn("nowplaying: incident not settled", "err", err)
			}
			if err := lib.ReportPlaying(ctx, id, outcome.SameRelease); err != nil {
				log.Warn("nowplaying: outcome not sent", "err", err)
			}
			t.siblings(ctx, lib, j, id)
			if _, err := lib.QueuePlayingSearch(ctx, id); err != nil {
				log.Warn("nowplaying: search of the release's other files not queued", "err", err)
			}
			return
		}
	}
	if !told {
		t.tell(ctx, j, msgLooking)
	}
	n, err := lib.MarkBrokenPlaying(ctx, id, []library.PlayingFile{{LinkID: src.LinkID, Offset: off}}, true, failed)
	if err != nil || n == 0 {
		log.Info("nowplaying: the broken file was not marked", "err", err)
		if err := lib.SettlePlayingIncident(ctx, id, "none"); err != nil {
			log.Warn("nowplaying: incident not settled", "err", err)
		}
		return
	}
	log.Info("nowplaying: the broken file is marked dead and searched for at once")
	t.siblings(ctx, lib, j, id)
}

// settleNone settles an incident where nothing was done, and tells its outcome.
func (t *Tracker) settleNone(ctx context.Context, lib Rescuer, id int64) {
	if err := lib.SettlePlayingIncident(ctx, id, "none"); err != nil {
		t.o.Logger.Warn("nowplaying: incident not settled", "incident", id, "err", err)
	}
	if err := lib.ReportPlaying(ctx, id, outcome.Unavailable); err != nil {
		t.o.Logger.Warn("nowplaying: outcome not sent", "incident", id, "err", err)
	}
}

// Step 1's answers.
const (
	copyNone     = iota // no other account has the release cached
	copyReadable        // the file reads again
	copyFailed          // an account took it, yet the file still does not read
)

// sameRelease is step 1: the same release added on another account with it cached, one account at a time in the
// library's provider order ("Add to other providers", automatic: Pause, pacing, capacity and ownership as for the
// automatic copy). An account counts only when the release was added there or is there complete; the same range is
// then read every copyEvery for copyFor (the viewer is told after copyQuiet). Still failing: that account gets the
// miss too (no other is tried), and step 2 follows.
func (t *Tracker) sameRelease(ctx context.Context, lib Rescuer, j *sign, id int64, hash string, off int64,
	accounts []string, told *bool) int {
	for _, acct := range accounts {
		ok, err := t.o.Copy(ctx, hash, acct)
		if err != nil || !ok {
			continue
		}
		t.o.Logger.Info("nowplaying: the release is on another account; reading the file again", "account", acct,
			"incident", id)
		if err := lib.ForgetRelease(ctx, j.src.Target); err != nil {
			t.o.Logger.Debug("nowplaying: clearing the release's cached listing", "err", err)
		}
		start := t.o.Now()
		for t.o.Now().Sub(start) < copyFor {
			if t.sleep(ctx, copyEvery) != nil {
				return copyFailed
			}
			if t.read(ctx, lib, j.src.Target, off) == readOK {
				return copyReadable
			}
			if !*told && t.o.Now().Sub(start) >= copyQuiet {
				*told = true
				t.tell(ctx, j, msgLooking)
			}
		}
		if err := lib.PlayingMiss(ctx, id, []string{acct}); err != nil {
			t.o.Logger.Warn("nowplaying: miss not recorded", "err", err)
		}
		return copyFailed
	}
	return copyNone
}

// siblings reads the release's other active files one at a time, a piece at mid-file each, once the watched file is
// handled. One whose read and an immediate retry both fail joins the incident and is marked dead, without a search of
// its own: their search is queued together once the watched file's search has ended (SettlePlayingRuns).
func (t *Tracker) siblings(ctx context.Context, lib Rescuer, j *sign, id int64) {
	folder := releaseFolder(j.src.Target, t.o.AllDir)
	hash, _ := lib.PlayingHash(ctx, j.src.LinkID)
	if folder == "" || hash == "" { // a hash only guessed from the folder: no sibling check
		return
	}
	rows, err := t.o.Store.Reader().QueryContext(ctx, `SELECT id, target FROM link WHERE state = 'active'
		AND kind = 'symlink' AND id <> ? AND instr(target, ?) > 0 ORDER BY path`, j.src.LinkID,
		"/"+t.o.AllDir+"/"+folder+"/")
	if err != nil {
		return
	}
	type file struct {
		id     int64
		target string
	}
	var files []file
	for rows.Next() {
		var f file
		if rows.Scan(&f.id, &f.target) == nil && releaseFolder(f.target, t.o.AllDir) == folder {
			files = append(files, f)
		}
	}
	rows.Close()
	for _, f := range files {
		size, err := lib.MountFileSize(ctx, f.target)
		if err != nil || size < 2*pieceSize {
			continue
		}
		off := size / 2
		if t.read(ctx, lib, f.target, off) != readFailed {
			continue
		}
		if t.read(ctx, lib, f.target, off) != readFailed { // the immediate retry
			continue
		}
		if n, err := lib.MarkBrokenPlaying(ctx, id, []library.PlayingFile{{LinkID: f.id, Offset: off}}, false,
			nil); err != nil || n == 0 {
			t.o.Logger.Info("nowplaying: a broken file of the release was not marked", "link_id", f.id, "err", err)
			continue
		}
		t.quietFor(f.id, 24*time.Hour)
		t.o.Logger.Info("nowplaying: another file of the release stopped working", "link_id", f.id,
			"file", filepath.Base(f.target), "incident", id)
	}
}

// The messages a stopped stream shows.
const (
	msgLooking = "This file stopped working. Looking for another copy. Try again in a few minutes."
	msgLater   = "This file stopped working. Try again later."
)

// tell is the viewer's message (step 5).
func (t *Tracker) tell(_ context.Context, j *sign, msg string) {
	t.o.Logger.Debug("nowplaying: viewer message", "session", j.key, "message", msg)
}

// realSleep waits d or until ctx ends.
func realSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
