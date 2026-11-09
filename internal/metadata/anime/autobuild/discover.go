package autobuild

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
	"golang.org/x/text/unicode/norm"

	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/store"
)

// Discovery outcomes (anime_discovery.outcome).
const (
	OutcomeQueued    = "queued"
	OutcomeBuilt     = "built"
	OutcomeCovered   = "covered"
	OutcomeNoMapping = "no_mapping"
	OutcomeFailed    = "failed"
)

// Franchise origins, kept in the franchise's config JSON ("origin").
const (
	OriginList   = "list"   // the shipped franchise list (<overrides_dir>/franchises.yaml)
	OriginAuto   = "auto"   // discovered from a TVDB series
	OriginManual = "manual" // an ad-hoc AniList seed from the UI, API or CLI
)

// Seed is what discovery decided for a TVDB series.
type Seed struct {
	// Outcome is OutcomeCovered, OutcomeNoMapping, or "" when Franchise should be built.
	Outcome string
	// CoveredBy is the franchise that already maps the series (OutcomeCovered).
	CoveredBy string
	Franchise anime.Franchise
	Origin    string
	// Candidates are the AniList ids the lists map the series to.
	Candidates []int
}

// LoadFranchises reads a franchise list (YAML or JSON): <overrides_dir>/franchises.yaml.
func LoadFranchises(path string) ([]anime.Franchise, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fs []anime.Franchise
	if err := yaml.Unmarshal(b, &fs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return fs, nil
}

// shippedFranchises reads <overridesDir>/franchises.yaml; no directory or no file is an empty list.
func shippedFranchises(overridesDir string) ([]anime.Franchise, error) {
	if overridesDir == "" {
		return nil, nil
	}
	fs, err := LoadFranchises(filepath.Join(overridesDir, "franchises.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return fs, err
}

// candidate is a ScudLee record of the series with its AniList id.
type candidate struct {
	rec     *anime.ScudLeeRec
	anilist int
}

// candidates maps the series' ScudLee records (AniDB ids) to AniList ids: Fribb, else manami.
func candidates(lists *anime.Lists, recs []*anime.ScudLeeRec) []candidate {
	var out []candidate
	seen := map[int]bool{}
	for _, r := range recs {
		id := 0
		if f := lists.FribbAniDB(r.AniDBID); f != nil && f.AniListID != nil {
			id = *f.AniListID
		} else if m := lists.Manami("anidb", r.AniDBID); m != nil && m.AniListID != nil {
			id = *m.AniListID
		}
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, candidate{rec: r, anilist: id})
		}
	}
	return out
}

// Discoverer turns a TVDB series into a franchise definition.
type Discoverer struct {
	Store   *store.Store
	Lists   *anime.Lists
	AniList anime.AniList
	// Shipped is the shipped franchise list.
	Shipped []anime.Franchise
	// index is ScudLee by TVDB id (built from Lists when nil).
	index map[int][]*anime.ScudLeeRec
}

// Discover decides the franchise for TVDB series tvdbID, named name:
//  1. covered when a franchise maps into the series, or has an entry for one of its AniList ids;
//  2. the AniList ids from ScudLee (by tvdbid) through Fribb (else manami); none is OutcomeNoMapping;
//  3. a shipped franchise whose seed is one of them;
//  4. else ad hoc: the candidate ScudLee puts at TVDB season 1 (or absolute) with offset 0, else the earliest by
//     AniList start date. The crawl walks prequels and sequels from any seed, so a later season still yields the line.
func (d *Discoverer) Discover(ctx context.Context, tvdbID int, name string) (*Seed, error) {
	if slug, err := mapsSeries(ctx, d.Store, tvdbID); err != nil || slug != "" {
		return &Seed{Outcome: OutcomeCovered, CoveredBy: slug}, err
	}
	idx := d.index
	if idx == nil {
		idx = parsed.index(d.Lists)
	}
	cands := candidates(d.Lists, idx[tvdbID])
	if len(cands) == 0 {
		return &Seed{Outcome: OutcomeNoMapping}, nil
	}
	ids := make([]int, len(cands))
	for i, c := range cands {
		ids[i] = c.anilist
	}
	if slug, err := hasEntry(ctx, d.Store, ids); err != nil || slug != "" {
		return &Seed{Outcome: OutcomeCovered, CoveredBy: slug, Candidates: ids}, err
	}
	for _, f := range d.Shipped {
		for _, id := range ids {
			if f.SeedAniList == id {
				exists, err := franchiseExists(ctx, d.Store, f.Slug)
				if err != nil {
					return nil, err
				}
				if exists {
					return &Seed{Outcome: OutcomeCovered, CoveredBy: f.Slug, Candidates: ids}, nil
				}
				return &Seed{Franchise: f, Origin: OriginList, Candidates: ids}, nil
			}
		}
	}
	seed, err := d.pickSeed(ctx, cands)
	if err != nil {
		return nil, err
	}
	slug, err := freeSlug(ctx, d.Store, Slugify(name), tvdbID)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = slug
	}
	return &Seed{Franchise: anime.Franchise{Slug: slug, Name: name, SeedAniList: seed}, Origin: OriginAuto, Candidates: ids}, nil
}

func (d *Discoverer) pickSeed(ctx context.Context, cands []candidate) (int, error) {
	first := 0
	for _, c := range cands {
		s := c.rec.TVDBSeason
		if s != nil && (*s == "1" || *s == "a") && c.rec.EpisodeOffset == 0 && (first == 0 || c.rec.AniDBID < first) {
			first = c.rec.AniDBID
		}
	}
	for _, c := range cands {
		if first != 0 && c.rec.AniDBID == first {
			return c.anilist, nil
		}
	}
	if len(cands) == 1 || d.AniList == nil {
		return cands[0].anilist, nil
	}
	ids := make([]int, len(cands))
	for i, c := range cands {
		ids[i] = c.anilist
	}
	media, err := d.AniList.Media(ctx, ids)
	if err != nil {
		return 0, fmt.Errorf("anilist start dates: %w", err)
	}
	start := map[int]string{}
	for _, m := range media {
		if m != nil {
			start[m.ID] = dateKey(m.StartDate)
		}
	}
	sort.SliceStable(ids, func(i, j int) bool {
		a, b := start[ids[i]], start[ids[j]]
		if a != b {
			return a < b
		}
		return ids[i] < ids[j]
	})
	return ids[0], nil
}

// dateKey sorts a fuzzy date; unknown parts sort after known ones, no year last.
func dateKey(d anime.FuzzyDate) string {
	part := func(p *int, w int) string {
		if p == nil || *p == 0 {
			return strings.Repeat("9", w)
		}
		return fmt.Sprintf("%0*d", w, *p)
	}
	return part(d.Year, 4) + part(d.Month, 2) + part(d.Day, 2)
}

// mapsSeries returns a franchise with a mapping into TVDB series tvdbID, or "".
func mapsSeries(ctx context.Context, st *store.Store, tvdbID int) (string, error) {
	var slug string
	err := st.Reader().QueryRowContext(ctx, `SELECT franchise FROM anime_mapping WHERE tvdb_series = ? ORDER BY franchise LIMIT 1`,
		tvdbID).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return slug, err
}

// hasEntry returns a franchise with an entry for one of the AniList ids, or "".
func hasEntry(ctx context.Context, st *store.Store, ids []int) (string, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	var slug string
	err := st.Reader().QueryRowContext(ctx, `SELECT franchise FROM anime_entry WHERE anilist_id IN (`+
		store.Placeholders(len(ids))+`) ORDER BY franchise LIMIT 1`, args...).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return slug, err
}

func franchiseExists(ctx context.Context, st *store.Store, slug string) (bool, error) {
	var n int
	err := st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM anime_franchise WHERE slug = ?`, slug).Scan(&n)
	return n > 0, err
}

// freeSlug is slug, or slug-<tvdbID> when another franchise has it.
func freeSlug(ctx context.Context, st *store.Store, slug string, tvdbID int) (string, error) {
	if slug == "" {
		return fmt.Sprintf("tvdb-%d", tvdbID), nil
	}
	taken, err := franchiseExists(ctx, st, slug)
	if err != nil || !taken {
		return slug, err
	}
	return fmt.Sprintf("%s-%d", slug, tvdbID), nil
}

// Slugify makes a franchise slug from a name: lower-case ASCII letters and digits joined by '-'.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFKD.String(name) {
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(unicode.ToLower(r))
			dash = false
		case unicode.Is(unicode.Mn, r) || r == '\'' || r == '’':
			// accents and apostrophes vanish: "Kimetsu no Yaiba" stays one word per word, "Re:Zero" becomes re-zero
		default:
			dash = true
		}
	}
	s := b.String()
	if len(s) > 60 {
		s = strings.TrimRight(s[:60], "-")
	}
	return s
}
