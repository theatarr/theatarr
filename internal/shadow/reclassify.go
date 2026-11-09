package shadow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Better is the short form of a release kept on a diff row: our search's pick when it ranks above the row's own
// (search_found_better).
type Better struct {
	Release string `json:"release"`
	Indexer string `json:"indexer,omitempty"`
	Quality string `json:"quality"`
	Score   int    `json:"score"`
}

// rankFor ranks releases of a version by its profile's quality order (nil when the profile was not built).
func (r *Runner) rankFor(key string) RankFunc {
	vp := r.pol.Versions[key]
	if vp == nil || r.eng == nil {
		return nil
	}
	fl := format.Radarr
	if vp.Kind == policy.KindSeries {
		fl = format.Sonarr
	}
	if p := r.eng.Profile(vp.Profile, fl); p != nil {
		return ProfileRank(p.Profile, vp.PreferSeaDex)
	}
	return nil
}

// classifyStored classifies a stored grab from its stored decisions and our RSS picks near it, links those picks to
// it, and writes its diff row. prev is the row's bucket/reason before ("" when it had none).
func (r *Runner) classifyStored(ctx context.Context, tx *sql.Tx, grabID int64) (cls Classification, prev string, err error) {
	var titleID, at int64
	var key, release, source, note, arrJSON string
	var pickJSON sql.NullString
	var searched, foundArr bool
	err = tx.QueryRowContext(ctx, `SELECT title_id, version_key, grabbed_at, release_title, release_source, searched,
		search_note, found_arr, arr_decision, pick FROM shadow_grab WHERE id = ? AND state = 'done'`, grabID).Scan(
		&titleID, &key, &at, &release, &source, &searched, &note, &foundArr, &arrJSON, &pickJSON)
	if err != nil {
		return cls, "", fmt.Errorf("shadow_grab %d: %w", grabID, err)
	}
	in := GrabInput{Source: source, GrabbedAt: store.FromMillis(at), Searched: searched, FoundArr: foundArr,
		Rank: r.rankFor(key)}
	if err := json.Unmarshal([]byte(arrJSON), &in.Arr); err != nil {
		return cls, "", err
	}
	if pickJSON.Valid {
		in.Pick = &Decided{}
		if err := json.Unmarshal([]byte(pickJSON.String), in.Pick); err != nil {
			return cls, "", err
		}
	}
	if BasisFor(source) == BasisRSS {
		w := store.Millis(in.GrabbedAt.Add(RSSWindow)) - at
		if _, err := tx.ExecContext(ctx, `UPDATE shadow_rss_pick SET status = 'linked', grab_id = ? WHERE title_id = ? AND
			version_key = ? AND status = 'open' AND grab_id IS NULL AND seen_at BETWEEN ? AND ?`, grabID, titleID, key,
			at-w, at+w); err != nil {
			return cls, "", err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id, indexer, seen_at, decision FROM shadow_rss_pick WHERE grab_id = ?
			AND seen_at BETWEEN ? AND ? ORDER BY seen_at, id`, grabID, at-w, at+w)
		if err != nil {
			return cls, "", err
		}
		for rows.Next() {
			var c RSSCandidate
			var seen int64
			var dec string
			if err := rows.Scan(&c.ID, &c.Indexer, &seen, &dec); err != nil {
				rows.Close()
				return cls, "", err
			}
			c.SeenAt = store.FromMillis(seen)
			if err := json.Unmarshal([]byte(dec), &c.Decided); err != nil {
				rows.Close()
				return cls, "", err
			}
			in.RSS = append(in.RSS, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return cls, "", err
		}
	}
	cls = ClassifyGrab(in)

	// The RSS pick the row names: the one it rests on, else the best one linked to the grab.
	var pickID sql.NullInt64
	detail := cls.Detail
	if cls.RSSPick != nil {
		pickID = sql.NullInt64{Int64: cls.RSSPick.ID, Valid: true}
	} else {
		var rel string
		var seen int64
		err := tx.QueryRowContext(ctx, `SELECT id, release_title, seen_at FROM shadow_rss_pick WHERE grab_id = ?
			ORDER BY score DESC, seen_at LIMIT 1`, grabID).Scan(&pickID, &rel, &seen)
		switch {
		case err == nil:
			detail += fmt.Sprintf("; our RSS read picked %s at %s", rel, store.FromMillis(seen).Format(time.RFC3339))
		case !errors.Is(err, sql.ErrNoRows):
			return cls, "", err
		}
	}
	if note != "" {
		detail += "; search: " + note
	}
	var ourRel, ourQ string
	var ourScore, better any
	if cls.Ours != nil {
		ourRel, ourQ, ourScore = cls.Ours.Release, cls.Ours.Quality, cls.Ours.Score
	}
	if b := cls.SearchBetter; b != nil {
		if better, err = jsonString(Better{Release: b.Release, Indexer: b.Indexer, Quality: b.Quality, Score: b.Score}); err != nil {
			return cls, "", err
		}
	}
	kind := string(policy.KindMovie)
	if vp := r.pol.Versions[key]; vp != nil && vp.Kind == policy.KindSeries {
		kind = string(policy.KindSeries)
	}
	var pb, pr string
	err = tx.QueryRowContext(ctx, `SELECT bucket, reason FROM shadow_diff WHERE grab_id = ?`, grabID).Scan(&pb, &pr)
	switch {
	case err == nil:
		prev = pb + "/" + pr
	case !errors.Is(err, sql.ErrNoRows):
		return cls, "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO shadow_diff (at, title_id, version_key, bucket, reason, detail, searched,
		grab_id, rss_pick_id, our_release, our_quality, our_score, arr_release, arr_quality, arr_score, created_at, kind,
		search_better) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (grab_id) DO UPDATE SET at = excluded.at, title_id = excluded.title_id,
			version_key = excluded.version_key, bucket = excluded.bucket, reason = excluded.reason,
			detail = excluded.detail, searched = excluded.searched, rss_pick_id = excluded.rss_pick_id,
			our_release = excluded.our_release, our_quality = excluded.our_quality, our_score = excluded.our_score,
			arr_release = excluded.arr_release, arr_quality = excluded.arr_quality, arr_score = excluded.arr_score,
			kind = excluded.kind, search_better = excluded.search_better`,
		at, titleID, key, cls.Bucket, cls.Reason, detail, boolInt(searched), grabID, pickID, ourRel, ourQ, ourScore,
		release, in.Arr.Quality, in.Arr.Score, store.Millis(r.now()), kind, better)
	return cls, prev, err
}

// ReclassifySummary is one Reclassify run.
type ReclassifySummary struct {
	// Grabs classified again; Changed those whose bucket or reason moved (Moves: "old -> new" counts); Skipped
	// grabs of versions not shadowed now.
	Grabs   int            `json:"grabs"`
	Changed int            `json:"changed"`
	Skipped int            `json:"skipped"`
	Moves   map[string]int `json:"moves"`
}

// Reclassify classifies every stored grab again from its stored decisions and our RSS picks, with no search: the
// rules may have changed since it was decided. theatarr serve runs it at start; theatarr shadow reclassify runs it by hand.
func (r *Runner) Reclassify(ctx context.Context) (*ReclassifySummary, error) {
	rows, err := r.st.Reader().QueryContext(ctx, `SELECT id, version_key FROM shadow_grab WHERE state = 'done' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	sum := &ReclassifySummary{Moves: map[string]int{}}
	for rows.Next() {
		var id int64
		var key string
		if err := rows.Scan(&id, &key); err != nil {
			rows.Close()
			return nil, err
		}
		if !r.versions[key] {
			sum.Skipped++
			continue
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		var cls Classification
		var prev string
		err := r.st.WriteTx(ctx, func(tx *sql.Tx) error {
			var err error
			cls, prev, err = r.classifyStored(ctx, tx, id)
			return err
		})
		if err != nil {
			return sum, err
		}
		sum.Grabs++
		if now := cls.Bucket + "/" + cls.Reason; now != prev {
			sum.Changed++
			sum.Moves[prev+" -> "+now]++
		}
	}
	r.log.Info("shadow reclassify", "grabs", sum.Grabs, "changed", sum.Changed, "skipped", sum.Skipped)
	if err := r.RefreshMetrics(ctx); err != nil {
		r.log.Warn("shadow metrics", "err", err)
	}
	return sum, nil
}

// linkRSSPick links a new RSS pick to an RSS grab of the same title and version within RSSWindow, when the arr's
// grab came first and was already decided, and classifies that grab again.
func (r *Runner) linkRSSPick(ctx context.Context, tx *sql.Tx, titleID int64, key string, seen time.Time) error {
	s := store.Millis(seen)
	w := store.Millis(seen.Add(RSSWindow)) - s
	var gid int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM shadow_grab WHERE title_id = ? AND version_key = ? AND state = 'done'
		AND LOWER(release_source) = 'rss' AND grabbed_at BETWEEN ? AND ? ORDER BY ABS(grabbed_at - ?), id LIMIT 1`,
		titleID, key, s-w, s+w, s).Scan(&gid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	cls, prev, err := r.classifyStored(ctx, tx, gid)
	if err == nil && prev != cls.Bucket+"/"+cls.Reason {
		r.log.Info("shadow grab reclassified by a later RSS pick", "grab_id", gid, "was", prev, "now",
			cls.Bucket+"/"+cls.Reason)
	}
	return err
}
