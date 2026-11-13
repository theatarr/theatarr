// Package ceiling records local source evidence and computes display-only ceilings. It never searches or grabs.
package ceiling

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// DB is a database or the transaction recording a decision.
type DB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

var tiers = []string{"unknown", "tv", "dvd", "web_1080", "bd", "web_2160", "uhd_bd"}

func rank(t string) int {
	for i, v := range tiers {
		if t == v {
			return i
		}
	}
	return 0
}

func ValidTier(t string) bool { return rank(t) > 0 }

func Label(t string) string {
	return map[string]string{"unknown": "Unknown", "tv": "TV", "dvd": "DVD", "web_1080": "1080p WEB", "bd": "BD", "web_2160": "2160p WEB", "uhd_bd": "4K disc"}[t]
}

// Tier maps an already parsed quality to a source, not a profile rank. Lower resolution BDs still prove a BD exists.
func Tier(quality string) string {
	q := strings.ToLower(quality)
	switch {
	case strings.Contains(q, "bluray") || strings.Contains(q, "remux"):
		if strings.Contains(q, "2160") {
			return "uhd_bd"
		}
		return "bd"
	case strings.Contains(q, "web") || strings.Contains(q, "2160"):
		if strings.Contains(q, "2160") {
			return "web_2160"
		}
		return "web_1080"
	case strings.Contains(q, "dvd"):
		return "dvd"
	case strings.Contains(q, "tv") || strings.Contains(q, "sdtv"):
		return "tv"
	}
	return "unknown"
}

// Coverage is a successful indexer answer. Cached, shared, truncated and episode-only answers cannot prove absence.
type Coverage struct {
	Indexer  string `json:"indexer"`
	Kind     string `json:"source_kind"`
	Season   int    `json:"season"`
	Complete bool   `json:"complete"`
}

func PositiveOnly(kind string) bool { return kind != "torznab" }

// Evidence is one aggregate sighting. Example never contains a link or a credential.
type Evidence struct {
	Source       string     `json:"source"`
	Kind         string     `json:"source_kind"`
	PositiveOnly bool       `json:"positive_only"`
	Tier         string     `json:"tier"`
	Example      string     `json:"example"`
	Quality      string     `json:"quality,omitempty"`
	Count        int        `json:"count"`
	FirstSeen    time.Time  `json:"first_seen_at"`
	LastSeen     time.Time  `json:"last_seen_at"`
	FetchedAt    *time.Time `json:"fetched_at,omitempty"`
}

type Search struct {
	At       time.Time  `json:"searched_at"`
	Source   string     `json:"source"`
	BestTier string     `json:"best_tier"`
	Indexers []Coverage `json:"indexers"`
}

type Override struct {
	Tier  string     `json:"tier"`
	Note  string     `json:"note"`
	By    *int64     `json:"by,omitempty"`
	At    time.Time  `json:"at"`
	Until *time.Time `json:"expires_at,omitempty"`
}

type Info struct {
	Tier        string     `json:"tier"`
	Label       string     `json:"label"`
	Status      string     `json:"status"`
	CheckedAt   time.Time  `json:"checked_at"`
	NextCheckAt time.Time  `json:"next_check_at"`
	Evidence    []Evidence `json:"evidence"`
	// Searches is the latest searches (at most maxInfoSearches, oldest first); Judge reads the whole kept history.
	Searches []Search  `json:"searches"`
	Override *Override `json:"override,omitempty"`
}

func safeExample(s string) string {
	l := strings.ToLower(s)
	for _, bad := range []string{"://", "magnet:", "apikey", "api_key", "passkey", "authkey", "token="} {
		if strings.Contains(l, bad) {
			return ""
		}
	}
	return s
}

// Sight records one tier without retaining torrents or links. Replayed imports use their original timestamps.
func Sight(ctx context.Context, db DB, titleID int64, season int, e Evidence) error {
	if !ValidTier(e.Tier) {
		return nil
	}
	if e.Kind == "tmdb" && e.FetchedAt == nil {
		return fmt.Errorf("ceiling: TMDb evidence needs fetched_at")
	}
	count := max(1, e.Count)
	var fetched any
	if e.FetchedAt != nil {
		fetched = e.FetchedAt.UnixMilli()
	}
	_, err := db.ExecContext(ctx, `INSERT INTO source_sighting
 (title_id,season,tier,source,source_kind,positive_only,example,quality,count,first_seen_at,last_seen_at,fetched_at)
 VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(title_id,season,tier,source,source_kind) DO UPDATE SET
 example = CASE WHEN excluded.last_seen_at >= last_seen_at THEN excluded.example ELSE example END,
 quality = CASE WHEN excluded.last_seen_at >= last_seen_at THEN excluded.quality ELSE quality END,
 count = count + CASE WHEN excluded.last_seen_at > last_seen_at THEN excluded.count ELSE 0 END,
 first_seen_at = MIN(first_seen_at,excluded.first_seen_at), last_seen_at = MAX(last_seen_at,excluded.last_seen_at),
 fetched_at = COALESCE(excluded.fetched_at,fetched_at)`, titleID, season, e.Tier, e.Source, e.Kind, e.PositiveOnly,
		safeExample(e.Example), e.Quality, count, e.FirstSeen.UnixMilli(), e.LastSeen.UnixMilli(), fetched)
	return err
}

// Record keeps every matched candidate, including rejected qualities, independently of the journal sample.
func Record(ctx context.Context, db DB, pol *policy.Policy, titleID int64, version, target, source string,
	ds []*decide.Decision, rels map[*decide.Decision]indexer.Release, coverage []Coverage, at time.Time) error {
	// RSS decides many items per pass: without a matched candidate there is nothing to record, so skip the write
	// transaction and the recompute.
	if source != "search" && !slices.ContainsFunc(ds, func(d *decide.Decision) bool {
		return d != nil && d.MatchType != decide.MatchNone
	}) {
		return nil
	}
	if sqlDB, ok := db.(*sql.DB); ok {
		tx, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := Record(ctx, tx, pol, titleID, version, target, source, ds, rels, coverage, at); err != nil {
			return err
		}
		return tx.Commit()
	}
	seasons := map[int]bool{}
	if source == "search" {
		if target == "movie" {
			seasons[-1] = true
		} else {
			var season int
			if _, err := fmt.Sscanf(strings.ToLower(target), "s%d", &season); err == nil {
				seasons[season] = true
			}
		}
	}
	best := map[int]string{}
	type sightKey struct {
		season             int
		tier, source, kind string
	}
	sightings := map[sightKey]Evidence{}
	for _, c := range coverage {
		seasons[c.Season] = true
	}
	for _, d := range ds {
		if d == nil || d.MatchType == decide.MatchNone {
			continue
		}
		ss := map[int]bool{}
		for _, ep := range d.Episodes {
			var s, n int
			if _, err := fmt.Sscanf(strings.ToLower(ep), "s%de%d", &s, &n); err == nil {
				ss[s] = true
			}
		}
		if len(ss) == 0 {
			if target == "" && len(seasons) > 0 {
				if len(seasons) == 1 {
					for season := range seasons {
						ss[season] = true
					}
				}
			} else if target == "movie" || target == "" {
				ss[-1] = true
			} else {
				var s int
				if _, err := fmt.Sscanf(strings.ToLower(target), "s%d", &s); err == nil {
					ss[s] = true
				}
			}
		}
		rel := rels[d]
		kind := "unknown"
		if pol != nil && pol.Indexers[rel.Indexer] != nil {
			kind = pol.Indexers[rel.Indexer].Kind
		}
		for s := range ss {
			tier := Tier(d.Quality)
			if rank(tier) > rank(best[s]) {
				best[s] = tier
			}
			k := sightKey{s, tier, source + ":" + rel.Indexer, kind}
			e := sightings[k]
			if e.Count == 0 {
				e = Evidence{Source: k.source, Kind: kind, PositiveOnly: PositiveOnly(kind), Tier: tier, Example: d.Release, Quality: d.Quality, FirstSeen: at, LastSeen: at}
			}
			e.Count++
			sightings[k] = e
			seasons[s] = true
		}
	}
	for k, e := range sightings {
		if err := Sight(ctx, db, titleID, k.season, e); err != nil {
			return err
		}
	}
	if source == "search" {
		for s := range seasons {
			tier := best[s]
			if tier == "" {
				tier = "unknown"
			}
			if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO ceiling_search (title_id,season,version_key,searched_at,source,best_tier)
    VALUES (?,?,?,?,?,?)`, titleID, s, version, at.UnixMilli(), source, tier); err != nil {
				return err
			}
			for _, c := range coverage {
				if c.Season != s {
					continue
				}
				if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO ceiling_search_indexer (search_id,indexer,source_kind,positive_only,complete)
     SELECT id,?,?,?,? FROM ceiling_search WHERE title_id=? AND season=? AND version_key=? AND searched_at=? AND source=?`,
					c.Indexer, c.Kind, PositiveOnly(c.Kind), c.Complete, titleID, s, version, at.UnixMilli(), source); err != nil {
					return err
				}
			}
			if err := pruneSearches(ctx, db, titleID, s, version, at); err != nil {
				return err
			}
		}
	}
	return Compute(ctx, db, pol, titleID, at)
}

// Search history retention (pruneSearches): every search of the last week, then one a day to 90 days, one a week
// to two years, none older. Judge needs successful searches spread over 90 days since the tier was first seen, so
// a bucket keeps its earliest successful search (else its earliest); a kept search can sit up to a week from the
// one it stands for, which only moves when "known" is reached by as much.
const (
	searchKeepAll   = 7 * 24 * time.Hour
	searchKeepDaily = 90 * 24 * time.Hour
	searchKeepMax   = 2 * 365 * 24 * time.Hour
)

// pruneSearches bounds one version's search history for a season (its indexer rows go by cascade).
func pruneSearches(ctx context.Context, db DB, titleID int64, season int, version string, now time.Time) error {
	n := now.UnixMilli()
	_, err := db.ExecContext(ctx, `DELETE FROM ceiling_search WHERE title_id=?1 AND season=?2 AND version_key=?3 AND searched_at<?4 AND (
 searched_at<?5 OR id NOT IN (SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY
  CASE WHEN searched_at>=?6 THEN searched_at/86400000 ELSE -1-searched_at/604800000 END
  ORDER BY EXISTS(SELECT 1 FROM ceiling_search_indexer i WHERE i.search_id=s.id AND i.source_kind='torznab' AND i.complete=1) DESC,
  searched_at, id) rn
  FROM ceiling_search s WHERE title_id=?1 AND season=?2 AND version_key=?3 AND searched_at<?4) WHERE rn=1))`,
		titleID, season, version, n-searchKeepAll.Milliseconds(), n-searchKeepMax.Milliseconds(), n-searchKeepDaily.Milliseconds())
	return err
}
