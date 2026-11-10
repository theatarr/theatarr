package arrview

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/indexer"
)

// parses reports whether the flavour's parser reads a release title at all.
func parses(flavour, title string) bool {
	if flavour == Sonarr {
		return sonarr.ParseTitle(title) != nil
	}
	return radarr.ParseMovieTitle(title, false) != nil
}

// mapRelease finds the title a release is for: by the ids it carries (TMDb for movies, TVDB for series, IMDb),
// else by its parsed title (and year) among the titles that have a version in the view, alternative titles
// included. 0 when none matches.
func (s *Service) mapRelease(ctx context.Context, v *View, rel *indexer.Release) (int64, error) {
	if id, err := s.titleByIDs(ctx, v, rel.TMDb, rel.TVDB, rel.IMDb); err != nil || id > 0 {
		return id, err
	}
	return s.titleByName(ctx, v, rel.Title)
}

func (s *Service) titleByIDs(ctx context.Context, v *View, tmdb, tvdb int, imdb string) (int64, error) {
	if v.Flavour == Radarr && tmdb > 0 || v.Flavour == Sonarr && tvdb > 0 {
		if id, err := s.titleByExternal(ctx, v, int64(tmdb), int64(tvdb)); err != nil || id > 0 {
			return id, err
		}
	}
	if imdb != "" {
		var id int64
		err := s.st.Reader().QueryRowContext(ctx, `SELECT id FROM title WHERE kind = ? AND imdb_id = ? LIMIT 1`,
			string(v.kind()), imdb).Scan(&id)
		if err != sql.ErrNoRows {
			return id, err
		}
	}
	return 0, nil
}

// titleByName matches a release title's parsed name (and year) against the view's titles, the way the arrs'
// ParsingService looks a release up in their library: clean titles equal, the year equal to the title's year or
// secondary year when the release has one.
func (s *Service) titleByName(ctx context.Context, v *View, title string) (int64, error) {
	var names []string
	year := 0
	if v.Flavour == Sonarr {
		p := sonarr.ParseTitle(title)
		if p == nil || p.SeriesTitle == "" {
			return 0, nil
		}
		names = []string{sonarr.CleanSeriesTitle(p.SeriesTitle)}
		if p.SeriesTitleInfo != nil {
			year = p.SeriesTitleInfo.Year
			if p.SeriesTitleInfo.TitleWithoutYear != "" {
				names = append(names, sonarr.CleanSeriesTitle(p.SeriesTitleInfo.TitleWithoutYear))
			}
		}
	} else {
		p := radarr.ParseMovieTitle(title, false)
		if p == nil {
			return 0, nil
		}
		for _, t := range p.MovieTitles {
			names = append(names, radarr.CleanMovieTitle(t))
		}
		year = p.Year
	}
	clean := func(n string) string {
		if v.Flavour == Sonarr {
			return sonarr.CleanSeriesTitle(n)
		}
		return radarr.CleanMovieTitle(n)
	}
	want := map[string]bool{}
	for _, n := range names {
		if n != "" {
			want[n] = true
		}
	}
	where, args := scope(v, nil)
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT DISTINCT t.id, t.name, COALESCE(t.year, 0),
		COALESCE(t.secondary_year, 0) FROM title t JOIN version v ON v.title_id = t.id WHERE `+where+`
		UNION SELECT DISTINCT t.id, a.name, COALESCE(t.year, 0), COALESCE(t.secondary_year, 0) FROM title_alias a
		JOIN title t ON t.id = a.title_id JOIN version v ON v.title_id = t.id WHERE `+where,
		append(args, args...)...)
	if err != nil {
		return 0, err
	}
	var hit, loose int64
	for rows.Next() {
		var id int64
		var name string
		var y, y2 int
		if err := rows.Scan(&id, &name, &y, &y2); err != nil {
			rows.Close()
			return 0, err
		}
		if !want[clean(name)] {
			continue
		}
		switch {
		case year > 0 && (year == y || year == y2):
			if hit == 0 {
				hit = id
			}
		case year == 0 || y == 0:
			if loose == 0 {
				loose = id
			}
		}
	}
	if err := closeRows(rows); err != nil {
		return 0, err
	}
	if hit > 0 {
		return hit, nil
	}
	return loose, nil
}

// GET parse?title= (Radarr ParseController.Parse; Sonarr's also takes path=): the parser's reading of a name, and,
// when it maps to a title of the view, the title, its languages, and the custom formats and score our decision
// gives it for the title's first version in the view (an upgrade script's scoring oracle). A blank title is 204.
func (s *Service) parse(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	title, _ := r.query("title")
	if strings.TrimSpace(title) == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	out := map[string]any{"title": title}
	var langs []string
	if r.v.Flavour == Sonarr {
		var p *sonarr.ParsedEpisodeInfo
		if path, ok := r.query("path"); ok && strings.TrimSpace(path) != "" {
			p = sonarr.ParsePath(path)
		} else {
			p = sonarr.ParseTitle(title)
		}
		if p == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
		out["parsedEpisodeInfo"] = p
		for _, l := range p.Languages {
			langs = append(langs, l.Name)
		}
	} else {
		p := radarr.ParseMovieTitle(title, false)
		if p == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
		out["parsedMovieInfo"] = p
		for _, l := range p.Languages {
			langs = append(langs, l.Name)
		}
	}
	rel := indexer.Release{Title: title, Seeders: -1, Peers: -1}
	if r.v.Flavour == Radarr {
		if p := radarr.ParseMovieTitle(title, false); p != nil {
			rel.TMDb, rel.IMDb = p.TmdbID, p.ImdbID
		}
	}
	tid, err := s.mapRelease(ctx, r.v, &rel)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	e, err := s.loadOne(ctx, r.v, tid)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if e == nil || len(e.versions) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	out["languages"] = languagesFrom(r.v.Flavour, langs)
	out["customFormats"] = []customFormatID{}
	out["customFormatScore"] = 0
	if s.decider != nil {
		d, err := s.decider.Decide(ctx, decide.Release{Release: rel}, e.id, e.versions[0].key)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		out["customFormats"], out["customFormatScore"] = formatIDs(d.Formats), d.Score
		if len(d.Languages) > 0 {
			out["languages"] = languagesFrom(r.v.Flavour, d.Languages)
		}
		if r.v.Flavour == Sonarr {
			eps, err := s.loadEpisodesWith(ctx, r.v, []*entry{e}, true)
			if err != nil {
				s.internal(w, r, err)
				return
			}
			list := []episodeResource{}
			for _, x := range eps[e.id] {
				for _, id := range d.EpisodeIDs {
					if x.id == id {
						list = append(list, episodeFrom(e, x))
					}
				}
			}
			out["episodes"] = list
		}
	}
	if r.v.Flavour == Sonarr {
		srs, err := s.seriesResources(ctx, r.v, []*entry{e})
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if len(srs) > 0 {
			out["series"] = srs[0]
		}
		if _, ok := out["episodes"]; !ok {
			out["episodes"] = []episodeResource{}
		}
	} else {
		out["movie"] = s.movieResource(r.v, e)
	}
	writeJSON(w, http.StatusOK, out)
}
