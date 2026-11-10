package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/auth"
)

// Users, the profile and API keys (docs/design/ui.md, docs/design/users.md):
//
//	GET  auth/me                    who am I: role, permissions, profile, CSRF token
//	PATCH me, POST me/password      own display name; own password (local accounts only)
//	GET/POST keys, DELETE keys/{id} own API keys; with the users permission, anyone's (?user=<id>|all, user_id)
//	GET users, PATCH users/{id}     the users screen: role, status, extra permissions, auto-approve, request versions
//	DELETE users/{id}/sessions      sign an account out everywhere
//	GET/PUT users/settings          what a first Plex sign-in gets (pending or active)
//
// A key reaches these as its owner would (its owner's permissions decide): an agent with an admin's key can make and
// revoke keys and manage users. Only logout (auth.go) is session only.

func (w *Web) mountUsersAPI(mux *http.ServeMux) {
	b := APIBase
	w.handle(mux, "GET "+b+"/auth/me", SignedIn, auth.PermBrowse, w.apiMe)
	w.handle(mux, "PATCH "+b+"/me", SignedIn, auth.PermBrowse, w.apiPatchMe)
	w.handle(mux, "POST "+b+"/me/password", SignedIn, auth.PermBrowse, w.apiMyPassword)
	w.handle(mux, "GET "+b+"/keys", SignedIn, auth.PermOwnKeys, w.apiKeys)
	w.handle(mux, "POST "+b+"/keys", SignedIn, auth.PermOwnKeys, w.apiCreateKey)
	w.handle(mux, "DELETE "+b+"/keys/{id}", SignedIn, auth.PermOwnKeys, w.apiRevokeKey)
	w.handle(mux, "GET "+b+"/users", SignedIn, auth.PermUsers, w.apiUsers)
	w.handle(mux, "PATCH "+b+"/users/{id}", SignedIn, auth.PermUsers, w.apiPatchUser)
	w.handle(mux, "DELETE "+b+"/users/{id}/sessions", SignedIn, auth.PermUsers, w.apiEndSessions)
	w.handle(mux, "GET "+b+"/users/settings", SignedIn, auth.PermUsers, w.apiUserSettings)
	w.handle(mux, "PUT "+b+"/users/settings", SignedIn, auth.PermUsers, w.apiPutUserSettings)
}

// Me is GET auth/me.
type Me struct {
	Kind        string   `json:"kind"`
	ID          int64    `json:"id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"display_name"`
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
	// Local: the account has a password (it may change it); Plex accounts sign in with Plex only.
	Local     bool   `json:"local"`
	Plex      bool   `json:"plex"`
	AvatarURL string `json:"avatar_url,omitempty"`
	// RequestVersions: the version keys the account's requests ask for; absent is every version that fits.
	RequestVersions []string `json:"request_versions,omitempty"`
	CSRFToken       string   `json:"csrf_token,omitempty"`
	KeyID           int64    `json:"key_id,omitempty"`
	KeyName         string   `json:"key_name,omitempty"`
}

func (w *Web) apiMe(rw http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	out := Me{Kind: id.Kind, ID: id.UserID, Username: id.Username, DisplayName: id.DisplayName, Role: id.Role,
		Permissions: w.perms(id).List(), Local: id.Local}
	if out.DisplayName == "" {
		out.DisplayName = id.Username
	}
	if u, err := w.auth.GetUser(r.Context(), id.UserID); err == nil {
		out.Plex, out.AvatarURL, out.RequestVersions = u.PlexID != nil, u.AvatarURL, u.RequestVersions
	}
	if id.Kind == "apikey" {
		out.KeyID, out.KeyName = id.KeyID, id.KeyName
	} else {
		out.CSRFToken = w.auth.CSRFToken(rw, r)
	}
	writeJSON(rw, http.StatusOK, out)
}

func (w *Web) apiPatchMe(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		DisplayName *string `json:"display_name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	if req.DisplayName != nil && len(strings.TrimSpace(*req.DisplayName)) > 64 {
		badRequest(rw, "display_name: at most 64 characters")
		return
	}
	id := auth.FromContext(r.Context())
	if err := w.auth.UpdateUser(r.Context(), id.UserID, auth.UserPatch{DisplayName: req.DisplayName}); err != nil {
		w.internal(rw, err)
		return
	}
	u, err := w.auth.GetUser(r.Context(), id.UserID)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, u)
}

func (w *Web) apiMyPassword(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "want JSON {\"current\", \"new\"}")
		return
	}
	id := auth.FromContext(r.Context())
	keep := ""
	if c, err := r.Cookie(auth.SessionCookie); err == nil {
		keep = c.Value
	}
	err := w.auth.ChangePassword(r.Context(), id.UserID, req.Current, req.New, keep)
	switch {
	case errors.Is(err, auth.ErrNotLocal):
		writeJSON(rw, http.StatusConflict, apiError{Error: "this account signs in with Plex and has no password"})
	case errors.Is(err, auth.ErrWrongPass):
		writeJSON(rw, http.StatusForbidden, apiError{Error: "the current password is wrong"})
	case err != nil && strings.Contains(err.Error(), "too short"):
		badRequest(rw, err.Error())
	case err != nil:
		w.internal(rw, err)
	default:
		w.log.Info("password changed", "user", id.UserID)
		rw.WriteHeader(http.StatusNoContent)
	}
}

// keyOwner is whose keys a keys request is about: the caller's, or (with the users permission) another account's
// (0 = everyone's, for listing).
func (w *Web) keyOwner(r *http.Request, asked string) (int64, bool) {
	id := auth.FromContext(r.Context())
	if asked == "" || asked == strconv.FormatInt(id.UserID, 10) {
		return id.UserID, true
	}
	if !w.permsOf(r).Has(auth.PermUsers) {
		return 0, false
	}
	if asked == "all" {
		return 0, true
	}
	n, err := strconv.ParseInt(asked, 10, 64)
	return n, err == nil && n > 0
}

func (w *Web) apiKeys(rw http.ResponseWriter, r *http.Request) {
	owner, ok := w.keyOwner(r, r.URL.Query().Get("user"))
	if !ok {
		writeJSON(rw, http.StatusForbidden, apiError{Error: "other accounts' keys need the users permission"})
		return
	}
	keys, err := w.auth.ListKeys(r.Context(), owner)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"items": keys})
}

func (w *Web) apiCreateKey(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		UserID int64  `json:"user_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 4096)).Decode(&req); err != nil {
		badRequest(rw, "want JSON {\"name\"}")
		return
	}
	asked := ""
	if req.UserID > 0 {
		asked = strconv.FormatInt(req.UserID, 10)
	}
	owner, ok := w.keyOwner(r, asked)
	if !ok || owner == 0 {
		writeJSON(rw, http.StatusForbidden, apiError{Error: "keys for other accounts need the users permission"})
		return
	}
	if owner != auth.FromContext(r.Context()).UserID {
		if _, err := w.auth.GetUser(r.Context(), owner); errors.Is(err, auth.ErrUserNotFound) {
			notFound(rw)
			return
		}
	}
	key, info, err := w.auth.CreateKey(r.Context(), strings.TrimSpace(req.Name), owner)
	switch {
	case errors.Is(err, auth.ErrKeyName):
		badRequest(rw, err.Error())
		return
	case errors.Is(err, auth.ErrKeyExists):
		writeJSON(rw, http.StatusConflict, apiError{Error: err.Error()})
		return
	case err != nil:
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusCreated, map[string]any{"key": key, "info": info})
}

func (w *Web) apiRevokeKey(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	owner := auth.FromContext(r.Context()).UserID
	if w.permsOf(r).Has(auth.PermUsers) {
		owner = 0
	}
	err := w.auth.RevokeKey(r.Context(), id, owner)
	if errors.Is(err, auth.ErrKeyNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}

// UserSettings are the account settings an admin sets for everyone.
type UserSettings struct {
	// PlexNewUsers is what a first Plex sign-in gets: "pending" (waits for an admin) or "active".
	PlexNewUsers string `json:"plex_new_users"`
	// PlexEnabled: Plex sign-in is configured (metadata.sources.plex-tv and library.plex with its token).
	PlexEnabled bool `json:"plex_enabled"`
}

func (w *Web) userSettings(r *http.Request) (UserSettings, error) {
	v, err := w.auth.Setting(r.Context(), auth.SettingPlexNewUsers, auth.StatusPending)
	if v != auth.StatusActive {
		v = auth.StatusPending
	}
	return UserSettings{PlexNewUsers: v, PlexEnabled: w.plex != nil}, err
}

// UsersPage is GET users.
type UsersPage struct {
	Items    []auth.User  `json:"items"`
	Settings UserSettings `json:"settings"`
	// Versions are the version keys an admin may pick for a user's requests, with their labels.
	Versions []VersionChoice `json:"versions"`
}

// VersionChoice is a version key with its label and kind.
type VersionChoice struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Anime *bool  `json:"anime,omitempty"`
}

func (w *Web) versionChoices() []VersionChoice {
	out := []VersionChoice{}
	if w.pol == nil {
		return out
	}
	keys := sortedKeys(w.pol.Versions)
	names := w.keyLabels(keys)
	for _, k := range keys {
		v := w.pol.Versions[k]
		out = append(out, VersionChoice{Key: k, Label: names[k], Kind: string(v.Kind), Anime: v.Anime})
	}
	return out
}

func (w *Web) apiUsers(rw http.ResponseWriter, r *http.Request) {
	us, err := w.auth.ListUsers(r.Context())
	if err != nil {
		w.internal(rw, err)
		return
	}
	set, err := w.userSettings(r)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, UsersPage{Items: us, Settings: set, Versions: w.versionChoices()})
}

// UserPatchRequest is the body of PATCH users/{id}. Absent fields stay; request_versions null resets to every
// version that fits.
type UserPatchRequest struct {
	Role             *string         `json:"role"`
	Status           *string         `json:"status"`
	ExtraPermissions *[]string       `json:"extra_permissions"`
	AutoApprove      *bool           `json:"auto_approve"`
	DisplayName      *string         `json:"display_name"`
	RequestVersions  json.RawMessage `json:"request_versions"`
	// DiscordID is the account's Discord user id ("" clears it): request-ready messages @mention it.
	DiscordID *string `json:"discord_id"`
}

func (w *Web) apiPatchUser(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var req UserPatchRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	p := auth.UserPatch{Role: req.Role, Status: req.Status, AutoApprove: req.AutoApprove, DisplayName: req.DisplayName,
		DiscordID: req.DiscordID}
	if req.ExtraPermissions != nil {
		bits, err := auth.ParsePerms(*req.ExtraPermissions)
		if err != nil {
			badRequest(rw, err.Error())
			return
		}
		if extra := bits &^ auth.OptionalUserPerms; extra != 0 {
			badRequest(rw, "extra_permissions: only "+strings.Join(auth.OptionalUserPerms.Names(), ", ")+" can be added to a role, not "+strings.Join(extra.Names(), ", "))
			return
		}
		p.ExtraPerms = &bits
	}
	if len(req.RequestVersions) > 0 {
		var keys []string
		if string(req.RequestVersions) != "null" {
			if err := json.Unmarshal(req.RequestVersions, &keys); err != nil {
				badRequest(rw, "request_versions: want a list of library keys or null")
				return
			}
			if len(keys) == 0 {
				badRequest(rw, "request_versions: pick at least one library, or null for every library that fits")
				return
			}
			for _, k := range keys {
				if w.pol == nil || w.pol.Versions[k] == nil {
					badRequest(rw, "request_versions: unknown library "+strconv.Quote(k))
					return
				}
			}
			slices.Sort(keys)
			keys = slices.Compact(keys)
		}
		p.RequestVersions = &keys
	}
	if req.Status != nil && *req.Status == auth.StatusDisabled && id == auth.FromContext(r.Context()).UserID {
		badRequest(rw, "you cannot disable your own account")
		return
	}
	err := w.auth.UpdateUser(r.Context(), id, p)
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		notFound(rw)
		return
	case errors.Is(err, auth.ErrLastAdmin):
		writeJSON(rw, http.StatusConflict, apiError{Error: "the last active admin keeps the role and stays enabled"})
		return
	case err != nil && (strings.HasPrefix(err.Error(), "role") || strings.HasPrefix(err.Error(), "status") ||
		strings.HasPrefix(err.Error(), "discord_id")):
		badRequest(rw, err.Error())
		return
	case err != nil:
		w.internal(rw, err)
		return
	}
	u, err := w.auth.GetUser(r.Context(), id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	w.log.Info("user changed", "user", id, "by", auth.FromContext(r.Context()).UserID, "role", u.Role,
		"status", u.Status)
	writeJSON(rw, http.StatusOK, u)
}

func (w *Web) apiEndSessions(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	n, err := w.auth.EndSessions(r.Context(), id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ended": n})
}

func (w *Web) apiUserSettings(rw http.ResponseWriter, r *http.Request) {
	s, err := w.userSettings(r)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, s)
}

func (w *Web) apiPutUserSettings(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		PlexNewUsers string `json:"plex_new_users"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	if req.PlexNewUsers != auth.StatusPending && req.PlexNewUsers != auth.StatusActive {
		badRequest(rw, "plex_new_users must be pending or active")
		return
	}
	if err := w.auth.SetSetting(r.Context(), auth.SettingPlexNewUsers, req.PlexNewUsers); err != nil {
		w.internal(rw, err)
		return
	}
	w.apiUserSettings(rw, r)
}
