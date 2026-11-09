// Response types, status mapping and paging ported from decypharr pkg/debrid/providers/alldebrid/{alldebrid,types}.go @ 917cfa2
// (github.com/sirrobot01/decypharr), MIT License, Copyright (c) 2025 Mukhtar Akere. The full notice is in
// LICENSE.decypharr in this directory.

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// AllDebrid, API v4 and v4.1 (https://docs.alldebrid.com/). Endpoints and fields follow the docs and decypharr's
// client (ref/sirrobot01_decypharr @ 917cfa2, pkg/debrid/providers/alldebrid/{alldebrid,types}.go), which uses v4.1.
//
//	GET /v4.1/user                       account: isPremium, isTrial, premiumUntil (unix), fidelityPoints,
//	                                     limitedHostersQuotas
//	GET /v4.1/magnet/status              every magnet on the account in one answer (no paging); v4.1 dropped the
//	                                     file tree from this list
//	GET /v4.1/magnet/status?id=          one magnet (data.magnets is then an object, not an array)
//	GET /v4.1/magnet/files?id[]=         the file tree: {n, s, l} leaves and {n, e} folders; "l" is a link and is
//	                                     only counted; read for a ready magnet only
//
// Every answer is {"status": "success"|"error", "data"|"error": ...}; errors can come with HTTP 200.
// The policy URL may end in /v4 or /v4.1; the client always uses v4.1. AllDebrid accepts GET with query parameters
// (decypharr reads this way); zurg sends the same calls as form POSTs. Ids are numbers in magnet/status and strings in
// magnet/files. Rate limits: 12 requests per second and 600 per minute per key (docs); 429 or 503 with Retry-After.
// AllDebrid removed /magnet/instant in 2024; decypharr returns ErrAvailabilityUnsupported for it.
type allDebrid struct {
	g   *getter
	v41 string // path prefix
}

func newAllDebrid(g *getter) *allDebrid {
	root := strings.TrimRight(g.base.Path, "/")
	for _, s := range []string{"/v4.1", "/v4"} {
		if strings.HasSuffix(root, s) {
			root = strings.TrimSuffix(root, s)
			break
		}
	}
	b := *g.base
	b.Path = root
	g.base = &b
	return &allDebrid{g: g, v41: "/v4.1"}
}

func (a *allDebrid) Key() string  { return a.g.provider }
func (a *allDebrid) Kind() string { return KindAllDebrid }

func (a *allDebrid) Cache() CacheSupport {
	return CacheSupport{Note: "magnet/instant was removed in 2024; only an upload (a write) shows whether a " +
		"magnet is ready at once; otherwise the DMM and Torrentio feeds and refused adds"}
}

func (a *allDebrid) CheckCached(context.Context, CacheQuery) (map[string]bool, error) {
	return nil, ErrCacheUnsupported
}

type adEnvelope struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (e *adEnvelope) apiError() *APIError {
	// Code and Message stay as sent: scrub masks the key, then cleans them.
	ae := &APIError{Code: "unknown", Message: "status " + e.Status}
	if e.Error != nil {
		ae.Code, ae.Message = e.Error.Code, e.Error.Message
	}
	switch code := cleanCode(ae.Code); {
	case code == "AUTH_BLOCKED":
		// The message names the account's e-mail address; the typed error says what to do instead.
		ae.kind, ae.Message = ErrLocationBlocked, ""
	case strings.HasPrefix(code, "AUTH_"), code == "MUST_BE_PREMIUM", code == "NO_SERVER", code == "FREE_TRIAL_LIMIT_REACHED":
		ae.kind = ErrAuth
	case code == "MAGNET_INVALID_ID":
		ae.kind = ErrNotFound
	case code == "MAINTENANCE", code == "GENERIC":
		// AllDebrid down or failing on its side (the adder's CodeUnavailable codes), not an answer about a magnet.
		ae.kind = ErrUnavailable
	}
	return ae
}

func adError(_ int, body []byte) *APIError {
	var e adEnvelope
	if json.Unmarshal(body, &e) != nil || e.Error == nil {
		return nil
	}
	return e.apiError()
}

// call GETs path and decodes the envelope's data into out, turning a "status": "error" answer into an *APIError.
func (a *allDebrid) call(ctx context.Context, path string, q url.Values, out any) error {
	var env adEnvelope
	if _, err := a.g.getJSON(ctx, path, q, adError, &env); err != nil {
		return err
	}
	if env.Status != "success" {
		e := env.apiError()
		e.Provider, e.Status = a.g.provider, 200
		scrub(e, a.g.redact)
		return e
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("%s: decoding %s: %w", a.g.provider, path, err)
	}
	return nil
}

func (a *allDebrid) Account(ctx context.Context) (*Account, error) {
	var d struct {
		User struct {
			IsPremium            bool           `json:"isPremium"`
			IsTrial              bool           `json:"isTrial"`
			PremiumUntil         int64          `json:"premiumUntil"`
			FidelityPoints       int            `json:"fidelityPoints"`
			LimitedHostersQuotas map[string]int `json:"limitedHostersQuotas"`
		} `json:"user"`
	}
	if err := a.call(ctx, a.v41+"/user", nil, &d); err != nil {
		return nil, err
	}
	u := d.User
	acc := &Account{Provider: a.g.provider, Plan: "free", Premium: u.IsPremium, Points: u.FidelityPoints,
		ActiveTorrents: -1, MaxActiveTorrents: -1}
	switch {
	case u.IsPremium:
		acc.Plan = "premium"
	case u.IsTrial:
		acc.Plan = "trial"
	}
	if u.PremiumUntil > 0 {
		acc.Expires = time.Unix(u.PremiumUntil, 0).UTC()
	}
	hosts := make([]string, 0, len(u.LimitedHostersQuotas))
	for h := range u.LimitedHostersQuotas {
		hosts = append(hosts, h)
	}
	slices.Sort(hosts)
	for _, h := range hosts {
		acc.Notes = append(acc.Notes, fmt.Sprintf("hoster quota %s: %d", h, u.LimitedHostersQuotas[h]))
	}
	return acc, nil
}

type adMagnet struct {
	ID             int64      `json:"id"`
	Filename       string     `json:"filename"`
	Size           int64      `json:"size"`
	Hash           string     `json:"hash"`
	Status         string     `json:"status"`
	StatusCode     int        `json:"statusCode"`
	Downloaded     int64      `json:"downloaded"`
	UploadDate     int64      `json:"uploadDate"`
	CompletionDate int64      `json:"completionDate"`
	NbLinks        *int       `json:"nbLinks"`
	Files          []adFileIn `json:"files"` // v4 lists had it; v4.1 needs magnet/files
}

// adFileIn is a file tree node. "l" (the link) is decoded only as present or not.
type adFileIn struct {
	Name  string      `json:"n"`
	Size  int64       `json:"s"`
	Link  linkPresent `json:"l"`
	Elems []adFileIn  `json:"e"`
}

// linkPresent records whether a link was there, never the link.
type linkPresent bool

func (l *linkPresent) UnmarshalJSON(b []byte) error {
	*l = linkPresent(len(b) > 2 && b[0] == '"')
	return nil
}

// adMagnets accepts an array, an id-keyed object, or a single magnet object (the answer to ?id=).
type adMagnets []adMagnet

func (m *adMagnets) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*m = nil
		return nil
	}
	if b[0] == '[' {
		var arr []adMagnet
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*m = arr
		return nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	if _, single := probe["id"]; single {
		var one adMagnet
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		*m = adMagnets{one}
		return nil
	}
	keys := make([]string, 0, len(probe))
	for k := range probe {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make(adMagnets, 0, len(keys))
	for _, k := range keys {
		var one adMagnet
		if err := json.Unmarshal(probe[k], &one); err != nil {
			return err
		}
		out = append(out, one)
	}
	*m = out
	return nil
}

// adState maps AllDebrid's statusCode (docs, magnet/status): 0 in queue, 1 downloading, 2 compressing or moving,
// 3 uploading, 4 ready; 5 upload failed, 6 unpacking error, 7 no peer within 20 min, 8 too big, 9 internal error,
// 10 over 72 h, 11 deleted on the hoster, 12-13 processing failed, 14 tracker error, 15 no peer.
func adState(code int) State {
	switch {
	case code == 0:
		return StateQueued
	case code >= 1 && code <= 3:
		return StateDownloading
	case code == 4:
		return StateDownloaded
	case code >= 5 && code <= 15:
		return StateError
	}
	return StateUnknown
}

func (m *adMagnet) torrent() Torrent {
	h, _ := normHash(m.Hash)
	t := Torrent{ID: strconv.FormatInt(m.ID, 10), Hash: h, Name: m.Filename, Bytes: m.Size, Status: m.Status,
		State: adState(m.StatusCode), Links: -1}
	if m.NbLinks != nil {
		t.Links = *m.NbLinks
	}
	switch {
	case t.State == StateDownloaded:
		t.Progress = 100
	case m.Size > 0:
		t.Progress = float64(m.Downloaded) / float64(m.Size) * 100
	}
	if m.UploadDate > 0 {
		t.Added = time.Unix(m.UploadDate, 0).UTC()
	}
	if m.Files != nil {
		t.Files, t.Links = flattenAD(m.Files)
	}
	return t
}

// flattenAD turns the file tree into paths and counts the leaves that carry a link.
func flattenAD(nodes []adFileIn) ([]File, int) {
	var out []File
	links := 0
	var walk func(prefix string, ns []adFileIn)
	walk = func(prefix string, ns []adFileIn) {
		for _, n := range ns {
			p := n.Name
			if prefix != "" {
				p = prefix + "/" + n.Name
			}
			if n.Elems != nil {
				walk(p, n.Elems)
				continue
			}
			out = append(out, File{Path: p, Bytes: n.Size, Selected: true})
			if n.Link {
				links++
			}
		}
	}
	walk("", nodes)
	if out == nil {
		out = []File{}
	}
	return out, links
}

func (a *allDebrid) Torrents(ctx context.Context) ([]Torrent, error) {
	var d struct {
		Magnets adMagnets `json:"magnets"`
	}
	if err := a.call(ctx, a.v41+"/magnet/status", nil, &d); err != nil {
		return nil, err
	}
	out := make([]Torrent, 0, len(d.Magnets))
	for i := range d.Magnets {
		out = append(out, d.Magnets[i].torrent())
	}
	return out, nil
}

func (a *allDebrid) Torrent(ctx context.Context, id string) (*Torrent, error) {
	var d struct {
		Magnets adMagnets `json:"magnets"`
	}
	if err := a.call(ctx, a.v41+"/magnet/status", url.Values{"id": {id}}, &d); err != nil {
		return nil, err
	}
	if len(d.Magnets) == 0 {
		return nil, &APIError{Provider: a.g.provider, Status: 200, Code: "MAGNET_INVALID_ID", kind: ErrNotFound}
	}
	t := d.Magnets[0].torrent()
	// The file tree is read only once the magnet is ready (statusCode 4), as decypharr does: before that AllDebrid has
	// no links to list, and a failed files call must not turn a queued, downloading or failed magnet's status into a
	// failed read. For a ready magnet its error stands: the files decide "no video" and the dangerous
	// file check, so a ready magnet is not reported without them.
	if t.Files == nil && t.State == StateDownloaded {
		var f struct {
			Magnets []struct {
				ID    json.RawMessage `json:"id"`
				Files []adFileIn      `json:"files"`
			} `json:"magnets"`
		}
		if err := a.call(ctx, a.v41+"/magnet/files", url.Values{"id[]": {id}}, &f); err != nil {
			return nil, err
		}
		for _, m := range f.Magnets {
			if strings.Trim(string(m.ID), `"`) == id {
				t.Files, t.Links = flattenAD(m.Files)
			}
		}
	}
	t.OriginalName = originalName(t.Files, t.Name)
	return &t, nil
}
