package torrents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/torrents/reffence"
)

// MaxDelete bounds one delete request.
const MaxDelete = 200

// YoungAfter: a torrent added less than this ago is not deleted without AllowRecent.
const YoungAfter = 24 * time.Hour

// deletePace spaces the deletes of one provider, on top of the gateway's budgets.
const deletePace = 250 * time.Millisecond

// Delete actions in a plan.
const (
	ActDelete = "delete"
	ActRefuse = "refuse"
)

// DeleteSelection is one torrent a person chose, as the report showed it.
type DeleteSelection struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Hash     string `json:"hash"`
	// Override deletes a used or unknown torrent; OverrideToken must be the item's override_token from a preview,
	// which names its references (the second confirmation).
	Override      bool   `json:"override,omitempty"`
	OverrideToken string `json:"override_token,omitempty"`
	// AllowRecent deletes a torrent added in the last 24 hours or still downloading.
	AllowRecent bool `json:"allow_recent,omitempty"`
}

// DeleteItem is one selection in a plan.
type DeleteItem struct {
	DeleteSelection
	Name    string         `json:"name"`
	Bytes   int64          `json:"bytes"`
	Added   time.Time      `json:"added,omitzero"`
	State   provider.State `json:"state,omitempty"`
	Usage   Usage          `json:"usage,omitempty"`
	Refs    map[string]int `json:"refs,omitempty"`
	RefList []Ref          `json:"ref_list,omitempty"`
	Reasons []string       `json:"reasons,omitempty"`
	Action  string         `json:"action"`
	// Refusal says why a selection is refused; NeedsOverride and NeedsRecent say which flag would allow it.
	Refusal       string `json:"refusal,omitempty"`
	NeedsOverride bool   `json:"needs_override,omitempty"`
	NeedsRecent   bool   `json:"needs_recent,omitempty"`
	// ExpectedOverride is the override token this item needs (set when it is used or unknown).
	ExpectedOverride string `json:"expected_override_token,omitempty"`
	Outcome          string `json:"outcome,omitempty"`
	Error            string `json:"error,omitempty"`
	// Note says how a delete was settled when the provider's answer alone did not say (TorBox's 500 for a gone id).
	Note string `json:"note,omitempty"`
}

// DeletePlan is a delete's preview: what would go, what is refused, and the token confirming exactly that set.
type DeletePlan struct {
	Items   []DeleteItem `json:"items"`
	Count   int          `json:"count"`
	Bytes   int64        `json:"bytes"`
	Refused int          `json:"refused"`
	Digest  string       `json:"digest"`
	Token   string       `json:"confirmation_token"`
	// Warning is said with every delete: other apps on the account may use these torrents.
	Warning string `json:"warning"`
	// BackupID is set by Delete: the backup taken before it.
	BackupID string `json:"backup_id,omitempty"`
	Deleted  int    `json:"deleted"`
	Failed   int    `json:"failed"`

	lists map[string][]provider.Torrent
}

// DeleteWarning is the plan's warning.
const DeleteWarning = "Debrid accounts are shared: another app or person using the same account may rely on a " +
	"torrent Theatarr sees as unused. A full backup of every provider is taken first; a deleted torrent can be " +
	"re-added from it while the provider still holds the content."

// ErrTooMany: more selections than MaxDelete.
var ErrTooMany = fmt.Errorf("torrents: at most %d torrents per delete", MaxDelete)

// PlanDelete previews sel against fresh provider lists and a fresh usage check. It writes nothing.
func (m *Manager) PlanDelete(ctx context.Context, sel []DeleteSelection) (*DeletePlan, error) {
	if len(sel) == 0 {
		return nil, errors.New("torrents: nothing selected")
	}
	if len(sel) > MaxDelete {
		return nil, ErrTooMany
	}
	ix, err := m.loadRefs(ctx)
	if err != nil {
		return nil, err
	}
	plan := &DeletePlan{Items: []DeleteItem{}, Warning: DeleteWarning, lists: map[string][]provider.Torrent{}}
	byID := map[string]map[string]provider.Torrent{}
	listErr := map[string]error{}
	now := m.now()
	seen := map[string]bool{}
	for _, s := range sel {
		it := DeleteItem{DeleteSelection: s, Action: ActRefuse}
		it.Hash = strings.ToLower(strings.TrimSpace(s.Hash))
		key := s.Provider + "\x00" + s.ID
		if _, ok := byID[s.Provider]; !ok && listErr[s.Provider] == nil {
			if m.Policy.Providers[s.Provider] == nil {
				listErr[s.Provider] = fmt.Errorf("provider %q is not in the policy", s.Provider)
			} else if list, err := m.list(ctx, s.Provider); err != nil {
				listErr[s.Provider] = err
			} else {
				plan.lists[s.Provider] = list
				byID[s.Provider] = map[string]provider.Torrent{}
				for _, t := range list {
					byID[s.Provider][t.ID] = t
				}
			}
		}
		t, found := byID[s.Provider][s.ID]
		switch {
		case seen[key]:
			it.Refusal = "selected twice"
		case listErr[s.Provider] != nil:
			it.Refusal = "the provider could not be listed: " + listErr[s.Provider].Error()
		case !found:
			it.Refusal = "no longer on the provider"
		case indexer.NormalizeHash(t.Hash) == "" || indexer.NormalizeHash(t.Hash) != indexer.NormalizeHash(it.Hash):
			it.Refusal = "the torrent with this id has another hash now"
		}
		seen[key] = true
		if found && it.Refusal == "" {
			it.Hash = indexer.NormalizeHash(t.Hash)
			it.Name, it.Bytes, it.Added, it.State = t.Name, t.Bytes, t.Added, t.State
			it.Usage, it.Refs, it.RefList, it.Reasons = ix.usage(s.Provider, t)
			if it.Usage != Unused {
				it.ExpectedOverride = overrideToken(s.Provider, t, it.Usage, it.Refs, it.RefList, it.Reasons)
			}
			recent := t.Added.IsZero() || now.Sub(t.Added) < YoungAfter
			busy := t.State == provider.StateQueued || t.State == provider.StateDownloading
			switch {
			case it.Usage != Unused && !s.Override:
				it.NeedsOverride = true
				it.Refusal = fmt.Sprintf("%s: %s", it.Usage, describe(it.Refs, it.Reasons))
			case it.Usage != Unused && s.OverrideToken != it.ExpectedOverride:
				it.NeedsOverride = true
				it.Refusal = "the override does not name this torrent's references as they are now: " +
					describe(it.Refs, it.Reasons)
			case (recent || busy) && !s.AllowRecent:
				it.NeedsRecent = true
				it.Refusal = "added in the last 24 hours (or the date is unknown)"
				if busy {
					it.Refusal = "still downloading"
				}
			default:
				it.Action = ActDelete
			}
		}
		if it.Action == ActDelete {
			plan.Count++
			plan.Bytes += it.Bytes
		} else {
			plan.Refused++
		}
		plan.Items = append(plan.Items, it)
	}
	digest, token := deleteToken(plan)
	plan.Digest, plan.Token = digest, callerToken(ctx, "d2-", token)
	return plan, nil
}

func describe(refs map[string]int, reasons []string) string {
	if len(refs) > 0 {
		var parts []string
		for _, k := range []string{RefLink, RefArrFile, RefDownload, RefScan} {
			if n := refs[k]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, k))
			}
		}
		return "referenced by " + strings.Join(parts, ", ")
	}
	return strings.Join(reasons, "; ")
}

// overrideToken names a used or unknown torrent's references: an override is valid only for them.
func overrideToken(prov string, t provider.Torrent, u Usage, refs map[string]int, list []Ref,
	reasons []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s|%s", prov, t.ID, indexer.NormalizeHash(t.Hash), u)
	keys := make([]string, 0, len(refs))
	for k := range refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "|%s=%d", k, refs[k])
	}
	for _, r := range list {
		fmt.Fprintf(&b, "|%s:%s", r.Kind, r.Detail)
	}
	for _, r := range reasons {
		fmt.Fprintf(&b, "|%s", r)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "o1-" + hex.EncodeToString(sum[:8])
}

// deleteToken is the plan digest of the exact set: count, total size and a digest of each deleted torrent's
// provider, id, hash, size, usage and flags (callerToken binds it to the caller).
func deleteToken(p *DeletePlan) (string, string) {
	var lines []string
	for _, it := range p.Items {
		if it.Action == ActDelete {
			lines = append(lines, fmt.Sprintf("%s|%s|%s|%d|%s|%t|%s|%t", it.Provider, it.ID, it.Hash, it.Bytes,
				it.Usage, it.Override, it.OverrideToken, it.AllowRecent))
		}
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	digest := hex.EncodeToString(sum[:8])
	return digest, fmt.Sprintf("d1-%d-%d-%s", len(lines), p.Bytes, digest)
}

// ErrBackupFailed: the backup before a delete failed, so nothing was deleted.
var ErrBackupFailed = errors.New("torrents: the backup before the delete failed; nothing was deleted")

// Delete deletes the selections a fresh plan allows, when token confirms exactly that plan: it plans again (fresh
// lists, fresh usage), takes a full backup (no backup, no delete), then deletes one by one through
// adder.UserDeleter, journaling each. A refused selection is never deleted. A provider answering with a rate,
// account or transport error stops its part of the run.
//
// The backup can take minutes. Each delete then runs under the router's add lock: a route that reused or
// re-added one of the torrents meanwhile (on AllDebrid an add of a hash on the account hands back the same magnet)
// has a download referencing it, and no route adds between an item's last usage check and the provider's answer.
// Each item's usage is checked once more right before its own delete, after the pacing wait, under the reference
// fence (reffence): a library link or arr file written while the earlier deletes ran counts, and no
// such reference is written between that check and the provider's answer. The fence is taken before the add lock:
// its wait for link writes in progress (at most reffence.HoldWait) never holds up routing. An item whose usage changed since the plan is skipped. The scan roots are walked once,
// after the backup; only the references Theatarr writes are read again per item.
func (m *Manager) Delete(ctx context.Context, sel []DeleteSelection, token, by string) (*DeletePlan, error) {
	opMu.Lock()
	defer opMu.Unlock()
	plan, err := m.PlanDelete(ctx, sel)
	if err != nil {
		return nil, err
	}
	if !tokenMatches(token, plan.Token) {
		return plan, ErrConfirmation
	}
	if plan.Count == 0 {
		return plan, nil
	}
	need := map[string]map[string]bool{}
	for _, it := range plan.Items {
		if it.Action == ActDelete {
			if need[it.Provider] == nil {
				need[it.Provider] = map[string]bool{}
			}
			need[it.Provider][it.ID] = true
		}
	}
	b, _, err := m.TakeBackup(ctx, BackupOptions{Reason: ReasonPreDelete, By: by, Lists: plan.lists, Need: need})
	if err == nil {
		err = backupHolds(b, plan)
	}
	if err != nil {
		m.journal(ctx, JournalEntry{Action: "backup", Outcome: "failed", Detail: err.Error(), By: by})
		return plan, fmt.Errorf("%w: %v", ErrBackupFailed, err)
	}
	plan.BackupID = b.ID
	m.journal(ctx, JournalEntry{Action: "backup", BackupID: b.ID, Outcome: "ok", By: by,
		Detail: fmt.Sprintf("before deleting %d torrents", plan.Count)})
	scan := newScanIndex()
	m.scanRefs(ctx, scan)
	stopped := map[string]string{}
	last := map[string]bool{}
	for i := range plan.Items {
		it := &plan.Items[i]
		if it.Action != ActDelete {
			continue
		}
		if why := stopped[it.Provider]; why != "" {
			it.Outcome, it.Error = OutSkipped, why
			continue
		}
		if err := ctx.Err(); err != nil {
			it.Outcome, it.Error = OutSkipped, err.Error()
			continue
		}
		if last[it.Provider] {
			if err := m.sleep(ctx, deletePace); err != nil {
				it.Outcome, it.Error = OutSkipped, err.Error()
				continue
			}
		}
		sent, err := m.fencedDelete(ctx, plan, it, scan, b.ID)
		if sent {
			last[it.Provider] = true
		}
		switch {
		case !sent: // skipped; it says why
		case err != nil:
			it.Outcome, it.Error = OutFailed, err.Error()
			plan.Failed++
			var e *adder.Error
			if !errors.As(err, &e) || !e.Code.Content() {
				stopped[it.Provider] = "stopped after an error: " + err.Error()
			}
		default:
			it.Outcome = "deleted"
			plan.Deleted++
		}
		m.journal(ctx, JournalEntry{Action: "delete", Provider: it.Provider, TorrentID: it.ID, Hash: it.Hash,
			Name: it.Name, Bytes: it.Bytes, BackupID: b.ID, Outcome: it.Outcome, Detail: deleteDetail(it), By: by})
	}
	return plan, nil
}

// deleteBound bounds one provider delete (with TorBox's list read after a 5xx), within the fence's lease.
const deleteBound = reffence.LeaseFor - time.Minute

// fencedDelete deletes it under the reference fence (reffence) and then the router's add lock, after checking its
// usage once more.
// sent says whether the provider was asked, and err is its answer; when not, it is skipped and says why.
func (m *Manager) fencedDelete(ctx context.Context, plan *DeletePlan, it *DeleteItem, scan *refIndex,
	backup string) (bool, error) {
	release, err := reffence.Hold(ctx, m.Store, m.now())
	if err != nil {
		it.Outcome, it.Error = OutSkipped, "not deleted: "+err.Error()
		return false, nil
	}
	defer release()
	if m.AddLock != nil {
		m.AddLock.Lock()
		defer m.AddLock.Unlock()
	}
	why, err := m.usageChanged(ctx, plan, it, scan)
	if err != nil {
		it.Outcome, it.Error = OutSkipped, "checking its usage: "+err.Error()
		return false, nil
	}
	if why != "" {
		it.Outcome, it.Error = OutSkipped, why
		return false, nil
	}
	dctx, cancel := context.WithTimeout(ctx, deleteBound)
	defer cancel()
	return true, m.deleteOne(dctx, it, backup, plan.Digest)
}

// usageChanged checks the usage of one of the plan's deletes again (right before it): why it is not deleted when
// its usage or references are no longer what the plan (and its confirmation) saw. The references Theatarr writes are read
// now, only those this torrent's usage can see (loadDBRefsFor, not the whole index per item); scan holds the scan
// roots' links as the delete walked them after its backup.
func (m *Manager) usageChanged(ctx context.Context, plan *DeletePlan, it *DeleteItem, scan *refIndex) (string,
	error) {
	var t provider.Torrent
	for _, lt := range plan.lists[it.Provider] {
		if lt.ID == it.ID {
			t = lt
			break
		}
	}
	h := indexer.NormalizeHash(t.Hash)
	on, decy := m.decypharrIndex(ctx)
	ix, err := m.loadDBRefsFor(ctx, &refScope{hash: h, folders: folderNames(decy, h, t.Name), decyOn: on, decy: decy})
	if err != nil {
		return "", err
	}
	ix.withScan(scan)
	u, refs, list, reasons := ix.usage(it.Provider, t)
	token := ""
	if u != Unused {
		token = overrideToken(it.Provider, t, u, refs, list, reasons)
	}
	if u == it.Usage && token == it.ExpectedOverride {
		return "", nil
	}
	what := string(u)
	if u != Unused {
		what += ": " + describe(refs, reasons)
	}
	return "its usage changed since the preview (now " + what + "); preview again", nil
}

func deleteDetail(it *DeleteItem) string {
	var parts []string
	if it.Override {
		parts = append(parts, "override: "+string(it.Usage)+", "+describe(it.Refs, it.Reasons))
	}
	if it.AllowRecent {
		parts = append(parts, "recent or downloading allowed")
	}
	if it.Error != "" {
		parts = append(parts, it.Error)
	}
	if it.Note != "" {
		parts = append(parts, it.Note)
	}
	return strings.Join(parts, "; ")
}

func (m *Manager) deleteOne(ctx context.Context, it *DeleteItem, backup, digest string) error {
	ad := m.Adders[it.Provider]
	if ad == nil {
		return ErrNoClient
	}
	ud, ok := ad.(adder.UserDeleter)
	if !ok {
		return fmt.Errorf("torrents: %s cannot delete", it.Provider)
	}
	c, err := adder.ChooseForDelete(it.ID, it.Hash, backup, digest)
	if err != nil {
		return err
	}
	err = ud.DeleteUserChosen(ctx, c)
	if gone := m.goneAfterServerError(ctx, it, err); gone {
		it.Note = fmt.Sprintf("%s answered HTTP %d to the delete; a fresh list no longer shows torrent %s, so it is "+
			"gone", it.Provider, statusOf(err), it.ID)
		return nil
	}
	return err
}

// goneAfterServerError reports whether a TorBox delete that failed with a 5xx left the torrent gone after all: TorBox
// answers a delete of an unknown or already-deleted id with HTTP 500 DATABASE_ERROR
// (https://notes.debridmediamanager.com/zurg/internals/torrent-lifecycle/, "TorBox answers a delete for an unknown id
// with HTTP 500"), and its single-torrent read (mylist?id=) answers 500 for a gone id too, so the account's list is
// read again. Gone means the list read succeeded and holds neither the id nor the hash. Any other answer, a failed
// read or a listed torrent keeps the error.
func (m *Manager) goneAfterServerError(ctx context.Context, it *DeleteItem, err error) bool {
	var e *adder.Error
	if err == nil || !errors.As(err, &e) || e.Status < 500 {
		return false
	}
	if pv := m.Policy.Providers[it.Provider]; pv == nil || pv.Kind != provider.KindTorBox {
		return false
	}
	list, lerr := m.list(ctx, it.Provider)
	if lerr != nil {
		return false
	}
	for _, t := range list {
		if t.ID == it.ID || (it.Hash != "" && strings.EqualFold(t.Hash, it.Hash)) {
			return false
		}
	}
	return true
}

func statusOf(err error) int {
	var e *adder.Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// backupHolds checks that b lists every torrent the plan deletes, with its selection: the selected files, or that
// the provider still awaited a selection (a restore leaves that to the person).
func backupHolds(b *Backup, plan *DeletePlan) error {
	have := map[string]bool{}
	for _, p := range b.Providers {
		for _, t := range p.Torrents {
			if t.selectionKnown() {
				have[p.Provider+"\x00"+t.ID+"\x00"+t.Hash] = true
			}
		}
	}
	for _, it := range plan.Items {
		if it.Action == ActDelete && !have[it.Provider+"\x00"+it.ID+"\x00"+it.Hash] {
			return fmt.Errorf("the backup does not hold %s %s with its file selection", it.Provider, it.ID)
		}
	}
	return nil
}
