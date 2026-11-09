package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/nowplaying"
)

// NowPlayingSource gives the now playing tracker's state (docs/design/now-playing.md), nil while it is off.
type NowPlayingSource func() *nowplaying.Snapshot

func (w *Web) mountNowPlayingAPI(mux *http.ServeMux) {
	w.handle(mux, "POST "+APIBase+"/nowplaying/rclone/test", SignedIn, auth.PermAdmin, w.apiRcloneTest)
	w.handle(mux, "GET "+APIBase+"/nowplaying", SignedIn, auth.PermBrowse, w.apiNowPlaying)
}

// apiNowPlaying answers what Plex plays. Managers and admins (SeeFiles) see every playback with its source, fetch,
// pre-warm and the cause detail; anyone else sees only their own playbacks (their Plex account), without file
// detail.
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
		writeJSON(rw, http.StatusOK, snap)
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
