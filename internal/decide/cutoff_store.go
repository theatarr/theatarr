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
// custom-format score aside; for a prefer-SeaDex library under the stored SeaDex standing, so its flags are stored
// again when the standing changes. No file, or a file that cannot be judged, stores 0. The library stores it at
// import, adoption, relink and unlink and with its state recomputes (a settings change re-judges the libraries whose
// settings changed).

// QualityCutoffs judges version versionID (library key, of title titleID) by the quality cutoff: whether a movie's
// file is below it, or the episodes whose file is.
func (e *Engine) QualityCutoffs(ctx context.Context, db *sql.DB, titleID, versionID int64, key string) (bool,
	map[int64]bool, error) {
	vp := e.Policy.Versions[key]
	if vp == nil {
		return false, nil, fmt.Errorf("decide: unknown version %q", key)
	}
	v := Version{ID: versionID, Key: key}
	var title *Title
	if vp.PreferSeaDex {
		var err error
		if ctx, err = WithStoredSeaDex(ctx, db); err != nil {
			return false, nil, err
		}
		ctx = WithSeaDexTitle(WithSeaDexPolicy(ctx, e.Policy, vp), titleID)
		t, err := LoadTitle(ctx, db, titleID)
		if err != nil {
			return false, nil, err
		}
		title = &t
	}
	judge := func(f *ExistingFile) bool {
		var below bool
		var err error
		if title != nil {
			below, err = e.QualityCutoffNotMet(v, f, title)
		} else {
			below, err = e.QualityCutoffNotMet(v, f)
		}
		return err == nil && below
	}
	if vp.Kind != policy.KindSeries {
		f, err := LoadExisting(ctx, db, versionID)
		if err != nil || f == nil {
			return false, nil, err
		}
		return judge(f), nil, nil
	}
	var eps []Episode
	var err error
	if title != nil {
		err = LoadSeriesVersion(ctx, db, title, versionID)
		eps = title.Episodes
	} else {
		eps, err = LoadEpisodes(ctx, db, titleID, versionID)
	}
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
			below = judge(ep.File)
			judged[ep.FileID] = below
		}
		if below {
			out[ep.ID] = true
		}
	}
	return false, out, nil
}

// StoreQualityCutoff judges version versionID (QualityCutoffs) and stores the answer: rd reads, wr writes.
func (e *Engine) StoreQualityCutoff(ctx context.Context, rd, wr *sql.DB, titleID, versionID int64, key string) error {
	movie, eps, err := e.QualityCutoffs(ctx, rd, titleID, versionID, key)
	if err != nil {
		return err
	}
	if e.Policy.Versions[key].Kind != policy.KindSeries {
		_, err = wr.ExecContext(ctx, `UPDATE version SET below_quality_cutoff = ? WHERE id = ?
			AND below_quality_cutoff <> ?`, movie, versionID, movie)
		return err
	}
	ids := make([]int64, 0, len(eps))
	for id := range eps {
		ids = append(ids, id)
	}
	list, _ := json.Marshal(ids)
	_, err = wr.ExecContext(ctx, `UPDATE episode_version_state SET below_quality_cutoff =
		(episode_id IN (SELECT value FROM json_each(?))) WHERE version_id = ?
		AND below_quality_cutoff <> (episode_id IN (SELECT value FROM json_each(?)))`, string(list), versionID, string(list))
	return err
}
