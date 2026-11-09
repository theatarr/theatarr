package animestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/store"
)

// ItemDetail is a conflict or heuristic item's detail: the proposed mapping and every candidate with its scores.
type ItemDetail struct {
	Winner     string                   `json:"winner"`
	TVDB       int                      `json:"tvdb"`
	Episodes   []anime.SE               `json:"episodes"`
	Candidates []anime.MappingCandidate `json:"candidates"`
}

// Detail decodes a mapping item's detail; ok is false for held-refresh items.
func (it *Item) MappingDetail() (ItemDetail, bool) {
	var d ItemDetail
	if it.Kind != "conflict" && it.Kind != "heuristic" {
		return d, false
	}
	return d, json.Unmarshal(it.Detail, &d) == nil
}

// Item loads one review item.
func (s *Store) Item(ctx context.Context, id int64) (*Item, error) {
	var it Item
	var detail string
	var created int64
	err := s.DB.Reader().QueryRowContext(ctx, `SELECT id, franchise, entry_key, kind, summary, detail, state, note, created_at
		FROM anime_review WHERE id = ?`, id).Scan(&it.ID, &it.Franchise, &it.EntryKey, &it.Kind, &it.Summary, &detail,
		&it.State, &it.Note, &created)
	if err != nil {
		return nil, err
	}
	it.Detail, it.Created = json.RawMessage(detail), store.FromMillis(created)
	return &it, nil
}

// ErrNoCandidate is returned when accepting a candidate an item does not have.
var ErrNoCandidate = errors.New("animestore: no such candidate")

// ResolveCandidate accepts candidate i (an index into the item's candidates) of an open conflict or heuristic item:
// the entry's mapping becomes that candidate's range with status override, and a later build proposing the same
// mapping takes it again instead of asking. Its series gets an anime_relink job, as for Resolve.
func (s *Store) ResolveCandidate(ctx context.Context, id int64, i int, note string) error {
	it, err := s.Item(ctx, id)
	if err != nil {
		return err
	}
	if it.State != "open" {
		return ErrNotOpen
	}
	d, ok := it.MappingDetail()
	if !ok || i < 0 || i >= len(d.Candidates) {
		return ErrNoCandidate
	}
	c := d.Candidates[i]
	if c.SeriesID == 0 || len(c.Episodes) == 0 || c.Dropped != "" {
		return fmt.Errorf("%w: candidate %d (%s) maps nothing it can keep", ErrNoCandidate, i, c.Source)
	}
	now := store.Millis(s.now())
	return s.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE anime_review SET state = 'accepted', note = ?, choice = ?, resolved_at = ?
			WHERE id = ? AND state = 'open'`, note, jsonText(c), now, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotOpen
		}
		_, err = tx.ExecContext(ctx, `UPDATE anime_mapping SET status = ?, winner = ?, tvdb_series = ?, episodes = ?, reason = ?,
			updated_at = ? WHERE franchise = ? AND entry_key = ?`, anime.StatusOverride, c.Source, c.SeriesID,
			jsonText(sortedEps(c.Episodes)), fmt.Sprintf("%s candidate accepted in review #%d", c.Source, id), now,
			it.Franchise, it.EntryKey)
		if err != nil {
			return err
		}
		return s.queueRelink(ctx, tx, c.SeriesID)
	})
}

// Snippet is the overrides YAML that pins one of an item's mappings for good (the UI shows the snippet to
// paste): one mappings entry per candidate, the proposed mapping first.
func (s *Store) Snippet(ctx context.Context, id int64) (string, error) {
	it, err := s.Item(ctx, id)
	if err != nil {
		return "", err
	}
	d, ok := it.MappingDetail()
	if !ok {
		return "", fmt.Errorf("animestore: item %d is a %s, not a mapping", id, it.Kind)
	}
	return SnippetFor(it.Franchise, it.EntryKey, id, d), nil
}

// SnippetFor renders the snippet for a mapping item's detail.
func SnippetFor(franchise, entry string, id int64, d ItemDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# overrides/%s.yaml: keep one of these under mappings:\nmappings:\n", franchise)
	line := func(prefix string, tvdb int, eps []anime.SE, why string) {
		fmt.Fprintf(&b, "%s- {entry: %q, tvdb: %d, episodes: %q, why: %q}\n", prefix, entry, tvdb, rangeLabel(eps), why)
	}
	seen := map[string]bool{}
	if d.TVDB != 0 && len(d.Episodes) > 0 {
		line("  ", d.TVDB, d.Episodes, fmt.Sprintf("review #%d: %s (proposed)", id, d.Winner))
		seen[fmt.Sprint(d.TVDB, rangeLabel(d.Episodes))] = true
	}
	for _, c := range d.Candidates {
		if c.SeriesID == 0 || len(c.Episodes) == 0 || seen[fmt.Sprint(c.SeriesID, rangeLabel(c.Episodes))] {
			continue
		}
		seen[fmt.Sprint(c.SeriesID, rangeLabel(c.Episodes))] = true
		why := fmt.Sprintf("review #%d: %s (score %.2f)", id, c.Source, c.Score)
		if c.Dropped != "" {
			line("  # ", c.SeriesID, c.Episodes, why+", dropped: "+c.Dropped)
			continue
		}
		line("  # ", c.SeriesID, c.Episodes, why)
	}
	return b.String()
}

// rangeLabel is the ranges() label the overrides' episodes field reads: "S00E05, S01E01-E03".
func rangeLabel(eps []anime.SE) string {
	eps = sortedEps(eps)
	var parts []string
	for i := 0; i < len(eps); {
		j := i
		for j+1 < len(eps) && eps[j+1][0] == eps[i][0] && eps[j+1][1] == eps[j][1]+1 {
			j++
		}
		if j > i {
			parts = append(parts, fmt.Sprintf("S%02dE%02d-E%02d", eps[i][0], eps[i][1], eps[j][1]))
		} else {
			parts = append(parts, eps[i].String())
		}
		i = j + 1
	}
	return strings.Join(parts, ", ")
}
