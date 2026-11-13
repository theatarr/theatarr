package decide

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

type grabEvent struct {
	episode  int64
	at       int64
	id       int64
	source   string
	download string
	title    string
	quality  profile.QualityModel
	imported bool
}

// loadGrabHistory reconstructs the last grab and matching import for each item in this version from Theatarr's own
// downloads of this version_id only. Theatarr always handles completed downloads.
func loadGrabHistory(ctx context.Context, db *sql.DB, versionID int64, kind policy.Kind) (map[int64]*profile.ImportedGrab, error) {
	out := map[int64]*profile.ImportedGrab{}
	var events []grabEvent
	rows, err := db.QueryContext(ctx, `SELECT d.id, d.added_at, d.info_hash, d.release_title, d.quality,
  d.episodes, d.imported_at, COALESCE(l.file_metadata, ''), COALESCE(l.quality, ''),
  COALESCE(l.release_title, ''), COALESCE(le.episode_id, 0)
  FROM download d LEFT JOIN link l ON l.download_id = d.id
  AND EXISTS (SELECT 1 FROM link_version lv WHERE lv.link_id = l.id AND lv.version_id = d.version_id)
  LEFT JOIN link_episode le ON le.link_id = l.id
  WHERE d.version_id = ? AND d.added_at IS NOT NULL
  ORDER BY d.added_at DESC, d.id DESC, l.id DESC`, versionID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, at, epID int64
		var hash, title, quality, eps, raw, fileQuality, fileTitle string
		var imported sql.NullInt64
		if err := rows.Scan(&id, &at, &hash, &title, &quality, &eps, &imported, &raw, &fileQuality, &fileTitle, &epID); err != nil {
			rows.Close()
			return nil, err
		}
		var ids []int64
		if kind == policy.KindMovie {
			ids = []int64{0}
		} else if err := json.Unmarshal([]byte(eps), &ids); err != nil {
			rows.Close()
			return nil, err
		}
		// The download quality comes from the decision; the revision is parsed from its original release title.
		f := LinkedFile(kind, Release{Release: indexer.Release{Title: title}}, quality)
		qm := historyQuality(kind, f)
		for _, ep := range ids {
			events = append(events, grabEvent{episode: ep, at: at, id: id, source: "theatarr", download: strings.ToUpper(hash), title: title, quality: qm})
		}
		if imported.Valid && raw != "" {
			var file ExistingFile
			if err := json.Unmarshal([]byte(raw), &file); err != nil {
				rows.Close()
				return nil, err
			}
			if file.Quality == "" {
				file.Quality = fileQuality
			}
			if file.Revision == 0 {
				file = *LinkedFile(kind, Release{Release: indexer.Release{Title: fileTitle}}, file.Quality)
			}
			events = append(events, grabEvent{episode: epID, at: imported.Int64, id: id, source: "theatarr", download: strings.ToUpper(hash), quality: historyQuality(kind, &file), imported: true})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].at == events[j].at {
			return events[i].id > events[j].id
		}
		return events[i].at > events[j].at
	})
	for _, ev := range events {
		if ev.imported || out[ev.episode] != nil {
			continue
		}
		g := &profile.ImportedGrab{DownloadID: ev.download, SourceTitle: ev.title, GrabbedQuality: ev.quality}
		for _, imp := range events {
			if imp.imported && imp.episode == ev.episode && imp.source == ev.source && imp.download == ev.download {
				g.Imported, g.ImportedQuality = true, imp.quality
				break
			}
		}
		out[ev.episode] = g
	}
	return out, nil
}

func historyQuality(kind policy.Kind, f *ExistingFile) profile.QualityModel {
	q, ok := profile.QualityByName(flavourOf(kind), f.Quality)
	if !ok {
		q = profile.MustQuality(flavourOf(kind), "Unknown")
	}
	return profile.QualityModel{Quality: q, Revision: profile.Revision{Version: max(f.Revision, 1), Real: f.Real}}
}
