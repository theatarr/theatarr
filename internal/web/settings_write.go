package web

// Settings record writes, generation checks and shared coordinator error handling.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

// SettingsWrite is the answer to a successful write: the published generation and the records it touched.
type SettingsWrite struct {
	Generation int64            `json:"generation"`
	Revision   string           `json:"revision"`
	Record     *SettingsRecord  `json:"record,omitempty"`
	Records    []SettingsRecord `json:"records,omitempty"`
}

// settingsBody is a create, update or candidate test. Omitted fields keep their stored values on update.
type settingsBody struct {
	Generation *int64          `json:"generation"`
	ID         int64           `json:"id"`
	Key        string          `json:"key"`
	Enabled    *bool           `json:"enabled"`
	Priority   *int            `json:"priority"`
	Config     json.RawMessage `json:"config"`
	// Secret sets the credential; omitted keeps it. ClearSecret removes it. An empty secret is refused as ambiguous.
	Secret      *string `json:"secret"`
	ClearSecret bool    `json:"clear_secret"`
	// OrphanActiveDownloads is required on provider updates and deletes: true confirms that disabling or removing
	// the provider fails its active downloads; false refuses such a save.
	OrphanActiveDownloads *bool `json:"orphan_active_downloads"`
	// ConfirmationToken confirms the write-enabling effects a previous 422 listed for this same request.
	ConfirmationToken string `json:"confirmation_token"`
}

// ---- writes ----

func (w *Web) apiSettingsCreate(entity string) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if w.refuseUnavailable(rw) {
			return
		}
		body, ok := decodeSettingsBody(rw, r)
		if !ok {
			return
		}
		if body.ID != 0 {
			badRequest(rw, "id is assigned by the server; create takes a key")
			return
		}
		if body.Key == "" || len(body.Config) == 0 {
			badRequest(rw, "key and config are required")
			return
		}
		snap, ok := w.settingsAt(rw, r, body.Generation)
		if !ok {
			return
		}
		req, scrub, err := applyBody(snap, entity, 0, body, true)
		if err != nil {
			writeBodyError(rw, err, scrub)
			return
		}
		req.ConfirmationToken = body.ConfirmationToken
		w.saveSettings(rw, r, req, scrub, entity, body.Key)
	}
}

func (w *Web) apiSettingsUpdate(entity string) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if w.refuseUnavailable(rw) {
			return
		}
		id, ok := pathID(r)
		if !ok {
			notFound(rw)
			return
		}
		body, ok := decodeSettingsBody(rw, r)
		if !ok {
			return
		}
		if body.ID != 0 && body.ID != id {
			badRequest(rw, "id does not match the path")
			return
		}
		if entity == entityProviders && body.OrphanActiveDownloads == nil {
			badRequest(rw, "orphan_active_downloads is required for provider updates: false refuses a change that would fail active downloads, true confirms it")
			return
		}
		snap, ok := w.settingsAt(rw, r, body.Generation)
		if !ok {
			return
		}
		req, scrub, err := applyBody(snap, entity, id, body, false)
		if err != nil {
			writeBodyError(rw, err, scrub)
			return
		}
		req.ConfirmationToken = body.ConfirmationToken
		w.saveSettings(rw, r, req, scrub, entity, recordKey(snap, entity, id))
	}
}

func (w *Web) apiSettingsDelete(entity string) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if w.refuseUnavailable(rw) {
			return
		}
		id, ok := pathID(r)
		if !ok {
			notFound(rw)
			return
		}
		q := r.URL.Query()
		var gen *int64
		if v := q.Get("generation"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				badRequest(rw, "generation must be an integer")
				return
			}
			gen = &n
		}
		var orphan *bool
		if v := q.Get("orphan_active_downloads"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				badRequest(rw, "orphan_active_downloads must be true or false")
				return
			}
			orphan = &b
		}
		if entity == entityProviders && orphan == nil {
			badRequest(rw, "orphan_active_downloads is required to delete a provider: false refuses when it has active downloads, true fails them")
			return
		}
		if entity != entityProviders && orphan != nil {
			badRequest(rw, "orphan_active_downloads applies to providers only")
			return
		}
		snap, ok := w.settingsAt(rw, r, gen)
		if !ok {
			return
		}
		key := recordKey(snap, entity, id)
		if key == "" {
			notFound(rw)
			return
		}
		records := cloneRecords(snap.Records)
		switch entity {
		case entityIndexers:
			records.Indexers = without(records.Indexers, func(v settings.Indexer) bool { return v.ID == id })
		case entityProviders:
			records.Providers = without(records.Providers, func(v settings.Provider) bool { return v.ID == id })
		case entityBudgets:
			records.Budgets = without(records.Budgets, func(v settings.Budget) bool { return v.ID == id })
		}
		req := settings.SaveRequest{ExpectedGeneration: snap.Generation, Records: records}
		if orphan != nil {
			req.OrphanActiveDownloads = *orphan
		}
		w.saveSettings(rw, r, req, nil, entity, "")
	}
}

func without[T any](in []T, drop func(T) bool) []T {
	out := make([]T, 0, len(in))
	for _, v := range in {
		if !drop(v) {
			out = append(out, v)
		}
	}
	return out
}

// apiSettingsPriority sets indexer priorities: {"generation": n, "priorities": [{"id": 1, "priority": 0}, ...]}.
func (w *Web) apiSettingsPriority(rw http.ResponseWriter, r *http.Request) {
	if w.refuseUnavailable(rw) {
		return
	}
	var body struct {
		Generation *int64 `json:"generation"`
		Priorities []struct {
			ID       int64 `json:"id"`
			Priority int   `json:"priority"`
		} `json:"priorities"`
		ConfirmationToken string `json:"confirmation_token"`
	}
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	if len(body.Priorities) == 0 {
		badRequest(rw, "priorities is required")
		return
	}
	snap, ok := w.settingsAt(rw, r, body.Generation)
	if !ok {
		return
	}
	records := cloneRecords(snap.Records)
	seen := map[int64]bool{}
	for _, p := range body.Priorities {
		if seen[p.ID] {
			badRequest(rw, fmt.Sprintf("indexer id %d listed twice", p.ID))
			return
		}
		seen[p.ID] = true
		if p.Priority < 0 {
			badRequest(rw, "priority must not be negative")
			return
		}
		found := false
		for i := range records.Indexers {
			if records.Indexers[i].ID == p.ID {
				records.Indexers[i].Priority, found = p.Priority, true
			}
		}
		if !found {
			badRequest(rw, fmt.Sprintf("no indexer with id %d", p.ID))
			return
		}
	}
	w.saveSettings(rw, r, settings.SaveRequest{ExpectedGeneration: snap.Generation, Records: records, ConfirmationToken: body.ConfirmationToken}, nil, entityIndexers, "")
}

// apiSettingsOrder sets the provider fallback order: {"generation": n, "order": [id, ...]} lists every provider once,
// first fallback first. Versions list their providers in fallback order in the YAML, so an order they contradict
// is refused by validation.
func (w *Web) apiSettingsOrder(rw http.ResponseWriter, r *http.Request) {
	if w.refuseUnavailable(rw) {
		return
	}
	var body struct {
		Generation        *int64  `json:"generation"`
		Order             []int64 `json:"order"`
		ConfirmationToken string  `json:"confirmation_token"`
	}
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	snap, ok := w.settingsAt(rw, r, body.Generation)
	if !ok {
		return
	}
	records := cloneRecords(snap.Records)
	if len(body.Order) != len(records.Providers) {
		badRequest(rw, fmt.Sprintf("order must list all %d providers once", len(records.Providers)))
		return
	}
	seen := map[int64]bool{}
	for rank, id := range body.Order {
		if seen[id] {
			badRequest(rw, fmt.Sprintf("provider id %d listed twice", id))
			return
		}
		seen[id] = true
		found := false
		for i := range records.Providers {
			if records.Providers[i].ID == id {
				records.Providers[i].Config.Order, found = rank+1, true
			}
		}
		if !found {
			badRequest(rw, fmt.Sprintf("no provider with id %d", id))
			return
		}
	}
	w.saveSettings(rw, r, settings.SaveRequest{ExpectedGeneration: snap.Generation, Records: records, ConfirmationToken: body.ConfirmationToken}, nil, entityProviders, "")
}

// settingsAt loads the head and checks the caller's generation (optimistic concurrency): stale answers 409.
func (w *Web) settingsAt(rw http.ResponseWriter, r *http.Request, gen *int64) (*settings.Snapshot, bool) {
	if gen == nil {
		badRequest(rw, "generation is required: the settings generation this change was made against")
		return nil, false
	}
	snap, ok := w.settingsHead(rw, r)
	if !ok {
		return nil, false
	}
	if snap.Generation != *gen || (r.Header.Get("If-Match") != "" && r.Header.Get("If-Match") != generationETag(snap.Generation)) {
		writeStale(rw, snap.Generation)
		return nil, false
	}
	return snap, true
}

func writeStale(rw http.ResponseWriter, current int64) {
	writeJSON(rw, http.StatusConflict, map[string]any{"error": "settings changed since this generation was read; reload and retry",
		"generation": current})
}

func (w *Web) saveSettings(rw http.ResponseWriter, r *http.Request, req settings.SaveRequest, scrub []string, entity, key string) {
	req.Actor = settingsActor(r)
	req.ConfirmationToken = confirmationToken(r, req.ConfirmationToken)
	saved, err := w.settings.SaveAndApply(r.Context(), req)
	if err != nil {
		w.settingsError(rw, r, err, scrub)
		return
	}
	out := SettingsWrite{Generation: saved.Generation, Revision: saved.Revision}
	recs := w.settingsRecords(r.Context(), saved, entity)
	if key != "" {
		for _, rec := range recs {
			if rec.Key == key {
				rec := rec
				out.Record = &rec
			}
		}
	} else {
		out.Records = recs
	}
	writeJSON(rw, http.StatusOK, out)
}

// settingsError maps coordinator errors to answers. Settings errors are opaque by construction (no driver text or
// credentials); validation and continuity refusals keep their paths. Anything else is logged with the request's own
// credential values removed and answered without detail.
func (w *Web) settingsError(rw http.ResponseWriter, r *http.Request, err error, scrub []string) {
	clean := func(s string) string { return scrubText(s, scrub) }
	var verr *policy.Errors
	var confirmation *settings.ConfirmationError
	var body bodyError
	switch {
	case errors.As(err, &body):
		// A refused request body (unknown field, wrong type, no such record) is the caller's mistake.
		writeBodyError(rw, err, scrub)
	case errors.As(err, &confirmation):
		// The refusal carries a token for exactly these effects: sending the same request again with it as
		// confirmation_token (or the X-Confirmation-Token header) confirms them.
		body := map[string]any{"error": "explicit confirmation required", "effects": confirmation.Effects}
		if confirmation.ConfirmInUI {
			// A caller that may not confirm gets the effects only (sessions and user API keys get a token).
			body["error"] = "explicit confirmation required: this caller cannot confirm it; a signed-in user or a user's API key must make this change"
			body["confirm_in_ui"] = true
		}
		if confirmation.Confirmation != nil {
			body["confirmation"] = confirmation.Confirmation
		}
		writeJSON(rw, http.StatusUnprocessableEntity, body)
	case errors.Is(err, settings.ErrConflict):
		current := int64(0)
		if snap, lerr := w.settings.Snapshot(r.Context()); lerr == nil {
			current = snap.Generation
		}
		writeStale(rw, current)
	case errors.Is(err, settings.ErrDownloadsChanged):
		writeJSON(rw, http.StatusConflict, apiError{Error: "downloads changed during save; retry"})
	case errors.Is(err, settings.ErrUninitialized):
		writeJSON(rw, http.StatusConflict, apiError{Error: "the settings database has no generation yet"})
	case errors.As(err, &verr) && len(verr.List) > 0:
		detail := make([]string, 0, len(verr.List))
		fields := make([]map[string]string, 0, len(verr.List))
		for _, fe := range verr.List {
			detail = append(detail, clean(fe.Error()))
			fields = append(fields, map[string]string{"path": clean(fe.Path), "message": clean(fe.Msg)})
		}
		writeJSON(rw, http.StatusUnprocessableEntity, map[string]any{"error": "settings refused: " + clean(verr.List[0].Error()), "detail": detail, "fields": fields})
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "settings request canceled or timed out; nothing was published"})
	case strings.HasPrefix(err.Error(), "settings: ") && err.Error() != "settings: operation failed":
		// The settings package's own refusals are fixed texts (missing credential, incomplete import, ...).
		writeJSON(rw, http.StatusUnprocessableEntity, apiError{Error: clean(err.Error())})
	default:
		w.log.Error("settings: apply failed", "err", clean(err.Error()))
		writeJSON(rw, http.StatusInternalServerError, apiError{Error: "settings could not be applied; the active generation is unchanged (see the log)"})
	}
}

// scrubText removes the request's credential values from a message.
func scrubText(s string, scrub []string) string {
	for _, v := range scrub {
		if v != "" {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}
