package importarr

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Parallel versions (policy versions[k].follows, docs/parallel.md). The import gives every version that follows the
// instance's version the same titles and monitoring: the version row with the movie's or series' monitored flag, for
// a series each episode's monitored flag the arr lists and the arr's season choices and monitorNewItems as the
// version's intent (series_monitor, which the metadata refresh applies to episodes the arr does not list yet). Its
// files are never the arr's: no version_file, version_source or arr_title row ever names a follower, so decide,
// the library and the views see only the links Theatarr makes for it (theatarr library adopt, then its own imports). Its state is
// those links': a new follower version starts wanted, and only the series summary is recomputed here, from the
// episode states the library keeps. Only the owning instance's view of a title is followed. A title the arr drops
// leaves its follower version in place (links may hang off it) but unmonitored, as the arr no longer wants it.

// FollowCounts counts what the import did for parallel versions.
type FollowCounts struct {
	// Titles is the owned titles followed, once per follower.
	Titles int `json:"titles"`
	// Created is the follower versions created in this run.
	Created int `json:"created"`
	// Unmonitored is the follower versions unmonitored because the arr no longer has their title.
	Unmonitored int `json:"unmonitored"`
}

func (w *wtx) follow(ctx context.Context, src Source, snap *Snapshot, arrTitles map[int]titleRef,
	episodes map[int]map[int]int64, sum *Summary) error {
	if len(src.Followers) == 0 {
		return nil
	}
	for _, t := range snap.Titles {
		ref, ok := arrTitles[t.ArrID]
		if !ok || !ref.owner {
			continue
		}
		for _, key := range src.Followers {
			vid, created, wasMonitored, err := w.followerVersion(ctx, ref.titleID, key, t.Monitored)
			if err != nil {
				return fmt.Errorf("arr id %d: follower %s: %w", t.ArrID, key, err)
			}
			sum.Followed.Titles++
			if created {
				sum.Followed.Created++
			}
			if (created || (!wasMonitored && t.Monitored)) && t.Monitored {
				addFollowSearch(&sum.FollowSearches, FollowSearchTarget{TitleID: ref.titleID, VersionKey: key, All: true})
			}
			if _, err := w.upsert(ctx, "version_follow", []kv{{"version_id", vid}},
				[]kv{{"follows_id", ref.versionID}}, nil, nil); err != nil {
				return err
			}
			if t.Kind != "series" {
				continue
			}
			newEpisodes, err := w.followEpisodes(ctx, vid, t, episodes[t.ArrID])
			if err != nil {
				return fmt.Errorf("arr id %d: follower %s: %w", t.ArrID, key, err)
			}
			for _, id := range newEpisodes {
				addFollowSearch(&sum.FollowSearches, FollowSearchTarget{TitleID: ref.titleID, VersionKey: key, EpisodeIDs: []int64{id}})
			}
			if err := w.seriesMonitor(ctx, vid, t); err != nil {
				return fmt.Errorf("arr id %d: follower %s: %w", t.ArrID, key, err)
			}
			if err := w.followSeriesState(ctx, vid); err != nil {
				return fmt.Errorf("arr id %d: follower %s: %w", t.ArrID, key, err)
			}
		}
	}
	return nil
}

func addFollowSearch(all *FollowSearches, t FollowSearchTarget) {
	for i := range *all {
		cur := &(*all)[i]
		if cur.TitleID != t.TitleID || cur.VersionKey != t.VersionKey {
			continue
		}
		if t.All {
			cur.All = true
			cur.EpisodeIDs = nil
			return
		}
		if cur.All {
			return
		}
		for _, id := range t.EpisodeIDs {
			if !slices.Contains(cur.EpisodeIDs, id) {
				cur.EpisodeIDs = append(cur.EpisodeIDs, id)
			}
		}
		return
	}
	*all = append(*all, t)
}

// followerVersion makes the title's follower version exist with the arr's monitored flag. A new one is wanted: it
// has no file until Theatarr links one.
func (w *wtx) followerVersion(ctx context.Context, titleID int64, key string, monitored bool) (int64, bool, bool, error) {
	var id int64
	var was bool
	err := w.tx.QueryRowContext(ctx, `SELECT id, monitored FROM version WHERE title_id = ? AND policy_key = ?`, titleID, key).Scan(&id, &was)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := w.tx.ExecContext(ctx, `INSERT INTO version (title_id, policy_key, monitored, state, created_at, updated_at)
			VALUES (?, ?, ?, 'wanted', ?, ?)`, titleID, key, dbValue(monitored), w.now, w.now)
		if err != nil {
			return 0, false, false, err
		}
		w.changes.add("version", opInsert, 1)
		id, err = res.LastInsertId()
		return id, true, false, err
	}
	if err != nil {
		return 0, false, false, err
	}
	if _, err := w.upsert(ctx, "version", []kv{{"id", id}}, []kv{{"monitored", monitored}}, nil,
		[]kv{{"updated_at", w.now}}); err != nil {
		return 0, false, was, err
	}
	return id, false, was, nil
}

// followEpisodes mirrors the complete episode set: listed episodes take the arr's monitored flag, missing ones
// are unmonitored. A new state is wanted; an existing one keeps its state (the follower's own links set it).
func (w *wtx) followEpisodes(ctx context.Context, versionID int64, t Title, ids map[int]int64) ([]int64, error) {
	live := map[int64]bool{}
	var newlyMonitored []int64
	for _, e := range t.Episodes {
		id, ok := ids[e.ArrID]
		if !ok {
			continue
		}
		live[id] = true
		var old bool
		err := w.tx.QueryRowContext(ctx, `SELECT monitored FROM episode_version_state WHERE version_id = ? AND episode_id = ?`, versionID, id).Scan(&old)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if _, err := w.upsert(ctx, "episode_version_state", []kv{{"version_id", versionID}, {"episode_id", id}},
			[]kv{{"monitored", e.Monitored}}, []kv{{"state", "wanted"}, {"cutoff_met", false}, {"updated_at", w.now}},
			[]kv{{"updated_at", w.now}}); err != nil {
			return nil, err
		}
		if e.Monitored && !old && e.AirDateUTC != nil && !e.AirDateUTC.After(w.nowTime) {
			newlyMonitored = append(newlyMonitored, id)
		}
	}
	rows, err := w.tx.QueryContext(ctx, `SELECT episode_id FROM episode_version_state WHERE version_id = ? AND monitored = 1`, versionID)
	if err != nil {
		return nil, err
	}
	var gone []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		if !live[id] {
			gone = append(gone, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range gone {
		if _, err := w.upsert(ctx, "episode_version_state", []kv{{"version_id", versionID}, {"episode_id", id}},
			[]kv{{"monitored", false}}, nil, []kv{{"updated_at", w.now}}); err != nil {
			return nil, err
		}
	}
	// Followers changed after episodes() computed the shared monitored flag.
	n, err := w.exec(ctx, `UPDATE episode SET monitored = s.monitored, updated_at = ? FROM
		(SELECT episode_id, MAX(monitored) AS monitored FROM episode_version_state GROUP BY episode_id) s
		WHERE episode.id = s.episode_id AND episode.title_id IN
		(SELECT title_id FROM version WHERE id = ?) AND episode.monitored <> s.monitored`, w.now, versionID)
	if err != nil {
		return nil, err
	}
	w.changes.add("episode", opUpdate, int(n))
	return newlyMonitored, nil
}

// followSeriesState recomputes a follower series' summary from its episode states, as the library does after an
// import: wanted while a monitored aired episode is wanted, else filled or cutoff_met by its files.
func (w *wtx) followSeriesState(ctx context.Context, versionID int64) error {
	n, err := w.exec(ctx, `UPDATE version SET state = s.state, updated_at = ? FROM (SELECT CASE
		WHEN EXISTS (SELECT 1 FROM episode_version_state es JOIN episode e ON e.id = es.episode_id
			WHERE es.version_id = ? AND es.monitored = 1 AND es.state = 'wanted'
			AND e.air_date_utc_at IS NOT NULL AND e.air_date_utc_at <= ?) THEN 'wanted'
		WHEN EXISTS (SELECT 1 FROM episode_version_state WHERE version_id = ? AND state = 'filled') THEN 'filled'
		WHEN EXISTS (SELECT 1 FROM episode_version_state WHERE version_id = ? AND state = 'cutoff_met') THEN 'cutoff_met'
		ELSE 'wanted' END AS state) s WHERE version.id = ? AND version.state <> s.state`,
		w.now, versionID, w.now, versionID, versionID, versionID)
	if err != nil {
		return err
	}
	w.changes.add("version", opUpdate, int(n))
	return nil
}

// unfollowStale unmonitors the follower versions of the titles this instance owned and no longer has. It runs before
// removeStale, which releases the ownership it reads.
func (w *wtx) unfollowStale(ctx context.Context, src Source, live map[int]titleRef, sum *Summary) error {
	if len(src.Followers) == 0 {
		return nil
	}
	rows, err := w.tx.QueryContext(ctx, `SELECT a.arr_id, a.title_id FROM arr_title a
		JOIN version_source vs ON vs.version_id = a.version_id AND vs.instance = a.instance WHERE a.instance = ?`,
		src.Instance)
	if err != nil {
		return err
	}
	var gone []int64
	for rows.Next() {
		var arrID int
		var titleID int64
		if err := rows.Scan(&arrID, &titleID); err != nil {
			rows.Close()
			return err
		}
		if _, ok := live[arrID]; !ok {
			gone = append(gone, titleID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, titleID := range gone {
		for _, key := range src.Followers {
			n, err := w.exec(ctx, `UPDATE version SET monitored = 0, updated_at = ? WHERE title_id = ? AND policy_key = ?
				AND monitored = 1`, w.now, titleID, key)
			if err != nil {
				return err
			}
			w.changes.add("version", opUpdate, int(n))
			sum.Followed.Unmonitored += int(n)
			n, err = w.exec(ctx, `UPDATE episode_version_state SET monitored = 0, updated_at = ? WHERE monitored = 1
				AND version_id = (SELECT id FROM version WHERE title_id = ? AND policy_key = ?)`, w.now, titleID, key)
			if err != nil {
				return err
			}
			w.changes.add("episode_version_state", opUpdate, int(n))
		}
	}
	return nil
}

// SaveFollowSearches records follower changes as add-time search intents (follow_search_intent) in tx, merged with
// the intents still pending for the same title and version as addFollowSearch merges them: the import writes them in
// its own transaction, so a change is never lost between the commit and the searches (internal/acquire drains them).
func SaveFollowSearches(ctx context.Context, tx *sql.Tx, targets FollowSearches, now int64) error {
	for _, t := range targets {
		cur := FollowSearchTarget{TitleID: t.TitleID, VersionKey: t.VersionKey}
		var all int
		var eps string
		err := tx.QueryRowContext(ctx, `SELECT all_items, episodes FROM follow_search_intent WHERE title_id = ?
			AND version_key = ?`, t.TitleID, t.VersionKey).Scan(&all, &eps)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			cur.All = all == 1
			_ = json.Unmarshal([]byte(eps), &cur.EpisodeIDs)
		}
		merged := FollowSearches{cur}
		addFollowSearch(&merged, t)
		m := merged[0]
		b, _ := json.Marshal(nonNil(m.EpisodeIDs))
		all = 0
		if m.All {
			all = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO follow_search_intent (title_id, version_key, all_items, episodes,
			created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (title_id, version_key) DO UPDATE SET
			all_items = excluded.all_items, episodes = excluded.episodes, updated_at = excluded.updated_at`,
			m.TitleID, m.VersionKey, all, string(b), now, now); err != nil {
			return err
		}
	}
	return nil
}
