package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/outcome"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// The put-back (docs/design/now-playing.md "A file that stops working while it plays"): a copy that broke while
// someone played it, replaced since and read again on two sweeps, comes back in place of its replacement through the
// same return as Undo (return.go), when it is a strict win under today's profile; else it is kept aside as before.

// PlayingNow answers whether a Plex session may be playing a replacement (its link, target and title): playing it, a
// session of its title whose file is not identified, or a video session nobody could place; answered false while
// Plex has not answered lately.
type PlayingNow func(linkID int64, target string, titleID int64) (may, answered bool)

// SetPlayingNow sets the answer the put-back asks before it displaces a file (nil: Plex never answers, so nothing is
// put back).
func (l *Library) SetPlayingNow(f PlayingNow) { l.playingNow = f }

// heldNow is the file of a version that holds a returned copy's film or episodes now.
type heldNow struct {
	id                                int64
	path, target, meta, release, qual string
	probe, media                      sql.NullString
	adopted                           bool
	download                          sql.NullInt64
}

// putBackPrefix starts the detail of a put-back's actions.
const putBackPrefix = store.HistoryPutBackPrefix + ": "

// putBackOrAside settles a replaced copy that read again: put back (putBackCopy), left for a later sweep, or kept aside
// exactly as before.
func (l *Library) putBackOrAside(ctx context.Context, r returned, target, newFolder string) error {
	aside, err := l.putBackCopy(ctx, r, target, newFolder)
	if err != nil || !aside {
		return err
	}
	return l.settleAsideNow(ctx, r, asideWords)
}

// putBackCopy puts the row's copy back in place of the one file of its version that holds its film or episodes now,
// when all hold: the library does not prefer SeaDex; the old file wins strictly under today's profile
// (ImportUpgrade accepts it over the current file and refuses the current file over it; a tie, an unknown quality
// or any error keeps it aside); today's profile still allows its quality and minimum score; the wrong-audio rule
// would not refuse it over the current file; no earlier incident put the same copy back in this library. It waits (wait, nothing settled) while Pause is on or the library does
// not write, while the incident's search is open, and while the replacement is playing or Plex does not answer.
// The facts are the row's own; the return's checks still apply (a refusal keeps it aside). aside: it is kept aside
// (settleAside); else it was put back or waits.
//
// newFolder is the renamed mount folder the copy is read in now ("" for its own): the return links it there and the
// release's mount folder follows, as a relink's repoint does.
func (l *Library) putBackCopy(ctx context.Context, r returned, target, newFolder string) (aside bool, err error) {
	if !l.writableNow(r.version) {
		return false, nil
	}
	db := l.st.Reader()
	// The incident's search still open: a later sweep, put back or aside alike (settleAsideNow does not ask again).
	if open, err := l.searchOpen(ctx, r); err != nil || open {
		return false, err
	}
	vp := l.pol.Versions[r.version]
	if vp == nil || vp.PreferSeaDex || l.engine == nil {
		return true, nil
	}
	// Once: a copy an earlier incident already put back in this library (its file, by content file or target) that
	// broke again stays aside.
	var before bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM playing_break pb JOIN playing_break e
		ON e.id <> pb.id AND e.title_id = pb.title_id AND e.version_key = pb.version_key AND e.settled = 'restored'
		AND (e.content_file_id = pb.content_file_id OR e.old_target = pb.old_target) WHERE pb.id = ?)`, r.id).Scan(&before); err != nil {
		return false, err
	}
	if before {
		return true, nil
	}
	// The row's copy as it was.
	e := &undoEntry{version: r.version, path: r.path, target: target}
	s := &e.snap
	var kind, eps, meta string
	var versionID int64
	var oldProbe sql.NullString // the copy's saved probe, for the wrong-audio rule
	if err := db.QueryRowContext(ctx, `SELECT t.kind, v.id, pb.content_file_id, pb.download_id, pb.release_title,
		pb.quality, pb.score, pb.file_metadata, pb.adopted, pb.adopted_from, pb.adopted_source, pb.adopted_media,
		pb.folder_hash, pb.episodes, cf.probe FROM playing_break pb JOIN title t ON t.id = pb.title_id
		JOIN version v ON v.title_id = pb.title_id AND v.policy_key = pb.version_key
		LEFT JOIN content_file cf ON cf.id = pb.content_file_id WHERE pb.id = ?`, r.id).Scan(&kind,
		&versionID, &s.ContentFileID, &s.DownloadID, &s.Release, &s.Quality, &s.Score, &meta, &s.Adopted,
		&s.AdoptedFrom, &s.AdoptedSource, &s.AdoptedMedia, &s.FolderHash, &eps, &oldProbe); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return true, nil
		}
		return false, err
	}
	s.FileMetadata, s.TitleID = &meta, r.titleID
	s.Episodes = []int64{}
	if err := json.Unmarshal([]byte(eps), &s.Episodes); err != nil {
		return true, nil
	}
	// The one file of the version that holds its film or some of its episodes now.
	var succ []heldNow
	if err := scanRows(ctx, db, `SELECT l.id, l.path, l.target, COALESCE(l.file_metadata, '{}'),
		COALESCE(l.release_title, ''), COALESCE(l.quality, ''), cf.probe, l.adopted_media, l.download_id IS NULL, l.download_id
		FROM link l JOIN link_version lv ON lv.link_id = l.id AND lv.version_id = ?
		LEFT JOIN content_file cf ON cf.id = l.content_file_id
		WHERE l.state = 'active' AND l.title_id = ? AND (? <> 'series' OR EXISTS (SELECT 1 FROM link_episode le
			WHERE le.link_id = l.id AND le.episode_id IN (SELECT value FROM json_each(?))))`,
		[]any{versionID, r.titleID, kind, eps}, func(rs *sql.Rows) error {
			var x heldNow
			err := rs.Scan(&x.id, &x.path, &x.target, &x.meta, &x.release, &x.qual, &x.probe, &x.media, &x.adopted,
				&x.download)
			succ = append(succ, x)
			return err
		}); err != nil {
		return false, err
	}
	if len(succ) != 1 {
		return true, nil
	}
	cur := succ[0]
	s.Successor = successor{LinkID: cur.id, Path: cur.path, Target: cur.target, DownloadID: cur.download.Int64}
	// A strict win under today's profile.
	v := &importVersion{VersionID: versionID, Key: r.version, Policy: vp, Title: titleInfo{ID: r.titleID, Kind: kind}}
	t, err := l.cutoffTitle(ctx, r.titleID, versionID, vp)
	if err != nil {
		return true, nil
	}
	oldF, err := decide.LinkFileInputs([]byte(meta), kind, deref(s.Release), deref(s.Quality))
	if err != nil {
		return true, nil
	}
	curF, err := decide.LinkFileInputs([]byte(cur.meta), kind, cur.release, cur.qual)
	if err != nil {
		return true, nil
	}
	// An unknown quality on either side (none, or the ranked Unknown, id 0) decides nothing: aside.
	if !knownQuality(kind, oldF.Quality) || !knownQuality(kind, curF.Quality) {
		return true, nil
	}
	dv := decide.Version{ID: versionID, Key: r.version, Policy: vp}
	if ok, _, err := l.engine.ImportUpgrade(t, dv, &oldF, &curF); err != nil || !ok {
		return true, nil
	}
	if ok, _, err := l.engine.ImportUpgrade(t, dv, &curF, &oldF); err != nil || ok {
		return true, nil
	}
	if !l.profileAllows(t, dv, kind, oldF) {
		return true, nil
	}
	// The wrong-audio rule, the old file over the current one, as an import judges it (audio.go): the returning
	// copy's saved audio (savedAudio: a probe with the tag fact read as a fresh one, else the arr's media info or the
	// old probe), the held file's stored audio. When the rule applies and the copy's audio gives no verdict, aside.
	want, err := l.audioWanted(ctx, &importDownload{Version: *v})
	if err != nil {
		return false, err
	}
	if want != nil {
		langs, ok := savedAudio(oldProbe, nullString(s.AdoptedMedia), s.DownloadID == nil)
		if !ok || !sharesLanguage(langs, want) && !sharesLanguage(langs, storedAudio(cur.probe, cur.media, cur.adopted)) {
			return true, nil
		}
	}
	// Nobody plays the file it would displace, and Plex says so.
	if l.playingNow == nil {
		return false, nil
	}
	if may, answered := l.playingNow(cur.id, cur.target, r.titleID); !answered || may {
		if may {
			l.log.Info("library: put-back waits: its replacement may be playing", "link", r.path, "replacement", cur.path)
		}
		return false, nil
	}
	reported := false
	rf := &returnFile{entry: e, prefix: putBackPrefix}
	rf.endTx = func(ctx context.Context, tx *sql.Tx, _ *importVersion, now int64) error {
		reported = false
		if err := settleReturnTx(ctx, tx, r.id, r.hash, "restored", now); err != nil {
			return err
		}
		if newFolder != "" && r.hash != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE content SET mount_folder = ? WHERE LOWER(info_hash) = ?`, newFolder,
				r.hash); err != nil {
				return err
			}
		}
		// Its own outcome, once: ReportPlaying would send nothing after the import's "replaced".
		res, err := tx.ExecContext(ctx, `UPDATE playing_break SET reported = ?, updated_at = ? WHERE id = ? AND watched = 1
			AND reported <> ?`, outcome.Restored, now, r.id, outcome.Restored)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		reported = n > 0
		return nil
	}
	if _, err := l.returnOne(ctx, rf); err != nil {
		var rr *ReturnRefused
		if errors.As(err, &rr) && rr.Passing {
			return false, nil // a refusal that may not hold: a later sweep
		}
		if errors.As(err, &rr) {
			l.log.Info("library: the copy that broke while playing is not put back", "link", r.path, "why", rr.Sentence)
			return true, nil
		}
		return false, err
	}
	if reported {
		if o, err := l.playingOutcome(ctx, r.id, outcome.Restored); err != nil {
			l.log.Warn("library: outcome of a file put back not sent", "incident", r.id, "err", err)
		} else if l.onPlaying != nil {
			l.onPlaying(o)
		}
	}
	return false, nil
}

// profileAllows says whether today's profile of the version still allows the file's quality and its format score
// reaches the profile's minimum.
func (l *Library) profileAllows(t *decide.Title, v decide.Version, kind string, f decide.ExistingFile) bool {
	fl := format.Radarr
	if policy.Kind(kind) == policy.KindSeries {
		fl = format.Sonarr
	}
	p := l.engine.Profile(v.Policy.Profile, fl)
	if p == nil || p.Profile == nil {
		return false
	}
	qf, ok := profile.QualityByName(fl, f.Quality)
	if !ok || !slices.ContainsFunc(p.Profile.AllowedQualities(), func(q profile.Quality) bool { return q.ID == qf.ID }) {
		return false
	}
	x := f
	if err := l.engine.ScoreFile(t, v, &x); err != nil || x.Score == nil {
		return false
	}
	pr := l.pol.Profiles[v.Policy.Profile]
	return pr == nil || *x.Score >= pr.MinScore
}

// nullString is p as a nullable column (nil: NULL).
func nullString(p *string) sql.NullString {
	if p == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *p, Valid: true}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// knownQuality says whether q is a quality of the title kind other than Unknown (id 0).
func knownQuality(kind, q string) bool {
	fl := format.Radarr
	if policy.Kind(kind) == policy.KindSeries {
		fl = format.Sonarr
	}
	x, ok := profile.QualityByName(fl, q)
	return ok && x.ID != 0
}
