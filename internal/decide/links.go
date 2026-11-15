package decide

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// LinkedFile takes the file's parse from the original release, never its library name. The quality is the
// imported decision's quality, which may differ from what the name alone says (manual imports).
func LinkedFile(kind policy.Kind, rel Release, quality string) *ExistingFile {
	title := rel.Title
	f := &ExistingFile{Quality: quality, Revision: 1, SceneName: title, Size: rel.Size, InfoHash: rel.InfoHash,
		SeaDex: SeaDexRank(&rel.Release), SeaDexPreference: indexer.SeaDexPreference(&rel.Release)}
	identity, conflict := indexer.ReleaseIdentity(rel.Release)
	f.seaDexNoIdentity = conflict || identity == ""
	f.SeaDexEntry, _ = strconv.Atoi(rel.Extras["anilist"])
	fl := format.Radarr
	if kind == policy.KindSeries {
		fl = format.Sonarr
	}
	f.IndexerFlags = indexerFlags(fl, &rel)
	if kind == policy.KindSeries {
		if p := sonarr.ParseTitle(title); p != nil {
			f.Revision, f.Real = max(p.Quality.Revision.Version, 1), p.Quality.Revision.Real
			f.ReleaseType = p.ReleaseType().String()
			if p.ReleaseGroup != nil {
				f.ReleaseGroup = *p.ReleaseGroup
			}
			for _, l := range p.Languages {
				f.Languages = append(f.Languages, l.Name)
			}
		}
	} else if p := radarr.ParseMovieTitle(title, false); p != nil {
		f.Revision, f.Real = max(p.Quality.Revision.Version, 1), p.Quality.Revision.Real
		f.Edition = p.Edition
		if p.ReleaseGroup != nil {
			f.ReleaseGroup = *p.ReleaseGroup
		}
		for _, l := range p.Languages {
			f.Languages = append(f.Languages, l.Name)
		}
	}
	if len(rel.Languages) > 0 {
		f.Languages = append([]string(nil), rel.Languages...)
	}
	return f
}

// loadLinks reads active links in replacement order. File ids are the negative link ids.
func loadLinks(ctx context.Context, db *sql.DB, versionID int64) (map[int64]*ExistingFile, []int64, error) {
	all, err := loadLinksOf(ctx, db, []int64{versionID})
	if err != nil {
		return nil, nil, err
	}
	if l := all[versionID]; l != nil {
		return l.files, l.order, nil
	}
	return map[int64]*ExistingFile{}, nil, nil
}

// versionLinks are one version's active links (loadLinks).
type versionLinks struct {
	files map[int64]*ExistingFile
	order []int64
}

// loadLinksOf is loadLinks for many versions at once, by version id; a version without links is left out.
func loadLinksOf(ctx context.Context, db *sql.DB, versionIDs []int64) (map[int64]*versionLinks, error) {
	list, _ := json.Marshal(versionIDs)
	rows, err := db.QueryContext(ctx, `SELECT lv.version_id, l.id, l.file_metadata, t.kind, COALESCE(l.release_title, ''),
		COALESCE(l.quality, ''), l.path, l.target, COALESCE(cf.size_bytes, 0), COALESCE(c.info_hash, ''),
		l.created_at, le.episode_id
		FROM link l JOIN link_version lv ON lv.link_id = l.id JOIN title t ON t.id = l.title_id
		LEFT JOIN content_file cf ON cf.id = l.content_file_id LEFT JOIN content c ON c.id = cf.content_id
		LEFT JOIN link_episode le ON le.link_id = l.id
		WHERE lv.version_id IN (SELECT value FROM json_each(?)) AND l.state = 'active'
		ORDER BY lv.version_id, l.created_at, l.id, le.episode_id`, string(list))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*versionLinks{}
	for rows.Next() {
		var version, id, size, added int64
		var ep sql.NullInt64
		var raw, kind, title, quality, path, target, hash string
		if err := rows.Scan(&version, &id, &raw, &kind, &title, &quality, &path, &target, &size, &hash, &added, &ep); err != nil {
			return nil, err
		}
		l := out[version]
		if l == nil {
			l = &versionLinks{files: map[int64]*ExistingFile{}}
			out[version] = l
		}
		files := l.files
		id = -id
		if files[id] == nil {
			var f ExistingFile
			if err := json.Unmarshal([]byte(raw), &f); err != nil {
				return nil, fmt.Errorf("decide: link %d metadata: %w", -id, err)
			}
			// Migration 00038 preserves the old release inputs. Reconstruct the parse on read for links made
			// before parsed metadata was stored; new imports always record a nonzero revision, and their scene name
			// as the import found it: a pack's file has none (its formats read its own file name), so the release
			// title is not put back for them.
			legacy := f.Revision == 0
			if legacy {
				var rel indexer.Release
				if err := json.Unmarshal([]byte(raw), &rel); err != nil {
					return nil, err
				}
				rel.Title = f.SceneName
				if rel.Title == "" {
					rel.Title = title
				}
				parsed := LinkedFile(policy.Kind(kind), Release{Release: rel}, f.Quality)
				parsed.Size, parsed.InfoHash = f.Size, f.InfoHash
				parsed.Formats, parsed.Score = f.Formats, f.Score
				parsed.Added = f.Added
				if len(f.Languages) > 0 {
					parsed.Languages = f.Languages
				}
				f = *parsed
			}
			if f.Quality == "" {
				f.Quality = quality
			}
			if f.SceneName == "" && legacy {
				f.SceneName = title
			}
			if f.Size == 0 {
				f.Size = size
			}
			if f.InfoHash == "" {
				f.InfoHash = hash
			}
			if f.RelativePath == "" {
				f.RelativePath = path
			}
			if f.OriginalFilePath == "" {
				f.OriginalFilePath = target
			}
			if f.Added.IsZero() && added > 0 {
				f.Added = time.UnixMilli(added).UTC()
			}
			files[id] = &f
			l.order = append(l.order, id)
		}
		if ep.Valid {
			files[id].EpisodeIDs = append(files[id].EpisodeIDs, ep.Int64)
		}
	}
	return out, rows.Err()
}
