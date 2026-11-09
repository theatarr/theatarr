package arrview

import (
	"net/http"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
)

// Endpoints the phone apps call on their screens that have nothing to show here. They answer empty, in the arrs'
// shape, so a screen loads instead of failing on a 404: extrafile (Ruddarr's movie screen, LunaSea), credit,
// exclusions, importlistexclusion, importlist/movie (LunaSea), filesystem (LunaSea's manual import browser:
// browsing the server's disk is not served), language, qualitydefinition and languageprofile (LunaSea's settings,
// Sonarr v3).

func (s *Service) emptyList(w http.ResponseWriter, _ *request) { writeJSON(w, http.StatusOK, []any{}) }

// GET filesystem: no browsing of the server's filesystem (FileSystemResource with nothing in it).
func (s *Service) filesystem(w http.ResponseWriter, _ *request) {
	writeJSON(w, http.StatusOK, map[string]any{"parent": "", "directories": []any{}, "files": []any{}})
}

// GET language: the flavour's language list (LanguageResource: id, name, nameLower).
func (s *Service) languages(w http.ResponseWriter, r *request) {
	fl := format.Radarr
	if r.v.Flavour == Sonarr {
		fl = format.Sonarr
	}
	out := []map[string]any{}
	for id := -2; id < 60; id++ {
		if n := fl.LanguageName(id); n != "" {
			out = append(out, map[string]any{"id": id, "name": n, "nameLower": strings.ToLower(n)})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// GET qualitydefinition: the flavour's qualities in their default order, without size limits (versions carry
// their own, per profile).
func (s *Service) qualityDefinitions(w http.ResponseWriter, r *request) {
	fl := format.Radarr
	if r.v.Flavour == Sonarr {
		fl = format.Sonarr
	}
	out := []map[string]any{}
	for i, q := range profile.Qualities(fl) {
		out = append(out, map[string]any{"id": i + 1, "quality": qualityFor(r.v.Flavour, q.Name), "title": q.Name,
			"weight": i + 1, "minSize": 0})
	}
	writeJSON(w, http.StatusOK, out)
}

// GET languageprofile (Sonarr v3; v4 keeps one "Deprecated" profile, which is what LunaSea gets).
func (s *Service) languageProfiles(w http.ResponseWriter, _ *request) {
	writeJSON(w, http.StatusOK, []map[string]any{{"id": 1, "name": "Deprecated", "upgradeAllowed": false,
		"cutoff": map[string]any{"id": 1, "name": "English"}, "languages": []any{}}})
}
