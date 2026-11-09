package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/store"
)

// Anime gating (docs/design/anime.md): an episode that only conflict or rejected franchise mappings cover is
// not imported automatically. Such episodes are taken out of the download before anything is read from the mount;
// the rest imports as usual. When every episode is held, the download stays ready and the import is recorded as a
// dry run with the reason (MappingNotReviewed), so it imports on a later pass once the mapping is reviewed.

// ReasonMappingNotReviewed prefixes the reason of a held import.
const ReasonMappingNotReviewed = "MappingNotReviewed"

// animeHold removes the held episodes from a series download and lists them with their reason. all reports that
// every episode was held.
func (l *Library) animeHold(ctx context.Context, d *importDownload) (held []string, all bool, err error) {
	v := &d.Version
	if v.Title.Kind != "series" || len(v.Episodes) == 0 {
		return nil, false, nil
	}
	am, err := animemap.Load(ctx, l.st.Reader(), v.Title.TvdbID)
	if err != nil || am == nil {
		return nil, false, err
	}
	var keep []episodeInfo
	for _, e := range v.Episodes {
		if ent, blocked := am.Blocked(e.Season, e.Number); blocked {
			held = append(held, animemap.Reason(anime.SE{e.Season, e.Number}, ent))
			continue
		}
		keep = append(keep, e)
	}
	v.Episodes = keep
	return held, len(held) > 0 && len(keep) == 0, nil
}

// holdAnimeReview records an import whose every episode is held: a dry run with the reason; the download stays ready.
func (l *Library) holdAnimeReview(ctx context.Context, d *importDownload, plan *Plan, dryRun bool) (*Plan, error) {
	plan.DryRun = true
	plan.Refused = ReasonMappingNotReviewed + ": " + plan.Held[0]
	if len(plan.Held) > 1 {
		plan.Refused += " (and " + strconv.Itoa(len(plan.Held)-1) + " more)"
	}
	if dryRun {
		return plan, nil
	}
	l.log.Info("library: import held, mapping not reviewed", "download", d.ID, "version", d.Version.Key,
		"episodes", len(plan.Held))
	pj, _ := json.Marshal(plan)
	now := store.Millis(l.now())
	return plan, l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := holdsClaim(ctx, tx, d); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO library_import (download_id, state, last_error, plan, created_at, updated_at)
			VALUES (?, 'dry_run', ?, ?, ?, ?) ON CONFLICT (download_id) DO UPDATE SET state = 'dry_run',
			last_error = excluded.last_error, plan = excluded.plan, updated_at = excluded.updated_at`,
			d.ID, strings.Join(plan.Held, "; "), string(pj), now, now)
		return err
	})
}
