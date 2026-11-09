// Response types, status mapping and paging ported from decypharr pkg/debrid/providers/realdebrid/{realdebrid,types}.go @ 917cfa2
// (github.com/sirrobot01/decypharr), MIT License, Copyright (c) 2025 Mukhtar Akere. The full notice is in
// LICENSE.decypharr in this directory.

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
	_ "time/tzdata" // Europe/Paris for RD dates, also in images without zoneinfo
)

// Real-Debrid, REST API 1.0 (https://api.real-debrid.com/). Field names and paging follow the official docs and
// decypharr's client (ref/sirrobot01_decypharr @ 917cfa2, pkg/debrid/providers/realdebrid/{realdebrid,types}.go).
//
//	GET /user                     account: type, premium seconds left, expiration, points
//	GET /torrents/activeCount     {nb, limit}: active torrents and the account's limit
//	GET /torrents?offset=&limit=  list, newest first; X-Total-Count header; 204 when the page is empty
//	GET /torrents/info/{id}       one torrent with files (id, path, bytes, selected)
//
// The torrent list and info carry "links" (hoster links to unrestrict). They are counted (linkCount), never kept.
// GET /torrents/instantAvailability answers {"error": "disabled_endpoint"} since November 2024, so it is not called.
type realDebrid struct{ g *getter }

// rdPageSize is the list page size: RD's maximum. Above 5000 RD silently answers its default page of 100.
// Fewer, larger pages leave less room for the list to shift and cost less of the 250/min budget. A var for tests.
var rdPageSize = 5000

func (r *realDebrid) Key() string  { return r.g.provider }
func (r *realDebrid) Kind() string { return KindRealDebrid }

func (r *realDebrid) Cache() CacheSupport {
	return CacheSupport{Note: "instantAvailability answers disabled_endpoint since 2024-11; cached state comes from " +
		"the DMM and Torrentio feeds and from refused adds"}
}

func (r *realDebrid) CheckCached(context.Context, CacheQuery) (map[string]bool, error) {
	return nil, ErrCacheUnsupported
}

// rdError is RD's error body: {"error": "bad_token", "error_code": 8}.
func rdError(_ int, body []byte) *APIError {
	var e struct {
		Error     string `json:"error"`
		ErrorCode int    `json:"error_code"`
	}
	if json.Unmarshal(body, &e) != nil || e.Error == "" {
		return nil
	}
	ae := &APIError{Code: e.Error, Message: "error_code " + strconv.Itoa(e.ErrorCode)}
	switch e.ErrorCode {
	case 8, 9, 14, 22: // bad_token, permission_denied, account_locked, ip_not_allowed
		ae.kind = ErrAuth
	case 7: // unknown_ressource: the torrent is gone
		ae.kind = ErrNotFound
	}
	return ae
}

func (r *realDebrid) Account(ctx context.Context) (*Account, error) {
	var u struct {
		Type       string `json:"type"`
		Premium    int64  `json:"premium"` // seconds left
		Expiration rdTime `json:"expiration"`
		Points     int    `json:"points"`
	}
	if _, err := r.g.getJSON(ctx, "/user", nil, rdError, &u); err != nil {
		return nil, err
	}
	a := &Account{Provider: r.g.provider, Plan: u.Type, Premium: u.Type == "premium" && u.Premium > 0,
		Expires: u.Expiration.Time, Points: u.Points, ActiveTorrents: -1, MaxActiveTorrents: -1}
	var ac struct {
		NB    int `json:"nb"`
		Limit int `json:"limit"`
	}
	if _, err := r.g.getJSON(ctx, "/torrents/activeCount", nil, rdError, &ac); err != nil {
		return nil, err
	}
	a.ActiveTorrents, a.MaxActiveTorrents = ac.NB, ac.Limit
	return a, nil
}

// rdTorrent is a list item, and the info answer without files.
type rdTorrent struct {
	ID       string    `json:"id"`
	Filename string    `json:"filename"`
	Hash     string    `json:"hash"`
	Bytes    int64     `json:"bytes"`
	Progress float64   `json:"progress"`
	Status   string    `json:"status"`
	Added    rdTime    `json:"added"`
	Links    linkCount `json:"links"`
	Files    []struct {
		ID       int    `json:"id"`
		Path     string `json:"path"`
		Bytes    int64  `json:"bytes"`
		Selected int    `json:"selected"`
	} `json:"files"`
}

func (t *rdTorrent) torrent() Torrent {
	h, _ := normHash(t.Hash)
	out := Torrent{ID: t.ID, Hash: h, Name: t.Filename, Bytes: t.Bytes, Status: t.Status, State: rdState(t.Status),
		Progress: t.Progress, Added: t.Added.Time, Links: int(t.Links)}
	if t.Files != nil {
		out.Files = make([]File, 0, len(t.Files))
		for _, f := range t.Files {
			out.Files = append(out.Files, File{ID: strconv.Itoa(f.ID), Path: trimSlash(f.Path), Bytes: f.Bytes,
				Selected: f.Selected == 1})
		}
	}
	return out
}

// rdState maps RD's statuses (API docs, /torrents): magnet_error, magnet_conversion, waiting_files_selection, queued,
// downloading, downloaded, error, virus, compressing, uploading, dead.
func rdState(s string) State {
	switch s {
	case "magnet_conversion", "waiting_files_selection", "queued":
		return StateQueued
	case "downloading", "compressing", "uploading":
		return StateDownloading
	case "downloaded":
		return StateDownloaded
	case "magnet_error", "error", "virus", "dead":
		return StateError
	}
	return StateUnknown
}

// rdListAttempts bounds how often a list that came back shorter than X-Total-Count is read again.
const rdListAttempts = 3

// Torrents pages through /torrents by offset. The list is newest first and shifts while it is read: an add between
// two pages repeats an entry (deduped by id), a delete drops one at a page boundary. So a list with fewer distinct
// torrents than X-Total-Count is read again, and after rdListAttempts it fails with ErrIncomplete rather than pass
// a list that would mark contents absent wrongly. RD keeps counting a deleted torrent for a few minutes, so a sync
// right after a delete can fail this way; the job retries later. (Behaviour checked against zurg.)
func (r *realDebrid) Torrents(ctx context.Context) ([]Torrent, error) {
	for attempt := 1; ; attempt++ {
		out, total, err := r.listOnce(ctx)
		if err != nil {
			return nil, err
		}
		if total < 0 || len(out) >= total {
			return out, nil
		}
		if attempt == rdListAttempts {
			return nil, fmt.Errorf("%s: %w: %d of %d torrents after %d reads", r.g.provider, ErrIncomplete,
				len(out), total, attempt)
		}
	}
}

// listOnce reads every page once. total is the first page's X-Total-Count, -1 when absent.
func (r *realDebrid) listOnce(ctx context.Context) ([]Torrent, int, error) {
	var out []Torrent
	seen := map[string]bool{}
	total := -1
	for offset := 0; ; {
		q := url.Values{"limit": {strconv.Itoa(rdPageSize)}}
		if offset > 0 {
			q.Set("offset", strconv.Itoa(offset))
		}
		var page []rdTorrent
		resp, err := r.g.getJSON(ctx, "/torrents", q, rdError, &page)
		if err != nil {
			return nil, 0, err
		}
		if n, err := strconv.Atoi(resp.Header.Get("X-Total-Count")); err == nil && total < 0 {
			total = n
		}
		for i := range page {
			if seen[page[i].ID] {
				continue
			}
			seen[page[i].ID] = true
			out = append(out, page[i].torrent())
		}
		offset += len(page)
		if resp.StatusCode == http.StatusNoContent || len(page) < rdPageSize || (total >= 0 && offset >= total) {
			return out, total, nil
		}
	}
}

func (r *realDebrid) Torrent(ctx context.Context, id string) (*Torrent, error) {
	var t rdTorrent
	if _, err := r.g.getJSON(ctx, "/torrents/info/"+url.PathEscape(id), nil, rdError, &t); err != nil {
		return nil, err
	}
	out := t.torrent()
	return &out, nil
}

// paris is RD's clock. RD writes its dates with a "Z" but they are Paris wall-clock times (behaviour checked against
// zurg), so rdTime reads the wall clock and places it in Europe/Paris.
var paris = func() *time.Location {
	l, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return time.UTC
	}
	return l
}()

type rdTime struct{ time.Time }

func (t *rdTime) UnmarshalJSON(b []byte) error {
	var f flexTime
	_ = f.UnmarshalJSON(b)
	if f.IsZero() {
		t.Time = time.Time{}
		return nil
	}
	w := f.Time
	t.Time = time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), w.Nanosecond(), paris).UTC()
	return nil
}
