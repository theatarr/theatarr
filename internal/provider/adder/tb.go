// Request shape ported from decypharr pkg/debrid/providers/torbox/{torbox,types}.go @ 917cfa2 (SubmitMagnet,
// AddMagnetResponse; github.com/sirrobot01/decypharr), MIT License, Copyright (c) 2025 Mukhtar Akere. The full
// notice is in internal/provider/LICENSE.decypharr.

package adder

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/provider"
)

// TorBox, API v1 (https://api-docs.torbox.app/):
//
//	POST /api/torrents/createtorrent   form magnet=<uri>, allow_zip=false, seed=3, add_only_if_cached=true (cached only);
//	                                   {"success", "error", "detail", "data": {"torrent_id", "hash", ...}}; data is
//	                                   sometimes an array (decypharr's AddMagnetResponse)
//
// With add_only_if_cached an uncached hash is refused with 400 DOWNLOAD_NOT_CACHED and nothing is created; the refusal
// still counts against TorBox's 60 uncached adds an hour (behaviour measured by zurg's maintainers). allow_zip=false
// keeps many-file torrents from being zipped, which the mount could not read. TorBox takes every file.
//
// seed=3 is "never seed" (the user does not seed). Sent on every add: with seeding on, a finished torrent holds an
// active slot for up to 30 days, and "any value sent, 1 included, replaces the account's Seeding setting"
// (https://notes.debridmediamanager.com/zurg/providers/torbox-limits/).
type torBox struct{ p *poster }

// tbNeverSeed is createtorrent's seed value for "never seed".
const tbNeverSeed = "3"

func (t *torBox) Key() string        { return t.p.provider }
func (t *torBox) Kind() string       { return provider.KindTorBox }
func (t *torBox) SelectsFiles() bool { return false }

func (t *torBox) SelectFiles(context.Context, string, []string) error { return ErrNoSelection }

type tbAdded struct {
	TorrentID int    `json:"torrent_id"`
	ID        int    `json:"id"`
	Hash      string `json:"hash"`
	Name      string `json:"name"`
}

func (t *torBox) AddMagnet(ctx context.Context, m Magnet, o AddOptions) (*Added, error) {
	if !m.valid() {
		return nil, t.p.newError(CodeBlocked, 0, "", "not a public magnet")
	}
	form := url.Values{"magnet": {m.URI()}, "allow_zip": {"false"}, "seed": {tbNeverSeed}}
	if o.OnlyIfCached {
		form.Set("add_only_if_cached", "true")
	}
	status, b, ra, err := t.p.post(ctx, "/api/torrents/createtorrent", form)
	if err != nil {
		return nil, err
	}
	var env struct {
		Success bool            `json:"success"`
		Error   json.RawMessage `json:"error"`
		Detail  string          `json:"detail"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(b, &env) != nil {
		return nil, t.err(status, "", "", ra)
	}
	ecode := strings.Trim(string(env.Error), `"`)
	if ecode == "null" {
		ecode = ""
	}
	if !env.Success || status < 200 || status >= 300 {
		e := t.err(status, ecode, env.Detail, ra)
		// TorBox's own server-side errors may follow a stored add; any other code it gives refuses the add.
		e.Refused = !env.Success && ecode != "" && e.Code != CodeUnavailable
		return nil, e
	}
	var list []tbAdded
	d := bytes.TrimSpace(env.Data)
	if len(d) > 0 && d[0] == '[' {
		_ = json.Unmarshal(d, &list)
	} else {
		var one tbAdded
		if json.Unmarshal(d, &one) == nil {
			list = []tbAdded{one}
		}
	}
	for _, a := range list {
		if len(list) > 1 && !strings.EqualFold(a.Hash, m.hash) {
			continue
		}
		id := a.TorrentID
		if id == 0 {
			id = a.ID
		}
		if id == 0 {
			break
		}
		return &Added{ID: strconv.Itoa(id), Hash: strings.ToLower(a.Hash), Name: a.Name,
			Ready: tbCached(env.Detail)}, nil
	}
	return nil, t.p.newError(CodeUnknown, status, "", "no torrent id in the answer")
}

// err maps a TorBox error code to an *Error.
func (t *torBox) err(status int, ecode, detail string, ra time.Duration) *Error {
	code := CodeUnknown
	switch {
	case ecode == "DOWNLOAD_NOT_CACHED":
		code = CodeNotCached
	case ecode == "ACTIVE_LIMIT":
		code = CodeTooManyActive
	case ecode == "MONTHLY_LIMIT", ecode == "COOLDOWN_LIMIT":
		code = CodeTrafficExceeded
	case ecode == "DOWNLOAD_TOO_LARGE", ecode == "BOZO_TORRENT":
		code = CodeBlocked
	case ecode == "INVALID_OPTION", ecode == "BAD_REQUEST":
		// These describe the request, not invalid torrent content. Never blocklist the release.
		code = CodeUnknown
	case ecode == "AUTH_ERROR", ecode == "BAD_TOKEN", ecode == "NO_AUTH", ecode == "PLAN_RESTRICTED_FEATURE",
		status == http.StatusUnauthorized, status == http.StatusForbidden:
		code = CodeAuth
	case status == http.StatusTooManyRequests:
		code = CodeRateLimited
	case ecode == "DATABASE_ERROR", ecode == "SERVER_ERROR", ecode == "UNKNOWN_ERROR", status >= 500:
		code = CodeUnavailable
	}
	e := t.p.newError(code, status, ecode, detail)
	if ra > 0 {
		e.RetryAt = time.Now().Add(ra)
	}
	return e
}

// Cache evidence must be affirmative: "not cached" and "uncached" are not hits.
func tbCached(detail string) bool {
	detail = strings.ToLower(strings.TrimSpace(detail))
	return detail == "found cached torrent. using cached torrent."
}
