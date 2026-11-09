package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/metadata/art"
)

// Title art and metadata (docs/design/artwork.md): the /img proxy, POST titles/{id}/refresh, and the art fields of the
// titles API. Images come from this origin only; the browser never talks to TMDb, and the CSP stays 'self'.

// ArtService serves title art and refreshes title metadata (art.Service).
type ArtService interface {
	ServeTitle(rw http.ResponseWriter, r *http.Request, titleID int64, kind, size string)
	ServeTMDb(rw http.ResponseWriter, r *http.Request, size, file string)
	Refresh(ctx context.Context, titleID int64) (int64, error)
}

// Image sizes the API's URLs use.
const (
	posterSize   = "w342"
	backdropSize = "w1280"
	logoSize     = "w500"
	// overviewShort caps the overview on list rows (runes).
	overviewShort = 300
)

func (w *Web) mountArt(mux *http.ServeMux) {
	w.handle(mux, "GET /img/t/{id}/{kind}/{size}", SignedIn, auth.PermBrowse, func(rw http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok || w.art == nil {
			http.NotFound(rw, r)
			return
		}
		w.art.ServeTitle(rw, r, id, r.PathValue("kind"), r.PathValue("size"))
	})
	w.handle(mux, "GET /img/tmdb/{size}/{file}", SignedIn, auth.PermBrowse, func(rw http.ResponseWriter, r *http.Request) {
		if w.art == nil {
			http.NotFound(rw, r)
			return
		}
		w.art.ServeTMDb(rw, r, r.PathValue("size"), r.PathValue("file"))
	})
	w.handle(mux, "POST "+APIBase+"/titles/{id}/refresh", SignedIn, auth.PermManage, w.apiRefreshTitle)
}

func (w *Web) apiRefreshTitle(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	if w.art == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "metadata refresh is off (no TMDb key)"})
		return
	}
	job, err := w.art.Refresh(r.Context(), id)
	switch {
	case errors.Is(err, art.ErrNoTitle):
		notFound(rw)
	case errors.Is(err, art.ErrNoTMDb):
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "metadata refresh is off (no TMDb key)"})
	case err != nil:
		w.internal(rw, err)
	default:
		writeJSON(rw, http.StatusAccepted, map[string]any{"job_id": job, "title_id": id})
	}
}

// TitleArt is the art on a title row: image URLs on this origin (generated art until the real art is fetched) and
// a short overview.
type TitleArt struct {
	Poster   string `json:"poster"`
	Backdrop string `json:"backdrop"`
	Overview string `json:"overview,omitempty"`
}

// TitleMeta is a title's TMDb metadata (title_meta), on the title page.
type TitleMeta struct {
	TMDbID           int        `json:"tmdb_id,omitempty"`
	Overview         string     `json:"overview"`
	Tagline          string     `json:"tagline,omitempty"`
	Genres           []string   `json:"genres"`
	RuntimeMinutes   *int       `json:"runtime_minutes,omitempty"`
	Rating           *float64   `json:"rating,omitempty"`
	RatingVotes      int        `json:"rating_votes"`
	Certification    string     `json:"certification,omitempty"`
	Status           string     `json:"status,omitempty"`
	Network          string     `json:"network,omitempty"`
	OriginalLanguage string     `json:"original_language,omitempty"`
	ReleaseDate      string     `json:"release_date,omitempty"`
	LastAirDate      string     `json:"last_air_date,omitempty"`
	NextAirDate      string     `json:"next_air_date,omitempty"`
	PosterPath       string     `json:"poster_path,omitempty"`
	BackdropPath     string     `json:"backdrop_path,omitempty"`
	LogoPath         string     `json:"logo_path,omitempty"`
	Logo             string     `json:"logo,omitempty"`
	ArtHash          string     `json:"art_hash,omitempty"`
	RefreshedAt      *time.Time `json:"refreshed_at,omitempty"`
	AttemptedAt      time.Time  `json:"attempted_at"`
	RefreshAfter     time.Time  `json:"refresh_after"`
	Error            string     `json:"error,omitempty"`
}

func imgURL(id int64, kind, size, hash string) string {
	u := fmt.Sprintf("/img/t/%d/%s/%s", id, kind, size)
	if hash != "" {
		u += "?v=" + hash
	}
	return u
}

func shortText(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)[:n]
	cut := string(r)
	if i := strings.LastIndexByte(cut, ' '); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}

func artFor(id int64, m *art.Meta) TitleArt {
	var hash, overview string
	if m != nil {
		hash, overview = m.ArtHash, m.Overview
	}
	return TitleArt{Poster: imgURL(id, art.KindPoster, posterSize, hash),
		Backdrop: imgURL(id, art.KindBackdrop, backdropSize, hash), Overview: shortText(overview, overviewShort)}
}

// fillArt sets the art of each row from title_meta.
func (w *Web) fillArt(ctx context.Context, db *sql.DB, items []TitleRow) error {
	ids := make([]int64, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	metas, err := art.LoadMeta(ctx, db, ids)
	if err != nil {
		return err
	}
	for i := range items {
		var mp *art.Meta
		if m, ok := metas[items[i].ID]; ok {
			mp = &m
		}
		items[i].TitleArt = artFor(items[i].ID, mp)
		items[i].TitleFacts = factsFor(mp)
	}
	return nil
}

// titleMeta loads one title's art, facts (library.go) and metadata; meta is nil before the first try.
func (w *Web) titleMeta(ctx context.Context, db *sql.DB, id int64) (TitleArt, TitleFacts, *TitleMeta, error) {
	metas, err := art.LoadMeta(ctx, db, []int64{id})
	if err != nil {
		return TitleArt{}, TitleFacts{}, nil, err
	}
	m, ok := metas[id]
	if !ok {
		return artFor(id, nil), factsFor(nil), nil, nil
	}
	tm := &TitleMeta{TMDbID: m.TMDbID, Overview: m.Overview, Tagline: m.Tagline, Genres: m.Genres,
		RatingVotes: m.RatingVotes, Certification: m.Certification, Status: m.Status, Network: m.Network,
		OriginalLanguage: m.OriginalLanguage, ReleaseDate: m.ReleaseDate, LastAirDate: m.LastAirDate,
		NextAirDate: m.NextAirDate, PosterPath: m.PosterPath, BackdropPath: m.BackdropPath, LogoPath: m.LogoPath,
		ArtHash: m.ArtHash, RefreshedAt: m.RefreshedAt, AttemptedAt: m.AttemptedAt, RefreshAfter: m.RefreshAfter,
		Error: m.Error}
	if m.RuntimeMinutes > 0 {
		v := m.RuntimeMinutes
		tm.RuntimeMinutes = &v
	}
	if m.RefreshedAt != nil && m.RatingVotes > 0 {
		v := m.Rating
		tm.Rating = &v
	}
	if m.LogoPath != "" {
		tm.Logo = imgURL(id, art.KindLogo, logoSize, m.ArtHash)
	}
	return artFor(id, &m), factsFor(&m), tm, nil
}
