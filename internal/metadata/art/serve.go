package art

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/theatarr/theatarr/internal/policy"
)

// Image kinds of a title.
const (
	KindPoster   = "poster"
	KindBackdrop = "backdrop"
	KindLogo     = "logo"
)

// Cache-Control values.
const (
	// CacheImmutable is for a TMDb file by path, or title art whose ?v= matches its art_hash.
	CacheImmutable = "private, max-age=31536000, immutable"
	// CacheCurrent is for title art asked for without (or with an old) ?v=.
	CacheCurrent = "private, max-age=3600"
	// CacheFallback is for generated art while there is no real art yet, or TMDb failed.
	CacheFallback = "private, max-age=300"
)

// ServeTitle answers GET /img/t/{id}/{kind}/{size}: the title's art from the cache (fetched on first use), or a
// generated SVG when there is none yet or the fetch fails. It answers 404 for an unknown title, kind or size.
func (s *Service) ServeTitle(rw http.ResponseWriter, r *http.Request, titleID int64, kind, size string) {
	if kind != KindPoster && kind != KindBackdrop && kind != KindLogo {
		http.NotFound(rw, r)
		return
	}
	set := s.imageSettings()
	if !set.ValidImageSize(size) {
		http.NotFound(rw, r)
		return
	}
	info, err := s.titleArt(r.Context(), titleID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(rw, r)
		return
	}
	if err != nil {
		s.log.Error("img: title art", "title", titleID, "err", err)
		http.Error(rw, "internal error", http.StatusInternalServerError)
		return
	}
	path := map[string]string{KindPoster: info.poster, KindBackdrop: info.backdrop, KindLogo: info.logo}[kind]
	file := strings.TrimPrefix(path, "/")
	if file == "" || s.images == nil {
		serveFallback(rw, r, kind, titleID, info.name, info.year)
		return
	}
	local, err := s.images.Get(r.Context(), size, file)
	if err != nil {
		if r.Context().Err() == nil {
			s.log.Info("img: fetch failed; fallback served", "title", titleID, "kind", kind, "size", size, "err", err)
		}
		serveFallback(rw, r, kind, titleID, info.name, info.year)
		return
	}
	cc := CacheCurrent
	if v := r.URL.Query().Get("v"); v != "" && v == info.hash {
		cc = CacheImmutable
	}
	serveFile(rw, r, local, file, cc)
}

// ServeTMDb answers GET /img/tmdb/{size}/{file}: any TMDb image by path, for titles not in the library. size and
// file must pass the size list and FileRE (404 otherwise).
func (s *Service) ServeTMDb(rw http.ResponseWriter, r *http.Request, size, file string) {
	if !s.imageSettings().ValidImageSize(size) || !FileRE.MatchString(file) {
		http.NotFound(rw, r)
		return
	}
	if s.images == nil {
		serveFallback(rw, r, KindPoster, 0, "", 0)
		return
	}
	local, err := s.images.Get(r.Context(), size, file)
	if err != nil {
		if r.Context().Err() == nil {
			s.log.Info("img: fetch failed; fallback served", "size", size, "file", file, "err", err)
		}
		serveFallback(rw, r, KindPoster, 0, "", 0)
		return
	}
	serveFile(rw, r, local, file, CacheImmutable)
}

func (s *Service) imageSettings() policy.ImageSettings {
	if s.images != nil {
		return s.images.set
	}
	return policy.ImageSettings{}
}

type artInfo struct {
	name, poster, backdrop, logo, hash string
	year                               int
}

func (s *Service) titleArt(ctx context.Context, id int64) (artInfo, error) {
	var a artInfo
	var year sql.NullInt64
	err := s.st.Reader().QueryRowContext(ctx, `SELECT t.name, t.year, COALESCE(m.poster_path, ''),
		COALESCE(m.backdrop_path, ''), COALESCE(m.logo_path, ''), COALESCE(m.art_hash, '')
		FROM title t LEFT JOIN title_meta m ON m.title_id = t.id WHERE t.id = ?`, id).Scan(&a.name, &year, &a.poster,
		&a.backdrop, &a.logo, &a.hash)
	a.year = int(year.Int64)
	return a, err
}

func serveFile(rw http.ResponseWriter, r *http.Request, local, file, cacheControl string) {
	f, err := os.Open(local)
	if err != nil {
		http.Error(rw, "image gone", http.StatusServiceUnavailable)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(rw, "image gone", http.StatusServiceUnavailable)
		return
	}
	h := rw.Header()
	h.Set("Content-Type", contentType(file))
	h.Set("Cache-Control", cacheControl)
	h.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(rw, r, "", st.ModTime(), f)
}

// serveFallback writes generated art: the title and year on a gradient picked by the title id (a poster or
// backdrop), or the title alone on nothing (a logo).
func serveFallback(rw http.ResponseWriter, r *http.Request, kind string, id int64, name string, year int) {
	h := rw.Header()
	h.Set("Content-Type", "image/svg+xml")
	h.Set("Cache-Control", CacheFallback)
	h.Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodHead {
		return
	}
	rw.Write([]byte(FallbackSVG(kind, id, name, year)))
}

// FallbackSVG is the generated art for a title with none.
func FallbackSVG(kind string, id int64, name string, year int) string {
	w, hgt, fs, maxLine := 500, 750, 44, 16
	switch kind {
	case KindBackdrop:
		w, hgt, fs, maxLine = 1280, 720, 64, 28
	case KindLogo:
		w, hgt, fs, maxLine = 600, 200, 56, 18
	}
	hue := int((uint64(id)*47 + 200) % 360)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, w, hgt, w, hgt)
	if kind != KindLogo {
		fmt.Fprintf(&b, `<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1">`+
			`<stop offset="0" stop-color="hsl(%d,45%%,32%%)"/><stop offset="1" stop-color="hsl(%d,50%%,14%%)"/>`+
			`</linearGradient></defs><rect width="100%%" height="100%%" fill="url(#g)"/>`, hue, (hue+40)%360)
	}
	lines := wrap(name, maxLine, 4)
	lh := fs * 6 / 5
	extra := 0
	if year > 0 && kind != KindLogo {
		extra = 1
	}
	y := hgt/2 - (len(lines)+extra-1)*lh/2
	fill := "#ffffff"
	if kind == KindLogo {
		fill = "#e5e7eb"
	}
	for _, l := range lines {
		fmt.Fprintf(&b, `<text x="50%%" y="%d" text-anchor="middle" dominant-baseline="middle" `+
			`font-family="system-ui,sans-serif" font-size="%d" font-weight="600" fill="%s">%s</text>`,
			y, fs, fill, html.EscapeString(l))
		y += lh
	}
	if extra > 0 {
		fmt.Fprintf(&b, `<text x="50%%" y="%d" text-anchor="middle" dominant-baseline="middle" `+
			`font-family="system-ui,sans-serif" font-size="%d" fill="#ffffff" fill-opacity="0.75">%d</text>`,
			y, fs*2/3, year)
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// wrap breaks s into at most n lines of about width runes, ending the last with an ellipsis when cut.
func wrap(s string, width, n int) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		for utf8.RuneCountInString(word) > width {
			r := []rune(word)
			if cur != "" {
				lines, cur = append(lines, cur), ""
			}
			lines, word = append(lines, string(r[:width])), string(r[width:])
		}
		switch {
		case cur == "":
			cur = word
		case utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(word) <= width:
			cur += " " + word
		default:
			lines, cur = append(lines, cur), word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) > n {
		lines = lines[:n]
		lines[n-1] += "…"
	}
	return lines
}
