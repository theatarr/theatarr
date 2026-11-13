// Request shapes ported from decypharr pkg/debrid/providers/{realdebrid/realdebrid.go,alldebrid/alldebrid.go} @
// 917cfa2 (DeleteTorrent; github.com/sirrobot01/decypharr), MIT License, Copyright (c) 2025 Mukhtar Akere. The full
// notice is in internal/provider/LICENSE.decypharr.

package adder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
)

// This file is the only delete in Theatarr. Debrid torrents are shared by every library and every client of an account,
// so automatic deletes are limited to our own adds: with router.cached_only, an uncached Real-Debrid add is deleted
// so the next provider can be tried. AllDebrid does the same only with router.alldebrid_delete_uncached (default
// off). AllDebrid returns the existing magnet id for an upload of a hash already on the account: another client
// uploading that hash between our fresh list read and our upload would be taken for ours and deleted, so the option
// is for an AllDebrid account nothing else writes to. The delete is fenced three ways:
//
//  1. Only the *Added that this adder's own AddMagnet returned can be deleted (pointer identity, kept in a ledger),
//     once, and only within ownAddWindow of the add: an id read from anywhere else, an old add, or a second call is
//     refused with ErrNotOwnAdd before any request.
//  2. The caller must prove the entry is ours ([OwnAdd]): the hash was not on the account before the add, and the
//     returned id is a new entry. The router builds that proof (internal/router, docs/design/router.md "Cached-only on
//     Real-Debrid and AllDebrid"); without both facts the delete is refused with ErrNotOwnAdd.
//  3. TorBox has no delete: its add_only_if_cached refuses an uncached add before anything is created.
//
// The second case (router.stalled_after) is [StalledDeleter.DeleteOwnStalledAdd]: a download still not ready long
// after its add fails as stalled, and its Real-Debrid torrent is deleted when the router recorded at the add that the
// entry was a new one of ours (download.own_add) and still finds it so. Real-Debrid makes a new torrent for every
// add, so an entry of ours stays ours. The router's stall timeout uses it on Real-Debrid only. AllDebrid and TorBox
// implement it for "Add to other providers" alone (the router's copy.go): a copy that is not complete after its
// settle is deleted on every provider, by the owner's decision, when the account's listing before the add did not
// show the hash (provider_copy.own_add), across a restart too.
//
// The third case is [UserDeleter.DeleteUserChosen], on all three providers: the torrent
// manager (internal/torrents, docs/design/torrents.md) deletes torrents a person picked and confirmed, Theatarr's or not, after
// a fresh list, a fresh usage check and a backup that can put them back. Its fence is a [UserChoice], which only
// [ChooseForDelete] makes and only with the backup id and the confirmation digest; boundary_test.go keeps every
// package but internal/torrents from naming either.
//
// Only internal/router and internal/torrents import this package (docs/dev.md).

// ownAddWindow bounds how long after an add its delete is allowed: one route attempt (an add, a few status reads
// with a grace of seconds), never a later poll or route.
const ownAddWindow = 2 * time.Minute

// ErrNotOwnAdd: the delete was refused because the entry is not provably an add this adder just made.
var ErrNotOwnAdd = errors.New("adder: not an add of ours from this route attempt; nothing deleted")

// OwnAdd is the caller's proof that an add created a new entry of ours. Both must be true.
type OwnAdd struct {
	// NotOnAccountBefore: the hash was not on the provider's account before the add (registry, our downloads, and on
	// AllDebrid a fresh read of the account's magnets).
	NotOnAccountBefore bool
	// NewEntry: the id the add returned names a new entry (Real-Debrid always creates one; on AllDebrid the id was
	// not in the magnet list read before the add).
	NewEntry bool
}

// Deleter is implemented by the Real-Debrid and AllDebrid adders only. It is not part of [Adder], so code that holds
// an Adder sees no delete; the router asserts it at the one place it may delete.
type Deleter interface {
	// DeleteOwnUncachedAdd deletes the entry a, which this adder's AddMagnet returned moments ago, given proof that
	// it is ours. ErrNotOwnAdd (no request sent) when a is not such an add or the proof is incomplete; an *Error when
	// the provider refused or failed.
	DeleteOwnUncachedAdd(ctx context.Context, a *Added, proof OwnAdd) error
}

// StalledDeleter is implemented by every adder (AllDebrid and TorBox for the router's copies only). Like Deleter, it
// is not part of [Adder].
type StalledDeleter interface {
	// DeleteOwnStalledAdd deletes torrent id, which the router added itself (proof: recorded at the add, and the
	// entry still holds the hash and is known to no other download or registry content). ErrNotOwnAdd (no request)
	// without the full proof.
	DeleteOwnStalledAdd(ctx context.Context, id string, proof OwnAdd) error
}

// ledger remembers the adds this adder made, by the *Added it returned.
type ledger struct {
	mu  sync.Mutex
	now func() time.Time
	ids map[*Added]time.Time
}

func newLedger() *ledger { return &ledger{now: time.Now, ids: map[*Added]time.Time{}} }

// issue records an accepted add.
func (l *ledger) issue(a *Added) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, v := range l.ids {
		if now.Sub(v) > ownAddWindow {
			delete(l.ids, k)
		}
	}
	l.ids[a] = now
}

// take checks a and the proof and removes a from the ledger: one delete per add.
func (l *ledger) take(a *Added, proof OwnAdd) error {
	if a == nil || a.ID == "" || !proof.NotOnAccountBefore || !proof.NewEntry {
		return ErrNotOwnAdd
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	at, ok := l.ids[a]
	if !ok {
		return ErrNotOwnAdd
	}
	delete(l.ids, a)
	if l.now().Sub(at) > ownAddWindow {
		return ErrNotOwnAdd
	}
	return nil
}

// DeleteOwnUncachedAdd: Real-Debrid DELETE /torrents/delete/{id}, 204. RD creates a new torrent for every
// addMagnet, even of a hash already on the account, so the id is always a new entry; the proof is still required.
func (r *realDebrid) DeleteOwnUncachedAdd(ctx context.Context, a *Added, proof OwnAdd) error {
	if err := r.own.take(a, proof); err != nil {
		return err
	}
	return r.deleteTorrent(ctx, a.ID)
}

// DeleteOwnStalledAdd: the same Real-Debrid request as DeleteOwnUncachedAdd, for an add of an earlier route.
func (r *realDebrid) DeleteOwnStalledAdd(ctx context.Context, id string, proof OwnAdd) error {
	if id == "" || !proof.NotOnAccountBefore || !proof.NewEntry {
		return ErrNotOwnAdd
	}
	return r.deleteTorrent(ctx, id)
}

func (r *realDebrid) deleteTorrent(ctx context.Context, id string) error {
	status, b, ra, err := r.p.send(ctx, http.MethodDelete, "/torrents/delete/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	if status == http.StatusNoContent || status == http.StatusOK {
		return nil
	}
	return r.err(status, b, ra, false)
}

// DeleteOwnUncachedAdd: AllDebrid POST /v4/magnet/delete, form id=<id>; {"status": "success", "data": {"message"}}
// (docs; v4.1 changed only upload and status). An upload of a hash already on the account returns the existing
// magnet's id, which is why the proof needs the id to be absent from the list read before the add.
func (a *allDebrid) DeleteOwnUncachedAdd(ctx context.Context, ad *Added, proof OwnAdd) error {
	if err := a.own.take(ad, proof); err != nil {
		return err
	}
	return a.deleteMagnet(ctx, ad.ID)
}

// DeleteOwnStalledAdd: the same AllDebrid request as DeleteOwnUncachedAdd, for a copy of ours (internal/router
// copy.go) whose add the router proved new from the magnet list read before it.
func (a *allDebrid) DeleteOwnStalledAdd(ctx context.Context, id string, proof OwnAdd) error {
	if id == "" || !proof.NotOnAccountBefore || !proof.NewEntry {
		return ErrNotOwnAdd
	}
	return a.deleteMagnet(ctx, id)
}

func (a *allDebrid) deleteMagnet(ctx context.Context, id string) error {
	status, b, ra, err := a.p.send(ctx, http.MethodPost, "/v4/magnet/delete", url.Values{"id": {id}})
	if err != nil {
		return err
	}
	var env struct {
		Status string   `json:"status"`
		Error  *adError `json:"error"`
	}
	if json.Unmarshal(b, &env) != nil || env.Status != "success" || env.Error != nil {
		return a.err(status, env.Error, ra)
	}
	return nil
}

// DeleteOwnStalledAdd: the same TorBox request as DeleteUserChosen, for a copy of ours (internal/router copy.go)
// whose add the router proved new from the torrent list read before it.
func (t *torBox) DeleteOwnStalledAdd(ctx context.Context, id string, proof OwnAdd) error {
	if id == "" || !proof.NotOnAccountBefore || !proof.NewEntry {
		return ErrNotOwnAdd
	}
	return t.deleteTorrent(ctx, id)
}

// UserChoice is one torrent a person chose to delete: its provider id and hash as a fresh list showed them, the
// backup taken before the delete, and the digest of the confirmed set. Only ChooseForDelete makes one.
type UserChoice struct {
	id, hash, backup, confirmed string
}

// ErrNotChosen: the delete was refused because the choice lacks an id, a v1 hash, a backup or a confirmation.
var ErrNotChosen = errors.New("adder: not a confirmed choice with a backup; nothing deleted")

// ChooseForDelete makes the choice for torrent id with hash, after backup (its id) and confirmation (the digest of
// the confirmed set). Each must be set; the hash must be a v1 info hash.
func ChooseForDelete(id, hash, backup, confirmation string) (UserChoice, error) {
	h := indexer.NormalizeHash(hash)
	if id == "" || h == "" || backup == "" || confirmation == "" {
		return UserChoice{}, ErrNotChosen
	}
	return UserChoice{id: id, hash: h, backup: backup, confirmed: confirmation}, nil
}

func (c UserChoice) ok() bool {
	return c.id != "" && c.hash != "" && c.backup != "" && c.confirmed != ""
}

// UserDeleter is implemented by every adder. Like Deleter it is not part of Adder; internal/torrents asserts it.
type UserDeleter interface {
	// DeleteUserChosen deletes the torrent c names. ErrNotChosen (no request) for a zero choice; an *Error when the
	// provider refused or failed.
	DeleteUserChosen(ctx context.Context, c UserChoice) error
}

// DeleteUserChosen: the same Real-Debrid request as the router's deletes.
func (r *realDebrid) DeleteUserChosen(ctx context.Context, c UserChoice) error {
	if !c.ok() {
		return ErrNotChosen
	}
	return r.deleteTorrent(ctx, c.id)
}

// DeleteUserChosen: AllDebrid POST /v4/magnet/delete, form id=<id>; {"status": "success", "data": {"message"}}.
func (a *allDebrid) DeleteUserChosen(ctx context.Context, c UserChoice) error {
	if !c.ok() {
		return ErrNotChosen
	}
	return a.deleteMagnet(ctx, c.id)
}

// DeleteUserChosen: TorBox POST /api/torrents/controltorrent, JSON {"torrent_id": <id>, "operation": "delete"}
// (API docs; decypharr's DeleteTorrent); {"success": true, ...}.
func (t *torBox) DeleteUserChosen(ctx context.Context, c UserChoice) error {
	if !c.ok() {
		return ErrNotChosen
	}
	return t.deleteTorrent(ctx, c.id)
}

func (t *torBox) deleteTorrent(ctx context.Context, tid string) error {
	id, err := strconv.Atoi(tid)
	if err != nil {
		return t.p.newError(CodeInvalid, 0, "", "TorBox torrent ids are numbers")
	}
	body, _ := json.Marshal(struct {
		TorrentID int    `json:"torrent_id"`
		Operation string `json:"operation"`
	}{id, "delete"})
	status, b, ra, err := t.p.sendJSON(ctx, "/api/torrents/controltorrent", body)
	if err != nil {
		return err
	}
	var env struct {
		Success bool            `json:"success"`
		Error   json.RawMessage `json:"error"`
		Detail  string          `json:"detail"`
	}
	_ = json.Unmarshal(b, &env)
	if !env.Success || status < 200 || status >= 300 {
		ecode := strings.Trim(string(env.Error), `"`)
		if ecode == "null" {
			ecode = ""
		}
		return t.err(status, ecode, env.Detail, ra)
	}
	return nil
}

// sendJSON POSTs a JSON body (TorBox's control endpoint takes no form) through the gateway, like send.
func (p *poster) sendJSON(ctx context.Context, path string, body []byte) (int, []byte, time.Duration, error) {
	u := *p.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, 0, fmt.Errorf("%s: bad request", p.provider)
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.doer.Do(ctx, gateway.Target{Class: gateway.ClassProvider, Key: p.provider}, req)
	if err != nil {
		e := &Error{Provider: p.provider, Code: CodeUnavailable, Message: "request not sent or failed", Err: err}
		var r *gateway.Refused
		if errors.As(err, &r) {
			e.Message = "gateway refused: " + string(r.Reason)
			e.RetryAt = r.RetryAt
		}
		return 0, nil, 0, e
	}
	return p.readResponse(resp)
}
