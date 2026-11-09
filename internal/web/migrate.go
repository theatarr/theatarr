package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/indexer/cardigann"
	"github.com/theatarr/theatarr/internal/migration"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

// Migration is what the /migrate routes need from serve besides the settings coordinator (docs/migration.md).
type Migration struct {
	// Checkpoint takes the rollback checkpoint apply records before its first change (a backup set and a copy of
	// the settings). Required.
	Checkpoint migration.Checkpointer
	// Verifier runs verify's read-only checks; its Policy defaults to the settings head.
	Verifier migration.Verifier
	// Dial opens the GET-only clients inspect uses; nil is migration.DefaultDialer.
	Dial migration.Dialer
}

// MigrateInspectRequest is POST /migrate/inspect. Sources name their keys; Keys holds the values for this request
// only: they are never stored, logged or returned.
type MigrateInspectRequest struct {
	Sources migration.Sources `json:"sources"`
	Keys    map[string]string `json:"keys"`
	// Options previews a plan while the profile bodies are available. SavePlan also saves it for apply/resume.
	Options  *migration.PlanOptions `json:"options,omitempty"`
	SavePlan bool                   `json:"save_plan,omitempty"`
}

// MigrateInspectResult adds an optional plan preview to the redacted inventory.
type MigrateInspectResult struct {
	*migration.Inventory
	Plan *migration.Plan `json:"plan,omitempty"`
}

// MigratePlanRequest is POST /migrate/plans: either sources and keys (the service inspects them), or an inventory
// a client inspected itself (theatarr migrate plan does).
type MigratePlanRequest struct {
	Sources   *migration.Sources    `json:"sources,omitempty"`
	Keys      map[string]string     `json:"keys,omitempty"`
	Inventory *migration.Inventory  `json:"inventory,omitempty"`
	Options   migration.PlanOptions `json:"options"`
}

// MigratePlan is a saved plan and the inventory it was built from.
type MigratePlan struct {
	Plan      *migration.Plan      `json:"plan"`
	Inventory *migration.Inventory `json:"inventory"`
}

// MigrateApplyRequest is POST /migrate/plans/{id}/apply. Keys resolves the key names the plan's records take
// (Payload.Secret); like inspect's, they are used for this request only.
type MigrateApplyRequest struct {
	Steps              []string          `json:"steps,omitempty"`
	Confirm            []string          `json:"confirm,omitempty"`
	Keys               map[string]string `json:"keys,omitempty"`
	ConfirmationTokens map[string]string `json:"confirmation_tokens,omitempty"`
	// ConfirmationToken confirms the one step in Confirm with the token the UI showed; it becomes ConfirmationTokens.
	ConfirmationToken string `json:"confirmation_token,omitempty"`
}

// maxMigrateBody bounds a plan request: an inventory carries the arrs' profile and format bodies.
const maxMigrateBody = 32 << 20

func (w *Web) mountMigrateAPI(mux *http.ServeMux) {
	b := APIBase + "/migrate"
	h := func(pattern string, fn http.HandlerFunc) { w.handle(mux, pattern, SignedIn, auth.PermAdmin, fn) }
	h("POST "+b+"/inspect", w.apiMigrateInspect)
	h("GET "+b+"/plans", w.apiMigratePlans)
	h("POST "+b+"/plans", w.apiMigrateCreatePlan)
	h("GET "+b+"/plans/{id}", w.apiMigrateGetPlan)
	h("POST "+b+"/plans/{id}/apply", w.apiMigrateApply)
	h("POST "+b+"/plans/{id}/verify", w.apiMigrateVerify)
}

func (w *Web) migrateReady(rw http.ResponseWriter) bool {
	if w.settings == nil || w.migration == nil || w.migration.Checkpoint == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "migration needs the settings database (theatarr serve with settings)"})
		return false
	}
	return true
}

func decodeMigrateJSON(rw http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(rw, r.Body, maxMigrateBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(rw, http.StatusRequestEntityTooLarge, apiError{Error: "request over 32 MiB"})
			return false
		}
		badRequest(rw, "invalid JSON: "+err.Error())
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		badRequest(rw, "invalid JSON: trailing data")
		return false
	}
	return true
}

func (w *Web) migrateDialer() migration.Dialer {
	if w.migration.Dial != nil {
		return w.migration.Dial
	}
	return migration.DefaultDialer
}

// apiMigrateInspect reads the old services with GET only and answers the inventory, without the raw arr bodies.
func (w *Web) apiMigrateInspect(rw http.ResponseWriter, r *http.Request) {
	if !w.migrateReady(rw) {
		return
	}
	var req MigrateInspectRequest
	if !decodeMigrateJSON(rw, r, &req) {
		return
	}
	if req.SavePlan && req.Options == nil {
		badRequest(rw, "save_plan needs options")
		return
	}
	inv, err := migration.Inspect(r.Context(), req.Sources, migration.MapKeys(req.Keys), w.migrateDialer(), w.now())
	if err != nil {
		badRequest(rw, scrubKeys(err.Error(), req.Keys))
		return
	}
	result := MigrateInspectResult{Inventory: inv.WithoutRaw()}
	if req.Options != nil {
		head, err := w.settings.Snapshot(r.Context())
		if err != nil {
			w.internal(rw, err)
			return
		}
		var lookup func(string) (cardigann.Summary, bool)
		if w.cardigann != nil {
			lookup = w.cardigann.Summary
		}
		result.Plan, err = migration.Build(migration.BuildInput{Inventory: inv, Options: *req.Options,
			Policy: head.Policy, Lookup: lookup, Now: w.now()})
		if err != nil {
			badRequest(rw, scrubKeys(err.Error(), req.Keys))
			return
		}
		if req.SavePlan {
			if err := migration.Save(r.Context(), w.st, result.Plan, inv); err != nil {
				w.internal(rw, err)
				return
			}
			w.log.Info("migration plan saved", "plan", result.Plan.ID, "steps", len(result.Plan.Steps),
				"gaps", len(result.Plan.Gaps), "actor", settingsActor(r))
		}
	}
	writeJSON(rw, http.StatusOK, result)
}

func (w *Web) apiMigratePlans(rw http.ResponseWriter, r *http.Request) {
	if !w.migrateReady(rw) {
		return
	}
	list, err := migration.List(r.Context(), w.st.Reader())
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, list)
}

// apiMigrateCreatePlan builds a plan against the settings head and saves it.
func (w *Web) apiMigrateCreatePlan(rw http.ResponseWriter, r *http.Request) {
	if !w.migrateReady(rw) {
		return
	}
	var req MigratePlanRequest
	if !decodeMigrateJSON(rw, r, &req) {
		return
	}
	if (req.Sources == nil) == (req.Inventory == nil) {
		badRequest(rw, "give either sources (with keys) or an inventory")
		return
	}
	if req.Inventory != nil && len(req.Keys) > 0 {
		badRequest(rw, "keys go with sources only")
		return
	}
	inv := req.Inventory
	if req.Sources != nil {
		var err error
		inv, err = migration.Inspect(r.Context(), *req.Sources, migration.MapKeys(req.Keys), w.migrateDialer(), w.now())
		if err != nil {
			badRequest(rw, scrubKeys(err.Error(), req.Keys))
			return
		}
	}
	head, err := w.settings.Snapshot(r.Context())
	if err != nil {
		w.internal(rw, err)
		return
	}
	var lookup func(string) (cardigann.Summary, bool)
	if w.cardigann != nil {
		lookup = w.cardigann.Summary
	}
	p, err := migration.Build(migration.BuildInput{Inventory: inv, Options: req.Options, Policy: head.Policy,
		Lookup: lookup, Now: w.now()})
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	if err := migration.Save(r.Context(), w.st, p, inv); err != nil {
		w.internal(rw, err)
		return
	}
	w.log.Info("migration plan saved", "plan", p.ID, "steps", len(p.Steps), "gaps", len(p.Gaps), "actor", settingsActor(r))
	writeJSON(rw, http.StatusCreated, MigratePlan{Plan: p, Inventory: inv.WithoutRaw()})
}

func (w *Web) migratePlanID(rw http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
	}
	return id, ok
}

func (w *Web) apiMigrateGetPlan(rw http.ResponseWriter, r *http.Request) {
	if !w.migrateReady(rw) {
		return
	}
	id, ok := w.migratePlanID(rw, r)
	if !ok {
		return
	}
	p, inv, err := migration.Load(r.Context(), w.st.Reader(), id)
	if errors.Is(err, migration.ErrNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, MigratePlan{Plan: p, Inventory: inv})
}

// apiMigrateApply runs the plan's automatic steps through the settings coordinator after a checkpoint.
func (w *Web) apiMigrateApply(rw http.ResponseWriter, r *http.Request) {
	if !w.migrateReady(rw) {
		return
	}
	id, ok := w.migratePlanID(rw, r)
	if !ok {
		return
	}
	var req MigrateApplyRequest
	if !decodeMigrateJSON(rw, r, &req) {
		return
	}
	if !settings.MayConfirm(r.Context()) && (len(req.Confirm) > 0 || len(req.ConfirmationTokens) > 0 || req.ConfirmationToken != "") {
		// A caller that may not confirm (settings refuses its token anyway): refuse before a checkpoint.
		badRequest(rw, "this caller cannot confirm: a signed-in user or an API key applies this step")
		return
	}
	if req.ConfirmationToken != "" {
		// The UI's form of a confirmation: one step and the token it showed. migration.Apply checks the token
		// against the step's current effects, as it does for MCP's map.
		if len(req.Confirm) != 1 || len(req.ConfirmationTokens) > 0 {
			badRequest(rw, "confirmation_token needs exactly one step in confirm and no confirmation_tokens")
			return
		}
		req.ConfirmationTokens = map[string]string{req.Confirm[0]: req.ConfirmationToken}
		req.Confirm = nil
	}
	actor := settingsActor(r)
	p, err := migration.Apply(r.Context(), w.st, id, w.settings, w.migration.Checkpoint, migration.ApplyOptions{
		Steps: req.Steps, Confirm: req.Confirm, ConfirmationTokens: req.ConfirmationTokens,
		Keys: migration.MapKeys(req.Keys), Actor: "migrate:" + actor, Now: w.now})
	switch {
	case errors.Is(err, migration.ErrNotFound):
		notFound(rw)
		return
	case err != nil && p == nil:
		// Nothing was applied: a bad step list, or no checkpoint.
		msg := scrubKeys(err.Error(), req.Keys)
		w.log.Warn("migration apply refused", "plan", id, "err", msg, "actor", actor)
		if strings.HasPrefix(msg, "checkpoint:") {
			writeJSON(rw, http.StatusInternalServerError, apiError{Error: msg})
			return
		}
		badRequest(rw, msg)
		return
	case err != nil:
		w.internal(rw, err)
		return
	}
	counts := map[string]int{}
	for _, s := range p.Steps {
		counts[s.Status]++
	}
	w.log.Info("migration plan applied", "plan", id, "done", counts[migration.StatusDone], "failed",
		counts[migration.StatusFailed], "confirm", counts[migration.StatusConfirmation], "actor", actor)
	writeJSON(rw, http.StatusOK, p)
}

func (w *Web) apiMigrateVerify(rw http.ResponseWriter, r *http.Request) {
	if !w.migrateReady(rw) {
		return
	}
	id, ok := w.migratePlanID(rw, r)
	if !ok {
		return
	}
	v := w.migration.Verifier
	if v.Policy == nil {
		v.Policy = func(ctx context.Context) (*policy.Policy, error) {
			head, err := w.settings.Snapshot(ctx)
			if err != nil {
				return nil, err
			}
			return head.Policy, nil
		}
	}
	rep, err := migration.Verify(r.Context(), w.st, id, v, w.now())
	if errors.Is(err, migration.ErrNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, rep)
}

// scrubKeys masks every key value of a request in an error text.
func scrubKeys(msg string, keys map[string]string) string {
	for _, v := range keys {
		if len(v) >= 4 {
			msg = strings.ReplaceAll(msg, v, "***")
		}
	}
	return msg
}
