// Package animestore keeps built anime franchises in the database (migration 00017): the franchise graph, entries,
// mapping decisions, the three orders, each stage's output, and the metadata review queue.
//
// A build replaces the franchise's rows, except when the refresh is held for review (docs/design/anime.md): when it would map more than 10% fewer of a TVDB series' episodes than the applied build, or would move
// a mapping whose episodes have linked files (an active link, or an imported arr file). A held build is kept as
// the franchise's pending result and applied when its review items are accepted.
//
// Mapping conflicts and heuristic mappings become review items. A decision taken in review sticks: a later build
// with the same candidate mapping takes the accepted mapping as an override (or keeps a rejected one out) instead
// of asking again.
//
// Import gating reads anime_mapping.status: auto, arbitrated, override and heuristic may import; conflict and
// rejected may not.
package animestore

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/store"
)

// ShrinkLimit is the share of a series' mapped episodes a refresh may drop without review.
const ShrinkLimit = 0.10

// Saved reports what Save did.
type Saved struct {
	Applied bool
	// Held lists why the build was held for review (empty when applied).
	Held []string
	// NewReview counts review items opened.
	NewReview int
}

// Store wraps the database.
type Store struct {
	DB  *store.Store
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func gz(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

func gunzip(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

type mappingRow struct {
	key, status, winner string
	series              int
	eps                 []anime.SE
}

func mappingOf(e *anime.Entry) *mappingRow {
	m := &mappingRow{key: e.Key}
	if e.TVDB != nil {
		m.series = e.TVDB.SeriesID
		m.eps = e.TVDB.Episodes
	}
	switch {
	case e.Mapping != nil:
		m.status, m.winner = e.Mapping.Status, e.Mapping.Winner
	case len(m.eps) > 0:
		m.status, m.winner = anime.StatusAuto, e.TVDB.Method
	default:
		return nil
	}
	if m.status == anime.StatusRejected {
		m.series, m.eps = 0, nil
	}
	return m
}

func (s *Store) previous(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, slug string) (map[string]*mappingRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT entry_key, status, winner, COALESCE(tvdb_series, 0), episodes
		FROM anime_mapping WHERE franchise = ?`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*mappingRow{}
	for rows.Next() {
		m := &mappingRow{}
		var eps string
		if err := rows.Scan(&m.key, &m.status, &m.winner, &m.series, &eps); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(eps), &m.eps); err != nil {
			return nil, err
		}
		out[m.key] = m
	}
	return out, rows.Err()
}

func countBySeries(ms map[string]*mappingRow) map[int]map[anime.SE]bool {
	out := map[int]map[anime.SE]bool{}
	for _, m := range ms {
		if m.series == 0 || m.status == anime.StatusRejected || m.status == anime.StatusConflict {
			continue
		}
		if out[m.series] == nil {
			out[m.series] = map[anime.SE]bool{}
		}
		for _, se := range m.eps {
			out[m.series][se] = true
		}
	}
	return out
}

// linkedEpisodes lists a TVDB series' episodes that have files: an active link, or an imported arr file.
func linkedEpisodes(ctx context.Context, tx *sql.Tx, tvdb int) (map[anime.SE]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.season, e.number FROM episode e JOIN title t ON t.id = e.title_id
		WHERE t.kind = 'series' AND t.tvdb_id = ? AND (
			EXISTS (SELECT 1 FROM link_episode le JOIN link l ON l.id = le.link_id AND l.state = 'active' WHERE le.episode_id = e.id)
			OR EXISTS (SELECT 1 FROM version_file_episode v WHERE v.episode_id = e.id))`, tvdb)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[anime.SE]bool{}
	for rows.Next() {
		var se anime.SE
		if err := rows.Scan(&se[0], &se[1]); err != nil {
			return nil, err
		}
		out[se] = true
	}
	return out, rows.Err()
}

func sameMapping(a, b *mappingRow) bool {
	if a.series != b.series || len(a.eps) != len(b.eps) {
		return false
	}
	seen := map[anime.SE]bool{}
	for _, x := range a.eps {
		seen[x] = true
	}
	for _, x := range b.eps {
		if !seen[x] {
			return false
		}
	}
	return true
}

type held struct {
	key, kind, summary string
	detail             any
}

// holds decides whether a build must wait for review.
func (s *Store) holds(ctx context.Context, tx *sql.Tx, prev map[string]*mappingRow, res *anime.Result) ([]held, error) {
	if len(prev) == 0 {
		return nil, nil
	}
	next := map[string]*mappingRow{}
	for _, e := range res.Entries {
		if m := mappingOf(e); m != nil {
			next[e.Key] = m
		}
	}
	var out []held
	before, after := countBySeries(prev), countBySeries(next)
	var series []int
	for t := range before {
		series = append(series, t)
	}
	sort.Ints(series)
	for _, t := range series {
		n0, n1 := len(before[t]), len(after[t])
		if n0 > 0 && float64(n0-n1) > ShrinkLimit*float64(n0) {
			out = append(out, held{key: fmt.Sprintf("tvdb:%d", t), kind: "refresh_shrink",
				summary: fmt.Sprintf("TVDB %d: the refresh maps %d of the %d episodes mapped now", t, n1, n0),
				detail:  map[string]any{"tvdb": t, "before": n0, "after": n1}})
		}
	}
	linked := map[int]map[anime.SE]bool{}
	var keys []string
	for k := range prev {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := prev[k]
		if p.series == 0 || len(p.eps) == 0 {
			continue
		}
		n := next[k]
		if n != nil && sameMapping(p, n) {
			continue
		}
		if _, ok := linked[p.series]; !ok {
			l, err := linkedEpisodes(ctx, tx, p.series)
			if err != nil {
				return nil, err
			}
			linked[p.series] = l
		}
		var files []anime.SE
		for _, se := range p.eps {
			if linked[p.series][se] {
				files = append(files, se)
			}
		}
		if len(files) == 0 {
			continue
		}
		to := "nothing"
		if n != nil && n.series != 0 {
			to = fmt.Sprintf("TVDB %d %s", n.series, rangeOf(n.eps))
		}
		out = append(out, held{key: k, kind: "refresh_moves_linked",
			summary: fmt.Sprintf("%s: TVDB %d %s (%d with files) would map to %s", k, p.series, rangeOf(p.eps), len(files), to),
			detail:  map[string]any{"before": map[string]any{"tvdb": p.series, "episodes": p.eps}, "with_files": files, "after": n}})
	}
	return out, nil
}

func rangeOf(eps []anime.SE) string {
	parts := make([]string, 0, len(eps))
	for _, se := range eps {
		parts = append(parts, se.String())
	}
	if len(parts) > 6 {
		return parts[0] + ".." + parts[len(parts)-1] + fmt.Sprintf(" (%d)", len(parts))
	}
	return strings.Join(parts, ", ")
}

// Save stores a build. source is "cache" or "live". force applies it even if it would be held (accepting a held
// refresh).
func (s *Store) Save(ctx context.Context, res *anime.Result, source string, force bool) (*Saved, error) {
	body, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	out := &Saved{}
	now := store.Millis(s.now())
	err = s.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		*out = Saved{}
		cfg, _ := json.Marshal(res.Config)
		scope, _ := res.Config["scope"].(string)
		if _, err := tx.ExecContext(ctx, `INSERT INTO anime_franchise (slug, name, seed_anilist, scope, config, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (slug) DO UPDATE SET name = excluded.name,
			seed_anilist = excluded.seed_anilist, scope = excluded.scope, config = excluded.config, updated_at = excluded.updated_at`,
			res.Slug, res.Franchise, res.SeedAniList, scope, string(cfg), now, now); err != nil {
			return err
		}
		prev, err := s.previous(ctx, tx, res.Slug)
		if err != nil {
			return err
		}
		if !force {
			hs, err := s.holds(ctx, tx, prev, res)
			if err != nil {
				return err
			}
			if len(hs) > 0 {
				if _, err := tx.ExecContext(ctx, `UPDATE anime_franchise SET pending_result = ?, pending_source = ?, pending_at = ?
					WHERE slug = ?`, gz(body), source, now, res.Slug); err != nil {
					return err
				}
				for _, h := range hs {
					out.Held = append(out.Held, h.summary)
					n, err := s.openItem(ctx, tx, res.Slug, h.key, h.kind, h.summary, h.detail, now)
					if err != nil {
						return err
					}
					out.NewReview += n
				}
				return nil
			}
		}
		n, err := s.apply(ctx, tx, res, body, source, now)
		out.Applied, out.NewReview = true, n
		return err
	})
	return out, err
}

func (s *Store) openItem(ctx context.Context, tx *sql.Tx, slug, key, kind, summary string, detail any, now int64) (int, error) {
	d, _ := json.Marshal(detail)
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM anime_review WHERE franchise = ? AND entry_key = ? AND kind = ? AND state = 'open'`,
		slug, key, kind).Scan(&id)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE anime_review SET summary = ?, detail = ? WHERE id = ?`, summary, string(d), id)
		return 0, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO anime_review (franchise, entry_key, kind, summary, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, slug, key, kind, summary, string(d), now)
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func bool01(b bool) int {
	if b {
		return 1
	}
	return 0
}

// apply replaces the franchise's rows with the build and syncs the review queue.
func (s *Store) apply(ctx context.Context, tx *sql.Tx, res *anime.Result, body []byte, source string, now int64) (int, error) {
	slug := res.Slug
	// Entries whose mapping this build turns importable (it was a conflict or rejected) re-link their series' files
	// (queueRelink). A first build has no earlier mapping and moves nothing.
	prev, err := s.previous(ctx, tx, slug)
	if err != nil {
		return 0, err
	}
	var relink []int
	if _, err := tx.ExecContext(ctx, `UPDATE anime_franchise SET result = ?, built_at = ?, source = ?, pending_result = NULL,
		pending_source = '', pending_at = NULL, updated_at = ? WHERE slug = ?`, gz(body), now, source, now, slug); err != nil {
		return 0, err
	}
	for _, t := range []string{"anime_stage", "anime_node", "anime_entry", "anime_mapping", "anime_order", "anime_alt_cut"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE franchise = ?`, slug); err != nil {
			return 0, err
		}
	}
	if st := res.Stages; st != nil {
		for name, v := range map[string]any{"crawl": st.Crawl, "joins": st.Joins, "mappings": st.Mappings, "flags": st.Flags, "orders": st.Orders} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO anime_stage (franchise, stage, body) VALUES (?, ?, ?)`, slug, name, jsonText(v)); err != nil {
				return 0, err
			}
		}
		if st.Crawl != nil {
			for _, m := range st.Crawl.Members {
				if _, err := tx.ExecContext(ctx, `INSERT INTO anime_node (franchise, anilist_id, kind, title, why) VALUES (?, ?, ?, ?, ?)
					ON CONFLICT DO NOTHING`, slug, m.AniListID, m.Kind, deref(m.Title), m.How); err != nil {
					return 0, err
				}
			}
		}
	}
	for _, x := range res.ExcludedNeighbours {
		if _, err := tx.ExecContext(ctx, `INSERT INTO anime_node (franchise, anilist_id, kind, title, format, why)
			VALUES (?, ?, 'excluded', ?, ?, ?) ON CONFLICT DO NOTHING`, slug, x.AniListID, deref(x.Title), deref(x.Format), x.Why); err != nil {
			return 0, err
		}
	}
	for _, c := range res.AltCuts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO anime_alt_cut (franchise, name, tvdb_series, entry_key, match, episodes)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, slug, c.Name, c.SeriesID, c.Entry, c.Match, jsonText(c.Episodes)); err != nil {
			return 0, err
		}
	}
	decided, err := s.decided(ctx, tx, slug)
	if err != nil {
		return 0, err
	}
	keep := map[string]bool{}
	opened := 0
	for _, e := range res.Entries {
		eb, _ := json.Marshal(e)
		if _, err := tx.ExecContext(ctx, `INSERT INTO anime_entry (franchise, key, origin, kind, title, format, start_date,
			anilist_id, anidb_id, kitsu_id, flags, target, mainline, tmdb_movie, body) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING`, slug, e.Key, e.Origin, e.Kind, deref(e.Title), deref(e.Format), e.Start, e.AniListID,
			e.AniDBID, e.KitsuID, jsonText(e.Flags), e.Target, bool01(e.Mainline), jsonText(e.TMDbMovie), string(eb)); err != nil {
			return 0, err
		}
		m := mappingOf(e)
		if m == nil {
			continue
		}
		reason := ""
		var cands []anime.MappingCandidate
		if e.Mapping != nil {
			reason, cands = e.Mapping.Reason, e.Mapping.Candidates
		}
		if m.status == anime.StatusConflict || m.status == anime.StatusHeuristic {
			if d, ok := decided[decisionKey{e.Key, m.status, jsonText(sortedEps(m.eps))}]; ok {
				if d.accepted {
					m.status, reason = anime.StatusOverride, fmt.Sprintf("accepted in review #%d", d.id)
					if c := d.choice; c != nil {
						m.series, m.eps, m.winner = c.SeriesID, c.Episodes, c.Source
						reason = fmt.Sprintf("%s candidate accepted in review #%d", c.Source, d.id)
					}
				} else {
					m.status, reason = anime.StatusRejected, fmt.Sprintf("rejected in review #%d", d.id)
				}
			} else {
				keep[e.Key+"\x00"+m.status] = true
				summary := fmt.Sprintf("%s %s: %s -> TVDB %d %s", e.Key, deref(e.Title), reason, m.series, rangeOf(m.eps))
				n, err := s.openItem(ctx, tx, slug, e.Key, m.status, summary,
					map[string]any{"winner": m.winner, "tvdb": m.series, "episodes": sortedEps(m.eps), "candidates": cands}, now)
				if err != nil {
					return 0, err
				}
				opened += n
			}
		}
		var series any
		if m.series != 0 {
			series = m.series
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO anime_mapping (franchise, entry_key, status, winner, tvdb_series, episodes,
			reason, candidates, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, slug, e.Key, m.status, m.winner,
			series, jsonText(nonNil(m.eps)), reason, jsonText(nonNilCands(cands)), now); err != nil {
			return 0, err
		}
		if p := prev[e.Key]; p != nil && !animemap.Importable(p.status) && animemap.Importable(m.status) {
			relink = append(relink, m.series)
		}
	}
	if err := s.queueRelink(ctx, tx, relink...); err != nil {
		return 0, err
	}
	// open items this build no longer raises, and held refreshes, are superseded
	rows, err := tx.QueryContext(ctx, `SELECT id, entry_key, kind FROM anime_review WHERE franchise = ? AND state = 'open'`, slug)
	if err != nil {
		return 0, err
	}
	var stale []int64
	for rows.Next() {
		var id int64
		var key, kind string
		if err := rows.Scan(&id, &key, &kind); err != nil {
			rows.Close()
			return 0, err
		}
		if !keep[key+"\x00"+kind] {
			stale = append(stale, id)
		}
	}
	rows.Close()
	for _, id := range stale {
		if _, err := tx.ExecContext(ctx, `UPDATE anime_review SET state = 'superseded', resolved_at = ? WHERE id = ?`, now, id); err != nil {
			return 0, err
		}
	}
	for _, o := range []struct {
		kind string
		rows []*anime.Row
	}{{"release", res.ReleaseOrder}, {"watch", res.WatchOrder}, {"story", append(append([]*anime.Row{}, res.StoryOrder.Order...), res.StoryOrder.Alternates...)}} {
		for i, r := range o.rows {
			rb, _ := json.Marshal(r)
			opt := r.Optional != nil && *r.Optional
			if _, err := tx.ExecContext(ctx, `INSERT INTO anime_order (franchise, kind, position, entry_key, part, role, optional, body)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, slug, o.kind, i+1, r.Key, deref(r.Part), r.Role, bool01(opt), string(rb)); err != nil {
				return 0, err
			}
		}
	}
	return opened, nil
}

type decisionKey struct{ key, kind, eps string }

type decision struct {
	id       int64
	accepted bool
	choice   *anime.MappingCandidate // the candidate accepted instead of the proposed winner
}

// decided lists the latest review decision per entry, kind and mapping.
func (s *Store) decided(ctx context.Context, tx *sql.Tx, slug string) (map[decisionKey]decision, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, entry_key, kind, detail, state, choice FROM anime_review
		WHERE franchise = ? AND state IN ('accepted', 'rejected') AND kind IN ('conflict', 'heuristic') ORDER BY id`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[decisionKey]decision{}
	for rows.Next() {
		var id int64
		var key, kind, detail, state, choice string
		if err := rows.Scan(&id, &key, &kind, &detail, &state, &choice); err != nil {
			return nil, err
		}
		var d struct {
			Episodes []anime.SE `json:"episodes"`
		}
		_ = json.Unmarshal([]byte(detail), &d)
		dec := decision{id: id, accepted: state == "accepted"}
		if choice != "" {
			var c anime.MappingCandidate
			if json.Unmarshal([]byte(choice), &c) == nil && c.SeriesID != 0 && len(c.Episodes) > 0 {
				dec.choice = &c
			}
		}
		out[decisionKey{key, kind, jsonText(sortedEps(d.Episodes))}] = dec
	}
	return out, rows.Err()
}

func sortedEps(x []anime.SE) []anime.SE {
	out := append([]anime.SE{}, x...)
	sort.Slice(out, func(i, j int) bool {
		return out[i][0] < out[j][0] || out[i][0] == out[j][0] && out[i][1] < out[j][1]
	})
	return out
}

func nonNil(x []anime.SE) []anime.SE {
	if x == nil {
		return []anime.SE{}
	}
	return x
}

func nonNilCands(x []anime.MappingCandidate) []anime.MappingCandidate {
	if x == nil {
		return []anime.MappingCandidate{}
	}
	return x
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Item is a review queue item.
type Item struct {
	ID        int64           `json:"id"`
	Franchise string          `json:"franchise"`
	EntryKey  string          `json:"entry_key"`
	Kind      string          `json:"kind"`
	Summary   string          `json:"summary"`
	Detail    json.RawMessage `json:"detail"`
	State     string          `json:"state"`
	Note      string          `json:"note,omitempty"`
	Created   time.Time       `json:"created"`
}

// Items lists review items in a state ("" for all), oldest first, optionally for one franchise.
func (s *Store) Items(ctx context.Context, state, franchise string) ([]Item, error) {
	q := `SELECT id, franchise, entry_key, kind, summary, detail, state, note, created_at FROM anime_review WHERE 1 = 1`
	var args []any
	if state != "" {
		q += ` AND state = ?`
		args = append(args, state)
	}
	if franchise != "" {
		q += ` AND franchise = ?`
		args = append(args, franchise)
	}
	rows, err := s.DB.Reader().QueryContext(ctx, q+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		var detail string
		var created int64
		if err := rows.Scan(&it.ID, &it.Franchise, &it.EntryKey, &it.Kind, &it.Summary, &detail, &it.State, &it.Note, &created); err != nil {
			return nil, err
		}
		it.Detail, it.Created = json.RawMessage(detail), store.FromMillis(created)
		out = append(out, it)
	}
	return out, rows.Err()
}

// ErrNotOpen is returned when resolving an item that is not open.
var ErrNotOpen = errors.New("animestore: review item is not open")

// Resolve accepts or rejects an open item. Accepting a held refresh applies the pending build (and closes the
// franchise's other held-refresh items); rejecting one drops the pending build. Accepting a conflict or heuristic
// mapping makes it an override until the candidates change and queues an anime_relink job for its series (files
// imported through Sonarr's numbering move to the episodes the mapping gives them); rejecting it keeps the range out of
// imports (its episodes keep Sonarr's numbering) and queues nothing.
func (s *Store) Resolve(ctx context.Context, id int64, accept bool, note string) error {
	now := store.Millis(s.now())
	return s.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		var slug, key, kind, state string
		if err := tx.QueryRowContext(ctx, `SELECT franchise, entry_key, kind, state FROM anime_review WHERE id = ?`, id).
			Scan(&slug, &key, &kind, &state); err != nil {
			return err
		}
		if state != "open" {
			return ErrNotOpen
		}
		newState := "rejected"
		if accept {
			newState = "accepted"
		}
		if kind == "refresh_shrink" || kind == "refresh_moves_linked" {
			if _, err := tx.ExecContext(ctx, `UPDATE anime_review SET state = ?, note = ?, resolved_at = ? WHERE franchise = ?
				AND state = 'open' AND kind IN ('refresh_shrink', 'refresh_moves_linked')`, newState, note, now, slug); err != nil {
				return err
			}
			var pending []byte
			if err := tx.QueryRowContext(ctx, `SELECT pending_result FROM anime_franchise WHERE slug = ?`, slug).Scan(&pending); err != nil {
				return err
			}
			if !accept || pending == nil {
				_, err := tx.ExecContext(ctx, `UPDATE anime_franchise SET pending_result = NULL, pending_source = '', pending_at = NULL
					WHERE slug = ?`, slug)
				return err
			}
			body, err := gunzip(pending)
			if err != nil {
				return err
			}
			var res anime.Result
			if err := json.Unmarshal(body, &res); err != nil {
				return fmt.Errorf("pending build: %w", err)
			}
			var source string
			_ = tx.QueryRowContext(ctx, `SELECT pending_source FROM anime_franchise WHERE slug = ?`, slug).Scan(&source)
			_, err = s.apply(ctx, tx, &res, body, source, now)
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE anime_review SET state = ?, note = ?, resolved_at = ? WHERE id = ?`,
			newState, note, now, id); err != nil {
			return err
		}
		status, reason := anime.StatusRejected, fmt.Sprintf("rejected in review #%d", id)
		if accept {
			status, reason = anime.StatusOverride, fmt.Sprintf("accepted in review #%d", id)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE anime_mapping SET status = ?, reason = ?, updated_at = ? WHERE franchise = ? AND entry_key = ?`,
			status, reason, now, slug, key); err != nil {
			return err
		}
		if !accept {
			return nil
		}
		var series int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(tvdb_series, 0) FROM anime_mapping WHERE franchise = ? AND entry_key = ?`,
			slug, key).Scan(&series); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return s.queueRelink(ctx, tx, series)
	})
}

// queueRelink queues an anime_relink job per TVDB series (one per series while queued or running), a minute out so
// accepts in a row join it. The library re-runs the import's resolver for the series' links and moves those whose
// episodes changed.
func (s *Store) queueRelink(ctx context.Context, tx *sql.Tx, series ...int) error {
	q := jobs.NewQueue(s.DB)
	q.SetClock(s.now)
	done := map[int]bool{}
	for _, id := range series {
		if id <= 0 || done[id] {
			continue
		}
		done[id] = true
		if _, _, err := q.EnqueueTx(ctx, tx, animemap.RelinkJobKind, animemap.RelinkPayload{TVDBID: id}, jobs.EnqueueOptions{
			RunAt: s.now().Add(animemap.RelinkDelay), DedupeKey: animemap.RelinkDedupeKey(id), MaxAttempts: 3}); err != nil {
			return err
		}
	}
	return nil
}

// Result loads a franchise's applied build.
func (s *Store) Result(ctx context.Context, slug string) (*anime.Result, error) {
	var b []byte
	if err := s.DB.Reader().QueryRowContext(ctx, `SELECT result FROM anime_franchise WHERE slug = ?`, slug).Scan(&b); err != nil {
		return nil, err
	}
	if b == nil {
		return nil, sql.ErrNoRows
	}
	body, err := gunzip(b)
	if err != nil {
		return nil, err
	}
	var res anime.Result
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	return &res, nil
}
