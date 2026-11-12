package router

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// versions[*].upgrades (docs/design/router.md "Upgrades") says what a route does with a release that would replace a
// file the version already has for the release's items (the movie, or any of the release's episodes):
//   - auto: nothing here; the release is routed like any other.
//   - off: the release is skipped (upgrades_off). A filled item keeps its file, whatever is found.
//
// Releases for items with no file are never refused by it: in a series route a pack that replaces some files is
// skipped while a release that only fills missing episodes is tried as usual.

// codeUpgradesOff is the step code of a release skipped by versions[*].upgrades: off.
const codeUpgradesOff = "upgrades_off"

// itemFiles is what a version holds for a release's items.
type itemFiles struct {
	file *decide.ExistingFile
	// episodes with a file (empty for a movie); quality and score of the first file; at the newest file's time.
	episodes []int64
	quality  string
	revision int
	real     int
	score    *int64
	at       int64
	n        int
}

// currentFiles reads the version's current files for the items: active links, then the arr's files of the
// instance that owns the version (as the title page reads them). episodes empty means every file of the title.
func currentFiles(ctx context.Context, db *sql.DB, titleID, versionID int64, episodes []int64) (itemFiles, error) {
	type file struct {
		quality  string
		revision int
		real     int
		score    *int64
		at       int64
	}
	byEp := map[int64]file{}
	add := func(eid int64, f file, win bool) {
		if len(episodes) > 0 && !slices.Contains(episodes, eid) {
			return
		}
		if old, ok := byEp[eid]; ok && !win && old.at >= f.at {
			return
		}
		byEp[eid] = f
	}
	scan := func(q string, win bool) error {
		rows, err := db.QueryContext(ctx, q, versionID, titleID)
		if err != nil {
			if strings.Contains(err.Error(), "no such table") {
				return nil
			}
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var eid int64
			var f file
			var score sql.NullInt64
			if err := rows.Scan(&eid, &f.quality, &f.revision, &f.real, &score, &f.at); err != nil {
				return err
			}
			if score.Valid {
				v := score.Int64
				f.score = &v
			}
			add(eid, f, win)
		}
		return rows.Err()
	}
	if err := scan(`SELECT COALESCE(fe.episode_id, 0), vf.quality, vf.quality_revision, 0, vf.custom_format_score,
		COALESCE(vf.added_at, 0)
		FROM version_file vf LEFT JOIN version_file_episode fe ON fe.version_file_id = vf.id
		LEFT JOIN version_source vs ON vs.version_id = vf.version_id
		WHERE vf.version_id = ? AND vf.title_id = ? AND (vs.instance IS NULL OR vs.instance = vf.instance)`, false); err != nil {
		return itemFiles{}, fmt.Errorf("router: current files: %w", err)
	}
	if err := scan(`SELECT COALESCE(le.episode_id, 0), COALESCE(l.quality, ''),
		COALESCE(json_extract(l.file_metadata, '$.Revision'), 0), COALESCE(json_extract(l.file_metadata, '$.Real'), 0),
		l.score, l.created_at
		FROM link l JOIN link_version lv ON lv.link_id = l.id LEFT JOIN link_episode le ON le.link_id = l.id
		WHERE lv.version_id = ? AND l.title_id = ? AND l.state = 'active'`, true); err != nil {
		return itemFiles{}, fmt.Errorf("router: current links: %w", err)
	}
	var out itemFiles
	ids := make([]int64, 0, len(byEp))
	for id := range byEp {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for i, id := range ids {
		f := byEp[id]
		if i == 0 {
			out.quality, out.revision, out.real, out.score = f.quality, f.revision, f.real, f.score
		}
		out.at = max(out.at, f.at)
		if id != 0 {
			out.episodes = append(out.episodes, id)
		}
	}
	out.n = len(ids)
	return out, nil
}

// screenUpgrades applies versions[*].upgrades before any candidate is tried: it returns the candidates the route
// may try, and records a step for each one it kept back.
func (r *Router) screenUpgrades(ctx context.Context, s *routeState, cands []Candidate) ([]Candidate, error) {
	if s.vp.UpgradeRule() != policy.UpgradesOff {
		return cands, nil
	}
	var out []Candidate
	for i := range cands {
		c := &cands[i]
		cur, err := r.currentFilesNow(ctx, s, c.Episodes)
		if err != nil {
			return nil, err
		}
		if cur.n == 0 {
			out = append(out, *c)
			continue
		}
		s.res.Steps = append(s.res.Steps, Step{Outcome: "skipped", Code: codeUpgradesOff,
			Release: c.Release.Title, Hash: indexer.NormalizeHash(c.Release.InfoHash),
			Message: fmt.Sprintf("versions.%s.upgrades is off: it would replace %s", s.req.Version, fileWords(cur))})
	}
	return out, nil
}

// fileWords names the files a release would replace: "WEBDL-2160p (score 1400)", "3 episode files (WEBDL-1080p)".
func fileWords(cur itemFiles) string {
	q := cur.quality
	if q == "" {
		q = "unknown quality"
	}
	if cur.score != nil {
		q += fmt.Sprintf(", score %d", *cur.score)
	}
	if len(cur.episodes) > 1 {
		return fmt.Sprintf("%d episode files (%s)", len(cur.episodes), q)
	}
	return "the file (" + q + ")"
}
