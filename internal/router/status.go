package router

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Status is the router's state as the database and the policy hold it (theatarr router status). It reads only.
type Status struct {
	Enabled        bool             `json:"enabled"`
	CachedOnly     bool             `json:"cached_only"`
	ActiveVersions []string         `json:"active_versions"`
	Versions       []VersionStatus  `json:"versions"`
	Providers      []ProviderStatus `json:"providers"`
	States         map[string]int   `json:"states"`
	Recent         []DownloadRow    `json:"recent"`
	Plans          []PlanRow        `json:"plans"`
	Misses         int              `json:"live_misses"`
	// UnfinishedChecks counts the downloads whose cached-only check the poller has yet to finish (cachedcheck.go).
	UnfinishedChecks int        `json:"unfinished_cache_checks"`
	Blocklist        []BlockRow `json:"blocklist"`
	BlocklistTotal   int        `json:"blocklist_total"`
}

// BlockRow is a hash a version never routes again.
type BlockRow struct {
	Version    string    `json:"version"`
	Hash       string    `json:"hash"`
	TitleID    int64     `json:"title_id,omitempty"`
	Release    string    `json:"release,omitempty"`
	Reason     string    `json:"reason"`
	Source     string    `json:"source"`
	DownloadID int64     `json:"download_id,omitempty"`
	Created    time.Time `json:"created"`
}

// VersionStatus is one version's gate.
type VersionStatus struct {
	Key       string   `json:"key"`
	Mode      string   `json:"mode"`
	Grab      bool     `json:"grab"`
	Writes    bool     `json:"writes"`
	Providers []string `json:"providers"`
}

// ProviderStatus is one provider's pacing.
type ProviderStatus struct {
	Key           string    `json:"key"`
	Kind          string    `json:"kind"`
	KeySet        bool      `json:"key_set"`
	AddsPerMinute int       `json:"adds_per_minute"`
	WindowAdds    int       `json:"window_adds"`
	HourUncached  int       `json:"hour_uncached"`
	CooldownUntil time.Time `json:"cooldown_until,omitzero"`
	BackoffUntil  time.Time `json:"backoff_until,omitzero"`
	Failures      int       `json:"failures"`
	Reason        string    `json:"reason,omitempty"`
}

// DownloadRow is a download as listed.
type DownloadRow struct {
	ID        int64     `json:"id"`
	TitleID   int64     `json:"title_id"`
	Version   string    `json:"version"`
	Release   string    `json:"release"`
	Hash      string    `json:"hash"`
	Provider  string    `json:"provider,omitempty"`
	TorrentID string    `json:"torrent_id,omitempty"`
	State     string    `json:"state"`
	Progress  float64   `json:"progress"`
	Error     string    `json:"error,omitempty"`
	Updated   time.Time `json:"updated"`
}

// PlanRow is a recorded dry run.
type PlanRow struct {
	ID       int64     `json:"id"`
	TitleID  int64     `json:"title_id"`
	Version  string    `json:"version"`
	Mode     string    `json:"mode"`
	Outcome  string    `json:"outcome"`
	Release  string    `json:"release,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Created  time.Time `json:"created"`
}

// ReadStatus reads the router's state. keySet reports whether a provider's API key is set (names only).
func ReadStatus(ctx context.Context, st *store.Store, pol *policy.Policy, keySet func(env string) bool,
	now time.Time, limit int) (*Status, error) {
	set := pol.RouterSettings()
	s := &Status{Enabled: set.Enabled, CachedOnly: set.CachedOnly, ActiveVersions: pol.ActiveVersions(),
		States: map[string]int{}}
	for _, k := range sortedKeys(pol.Versions) {
		v := pol.Versions[k]
		if v.Storage != policy.StorageDebrid {
			continue
		}
		s.Versions = append(s.Versions, VersionStatus{Key: k, Mode: string(v.Mode),
			Grab: v.Grab, Writes: pol.Grabs(k), Providers: v.Providers})
	}
	db := st.Reader()
	for _, k := range sortedKeys(pol.Providers) {
		pv := pol.Providers[k]
		p, err := loadPace(ctx, db, k)
		if err != nil {
			return nil, err
		}
		ps := ProviderStatus{Key: k, Kind: pv.Kind, KeySet: keySet != nil && keySet(pv.APIKeyEnv),
			AddsPerMinute: pv.Pacing.AddsPerMinute, Failures: p.Failures, Reason: p.Reason}
		if now.Sub(p.WindowStart) < time.Minute {
			ps.WindowAdds = p.WindowAdds
		}
		if sameHour(p.HourStart, now) {
			ps.HourUncached = p.HourUncached
		}
		if p.CooldownUntil.After(now) {
			ps.CooldownUntil = p.CooldownUntil
		}
		if p.BackoffUntil.After(now) {
			ps.BackoffUntil = p.BackoffUntil
		}
		s.Providers = append(s.Providers, ps)
	}
	rows, err := db.QueryContext(ctx, `SELECT state, COUNT(*) FROM download GROUP BY state`)
	if err != nil {
		return nil, fmt.Errorf("router: status: %w", err)
	}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return nil, err
		}
		s.States[k] = n
	}
	rows.Close()
	if s.Recent, err = Downloads(ctx, db, "", limit); err != nil {
		return nil, err
	}
	prow, err := db.QueryContext(ctx, `SELECT id, title_id, version_key, mode, outcome, release_title, provider,
		created_at FROM router_plan ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("router: status: %w", err)
	}
	for prow.Next() {
		var p PlanRow
		var at int64
		if err := prow.Scan(&p.ID, &p.TitleID, &p.Version, &p.Mode, &p.Outcome, &p.Release, &p.Provider, &at); err != nil {
			prow.Close()
			return nil, err
		}
		p.Created = store.FromMillis(at)
		s.Plans = append(s.Plans, p)
	}
	prow.Close()
	if err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT info_hash) FROM provider_cache WHERE state = 'miss'
		AND expires_at > ?`, store.Millis(now)).Scan(&s.Misses); err != nil {
		return nil, err
	}
	if err := db.QueryRowContext(ctx, unfinishedChecksSQL).Scan(&s.UnfinishedChecks); err != nil {
		return nil, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM router_blocklist`).Scan(&s.BlocklistTotal); err != nil {
		return nil, err
	}
	brow, err := db.QueryContext(ctx, `SELECT version_key, info_hash, COALESCE(title_id, 0), release_title, reason,
		source, COALESCE(download_id, 0), created_at FROM router_blocklist ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("router: status: %w", err)
	}
	defer brow.Close()
	for brow.Next() {
		var b BlockRow
		var at int64
		if err := brow.Scan(&b.Version, &b.Hash, &b.TitleID, &b.Release, &b.Reason, &b.Source, &b.DownloadID,
			&at); err != nil {
			return nil, err
		}
		b.Created = store.FromMillis(at)
		s.Blocklist = append(s.Blocklist, b)
	}
	return s, brow.Err()
}

// Downloads lists downloads, newest first; state "" lists every state.
func Downloads(ctx context.Context, db *sql.DB, state string, limit int) ([]DownloadRow, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.QueryContext(ctx, `SELECT id, title_id, version_key, release_title, info_hash,
		COALESCE(provider, ''), COALESCE(provider_torrent_id, ''), state, progress, COALESCE(error, ''), updated_at
		FROM download WHERE ? = '' OR state = ? ORDER BY id DESC LIMIT ?`, state, state, limit)
	if err != nil {
		return nil, fmt.Errorf("router: downloads: %w", err)
	}
	defer rows.Close()
	var out []DownloadRow
	for rows.Next() {
		var d DownloadRow
		var at int64
		if err := rows.Scan(&d.ID, &d.TitleID, &d.Version, &d.Release, &d.Hash, &d.Provider, &d.TorrentID, &d.State,
			&d.Progress, &d.Error, &at); err != nil {
			return nil, err
		}
		d.Updated = store.FromMillis(at)
		out = append(out, d)
	}
	return out, rows.Err()
}

func sortedKeys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }
