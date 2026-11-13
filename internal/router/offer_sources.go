package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/store"
)

// adoptedSource is what the arr history gave for an adopted link (link.adopted_source, 00114), stored so the link
// keeps qualifying for offers after the arr history is gone. From is the link.adopted_from it was read for: a refresh
// to another arr file makes it stale, and adoption clears it when it records a changed file.
type adoptedSource struct {
	From     string   `json:"from"`
	Hash     string   `json:"hash"`
	Release  string   `json:"release"`
	Indexers []string `json:"indexers"` // the arr's indexer names as read, "" for a grab without one
	// Version is the library the owning arr filled when this was read: it gets no offer of the file.
	Version string `json:"version,omitempty"`
}

// fileSources reads file identities, not release-name guesses: adopted links (no download of ours), offered from
// the source stored on the link (link.adopted_source) for the arr file it was adopted from. An adopted link without
// one is not offered.
func (o *Offerer) fileSources(ctx context.Context) ([]offerSource, error) {
	rows, err := o.Store.Reader().QueryContext(ctx, `SELECT 'link', l.id, l.title_id, v.policy_key, t.kind,
			COALESCE(NULLIF(c.info_hash, ''), json_extract(l.file_metadata, '$.InfoHash'), ''),
			COALESCE(NULLIF(l.release_title, ''), json_extract(l.file_metadata, '$.SceneName'), ''),
			COALESCE(l.quality, ''), COALESCE(cf.size_bytes, json_extract(l.file_metadata, '$.Size'), 0),
			(SELECT json_group_array(episode_id) FROM link_episode WHERE link_id = l.id), l.path, l.target,
			l.state = 'dead' OR EXISTS (SELECT 1 FROM library_dangling WHERE path = l.path),
			l.adopted_from, l.adopted_source
		FROM link l JOIN link_version lv ON lv.link_id = l.id JOIN version v ON v.id = lv.version_id
		JOIN title t ON t.id = l.title_id LEFT JOIN content_file cf ON cf.id = l.content_file_id
		LEFT JOIN content c ON c.id = cf.content_id
		WHERE l.download_id IS NULL AND l.state IN ('active', 'dead') AND l.adopted_from IS NOT NULL
		AND l.adopted_source IS NOT NULL ORDER BY l.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []offerSource
	for rows.Next() {
		var f offerSource
		var episodes, from, raw string
		if err := rows.Scan(&f.sourceKind, &f.id, &f.titleID, &f.version, &f.kind, &f.hash, &f.release,
			&f.quality, &f.size, &episodes, &f.path, &f.target, &f.unreadable, &from, &raw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(episodes), &f.episodes)
		var stored adoptedSource
		if o.Policy.Versions[f.version] == nil || json.Unmarshal([]byte(raw), &stored) != nil || stored.From != from {
			continue
		}
		f.hash = indexer.NormalizeHash(f.hash)
		if f.hash == "" {
			f.hash = indexer.NormalizeHash(stored.Hash)
		}
		if f.release == "" {
			f.release = stored.Release
		}
		f.indexer = o.indexerOf(stored.Indexers)
		f.arrVersion = stored.Version
		out = append(out, f)
	}
	return out, rows.Err()
}

// indexerOf resolves stored arr indexer names: any unknown name (or a grab without one) leaves no indexer, else the
// first.
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
	// Imported arr files are no source any more: their open offers are forgotten, not declined, so the same file
	// offered from an adopted link is decided afresh.
	if !dryRun {
		if err := o.Store.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `DELETE FROM router_offer WHERE source_kind = 'arr_file'
				AND outcome IN ('pending', 'gave_up')`)
			return err
		}); err != nil {
			return nil, err
		}
	}
	files, err := o.fileSources(ctx)
	if err != nil {
		return nil, err
	}
	key := func(kind string, id int64, version string) string { return fmt.Sprintf("%s/%d/%s", kind, id, version) }
	live := map[string]string{}
	for _, f := range files {
		live[key(f.sourceKind, f.id, f.version)] = f.hash
	}
	rows, err := o.Store.Reader().QueryContext(ctx, `SELECT DISTINCT source_kind, source_id, title_id, from_version,
		info_hash, release_title FROM router_offer WHERE outcome IN ('pending', 'gave_up') AND source_kind <> 'arr_file'`)
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
