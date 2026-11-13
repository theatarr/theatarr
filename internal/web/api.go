package web

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/policy"
)

//go:embed openapi.json
var openapiJSON []byte

func (w *Web) mountAPI(mux *http.ServeMux) {
	b := APIBase
	w.handle(mux, "GET "+b+"/openapi.json", SignedIn, auth.PermBrowse, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.Write(openapiJSON)
	})
	w.handle(mux, "GET "+b+"/status", SignedIn, auth.PermAdmin, w.apiStatus)
	w.handle(mux, "GET "+b+"/health", SignedIn, auth.PermAdmin, w.apiHealth)
	w.handle(mux, "GET "+b+"/health/dangling", SignedIn, auth.PermAdmin, w.apiHealthDangling)
	w.handle(mux, "GET "+b+"/titles", SignedIn, auth.PermBrowse, w.apiTitles)
	w.handle(mux, "GET "+b+"/titles/{id}", SignedIn, auth.PermBrowse, w.apiTitle)
	w.handle(mux, "GET "+b+"/versions", SignedIn, auth.PermBrowse, w.apiVersions)
	w.handle(mux, "GET "+b+"/versions/{id}", SignedIn, auth.PermBrowse, w.apiVersion)
	w.handle(mux, "GET "+b+"/registry", SignedIn, auth.PermAdmin, w.apiRegistryList)
	w.handle(mux, "GET "+b+"/registry/{hash}", SignedIn, auth.PermAdmin, w.apiRegistry)
	w.handle(mux, "GET "+b+"/gateway", SignedIn, auth.PermAdmin, w.apiGateway)
	w.handle(mux, "GET "+b+"/policy", SignedIn, auth.PermAdmin, w.apiPolicy)
	w.handle(mux, "GET "+b+"/policy/validate", SignedIn, auth.PermAdmin, w.apiPolicyValidate)
	w.handle(mux, "GET "+b+"/events", SignedIn, auth.PermBrowse, w.apiEvents)
	w.mountAnimeAPI(mux)
	w.mountDevicesAPI(mux)
	w.mountAdminAPI(mux)
	w.mountJournalAPI(mux)
	w.mountLibraryAPI(mux)
	w.mountCopiesAPI(mux)
	w.mountActivityAPI(mux)
	w.mountJobsAPI(mux)
	w.mountErrorsAPI(mux)

	w.handle(mux, "POST "+b+"/auth/login", Public, 0, w.apiLogin)
	w.handle(mux, "GET "+b+"/auth/first-account", Public, 0, w.apiFirstAccountStatus)
	w.handle(mux, "POST "+b+"/auth/first-account", Public, 0, w.apiFirstAccountCreate)
	w.handle(mux, "POST "+b+"/auth/logout", SessionOnly, auth.PermBrowse, w.apiLogout)
	w.mountUsersAPI(mux)
	w.mountPlexLogin(mux)
	w.mountRequestsAPI(mux)
}

func (w *Web) internal(rw http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		w.log.Info("api: request cancelled", "err", err) // the client went away; the request line says 499
	} else {
		w.log.Error("api", "err", err)
	}
	writeJSON(rw, http.StatusInternalServerError, apiError{Error: "internal error"})
}

func badRequest(rw http.ResponseWriter, msg string) {
	writeJSON(rw, http.StatusBadRequest, apiError{Error: msg})
}

func notFound(rw http.ResponseWriter) {
	writeJSON(rw, http.StatusNotFound, apiError{Error: "not found"})
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func intParam(r *http.Request, name string, def int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, errors.New(name + " must be a non-negative integer")
	}
	return n, nil
}

var (
	titleKinds    = map[string]bool{"": true, "movie": true, "series": true}
	versionStates = map[string]bool{"": true, "wanted": true, "filled": true, "cutoff_met": true}
	jobStates     = map[string]bool{"": true, "queued": true, "running": true, "done": true, "failed": true}
	contentStates = map[string]bool{"": true, "present": true, "absent": true, "unreferenced": true, "gone": true, "unknown": true}
)

// titleFilter reads the library filters; the API and the library page share it.
func titleFilter(r *http.Request) (TitleFilter, error) {
	q := r.URL.Query()
	f := TitleFilter{Kind: q.Get("kind"), Q: strings.TrimSpace(q.Get("q")), With: q.Get("with"),
		Without: q.Get("without"), State: q.Get("state")}
	if !titleKinds[f.Kind] {
		return f, errors.New("kind must be movie or series")
	}
	if !versionStates[f.State] {
		return f, errors.New("state must be wanted, filled or cutoff_met")
	}
	var err error
	if f.Limit, err = intParam(r, "limit", 100); err != nil {
		return f, err
	}
	if f.Offset, err = intParam(r, "offset", 0); err != nil {
		return f, err
	}
	f.TitleGridFilter, err = gridFilter(r)
	return f, err
}

func (w *Web) apiStatus(rw http.ResponseWriter, r *http.Request) {
	s, err := w.status(r.Context())
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, s)
}

// HealthReport is GET /health: every check with its result (ok ones too), and the worst type among them.
type HealthReport struct {
	Status string          `json:"status"`
	Checks []health.Result `json:"checks"`
}

// apiHealth runs the health checks (cached for a minute; ?refresh=1 runs them now).
func (w *Web) apiHealth(rw http.ResponseWriter, r *http.Request) {
	rep := HealthReport{Status: health.OK, Checks: []health.Result{}}
	if w.health != nil {
		if r.URL.Query().Get("refresh") == "1" {
			w.health.Refresh()
		}
		rep.Checks = w.health.Results(r.Context())
		rep.Status = health.Worst(rep.Checks)
	}
	writeJSON(rw, http.StatusOK, rep)
}

func (w *Web) apiTitles(rw http.ResponseWriter, r *http.Request) {
	f, err := titleFilter(r)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	perms := w.permsOf(r)
	if f.Provider != "" && !perms.SeeFiles {
		writeJSON(rw, http.StatusForbidden, apiError{Error: "the provider filter needs the see-files permission"})
		return
	}
	if (f.Sort == "size" || f.Sort == "quality" || f.Sort == "activity") && !perms.SeeFiles {
		writeJSON(rw, http.StatusForbidden, apiError{Error: "sorting by " + f.Sort + " needs the see-files permission"})
		return
	}
	if !perms.SeeFiles {
		if msg := f.plainFilter(); msg != "" {
			writeJSON(rw, http.StatusForbidden, apiError{Error: msg})
			return
		}
	}
	page, err := w.listTitles(r.Context(), f)
	if err != nil {
		w.internal(rw, err)
		return
	}
	for i := range page.Items {
		redactRowFor(perms, &page.Items[i])
	}
	writeJSON(rw, http.StatusOK, page)
}

func (w *Web) apiTitle(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	d, err := w.getTitle(r.Context(), id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err == nil {
		err = w.enrichTitle(r.Context(), d)
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	if !canManage(w.permsOf(r)) {
		for i := range d.Versions {
			redactCeiling(d.Versions[i].Upgrade)
		}
	}
	if !w.permsOf(r).SeeFiles {
		redactTitle(d)
	}
	writeJSON(rw, http.StatusOK, d)
}

func (w *Web) apiVersions(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := VersionFilter{Key: q.Get("key"), State: q.Get("state")}
	if !versionStates[f.State] {
		badRequest(rw, "state must be wanted, filled or cutoff_met")
		return
	}
	if !w.permsOf(r).SeeFiles {
		// A caller without "see files" sees cutoff_met as filled (perms.go).
		if f.State == "cutoff_met" {
			writeJSON(rw, http.StatusForbidden, apiError{Error: "filtering by cutoff needs the see-files permission"})
			return
		}
		f.plainState = true
	}
	switch q.Get("has_file") {
	case "":
	case "true", "1":
		t := true
		f.HasFile = &t
	case "false", "0":
		t := false
		f.HasFile = &t
	default:
		badRequest(rw, "has_file must be true or false")
		return
	}
	var err error
	if f.Limit, err = intParam(r, "limit", 100); err != nil {
		badRequest(rw, err.Error())
		return
	}
	if f.Offset, err = intParam(r, "offset", 0); err != nil {
		badRequest(rw, err.Error())
		return
	}
	vs, err := w.listVersions(r.Context(), f)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if !w.permsOf(r).SeeFiles {
		for i := range vs {
			redactVersion(&vs[i])
		}
	}
	writeJSON(rw, http.StatusOK, map[string]any{"items": vs, "limit": max(f.Limit, 1), "offset": f.Offset})
}

func (w *Web) apiVersion(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	vs, err := w.versions(r.Context(), "v.id = ?", []any{id}, 0, 0)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if len(vs) == 0 {
		notFound(rw)
		return
	}
	if !w.permsOf(r).SeeFiles {
		redactVersion(&vs[0])
	}
	writeJSON(rw, http.StatusOK, vs[0])
}

func (w *Web) apiRegistryList(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !contentStates[q.Get("state")] {
		badRequest(rw, "state must be present, absent, unreferenced, gone or unknown")
		return
	}
	limit, err := intParam(r, "limit", 100)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	offset, err := intParam(r, "offset", 0)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	items, total, err := w.listContent(r.Context(), q.Get("q"), q.Get("state"), limit, offset)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"total": total, "items": items})
}

func (w *Web) apiRegistry(rw http.ResponseWriter, r *http.Request) {
	h := normHash(r.PathValue("hash"))
	if !validHash(h) {
		badRequest(rw, "hash must be a 40-character hex info hash")
		return
	}
	c, err := w.getContent(r.Context(), h)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, c)
}

func (w *Web) apiGateway(rw http.ResponseWriter, r *http.Request) {
	gs, err := w.gatewayState(r.Context())
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, gs)
}

// PolicySummary is the running policy, without anything that could be a secret: env var names, not values.
type PolicySummary struct {
	Dir      string           `json:"dir"`
	Files    []string         `json:"files"`
	Versions []VersionPolicy  `json:"versions"`
	Indexers []IndexerPolicy  `json:"indexers"`
	Budgets  []string         `json:"budgets"`
	Hosts    []map[string]any `json:"outbound_hosts"`
}

// VersionPolicy is one version policy, as the settings page shows it.
type VersionPolicy struct {
	GrabState VersionGrabState `json:"grab_state"`
	Key       string           `json:"key"`
	Mode      string           `json:"mode"`
	Kind      string           `json:"kind"`
	Profile   string           `json:"profile"`
	Library   string           `json:"library"`
	Storage   string           `json:"storage"`
	Indexers  []string         `json:"indexers"`
	Providers []string         `json:"providers"`
}

// IndexerPolicy is one indexer.
type IndexerPolicy struct {
	Key     string `json:"key"`
	Kind    string `json:"kind"`
	Privacy string `json:"privacy"`
	Budget  string `json:"budget,omitempty"`
	Proxy   bool   `json:"proxy"`
}

func summarise(p *policy.Policy) PolicySummary {
	s := PolicySummary{Files: []string{}, Versions: []VersionPolicy{}, Indexers: []IndexerPolicy{}, Budgets: []string{},
		Hosts: []map[string]any{}}
	if p == nil {
		return s
	}
	s.Dir, s.Files = p.Dir, append(s.Files, p.Files...)
	for _, k := range sortedKeys(p.Versions) {
		v := p.Versions[k]
		s.Versions = append(s.Versions, VersionPolicy{GrabState: versionGrabState(p, k), Key: k, Mode: string(v.Mode), Kind: string(v.Kind),
			Profile: v.Profile, Library: v.Library, Storage: string(v.Storage),
			Indexers: append([]string{}, v.Indexers...), Providers: append([]string{}, v.Providers...)})
	}
	for _, k := range sortedKeys(p.Indexers) {
		ix := p.Indexers[k]
		priv := string(ix.Privacy)
		if priv == "" {
			priv = "unknown"
		}
		s.Indexers = append(s.Indexers, IndexerPolicy{Key: k, Kind: ix.Kind, Privacy: priv, Budget: ix.Budget,
			Proxy: ix.Proxy != ""})
	}
	s.Budgets = append(s.Budgets, sortedKeys(p.Budgets)...)
	for _, h := range p.OutboundHosts() {
		s.Hosts = append(s.Hosts, map[string]any{"host": h.Host, "used_by": h.Users})
	}
	return s
}

func (w *Web) apiPolicy(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, http.StatusOK, summarise(w.pol))
}

// ValidateResult is a fresh validation of the policy directory. It never reloads the running policy.
type ValidateResult struct {
	Dir      string   `json:"dir"`
	OK       bool     `json:"ok"`
	Problems []string `json:"problems"`
}

func (w *Web) validatePolicy() ValidateResult {
	res := ValidateResult{Dir: w.policyDir, Problems: []string{}}
	if w.policyDir == "" {
		res.Problems = append(res.Problems, "no policy directory configured")
		return res
	}
	_, err := policy.Load(w.policyDir)
	if err == nil {
		res.OK = true
		return res
	}
	var verr *policy.Errors
	if errors.As(err, &verr) {
		for _, fe := range verr.List {
			res.Problems = append(res.Problems, fe.Error())
		}
	} else {
		res.Problems = append(res.Problems, err.Error())
	}
	return res
}

func (w *Web) apiPolicyValidate(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, http.StatusOK, w.validatePolicy())
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (w *Web) apiLogin(rw http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		badRequest(rw, "want JSON {\"username\", \"password\"}")
		return
	}
	token, err := w.auth.Login(r.Context(), req.Username, req.Password, auth.ClientAddr(r), r.UserAgent())
	var thr *auth.Throttled
	switch {
	case errors.As(err, &thr):
		rw.Header().Set("Retry-After", retryAfter(thr, w.now()))
		writeJSON(rw, http.StatusTooManyRequests, apiError{Error: "too many failed logins; try again later"})
		return
	case errors.Is(err, auth.ErrInvalid):
		writeJSON(rw, http.StatusUnauthorized, apiError{Error: "wrong username or password"})
		return
	case err != nil:
		w.internal(rw, err)
		return
	}
	w.auth.SetSessionCookie(rw, r, token)
	// The CSRF token for this new session, so a script can go on to POST.
	writeJSON(rw, http.StatusOK, map[string]any{"username": req.Username, "csrf_token": w.auth.SessionCSRF(token)})
}

// FirstAccountStatus is GET auth/first-account, for the sign-in page. NeedsAccount: the user table is empty, so the
// page offers "Create your admin account". PasswordPending: an older version's generated admin password file is
// still unused (only reported once an account exists).
type FirstAccountStatus struct {
	NeedsAccount    bool `json:"needs_account"`
	PasswordPending bool `json:"password_pending"`
}

func (w *Web) apiFirstAccountStatus(rw http.ResponseWriter, r *http.Request) {
	need, err := w.auth.NeedsFirstAccount(r.Context())
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, FirstAccountStatus{NeedsAccount: need, PasswordPending: !need && w.auth.PasswordPending()})
}

// apiFirstAccountCreate creates the first account, an admin, and signs it in. It takes a username and a password
// only, and works only while no account exists; afterwards it answers 409.
func (w *Web) apiFirstAccountCreate(rw http.ResponseWriter, r *http.Request) {
	var req loginRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		badRequest(rw, "want JSON {\"username\", \"password\"}")
		return
	}
	token, err := w.auth.CreateFirstAdmin(r.Context(), req.Username, req.Password, auth.ClientAddr(r), r.UserAgent())
	var bad auth.InvalidAccount
	var thr *auth.Throttled
	switch {
	case errors.Is(err, auth.ErrClaimed):
		writeJSON(rw, http.StatusConflict, apiError{Error: "this install already has an account; sign in instead"})
		return
	case errors.As(err, &bad):
		badRequest(rw, bad.Error())
		return
	case errors.As(err, &thr):
		rw.Header().Set("Retry-After", retryAfter(thr, w.now()))
		writeJSON(rw, http.StatusTooManyRequests, apiError{Error: "busy; try again in a moment"})
		return
	case err != nil:
		w.internal(rw, err)
		return
	}
	w.auth.SetSessionCookie(rw, r, token)
	writeJSON(rw, http.StatusOK, map[string]any{"username": strings.TrimSpace(req.Username),
		"csrf_token": w.auth.SessionCSRF(token)})
}

func (w *Web) apiLogout(rw http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookie); err == nil {
		if err := w.auth.Logout(r.Context(), c.Value); err != nil {
			w.internal(rw, err)
			return
		}
	}
	w.auth.ClearSessionCookie(rw, r)
	rw.WriteHeader(http.StatusNoContent)
}
