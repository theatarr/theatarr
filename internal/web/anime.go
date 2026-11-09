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

// AnimeReviewItem is a review item with its decoded mapping detail and the overrides snippet.
type AnimeReviewItem struct {
	animestore.Item
	Title   string                 `json:"title,omitempty"`
	Mapping *animestore.ItemDetail `json:"mapping,omitempty"`
	Snippet string                 `json:"snippet,omitempty"`
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
	return out, err
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
	for _, it := range items {
		ri := AnimeReviewItem{Item: it, Title: titles[it.Franchise+"\x00"+it.EntryKey]}
		if d, ok := it.MappingDetail(); ok {
			if d.Candidates == nil {
				d.Candidates = []anime.MappingCandidate{}
			}
			ri.Mapping = &d
			ri.Snippet = animestore.SnippetFor(it.Franchise, it.EntryKey, it.ID, d)
		}
		out = append(out, ri)
	}
	return out, nil
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

// resolveInput is an accept or reject: an optional candidate (accept only) and a note.
type resolveInput struct {
	Candidate *int   `json:"candidate"`
	Note      string `json:"note"`
}

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
	default:
		err = s.Resolve(ctx, id, accept, in.Note)
	}
	switch {
	case err == nil:
		w.log.Info("anime review", "item", id, "accepted", accept, "candidate", in.Candidate)
		return http.StatusOK, nil
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
