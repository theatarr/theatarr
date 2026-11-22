package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// A return puts an earlier file back where an import displaced it (docs/design/library.md "Undo an upgrade"): a
// plan of exactly one link, the old file's path, mount target and episodes, written through installLinks with every
// fact of its old row as the import's trash row saved it (link_trash.snapshot). No download row, no other library,
// no blocklist from a failure, no retry, no search: a return that cannot be made says why in one sentence and
// changes nothing.

// Return prefixes: the detail of a return's journal actions starts with one, so History reads it as what it is.
const (
	undoPrefix = store.HistoryUndonePrefix
)

// The sentences a refused return answers with.
const (
	returnGone    = "The earlier file is no longer available"
	returnChanged = "The library changed since; nothing was undone"
	returnShared  = "That library path is shared with another copy"
)

// ReturnRefused is a return that changed nothing; Sentence says why. Passing is a refusal that may not hold on a later
// try (a path another run changes, the delete fence, the slot changing between the reads, a read with no verdict):
// the put-back tries again on the next sweep instead of keeping the copy aside.
type ReturnRefused struct {
	Sentence string
	Passing  bool
}

func (e *ReturnRefused) Error() string { return e.Sentence }

// trashSnapshot is a displaced file's link row as an import found it, before the new link took its place
// (link_trash.snapshot), and the link that took it (Successor).
type trashSnapshot struct {
	LinkID        int64   `json:"link_id"`
	TitleID       int64   `json:"title_id"`
	State         string  `json:"state"`
	Target        string  `json:"target"`
	ContentFileID *int64  `json:"content_file_id,omitempty"`
	DownloadID    *int64  `json:"download_id,omitempty"`
	FileMetadata  *string `json:"file_metadata,omitempty"`
	keptFacts
	Episodes  []int64   `json:"episodes"`
	Release   *string   `json:"release,omitempty"`
	Quality   *string   `json:"quality,omitempty"`
	Score     *int64    `json:"score,omitempty"`
	Successor successor `json:"successor"`
}

// relFacts are the snapshot's release facts, as History records the displaced file (linkFacts).
func (s *trashSnapshot) relFacts() relFacts {
	f := relFacts{TitleID: s.TitleID}
	if s.Release != nil {
		f.Release = *s.Release
	}
	if s.Quality != nil {
		f.Quality = *s.Quality
	}
	if s.Score != nil {
		f.Score = sql.NullInt64{Int64: *s.Score, Valid: true}
	}
	if len(s.Episodes) > 0 {
		f.Episodes = s.Episodes
	}
	return f
}

// successor is the link that took a displaced file's place.
type successor struct {
	LinkID     int64  `json:"link_id"`
	Path       string `json:"path"`
	Target     string `json:"target"`
	DownloadID int64  `json:"download_id,omitempty"`
}

// readSnapshot reads the link row id (else the row at path) as a trash snapshot; nil when there is none.
func readSnapshot(ctx context.Context, tx *sql.Tx, id int64, path string) (*trashSnapshot, error) {
	where, arg := `id = ?`, any(id)
	if id == 0 {
		where, arg = `path = ?`, path
	}
	s := &trashSnapshot{}
	err := tx.QueryRowContext(ctx, `SELECT id, title_id, state, target, content_file_id, download_id, file_metadata,
		adopted, adopted_from, adopted_source, adopted_media, folder_hash, release_title, quality, score FROM link
		WHERE `+where, arg).Scan(&s.LinkID, &s.TitleID, &s.State, &s.Target, &s.ContentFileID, &s.DownloadID,
		&s.FileMetadata, &s.Adopted, &s.AdoptedFrom, &s.AdoptedSource, &s.AdoptedMedia, &s.FolderHash, &s.Release,
		&s.Quality, &s.Score)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.Episodes = []int64{}
	rows, err := tx.QueryContext(ctx, `SELECT episode_id FROM link_episode WHERE link_id = ? ORDER BY episode_id`,
		s.LinkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e int64
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		s.Episodes = append(s.Episodes, e)
	}
	return s, rows.Err()
}

// lowerQuality says whether quality now ranks below quality was (both known qualities of the title kind).
func lowerQuality(kind, now, was string) bool {
	fl := format.Radarr
	if policy.Kind(kind) == policy.KindSeries {
		fl = format.Sonarr
	}
	qn, ok := profile.QualityByName(fl, now)
	qw, ok2 := profile.QualityByName(fl, was)
	return ok && ok2 && profile.Weight(fl, qn) < profile.Weight(fl, qw)
}

// undoEntry is the trash entry of a History replacement: the file an import moved aside, as it was.
type undoEntry struct {
	version, path, target string
	snap                  trashSnapshot
	release               string // the replacing release, as the Undo button names it
}

// undoEntryOf resolves a replace action (History's replaced file, by its library_action id) to its trash entry with
// a snapshot, not purged: sql.ErrNoRows when there is none (a replacement before link_trash.snapshot, or a purged
// trash copy).
func (l *Library) undoEntryOf(ctx context.Context, actionID int64) (*undoEntry, error) {
	db := l.st.Reader()
	var detail string
	if err := db.QueryRowContext(ctx, `SELECT detail FROM library_action WHERE id = ? AND kind = 'replace'
		AND mode = 'applied' AND download_id IS NOT NULL`, actionID).Scan(&detail); err != nil {
		return nil, err
	}
	tp, ok := strings.CutPrefix(detail, "moved aside to ")
	if !ok {
		return nil, sql.ErrNoRows
	}
	tp = strings.TrimSuffix(tp, store.HistoryDeadLower)
	e := &undoEntry{}
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT version_key, path, target, snapshot FROM link_trash WHERE trash_path = ?
		AND purged_at IS NULL AND snapshot IS NOT NULL`, tp).Scan(&e.version, &e.path, &e.target, &raw); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(raw), &e.snap); err != nil {
		return nil, err
	}
	if e.snap.Successor.LinkID == 0 {
		return nil, sql.ErrNoRows
	}
	_ = db.QueryRowContext(ctx, `SELECT COALESCE((SELECT release_title FROM download WHERE id = ?),
		(SELECT release_title FROM link WHERE id = ?), '')`, e.snap.Successor.DownloadID, e.snap.Successor.LinkID).
		Scan(&e.release)
	return e, nil
}

// slotHeldSQL says whether the recorded successor still holds the slot of a return r (columns version_key, path and
// snapshot): the same row at its path and target, holding the same download's file, in the return's library, active
// or dead (a dead successor still owns the slot: Undo then helps most); and, when the earlier file had another path,
// no active link at that path.
const slotHeldSQL = `(EXISTS (SELECT 1 FROM link s JOIN link_version sv ON sv.link_id = s.id
		JOIN version v ON v.id = sv.version_id AND v.policy_key = r.version_key AND v.title_id = s.title_id
		WHERE s.id = json_extract(r.snapshot, '$.successor.link_id')
		AND s.path = json_extract(r.snapshot, '$.successor.path') AND s.target = json_extract(r.snapshot, '$.successor.target')
		AND COALESCE(s.download_id, 0) = COALESCE(json_extract(r.snapshot, '$.successor.download_id'), 0)
		AND s.state IN ('active', 'dead'))
	AND (r.path = json_extract(r.snapshot, '$.successor.path')
		OR NOT EXISTS (SELECT 1 FROM link WHERE path = r.path AND state = 'active')))`

// othersHoldSQL says whether another active link of the return r's library, besides the successor, holds the film or
// an episode of the returning file: more than one file took its place, and a one-file return would leave two files on
// one episode.
const othersHoldSQL = `EXISTS (SELECT 1 FROM link o JOIN link_version ov ON ov.link_id = o.id
	JOIN version v ON v.id = ov.version_id AND v.policy_key = r.version_key AND v.title_id = o.title_id
	JOIN title t ON t.id = o.title_id
	WHERE o.state = 'active' AND o.id <> json_extract(r.snapshot, '$.successor.link_id') AND o.path <> r.path
	AND o.title_id = (SELECT title_id FROM link WHERE id = json_extract(r.snapshot, '$.successor.link_id'))
	AND (t.kind <> 'series' OR EXISTS (SELECT 1 FROM link_episode oe WHERE oe.link_id = o.id
		AND oe.episode_id IN (SELECT value FROM json_each(r.snapshot, '$.episodes')))))`

// importingSQL says whether the successor's download is still being imported in the return r's library (a partial
// import waiting for its retry): Undo waits for it.
const importingSQL = `EXISTS (SELECT 1 FROM download WHERE id = json_extract(r.snapshot, '$.successor.download_id')
	AND state IN ('ready', 'importing'))`

// returnImporting is the refusal while the successor's download is still being imported.
const returnImporting = "That download is still being imported; try again when it has finished"

// returnOthers is the refusal when more than one file took the earlier file's place.
const returnOthers = "More than one file took the earlier file's place; nothing was undone"

// slot is slotHeldSQL, othersHoldSQL and importingSQL for this entry.
func (e *undoEntry) slot(ctx context.Context, q rowQuerier) (held, others, importing bool, err error) {
	snap, err := json.Marshal(&e.snap)
	if err != nil {
		return false, false, false, err
	}
	err = q.QueryRowContext(ctx, `WITH r (version_key, path, snapshot) AS (SELECT ?, ?, ?) SELECT `+slotHeldSQL+`, `+
		othersHoldSQL+`, `+importingSQL+` FROM r`, e.version, e.path, string(snap)).Scan(&held, &others, &importing)
	return held, others, importing, err
}

// UndoWords is the sentence the Undo button shows before it acts.
func UndoWords(release string) string {
	return "Undo upgrade: puts the earlier file back and blocks " + release + " in this library"
}

// UndoOffers says, for History's replaced files (their library_action ids), which offer Undo, with the button's
// sentence: the replacement's trash entry and snapshot exist, it is the latest replacement of that slot (the
// recorded successor still holds it, alone), and the earlier file is still on the mount (a stat, its size not zero;
// the Undo itself reads a piece). One query for all of them, then a stat each.
func (l *Library) UndoOffers(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if l.mount == nil || len(ids) == 0 {
		return out, nil
	}
	idj, _ := json.Marshal(ids)
	type offer struct {
		id                                             int64
		version, target, release, succPath, succTarget string
	}
	var offers []offer
	if err := scanRows(ctx, l.st.Reader(), `SELECT la.id, r.version_key, r.target,
		json_extract(r.snapshot, '$.successor.path'), json_extract(r.snapshot, '$.successor.target'),
		COALESCE((SELECT release_title FROM download WHERE id = json_extract(r.snapshot, '$.successor.download_id')),
			(SELECT release_title FROM link WHERE id = json_extract(r.snapshot, '$.successor.link_id')), '')
		FROM library_action la JOIN link_trash r ON r.trash_path = CASE WHEN la.detail LIKE '%' || ?1
			THEN substr(la.detail, 16, length(la.detail) - 15 - length(?1)) ELSE substr(la.detail, 16) END
		WHERE la.id IN (SELECT value FROM json_each(?2)) AND la.kind = 'replace' AND la.mode = 'applied'
		AND la.download_id IS NOT NULL AND la.detail LIKE 'moved aside to %' AND r.purged_at IS NULL
		AND r.snapshot IS NOT NULL AND json_extract(r.snapshot, '$.successor.link_id') > 0
		AND `+slotHeldSQL+` AND NOT `+othersHoldSQL+` AND NOT `+importingSQL, []any{store.HistoryDeadLower, string(idj)},
		func(rs *sql.Rows) error {
			var o offer
			err := rs.Scan(&o.id, &o.version, &o.target, &o.succPath, &o.succTarget, &o.release)
			offers = append(offers, o)
			return err
		}); err != nil {
		return nil, err
	}
	for _, o := range offers {
		if l.pol.Versions[o.version] == nil || !l.gate.Active(o.version) {
			continue
		}
		if cur, err := os.Readlink(o.succPath); err != nil || cur != o.succTarget {
			continue // the successor's symlink is gone or changed
		}
		p, ok := l.mount.FromLink(o.target)
		if !ok {
			continue
		}
		if fi, err := l.mount.Stat(ctx, p); err != nil || fi.Size() == 0 {
			continue
		}
		out[o.id] = UndoWords(o.release)
	}
	return out, nil
}

// Undone is what UndoUpgrade did: the file put back, and the release it blocked in that library.
type Undone struct {
	TitleID int64
	Version string
	Path    string
	Release string
}

// UndoUpgrade puts back the file a History replacement (its library_action id) displaced, from its trash entry's
// snapshot, and blocks the replacing release in that library, marked researched (no search starts). by names who
// asked. A refusal is a *ReturnRefused; sql.ErrNoRows when the replacement has no trash entry with a snapshot.
func (l *Library) UndoUpgrade(ctx context.Context, actionID int64, by string) (*Undone, error) {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.undoUpgrade(pinned, actionID, by)
	}
	return l.undoUpgrade(ctx, actionID, by)
}

func (l *Library) undoUpgrade(ctx context.Context, actionID int64, by string) (*Undone, error) {
	e, err := l.undoEntryOf(ctx, actionID)
	if err != nil {
		return nil, err
	}
	if l.pol.Versions[e.version] == nil {
		return nil, &ReturnRefused{Sentence: returnChanged}
	}
	// The library's write gate, in its own words.
	if err := l.gate.CheckVersion(e.version); err != nil {
		var ref *Refused
		if errors.As(err, &ref) {
			return nil, &ReturnRefused{Sentence: "The library does not write now: " + ref.Detail}
		}
		return nil, err
	}
	var hash, release string
	_ = l.st.Reader().QueryRowContext(ctx, `SELECT COALESCE(info_hash, ''), COALESCE(release_title, '') FROM download
		WHERE id = ?`, e.snap.Successor.DownloadID).Scan(&hash, &release)
	if release == "" {
		release = e.release
	}
	r := &returnFile{entry: e, prefix: undoPrefix + " by " + orSomeone(by) + ": "}
	r.endTx = func(ctx context.Context, tx *sql.Tx, v *importVersion, now int64) error {
		// The undone release stays out of this library: blocked, researched (no search starts).
		if err := router.Blocklist(ctx, tx, v.Key, hash, v.Title.ID, e.snap.Successor.DownloadID, release,
			"the upgrade was undone by "+orSomeone(by), UndoBlockSource(by), now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE router_blocklist SET researched_at = ? WHERE version_key = ?
			AND info_hash = ?`, now, v.Key, router.BlocklistKey(hash, release))
		return err
	}
	v, err := l.returnOne(ctx, r)
	if err != nil {
		return nil, err
	}
	return &Undone{TitleID: v.Title.ID, Version: v.Key, Path: e.path, Release: release}, nil
}

// UndoBlockSource is the blocklist source of an Undo's block: undo:<who> (Activity › Blocklist: "Upgrade undone by
// <who>").
func UndoBlockSource(by string) string { return "undo:" + orSomeone(by) }

func orSomeone(by string) string {
	if by == "" {
		return "someone"
	}
	return by
}

// returnFile is one return: the trash entry it puts back, the prefix of its journal actions, and what it adds to
// the commit (endTx; nil for nothing).
type returnFile struct {
	entry  *undoEntry
	prefix string
	endTx  func(ctx context.Context, tx *sql.Tx, v *importVersion, now int64) error
}

// errReturnFailed is a return whose link or move failed: it is rolled back.
var errReturnFailed = errors.New("library: return failed")

// returnOne writes a return through installLinks: after the leases, the checks (the file reads, the successor still
// holds the slot, the returning file holds every episode of the file it displaces, no other library shares either
// path); any failure takes back every symlink change and leaves no row.
func (l *Library) returnOne(ctx context.Context, r *returnFile) (*importVersion, error) {
	e := r.entry
	db := l.st.Reader()
	s := e.snap.Successor
	v := &importVersion{Key: e.version, Policy: l.pol.Versions[e.version]}
	if err := db.QueryRowContext(ctx, `SELECT l.title_id, t.kind, v.id FROM link l JOIN title t ON t.id = l.title_id
		JOIN version v ON v.title_id = l.title_id AND v.policy_key = ? WHERE l.id = ?`, e.version, s.LinkID).
		Scan(&v.Title.ID, &v.Title.Kind, &v.VersionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &ReturnRefused{Sentence: returnChanged}
		}
		return nil, err
	}
	root := ""
	for _, rt := range l.gate.Roots(e.version) {
		if strings.HasPrefix(e.path, strings.TrimSuffix(rt, "/")+"/") {
			root = rt
		}
	}
	lk := PlannedLink{Path: e.path, Target: e.target, Episodes: e.snap.Episodes}
	lk.prev, _ = os.Readlink(e.path)
	if s.Path != e.path {
		lk.Replaces, lk.ReplaceIDs = []string{s.Path}, []int64{s.LinkID}
	}
	// The file's inputs as they were; its state judged as an import judges it (left to the next pass when the
	// engine cannot judge it).
	lf := linkedFile{}
	lf.metadata, lf.quality = deref(e.snap.FileMetadata), deref(e.snap.Quality)
	if e.snap.Score != nil {
		lf.score = int(*e.snap.Score)
	}
	if l.engine != nil && v.Policy != nil {
		f, err := decide.LinkFileInputs([]byte(cmpOr(lf.metadata, "{}")), v.Title.Kind, deref(e.snap.Release), lf.quality)
		var t *decide.Title
		if err == nil {
			t, err = l.cutoffTitle(ctx, v.Title.ID, v.VersionID, v.Policy)
		}
		if err == nil {
			lf.state, err = l.importedState(v, t, &f)
		}
		if err != nil {
			l.log.Warn("library: return: state left to the next pass", "path", e.path, "err", err)
		}
	}
	plan := &Plan{Version: e.version, Links: []PlannedLink{lk}}
	score, quality := sql.NullInt64{}, sql.NullString{}
	if e.snap.Quality != nil {
		quality = sql.NullString{String: *e.snap.Quality, Valid: true}
	}
	if e.snap.Score != nil {
		score = sql.NullInt64{Int64: *e.snap.Score, Valid: true}
	}
	var dl int64
	if e.snap.DownloadID != nil {
		dl = *e.snap.DownloadID
	}
	var release any
	if e.snap.Release != nil {
		release = *e.snap.Release
	}
	kept := e.snap.keptFacts
	_, err := l.installLinks(ctx, &install{v: v, root: root, plan: plan, files: map[string]linkedFile{e.path: lf},
		now: store.Millis(l.now()), prefix: r.prefix,
		begin: func(ctx context.Context) error { return l.returnChecks(ctx, e, v) },
		stopped: func(ctx context.Context, made []PlannedLink, moved []trashed, cause error) error {
			l.rollbackReturn(v, made, moved)
			return cause
		},
		orphan: func(version, tp string) {
			if err := l.w.purgeTrash(version, tp); err != nil {
				l.log.Warn("library: return: a trash copy not taken back", "path", tp, "err", err)
			}
		},
		row: func(ctx context.Context, tx *sql.Tx, lk *PlannedLink) (linkRow, error) {
			row := linkRow{downloadID: dl, release: release, quality: &quality, score: &score, kept: &kept}
			if e.snap.ContentFileID != nil {
				var id int64
				err := tx.QueryRowContext(ctx, `SELECT id FROM content_file WHERE id = ?`, *e.snap.ContentFileID).Scan(&id)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return row, err
				}
				if err == nil {
					row.contentFileID = id
				}
			}
			return row, nil
		},
		linkTx: func(ctx context.Context, tx *sql.Tx, lk *PlannedLink) (string, error) {
			return r.prefix + "the earlier file is back", nil
		},
		endTx: func(ctx context.Context, tx *sql.Tx, res *installed) error {
			if len(res.failures) > 0 || len(res.linked) == 0 {
				return fmt.Errorf("%w: %s", errReturnFailed, strings.Join(res.failures, "; "))
			}
			if r.endTx != nil {
				return r.endTx(ctx, tx, v, store.Millis(l.now()))
			}
			return nil
		},
	})
	if err != nil {
		var rr *ReturnRefused
		switch {
		case errors.As(err, &rr):
		case errors.Is(err, ErrLinkChanged), errors.Is(err, errReturnFailed):
			l.log.Info("library: return not made", "path", e.path, "err", err)
			err = &ReturnRefused{Sentence: returnChanged, Passing: true}
		}
		return nil, err
	}
	l.log.Info("library: earlier file put back", "path", e.path, "version", e.version, "replaced", s.Path)
	// The torrents the library uses changed: Torrents and Sources are worked out again now.
	if l.recordChanged != nil {
		l.recordChanged()
	}
	return v, nil
}

func cmpOr(s, or string) string {
	if s == "" {
		return or
	}
	return s
}

// returnChecks are a return's checks under its leases: the earlier file reads (one piece through the mount), the
// recorded successor still holds the slot, the returning file holds every episode of the file it displaces, and no
// other library shares either path. Each refusal is one sentence.
func (l *Library) returnChecks(ctx context.Context, e *undoEntry, v *importVersion) error {
	db := l.st.Reader()
	s := e.snap.Successor
	if held, others, importing, err := e.slot(ctx, db); err != nil {
		return err
	} else if !held {
		return &ReturnRefused{Sentence: returnChanged, Passing: true}
	} else if others {
		return &ReturnRefused{Sentence: returnOthers}
	} else if importing {
		return &ReturnRefused{Sentence: returnImporting, Passing: true}
	}
	if cur, err := os.Readlink(s.Path); err != nil || cur != s.Target {
		return &ReturnRefused{Sentence: returnChanged, Passing: true}
	}
	if s.Path != e.path {
		if _, err := os.Lstat(e.path); !errors.Is(err, os.ErrNotExist) {
			return &ReturnRefused{Sentence: returnChanged, Passing: true}
		}
	}
	var shared bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM link_version WHERE version_id <> ? AND (link_id = ?
		OR link_id IN (SELECT id FROM link WHERE path = ?)))`, v.VersionID, s.LinkID, e.path).Scan(&shared); err != nil {
		return err
	}
	if shared {
		return &ReturnRefused{Sentence: returnShared}
	}
	var held []int64
	if err := scanRows(ctx, db, `SELECT episode_id FROM link_episode WHERE link_id = ? ORDER BY episode_id`,
		[]any{s.LinkID}, func(r *sql.Rows) error {
			var id int64
			err := r.Scan(&id)
			held = append(held, id)
			return err
		}); err != nil {
		return err
	}
	var extra []int64
	for _, id := range held {
		if !slices.Contains(e.snap.Episodes, id) {
			extra = append(extra, id)
		}
	}
	if len(extra) > 0 {
		words, err := episodeWords(ctx, db, extra, e.snap.Episodes)
		if err != nil {
			return err
		}
		return &ReturnRefused{Sentence: words}
	}
	if _, err := l.ReadPiece(ctx, e.target, 0, PlayingPiece, PlayingPieceLimit); err != nil {
		l.log.Info("library: return: the earlier file does not read", "target", e.target, "err", err)
		// No verdict, or no answer in time: not a "no".
		passing := errors.Is(err, ErrPieceNoVerdict) || errors.Is(err, errPieceTimedOut)
		return &ReturnRefused{Sentence: returnGone, Passing: passing}
	}
	return nil
}

// episodeWords is the refusal of a return whose file would leave episodes uncovered: "The current file also holds
// episode 4; the earlier file holds only episode 3" (S01E04 when the episodes span seasons).
func episodeWords(ctx context.Context, db *sql.DB, extra, own []int64) (string, error) {
	type ep struct{ season, number int }
	eps := map[int64]ep{}
	all := append(slices.Clone(extra), own...)
	for _, id := range all {
		var e ep
		if err := db.QueryRowContext(ctx, `SELECT season, number FROM episode WHERE id = ?`, id).Scan(&e.season,
			&e.number); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		eps[id] = e
	}
	seasons := map[int]bool{}
	for _, e := range eps {
		seasons[e.season] = true
	}
	list := func(ids []int64) string {
		var ws []string
		for _, id := range ids {
			e := eps[id]
			if len(seasons) > 1 {
				ws = append(ws, fmt.Sprintf("S%02dE%02d", e.season, e.number))
			} else {
				ws = append(ws, fmt.Sprint(e.number))
			}
		}
		w := "episode"
		if len(ids) > 1 {
			w = "episodes"
		}
		if len(seasons) > 1 {
			return strings.Join(ws, ", ")
		}
		return w + " " + strings.Join(ws, ", ")
	}
	if len(own) == 0 {
		return "The current file also holds " + list(extra) + "; the earlier file holds none", nil
	}
	return "The current file also holds " + list(extra) + "; the earlier file holds only " + list(own), nil
}

// rollbackReturn takes back a return's symlink changes: each link it made goes (a link made in place points at the
// file it replaced again), each file it moved aside is linked back, and the trash copies it made go. Nothing was
// recorded. A step that fails is logged; the others still run.
func (l *Library) rollbackReturn(v *importVersion, made []PlannedLink, moved []trashed) {
	inPlace := map[string]trashed{}
	for _, m := range moved {
		if m.path == m.linkPath {
			inPlace[m.path] = m
		}
	}
	warn := func(path string, err error) {
		if err != nil {
			l.log.Warn("library: return not taken back", "path", path, "err", err)
		}
	}
	for _, lk := range made {
		if m, ok := inPlace[lk.Path]; ok {
			warn(lk.Path, l.w.symlink(v.Key, m.target, lk.Path))
			continue
		}
		warn(lk.Path, l.w.removeLink(v.Key, lk.Path))
	}
	for _, m := range moved {
		if m.path != m.linkPath {
			warn(m.path, l.w.symlink(v.Key, m.target, m.path))
		}
		warn(m.trashPath, l.w.purgeTrash(v.Key, m.trashPath))
	}
}
