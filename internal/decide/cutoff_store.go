package decide

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/theatarr/theatarr/internal/policy"
)

// "Below the quality cutoff" is stored with the file state it judges (version.below_quality_cutoff for a movie,
// episode_version_state.below_quality_cutoff for an episode), so Wanted › Cutoff unmet and the search round's cutoff
// targets read it instead of judging every file (router.wanted.cutoff_unmet: quality). It is QualityCutoffNotMet
// for the file decide compares a release with (the newest active link): the arrs' Cutoff Unmet rule, the
// custom-format score aside. No file, or a file that cannot be judged, stores 0. A prefer-SeaDex library is not
// stored: its cutoff reads the curator's standing and cache evidence, which change on their own, so acquisition
// judges it when it runs. The library stores the flags at import, adoption, Change episodes, unlink and with its
// state recompute (a settings change re-judges the libraries whose settings changed).

// QualityCutoffs judges version versionID (library key, of title titleID) by the quality cutoff: whether a movie's
// file is below it, or the episodes whose file is. Nothing for a prefer-SeaDex library.
func (e *Engine) QualityCutoffs(ctx context.Context, db *sql.DB, titleID, versionID int64, key string) (bool,
	map[int64]bool, error) {
	vp := e.Policy.Versions[key]
	if vp == nil {
		return false, nil, fmt.Errorf("decide: unknown version %q", key)
	}
	if vp.PreferSeaDex {
		return false, nil, nil
	}
	v := Version{ID: versionID, Key: key}
	if vp.Kind != policy.KindSeries {
		f, err := LoadExisting(ctx, db, versionID)
		if err != nil || f == nil {
			return false, nil, err
		}
		return e.QualityBelow(v, f), nil, nil
	}
	eps, err := LoadEpisodes(ctx, db, titleID, versionID)
	if err != nil {
		return false, nil, err
	}
	out := map[int64]bool{}
	judged := map[int64]bool{} // by file
	for _, ep := range eps {
		if ep.File == nil {
			continue
		}
		below, ok := judged[ep.FileID]
		if !ok {
			below = e.QualityBelow(v, ep.File)
			judged[ep.FileID] = below
		}
		if below {
			out[ep.ID] = true
		}
	}
	return false, out, nil
}

// QualityBelow is QualityCutoffNotMet for a file of a library without prefer-SeaDex; false when it cannot be judged.
func (e *Engine) QualityBelow(v Version, f *ExistingFile) bool {
	below, err := e.QualityCutoffNotMet(v, f)
	return err == nil && below
}

// StoreQualityCutoff judges version versionID (QualityCutoffs) and stores the answer: rd reads, wr writes. A
// prefer-SeaDex library is left.
func (e *Engine) StoreQualityCutoff(ctx context.Context, rd, wr *sql.DB, titleID, versionID int64, key string) error {
	vp := e.Policy.Versions[key]
	if vp == nil || vp.PreferSeaDex {
		return nil
	}
	movie, eps, err := e.QualityCutoffs(ctx, rd, titleID, versionID, key)
	if err != nil {
		return err
	}
	return WriteQualityCutoff(ctx, wr, vp.Kind, versionID, movie, eps)
}

// WriteQualityCutoff stores a version's quality-cutoff answer: a movie's flag, or a series' episodes below it (every
// other episode 0).
func WriteQualityCutoff(ctx context.Context, wr *sql.DB, kind policy.Kind, versionID int64, movie bool,
	episodes map[int64]bool) error {
	if kind != policy.KindSeries {
		_, err := wr.ExecContext(ctx, `UPDATE version SET below_quality_cutoff = ? WHERE id = ?
			AND below_quality_cutoff <> ?`, movie, versionID, movie)
		return err
	}
	ids := make([]int64, 0, len(episodes))
	for id := range episodes {
		ids = append(ids, id)
	}
	list, _ := json.Marshal(ids)
	_, err := wr.ExecContext(ctx, `UPDATE episode_version_state SET below_quality_cutoff =
		(episode_id IN (SELECT value FROM json_each(?))) WHERE version_id = ?
		AND below_quality_cutoff <> (episode_id IN (SELECT value FROM json_each(?)))`, string(list), versionID, string(list))
	return err
}
