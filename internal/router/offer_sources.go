package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/store"
)

// adoptedSource is what the arr history gave for an adopted link (link.adopted_source, 00114), read once and kept so
// the link keeps qualifying for offers after the arr history is gone. From is the link.adopted_from it was read for:
// a refresh to another arr file makes it stale, and adoption clears it when it records a changed file.
type adoptedSource struct {
	From     string   `json:"from"`
	Hash     string   `json:"hash"`
	Release  string   `json:"release"`
	Indexers []string `json:"indexers"` // the arr's indexer names as read, "" for a grab without one
	// Version is the library the owning arr filled when this was read: it gets no offer of the file, also after the
	// arr instance is gone.
	Version string `json:"version,omitempty"`
}

// adoptedRecord is an adopted link's source to store (offer sweep, not a dry run).
type adoptedRecord struct {
	linkID int64
	from   string
	source string
}

// fileSources reads file identities, not release-name guesses. Adoption deliberately has no download or content
// row: its frozen file metadata and link.adopted_from retain the hash and the owning arr respectively. It also
// returns the adopted links whose source it read from the arr history and that should be stored.
func (o *Offerer) fileSources(ctx context.Context) ([]offerSource, []adoptedRecord, error) {
	rows, err := o.Store.Reader().QueryContext(ctx, `SELECT * FROM (
		SELECT 'link' AS source_kind, l.id, l.title_id, v.policy_key, t.kind,
			COALESCE(NULLIF(c.info_hash, ''), json_extract(l.file_metadata, '$.InfoHash'), ''),
			COALESCE(NULLIF(l.release_title, ''), json_extract(l.file_metadata, '$.SceneName'), ''),
			COALESCE(l.quality, ''), COALESCE(cf.size_bytes, json_extract(l.file_metadata, '$.Size'), 0),
			(SELECT json_group_array(episode_id) FROM link_episode WHERE link_id = l.id), l.path, l.target,
			l.state = 'dead' OR EXISTS (SELECT 1 FROM library_dangling WHERE path = l.path),
			COALESCE('adopted from ' || l.adopted_from, ''), '', '', 0, l.adopted_source
		FROM link l JOIN link_version lv ON lv.link_id = l.id JOIN version v ON v.id = lv.version_id
		JOIN title t ON t.id = l.title_id LEFT JOIN content_file cf ON cf.id = l.content_file_id
		LEFT JOIN content c ON c.id = cf.content_id
		WHERE l.download_id IS NULL AND l.state IN ('active', 'dead')
		UNION ALL
		SELECT 'arr_file', vf.id, vf.title_id, v.policy_key, t.kind, COALESCE(c.info_hash, ''),
			COALESCE(NULLIF(vf.release_title, ''), vf.scene_name), vf.quality, vf.size_bytes,
			(SELECT json_group_array(episode_id) FROM version_file_episode WHERE version_file_id = vf.id),
			vf.path, '', EXISTS (SELECT 1 FROM library_dangling WHERE path = vf.path), '',
			vf.instance, COALESCE(vf.download_id, ''), vf.arr_file_id, NULL
		FROM version_file vf JOIN version v ON v.id = vf.version_id JOIN title t ON t.id = vf.title_id
		LEFT JOIN content c ON c.id = vf.content_id
		WHERE NOT EXISTS (SELECT 1 FROM library_action a JOIN link l ON l.path = a.path
			JOIN link_version lv ON lv.link_id = l.id WHERE a.path = vf.path AND a.version_key = v.policy_key
			AND a.kind = 'replace' AND a.mode = 'applied' AND lv.version_id = vf.version_id
			AND l.download_id IS NOT NULL AND l.state IN ('active', 'dead'))
	) ORDER BY source_kind DESC, id`)
	if err != nil {
		return nil, nil, err
	}
	type fileSource struct {
		offerSource
		detail, instance, download string
		arrID                      int64
		stored                     sql.NullString
	}
	var files []fileSource
	for rows.Next() {
		var f fileSource
		var episodes string
		if err := rows.Scan(&f.sourceKind, &f.id, &f.titleID, &f.version, &f.kind, &f.hash, &f.release,
			&f.quality, &f.size, &episodes, &f.path, &f.target, &f.unreadable, &f.detail, &f.instance,
			&f.download, &f.arrID, &f.stored); err != nil {
			rows.Close()
			return nil, nil, err
		}
		_ = json.Unmarshal([]byte(episodes), &f.episodes)
		files = append(files, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	var out []offerSource
	var record []adoptedRecord
	for _, f := range files {
		vp := o.Policy.Versions[f.version]
		if vp == nil {
			continue
		}
		f.hash = indexer.NormalizeHash(f.hash)
		f.arrVersion = f.version
		arrPath := f.path
		adoptedFrom := ""
		if f.sourceKind == "link" {
			if f.detail == "" {
				continue
			}
			f.arrVersion = "" // the arr's library, from the instance or the stored source below
			arrPath = ""
			if detail, ok := strings.CutPrefix(f.detail, "adopted from "); ok {
				adoptedFrom = detail
				f.instance, arrPath, _ = strings.Cut(detail, " ")
				arrPath, _, _ = strings.Cut(arrPath, " (refresh: replaces ")
				if o.Policy.Import != nil && o.Policy.Import.Arr[f.instance] != nil {
					f.arrVersion = o.Policy.Import.Arr[f.instance].Version
				} else if err := o.Store.Reader().QueryRowContext(ctx,
					`SELECT policy_key FROM arr_instance WHERE name = ?`, f.instance).Scan(&f.arrVersion); err != nil && err != sql.ErrNoRows {
					return nil, nil, err
				}
			}
		}
		var stored adoptedSource
		if adoptedFrom != "" && f.stored.Valid && json.Unmarshal([]byte(f.stored.String), &stored) == nil &&
			stored.From == adoptedFrom {
			// Read before: the stored source stands in for the mount folder and the arr history.
			if f.hash == "" {
				f.hash = indexer.NormalizeHash(stored.Hash)
			}
			if f.release == "" {
				f.release = stored.Release
			}
			f.indexer = o.indexerOf(stored.Indexers)
			if f.arrVersion == "" {
				f.arrVersion = stored.Version // the arr instance can no longer be looked up
			}
			out = append(out, f.offerSource)
			continue
		}
		if f.hash == "" {
			target := f.target
			if target == "" {
				target, _ = os.Readlink(f.path)
				if target != "" && !filepath.IsAbs(target) {
					target = filepath.Join(filepath.Dir(f.path), target)
				}
			}
			f.hash, err = o.folderHash(ctx, target)
			if err != nil {
				return nil, nil, err
			}
		}
		// Only history tied to this file, download or known hash supplies provenance. Unknown indexers still
		// fail closed in offerTo.
		names, err := o.fileHistory(ctx, &f.offerSource, f.instance, f.download, f.arrID, arrPath)
		if err != nil {
			return nil, nil, err
		}
		if adoptedFrom != "" {
			if names == nil {
				names = []string{}
			}
			raw, err := json.Marshal(adoptedSource{From: adoptedFrom, Hash: f.hash, Release: f.release, Indexers: names,
				Version: f.arrVersion})
			if err != nil {
				return nil, nil, err
			}
			record = append(record, adoptedRecord{linkID: f.id, from: adoptedFrom, source: string(raw)})
		}
		out = append(out, f.offerSource)
	}
	return out, record, nil
}

// indexerOf resolves stored arr indexer names as fileHistory does: any unknown name (or a grab without one) leaves
// no indexer, else the first.
func (o *Offerer) indexerOf(names []string) string {
	first := ""
	for _, name := range names {
		key := o.Policy.IndexerByName(name)
		if key == "" {
			return ""
		}
		if first == "" {
			first = key
		}
	}
	return first
}

// recordAdoptedSources stores what fileSources read from the arr history for adopted links, for the link it was
// read for only.
func (o *Offerer) recordAdoptedSources(ctx context.Context, record []adoptedRecord) error {
	if len(record) == 0 {
		return nil
	}
	return o.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, r := range record {
			if _, err := tx.ExecContext(ctx, `UPDATE link SET adopted_source = ? WHERE id = ? AND adopted_from = ?`,
				r.source, r.linkID, r.from); err != nil {
				return err
			}
		}
		return nil
	})
}

// Several episode files can belong to one torrent. Decide once with all its retained episodes, rather than
// letting the first file consume the target/title/hash memory and hide the rest of a season.
func groupOfferFiles(files []offerSource) []offerSource {
	var out []offerSource
	groups := map[string]int{}
	for _, f := range files {
		key := fmt.Sprintf("%s/%s/%d/%s", f.version, f.arrVersion, f.titleID, f.hash)
		if i, ok := groups[key]; ok && f.hash != "" {
			s := &out[i]
			s.episodes = append(s.episodes, f.episodes...)
			slices.Sort(s.episodes)
			s.episodes = slices.Compact(s.episodes)
			s.size += f.size
			s.unreadable = s.unreadable || f.unreadable
			if s.indexer != f.indexer {
				s.indexer = "" // inconsistent provenance fails closed
			}
		} else {
			groups[key] = len(out)
			out = append(out, f)
		}
	}
	return out
}

func (o *Offerer) folderHash(ctx context.Context, target string) (string, error) {
	settings := o.Policy.LibrarySettings()
	if settings.Decypharr != nil {
		// decypharr serves same-named torrents from one folder: its name alone names no torrent.
		return "", nil
	}
	for _, root := range []string{settings.LinkPath, settings.MountPath} {
		if root == "" || target == "" {
			continue
		}
		rel, err := filepath.Rel(filepath.Join(root, settings.AllDir), target)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		folder, _, _ := strings.Cut(rel, string(filepath.Separator))
		var hash sql.NullString
		var count int
		err = o.Store.Reader().QueryRowContext(ctx, `SELECT MIN(lower(info_hash)), COUNT(DISTINCT lower(info_hash))
			FROM content WHERE mount_folder = ?`, folder).Scan(&hash, &count)
		if err != nil {
			return "", err
		}
		if count == 1 {
			return indexer.NormalizeHash(hash.String), nil
		}
	}
	return "", nil
}

// fileHistory fills the source's hash, release and indexer from the arr history, and returns the arr indexer names it
// resolved.
func (o *Offerer) fileHistory(ctx context.Context, s *offerSource, instance, download string, arrID int64, arrPath string) ([]string, error) {
	rows, err := o.Store.Reader().QueryContext(ctx, `WITH history AS (
		SELECT h.* FROM arr_history h JOIN version v ON v.id = h.version_id
		WHERE h.title_id = ? AND (? = '' OR h.instance = ?)
		AND (v.policy_key = ? OR v.policy_key = ?)
		AND h.event_type IN ('grabbed', 'downloadFolderImported')
	), imports AS (
		SELECT * FROM history WHERE event_type = 'downloadFolderImported' AND (
			(? <> 0 AND CAST(json_extract(data, '$.fileId') AS INTEGER) = ?)
			OR (? <> '' AND json_extract(data, '$.importedPath') = ?))
	)
	SELECT COALESCE(h.info_hash, ''), h.source_title, h.data, h.event_type
		FROM history h WHERE (
			(? <> '' AND h.download_id = ?) OR (? <> '' AND lower(h.info_hash) = ?)
			OR EXISTS (SELECT 1 FROM imports i WHERE i.instance = h.instance AND i.arr_id = h.arr_id)
			OR EXISTS (SELECT 1 FROM imports i WHERE i.download_id <> '' AND i.download_id = h.download_id
				AND i.instance = h.instance))
		ORDER BY h.event_type = 'grabbed' DESC, h.date_at DESC, h.arr_id DESC`,
		s.titleID, instance, instance, s.version, s.arrVersion, arrID, arrID, arrPath, arrPath,
		download, download, s.hash, s.hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	knownHash := s.hash
	ambiguous, unsafe := false, false
	var names []string
	for rows.Next() {
		var hash, release, data, event string
		if err := rows.Scan(&hash, &release, &data, &event); err != nil {
			return nil, err
		}
		hash = indexer.NormalizeHash(hash)
		if knownHash != "" && hash != "" && knownHash != hash {
			continue // history of a different file at a reused arr path
		}
		if hash != "" {
			if s.hash != "" && s.hash != hash {
				ambiguous = true
			} else {
				s.hash = hash
			}
		}
		if s.release == "" {
			s.release = release
		}
		var fields map[string]string
		_ = json.Unmarshal([]byte(data), &fields)
		if name := fields["indexer"]; name != "" || event == "grabbed" {
			// The arr's name for the indexer ("TheRARBG (Prowlarr)"): the policy indexer of that name
			// (IndexerByName). An unknown one leaves the source without an indexer, and the offer is declined.
			names = append(names, name)
			key := o.Policy.IndexerByName(name)
			if key == "" {
				unsafe = true
			}
			if s.indexer == "" {
				s.indexer = key
			}
		}
	}
	if ambiguous {
		s.hash = "" // conflicting history does not identify the current file
	}
	if unsafe {
		s.indexer = ""
	}
	return names, rows.Err()
}

// Applied replacement records survive in-place upgrades, which reuse the old link id and path. The replacing
// download owns the replace action; the original download owns the earlier link action.
func (o *Offerer) downloadSuperseded(ctx context.Context, id int64, version string) (bool, error) {
	var superseded bool
	err := o.Store.Reader().QueryRowContext(ctx, `SELECT
		NOT EXISTS (SELECT 1 FROM link l JOIN link_version lv ON lv.link_id = l.id
			JOIN version v ON v.id = lv.version_id WHERE l.download_id = ? AND v.policy_key = ?
			AND l.state IN ('active', 'dead')) AND (
		EXISTS (SELECT 1 FROM link WHERE download_id = ? AND state = 'unlinked') OR
		EXISTS (SELECT 1 FROM library_action a JOIN library_action r ON r.path = a.path
			AND r.version_key = a.version_key AND (r.target = a.target OR r.target IS NULL)
			WHERE a.download_id = ? AND a.version_key = ? AND a.kind = 'link' AND a.mode = 'applied'
			AND r.kind = 'replace' AND r.mode = 'applied'))`, id, version, id, id, version).Scan(&superseded)
	return superseded, err
}

func (o *Offerer) cancelSource(ctx context.Context, s *offerSource, dryRun bool) ([]OfferResult, error) {
	rows, err := o.Store.Reader().QueryContext(ctx, `SELECT version_key FROM router_offer WHERE title_id = ?
		AND info_hash = ? AND from_version = ? AND outcome IN ('pending', 'gave_up')`, s.titleID, s.hash, s.version)
	if err != nil {
		return nil, err
	}
	var out []OfferResult
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return nil, err
		}
		identity := fmt.Sprintf("%s/%d/%s", version, s.titleID, s.hash)
		if o.seen[identity] {
			continue
		}
		if o.seen != nil {
			o.seen[identity] = true
		}
		out = append(out, OfferResult{Version: version, Release: s.release, Outcome: OfferDeclined, Reasons: []string{"superseded"}})
	}
	err = rows.Err()
	rows.Close()
	if err != nil || dryRun || len(out) == 0 {
		return out, err
	}
	err = o.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE router_offer SET outcome = 'declined', reasons = '["superseded"]',
			next_at = NULL, updated_at = ? WHERE title_id = ? AND info_hash = ? AND from_version = ?
			AND outcome IN ('pending', 'gave_up')`, store.Millis(o.now()), s.titleID, s.hash, s.version)
		return err
	})
	return out, err
}

func (o *Offerer) cancelSuperseded(ctx context.Context, dryRun bool) ([]OfferResult, error) {
	files, _, err := o.fileSources(ctx)
	if err != nil {
		return nil, err
	}
	key := func(kind string, id int64, version string) string { return fmt.Sprintf("%s/%d/%s", kind, id, version) }
	live := map[string]string{}
	for _, f := range files {
		live[key(f.sourceKind, f.id, f.version)] = f.hash
	}
	rows, err := o.Store.Reader().QueryContext(ctx, `SELECT DISTINCT source_kind, source_id, title_id, from_version,
		info_hash, release_title FROM router_offer WHERE outcome IN ('pending', 'gave_up')`)
	if err != nil {
		return nil, err
	}
	var sources []offerSource
	for rows.Next() {
		var s offerSource
		if err := rows.Scan(&s.sourceKind, &s.id, &s.titleID, &s.version, &s.hash, &s.release); err != nil {
			rows.Close()
			return nil, err
		}
		sources = append(sources, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var out []OfferResult
	for _, s := range sources {
		superseded := false
		if s.sourceKind == "download" {
			superseded, err = o.downloadSuperseded(ctx, s.id, s.version)
		} else {
			hash, exists := live[key(s.sourceKind, s.id, s.version)]
			superseded = !exists || hash != s.hash
		}
		if err != nil {
			return out, err
		}
		if superseded {
			res, err := o.cancelSource(ctx, &s, dryRun)
			out = append(out, res...)
			if err != nil {
				return out, err
			}
		}
	}
	return out, nil
}
