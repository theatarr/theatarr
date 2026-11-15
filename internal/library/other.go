package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// Other libraries at import (docs/design/router.md "Other libraries at import"): one download can fill more than one
// library of a title. After a download is imported for library A, each other library B of the title is tried once:
// it must fit (kind, anime flag, storage, the download's provider or client, its indexer), have the title monitored
// and not removed, its gate open, nothing in flight and not hold the hash already, nor have blocklisted it. B's own
// decision judges the release against B's current files; accepted, a download of B for the same torrent is written
// ready and imported at once through the ordinary import. Nothing is stored for a library that is skipped or
// rejects it, and nothing is retried: a B download whose import does not go through at once ends failed with its
// reason (Retry in Activity). B's import never touches A's.

// OtherSourcePrefix starts the source of a download linked from another library's import: "library:<A's key>".
const OtherSourcePrefix = "library:"

// linkOthers tries the title's other libraries with the files of download d, just imported. Errors are logged; A's
// import stands whatever happens here.
func (l *Library) linkOthers(ctx context.Context, d *importDownload) {
	hash := indexer.NormalizeHash(d.InfoHash)
	src := d.Version.Policy
	if l.engine == nil || !l.pol.RouterSettings().Enabled || hash == "" || src == nil ||
		strings.HasPrefix(d.Source, OtherSourcePrefix) { // linked from another library's import: that import tried them
		return
	}
	episodes := d.rescoped
	if episodes == nil {
		for _, e := range d.Version.Episodes {
			episodes = append(episodes, e.ID)
		}
	}
	db := l.st.Reader()
	ctx, err := decide.WithStoredSeaDex(ctx, db)
	var t decide.Title
	if err == nil {
		t, err = decide.LoadTitle(ctx, db, d.Version.Title.ID)
	}
	if err != nil {
		l.log.Warn("library: other libraries not tried", "download", d.ID, "err", err)
		return
	}
	rel := router.OtherRelease{Indexer: d.Indexer, Title: d.ReleaseTitle, Hash: hash, Size: d.Size, Episodes: episodes}
	for _, key := range slices.Sorted(maps.Keys(l.pol.Versions)) {
		vp := l.pol.Versions[key]
		if key == d.Version.Key || !otherFits(src, vp, d) {
			continue
		}
		id, why, err := l.tryOther(ctx, d, t, key, vp, rel)
		switch {
		case err != nil:
			l.log.Warn("library: other library not tried", "download", d.ID, "library", key, "err", err)
		case id != 0:
			l.log.Info("library: linked into another library", "download", d.ID, "library", key, "as", id)
		default:
			l.log.Debug("library: other library skipped", "download", d.ID, "library", key, "why", why)
		}
	}
}

// otherFits reports whether library vp could hold the files of d (from library src): the same kind, the same anime
// flag when both set one, the same storage, the download's provider (debrid) or client listed, and its indexer when
// vp lists indexers.
func otherFits(src, vp *policy.VersionPolicy, d *importDownload) bool {
	if vp.Kind != src.Kind || vp.Storage != src.Storage {
		return false
	}
	if src.Anime != nil && vp.Anime != nil && *src.Anime != *vp.Anime {
		return false
	}
	switch {
	case d.Client != "":
		if vp.Client != d.Client {
			return false
		}
	case d.Provider != "":
		if !slices.Contains(vp.Providers, d.Provider) {
			return false
		}
	default:
		return false
	}
	return len(vp.Indexers) == 0 || slices.Contains(vp.Indexers, d.Indexer)
}

// tryOther tries one other library: the id of the download it imported there, or why it did not.
func (l *Library) tryOther(ctx context.Context, d *importDownload, t decide.Title, key string, vp *policy.VersionPolicy,
	rel router.OtherRelease) (int64, string, error) {
	if err := l.gate.CheckVersion(key); err != nil {
		return 0, err.Error(), nil
	}
	db := l.st.Reader()
	titleID := d.Version.Title.ID
	var versionID int64
	var monitored, removed bool
	err := db.QueryRowContext(ctx, `SELECT v.id, v.monitored, EXISTS (SELECT 1 FROM arrview_removed_version r
		WHERE r.version_id = v.id) FROM version v WHERE v.title_id = ? AND v.policy_key = ?`, titleID, key).Scan(
		&versionID, &monitored, &removed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, "the title is not in this library", nil
	case err != nil:
		return 0, "", err
	case !monitored:
		return 0, "not monitored", nil
	case removed:
		return 0, "removed from this library", nil
	}
	if held, err := router.HoldsHash(ctx, db, versionID, rel.Hash); err != nil || held {
		return 0, "holds this release already", err
	}
	if blocked, err := router.Blocklisted(ctx, db, key, rel.Hash, rel.Title); err != nil || blocked {
		return 0, "blocklisted", err
	}
	v := decide.Version{ID: versionID, Key: key, Policy: vp}
	var existing *decide.ExistingFile
	if t.Kind == policy.KindSeries {
		if err := decide.LoadSeriesVersion(ctx, db, &t, v.ID); err != nil {
			return 0, "", err
		}
	} else if existing, err = decide.LoadExisting(ctx, db, v.ID); err != nil {
		return 0, "", err
	}
	dctx := decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, l.pol, vp), titleID)
	c, why, err := router.DecideOther(dctx, l.pol, l.engine, rel, t, v, existing, l.now())
	if err != nil || c == nil {
		return 0, why, err
	}
	// What B's own route would check before an add, with the route's functions: monitoring, minimum availability,
	// B's upgrade setting and the improvement over its files, and the queue checks.
	c, why, err = router.ScreenOther(dctx, l.st, l.pol, l.engine.Profile, l.now, router.Request{TitleID: titleID,
		VersionID: versionID, Version: key, Source: OtherSourcePrefix + d.Version.Key, Candidates: []router.Candidate{*c}})
	if err != nil || c == nil {
		return 0, why, err
	}
	id, err := l.insertOther(ctx, d, key, versionID, c)
	if err != nil {
		return 0, "", err
	}
	plan, err := l.Import(ctx, id, false)
	if err == nil && plan != nil && !plan.Waiting && !plan.DryRun {
		var state string
		if err := l.st.Reader().QueryRowContext(ctx, `SELECT state FROM download WHERE id = ?`, id).Scan(&state); err != nil {
			return id, "", err
		}
		if state == "imported" {
			return id, "", nil
		}
	}
	// Tried once: anything but a finished import ends the download failed, with its reason.
	why = "it did not import at once"
	switch {
	case errors.Is(err, ErrClaimed):
		return id, "", nil // another run is importing it
	case err != nil:
		why = err.Error()
	case plan != nil && plan.Waiting:
		why = "its files were not ready on the mount"
	}
	msg := fmt.Sprintf("library: linking from %s's import: %s", d.Version.Key, why)
	now := store.Millis(l.now())
	if ferr := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE download SET state = 'failed', error = ?, updated_at = ?
			WHERE id = ? AND state IN ('ready', 'importing')`, msg, now, id); err != nil {
			return err
		}
		store.HistoryFailure(ctx, tx, id)
		return nil
	}); ferr != nil {
		return id, "", ferr
	}
	return 0, msg, nil
}

// insertOther writes the download of library key for d's torrent, ready to import: the same provider entry (reused,
// never our add) with key's candidate (its decision, episodes narrowed by the route's checks).
func (l *Library) insertOther(ctx context.Context, d *importDownload, key string, versionID int64,
	c *router.Candidate) (int64, error) {
	eps, _ := json.Marshal(c.Episodes)
	if c.Episodes == nil {
		eps = []byte("[]")
	}
	var formats, epMap any
	if c.Formats != nil {
		b, _ := json.Marshal(c.Formats)
		formats = string(b)
	}
	if c.EpisodeMap != nil {
		b, _ := json.Marshal(c.EpisodeMap)
		epMap = string(b)
	}
	now := store.Millis(l.now())
	var id int64
	err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO download (title_id, version_id, version_key, info_hash,
		release_title, indexer, quality, score, size_bytes, episodes, formats, episode_map, provider, provider_torrent_id,
		provider_status, name, original_name, client, source_path, provider_epoch, cached, files_selected, progress,
		reused, own_add, state, source, policy_rev, release_data, created_at, updated_at, added_at, ready_at)
		SELECT title_id, ?, ?, info_hash, release_title, indexer, ?, ?, size_bytes, ?, ?, ?, provider, provider_torrent_id,
		provider_status, name, original_name, client, source_path, provider_epoch, cached, files_selected, 100,
		1, 0, 'ready', ?, ?, release_data, ?, ?, ?, ? FROM download WHERE id = ?`,
			versionID, key, c.Quality, c.Score, string(eps), formats, epMap, OtherSourcePrefix+d.Version.Key,
			l.pol.Revision, now, now, now, now, d.ID)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		// Its History row, as a manual import's: the torrent it holds was already on the provider (reused).
		store.HistoryGrab(ctx, tx, id)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("library: download for %s: %w", key, err)
	}
	return id, nil
}
