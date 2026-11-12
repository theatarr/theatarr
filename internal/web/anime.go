package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/metadata/anime/animestore"
	"github.com/theatarr/theatarr/internal/metadata/anime/autobuild"
)

// Anime metadata (the metadata review, the title page's order panel and numbering switcher; docs/design/anime.md): the franchises built, their three orders with each item's source, the numbering of each
// mapped episode, and the review queue, where a person accepts the proposed mapping or another candidate, rejects
// it, or copies the overrides snippet that pins it for good. Accepting and rejecting are the only writes.

func (w *Web) mountAnimeAPI(mux *http.ServeMux) {
	b := APIBase + "/anime"
	w.handle(mux, "GET "+b+"/franchises", SignedIn, auth.PermBrowse, w.apiAnimeFranchises)
	w.handle(mux, "GET "+b+"/franchises/{slug}", SignedIn, auth.PermBrowse, w.apiAnimeFranchise)
	w.handle(mux, "GET "+b+"/review", SignedIn, auth.PermAdmin, w.apiAnimeReview)
	w.handle(mux, "GET "+b+"/review/{id}/snippet", SignedIn, auth.PermAdmin, w.apiAnimeSnippet)
	w.handle(mux, "POST "+b+"/review/{id}/accept", SignedIn, auth.PermAdmin, w.apiAnimeResolve(true))
	w.handle(mux, "POST "+b+"/review/{id}/reject", SignedIn, auth.PermAdmin, w.apiAnimeResolve(false))
	w.handle(mux, "GET "+b+"/playlists", SignedIn, auth.PermBrowse, w.apiAnimePlaylists)
	w.mountAnimeBuildAPI(mux)
}

// AnimeFranchise is a built franchise in the list.
type AnimeFranchise struct {
	Slug       string         `json:"slug"`
	Name       string         `json:"name"`
	Source     string         `json:"source"`
	BuiltAt    *time.Time     `json:"built_at"`
	Pending    bool           `json:"pending"` // a refresh is held for review
	Entries    int            `json:"entries"`
	Statuses   map[string]int `json:"statuses"`
	OpenReview int            `json:"open_review"`
	// NextRefresh is when automatic builds rebuild it (built_at + metadata.anime.refresh_interval); null while they
	// are off or a refresh waits for review.
	NextRefresh *time.Time `json:"next_refresh"`
	// Job is the franchise's latest anime_build job (queued, running, done, failed); null when none is kept.
	Job *autobuild.Job `json:"job"`
}

// AnimeOrderItem is one row of an order, with where its place comes from.
type AnimeOrderItem struct {
	N           *int   `json:"n"`
	Key         string `json:"key"`
	Title       string `json:"title"`
	Format      string `json:"format,omitempty"`
	Date        string `json:"date,omitempty"`
	TVDB        string `json:"tvdb,omitempty"`
	TMDb        []int  `json:"tmdb,omitempty"`
	Part        string `json:"part,omitempty"`
	Role        string `json:"role,omitempty"`
	Optional    bool   `json:"optional"`
	Route       string `json:"route,omitempty"`
	AlternateOf string `json:"alternate_of,omitempty"`
	AltCut      string `json:"alt_cut,omitempty"`
	Mapping     string `json:"mapping,omitempty"` // the entry's mapping status
	Source      string `json:"source"`            // why the item is here
	Override    bool   `json:"override"`          // placed (or mapped) by an override or a review decision
}

// AnimeOrder is one of the three orders.
type AnimeOrder struct {
	Kind       string           `json:"kind"`
	Items      []AnimeOrderItem `json:"items"`
	Alternates []AnimeOrderItem `json:"alternates"`
}

// AnimeMapping is an entry's mapping decision.
type AnimeMapping struct {
	Entry      string                   `json:"entry"`
	Title      string                   `json:"title"`
	Status     string                   `json:"status"`
	Importable bool                     `json:"importable"`
	Winner     string                   `json:"winner"`
	TVDB       int                      `json:"tvdb,omitempty"`
	Range      string                   `json:"range,omitempty"`
	Reason     string                   `json:"reason"`
	Candidates []anime.MappingCandidate `json:"candidates"`
}

// AnimeNumber is one mapped episode in every numbering we know (the numbering switcher).
type AnimeNumber struct {
	TVDB        int    `json:"tvdb"`
	Episode     string `json:"episode"` // TVDB S01E02
	Absolute    *int   `json:"absolute"`
	Entry       string `json:"entry"` // the AniList (or AniDB, Kitsu) entry
	EntryTitle  string `json:"entry_title"`
	EntryNumber string `json:"entry_number"` // the entry's own episode number; "S" + n for its specials
	Status      string `json:"status"`
	Source      string `json:"source"` // the mapping's winner: scudlee, anibridge, heuristic, override, ...
	Importable  bool   `json:"importable"`
}

// AnimeFranchiseDetail is one franchise: its orders, mappings, numbering, alternate cuts and playlists.
type AnimeFranchiseDetail struct {
	AnimeFranchise
	Orders    []AnimeOrder       `json:"orders"`
	Routes    anime.RouteResult  `json:"routes"`
	AltCuts   []*anime.AltCutMap `json:"alt_cuts"`
	Mappings  []AnimeMapping     `json:"mappings"`
	Numbering []AnimeNumber      `json:"numbering"`
	Playlists []AnimePlaylist    `json:"playlists"`
}

// AnimePlaylist is a playlist row (plex_playlist): the last plan or sync.
type AnimePlaylist struct {
	Franchise string          `json:"franchise"`
	Order     string          `json:"order"`
	Route     string          `json:"route,omitempty"`
	Name      string          `json:"name"`
	RatingKey string          `json:"rating_key,omitempty"`
	Mode      string          `json:"mode"`
	Detail    string          `json:"detail,omitempty"`
	Items     int             `json:"items"`
	Plan      json.RawMessage `json:"plan,omitempty"`
	SyncedAt  *time.Time      `json:"synced_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// AnimeReviewItem is a review item with its decoded mapping detail and the overrides snippet, plus what the review
// card shows: the franchise name, the AniList entry, the options with sample episodes (conflict, heuristic) and,
// for a held refresh, what it changes.
type AnimeReviewItem struct {
	animestore.Item
	Title         string                 `json:"title,omitempty"`
	Mapping       *animestore.ItemDetail `json:"mapping,omitempty"`
	Snippet       string                 `json:"snippet,omitempty"`
	FranchiseName string                 `json:"franchise_name,omitempty"`
	Entry         *AnimeReviewEntry      `json:"entry,omitempty"`
	Options       []AnimeReviewOption    `json:"options,omitempty"`
	Refresh       *AnimeReviewRefresh    `json:"refresh,omitempty"`
}

// AnimeReviewEntry is the AniList (or AniDB) entry a review item is about.
type AnimeReviewEntry struct {
	Key       string `json:"key"`
	Title     string `json:"title,omitempty"`
	TitleEn   string `json:"title_en,omitempty"`
	Format    string `json:"format,omitempty"`
	Start     string `json:"start,omitempty"`
	End       string `json:"end,omitempty"`
	Episodes  int    `json:"episodes,omitempty"`
	AniListID int    `json:"anilist_id,omitempty"`
	AniDBID   int    `json:"anidb_id,omitempty"`
}

// AnimeReviewSeries is a TVDB series, with its Theatarr title when there is one.
type AnimeReviewSeries struct {
	TVDB    int    `json:"tvdb"`
	TitleID int64  `json:"title_id,omitempty"`
	Name    string `json:"name,omitempty"`
}

// AnimeReviewEpisode is a sample TVDB episode; name and air date are empty when Theatarr's episode list lacks them.
type AnimeReviewEpisode struct {
	Season  int    `json:"season"`
	Number  int    `json:"number"`
	Name    string `json:"name"`
	AirDate string `json:"air_date"`
}

// AnimeReviewOption is one mapping a review offers: the proposed one, or a candidate (index into
// mapping.candidates).
type AnimeReviewOption struct {
	Candidate *int                 `json:"candidate,omitempty"`
	Proposed  bool                 `json:"proposed"`
	Source    string               `json:"source,omitempty"`
	Series    AnimeReviewSeries    `json:"series"`
	Episodes  []anime.SE           `json:"episodes"`
	Count     int                  `json:"count"`
	Samples   []AnimeReviewEpisode `json:"samples"`
	Dropped   string               `json:"dropped,omitempty"`
}

// AnimeReviewRefresh is what a held refresh changes. Shrink: series, before, after and lost. Moves: from, to (absent:
// maps to nothing) and with_files. Items counts the franchise's open held-refresh items one answer settles.
type AnimeReviewRefresh struct {
	Series    *AnimeReviewSeries   `json:"series,omitempty"`
	Before    *int                 `json:"before,omitempty"`
	After     *int                 `json:"after,omitempty"`
	Lost      []AnimeReviewEpisode `json:"lost,omitempty"`
	From      *AnimeReviewOption   `json:"from,omitempty"`
	To        *AnimeReviewOption   `json:"to,omitempty"`
	WithFiles int                  `json:"with_files,omitempty"`
	Items     int                  `json:"items"`
}

func (w *Web) animeStore() *animestore.Store { return &animestore.Store{DB: w.st, Now: w.now} }

func (w *Web) listFranchises(ctx context.Context) ([]AnimeFranchise, error) {
	db := w.st.Reader()
	var out []AnimeFranchise
	err := scanAll(ctx, db, `SELECT f.slug, f.name, f.source, f.built_at, f.pending_result IS NOT NULL,
		(SELECT count(*) FROM anime_entry e WHERE e.franchise = f.slug),
		(SELECT count(*) FROM anime_review r WHERE r.franchise = f.slug AND r.state = 'open')
		FROM anime_franchise f ORDER BY f.name`, nil, func(r *sql.Rows) error {
		var f AnimeFranchise
		var built sql.NullInt64
		if err := r.Scan(&f.Slug, &f.Name, &f.Source, &built, &f.Pending, &f.Entries, &f.OpenReview); err != nil {
			return err
		}
		if built.Valid {
			t := ts(built.Int64)
			f.BuiltAt = &t
		}
		f.Statuses = map[string]int{}
		out = append(out, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	idx := map[string]*AnimeFranchise{}
	for i := range out {
		idx[out[i].Slug] = &out[i]
	}
	err = scanAll(ctx, db, `SELECT franchise, status, count(*) FROM anime_mapping GROUP BY franchise, status`, nil,
		func(r *sql.Rows) error {
			var slug, status string
			var n int
			if err := r.Scan(&slug, &status, &n); err != nil {
				return err
			}
			if f := idx[slug]; f != nil {
				f.Statuses[status] = n
			}
			return nil
		})
	if out == nil {
		out = []AnimeFranchise{}
	}
	if err != nil {
		return nil, err
	}
	return out, w.annotateBuilds(ctx, out)
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func orderItems(rows []*anime.Row, kind string, maps map[string]AnimeMapping, alternates bool) []AnimeOrderItem {
	out := []AnimeOrderItem{}
	for _, r := range rows {
		it := AnimeOrderItem{N: r.N, Key: r.Key, Title: str(r.Title), Format: str(r.Format), Date: str(r.Date),
			TVDB: str(r.TVDB), TMDb: r.TMDb, Part: str(r.Part), Role: r.Role, Optional: r.Optional != nil && *r.Optional,
			Route: r.Route, AlternateOf: r.AlternateOf, AltCut: r.AltCut}
		if it.Title == "" {
			it.Title = r.Key
		}
		if m, ok := maps[r.Key]; ok {
			it.Mapping = m.Status
		}
		var src []string
		switch {
		case r.MovedByOverride != "":
			src, it.Override = append(src, "override: moved ("+r.MovedByOverride+")"), true
		case r.Role == "route" || (!alternates && r.Route != "" && kind != "release"):
			src, it.Override = append(src, "override: route "+r.Route), true
		case kind == "release":
			src = append(src, "release date")
			if r.Route != "" {
				src, it.Override = append(src, "override: route "+r.Route), true
			}
		case r.PlacedBy != "":
			src = append(src, r.PlacedBy)
		case r.Role == "alternate":
			src = append(src, "alternate of "+r.AlternateOf)
		case kind == "story":
			src = append(src, "AniList chain")
		default:
			src = append(src, "release date")
		}
		if r.AltCut != "" {
			src, it.Override = append(src, "override: alt cut "+r.AltCut+" → TVDB "+r.AltCutTVDB), true
		}
		if r.TVDBPlacesAfter != "" {
			src = append(src, "TVDB places it after "+r.TVDBPlacesAfter)
		}
		if r.TVDBNote != "" {
			src = append(src, r.TVDBNote)
		}
		if it.Mapping == anime.StatusOverride {
			it.Override = true
		}
		it.Source = strings.Join(src, "; ")
		out = append(out, it)
	}
	return out
}

var errNoFranchise = errors.New("no such franchise")

func (w *Web) getFranchise(ctx context.Context, slug string, onlyTVDB int, seeFiles bool) (*AnimeFranchiseDetail, error) {
	all, err := w.listFranchises(ctx)
	if err != nil {
		return nil, err
	}
	d := &AnimeFranchiseDetail{}
	found := false
	for _, f := range all {
		if f.Slug == slug {
			d.AnimeFranchise, found = f, true
		}
	}
	if !found {
		return nil, errNoFranchise
	}
	res, err := w.animeStore().Result(ctx, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoFranchise
	}
	if err != nil {
		return nil, err
	}
	db := w.st.Reader()
	maps := map[string]AnimeMapping{}
	var mapEps = map[string][]anime.SE{}
	err = scanAll(ctx, db, `SELECT m.entry_key, COALESCE(e.title, ''), m.status, m.winner, COALESCE(m.tvdb_series, 0),
		m.episodes, m.reason, m.candidates FROM anime_mapping m
		LEFT JOIN anime_entry e ON e.franchise = m.franchise AND e.key = m.entry_key
		WHERE m.franchise = ? ORDER BY m.entry_key`, []any{slug}, func(r *sql.Rows) error {
		var m AnimeMapping
		var eps, cands string
		if err := r.Scan(&m.Entry, &m.Title, &m.Status, &m.Winner, &m.TVDB, &eps, &m.Reason, &cands); err != nil {
			return err
		}
		var se []anime.SE
		_ = json.Unmarshal([]byte(eps), &se)
		_ = json.Unmarshal([]byte(cands), &m.Candidates)
		if m.Candidates == nil {
			m.Candidates = []anime.MappingCandidate{}
		}
		sortSE(se)
		m.Range, m.Importable = rangeText(se), animemap.Importable(m.Status)
		maps[m.Entry], mapEps[m.Entry] = m, se
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, k := range sortedKeys(maps) {
		d.Mappings = append(d.Mappings, maps[k])
	}
	var wMain, wAlt []*anime.Row
	for _, r := range res.WatchOrder {
		if r.Role == "alternate" {
			wAlt = append(wAlt, r)
		} else {
			wMain = append(wMain, r)
		}
	}
	d.Orders = []AnimeOrder{
		{Kind: "release", Items: orderItems(res.ReleaseOrder, "release", maps, false), Alternates: []AnimeOrderItem{}},
		{Kind: "watch", Items: orderItems(wMain, "watch", maps, false), Alternates: orderItems(wAlt, "watch", maps, true)},
	}
	if so := res.StoryOrder; so != nil {
		d.Orders = append(d.Orders, AnimeOrder{Kind: "story", Items: orderItems(so.Order, "story", maps, false),
			Alternates: orderItems(so.Alternates, "story", maps, true)})
		d.Routes = so.Routes
	}
	d.AltCuts = res.AltCuts
	if d.AltCuts == nil {
		d.AltCuts = []*anime.AltCutMap{}
	}
	if d.Numbering, err = w.numbering(ctx, maps, mapEps, onlyTVDB); err != nil {
		return nil, err
	}
	if d.Playlists, err = w.animePlaylists(ctx, slug, seeFiles); err != nil {
		return nil, err
	}
	if d.Mappings == nil {
		d.Mappings = []AnimeMapping{}
	}
	return d, nil
}

func sortSE(x []anime.SE) {
	sort.Slice(x, func(i, j int) bool { return x[i][0] < x[j][0] || x[i][0] == x[j][0] && x[i][1] < x[j][1] })
}

// rangeText is "S01E01-E12, S00E03".
func rangeText(eps []anime.SE) string {
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

// numbering lists every mapped episode, in TVDB order, with its absolute number (as the imported arr has it) and
// its number inside the entry.
func (w *Web) numbering(ctx context.Context, maps map[string]AnimeMapping, eps map[string][]anime.SE, onlyTVDB int) ([]AnimeNumber, error) {
	out := []AnimeNumber{}
	abs := map[int]map[anime.SE]int{}
	for _, k := range sortedKeys(maps) {
		m := maps[k]
		if m.TVDB == 0 || onlyTVDB != 0 && m.TVDB != onlyTVDB {
			continue
		}
		if _, ok := abs[m.TVDB]; !ok {
			a := map[anime.SE]int{}
			err := scanAll(ctx, w.st.Reader(), `SELECT e.season, e.number, min(n.number) FROM episode e
				JOIN title t ON t.id = e.title_id JOIN episode_number n ON n.episode_id = e.id AND n.scheme = 'absolute_tvdb'
				WHERE t.kind = 'series' AND t.tvdb_id = ? GROUP BY e.id`, []any{m.TVDB}, func(r *sql.Rows) error {
				var se anime.SE
				var n int
				if err := r.Scan(&se[0], &se[1], &n); err != nil {
					return err
				}
				a[se] = n
				return nil
			})
			if err != nil {
				return nil, err
			}
			abs[m.TVDB] = a
		}
		reg, sp := 0, 0
		for _, se := range eps[k] {
			n := AnimeNumber{TVDB: m.TVDB, Episode: se.String(), Entry: m.Entry, EntryTitle: m.Title, Status: m.Status,
				Source: m.Winner, Importable: m.Importable}
			if se[0] > 0 {
				reg++
				n.EntryNumber = strconv.Itoa(reg)
			} else {
				sp++
				n.EntryNumber = "S" + strconv.Itoa(sp)
			}
			if a, ok := abs[m.TVDB][se]; ok {
				n.Absolute = &a
			}
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.TVDB != b.TVDB {
			return a.TVDB < b.TVDB
		}
		return a.Episode < b.Episode
	})
	return out, nil
}

// animePlaylists are the stored playlists (one franchise's with their plans). Without see_files a plan keeps the
// watch order but not the files (e2esec 7): each item's path, file source and Plex rating key are left out.
func (w *Web) animePlaylists(ctx context.Context, slug string, seeFiles bool) ([]AnimePlaylist, error) {
	q := `SELECT franchise, order_kind, route, name, rating_key, items, plan, mode, detail, synced_at, updated_at
		FROM plex_playlist`
	var args []any
	if slug != "" {
		q += ` WHERE franchise = ?`
		args = append(args, slug)
	}
	out := []AnimePlaylist{}
	err := scanAll(ctx, w.st.Reader(), q+` ORDER BY franchise, order_kind, route`, args, func(r *sql.Rows) error {
		var p AnimePlaylist
		var items, plan string
		var synced sql.NullInt64
		var updated int64
		if err := r.Scan(&p.Franchise, &p.Order, &p.Route, &p.Name, &p.RatingKey, &items, &plan, &p.Mode, &p.Detail,
			&synced, &updated); err != nil {
			return err
		}
		var planned []struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(plan), &planned)
		for _, it := range planned {
			if it.Path != "" {
				p.Items++
			}
		}
		if slug != "" {
			p.Plan = json.RawMessage(plan)
			if !seeFiles {
				p.Plan = planWithoutFiles(plan)
			}
		}
		if synced.Valid {
			t := ts(synced.Int64)
			p.SyncedAt = &t
		}
		p.UpdatedAt = ts(updated)
		out = append(out, p)
		return nil
	})
	return out, err
}

// planWithoutFiles is a stored playlist plan without its items' file details (path, from, rating_key).
func planWithoutFiles(plan string) json.RawMessage {
	var items []map[string]json.RawMessage
	if json.Unmarshal([]byte(plan), &items) != nil {
		return json.RawMessage("[]")
	}
	for _, it := range items {
		delete(it, "path")
		delete(it, "from")
		delete(it, "rating_key")
	}
	b, err := json.Marshal(items)
	if err != nil {
		return json.RawMessage("[]")
	}
	return b
}

func (w *Web) reviewItems(ctx context.Context, state, franchise string) ([]AnimeReviewItem, error) {
	items, err := w.animeStore().Items(ctx, state, franchise)
	if err != nil {
		return nil, err
	}
	titles := map[string]string{}
	_ = scanAll(ctx, w.st.Reader(), `SELECT franchise || char(0) || key, title FROM anime_entry`, nil, func(r *sql.Rows) error {
		var k, t string
		if err := r.Scan(&k, &t); err != nil {
			return err
		}
		titles[k] = t
		return nil
	})
	out := []AnimeReviewItem{}
	en := &reviewEnricher{w: w, db: w.st.Reader(), names: map[string]string{}, series: map[int]AnimeReviewSeries{},
		eps: map[int64]map[anime.SE]AnimeReviewEpisode{}, held: map[string]map[string]animestore.Mapping{}, open: map[string]int{}}
	for _, it := range items {
		ri := AnimeReviewItem{Item: it, Title: titles[it.Franchise+"\x00"+it.EntryKey]}
		if d, ok := it.MappingDetail(); ok {
			if d.Candidates == nil {
				d.Candidates = []anime.MappingCandidate{}
			}
			ri.Mapping = &d
			ri.Snippet = animestore.SnippetFor(it.Franchise, it.EntryKey, it.ID, d)
		}
		if err := en.enrich(ctx, &ri); err != nil {
			w.log.Warn("anime review: card details", "item", it.ID, "err", err)
		}
		out = append(out, ri)
	}
	return out, nil
}

// reviewEnricher fills the review card's fields, loading each franchise, series, title's episodes and held build once
// per request.
type reviewEnricher struct {
	w      *Web
	db     *sql.DB
	names  map[string]string
	series map[int]AnimeReviewSeries
	eps    map[int64]map[anime.SE]AnimeReviewEpisode
	held   map[string]map[string]animestore.Mapping
	open   map[string]int
}

// enrich sets ri's card fields; on an error it leaves them all out.
func (en *reviewEnricher) enrich(ctx context.Context, ri *AnimeReviewItem) error {
	out := *ri
	name, ok := en.names[ri.Franchise]
	if !ok {
		if err := en.db.QueryRowContext(ctx, `SELECT name FROM anime_franchise WHERE slug = ?`, ri.Franchise).Scan(&name); err != nil &&
			!errors.Is(err, sql.ErrNoRows) {
			return err
		}
		en.names[ri.Franchise] = name
	}
	out.FranchiseName = name
	var body string
	err := en.db.QueryRowContext(ctx, `SELECT body FROM anime_entry WHERE franchise = ? AND key = ?`, ri.Franchise, ri.EntryKey).Scan(&body)
	switch {
	case err == nil:
		var e anime.Entry
		if err := json.Unmarshal([]byte(body), &e); err != nil {
			return fmt.Errorf("entry %s: %w", ri.EntryKey, err)
		}
		out.Entry = &AnimeReviewEntry{Key: ri.EntryKey, Title: derefStr(e.Title), TitleEn: derefStr(e.TitleEn), Format: derefStr(e.Format),
			Start: derefStr(e.Start), End: derefStr(e.End), Episodes: derefInt(e.Episodes), AniListID: derefInt(e.AniListID), AniDBID: derefInt(e.AniDBID)}
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if d := ri.Mapping; d != nil {
		if d.TVDB != 0 && len(d.Episodes) > 0 {
			o, err := en.option(ctx, d.TVDB, d.Episodes)
			if err != nil {
				return err
			}
			o.Proposed, o.Source = true, d.Winner
			out.Options = append(out.Options, o)
		}
		proposed := fmt.Sprint(d.TVDB, sortedSE(d.Episodes))
		for i, c := range d.Candidates {
			if c.SeriesID == 0 || len(c.Episodes) == 0 || fmt.Sprint(c.SeriesID, sortedSE(c.Episodes)) == proposed {
				continue
			}
			o, err := en.option(ctx, c.SeriesID, c.Episodes)
			if err != nil {
				return err
			}
			o.Candidate, o.Source, o.Dropped = &i, c.Source, c.Dropped
			out.Options = append(out.Options, o)
		}
	}
	if ri.Kind == "refresh_shrink" || ri.Kind == "refresh_moves_linked" {
		r, err := en.refresh(ctx, ri)
		if err != nil {
			return err
		}
		out.Refresh = r
	}
	*ri = out
	return nil
}

func (en *reviewEnricher) refresh(ctx context.Context, ri *AnimeReviewItem) (*AnimeReviewRefresh, error) {
	held, ok := en.held[ri.Franchise]
	if !ok {
		var err error
		if held, err = en.w.animeStore().HeldMappings(ctx, ri.Franchise); err != nil {
			return nil, err
		}
		en.held[ri.Franchise] = held
	}
	items, ok := en.open[ri.Franchise]
	if !ok {
		if err := en.db.QueryRowContext(ctx, `SELECT count(*) FROM anime_review WHERE franchise = ? AND state = 'open'
			AND kind IN ('refresh_shrink', 'refresh_moves_linked')`, ri.Franchise).Scan(&items); err != nil {
			return nil, err
		}
		en.open[ri.Franchise] = items
	}
	r := &AnimeReviewRefresh{Items: items}
	if ri.Kind == "refresh_shrink" {
		var d struct {
			TVDB   int `json:"tvdb"`
			Before int `json:"before"`
			After  int `json:"after"`
		}
		if err := json.Unmarshal(ri.Detail, &d); err != nil {
			return nil, err
		}
		s, err := en.seriesOf(ctx, d.TVDB)
		if err != nil {
			return nil, err
		}
		r.Series, r.Before, r.After = &s, &d.Before, &d.After
		kept := map[anime.SE]bool{}
		for _, m := range held {
			if m.TVDB == d.TVDB {
				for _, se := range m.Episodes {
					kept[se] = true
				}
			}
		}
		var now []anime.SE
		err = scanAll(ctx, en.db, `SELECT episodes FROM anime_mapping WHERE franchise = ? AND tvdb_series = ?`,
			[]any{ri.Franchise, d.TVDB}, func(rows *sql.Rows) error {
				var b string
				if err := rows.Scan(&b); err != nil {
					return err
				}
				var eps []anime.SE
				_ = json.Unmarshal([]byte(b), &eps)
				now = append(now, eps...)
				return nil
			})
		if err != nil {
			return nil, err
		}
		var lost []anime.SE
		for _, se := range sortedSE(now) {
			if !kept[se] && len(lost) < 5 && (len(lost) == 0 || lost[len(lost)-1] != se) {
				lost = append(lost, se)
			}
		}
		if r.Lost, err = en.episodes(ctx, s, lost); err != nil {
			return nil, err
		}
		return r, nil
	}
	var d struct {
		Before    animestore.Mapping `json:"before"`
		WithFiles []anime.SE         `json:"with_files"`
	}
	if err := json.Unmarshal(ri.Detail, &d); err != nil {
		return nil, err
	}
	r.WithFiles = len(d.WithFiles)
	if d.Before.TVDB != 0 {
		o, err := en.option(ctx, d.Before.TVDB, d.Before.Episodes)
		if err != nil {
			return nil, err
		}
		r.From = &o
	}
	if m, ok := held[ri.EntryKey]; ok {
		o, err := en.option(ctx, m.TVDB, m.Episodes)
		if err != nil {
			return nil, err
		}
		r.To = &o
	}
	return r, nil
}

// option is a mapping with its series and sample episodes: the first two and the last of the range.
func (en *reviewEnricher) option(ctx context.Context, tvdb int, eps []anime.SE) (AnimeReviewOption, error) {
	s, err := en.seriesOf(ctx, tvdb)
	if err != nil {
		return AnimeReviewOption{}, err
	}
	eps = sortedSE(eps)
	pick := eps
	if len(eps) > 3 {
		pick = []anime.SE{eps[0], eps[1], eps[len(eps)-1]}
	}
	samples, err := en.episodes(ctx, s, pick)
	if err != nil {
		return AnimeReviewOption{}, err
	}
	return AnimeReviewOption{Series: s, Episodes: eps, Count: len(eps), Samples: samples}, nil
}

func (en *reviewEnricher) seriesOf(ctx context.Context, tvdb int) (AnimeReviewSeries, error) {
	if s, ok := en.series[tvdb]; ok {
		return s, nil
	}
	s := AnimeReviewSeries{TVDB: tvdb}
	err := en.db.QueryRowContext(ctx, `SELECT id, name FROM title WHERE kind = 'series' AND tvdb_id = ? ORDER BY id LIMIT 1`,
		tvdb).Scan(&s.TitleID, &s.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return s, err
	}
	en.series[tvdb] = s
	return s, nil
}

// episodes names eps from the series' title's episode list (loaded once per title).
func (en *reviewEnricher) episodes(ctx context.Context, s AnimeReviewSeries, eps []anime.SE) ([]AnimeReviewEpisode, error) {
	known := en.eps[s.TitleID]
	if known == nil && s.TitleID != 0 {
		known = map[anime.SE]AnimeReviewEpisode{}
		err := scanAll(ctx, en.db, `SELECT season, number, name, COALESCE(air_date, '') FROM episode WHERE title_id = ?`,
			[]any{s.TitleID}, func(rows *sql.Rows) error {
				var e AnimeReviewEpisode
				if err := rows.Scan(&e.Season, &e.Number, &e.Name, &e.AirDate); err != nil {
					return err
				}
				known[anime.SE{e.Season, e.Number}] = e
				return nil
			})
		if err != nil {
			return nil, err
		}
		en.eps[s.TitleID] = known
	}
	out := []AnimeReviewEpisode{}
	for _, se := range eps {
		e, ok := known[se]
		if !ok {
			e = AnimeReviewEpisode{Season: se[0], Number: se[1]}
		}
		out = append(out, e)
	}
	return out, nil
}

func sortedSE(x []anime.SE) []anime.SE {
	out := append([]anime.SE{}, x...)
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] || out[i][0] == out[j][0] && out[i][1] < out[j][1] })
	return out
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func reviewState(r *http.Request) (string, error) {
	s := r.URL.Query().Get("state")
	switch s {
	case "":
		return "open", nil
	case "all":
		return "", nil
	case "open", "accepted", "rejected", "superseded":
		return s, nil
	}
	return "", errors.New("state must be open, accepted, rejected, superseded or all")
}

func (w *Web) apiAnimeFranchises(rw http.ResponseWriter, r *http.Request) {
	fs, err := w.listFranchises(r.Context())
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"items": fs})
}

func (w *Web) apiAnimeFranchise(rw http.ResponseWriter, r *http.Request) {
	d, err := w.getFranchise(r.Context(), r.PathValue("slug"), 0, w.permsOf(r).SeeFiles)
	if errors.Is(err, errNoFranchise) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, d)
}

func (w *Web) apiAnimeReview(rw http.ResponseWriter, r *http.Request) {
	state, err := reviewState(r)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	items, err := w.reviewItems(r.Context(), state, r.URL.Query().Get("franchise"))
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"items": items})
}

func (w *Web) apiAnimeSnippet(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	y, err := w.animeStore().Snippet(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]string{"yaml": y})
}

func (w *Web) apiAnimePlaylists(rw http.ResponseWriter, r *http.Request) {
	ps, err := w.animePlaylists(r.Context(), r.URL.Query().Get("franchise"), w.permsOf(r).SeeFiles)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"items": ps})
}

// resolveInput is an accept or reject: an optional candidate or hand-set mapping (accept only) and a note.
type resolveInput struct {
	Candidate *int           `json:"candidate"`
	Mapping   *manualMapping `json:"mapping"`
	Note      string         `json:"note"`
}

// manualMapping is a mapping set by hand: count episodes of one TVDB season from first.
type manualMapping struct {
	TVDB   int `json:"tvdb"`
	Season int `json:"season"`
	First  int `json:"first"`
	Count  int `json:"count"`
}

// check is the shape check; checkEpisodes then needs the store.
func (m *manualMapping) check() error {
	switch {
	case m.TVDB <= 0:
		return errors.New("mapping.tvdb must be a TVDB series id")
	case m.Season < 0:
		return errors.New("mapping.season must be 0 or more")
	case m.First < 1:
		return errors.New("mapping.first must be 1 or more")
	case m.Count < 1 || m.Count > 500:
		return errors.New("mapping.count must be 1 to 500")
	}
	return nil
}

func (m *manualMapping) episodes() []anime.SE {
	out := make([]anime.SE, 0, m.Count)
	for n := m.First; n < m.First+m.Count; n++ {
		out = append(out, anime.SE{m.Season, n})
	}
	return out
}

// checkEpisodes: when the series is a Theatarr title with episodes in that season, each mapped episode must be one.
func (w *Web) checkEpisodes(ctx context.Context, m *manualMapping) error {
	db := w.st.Reader()
	var id int64
	var name string
	err := db.QueryRowContext(ctx, `SELECT id, name FROM title WHERE kind = 'series' AND tvdb_id = ? ORDER BY id LIMIT 1`, m.TVDB).
		Scan(&id, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	have := map[int]bool{}
	err = scanAll(ctx, db, `SELECT number FROM episode WHERE title_id = ? AND season = ?`, []any{id, m.Season}, func(r *sql.Rows) error {
		var n int
		if err := r.Scan(&n); err != nil {
			return err
		}
		have[n] = true
		return nil
	})
	if err != nil || len(have) == 0 {
		return err
	}
	for n := m.First; n < m.First+m.Count; n++ {
		if !have[n] {
			return badMapping(fmt.Sprintf("Season %d has no episode %d in Theatarr's episode list for %s", m.Season, n, name))
		}
	}
	return nil
}

// badMapping is a hand-set mapping the episode list refuses (a 400).
type badMapping string

func (b badMapping) Error() string { return string(b) }

// resolve applies a decision and maps the store's errors to HTTP codes.
func (w *Web) resolve(ctx context.Context, id int64, accept bool, in resolveInput) (int, error) {
	s := w.animeStore()
	if _, err := s.Item(ctx, id); errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound, errors.New("no such review item")
	} else if err != nil {
		return http.StatusInternalServerError, err
	}
	var err error
	switch {
	case accept && in.Candidate != nil:
		err = s.ResolveCandidate(ctx, id, *in.Candidate, in.Note)
	case accept && in.Mapping != nil:
		if err = w.checkEpisodes(ctx, in.Mapping); err == nil {
			eps := in.Mapping.episodes()
			err = s.ResolveMapping(ctx, id, anime.MappingCandidate{Source: animestore.ManualSource, SeriesID: in.Mapping.TVDB,
				Episodes: eps, Range: animestore.RangeLabel(eps)}, in.Note)
		}
	default:
		err = s.Resolve(ctx, id, accept, in.Note)
	}
	switch {
	case err == nil:
		w.log.Info("anime review", "item", id, "accepted", accept, "candidate", in.Candidate, "mapping", in.Mapping)
		return http.StatusOK, nil
	case errors.As(err, new(badMapping)):
		return http.StatusBadRequest, err
	case errors.Is(err, animestore.ErrNotOpen):
		return http.StatusConflict, err
	case errors.Is(err, animestore.ErrNoCandidate):
		return http.StatusBadRequest, err
	}
	return http.StatusInternalServerError, err
}

func (w *Web) apiAnimeResolve(accept bool) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok {
			notFound(rw)
			return
		}
		var in resolveInput
		if r.ContentLength != 0 {
			if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 64<<10)).Decode(&in); err != nil {
				badRequest(rw, "want JSON: {\"candidate\": <index>, \"note\": \"...\"}")
				return
			}
		}
		if !accept && in.Candidate != nil {
			badRequest(rw, "a candidate is only for accept")
			return
		}
		if in.Mapping != nil {
			if !accept {
				badRequest(rw, "a mapping is only for accept")
				return
			}
			if in.Candidate != nil {
				badRequest(rw, "give a candidate or a mapping, not both")
				return
			}
			if err := in.Mapping.check(); err != nil {
				badRequest(rw, err.Error())
				return
			}
		}
		code, err := w.resolve(r.Context(), id, accept, in)
		if code == http.StatusInternalServerError {
			w.internal(rw, err)
			return
		}
		if err != nil {
			writeJSON(rw, code, apiError{Error: err.Error()})
			return
		}
		it, _ := w.animeStore().Item(r.Context(), id)
		writeJSON(rw, http.StatusOK, it)
	}
}
