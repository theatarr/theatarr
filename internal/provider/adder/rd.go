// Request shapes and status handling ported from decypharr pkg/debrid/providers/realdebrid/realdebrid.go @ 917cfa2
// (addMagnet, CheckStatus's selectFiles; github.com/sirrobot01/decypharr), MIT License, Copyright (c) 2025 Mukhtar
// Akere. The full notice is in internal/provider/LICENSE.decypharr.

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

// Real-Debrid, REST API 1.0 (https://api.real-debrid.com/):
//
//	POST /torrents/addMagnet          form magnet=<uri>; 201 {"id", "uri"}
//	POST /torrents/selectFiles/{id}   form files=<id,id,...>; 204, or 202 when already done
//
// Errors are {"error": "...", "error_code": N} with an HTTP status. decypharr reads 509 as too many active downloads
// and 451 as blocked. RD also answers 451 infringing_file during short blocks of the whole account's adds (after
// bursts; measured by zurg's maintainers), so a 451 on a name RD's rule does not refuse is CodeAccountHold, not
// CodeBlocked (indexer.RealDebridRefuses decides); the router tells a hold from a refused hash (router note451).
type realDebrid struct {
	p   *poster
	own *ledger // adds made here, for DeleteOwnUncachedAdd (delete.go)
}

func (r *realDebrid) Key() string        { return r.p.provider }
func (r *realDebrid) Kind() string       { return provider.KindRealDebrid }
func (r *realDebrid) SelectsFiles() bool { return true }

func (r *realDebrid) AddMagnet(ctx context.Context, m Magnet, _ AddOptions) (*Added, error) {
	if !m.valid() {
		return nil, r.p.newError(CodeBlocked, 0, "", "not a public magnet")
	}
	status, b, ra, err := r.p.post(ctx, "/torrents/addMagnet", url.Values{"magnet": {m.URI()}})
	if err != nil {
		return nil, err
	}
	if status == http.StatusOK || status == http.StatusCreated {
		var out struct {
			ID string `json:"id"`
		}
		if err := r.p.decodeJSON(status, b, &out); err != nil {
			return nil, err
		}
		if out.ID == "" {
			return nil, r.p.newError(CodeUnknown, status, "", "no torrent id in the answer")
		}
		a := &Added{ID: out.ID, Hash: m.hash}
		r.own.issue(a)
		return a, nil
	}
	return nil, r.err(status, b, ra, m.refused)
}

func (r *realDebrid) SelectFiles(ctx context.Context, id string, fileIDs []string) error {
	if id == "" || len(fileIDs) == 0 {
		return r.p.newError(CodeInvalid, 0, "", "no torrent id or no files to select")
	}
	for _, f := range fileIDs {
		if _, err := strconv.Atoi(f); err != nil {
			return r.p.newError(CodeInvalid, 0, "", "file ids are numbers")
		}
	}
	status, b, ra, err := r.p.post(ctx, "/torrents/selectFiles/"+url.PathEscape(id),
		url.Values{"files": {strings.Join(fileIDs, ",")}})
	if err != nil {
		return err
	}
	if status == http.StatusNoContent || status == http.StatusAccepted || status == http.StatusOK {
		return nil
	}
	return r.err(status, b, ra, false)
}

// err maps an RD error answer to an *Error.
func (r *realDebrid) err(status int, b []byte, ra time.Duration, refusedName bool) *Error {
	var body struct {
		Error     string `json:"error"`
		ErrorCode int    `json:"error_code"`
	}
	_ = json.Unmarshal(b, &body)
	pcode := body.Error
	if body.ErrorCode != 0 {
		pcode = strconv.Itoa(body.ErrorCode) + " " + body.Error
	}
	code := CodeUnknown
	switch {
	case body.ErrorCode == 35 || status == http.StatusUnavailableForLegalReasons && body.Error == "infringing_file":
		code = CodeAccountHold
		if refusedName {
			code = CodeBlocked
		}
	case status == 509 || body.ErrorCode == 21:
		code = CodeTooManyActive
	case status == http.StatusTooManyRequests || body.ErrorCode == 34 || body.ErrorCode == 5:
		code = CodeRateLimited
	case body.ErrorCode == 23 || body.ErrorCode == 36:
		code = CodeTrafficExceeded
	case body.ErrorCode == 8 || body.ErrorCode == 9 || body.ErrorCode == 14 || body.ErrorCode == 15 ||
		body.ErrorCode == 22 || status == http.StatusUnauthorized || status == http.StatusForbidden:
		code = CodeAuth
	case body.ErrorCode == 26 || body.ErrorCode == 28 || body.ErrorCode == 29:
		code = CodeBlocked
	case body.ErrorCode == 2 || body.ErrorCode == 30:
		code = CodeInvalid
	case status >= 500 || body.ErrorCode == 6 || body.ErrorCode == 25:
		code = CodeUnavailable
	}
	// Some codes are worded strings only, without the number in a body we could read.
	pcode = strings.Join(strings.Fields(pcode), "_")
	e := r.p.newError(code, status, pcode, "")
	if ra > 0 {
		e.RetryAt = time.Now().Add(ra)
	}
	return e
}
