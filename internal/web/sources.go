package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/torrents"
)

// GET /sources (Admin › Connections) and GET /logs (Admin › Advanced › Logs), docs/design/ui.md.

// IndexerSource is one policy indexer with its live state.
type IndexerSource struct {
	Key     string `json:"key"`
	Kind    string `json:"kind"`
	Privacy string `json:"privacy"`
	// Disabled is an indexer switched off in Settings (setting_indexer.enabled 0): it is not searched.
	Disabled bool     `json:"disabled,omitempty"`
	Budget   string   `json:"budget,omitempty"`
	Proxy    bool     `json:"proxy"`
	Versions []string `json:"versions"`
	// Backoff is the gateway's row for indexer:<key>: the last failure (a 429, a 5xx) and any back-off.
	Backoff *Backoff `json:"backoff,omitempty"`
	// BudgetUse is the indexer's budget as /gateway shows it.
	BudgetUse *BudgetUse `json:"budget_use,omitempty"`
}

// ProviderSync is one provider_sync_run row.
type ProviderSync struct {
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt time.Time       `json:"finished_at"`
	OK         bool            `json:"ok"`
	Error      string          `json:"error,omitempty"`
	Summary    json.RawMessage `json:"summary"`
}

// ProviderPacing is the router's pacing row for a provider.
type ProviderPacing struct {
	WindowAdds    int        `json:"window_adds"`
	HourUncached  int        `json:"hour_uncached"`
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
	BackoffUntil  *time.Time `json:"backoff_until,omitempty"`
	Failures      int        `json:"failures"`
	Reason        string     `json:"reason,omitempty"`
	// LastAddAt is when the last add was sent (pacing.min_gap counts from it); HoldLevel the account-hold
	// cooldown step (0 first, 2 the 60-minute top).
	LastAddAt *time.Time `json:"last_add_at,omitempty"`
	HoldLevel int        `json:"hold_level"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ProviderSource is one policy provider with its live state.
type ProviderSource struct {
	Key      string   `json:"key"`
	Kind     string   `json:"kind"`
	Order    int      `json:"order"`
	Budget   string   `json:"budget,omitempty"`
	Versions []string `json:"versions"`
	// Counts are the registry's hashes for this provider by content_provider.state: present and absent.
	Counts map[string]int `json:"counts"`
	// Record is the account in the record (the Torrents page's): when it was read, its account torrents and unique
	// hashes, and their stored usage (worked out after the same sync).
	Record     *torrents.ProviderRecord `json:"record,omitempty"`
	LastSync   *ProviderSync            `json:"last_sync,omitempty"`
	LastOKSync *time.Time               `json:"last_ok_sync,omitempty"`
	Pacing     *ProviderPacing          `json:"pacing,omitempty"`
	// Attempts24h counts the router's download attempts in the last 24 hours by outcome (accepted, refused, ...).
	Attempts24h map[string]int `json:"attempts_24h"`
	Backoff     *Backoff       `json:"backoff,omitempty"`
	BudgetUse   *BudgetUse     `json:"budget_use,omitempty"`
}

// Sources is GET /sources.
type Sources struct {
	Indexers  []IndexerSource  `json:"indexers"`
	Providers []ProviderSource `json:"providers"`
}

func (w *Web) sources(ctx context.Context) (Sources, error) {
	out := Sources{Indexers: []IndexerSource{}, Providers: []ProviderSource{}}
	gs, err := w.gatewayState(ctx)
	if err != nil {
		return out, err
	}
	backoff := func(subject string) *Backoff {
		for _, b := range gs.Backoffs {
			if b.Subject == subject {
				bo := b
				return &bo
			}
		}
		return nil
	}
	budget := func(key string) *BudgetUse {
		for _, b := range gs.Budgets {
			if key != "" && b.Key == key {
				bu := b
				return &bu
			}
		}
		return nil
	}
	if w.pol == nil {
		return out, nil
	}
	usedBy := func(pick func(idx, prov []string) []string, key string) []string {
		vs := []string{}
		for _, vk := range sortedKeys(w.pol.Versions) {
			v := w.pol.Versions[vk]
			if slices.Contains(pick(v.Indexers, v.Providers), key) {
				vs = append(vs, vk)
			}
		}
		return vs
	}
	off := map[string]bool{}
	if err := scanAll(ctx, w.st.Reader(), `SELECT key FROM setting_indexer WHERE enabled = 0`, nil,
		func(r *sql.Rows) error {
			var k string
			if err := r.Scan(&k); err != nil {
				return err
			}
			off[k] = true
			return nil
		}); err != nil {
		return out, err
	}
	for _, k := range sortedKeys(w.pol.Indexers) {
		ix := w.pol.Indexers[k]
		priv := string(ix.Privacy)
		if priv == "" {
			priv = "unknown"
		}
		out.Indexers = append(out.Indexers, IndexerSource{Key: k, Kind: ix.Kind, Privacy: priv, Disabled: off[k], Budget: ix.Budget,
			Proxy: ix.Proxy != "", Versions: usedBy(func(i, _ []string) []string { return i }, k),
			Backoff: backoff("indexer:" + k), BudgetUse: budget(ix.Budget)})
	}
	db := w.st.Reader()
	since := w.now().Add(-24 * time.Hour).UnixMilli()
	for _, k := range sortedKeys(w.pol.Providers) {
		p := w.pol.Providers[k]
		ps := ProviderSource{Key: k, Kind: p.Kind, Order: p.Order, Budget: p.Budget,
			Versions: usedBy(func(_, pr []string) []string { return pr }, k),
			Counts:   map[string]int{"present": 0, "absent": 0}, Attempts24h: map[string]int{},
			Backoff: backoff("provider:" + k), BudgetUse: budget(p.Budget)}
		if err := scanAll(ctx, db, `SELECT state, COUNT(*) FROM content_provider WHERE provider = ? GROUP BY state`,
			[]any{k}, func(r *sql.Rows) error {
				var s string
				var n int
				if err := r.Scan(&s, &n); err != nil {
					return err
				}
				ps.Counts[s] = n
				return nil
			}); err != nil {
			return out, err
		}
		rec, rerr := torrents.ReadProviderRecord(ctx, db, k)
		if rerr != nil {
			return out, rerr
		}
		ps.Record = &rec
		var run ProviderSync
		var s, f int64
		var ok int
		var errText sql.NullString
		var sum string
		err := db.QueryRowContext(ctx, `SELECT started_at, finished_at, ok, error, summary FROM provider_sync_run
			WHERE provider = ? ORDER BY finished_at DESC, id DESC LIMIT 1`, k).Scan(&s, &f, &ok, &errText, &sum)
		switch {
		case err == nil:
			run.StartedAt, run.FinishedAt, run.OK, run.Error = ts(s), ts(f), ok == 1, errText.String
			if !json.Valid([]byte(sum)) {
				sum = "{}"
			}
			run.Summary = json.RawMessage(sum)
			ps.LastSync = &run
		case err != sql.ErrNoRows:
			return out, err
		}
		var last sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT MAX(finished_at) FROM provider_sync_run WHERE provider = ? AND ok = 1`,
			k).Scan(&last); err != nil {
			return out, err
		}
		ps.LastOKSync = nullTS(last)
		var pc ProviderPacing
		var cool, back, upd, lastAdd int64
		err = db.QueryRowContext(ctx, `SELECT window_adds, hour_uncached, cooldown_until, backoff_until, failures, reason,
			updated_at, last_add_at, hold_level FROM router_pacing WHERE provider = ?`, k).Scan(&pc.WindowAdds,
			&pc.HourUncached, &cool, &back, &pc.Failures, &pc.Reason, &upd, &lastAdd, &pc.HoldLevel)
		switch {
		case err == nil:
			pc.UpdatedAt = ts(upd)
			if cool > 0 {
				t := ts(cool)
				pc.CooldownUntil = &t
			}
			if back > 0 {
				t := ts(back)
				pc.BackoffUntil = &t
			}
			if lastAdd > 0 {
				t := ts(lastAdd)
				pc.LastAddAt = &t
			}
			ps.Pacing = &pc
		case err != sql.ErrNoRows:
			return out, err
		}
		if err := scanAll(ctx, db, `SELECT outcome, COUNT(*) FROM download_attempt WHERE provider = ? AND attempted_at >= ?
			GROUP BY outcome`, []any{k, since}, func(r *sql.Rows) error {
			var o string
			var n int
			if err := r.Scan(&o, &n); err != nil {
				return err
			}
			ps.Attempts24h[o] = n
			return nil
		}); err != nil {
			return out, err
		}
		out.Providers = append(out.Providers, ps)
	}
	slices.SortStableFunc(out.Providers, func(a, b ProviderSource) int { return a.Order - b.Order })
	return out, nil
}

func (w *Web) apiSources(rw http.ResponseWriter, r *http.Request) {
	s, err := w.sources(r.Context())
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, s)
}

// mountAdminAPI registers the sources and logs admin routes.
func (w *Web) mountAdminAPI(mux *http.ServeMux) {
	w.handle(mux, "GET "+APIBase+"/sources", SignedIn, auth.PermAdmin, w.apiSources)
	w.handle(mux, "GET "+APIBase+"/logs", SignedIn, auth.PermAdmin, w.apiLogs)
	w.handle(mux, "GET "+APIBase+"/logs/files", SignedIn, auth.PermAdmin, w.apiLogFiles)
	w.handle(mux, "GET "+APIBase+"/logs/files/{name}", SignedIn, auth.PermAdmin, w.apiLogFile)
	w.handle(mux, "GET "+APIBase+"/logs/level", SignedIn, auth.PermAdmin, w.apiLogLevel)
	w.handle(mux, "PUT "+APIBase+"/logs/level", SignedIn, auth.PermAdmin, w.apiLogLevel)
}
