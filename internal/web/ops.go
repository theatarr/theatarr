package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// Recovery actions (docs/design/ui.md "Recovery actions"): what an operator does by hand when automation got it wrong.
// Grab a release the live search found; remove a download (optionally blocklisting it and searching again) or retry
// it; unblock a blocklisted release; remove a copy or a whole title from the library; unlink one of Theatarr's library
// symlinks. They run on the same parts the arr views use for the same things (Ops is internal/arrview.Service:
// its decision engine, router and library; router.Dismiss and router.Unblock; catalog.Remove), each is recorded in
// the journal (kind action, with who), and each needs the manage permission (managers and admins:
// docs/design/ui.md, "search now, show releases, relink, remove a version").
//
// Hard rules. Nothing here deletes a debrid torrent: they are shared by every library, so a removal ends only Theatarr's
// record of a download. Nothing deletes a real file: an unlink moves one of Theatarr's own symlinks to the trash through
// the library's gate, and what it points at is never touched. A grab and a retry go through the router like any
// route (gate, blocklist, pacing), so a shadow version or a router that is off places nothing.

// Ops is the arr views' service as the recovery actions use it (arrview.Service in theatarr serve). nil answers 503.
type Ops interface {
	GrabRelease(ctx context.Context, titleID int64, version string, rel indexer.Release, override bool,
		source string) (*GrabbedRelease, error)
	RetryDownload(ctx context.Context, id int64, source string) (*RetriedDownload, error)
	UnlinkLink(ctx context.Context, linkID int64, reason string) (*UnlinkedLink, error)
}

// GrabbedRelease is what Ops.GrabRelease did: the release decided again now, whether it was grabbed although
// rejected, and the route job. Decision may be set with a refusal (the rejections).
type GrabbedRelease struct {
	Decision   *decide.Decision
	Overridden bool
	Job        int64
}

// RetriedDownload is what Ops.RetryDownload did; What is the outcome in words ("routed again", "imported", ...).
type RetriedDownload struct {
	TitleID int64
	Version string
	Hash    string
	Release string
	What    string
	// Outcome is what an import now did: imported, partial, rejected, held, waiting or dry_run; empty for a route.
	Outcome string
	Job     int64
}

// UnlinkedLink is what Ops.UnlinkLink did; Trash is "" when the link was already gone.
type UnlinkedLink struct {
	TitleID  int64
	Path     string
	Versions []string
	Trash    string
}

// refusedErr is how Ops says it did nothing and why (arrview.Refused): 409 with the reason.
type refusedErr interface {
	error
	Refused() bool
}

func (w *Web) mountOpsAPI(mux *http.ServeMux) {
	b := APIBase
	w.handle(mux, "POST "+b+"/titles/{id}/releases/grab", SignedIn, auth.PermManage, w.apiGrab)
	w.handle(mux, "POST "+b+"/titles/{id}/releases/add", SignedIn, auth.PermAdmin, w.apiAddRelease)
	w.handle(mux, "GET "+b+"/titles/{id}/removal", SignedIn, auth.PermManage, w.apiRemovalScope)
	w.handle(mux, "POST "+b+"/titles/{id}/remove", SignedIn, auth.PermManage, w.apiRemove)
	w.handle(mux, "POST "+b+"/links/{id}/unlink", SignedIn, auth.PermManage, w.apiUnlink)
	w.handle(mux, "POST "+b+"/activity/downloads/{id}/remove", SignedIn, auth.PermManage, w.apiRemoveDownload)
	w.handle(mux, "POST "+b+"/activity/downloads/{id}/retry", SignedIn, auth.PermManage, w.apiRetryDownload)
	w.handle(mux, "POST "+b+"/activity/blocked/unblock", SignedIn, auth.PermManage, w.apiUnblock)
	w.mountRenameAPI(mux)
	w.mountReassignAPI(mux)
}

// actor names the caller for the journal: the user name, or key:<name> for an API key.
func actor(r *http.Request) string {
	id := auth.FromContext(r.Context())
	if id == nil {
		return ""
	}
	if id.Kind == "apikey" {
		return "key:" + id.KeyName
	}
	return id.Username
}

// record writes an action to the journal after its change; a failure is logged, not answered (the change is done).
func (w *Web) record(ctx context.Context, a journal.Action) {
	if a.At.IsZero() {
		a.At = w.now()
	}
	if _, err := w.journal.Record(ctx, a); err != nil {
		w.log.Error("journal: recording an action", "action", a.Action, "err", err)
	}
}

// opsError answers an Ops error: 409 with the reason when it refused, 404 when the row is gone, else 500.
func (w *Web) opsError(rw http.ResponseWriter, err error) {
	var ref refusedErr
	switch {
	case errors.As(err, &ref):
		writeJSON(rw, http.StatusConflict, apiError{Error: ref.Error()})
	case errors.Is(err, sql.ErrNoRows):
		notFound(rw)
	default:
		w.internal(rw, err)
	}
}

func (w *Web) noOps(rw http.ResponseWriter) bool {
	if w.ops == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "the router and library are not running"})
		return true
	}
	return false
}

// ---- Grab ----

// grabTTL is how long a live search's release can be grabbed (the arrs' release cache: 30 minutes).
const grabTTL = 30 * time.Minute

type liveRelease struct {
	rel indexer.Release
	at  time.Time
}

// liveCache keeps the releases of the panel's live searches by title, version and hash, so a grab routes exactly
// the release the person saw (with its indexer and privacy), never one rebuilt from what the browser sends.
type liveCache struct {
	mu sync.Mutex
	m  map[string]liveRelease
}

func liveKey(titleID int64, version, hash string) string {
	return strconv.FormatInt(titleID, 10) + "|" + version + "|" + hash
}

func (c *liveCache) put(titleID int64, version string, sr *router.Searched, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]liveRelease{}
	}
	for k, v := range c.m {
		if now.Sub(v.at) > grabTTL {
			delete(c.m, k)
		}
	}
	for _, d := range sr.Ranked {
		rel, ok := sr.Releases[d]
		h := indexer.NormalizeHash(rel.InfoHash)
		if !ok || h == "" {
			continue
		}
		k := liveKey(titleID, version, h)
		if old, seen := c.m[k]; seen && old.at.Equal(now) {
			continue // the same hash from another indexer in this search: the better ranked one stays
		}
		c.m[k] = liveRelease{rel: rel, at: now}
	}
}

func (c *liveCache) get(titleID int64, version, hash string, now time.Time) (indexer.Release, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[liveKey(titleID, version, indexer.NormalizeHash(hash))]
	if !ok || now.Sub(v.at) > grabTTL {
		return indexer.Release{}, false
	}
	return v.rel, true
}

// keepLive remembers a live answer's releases for a grab and says whether the panel may offer one.
func (w *Web) keepLive(out *Releases, sr *router.Searched) {
	w.live.put(out.TitleID, out.Version, sr, w.now())
	out.Grab = w.ops != nil
}

// GrabRequest is the body of POST /titles/{id}/releases/grab.
type GrabRequest struct {
	Version  string `json:"version"`
	InfoHash string `json:"info_hash"`
	// Override grabs a release the decision engine rejects now (the person saw the rejections and confirmed).
	Override bool `json:"override"`
}

// GrabResult is the answer of a grab.
type GrabResult struct {
	Version    string   `json:"version"`
	Release    string   `json:"release"`
	InfoHash   string   `json:"info_hash"`
	Accepted   bool     `json:"accepted"`
	Overridden bool     `json:"overridden"`
	Rejections []string `json:"rejections,omitempty"`
	Message    string   `json:"message"`
}

// POST /titles/{id}/releases/grab {version, info_hash, override}: route one release of the panel's last live search
// (30 minutes) for the version. The release is decided again now; a rejected one needs override (409 with the
// rejections otherwise). Only for a version a search would run for (mode active, router on).
func (w *Web) apiGrab(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var req GrabRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	hash := indexer.NormalizeHash(req.InfoHash)
	if req.Version == "" || hash == "" {
		badRequest(rw, "version and info_hash are required")
		return
	}
	if w.noOps(rw) {
		return
	}
	if w.pol == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "no policy loaded"})
		return
	}
	if why := w.searchBlock(req.Version); why != "" && why != "The acquirer is not running" {
		writeJSON(rw, http.StatusConflict, apiError{Error: "Not grabbed: " + why})
		return
	}
	rel, ok := w.live.get(id, req.Version, hash, w.now())
	if !ok {
		writeJSON(rw, http.StatusNotFound, apiError{Error: "the release is not in a live search of the last 30 minutes: search again"})
		return
	}
	by := actor(r)
	g, err := w.ops.GrabRelease(ctx, id, req.Version, rel, req.Override, "ui:grab:"+by)
	w.grabbed(rw, r, id, req.Version, rel, g, err, "")
}

// grabbed answers a grab or a manual add from what Ops did and records it; how is "" for a grab from a search and
// "manual add" for a release added by its hash.
func (w *Web) grabbed(rw http.ResponseWriter, r *http.Request, id int64, version string, rel indexer.Release,
	g *GrabbedRelease, err error, how string) {
	ctx, by, hash := r.Context(), actor(r), indexer.NormalizeHash(rel.InfoHash)
	out := GrabResult{Version: version, Release: rel.Title, InfoHash: hash}
	if g != nil && g.Decision != nil {
		out.Accepted, out.Overridden = g.Decision.Accepted, g.Overridden
		for _, rj := range g.Decision.Rejections {
			m := rj.Message
			if m == "" {
				m = rj.Reason
			}
			out.Rejections = append(out.Rejections, m)
		}
	}
	if err != nil {
		var ref refusedErr
		if errors.As(err, &ref) {
			writeJSON(rw, http.StatusConflict, apiError{Error: ref.Error(), Detail: out.Rejections})
			return
		}
		w.internal(rw, err)
		return
	}
	out.Message = "Sent to the router: it adds the release at the first provider that has it"
	if out.Overridden {
		verb := "Grabbed"
		if how != "" {
			verb = "Added"
		}
		out.Message = verb + " although rejected; " + strings.ToLower(out.Message[:1]) + out.Message[1:]
	}
	detail := "routed"
	if out.Overridden {
		detail = "routed although rejected: " + strings.Join(out.Rejections, "; ")
	}
	target := rel.Indexer
	if how != "" {
		detail, target = how+": "+detail, how
	}
	w.record(ctx, journal.Action{Action: journal.ActGrab, TitleID: id, Version: version, InfoHash: hash,
		Release: rel.Title, Target: target, Detail: detail, By: by})
	writeJSON(rw, http.StatusOK, out)
}

// ---- Add by magnet or hash ----

// addOps is the part of Ops that routes a release no search found (arrview.Service.AddRelease in theatarr serve).
type addOps interface {
	AddRelease(ctx context.Context, titleID int64, version string, rel indexer.Release, override bool,
		source string) (*GrabbedRelease, error)
}

// AddReleaseRequest is the body of POST /titles/{id}/releases/add.
type AddReleaseRequest struct {
	Version string `json:"version"`
	// Magnet or InfoHash names the torrent: a magnet link's btih, or a 40-character hex (32 base32) v1 hash.
	Magnet   string `json:"magnet,omitempty"`
	InfoHash string `json:"info_hash,omitempty"`
	// Name is the release name the rules judge; default the magnet's dn.
	Name string `json:"name,omitempty"`
	// Override adds a release the decision engine rejects (the person saw the rejections and confirmed).
	Override bool `json:"override"`
}

// magnetName is a magnet's display name (dn), "" when it has none.
func magnetName(m string) string {
	_, q, ok := strings.Cut(strings.TrimSpace(m), "?")
	if !ok {
		return ""
	}
	for _, part := range strings.FieldsFunc(q, func(r rune) bool { return r == '&' || r == '#' }) {
		k, v, _ := strings.Cut(part, "=")
		if !strings.EqualFold(k, "dn") {
			continue
		}
		if u, err := url.QueryUnescape(v); err == nil {
			v = u
		}
		return strings.TrimSpace(v)
	}
	return ""
}

// magnetHash is the v1 info hash of a magnet a person pasted, "" unless the magnet has exactly one urn:btih exact
// topic and all of its value is a hash (40 hex or 32 base32 characters). The shared indexer.HashFromMagnet is lenient
// for what indexers send (it reads the hash up to the first other character and skips an xt it cannot read); a
// person's typo is refused here instead. The parameters are split as it splits them.
func magnetHash(m string) string {
	m = strings.TrimSpace(m)
	if len(m) < 8 || !strings.EqualFold(m[:8], "magnet:?") {
		return ""
	}
	unescape := func(s string) string {
		if u, err := url.QueryUnescape(s); err == nil {
			return u
		}
		return s
	}
	hash, n := "", 0
	for _, part := range strings.FieldsFunc(m[8:], func(r rune) bool { return r == '&' || r == '#' }) {
		k, v, _ := strings.Cut(part, "=")
		k = strings.ToLower(strings.TrimSpace(unescape(k)))
		if k != "xt" && !strings.HasPrefix(k, "xt.") {
			continue
		}
		v = strings.TrimSpace(unescape(v))
		if len(v) < 9 || !strings.EqualFold(v[:9], "urn:btih:") {
			continue
		}
		n++
		hash = indexer.NormalizeHash(v[9:])
	}
	if n != 1 || hash == "" || hash != indexer.HashFromMagnet(m) {
		return ""
	}
	return hash
}

// POST /titles/{id}/releases/add {version, magnet | info_hash, name, override}: route a release the person names by
// magnet or hash for the version, one no search of ours found (admins). It takes the grab's route from the decision
// on: decided now under the given name, a rejected one needs override (409 with the rejections otherwise), then the
// router with a person's grab as the source, so provider order, pacing, the cached-only rule, the record of our own
// adds and the history are the grab's. The release has no indexer, and no size or seeders are known: a size limit set
// for its quality could not be checked, which is a rejection like any other (decide: UnknownSize). Only for a
// version a search would run for (mode active, router on). Nothing is sent to a provider here.
func (w *Web) apiAddRelease(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var req AddReleaseRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	if req.Version == "" {
		badRequest(rw, "version is required")
		return
	}
	req.Magnet, req.InfoHash = strings.TrimSpace(req.Magnet), strings.TrimSpace(req.InfoHash)
	var hash string
	switch {
	case req.Magnet != "" && req.InfoHash != "":
		badRequest(rw, "Give a magnet link or an info hash, not both")
		return
	case req.Magnet != "":
		hash = magnetHash(req.Magnet)
	case req.InfoHash != "":
		hash = indexer.NormalizeHash(req.InfoHash)
	default:
		badRequest(rw, "Give a magnet link or an info hash")
		return
	}
	if hash == "" {
		badRequest(rw, "Not a magnet link with one BitTorrent v1 hash, nor a 40-character info hash")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = magnetName(req.Magnet)
	}
	if name == "" {
		badRequest(rw, "Give the release name: the rules judge the release by it")
		return
	}
	if w.noOps(rw) {
		return
	}
	ops, ok := w.ops.(addOps)
	if !ok {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "adding a release by hash is unavailable"})
		return
	}
	if w.pol == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "no policy loaded"})
		return
	}
	var exists int
	if err := w.st.Reader().QueryRowContext(ctx, `SELECT 1 FROM title WHERE id = ?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	} else if err != nil {
		w.internal(rw, err)
		return
	}
	if why := w.searchBlock(req.Version); why != "" && why != "The acquirer is not running" {
		writeJSON(rw, http.StatusConflict, apiError{Error: "Not added: " + why})
		return
	}
	// No size, seeders or peers are known for a release nobody searched for: 0 and -1 are "unknown".
	rel := indexer.Release{Title: name, InfoHash: hash, Seeders: -1, Peers: -1}
	g, err := ops.AddRelease(ctx, id, req.Version, rel, req.Override, "ui:grab:"+actor(r))
	w.grabbed(rw, r, id, req.Version, rel, g, err, "manual add")
}

// ---- Downloads ----

// BlocklistUI is the router_blocklist source of a removal with blocklist in Activity.
const BlocklistUI = "ui"

// RemoveDownloadRequest is the body of POST /activity/downloads/{id}/remove.
type RemoveDownloadRequest struct {
	// Blocklist keeps the release from being routed again for the version.
	Blocklist bool `json:"blocklist"`
	// Search starts a search for the copy at once (only with blocklist, or the same release could come back).
	Search bool `json:"search"`
}

// RemoveDownloadResult is the answer of a removal.
type RemoveDownloadResult struct {
	ID          int64          `json:"id"`
	Ended       bool           `json:"ended"`
	State       string         `json:"state"`
	Blocklisted bool           `json:"blocklisted"`
	Search      *SearchOutcome `json:"search,omitempty"`
	Message     string         `json:"message"`
}

// POST /activity/downloads/{id}/remove {blocklist, search}: end Theatarr's record of a download (router.Dismiss, as the
// arr views' DELETE queue does): a download still in hand becomes failed, nothing is removed at the provider (the
// debrid torrent is shared by every library), files already linked stay. With blocklist the release is never
// routed again for the copy; with search a search for the copy starts now.
func (w *Web) apiRemoveDownload(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var req RemoveDownloadRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	if req.Search && !req.Blocklist {
		badRequest(rw, "search needs blocklist: without it the same release could be grabbed again")
		return
	}
	by := actor(r)
	d, err := router.Dismiss(ctx, w.st, id, "removed in Activity by "+orSomeone(by), req.Blocklist, BlocklistUI,
		store.Millis(w.now()))
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	// Dismissal is separate from the failure reason and ownership history, which retries and the journal still read.
	if _, err := w.st.Writer().ExecContext(ctx, `UPDATE download SET dismissed_at=? WHERE id=? AND state='failed'`, store.Millis(w.now()), id); err != nil {
		w.internal(rw, err)
		return
	}
	out := RemoveDownloadResult{ID: id, Ended: d.Ended, State: d.State, Blocklisted: req.Blocklist && d.Hash != ""}
	switch {
	case d.Ended:
		out.Message = "Removed: Theatarr stopped following this download; the provider's copy is left alone"
	default:
		if d.State == router.StateFailed {
			out.Message = "Dismissed: the failed download stays in history; the provider's copy is left alone"
		} else {
			out.Message = "The download had already finished"
		}
	}
	if out.Blocklisted {
		out.Message += "; the release is blocklisted for " + d.Version
	}
	detail := []string{"ended " + d.State}
	if !d.Ended {
		detail[0] = "was " + d.State
	}
	if out.Blocklisted {
		detail = append(detail, "blocklisted")
	}
	if req.Search {
		s, err := w.searchCopy(ctx, d.TitleID, d.Version)
		if err != nil {
			w.internal(rw, err)
			return
		}
		out.Search = s
		out.Message += ". " + s.Message
		detail = append(detail, "search: "+s.Message)
	}
	w.record(ctx, journal.Action{Action: journal.ActRemoveDownload, TitleID: d.TitleID, Version: d.Version,
		InfoHash: d.Hash, Release: d.Release, DownloadID: id, Target: "download " + strconv.FormatInt(id, 10),
		Detail: strings.Join(detail, "; "), By: by})
	w.log.Info("download removed by hand", "download", id, "version", d.Version, "ended", d.Ended,
		"blocklisted", out.Blocklisted, "search", req.Search, "by", by)
	writeJSON(rw, http.StatusOK, out)
}

func orSomeone(s string) string {
	if s == "" {
		return "someone"
	}
	return s
}

// searchCopy searches one copy of a title now when it is monitored (searchNow's rules: mode active, router on).
func (w *Web) searchCopy(ctx context.Context, titleID int64, version string) (*SearchOutcome, error) {
	on, err := w.versionsOn(ctx, titleID)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(on, version) {
		return &SearchOutcome{Versions: []string{}, Skipped: []catalog.Skip{},
			Message: "Not searched: the " + version + " library is not monitored"}, nil
	}
	if w.pol == nil {
		return &SearchOutcome{Versions: []string{}, Skipped: []catalog.Skip{}, Message: "Not searched: no policy loaded"}, nil
	}
	return w.searchNow(ctx, titleID, []string{version})
}

// RetryResult is the answer of a retry.
type RetryResult struct {
	ID      int64  `json:"id"`
	Version string `json:"version"`
	Message string `json:"message"`
	// Outcome is what an import now did (imported, partial, rejected, held, waiting, dry_run); empty for a route.
	Outcome string `json:"outcome,omitempty"`
}

// POST /activity/downloads/{id}/retry: try a download again. A failed one's release is routed again for its copy
// (refused while it is blocklisted); one that finished but is not linked is imported now. 409 with the reason when
// there is nothing to retry.
func (w *Web) apiRetryDownload(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	if w.noOps(rw) {
		return
	}
	by := actor(r)
	res, err := w.ops.RetryDownload(ctx, id, "ui:retry:"+strconv.FormatInt(id, 10))
	if err != nil {
		w.opsError(rw, err)
		return
	}
	out := RetryResult{ID: id, Version: res.Version, Outcome: res.Outcome}
	switch {
	case res.What == "routed again":
		out.Message = "Sent to the router again: a new download starts when a provider takes it"
	case res.Outcome == "imported" || (res.Outcome == "" && res.What == "imported"):
		out.Message = "Imported now"
	case res.Outcome == "rejected":
		out.Message = "Not imported: " + res.What
	case res.Outcome == "held":
		out.Message = "Not imported, " + res.What
	default:
		out.Message = "Import tried now: " + res.What
	}
	w.record(ctx, journal.Action{Action: journal.ActRetryDownload, TitleID: res.TitleID, Version: res.Version,
		InfoHash: res.Hash, Release: res.Release, DownloadID: id, Target: "download " + strconv.FormatInt(id, 10),
		Detail: res.What, By: by})
	writeJSON(rw, http.StatusOK, out)
}

// UnblockRequest is the body of POST /activity/blocked/unblock.
type UnblockRequest struct {
	Version  string `json:"version"`
	InfoHash string `json:"info_hash"`
	// Search starts a search for the copy once the release may be routed again.
	Search bool `json:"search"`
}

// UnblockResult is the answer of an unblock.
type UnblockResult struct {
	Version  string         `json:"version"`
	InfoHash string         `json:"info_hash"`
	Release  string         `json:"release"`
	Search   *SearchOutcome `json:"search,omitempty"`
	Message  string         `json:"message"`
}

// POST /activity/blocked/unblock {version, info_hash, search}: take a release off a copy's blocklist
// (router.Unblock), recorded in the journal in the same transaction; with search, a search for the copy starts.
func (w *Web) apiUnblock(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req UnblockRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	hash := strings.ToLower(strings.TrimSpace(req.InfoHash))
	if req.Version == "" || hash == "" {
		badRequest(rw, "version and info_hash are required")
		return
	}
	by := actor(r)
	var b *router.Blocked
	err := w.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		if b, err = router.Unblock(ctx, tx, req.Version, hash); err != nil {
			return err
		}
		_, err = journal.RecordAction(ctx, tx, journal.Action{Action: journal.ActUnblock, TitleID: b.TitleID,
			Version: b.Version, InfoHash: b.Hash, Release: b.Release, DownloadID: b.DownloadID, Target: b.Hash,
			Detail: "was blocked by " + b.Source + ": " + b.Reason, By: by, At: w.now()})
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	out := UnblockResult{Version: b.Version, InfoHash: b.Hash, Release: b.Release,
		Message: "Unblocked: the release may be grabbed again for " + b.Version}
	if req.Search && b.TitleID > 0 {
		s, err := w.searchCopy(ctx, b.TitleID, b.Version)
		if err != nil {
			w.internal(rw, err)
			return
		}
		out.Search = s
		out.Message += ". " + s.Message
	}
	w.log.Info("release unblocked by hand", "version", b.Version, "hash", b.Hash, "by", by)
	writeJSON(rw, http.StatusOK, out)
}

// ---- Copies, titles and files ----

// RemovalCopy is one copy a removal would take, with what goes with it.
type RemovalCopy struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Links are the copy's active library symlinks (unlinked only when asked); Downloads its downloads in hand,
	// whose records end (the provider's copies are left alone).
	Links     int `json:"links"`
	Downloads int `json:"downloads"`
	// ManagedBy is the arr instance that still owns the copy; such a copy cannot be removed here.
	ManagedBy string `json:"managed_by,omitempty"`
}

// RemovalScope is what removing copies (or the whole title) would do: GET /titles/{id}/removal?versions=.
type RemovalScope struct {
	TitleID int64         `json:"title_id"`
	Name    string        `json:"name"`
	Copies  []RemovalCopy `json:"copies"`
	// Title is true when the copies are every copy the title has: the title leaves the library and the arr views.
	Title bool `json:"title"`
	// Leftover are copies in scope removed before whose library symlinks are still active (a removal with unlink
	// whose file moves failed part way, or one without unlink): a removal with unlink moves their links to the trash
	// too, and finishes the earlier one.
	Leftover []RemovalCopy `json:"leftover"`
}

// removalScope reads what removing keys (every copy when empty) of a title would touch.
func (w *Web) removalScope(ctx context.Context, titleID int64, keys []string) (*RemovalScope, error) {
	t, err := w.findTitle(ctx, titleID, "", 0)
	if err != nil || t == nil {
		return nil, err
	}
	out := &RemovalScope{TitleID: titleID, Name: t.name, Copies: []RemovalCopy{}, Leftover: []RemovalCopy{}}
	var all []string
	var named []copyName
	err = scanAll(ctx, w.st.Reader(), `SELECT v.policy_key, COALESCE(vs.instance, ''),
		(SELECT COUNT(*) FROM link l JOIN link_version lv ON lv.link_id = l.id WHERE lv.version_id = v.id
			AND l.state = 'active' AND l.kind = 'symlink'),
		(SELECT COUNT(*) FROM download d WHERE d.version_id = v.id AND d.state NOT IN ('failed', 'imported'))
		FROM version v LEFT JOIN version_source vs ON vs.version_id = v.id WHERE v.title_id = ?
		AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id) ORDER BY v.policy_key`,
		[]any{titleID}, func(r *sql.Rows) error {
			var c RemovalCopy
			var inst string
			if err := r.Scan(&c.Key, &inst, &c.Links, &c.Downloads); err != nil {
				return err
			}
			all = append(all, c.Key)
			c.ManagedBy = w.managedBy(inst)
			named = append(named, copyName{key: c.Key, label: w.versionLabel(c.Key), owner: ownerOf(c.ManagedBy)})
			if len(keys) > 0 && !slices.Contains(keys, c.Key) {
				return nil
			}
			out.Copies = append(out.Copies, c)
			return nil
		})
	if err != nil {
		return nil, err
	}
	// Named apart among the title's copies, as the title page names them.
	labels := distinctLabels(named)
	for i := range out.Copies {
		out.Copies[i].Label = labels[out.Copies[i].Key]
	}
	var hidden []string
	err = scanAll(ctx, w.st.Reader(), `SELECT v.policy_key, COALESCE(vs.instance, ''), COUNT(DISTINCT l.id)
		FROM version v JOIN arrview_removed_version r ON r.version_id = v.id
		LEFT JOIN version_source vs ON vs.version_id = v.id
		JOIN link_version lv ON lv.version_id = v.id JOIN link l ON l.id = lv.link_id
		WHERE v.title_id = ? AND l.state = 'active' AND l.kind = 'symlink'
		GROUP BY v.id ORDER BY v.policy_key`, []any{titleID}, func(r *sql.Rows) error {
		var c RemovalCopy
		var inst string
		if err := r.Scan(&c.Key, &inst, &c.Links); err != nil {
			return err
		}
		hidden = append(hidden, c.Key)
		if len(keys) > 0 && !slices.Contains(keys, c.Key) {
			return nil
		}
		c.ManagedBy = w.managedBy(inst)
		c.Label = w.versionLabel(c.Key)
		out.Leftover = append(out.Leftover, c)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if !slices.Contains(all, k) && !slices.Contains(hidden, k) {
			return nil, fmt.Errorf("%w: the title has no copy %q", errBadRemoval, k)
		}
	}
	out.Title = len(all) > 0 && len(out.Copies) == len(all)
	return out, nil
}

var errBadRemoval = errors.New("bad removal")

func splitKeys(s string) []string {
	var out []string
	for _, k := range strings.Split(s, ",") {
		if k = strings.TrimSpace(k); k != "" && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// GET /titles/{id}/removal?versions=a,b: what Remove would do, for the confirmation (every copy when versions is
// empty).
func (w *Web) apiRemovalScope(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	s, err := w.removalScope(r.Context(), id, splitKeys(r.URL.Query().Get("versions")))
	switch {
	case errors.Is(err, errBadRemoval):
		badRequest(rw, strings.TrimPrefix(err.Error(), errBadRemoval.Error()+": "))
	case err != nil:
		w.internal(rw, err)
	case s == nil:
		notFound(rw)
	default:
		writeJSON(rw, http.StatusOK, s)
	}
}

// RemoveRequest is the body of POST /titles/{id}/remove.
type RemoveRequest struct {
	// Versions are the copies to remove; empty removes the title (every copy).
	Versions []string `json:"versions"`
	// Unlink also moves the copies' library symlinks to the trash.
	Unlink bool `json:"unlink"`
}

// KeptLink is a link a removal left in place, and why (or, in RemoveResult.Failed, the error that kept it).
type KeptLink struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// RemoveResult is the answer of a removal.
type RemoveResult struct {
	Removed        []string   `json:"removed"`
	Title          bool       `json:"title"`
	DownloadsEnded int        `json:"downloads_ended"`
	Unlinked       []string   `json:"unlinked"`
	Kept           []KeptLink `json:"kept"`
	// Failed are links whose move to the trash failed (a full disk, an I/O error): they stay active, and removing
	// the copies again with unlink retries them (Leftover).
	Failed  []KeptLink `json:"failed"`
	Message string     `json:"message"`
}

// POST /titles/{id}/remove {versions, unlink}: take copies (or, without versions, the whole title) out of the
// library, as the arr views' DELETE movie|series does (catalog.Remove): each copy is unmonitored and hidden from
// every view, its rows and history stay, and adding it again restores it. Its downloads in hand end in Theatarr's records
// (never at the provider). With unlink, its library symlinks move to the trash through the library's gate (a link
// the gate refuses stays, with the reason); without, the files stay in the library. A copy an arr still owns is
// refused (409). A link whose move fails otherwise stays too (Failed): the copies are hidden by then, and a removal
// with unlink of the same scope moves what is left (RemovalScope.Leftover).
func (w *Web) apiRemove(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var req RemoveRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	if req.Unlink && w.noOps(rw) {
		return
	}
	scope, err := w.removalScope(ctx, id, req.Versions)
	switch {
	case errors.Is(err, errBadRemoval):
		badRequest(rw, strings.TrimPrefix(err.Error(), errBadRemoval.Error()+": "))
		return
	case err != nil:
		w.internal(rw, err)
		return
	case scope == nil:
		notFound(rw)
		return
	case len(scope.Copies) == 0 && (!req.Unlink || len(scope.Leftover) == 0):
		msg := "nothing to remove: the title is in no library"
		if len(scope.Leftover) > 0 {
			msg += "; removed libraries still have library links: remove with unlink to move them to the trash"
		}
		writeJSON(rw, http.StatusConflict, apiError{Error: msg})
		return
	}
	var keys, unlinkKeys []string
	for _, c := range scope.Copies {
		if c.ManagedBy != "" {
			writeJSON(rw, http.StatusConflict, apiError{Error: c.Label + ": " + managedReason(c.ManagedBy)})
			return
		}
		keys = append(keys, c.Key)
	}
	unlinkKeys = append(unlinkKeys, keys...)
	if req.Unlink {
		for _, c := range scope.Leftover {
			if c.ManagedBy != "" {
				writeJSON(rw, http.StatusConflict, apiError{Error: c.Label + ": " + managedReason(c.ManagedBy)})
				return
			}
			unlinkKeys = append(unlinkKeys, c.Key)
		}
	}
	by := actor(r)
	act := journal.ActRemoveVersion
	if scope.Title {
		act = journal.ActRemoveTitle
	}
	out := RemoveResult{Title: scope.Title, Removed: []string{}, Unlinked: []string{}, Kept: []KeptLink{},
		Failed: []KeptLink{}}
	now := w.now()
	err = w.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if len(keys) == 0 {
			return nil // only the links of copies removed before (Leftover)
		}
		var err error
		if out.Removed, err = catalog.Remove(ctx, tx, id, keys, "ui", now); err != nil {
			return err
		}
		// Each link moved to the trash gets its own unlink_file entry once it has moved.
		detail := "files left linked"
		if req.Unlink {
			detail = "its library links go to the trash next (one unlink_file entry each)"
		}
		for _, k := range out.Removed {
			if _, err := journal.RecordAction(ctx, tx, journal.Action{Action: act, TitleID: id, Version: k,
				Target: scope.Name, Detail: detail, By: by, At: now}); err != nil {
				return err
			}
		}
		// Downloads in hand of the removed copies end in our records, in the same transaction: an
		// import running now cannot commit a link after it (it takes its links back), so the enumeration below
		// sees every link the copies have. The provider keeps its torrents.
		out.DownloadsEnded, err = router.EndRemoved(ctx, tx, id, keys, "copy removed by "+orSomeone(by),
			store.Millis(now))
		return err
	})
	if err != nil {
		w.internal(rw, err)
		return
	}
	if out.Removed == nil {
		out.Removed = []string{}
	}
	if req.Unlink {
		var links []struct {
			id   int64
			path string
		}
		err = scanAll(ctx, w.st.Reader(), `SELECT DISTINCT l.id, l.path FROM link l JOIN link_version lv ON lv.link_id = l.id
			JOIN version v ON v.id = lv.version_id WHERE v.title_id = ? AND v.policy_key IN (`+store.Placeholders(len(unlinkKeys))+`)
			AND l.state = 'active' AND l.kind = 'symlink' ORDER BY l.path`, append([]any{id}, anySlice(unlinkKeys)...),
			func(r *sql.Rows) error {
				var x struct {
					id   int64
					path string
				}
				if err := r.Scan(&x.id, &x.path); err != nil {
					return err
				}
				links = append(links, x)
				return nil
			})
		if err != nil {
			w.internal(rw, err)
			return
		}
		for _, l := range links {
			u, err := w.ops.UnlinkLink(ctx, l.id, "copy removed by "+orSomeone(by))
			var ref refusedErr
			switch {
			case errors.As(err, &ref):
				out.Kept = append(out.Kept, KeptLink{Path: l.path, Reason: ref.Error()})
				continue
			case err != nil:
				// The copies are hidden already: report the link and go on; removing them again with unlink
				// retries it.
				w.log.Warn("removal: moving a library link to the trash failed", "title", id, "link", l.id,
					"path", l.path, "err", err)
				out.Failed = append(out.Failed, KeptLink{Path: l.path, Reason: err.Error()})
				continue
			}
			out.Unlinked = append(out.Unlinked, u.Path)
			w.record(ctx, journal.Action{Action: journal.ActUnlinkFile, TitleID: id, Version: strings.Join(u.Versions, ","),
				Target: u.Path, Detail: "moved to " + orDash(u.Trash) + " when its library was removed", By: by})
		}
	}
	what := "Removed " + strings.Join(out.Removed, ", ")
	switch {
	case out.Title:
		what = "Removed " + scope.Name + " from the library"
	case len(out.Removed) == 0:
		what = "The libraries were removed before"
	}
	switch {
	case !req.Unlink:
		what += "; its files stay in the library"
	case len(out.Kept) > 0 || len(out.Failed) > 0:
		what += fmt.Sprintf("; %d file(s) unlinked, %d left in place", len(out.Unlinked), len(out.Kept)+len(out.Failed))
	default:
		what += fmt.Sprintf("; %d file(s) unlinked", len(out.Unlinked))
	}
	if len(out.Failed) > 0 {
		what += fmt.Sprintf("; moving %d to the trash failed: remove again with unlink to retry", len(out.Failed))
	}
	out.Message = what
	w.log.Info("copies removed by hand", "title", id, "versions", strings.Join(out.Removed, ","), "title_removed",
		out.Title, "downloads_ended", out.DownloadsEnded, "unlinked", len(out.Unlinked), "kept", len(out.Kept),
		"failed", len(out.Failed), "by", by)
	writeJSON(rw, http.StatusOK, out)
}

func anySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// UnlinkResult is the answer of an unlink.
type UnlinkResult struct {
	Path     string   `json:"path"`
	Versions []string `json:"versions"`
	Trash    string   `json:"trash,omitempty"`
	Message  string   `json:"message"`
}

// POST /links/{id}/unlink: move one of Theatarr's library symlinks to the trash (arrview.UnlinkLink); the copy, or the
// episodes the link held, are wanted again when no other file remains. Only an active symlink (409 otherwise, and
// when the library's gate refuses).
func (w *Web) apiUnlink(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	if w.noOps(rw) {
		return
	}
	by := actor(r)
	u, err := w.ops.UnlinkLink(ctx, id, "unlinked by "+orSomeone(by))
	if err != nil {
		w.opsError(rw, err)
		return
	}
	out := UnlinkResult{Path: u.Path, Versions: u.Versions, Trash: u.Trash,
		Message: "Unlinked: the link is in the trash and the file it pointed at is untouched"}
	if u.Trash == "" {
		out.Message = "The link was already gone; Theatarr's records now say so"
	}
	w.record(ctx, journal.Action{Action: journal.ActUnlinkFile, TitleID: u.TitleID, Version: strings.Join(u.Versions, ","),
		Target: u.Path, Detail: "moved to " + orDash(u.Trash), By: by})
	writeJSON(rw, http.StatusOK, out)
}
