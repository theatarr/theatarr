package torrents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
)

// The record (docs/design/torrents.md "The record"): what each account held at the provider sync's last successful
// complete listing (provider_torrent, provider_torrent_read; written by provider.WriteRecord). The Torrents page,
// Sources and Saved inventory read it; nothing here calls a provider. Refresh runs the sync now. A delete never
// trusts it: it lists the provider again (PlanDelete, Delete).

// recorded is one provider's part of the record.
type recorded struct {
	// read: the provider was listed completely at least once, at (the listing's end).
	read bool
	at   time.Time
	list []listed
	// failed is the newest sync's error when it failed after the read (or with no read yet), at failedAt.
	failed   string
	failedAt time.Time
}

// listed is one torrent of the record: as the listing gave it, with its selected file count (-1: the listing had
// none).
type listed struct {
	provider.Torrent
	files int
}

// readRecord reads the record of the policy's providers.
func (m *Manager) readRecord(ctx context.Context) (map[string]*recorded, error) {
	db := m.Store.Reader()
	out := map[string]*recorded{}
	for _, k := range m.keys() {
		rc := &recorded{}
		out[k] = rc
		var at int64
		err := db.QueryRowContext(ctx, `SELECT read_at FROM provider_torrent_read WHERE provider = ?`, k).Scan(&at)
		switch {
		case err == nil:
			rc.read, rc.at = true, store.FromMillis(at)
		case !errors.Is(err, sql.ErrNoRows):
			return nil, fmt.Errorf("torrents: reading the record: %w", err)
		}
		var ok int
		var errText sql.NullString
		var fin int64
		err = db.QueryRowContext(ctx, `SELECT ok, error, finished_at FROM provider_sync_run WHERE provider = ?
			ORDER BY id DESC LIMIT 1`, k).Scan(&ok, &errText, &fin)
		switch {
		case err == nil:
			if ok == 0 && (!rc.read || fin >= at) {
				rc.failed, rc.failedAt = errText.String, store.FromMillis(fin)
				if rc.failed == "" {
					rc.failed = "the last sync failed"
				}
			}
		case !errors.Is(err, sql.ErrNoRows):
			return nil, fmt.Errorf("torrents: reading sync runs: %w", err)
		}
		if !rc.read {
			continue
		}
		rows, err := db.QueryContext(ctx, `SELECT torrent_id, info_hash, name, original_name, size_bytes, status,
			state, progress, added_at, files, links FROM provider_torrent WHERE provider = ? ORDER BY torrent_id`, k)
		if err != nil {
			return nil, fmt.Errorf("torrents: reading the record: %w", err)
		}
		for rows.Next() {
			var l listed
			var state string
			var added sql.NullInt64
			if err := rows.Scan(&l.ID, &l.Hash, &l.Name, &l.OriginalName, &l.Bytes, &l.Status, &state, &l.Progress,
				&added, &l.files, &l.Links); err != nil {
				rows.Close()
				return nil, err
			}
			l.State = provider.State(state)
			if added.Valid {
				l.Added = store.FromMillis(added.Int64)
			}
			rc.list = append(rc.list, l)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Refresh lists every provider now (the provider sync, back-off or not) and writes the record; it answers when the
// sync is done. A provider whose list fails keeps its previous record, and the report names its error.
func (m *Manager) Refresh(ctx context.Context) error {
	if m.Syncer == nil {
		return ErrNoSync
	}
	_, err := m.Syncer.Refresh(ctx)
	return err
}

// ErrNoSync: no provider sync runs in this process (Refresh is unavailable).
var ErrNoSync = errors.New("torrents: the provider sync is not running")

// forget removes a deleted torrent from the record.
func (m *Manager) forget(ctx context.Context, prov, id string) {
	if _, err := m.Store.Writer().ExecContext(ctx, `DELETE FROM provider_torrent WHERE provider = ? AND torrent_id = ?`,
		prov, id); err != nil {
		m.log().Error("torrents: removing a deleted torrent from the record", "provider", prov, "id", id, "err", err)
	}
}

// ProviderRecord is one account in the record, for Sources: when it was read and what it held then, with the
// usage of those torrents.
type ProviderRecord struct {
	// AsOf is the end of the last successful complete listing; NeverRead is set (and AsOf absent) when there is none.
	AsOf      *time.Time `json:"as_of,omitempty"`
	NeverRead bool       `json:"never_read,omitempty"`
	// Error is the newest sync's error when it failed after AsOf.
	Error string `json:"error,omitempty"`
	// AccountTorrents counts the account's torrents (two of one hash are two); UniqueHashes the distinct hashes.
	AccountTorrents int       `json:"account_torrents"`
	UniqueHashes    int       `json:"unique_hashes"`
	Bytes           int64     `json:"bytes"`
	Usage           UsageSums `json:"usage"`
}

// HashUsage is a hash's usage in the record: used when any account torrent of it is used, else unknown when any is
// unknown, else unused. Refs and RefList are the used torrent's (or the unknown one's reasons).
type HashUsage struct {
	Usage       Usage          `json:"usage"`
	LinkedFiles int            `json:"linked_files"`
	Refs        map[string]int `json:"refs,omitempty"`
	RefList     []Ref          `json:"ref_list,omitempty"`
	Titles      []LinkedTitle  `json:"titles,omitempty"`
	Reasons     []string       `json:"reasons,omitempty"`
}

// RecordUsage is the Torrents page's "used" for Sources and Saved inventory: the record read with the same
// reference index, at read time.
type RecordUsage struct {
	Providers map[string]ProviderRecord
	// Hashes holds every hash an account holds in the record; a hash no account holds is absent.
	Hashes map[string]HashUsage
}

// RecordUsage reads the record and decides every torrent's usage as the report does (without parsing names).
func (m *Manager) RecordUsage(ctx context.Context) (*RecordUsage, error) {
	ix, err := m.loadRefs(ctx)
	if err != nil {
		return nil, err
	}
	rec, err := m.readRecord(ctx)
	if err != nil {
		return nil, err
	}
	r := m.build(ix, rec, false)
	out := &RecordUsage{Providers: map[string]ProviderRecord{}, Hashes: map[string]HashUsage{}}
	rank := map[Usage]int{Unused: 1, Unknown: 2, Used: 3}
	for _, p := range r.Providers {
		out.Providers[p.Provider] = ProviderRecord{AsOf: p.AsOf, NeverRead: p.NeverRead, Error: p.Error,
			AccountTorrents: p.AccountTorrents, UniqueHashes: p.UniqueHashes, Bytes: p.Bytes, Usage: p.Usage}
		for _, t := range p.Torrents {
			if t.Hash == "" {
				continue
			}
			if cur, ok := out.Hashes[t.Hash]; ok && rank[cur.Usage] >= rank[t.Usage] {
				continue
			}
			out.Hashes[t.Hash] = HashUsage{Usage: t.Usage, LinkedFiles: t.LinkedFiles, Refs: t.Refs,
				RefList: t.RefList, Titles: t.Titles, Reasons: t.Reasons}
		}
	}
	return out, nil
}

// pctEscape is a percent-encoded byte.
var pctEscape = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)

// DisplayName is a torrent name for display: a name that arrives URL-encoded (TorBox's "[SoM]%20Dragon%20Ball…": a
// %XX escape and no space) decoded, any other name as it is. Matching keeps the provider's own name.
func DisplayName(s string) string {
	if strings.Contains(s, " ") || !pctEscape.MatchString(s) {
		return s
	}
	if d, err := url.PathUnescape(s); err == nil && utf8.ValidString(d) {
		return d
	}
	return s
}

// uniqueHashes counts the distinct v1 hashes (other hashes by their own text) of a provider's torrents.
func uniqueHashes(ts []Torrent) int {
	seen := map[string]bool{}
	for _, t := range ts {
		h := indexer.NormalizeHash(t.Hash)
		if h == "" {
			h = t.Hash
		}
		if h != "" {
			seen[h] = true
		}
	}
	return len(seen)
}
