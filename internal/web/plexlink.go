package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"

	"github.com/theatarr/theatarr/internal/plex"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/uipages"
)

// PlexItems is what "Watch in Plex" reads from Plex: the server's identifier and a section's items by file
// (plex.Client).
type PlexItems interface {
	Identity(ctx context.Context) (string, error)
	SectionFiles(ctx context.Context, section, typ int) (map[string]plex.FileItem, error)
}

// PlexItemsSource gives the Plex client of the settings in force, with the policy it was built from; without Plex or
// its token no client, but still the policy (its libraries' Plex sections).
type PlexItemsSource func(ctx context.Context) (PlexItems, *policy.Policy, error)

// PlexLink is GET /titles/{id}/plex: where the title of one library is in Plex.
type PlexLink struct {
	Found bool `json:"found"`
	// WebURL opens the item in Plex Web (app.plex.tv); AppURL in the Plex app (plex://, the form the iOS app takes).
	WebURL  string `json:"web_url,omitempty"`
	AppURL  string `json:"app_url,omitempty"`
	Message string `json:"message,omitempty"`
}

const plexNotFound = "Not found in Plex yet"

// plexNoSection: the library names no Plex section (library.versions.<key>.plex_section), so nothing is looked up.
const plexNoSection = "This library has no Plex section."

// apiPlexLink looks the title up in Plex on demand, in the section of library ?version=: one identity call and one
// listing of the section's items by file, matched on the library's own files of the title (a series by the show of
// one of its episodes). Nothing is stored or polled. A title in two libraries is two Plex items, asked one by one.
func (w *Web) apiPlexLink(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	key := r.URL.Query().Get("version")
	if !ok || key == "" {
		badRequest(rw, "version is required")
		return
	}
	var kind string
	err := w.st.Reader().QueryRowContext(r.Context(), `SELECT kind FROM title WHERE id = ?`, id).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	notFoundYet := func() { writeJSON(rw, http.StatusOK, PlexLink{Message: plexNotFound}) }
	if w.plexItems == nil {
		notFoundYet()
		return
	}
	pc, pol, err := w.plexItems(r.Context())
	// The library's own section first: with none, no Plex set-up would find it.
	if err == nil && pol != nil && pol.Library != nil {
		if lv := pol.Library.Versions[key]; lv != nil && lv.PlexSection <= 0 {
			writeJSON(rw, http.StatusOK, PlexLink{Message: plexNoSection})
			return
		}
	}
	if err != nil || pc == nil || pol == nil || pol.Library == nil || pol.Library.Plex == nil {
		notFoundYet()
		return
	}
	lv := pol.Library.Versions[key]
	if lv == nil {
		notFoundYet()
		return
	}
	rows, err := w.st.Reader().QueryContext(r.Context(), `SELECT l.path FROM link l
		JOIN link_version x ON x.link_id = l.id JOIN version v ON v.id = x.version_id
		WHERE l.title_id = ? AND v.policy_key = ? AND l.state = 'active' ORDER BY l.id`, id, key)
	if err != nil {
		w.internal(rw, err)
		return
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			w.internal(rw, err)
			return
		}
		paths = append(paths, p)
	}
	if err := rows.Close(); err != nil {
		w.internal(rw, err)
		return
	}
	if len(paths) == 0 {
		notFoundYet()
		return
	}
	unreachable := func(err error) {
		class := uipages.FailConnectivity
		if se := (*plex.StatusError)(nil); errors.As(err, &se) {
			class = uipages.HTTPClass(se.Status)
		}
		msg := "Plex does not answer" // as Now playing: a member reads no address and no settings page
		if w.permsOf(r).SeeFiles {
			msg = uipages.Fix(uipages.FixPlex, "", pol.Library.Plex.URL, class)
		}
		writeJSON(rw, http.StatusOK, PlexLink{Message: msg})
	}
	machine, err := pc.Identity(r.Context())
	if err != nil {
		unreachable(err)
		return
	}
	typ := plex.TypeMovie
	if kind != "movie" {
		typ = plex.TypeEpisode
	}
	files, err := pc.SectionFiles(r.Context(), lv.PlexSection, typ)
	if err != nil {
		unreachable(err)
		return
	}
	for _, p := range paths {
		it, ok := files[p]
		if !ok {
			continue
		}
		rk := it.RatingKey
		if typ == plex.TypeEpisode {
			rk = it.ShowKey
		}
		if rk == "" {
			continue
		}
		mk := url.QueryEscape("/library/metadata/" + rk)
		writeJSON(rw, http.StatusOK, PlexLink{Found: true,
			WebURL: "https://app.plex.tv/desktop/#!/server/" + url.PathEscape(machine) + "/details?key=" + mk,
			AppURL: "plex://preplay/?metadataKey=" + mk + "&server=" + url.QueryEscape(machine)})
		return
	}
	writeJSON(rw, http.StatusOK, PlexLink{Message: plexNotFound})
}
