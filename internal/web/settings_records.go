package web

// Settings read responses and policy/record projections.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/theatarr/theatarr/internal/settings"
)

// SettingsRecord is one indexer, provider or budget as the API shows it. Config uses the policy YAML field names
// without api_key_env; credentials appear only as secret_set.
type SettingsRecord struct {
	ID             int64                      `json:"id,omitempty"`
	Key            string                     `json:"key"`
	Kind           string                     `json:"kind,omitempty"`
	Enabled        *bool                      `json:"enabled,omitempty"`
	Priority       *int                       `json:"priority,omitempty"`
	Order          *int                       `json:"order,omitempty"`
	Config         map[string]any             `json:"config"`
	Specification  json.RawMessage            `json:"specification,omitempty"`
	Specifications map[string]json.RawMessage `json:"specifications,omitempty"`
	SecretSet      *bool                      `json:"secret_set,omitempty"`
	CreatedAt      *time.Time                 `json:"created_at,omitempty"`
	UpdatedAt      *time.Time                 `json:"updated_at,omitempty"`
	// LastTest is this process's latest test of the saved record (kept in memory, not stored).
	LastTest *SettingsTestResult `json:"last_test,omitempty"`
	// Backoff is the gateway's back-off row for the source (indexer:<key> or provider:<key>), if any.
	Backoff *Backoff `json:"backoff,omitempty"`
	// Usage is the gateway's current use of a budget.
	Usage *BudgetUse `json:"usage,omitempty"`
	// Requests are an indexer's requests by purpose, from the gateway.
	Requests *IndexerRequests `json:"requests,omitempty"`
}

// SettingsOverview is GET /settings.
type SettingsOverview struct {
	// Source is db (the settings database is live) only.
	Source           string   `json:"source"`
	Generation       int64    `json:"generation"`
	Revision         string   `json:"revision,omitempty"`
	ActiveGeneration int64    `json:"active_generation"`
	ProviderEpoch    int64    `json:"provider_epoch"`
	Writable         bool     `json:"writable"`
	PolicyDir        string   `json:"policy_dir"`
	YAMLOwned        []string `json:"yaml_owned"`
	// Switches are the running values, with their authority and any environment overrides.
	Switches  SettingsSwitches `json:"switches"`
	Indexers  []SettingsRecord `json:"indexers"`
	Providers []SettingsRecord `json:"providers"`
	Budgets   []SettingsRecord `json:"budgets"`
}

// SettingsSwitches is Pause as the running policy has it: Paused when router.enabled is off (both runtime switches
// follow Pause), PauseForced when THEATARR_FORCE_OFF forces it, NotifyForced when it forces notifications off.
type SettingsSwitches struct {
	Paused       bool                  `json:"paused"`
	PauseForced  bool                  `json:"pause_forced"`
	NotifyForced bool                  `json:"notify_forced"`
	Versions     []SettingsVersionMode `json:"versions"`
}

// SettingsVersionMode is one version's running mode and grab gates.
type SettingsVersionMode struct {
	Key       string           `json:"key"`
	Mode      string           `json:"mode"`
	GrabState VersionGrabState `json:"grab_state"`
}

// settingsSwitches reads the switches from the active runtime's policy, else the web layer's own.
func (w *Web) settingsSwitches() SettingsSwitches {
	pol := w.pol
	if w.settings != nil {
		if rt := w.settings.Current(); rt != nil && rt.Policy != nil {
			pol = rt.Policy
		}
	}
	out := SettingsSwitches{Paused: true, Versions: []SettingsVersionMode{}}
	if pol == nil {
		return out
	}
	out.Paused = pol.Router == nil || !pol.Router.Enabled
	out.PauseForced = slices.Contains(pol.ForcedOff, "pause")
	out.NotifyForced = slices.Contains(pol.ForcedOff, "notify")
	for _, k := range sortedKeys(pol.Versions) {
		out.Versions = append(out.Versions, SettingsVersionMode{Key: k, Mode: string(pol.Versions[k].Mode),
			GrabState: versionGrabState(pol, k)})
	}
	return out
}

// ---- reads ----

func (w *Web) apiSettingsOverview(rw http.ResponseWriter, r *http.Request) {
	o := SettingsOverview{Source: "db", PolicyDir: w.policyDir, YAMLOwned: yamlOwned, Switches: w.settingsSwitches()}
	if w.refuseUnavailable(rw) {
		return
	}
	snap, ok := w.settingsHead(rw, r)
	if !ok {
		return
	}
	o.Source, o.Writable, o.Generation, o.Revision, o.ProviderEpoch = "db", true, snap.Generation, snap.Revision, snap.ProviderEpoch
	if active := w.settings.Current(); active != nil {
		o.ActiveGeneration = active.Generation
	}
	o.Indexers, o.Providers, o.Budgets = w.settingsRecords(r.Context(), snap, entityIndexers), w.settingsRecords(r.Context(), snap, entityProviders),
		w.settingsRecords(r.Context(), snap, entityBudgets)
	rw.Header().Set("ETag", generationETag(snap.Generation))
	writeJSON(rw, http.StatusOK, o)
}

func generationETag(gen int64) string { return `"settings-` + strconv.FormatInt(gen, 10) + `"` }

func (w *Web) apiSettingsList(entity string) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if w.refuseUnavailable(rw) {
			return
		}
		snap, ok := w.settingsHead(rw, r)
		if !ok {
			return
		}
		rw.Header().Set("ETag", generationETag(snap.Generation))
		writeJSON(rw, http.StatusOK, map[string]any{"source": "db", "generation": snap.Generation, entity: w.settingsRecords(r.Context(), snap, entity)})
	}
}

func (w *Web) apiSettingsGet(entity string) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok {
			// YAML records have no settings IDs; the list shows them by key.
			notFound(rw)
			return
		}
		snap, ok := w.settingsHead(rw, r)
		if !ok {
			return
		}
		for _, rec := range w.settingsRecords(r.Context(), snap, entity) {
			if rec.ID == id {
				rw.Header().Set("ETag", generationETag(snap.Generation))
				writeJSON(rw, http.StatusOK, map[string]any{"generation": snap.Generation, "record": rec})
				return
			}
		}
		notFound(rw)
	}
}

func (w *Web) apiSettingsSchema(rw http.ResponseWriter, r *http.Request) {
	if w.settings != nil {
		if _, ok := w.settingsHead(rw, r); !ok {
			return
		}
	}
	writeJSON(rw, http.StatusOK, staticSettingsSchema())
}

func (w *Web) apiSettingsExport(rw http.ResponseWriter, r *http.Request) {
	if w.refuseUnavailable(rw) {
		return
	}
	snap, ok := w.settingsHead(rw, r)
	if !ok {
		return
	}
	b, err := snap.Export()
	if err != nil {
		w.internal(rw, errors.New("settings export failed"))
		return
	}
	rw.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	rw.Header().Set("ETag", generationETag(snap.Generation))
	rw.Write(b)
}

// SettingsAuditPage is GET /settings/audit: redacted records oldest first after an audit ID, or with order=desc
// newest first before one.
type SettingsAuditPage struct {
	Records []settings.Audit `json:"records"`
	// Next is the after value (with order=desc, the before value) for the following page; zero when this page is
	// the last.
	Next int64 `json:"next"`
}

func (w *Web) apiSettingsAudit(rw http.ResponseWriter, r *http.Request) {
	if w.refuseUnavailable(rw) {
		return
	}
	order := r.URL.Query().Get("order")
	if order != "" && order != "asc" && order != "desc" {
		badRequest(rw, "order: want asc or desc")
		return
	}
	cursorName, other := "after", "before"
	if order == "desc" {
		cursorName, other = "before", "after"
	}
	if r.URL.Query().Has(other) {
		badRequest(rw, other+": not with this order; "+cursorName+" pages it")
		return
	}
	cursor, err := int64Param(r, cursorName)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	limit, err := intParam(r, "limit", 100)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	limit = min(max(limit, 1), 500)
	page := w.settings.Audit
	if order == "desc" {
		page = w.settings.AuditBefore
	}
	rows, err := page(r.Context(), cursor, limit+1)
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	out := SettingsAuditPage{Records: rows}
	if len(rows) > limit {
		out.Records = rows[:limit]
		out.Next = rows[limit-1].ID
	}
	// The store never writes credentials into audit diffs; secret_set markers are the only credential state.
	writeJSON(rw, http.StatusOK, out)
}

func int64Param(r *http.Request, name string) (int64, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New(name + " must be a non-negative integer")
	}
	return n, nil
}

// settingsHead loads the stored head generation or answers the error.
func (w *Web) settingsHead(rw http.ResponseWriter, r *http.Request) (*settings.Snapshot, bool) {
	if w.refuseUnavailable(rw) {
		return nil, false
	}
	snap, err := w.settings.Snapshot(r.Context())
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return nil, false
	}
	return snap, true
}

func (w *Web) settingsRecords(ctx context.Context, snap *settings.Snapshot, entity string) []SettingsRecord {
	out := []SettingsRecord{}
	switch entity {
	case entityIndexers:
		for _, rec := range snap.Records.Indexers {
			rec.SecretSet = snap.CredentialSet(entityIndexers, rec.Key, w.settingsLookup)
			out = append(out, indexerRecord(rec))
		}
		sort.SliceStable(out, func(i, j int) bool {
			if *out[i].Priority != *out[j].Priority {
				return *out[i].Priority < *out[j].Priority
			}
			return out[i].Key < out[j].Key
		})
	case entityProviders:
		for _, rec := range snap.Records.Providers {
			rec.SecretSet = snap.CredentialSet(entityProviders, rec.Key, w.settingsLookup)
			out = append(out, providerRecord(rec))
		}
		sort.SliceStable(out, func(i, j int) bool { return *out[i].Order < *out[j].Order })
	case entityBudgets:
		for _, rec := range snap.Records.Budgets {
			out = append(out, budgetRecord(rec))
		}
	}
	w.decorate(ctx, entity, out)
	return out
}

func indexerRecord(rec settings.Indexer) SettingsRecord {
	enabled, priority, set := rec.Enabled, rec.Priority, rec.SecretSet
	out := SettingsRecord{ID: rec.ID, Key: rec.Key, Kind: rec.Config.Kind, Enabled: &enabled, Priority: &priority,
		Config: configMap(rec.Config), SecretSet: &set}
	stamp(&out, rec.Record)
	return out
}

func providerRecord(rec settings.Provider) SettingsRecord {
	enabled, order, set := rec.Enabled, rec.Config.Order, rec.SecretSet
	out := SettingsRecord{ID: rec.ID, Key: rec.Key, Kind: rec.Config.Kind, Enabled: &enabled, Order: &order,
		Config: configMap(rec.Config), SecretSet: &set}
	stamp(&out, rec.Record)
	return out
}

func budgetRecord(rec settings.Budget) SettingsRecord {
	out := SettingsRecord{ID: rec.ID, Key: rec.Key, Config: configMap(rec.Config)}
	stamp(&out, rec.Record)
	return out
}

func stamp(out *SettingsRecord, rec settings.Record) {
	if !rec.CreatedAt.IsZero() {
		c, u := rec.CreatedAt, rec.UpdatedAt
		out.CreatedAt, out.UpdatedAt = &c, &u
	}
}

// configMap renders a policy struct with its YAML field names, dropping credential environment names.
func configMap(v any) map[string]any {
	out := map[string]any{}
	b, err := yaml.Marshal(v)
	if err != nil {
		return out
	}
	if err := yaml.Unmarshal(b, &out); err != nil {
		return map[string]any{}
	}
	delete(out, "api_key_env")
	return out
}

// decorate adds the last test result and the gateway's back-off, budget use and indexer requests by purpose.
func (w *Web) decorate(ctx context.Context, entity string, recs []SettingsRecord) {
	for i := range recs {
		if res, ok := w.settingsTests.last(entity, recs[i].Key); ok {
			recs[i].LastTest = &res
		}
	}
	if w.egress == nil {
		return
	}
	gs, err := w.gatewayState(ctx)
	if err != nil {
		return
	}
	class := map[string]string{entityIndexers: "indexer:", entityProviders: "provider:"}[entity]
	if entity == entityIndexers {
		requests := w.gatewaySnapshot(ctx).Requests
		for i := range recs {
			recs[i].Requests = indexerRequests(requests[recs[i].Key])
		}
	}
	for i := range recs {
		if entity == entityBudgets {
			for _, b := range gs.Budgets {
				if b.Key == recs[i].Key {
					b := b
					recs[i].Usage = &b
				}
			}
			continue
		}
		for _, b := range gs.Backoffs {
			if b.Subject == class+recs[i].Key {
				b := b
				recs[i].Backoff = &b
			}
		}
	}
}
