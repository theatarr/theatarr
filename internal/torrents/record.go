package torrents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
)

// The record (docs/design/torrents.md "The record"): what each account held at the provider sync's last successful
// complete listing (provider_torrent, provider_torrent_read; written by provider.WriteRecord), with the usage worked
// out once right after each sync and Refresh (StoreUsage) and stored beside it (provider_torrent's usage columns,
// provider_hash_usage). The Torrents page, Sources and Saved inventory read the stored answer with plain queries:
// no provider call, no reference index, no scan-root walk. A delete never trusts it: it lists the provider again and
// checks usage live (PlanDelete, Delete).

// reasonNotWorkedOut is the reason of a recorded torrent whose usage was not worked out yet (a sync outside serve).
const reasonNotWorkedOut = "its usage is worked out after the next provider sync; Refresh works it out now"

// stored is the usage detail kept with a record row (provider_torrent.detail, provider_hash_usage.detail).
type stored struct {
	Files    int            `json:"files"`
	Refs     map[string]int `json:"refs,omitempty"`
	RefList  []Ref          `json:"ref_list,omitempty"`
	Titles   []LinkedTitle  `json:"titles,omitempty"`
	Reasons  []string       `json:"reasons,omitempty"`
	Added    bool           `json:"added_by_theatarr,omitempty"`
	Parsed   string         `json:"parsed,omitempty"`
	Quality  string         `json:"quality,omitempty"`
	TitleKey string         `json:"title_key,omitempty"`
}

// recorded is one provider's part of the record.
type recorded struct {
	// read: the provider was listed completely at least once, at (the listing's end).
	read bool
	at   time.Time
	// unknown are the reasons every unreferenced torrent was unknown when its usage was worked out.
	unknown []string
	list    []Torrent
	// failed is the newest sync's error when it failed after the read (or with no read yet), at failedAt.
	failed   string
	failedAt time.Time
}

// readInfo reads a provider's read time, unknown reasons and the newest sync's failure after it.
func readInfo(ctx context.Context, db *sql.DB, k string) (*recorded, error) {
	rc := &recorded{}
	var at int64
	var unknown string
	err := db.QueryRowContext(ctx, `SELECT read_at, unknown FROM provider_torrent_read WHERE provider = ?`, k).
		Scan(&at, &unknown)
	switch {
	case err == nil:
		rc.read, rc.at = true, store.FromMillis(at)
		_ = json.Unmarshal([]byte(unknown), &rc.unknown)
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("torrents: reading the record: %w", err)
	}
	var ok int
	var errText sql.NullString
	var fin int64
	err = db.QueryRowContext(ctx, `SELECT ok, error, finished_at FROM provider_sync_run WHERE provider = ?
		ORDER BY finished_at DESC, id DESC LIMIT 1`, k).Scan(&ok, &errText, &fin)
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
	return rc, nil
}

// readRecord reads the record of the policy's providers with its stored usage.
func (m *Manager) readRecord(ctx context.Context) (map[string]*recorded, error) {
	db := m.Store.Reader()
	out := map[string]*recorded{}
	for _, k := range m.keys() {
		rc, err := readInfo(ctx, db, k)
		if err != nil {
			return nil, err
		}
		out[k] = rc
		if !rc.read {
			continue
		}
		rows, err := db.QueryContext(ctx, `SELECT torrent_id, info_hash, name, size_bytes, status, state, progress,
			added_at, files, usage, linked_files, detail FROM provider_torrent WHERE provider = ? ORDER BY torrent_id`, k)
		if err != nil {
			return nil, fmt.Errorf("torrents: reading the record: %w", err)
		}
		for rows.Next() {
			t := Torrent{Provider: k}
			var state, usage, detail, raw string
			var added sql.NullInt64
			if err := rows.Scan(&t.ID, &raw, &t.Name, &t.Bytes, &t.Status, &state, &t.Progress, &added, &t.Files,
				&usage, &t.LinkedFiles, &detail); err != nil {
				rows.Close()
				return nil, err
			}
			t.State = provider.State(state)
			if added.Valid {
				t.Added = store.FromMillis(added.Int64)
			}
			if t.Hash = indexer.NormalizeHash(raw); t.Hash == "" {
				t.Hash = raw
			}
			if d := DisplayName(t.Name); d != t.Name {
				t.RawName, t.Name = t.Name, d
			}
			if usage == "" {
				t.Usage, t.Reasons = Unknown, []string{reasonNotWorkedOut}
			} else {
				var s stored
				_ = json.Unmarshal([]byte(detail), &s)
				t.Usage, t.Files, t.Refs, t.RefList, t.Titles, t.Reasons = Usage(usage), s.Files, s.Refs, s.RefList,
					s.Titles, s.Reasons
				t.AddedByTheatarr, t.Parsed, t.Quality, t.titleKey = s.Added, s.Parsed, s.Quality, s.TitleKey
			}
			rc.list = append(rc.list, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// hashTorrent is one account torrent of a hash, with its own usage, for hashUsage.
type hashTorrent struct {
	t       provider.Torrent
	usage   Usage
	reasons []string
}

// hashUsage is a hash's usage across its account torrents: used when any is used, with the references to the hash
// and to every folder any of them may be under, united and counted once (then cut to maxRefList); else unknown when
// any is unknown (their reasons); else unused.
func (ix *refIndex) hashUsage(h string, ts []hashTorrent) (Usage, stored) {
	var reasons []string
	used, unknown := false, false
	for _, x := range ts {
		switch x.usage {
		case Used:
			used = true
		case Unknown:
			unknown = true
			for _, r := range x.reasons {
				if !slices.Contains(reasons, r) {
					reasons = append(reasons, r)
				}
			}
		}
	}
	if used {
		var folders []string
		for _, x := range ts {
			folders = append(folders, torrentFolders(ix.decy, nil, h, x.t)...)
		}
		if z, ok := ix.zurg.Name(h); ok {
			folders = append(folders, library.ReleaseFolderNames(h, z)...)
		}
		counts, refs, titles := ix.refsOf(h, folders)
		return Used, stored{Refs: counts, RefList: refs, Titles: titles}
	}
	if unknown {
		return Unknown, stored{Reasons: reasons}
	}
	return Unused, stored{}
}

// StoreUsage works out the usage of every recorded torrent and hash from the reference index (the database's
// references, the scan roots' walk, the mount's listing) and stores it with the record. serve runs it after each
// provider sync, and Refresh after its sync: the screens' "used" is as of then.
func (m *Manager) StoreUsage(ctx context.Context) error {
	ix, err := m.loadRefs(ctx)
	if err != nil {
		return err
	}
	type row struct {
		key   string
		lt    provider.Torrent
		files int
	}
	var list []row
	rows, err := m.Store.Reader().QueryContext(ctx, `SELECT provider, torrent_id, info_hash, name, original_name,
		size_bytes, status, state, progress, added_at, files FROM provider_torrent ORDER BY provider, torrent_id`)
	if err != nil {
		return fmt.Errorf("torrents: reading the record: %w", err)
	}
	for rows.Next() {
		var r row
		var state string
		var added sql.NullInt64
		if err := rows.Scan(&r.key, &r.lt.ID, &r.lt.Hash, &r.lt.Name, &r.lt.OriginalName, &r.lt.Bytes, &r.lt.Status,
			&state, &r.lt.Progress, &added, &r.files); err != nil {
			rows.Close()
			return err
		}
		r.lt.State = provider.State(state)
		if added.Valid {
			r.lt.Added = store.FromMillis(added.Int64)
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	now := m.now()
	type update struct {
		key, id, hash, usage, detail string
		linked                       int
	}
	var ups []update
	byHash := map[string][]hashTorrent{}
	var hashes []string
	for _, r := range list {
		t := ix.torrent(r.key, r.lt, r.files, now)
		b, err := json.Marshal(stored{Files: t.Files, Refs: t.Refs, RefList: t.RefList, Titles: t.Titles,
			Reasons: t.Reasons, Added: t.AddedByTheatarr, Parsed: t.Parsed, Quality: t.Quality, TitleKey: t.titleKey})
		if err != nil {
			return err
		}
		ups = append(ups, update{r.key, r.lt.ID, r.lt.Hash, string(t.Usage), string(b), t.LinkedFiles})
		if h := indexer.NormalizeHash(r.lt.Hash); h != "" {
			if byHash[h] == nil {
				hashes = append(hashes, h)
			}
			byHash[h] = append(byHash[h], hashTorrent{t: r.lt, usage: t.Usage, reasons: t.Reasons})
		}
	}
	unknown, err := json.Marshal(append([]string{}, ix.unknown...))
	if err != nil {
		return err
	}
	return m.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		up, err := tx.PrepareContext(ctx, `UPDATE provider_torrent SET usage = ?, linked_files = ?, detail = ?
			WHERE provider = ? AND torrent_id = ? AND info_hash = ?`)
		if err != nil {
			return err
		}
		defer up.Close()
		for _, u := range ups {
			if _, err := up.ExecContext(ctx, u.usage, u.linked, u.detail, u.key, u.id, u.hash); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM provider_hash_usage`); err != nil {
			return err
		}
		ins, err := tx.PrepareContext(ctx, `INSERT INTO provider_hash_usage (info_hash, usage, linked_files, detail)
			VALUES (?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer ins.Close()
		for _, h := range hashes {
			u, s := ix.hashUsage(h, byHash[h])
			b, err := json.Marshal(s)
			if err != nil {
				return err
			}
			if _, err := ins.ExecContext(ctx, h, string(u), s.Refs[RefLink]+s.Refs[RefScan], string(b)); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE provider_torrent_read SET unknown = ?`, string(unknown))
		return err
	})
}

// Refresh lists every provider now (the provider sync, back-off or not), writes the record and works out its usage;
// it answers when that is done. A provider whose list fails keeps its previous record, and the report names its error.
func (m *Manager) Refresh(ctx context.Context) error {
	if m.Syncer == nil {
		return ErrNoSync
	}
	_, err := m.Syncer.Refresh(ctx)
	if uerr := m.StoreUsage(ctx); uerr != nil {
		err = errors.Join(err, fmt.Errorf("working out the usage: %w", uerr))
	}
	return err
}

// ErrNoSync: no provider sync runs in this process (Refresh is unavailable).
var ErrNoSync = errors.New("torrents: the provider sync is not running")

// forget removes a deleted torrent from the record and its hash's stored usage (another account may still hold the
// hash: it reads as not worked out yet), and asks for the usage to be worked out again (UsageStale).
func (m *Manager) forget(ctx context.Context, prov, id, hash string) {
	err := m.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM provider_torrent WHERE provider = ? AND torrent_id = ?`,
			prov, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM provider_hash_usage WHERE info_hash = ?`, hash)
		return err
	})
	if err != nil {
		m.log().Error("torrents: removing a deleted torrent from the record", "provider", prov, "id", id, "err", err)
		return
	}
	if m.UsageStale != nil {
		m.UsageStale()
	}
}

// ProviderRecord is one account in the record, for Sources: when it was read and what it held then, with the
// stored usage of those torrents. Every count is of the rows.
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

// ReadProviderRecord is provider key's part of the record for Sources: plain queries, nothing worked out.
func ReadProviderRecord(ctx context.Context, db *sql.DB, key string) (ProviderRecord, error) {
	rc, err := readInfo(ctx, db, key)
	if err != nil {
		return ProviderRecord{}, err
	}
	pr := ProviderRecord{NeverRead: !rc.read, Error: rc.failed}
	if !rc.read {
		return pr, nil
	}
	pr.AsOf = &rc.at
	err = db.QueryRowContext(ctx, `SELECT count(*), COALESCE(sum(size_bytes), 0), count(DISTINCT NULLIF(info_hash, '')),
		COALESCE(sum(usage = 'used'), 0), COALESCE(sum(usage = 'unused'), 0),
		COALESCE(sum(CASE WHEN usage = 'unused' THEN size_bytes END), 0),
		COALESCE(sum(usage NOT IN ('used', 'unused')), 0),
		COALESCE(sum(CASE WHEN usage NOT IN ('used', 'unused') THEN size_bytes END), 0)
		FROM provider_torrent WHERE provider = ?`, key).Scan(&pr.AccountTorrents, &pr.Bytes, &pr.UniqueHashes,
		&pr.Usage.Used, &pr.Usage.Unused, &pr.Usage.UnusedBytes, &pr.Usage.Unknown, &pr.Usage.UnknownBytes)
	return pr, err
}

// HashUsage is a hash's stored usage (provider_hash_usage): used when any account torrent of it is used, with the
// references united across them; else unknown when any is unknown; else unused.
type HashUsage struct {
	Usage       Usage          `json:"usage"`
	LinkedFiles int            `json:"linked_files"`
	Refs        map[string]int `json:"refs,omitempty"`
	RefList     []Ref          `json:"ref_list,omitempty"`
	Titles      []LinkedTitle  `json:"titles,omitempty"`
	Reasons     []string       `json:"reasons,omitempty"`
}

// HashUsages reads the stored usage of hashes (normalized v1, lower case) for Saved inventory; a hash no account
// holds in the record is absent, and one an account holds whose usage is not worked out (yet, or again) is unknown.
func HashUsages(ctx context.Context, db *sql.DB, hashes []string) (map[string]HashUsage, error) {
	out := map[string]HashUsage{}
	if len(hashes) == 0 {
		return out, nil
	}
	b, err := json.Marshal(hashes)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT info_hash, usage, linked_files, detail FROM provider_hash_usage
		WHERE info_hash IN (SELECT value FROM json_each(?))`, string(b))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h, u, detail string
		var hu HashUsage
		if err := rows.Scan(&h, &u, &hu.LinkedFiles, &detail); err != nil {
			return nil, err
		}
		var s stored
		_ = json.Unmarshal([]byte(detail), &s)
		hu.Usage, hu.Refs, hu.RefList, hu.Titles, hu.Reasons = Usage(u), s.Refs, s.RefList, s.Titles, s.Reasons
		out[h] = hu
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	held, err := db.QueryContext(ctx, `SELECT DISTINCT info_hash FROM provider_torrent
		WHERE info_hash IN (SELECT value FROM json_each(?))`, string(b))
	if err != nil {
		return nil, err
	}
	defer held.Close()
	for held.Next() {
		var h string
		if err := held.Scan(&h); err != nil {
			return nil, err
		}
		if _, ok := out[h]; !ok {
			out[h] = HashUsage{Usage: Unknown, Reasons: []string{reasonNotWorkedOut}}
		}
	}
	return out, held.Err()
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

// uniqueHashes counts the distinct hashes of a provider's torrents.
func uniqueHashes(ts []Torrent) int {
	seen := map[string]bool{}
	for _, t := range ts {
		if t.Hash != "" {
			seen[t.Hash] = true
		}
	}
	return len(seen)
}
