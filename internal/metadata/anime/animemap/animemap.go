// Package animemap reads the franchise mappings of one TVDB series for decisions and imports (a conflict range is
// not used: its episodes take Sonarr's numbering; docs/design/anime.md): which entries map into the series, with
// their names, status and TVDB episodes, and the series' alternate cuts. It reads the tables animestore writes (migrations 00017 and 00018) and nothing
// else, so internal/decide and internal/library can use it without the pipeline.
package animemap

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/theatarr/theatarr/internal/metadata/anime"
)

// Importable reports whether a mapping status may be used to import: auto, arbitrated, override and heuristic
// (a heuristic that review rejects becomes rejected); conflict and rejected may not (Sonarr's numbering applies).
func Importable(status string) bool {
	switch status {
	case anime.StatusAuto, anime.StatusArbitrated, anime.StatusOverride, anime.StatusHeuristic:
		return true
	}
	return false
}

// Entry is one franchise entry's mapping into the series.
type Entry struct {
	Franchise string
	Key       string
	Title     string
	Names     []string
	Status    string
	Winner    string
	Reason    string
	Episodes  []anime.SE // sorted
}

// Importable reports whether the entry's range may import automatically.
func (e *Entry) Importable() bool { return Importable(e.Status) }

// Regular is the entry's episodes outside season 0, in order: the entry's own episode n is Regular()[n-1].
func (e *Entry) Regular() []anime.SE {
	var out []anime.SE
	for _, se := range e.Episodes {
		if se[0] > 0 {
			out = append(out, se)
		}
	}
	return out
}

// Label is "al:123 (Title), conflict".
func (e *Entry) Label() string {
	s := e.Key
	if e.Title != "" {
		s += " (" + e.Title + ")"
	}
	return s + ", " + e.Status
}

// Series is every franchise mapping into one TVDB series.
type Series struct {
	TVDB    int
	Entries []*Entry
	AltCuts []*anime.AltCutMap
	byEp    map[anime.SE][]*Entry
}

// Load reads the mappings into a TVDB series. It returns nil (and no error) when no franchise maps into it.
func Load(ctx context.Context, db *sql.DB, tvdb int) (*Series, error) {
	if tvdb <= 0 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT m.franchise, m.entry_key, m.status, m.winner, m.reason, m.episodes,
		COALESCE(e.title, ''), COALESCE(e.body, '{}')
		FROM anime_mapping m LEFT JOIN anime_entry e ON e.franchise = m.franchise AND e.key = m.entry_key
		WHERE m.tvdb_series = ? ORDER BY m.franchise, m.entry_key`, tvdb)
	if err != nil {
		return nil, fmt.Errorf("animemap: %w", err)
	}
	defer rows.Close()
	s := &Series{TVDB: tvdb, byEp: map[anime.SE][]*Entry{}}
	for rows.Next() {
		e := &Entry{}
		var eps, body string
		if err := rows.Scan(&e.Franchise, &e.Key, &e.Status, &e.Winner, &e.Reason, &eps, &e.Title, &body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(eps), &e.Episodes); err != nil {
			return nil, fmt.Errorf("animemap: %s %s episodes: %w", e.Franchise, e.Key, err)
		}
		sort.Slice(e.Episodes, func(i, j int) bool {
			a, b := e.Episodes[i], e.Episodes[j]
			return a[0] < b[0] || a[0] == b[0] && a[1] < b[1]
		})
		var b struct {
			Title   *string  `json:"title"`
			TitleEn *string  `json:"title_en"`
			Names   []string `json:"names"`
		}
		_ = json.Unmarshal([]byte(body), &b)
		seen := map[string]bool{}
		for _, n := range append([]string{e.Title, deref(b.Title), deref(b.TitleEn)}, b.Names...) {
			if n != "" && !seen[n] {
				seen[n] = true
				e.Names = append(e.Names, n)
			}
		}
		s.Entries = append(s.Entries, e)
		for _, se := range e.Episodes {
			s.byEp[se] = append(s.byEp[se], e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	cr, err := db.QueryContext(ctx, `SELECT name, entry_key, match, episodes FROM anime_alt_cut WHERE tvdb_series = ?
		ORDER BY franchise, name`, tvdb)
	if err != nil {
		return nil, fmt.Errorf("animemap: %w", err)
	}
	defer cr.Close()
	for cr.Next() {
		c := &anime.AltCutMap{SeriesID: tvdb}
		var eps string
		if err := cr.Scan(&c.Name, &c.Entry, &c.Match, &eps); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(eps), &c.Episodes); err != nil {
			return nil, fmt.Errorf("animemap: alt cut %s: %w", c.Name, err)
		}
		s.AltCuts = append(s.AltCuts, c)
	}
	if err := cr.Err(); err != nil {
		return nil, err
	}
	if len(s.Entries) == 0 && len(s.AltCuts) == 0 {
		return nil, nil
	}
	return s, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Covering lists the entries whose range holds an episode.
func (s *Series) Covering(season, number int) []*Entry {
	if s == nil {
		return nil
	}
	return s.byEp[anime.SE{season, number}]
}

// Blocked reports whether an episode lies only in ranges whose mapping is not importable (conflict, rejected): at
// least one entry maps it and none of them is importable. The entry returned is the first such one. The episode
// still imports through Sonarr's numbering, like one no entry maps; decisions list it as a fallback.
func (s *Series) Blocked(season, number int) (*Entry, bool) {
	cov := s.Covering(season, number)
	if len(cov) == 0 {
		return nil, false
	}
	for _, e := range cov {
		if e.Importable() {
			return nil, false
		}
	}
	return cov[0], true
}

// New builds a Series from entries and cuts (tests, and callers that hold the mappings already).
func New(tvdb int, entries []*Entry, cuts []*anime.AltCutMap) *Series {
	s := &Series{TVDB: tvdb, Entries: entries, AltCuts: cuts, byEp: map[anime.SE][]*Entry{}}
	for _, e := range entries {
		for _, se := range e.Episodes {
			s.byEp[se] = append(s.byEp[se], e)
		}
	}
	return s
}

// Named reports whether a release names the series through the franchise: a cleaned entry name equals clean, or
// an alternate cut matches the release title. clean is the caller's title cleaner applied to the parsed title.
func (s *Series) Named(cleanTitle, releaseTitle string, cleaner func(string) string) bool {
	if s == nil {
		return false
	}
	for _, c := range s.AltCuts {
		if c.Matches(releaseTitle) {
			return true
		}
	}
	for _, e := range s.Entries {
		for _, n := range e.Names {
			if cleaner(n) == cleanTitle {
				return true
			}
		}
	}
	return false
}
