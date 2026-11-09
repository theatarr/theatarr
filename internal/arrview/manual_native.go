package arrview

import (
	"net/http"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
)

// ServeManualImport reuses the arr import handlers behind the native API's admin/session and CSRF checks.
// Arr endpoints themselves require an API key, which the SPA does not hold.
func (s *Service) ServeManualImport(w http.ResponseWriter, r *http.Request) {
	var view *View
	for _, v := range s.views {
		if v.Key == "v/"+r.URL.Query().Get("version") {
			view = v
			break
		}
	}
	if view == nil {
		badRequest(w, "version", "Choose a version", nil)
		return
	}
	req := &request{Request: r, v: view, client: "session"}
	switch {
	case strings.HasSuffix(r.URL.Path, "/options"):
		s.manualImportOptions(w, req)
	case strings.HasSuffix(r.URL.Path, "/execute"):
		s.postCommand(w, req)
	case r.Method == http.MethodPost:
		s.manualImportReprocess(w, req)
	default:
		s.manualImportList(w, req)
	}
}

func (s *Service) manualImportOptions(w http.ResponseWriter, r *request) {
	entries, err := s.load(r.Context(), r.v, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	titles := []map[string]any{}
	for _, e := range entries {
		eps := []map[string]any{}
		if r.v.Flavour == Sonarr {
			rows, err := s.loadEpisodesWith(r.Context(), r.v, []*entry{e}, true)
			if err != nil {
				s.internal(w, r, err)
				return
			}
			for _, ep := range rows[e.id] {
				eps = append(eps, map[string]any{"id": ep.id, "name": ep.name, "season": ep.season, "number": ep.number})
			}
		}
		titles = append(titles, map[string]any{"id": viewID(e.id), "name": e.name, "episodes": eps})
	}
	fl := format.Radarr
	if r.v.Flavour == Sonarr {
		fl = format.Sonarr
	}
	qualities := []quality{}
	for _, q := range profile.Qualities(fl) {
		qualities = append(qualities, qualityFor(r.v.Flavour, q.Name))
	}
	languages := []language{}
	for id := 1; id < 60; id++ {
		if l, ok := manualLanguage(r.v.Flavour, id); ok {
			languages = append(languages, l)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"flavour": r.v.Flavour, "titles": titles,
		"qualities": qualities, "languages": languages})
}
