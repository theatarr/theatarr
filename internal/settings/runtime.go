package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

type generationContextKey struct{}
type runtimeContextKey struct{}
type orphanContextKey struct{}
type confirmationContextKey struct{}
type definitionSyncContextKey struct{}
type confirmerContextKey struct{}

// WithGeneration pins work to one published settings generation.
func WithGeneration(ctx context.Context, generation int64) context.Context {
	if generation == 0 {
		return ctx
	}
	return context.WithValue(ctx, generationContextKey{}, generation)
}

// ContextGeneration returns the settings generation pinned to ctx, or zero when it is not pinned.
func ContextGeneration(ctx context.Context) int64 {
	if active := ContextRuntime(ctx); active != nil {
		return active.Generation
	}
	generation, _ := ctx.Value(generationContextKey{}).(int64)
	return generation
}

// WithRuntime pins one operation to an immutable runtime pointer.
func WithRuntime(ctx context.Context, active *Runtime) context.Context {
	if active == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, runtimeContextKey{}, active)
	return WithGeneration(ctx, active.Generation)
}

// ContextRuntime returns the runtime captured for an operation, when present.
func ContextRuntime(ctx context.Context) *Runtime {
	active, _ := ctx.Value(runtimeContextKey{}).(*Runtime)
	return active
}

// WithOrphanActiveDownloads records the explicit confirmation needed to remove a provider with active downloads.
func WithOrphanActiveDownloads(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, orphanContextKey{}, enabled)
}

// OrphanActiveDownloads reports the confirmation; a caller that may not confirm (WithoutConfirmation) never has it.
func OrphanActiveDownloads(ctx context.Context) bool {
	enabled, _ := ctx.Value(orphanContextKey{}).(bool)
	return enabled && MayConfirm(ctx)
}

type noConfirmContextKey struct{}

// WithoutConfirmation marks a request from a caller that may not confirm risky changes: one that is neither a
// signed-in user nor a user's API key (the web guard's fallback; sessions and keys confirm, docs/design/settings.md
// "Who confirms"). Its risky saves and reloads are refused with the effects but no token, and a token it sends is
// ignored.
func WithoutConfirmation(ctx context.Context) context.Context {
	return context.WithValue(ctx, noConfirmContextKey{}, true)
}

// MayConfirm reports whether the caller may confirm risky changes (see WithoutConfirmation).
func MayConfirm(ctx context.Context) bool {
	no, _ := ctx.Value(noConfirmContextKey{}).(bool)
	return !no
}

type keyConfirmerContextKey struct{}

// WithKeyConfirmer marks a request from a user's API key (docs/design/settings.md "Who confirms"): it confirms like a
// session, with its tokens bound to the key (confirmer "key:<id>"), not to the owner's sessions or other keys. label
// names the key in the audit of the saves it confirms.
func WithKeyConfirmer(ctx context.Context, id int64, label string) context.Context {
	ctx = WithConfirmer(ctx, "key:"+strconv.FormatInt(id, 10))
	return context.WithValue(ctx, keyConfirmerContextKey{}, label)
}

func keyConfirmer(ctx context.Context) (string, bool) {
	label, ok := ctx.Value(keyConfirmerContextKey{}).(string)
	return label, ok
}

// WithConfirmationToken carries a confirmation token to a write that takes no SaveRequest of its own: a reload, or a
// quality-sync review apply. A SaveRequest's own ConfirmationToken wins.
func WithConfirmationToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, confirmationContextKey{}, token)
}

// withDefinitionSync marks a quality-sync review apply: format definitions from the source apply without confirmation;
// every other effect still needs it.
func withDefinitionSync(ctx context.Context) context.Context {
	return context.WithValue(ctx, definitionSyncContextKey{}, true)
}

func definitionSync(ctx context.Context) bool {
	on, _ := ctx.Value(definitionSyncContextKey{}).(bool)
	return on
}

// WithConfirmer names the caller a confirmation token is issued to, such as the signed-in user: a token issued to one
// confirmer is refused to any other. Callers without one (the CLI, tests) share the empty name.
func WithConfirmer(ctx context.Context, confirmer string) context.Context {
	return context.WithValue(ctx, confirmerContextKey{}, confirmer)
}

// Confirmer is the caller WithConfirmer named ("user:<id>" for a session, "key:<id>" for an API key), or "".
func Confirmer(ctx context.Context) string { return contextConfirmer(ctx) }

func contextConfirmer(ctx context.Context) string {
	confirmer, _ := ctx.Value(confirmerContextKey{}).(string)
	return confirmer
}

func contextConfirmationToken(ctx context.Context) string {
	token, _ := ctx.Value(confirmationContextKey{}).(string)
	return token
}

// Runtime is one immutable settings generation and the services prepared for it.
type Runtime struct {
	Generation    int64
	Revision      string
	ProviderEpoch int64
	Policy        *policy.Policy
	Components    any
	// snapshot is the published generation's records and credentials, for confirmation effects of the next save.
	snapshot *Snapshot
}

// snapshotView is the runtime's settings snapshot; a runtime published without one shows only its policy.
func (r *Runtime) snapshotView() *Snapshot {
	if r.snapshot != nil {
		return r.snapshot
	}
	return &Snapshot{Generation: r.Generation, Revision: r.Revision, Policy: r.Policy}
}

// Prepare builds all services for a candidate before it becomes visible.
type Prepare func(context.Context, *Snapshot, *Runtime) (any, error)

// ProviderEpochPreparer is implemented by prepared components that fence provider writes with the provider epoch.
// For a candidate, the epoch is the stored epoch plus one when the provider transition from the active runtime
// changes or removes an account; the commit must leave exactly that epoch in the database.
type ProviderEpochPreparer interface {
	PreparedProviderEpoch() int64
}

// RuntimeCoordinator serializes reloads and publishes complete runtime generations.
type RuntimeCoordinator struct {
	store         *Store
	base          *policy.Policy
	prepare       Prepare
	current       atomic.Pointer[Runtime]
	reloadMu      sync.Mutex
	confirmMu     sync.Mutex
	confirmations map[string]confirmationToken
}

// NewRuntimeCoordinator installs the already-prepared startup generation.
func NewRuntimeCoordinator(st *Store, base *policy.Policy, initial *Snapshot, components any, prepare Prepare) (*RuntimeCoordinator, error) {
	if st == nil || base == nil || initial == nil || initial.Policy == nil || prepare == nil {
		return nil, errors.New("settings: runtime coordinator requires store, base, snapshot and prepare")
	}
	// The coordinator keeps its own copy: YAML-owned sections come only from it.
	owned, err := base.Clone()
	if err != nil {
		return nil, errors.New("settings: cannot copy base policy")
	}
	r := &RuntimeCoordinator{store: st, base: owned, prepare: prepare, confirmations: map[string]confirmationToken{}}
	r.publish(&Runtime{Generation: initial.Generation, Revision: initial.Revision, ProviderEpoch: initial.ProviderEpoch, Policy: initial.Policy,
		Components: components, snapshot: initial})
	return r, nil
}

func (r *RuntimeCoordinator) publish(next *Runtime) {
	if active, ok := next.Components.(interface{ Activate() }); ok {
		active.Activate()
	}
	r.current.Store(next)
	if active, ok := next.Components.(interface{ AfterPublish() }); ok {
		active.AfterPublish()
	}
}

// Current captures the active runtime. The returned value remains valid across later reloads.
func (r *RuntimeCoordinator) Current() *Runtime { return r.current.Load() }

// Capture pins a request to the current generation once. A child operation keeps an existing pin.
func (r *RuntimeCoordinator) Capture(ctx context.Context) context.Context {
	if r == nil || ContextRuntime(ctx) != nil {
		return ctx
	}
	active := r.Current()
	if active == nil {
		return ctx
	}
	return WithRuntime(ctx, active)
}

// Generation only returns the current runtime. In-flight operations keep retired runtimes through their context.
func (r *RuntimeCoordinator) Generation(generation int64) (*Runtime, bool) {
	v := r.Current()
	return v, v != nil && v.Generation == generation
}

// Reload prepares and publishes the stored head generation, for example after a startup fallback left an older
// generation active. It is the same provider transition as a save (apply): prepared against the active runtime,
// then committed with the download recheck, registry invalidation and provider epoch in one transaction, so a reload
// that would orphan active downloads is refused unless the context confirms it (WithOrphanActiveDownloads). A
// prepare or commit error leaves the active runtime untouched.
func (r *RuntimeCoordinator) Reload(ctx context.Context, generation int64) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	active := r.current.Load()
	if active != nil && active.Generation == generation {
		return r.markPublished(ctx, generation)
	}
	snap, err := r.store.Load(ctx, r.base)
	if err != nil {
		return err
	}
	if snap.Generation != generation {
		return fmt.Errorf("%w: requested %d, current %d", ErrConflict, generation, snap.Generation)
	}
	token := contextConfirmationToken(ctx)
	var taken confirmationToken
	confirmed := false
	if active != nil {
		if err := validateVersionChanges(ctx, r.store.db.Reader(), active.Policy, snap.Policy, false); err != nil {
			return err
		}
		effects, err := r.confirmationEffects(ctx, active.snapshotView(), snap)
		if err != nil {
			return err
		}
		// After a startup fallback the head may enable writes the active runtime does not: the reload needs the
		// token for exactly those effects (WithConfirmationToken), like a save.
		if len(effects) > 0 {
			if !MayConfirm(ctx) {
				return r.refuse(ctx, snap.Generation, snap.Revision, effects)
			}
			if taken, confirmed = r.takeConfirmationValue(ctx, token, snap.Generation, snap.Revision, effects); !confirmed {
				return r.refuse(ctx, snap.Generation, snap.Revision, effects)
			}
		} else if token != "" && !r.takeConfirmation(ctx, token, snap.Generation, snap.Revision, effects) {
			return errors.New("settings: confirmation does not match candidate")
		}
	}
	// The stored epoch is the one before this transition; the runtime preparing the head decides whether it advances.
	snap.epochPending = true
	_, err = r.apply(ctx, active, snap, func(ctx context.Context, hook func(context.Context, *sql.Tx) error) (*Snapshot, error) {
		saved, err := r.store.commitTransition(ctx, snap, hook)
		if err != nil && confirmed {
			r.restoreConfirmation(token, taken)
		}
		return saved, err
	})
	return err
}

// SaveAndApply prepares a candidate before committing it, then publishes the committed generation. The caller's
// Base is ignored: YAML-owned sections always come from the coordinator's retained base.
func (r *RuntimeCoordinator) SaveAndApply(ctx context.Context, req SaveRequest) (*Snapshot, error) {
	req.Base = r.base
	return r.saveAndApply(ctx, req, "save")
}

// ImportAndApply prepares, commits and publishes a YAML import through the live safety checks.
func (r *RuntimeCoordinator) ImportAndApply(ctx context.Context, req ImportRequest) (*Snapshot, error) {
	req.Base = r.base
	write, err := r.store.importAt(ctx, req)
	if err != nil {
		return nil, err
	}
	write.OrphanActiveDownloads = req.OrphanActiveDownloads
	return r.saveAndApply(ctx, write, "import")
}

func (r *RuntimeCoordinator) saveAndApply(ctx context.Context, req SaveRequest, operation string) (*Snapshot, error) {
	return r.saveAndApplyWithHook(ctx, req, operation, nil)
}

func (r *RuntimeCoordinator) saveAndApplyWithHook(ctx context.Context, req SaveRequest, operation string, extra func(context.Context, *sql.Tx) error) (*Snapshot, error) {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	if req.OrphanActiveDownloads {
		ctx = WithOrphanActiveDownloads(ctx, true)
	}
	candidate, err := r.store.Candidate(ctx, req)
	if err != nil {
		return nil, err
	}
	var previous *Snapshot
	if active := r.Current(); active != nil {
		previous = active.snapshotView()
		if err := validateVersionChanges(ctx, r.store.db.Reader(), previous.Policy, candidate.Policy, false); err != nil {
			return nil, err
		}
	}
	effects, err := r.confirmationEffects(ctx, previous, candidate)
	if err != nil {
		return nil, err
	}
	if req.ConfirmationToken == "" {
		req.ConfirmationToken = contextConfirmationToken(ctx)
	}
	var taken confirmationToken
	confirmed := false
	if len(effects) > 0 {
		if !MayConfirm(ctx) {
			return nil, r.refuse(ctx, candidate.Generation, candidate.Revision, effects)
		}
		if taken, confirmed = r.takeConfirmationValue(ctx, req.ConfirmationToken, candidate.Generation, candidate.Revision, effects); !confirmed {
			return nil, r.refuse(ctx, candidate.Generation, candidate.Revision, effects)
		}
		req.confirmedEffects = effects
		if label, key := keyConfirmer(ctx); key {
			req.Actor += ", confirmed by " + label
		}
	} else if req.ConfirmationToken != "" && !r.takeConfirmation(ctx, req.ConfirmationToken, candidate.Generation, candidate.Revision, effects) {
		return nil, errors.New("settings: confirmation does not match candidate")
	}
	return r.apply(ctx, r.Current(), candidate, func(ctx context.Context, hook func(context.Context, *sql.Tx) error) (*Snapshot, error) {
		saved, err := r.store.saveWithHook(ctx, req, operation, func(ctx context.Context, tx *sql.Tx) error {
			if err := hook(ctx, tx); err != nil {
				return err
			}
			if extra != nil {
				return extra(ctx, tx)
			}
			return nil
		})
		// A commit that failed changed nothing: the same request may retry with the same token. A failed
		// preparation (before this commit) still uses it up.
		if err != nil && confirmed {
			r.restoreConfirmation(req.ConfirmationToken, taken)
		}
		return saved, err
	})
}

// apply is the one provider transition behind save, import and reload: prepare next against the active runtime,
// commit through commit with the prepared runtime's hook (CommitSettings, then the epoch check) inside the commit's
// transaction, and publish only what was committed. The caller holds reloadMu.
func (r *RuntimeCoordinator) apply(ctx context.Context, active *Runtime, next *Snapshot,
	commit func(context.Context, func(context.Context, *sql.Tx) error) (*Snapshot, error)) (_ *Snapshot, err error) {
	// The candidate's view and section credentials are its own policy's (Policy.Getenv): preparing it changes
	// nothing the active runtime resolves.
	components, err := r.prepare(ctx, next, active)
	if err != nil {
		return nil, err
	}
	committer, _ := components.(interface {
		CommitSettings(context.Context, *sql.Tx) error
	})
	preparer, fenced := components.(ProviderEpochPreparer)
	hook := func(ctx context.Context, tx *sql.Tx) error {
		if committer != nil {
			if err := committer.CommitSettings(ctx, tx); err != nil {
				return err
			}
		}
		if !fenced {
			return nil
		}
		// The commit and the prepared router must agree on the epoch, or every add of the new runtime is refused.
		committed, err := providerEpoch(ctx, tx)
		if err != nil {
			return err
		}
		if want := preparer.PreparedProviderEpoch(); committed != want {
			return fmt.Errorf("%w: provider epoch %d committed, %d prepared", ErrConflict, committed, want)
		}
		return nil
	}
	saved, err := commit(ctx, hook)
	if err != nil {
		return nil, err
	}
	// The hook already refused a mismatch inside the transaction; never publish a router fenced on another epoch.
	if fenced && saved.ProviderEpoch != preparer.PreparedProviderEpoch() {
		return nil, fmt.Errorf("settings: committed provider epoch %d differs from prepared %d", saved.ProviderEpoch,
			preparer.PreparedProviderEpoch())
	}
	r.publish(&Runtime{Generation: saved.Generation, Revision: saved.Revision, ProviderEpoch: saved.ProviderEpoch, Policy: saved.Policy, Components: components,
		snapshot: saved})
	if err := r.markPublished(ctx, saved.Generation); err != nil {
		slog.Error("settings committed and published but publication marker failed", "generation", saved.Generation, "err", err)
	}
	return saved, nil
}

func (r *RuntimeCoordinator) markPublished(ctx context.Context, generation int64) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return r.store.MarkPublished(ctx, generation)
}

// Behind reports the active and the stored head generation when the active runtime is older than the head: a startup
// fallback (or a save whose runtime was not published) that a reload has not replaced yet. It reads one row.
func (r *RuntimeCoordinator) Behind(ctx context.Context) (active, head int64, behind bool, err error) {
	cur := r.Current()
	if cur == nil {
		return 0, 0, false, nil
	}
	head, _, err = generation(ctx, r.store.db.Reader())
	if err != nil {
		return 0, 0, false, dbError(err)
	}
	return cur.Generation, head, cur.Generation < head, nil
}

// Snapshot loads the stored head generation over the coordinator's YAML base. The admin API edits this head; the
// active runtime may be an older generation after a startup fallback (see Current).
func (r *RuntimeCoordinator) Snapshot(ctx context.Context) (*Snapshot, error) {
	return r.store.Load(ctx, r.base)
}

// Candidate validates a save without committing it, over the coordinator's YAML base. Admin tests of unsaved
// configurations use it: nothing is stored and no runtime is prepared.
func (r *RuntimeCoordinator) Candidate(ctx context.Context, req SaveRequest) (*Snapshot, error) {
	req.Base = r.base
	return r.store.Candidate(ctx, req)
}

// ValidateRoot checks a proposed path without writing to the filesystem or settings store.
func (r *RuntimeCoordinator) ValidateRoot(ctx context.Context, key, root string) (RootCheck, error) {
	return r.store.ValidateRoot(ctx, r.base, key, root)
}

// PreviewImport is Store.PreviewImport over the coordinator's YAML base.
func (r *RuntimeCoordinator) PreviewImport(ctx context.Context, req ImportRequest) (*Preview, error) {
	req.Base = r.base
	return r.store.PreviewImport(ctx, req)
}

// Audit lists redacted audit records (Store.Audit).
func (r *RuntimeCoordinator) Audit(ctx context.Context, after int64, limit int) ([]Audit, error) {
	return r.store.Audit(ctx, after, limit)
}

// AuditBefore lists redacted audit records newest first (Store.AuditBefore).
func (r *RuntimeCoordinator) AuditBefore(ctx context.Context, before int64, limit int) ([]Audit, error) {
	return r.store.AuditBefore(ctx, before, limit)
}

// RecordAction audits an outbound admin action, such as a source test, against the stored head generation.
func (r *RuntimeCoordinator) RecordAction(ctx context.Context, actor, entity, key, operation string, detail any) error {
	return r.store.RecordAction(ctx, actor, entity, key, operation, detail)
}

// ProfileDefaults reads the profiles "Add version" preselects (Store.ProfileDefaults).
func (r *RuntimeCoordinator) ProfileDefaults(ctx context.Context) (ProfileDefaults, error) {
	return r.store.ProfileDefaults(ctx)
}

// SetProfileDefaults replaces them, checked against the stored head's profiles (Store.SetProfileDefaults).
func (r *RuntimeCoordinator) SetProfileDefaults(ctx context.Context, actor string, defaults ProfileDefaults) (ProfileDefaults, error) {
	snap, err := r.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return r.store.SetProfileDefaults(ctx, snap.Policy, actor, defaults)
}
