// Request shape ported from decypharr pkg/debrid/providers/alldebrid/{alldebrid,types}.go @ 917cfa2 (addMagnetLink,
// UploadMagnetResponse; github.com/sirrobot01/decypharr), MIT License, Copyright (c) 2025 Mukhtar Akere. The full
// notice is in internal/provider/LICENSE.decypharr.

package adder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/provider"
)

// AllDebrid, API v4.1 (https://docs.alldebrid.com/):
//
//	POST /v4.1/magnet/upload   form magnets[]=<uri>; {"status": "success", "data": {"magnets": [{"magnet", "hash",
//	                           "name", "size", "ready", "id"}]}}, or a per-magnet {"magnet", "error": {"code",
//	                           "message"}}
//
// Every answer is {"status": "success"|"error", ...}; errors can come with HTTP 200. AllDebrid takes every file of
// a magnet, so there is no selection. decypharr sends the upload as a GET with a query; the docs list POST, which
// keeps the magnet out of request logs, so this sends a form POST.
type allDebrid struct {
	p   *poster
	own *ledger // adds made here, for DeleteOwnUncachedAdd (delete.go)
}

func newAllDebrid(p *poster) *allDebrid {
	root := strings.TrimRight(p.base.Path, "/")
	for _, s := range []string{"/v4.1", "/v4"} {
		if strings.HasSuffix(root, s) {
			root = strings.TrimSuffix(root, s)
			break
		}
	}
	b := *p.base
	b.Path = root
	p.base = &b
	return &allDebrid{p: p, own: newLedger()}
}

func (a *allDebrid) Key() string        { return a.p.provider }
func (a *allDebrid) Kind() string       { return provider.KindAllDebrid }
func (a *allDebrid) SelectsFiles() bool { return false }

func (a *allDebrid) SelectFiles(context.Context, string, []string) error { return ErrNoSelection }

type adError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (a *allDebrid) AddMagnet(ctx context.Context, m Magnet, _ AddOptions) (*Added, error) {
	if !m.valid() {
		return nil, a.p.newError(CodeBlocked, 0, "", "not a public magnet")
	}
	status, b, ra, err := a.p.post(ctx, "/v4.1/magnet/upload", url.Values{"magnets[]": {m.URI()}})
	if err != nil {
		return nil, err
	}
	var env struct {
		Status string   `json:"status"`
		Error  *adError `json:"error"`
		Data   struct {
			Magnets []struct {
				Hash  string          `json:"hash"`
				Name  string          `json:"name"`
				Ready bool            `json:"ready"`
				ID    json.RawMessage `json:"id"`
				Error *adError        `json:"error"`
			} `json:"magnets"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &env) != nil {
		if status == http.StatusTooManyRequests {
			return nil, a.err(status, &adError{Code: "429"}, ra)
		}
		return nil, a.err(status, nil, ra)
	}
	if env.Status != "success" || env.Error != nil {
		e := a.err(status, env.Error, ra)
		e.Refused = env.Error != nil && env.Error.Code != ""
		return nil, e
	}
	if len(env.Data.Magnets) == 0 {
		return nil, a.p.newError(CodeUnknown, status, "", "no magnet in the answer")
	}
	mg := env.Data.Magnets[0]
	if mg.Error != nil {
		e := a.err(status, mg.Error, ra)
		e.Refused = mg.Error.Code != ""
		return nil, e
	}
	if !strings.EqualFold(mg.Hash, m.hash) {
		return nil, a.p.newError(CodeUnknown, status, "", "magnet hash does not match the requested release")
	}
	id := strings.Trim(string(mg.ID), `"`)
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return nil, a.p.newError(CodeUnknown, status, "", "no magnet id in the answer")
	}
	out := &Added{ID: id, Hash: strings.ToLower(mg.Hash), Name: mg.Name, Ready: mg.Ready}
	a.own.issue(out)
	return out, nil
}

// err maps an AllDebrid error (docs' error list) to an *Error.
func (a *allDebrid) err(status int, e *adError, ra time.Duration) *Error {
	if e == nil {
		e = &adError{}
	}
	code := CodeUnknown
	switch c := e.Code; {
	case c == "MAGNET_TOO_MANY_ACTIVE":
		code = CodeTooManyActive
	case c == "MAGNET_TOO_MANY":
		code = CodeTrafficExceeded
	case c == "MAGNET_INVALID_URI", c == "MAGNET_NO_URI", c == "MAGNET_INVALID_FILE":
		code = CodeInvalid
	case c == "MAGNET_MUST_BE_PREMIUM", c == "MUST_BE_PREMIUM", c == "MAGNET_NO_SERVER", strings.HasPrefix(c, "AUTH_"),
		c == "FREE_TRIAL_LIMIT_REACHED", c == "NO_SERVER", status == http.StatusUnauthorized, status == http.StatusForbidden:
		code = CodeAuth
	case c == "429", status == http.StatusTooManyRequests:
		code = CodeRateLimited
	case c == "MAGNET_FILE_UPLOAD_FAILED", c == "MAINTENANCE", c == "GENERIC", status >= 500:
		code = CodeUnavailable
	}
	msg := e.Message
	if strings.HasPrefix(e.Code, "AUTH_") {
		msg = "" // AUTH_BLOCKED names the account's address
	}
	out := a.p.newError(code, status, e.Code, msg)
	if ra > 0 {
		out.RetryAt = time.Now().Add(ra)
	}
	return out
}
