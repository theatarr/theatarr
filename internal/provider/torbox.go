// Response types, status mapping and paging ported from decypharr pkg/debrid/providers/torbox/{torbox,types}.go @ 917cfa2
// (github.com/sirrobot01/decypharr), MIT License, Copyright (c) 2025 Mukhtar Akere. The full notice is in
// LICENSE.decypharr in this directory.

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// TorBox, API v1 (https://api-docs.torbox.app/). Endpoints and fields follow the docs and decypharr's client
// (ref/sirrobot01_decypharr @ 917cfa2, pkg/debrid/providers/torbox/{torbox,types}.go).
//
//	GET /api/user/me                                   plan (0 free, 1 essential, 2 pro, 3 standard),
//	                                                   premium_expires_at, cooldown_until
//	GET /api/torrents/mylist?bypass_cache=true&offset=&limit=
//	                                                   list with files; paged by offset, empty page at the end
//	GET /api/torrents/mylist?id=&bypass_cache=true     one torrent (data is an object, sometimes a 1-item array)
//	GET /api/torrents/checkcached?hash=a,b&format=object&list_files=false
//	                                                   cache check: data maps each cached hash to {name, size, hash}
//
// Answers are {"success": bool, "error": ..., "detail": ..., "data": ...}. TorBox lists carry no download links
// (those come from /api/torrents/requestdl, which this package never calls), so Links is -1.
type torBox struct{ g *getter }

// tbPageSize is the list page size (TorBox's default limit is 1000). A var for the paging tests.
var tbPageSize = 1000

const (
	// tbCacheBatch is how many hashes one checkcached call carries (decypharr uses 100).
	tbCacheBatch = 100
)

func (t *torBox) Key() string  { return t.g.provider }
func (t *torBox) Kind() string { return KindTorBox }

func (t *torBox) Cache() CacheSupport {
	return CacheSupport{Supported: true, Endpoint: "GET /api/torrents/checkcached",
		Note: "hashes in batches of 100"}
}

type tbEnvelope struct {
	Success bool            `json:"success"`
	Error   json.RawMessage `json:"error"`
	Detail  string          `json:"detail"`
	Data    json.RawMessage `json:"data"`
}

func (e *tbEnvelope) apiError() *APIError {
	code := strings.Trim(string(e.Error), `"`)
	if code == "null" {
		code = ""
	}
	// Code and Message stay as sent: scrub masks the key, then cleans them.
	ae := &APIError{Code: code, Message: e.Detail}
	switch cleanCode(code) {
	case "AUTH_ERROR", "BAD_TOKEN", "NO_AUTH", "PLAN_RESTRICTED_FEATURE":
		ae.kind = ErrAuth
	case "ITEM_NOT_FOUND":
		ae.kind = ErrNotFound
	case "DATABASE_ERROR", "SERVER_ERROR", "UNKNOWN_ERROR":
		// TorBox failing on its side (the adder's CodeUnavailable codes), not an answer about a torrent.
		ae.kind = ErrUnavailable
	}
	return ae
}

func tbError(_ int, body []byte) *APIError {
	var e tbEnvelope
	if json.Unmarshal(body, &e) != nil {
		return nil
	}
	return e.apiError()
}

func (t *torBox) call(ctx context.Context, path string, q url.Values, out any) error {
	var env tbEnvelope
	if _, err := t.g.getJSON(ctx, path, q, tbError, &env); err != nil {
		return err
	}
	if !env.Success {
		e := env.apiError()
		e.Provider, e.Status = t.g.provider, 200
		scrub(e, t.g.redact)
		return e
	}
	d := bytes.TrimSpace(env.Data)
	if len(d) == 0 || bytes.Equal(d, []byte("null")) {
		return nil
	}
	if err := json.Unmarshal(d, out); err != nil {
		return fmt.Errorf("%s: decoding %s: %w", t.g.provider, path, err)
	}
	return nil
}

func (t *torBox) Account(ctx context.Context) (*Account, error) {
	var u struct {
		Plan             int      `json:"plan"`
		IsSubscribed     bool     `json:"is_subscribed"`
		PremiumExpiresAt flexTime `json:"premium_expires_at"`
		CooldownUntil    flexTime `json:"cooldown_until"`
	}
	if err := t.call(ctx, "/api/user/me", url.Values{"settings": {"false"}}, &u); err != nil {
		return nil, err
	}
	// Plans and their active-download slots (TorBox pricing; checked against zurg).
	plans := map[int]struct {
		name  string
		slots int
	}{0: {"free", 1}, 1: {"essential", 3}, 2: {"pro", 10}, 3: {"standard", 5}}
	p, ok := plans[u.Plan]
	if !ok {
		p.name, p.slots = "plan "+strconv.Itoa(u.Plan), -1
	}
	return &Account{Provider: t.g.provider, Plan: p.name, Premium: u.Plan > 0, Expires: u.PremiumExpiresAt.Time,
		CooldownUntil: u.CooldownUntil.Time, ActiveTorrents: -1, MaxActiveTorrents: p.slots}, nil
}

type tbTorrent struct {
	ID               int64    `json:"id"`
	Hash             string   `json:"hash"`
	Name             string   `json:"name"`
	Size             int64    `json:"size"`
	CreatedAt        flexTime `json:"created_at"`
	DownloadState    string   `json:"download_state"`
	Progress         float64  `json:"progress"` // 0-1
	DownloadFinished bool     `json:"download_finished"`
	DownloadPresent  bool     `json:"download_present"`
	Files            []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"` // path including the torrent folder
		Size int64  `json:"size"`
	} `json:"files"`
}

var tbParen = regexp.MustCompile(`\s*\(.*?\)\s*`)

// tbState maps download_state. Finished and present means downloaded. Finished without the files present is a
// brief finalising step, unless the state is empty, which means the files are gone. Parenthesised details
// ("stalled (no seeds)") are dropped first. Lists from decypharr's getTorboxStatus, checked against zurg.
//
// metaDL and checking are TorBox resolving the magnet's metadata, not downloading: "checking is where TorBox waits
// for metadata and not where it verifies a hash", and TorBox never gives up on a dead hash there (still checking at
// 910 s; https://notes.debridmediamanager.com/zurg/internals/torrent-lifecycle/). They are queued, so the router's
// stalled_after applies to them; so is checkingDL while the size is still unknown (size <= 0, the notes' size -1).
// processing and uploading stay downloading.
func tbState(s string, finished, present bool, size int64) State {
	if finished && present {
		return StateDownloaded
	}
	base := strings.ToLower(tbParen.ReplaceAllString(s, ""))
	if finished && base == "" {
		return StateError
	}
	switch base {
	case "queued", "queueddl", "paused", "pauseddl", "stalled", "stalleddl", "allocating", "metadl", "checking":
		return StateQueued
	case "checkingdl":
		if size <= 0 {
			return StateQueued
		}
		return StateDownloading
	case "downloading", "checkingresumedata", "processing", "forceddl", "moving",
		"incomplete", "uploading":
		return StateDownloading
	case "completed", "cached", "seeding", "downloaded", "pausedup", "queuedup", "checkingup", "forcedup",
		"stalledup":
		return StateDownloaded
	case "error", "failed", "missingfiles", "expired", "reported missing":
		return StateError
	}
	return StateUnknown
}

func (x *tbTorrent) torrent() Torrent {
	h, _ := normHash(x.Hash)
	t := Torrent{ID: strconv.FormatInt(x.ID, 10), Hash: h, Name: x.Name, Bytes: x.Size, Status: x.DownloadState,
		State: tbState(x.DownloadState, x.DownloadFinished, x.DownloadPresent, x.Size), Progress: x.Progress * 100, Added: x.CreatedAt.Time,
		Links: -1}
	if x.Files != nil {
		t.Files = make([]File, 0, len(x.Files))
		for _, f := range x.Files {
			t.Files = append(t.Files, File{ID: strconv.FormatInt(f.ID, 10), Path: trimSlash(f.Name), Bytes: f.Size,
				Selected: true})
		}
	}
	return t
}

// tbOverlap is how many entries of the previous page each page reads again. The list is id-descending and shifts
// while it is paged; a page that shares none of the overlap means entries moved past the boundary, and the listing
// starts over (checked against zurg). TorBox gives no total, so a short page is the end.
const (
	tbOverlap      = 50
	tbListAttempts = 3
)

func (t *torBox) Torrents(ctx context.Context) ([]Torrent, error) {
	for attempt := 1; ; attempt++ {
		out, ok, err := t.listOnce(ctx)
		if err != nil {
			return nil, err
		}
		if ok {
			return out, nil
		}
		if attempt == tbListAttempts {
			return nil, fmt.Errorf("%s: %w: the list kept shifting after %d reads", t.g.provider, ErrIncomplete, attempt)
		}
	}
}

// listOnce reads every page once; ok is false when two pages did not overlap.
func (t *torBox) listOnce(ctx context.Context) ([]Torrent, bool, error) {
	var out []Torrent
	seen := map[int64]bool{}
	overlap := min(tbOverlap, tbPageSize/2)
	for offset := 0; ; {
		q := url.Values{"bypass_cache": {"true"}, "offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(tbPageSize)}}
		var page []tbTorrent
		if err := t.call(ctx, "/api/torrents/mylist", q, &page); err != nil {
			return nil, false, err
		}
		shared := 0
		for i := range page {
			if seen[page[i].ID] {
				shared++
				continue
			}
			seen[page[i].ID] = true
			out = append(out, page[i].torrent())
		}
		if offset > 0 && len(page) > 0 && shared == 0 {
			return nil, false, nil
		}
		if len(page) < tbPageSize {
			return out, true, nil
		}
		offset += len(page) - overlap
	}
}

// tbOne accepts an object or a one-item array.
type tbOne struct{ t *tbTorrent }

func (o *tbOne) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '[' {
		var arr []tbTorrent
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		if len(arr) > 0 {
			o.t = &arr[0]
		}
		return nil
	}
	var x tbTorrent
	if err := json.Unmarshal(b, &x); err != nil {
		return err
	}
	o.t = &x
	return nil
}

// Torrent reads one torrent. TorBox answers the single read of an unknown or deleted id with HTTP 500
// (https://notes.debridmediamanager.com/zurg/internals/torrent-lifecycle/; the torrent manager's delete relies on the
// same), so a 5xx is checked against the whole list: absent from a list that was read completely is not found, listed
// is the listed torrent. A failed list keeps the 5xx: the provider may be down.
func (t *torBox) Torrent(ctx context.Context, id string) (*Torrent, error) {
	var one tbOne
	if err := t.call(ctx, "/api/torrents/mylist", url.Values{"id": {id}, "bypass_cache": {"true"}}, &one); err != nil {
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status < 500 {
			return nil, err
		}
		list, lerr := t.Torrents(ctx)
		if lerr != nil {
			return nil, err
		}
		for i := range list {
			if list[i].ID == id {
				list[i].OriginalName = OriginalName(list[i].Files, list[i].Name)
				return &list[i], nil
			}
		}
		return nil, &APIError{Provider: t.g.provider, Status: ae.Status, Code: ae.Code,
			Message: "not in the account's list", kind: ErrNotFound}
	}
	if one.t == nil {
		return nil, &APIError{Provider: t.g.provider, Status: 200, Code: "NOT_FOUND", kind: ErrNotFound}
	}
	x := one.t.torrent()
	x.OriginalName = OriginalName(x.Files, x.Name)
	return &x, nil
}

func (t *torBox) CheckCached(ctx context.Context, q CacheQuery) (map[string]bool, error) {
	hashes, err := cleanHashes(q.Hashes)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(hashes))
	for i := 0; i < len(hashes); i += tbCacheBatch {
		batch := hashes[i:min(i+tbCacheBatch, len(hashes))]
		var data map[string]struct {
			Size int64 `json:"size"`
		}
		qv := url.Values{"hash": {strings.Join(batch, ",")}, "format": {"object"}, "list_files": {"false"}}
		if err := t.call(ctx, "/api/torrents/checkcached", qv, &data); err != nil {
			return nil, err
		}
		cached := make(map[string]bool, len(data))
		for h, v := range data {
			cached[strings.ToLower(h)] = v.Size > 0
		}
		for _, h := range batch {
			out[h] = cached[h]
		}
	}
	return out, nil
}
