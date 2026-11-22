package web

import (
	"cmp"
	"net/http"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/nowplaying"
	"github.com/theatarr/theatarr/internal/uipages"
)

// NowPlayingSource gives the now playing tracker's state (docs/design/now-playing.md), nil while it is off.
type NowPlayingSource func() *nowplaying.Snapshot

func (w *Web) mountNowPlayingAPI(mux *http.ServeMux) {
	w.handle(mux, "POST "+APIBase+"/nowplaying/rclone/test", SignedIn, auth.PermAdmin, w.apiRcloneTest)
	w.handle(mux, "GET "+APIBase+"/nowplaying", SignedIn, auth.PermBrowse, w.apiNowPlaying)
}

// apiNowPlaying answers what Plex plays, and the files that stopped working while playing in the last 24 hours.
// Managers and admins (SeeFiles) see every playback with its source, fetch, pre-warm and the cause detail, and every
// incident with its releases; anyone else sees only their own playbacks (their Plex account), without file detail, and
// only the incidents of their own playbacks, without the release names, the library and the file count, and with the
// message only when it is one of Theatarr's fixed sentences (what their player was told, or why it was not stopped),
// never Plex's refusal.
func (w *Web) apiNowPlaying(rw http.ResponseWriter, r *http.Request) {
	var snap *nowplaying.Snapshot
	if w.nowPlaying != nil {
		snap = w.nowPlaying()
	}
	if snap == nil {
		writeJSON(rw, http.StatusOK, nowplaying.Snapshot{Sessions: []nowplaying.SessionView{}})
		return
	}
	if w.permsOf(r).SeeFiles {
		out := *snap
		if out.Error != "" {
			// What to fix first; Plex's own error follows as the detail.
			at := ""
			if w.pol != nil && w.pol.Library != nil && w.pol.Library.Plex != nil {
				at = w.pol.Library.Plex.URL
			}
			out.Error, out.ErrorDetail = uipages.Fix(uipages.FixPlex, "", at, cmp.Or(out.ErrorClass, uipages.FailConnectivity)), out.Error
		}
		writeJSON(rw, http.StatusOK, out)
		return
	}
	out := nowplaying.Snapshot{Enabled: snap.Enabled, UpdatedAt: snap.UpdatedAt, Sessions: []nowplaying.SessionView{}}
	if snap.Error != "" {
		out.Error = "Plex does not answer"
	}
	plexID, plexName := w.plexAccount(r)
	for _, s := range snap.Sessions {
		if !ownSession(s, plexID, plexName) {
			continue
		}
		s.Health.Detail = ""
		if s.Source != nil {
			s.Source = &nowplaying.Source{TitleID: s.Source.TitleID, Title: s.Source.Title, Matched: s.Source.Matched}
		}
		s.Fetch, s.Prewarm = nil, nil
		out.Sessions = append(out.Sessions, s)
	}
	for _, in := range snap.Incidents {
		if plexName == "" || !strings.EqualFold(in.Viewer, plexName) {
			continue
		}
		in.OldRelease, in.NewRelease, in.Version, in.Files = "", "", "", 0
		if !strings.HasPrefix(in.Message, nowplaying.StoppedNote) && !strings.HasPrefix(in.Message, nowplaying.NotStoppedNote) {
			in.Message = "" // why Plex refused is file detail; the other messages are Theatarr's own fixed sentences
		}
		out.Incidents = append(out.Incidents, in)
	}
	writeJSON(rw, http.StatusOK, out)
}

// plexAccount is the caller's linked Plex account (id and name); empty for a local account without one.
func (w *Web) plexAccount(r *http.Request) (string, string) {
	id := auth.FromContext(r.Context())
	if id == nil || id.UserID == 0 {
		return "", ""
	}
	u, err := w.auth.GetUser(r.Context(), id.UserID)
	if err != nil || u == nil {
		return "", ""
	}
	var pid string
	if u.PlexID != nil {
		pid = strconv.FormatInt(*u.PlexID, 10)
	}
	return pid, u.PlexName
}

// ownSession: Plex names the account playing by its plex.tv id and its name.
func ownSession(s nowplaying.SessionView, plexID, plexName string) bool {
	switch {
	case plexID != "" && s.UserID == plexID:
		return true
	case plexName != "" && strings.EqualFold(s.User, plexName):
		return true
	}
	return false
}
