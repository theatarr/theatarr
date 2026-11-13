package settings

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// ConfirmationEffect describes a change that asks for confirmation before it is saved: going Live, Pause off,
// importing an arr into a Live library, removing a library, and an address change that keeps a stored credential.
type ConfirmationEffect struct {
	Version string `json:"version"`
	Effect  string `json:"effect"`
}

// Confirmation is bound to one candidate generation and revision.
type Confirmation struct {
	Token      string               `json:"token,omitempty"`
	Generation int64                `json:"generation"`
	Effects    []ConfirmationEffect `json:"effects"`
}

// ConfirmationError is returned when a candidate has write-enabling effects without its token.
type ConfirmationError struct {
	Effects []ConfirmationEffect
	// Confirmation is a token for exactly these effects of the refused candidate, when one could be issued: sending
	// the same request again with it confirms them.
	Confirmation *Confirmation
	// ConfirmInUI is set when the caller may not confirm (see WithoutConfirmation): no token is issued and a
	// signed-in user or a user's API key has to make the change.
	ConfirmInUI bool
}

func (e *ConfirmationError) Error() string { return "settings: explicit confirmation required" }

type confirmationToken struct {
	generation int64
	revision   string
	effects    []ConfirmationEffect
	// confirmer is the caller the token was issued to (WithConfirmer); a token is only taken by the same one.
	confirmer string
	expires   time.Time
}

// Confirm evaluates the exact candidate and returns a one-use token for its write-enabling effects.
func (r *RuntimeCoordinator) Confirm(ctx context.Context, req SaveRequest) (Confirmation, error) {
	req.Base = r.base
	candidate, err := r.store.Candidate(ctx, req)
	if err != nil {
		return Confirmation{}, err
	}
	var previous *Snapshot
	if active := r.Current(); active != nil {
		previous = active.snapshotView()
		if err := validateVersionChanges(ctx, r.store.db.Reader(), previous.Policy, candidate.Policy, false); err != nil {
			return Confirmation{}, err
		}
	}
	effects, err := r.confirmationEffects(ctx, previous, candidate)
	if err != nil {
		return Confirmation{}, err
	}
	if len(effects) > 0 && !MayConfirm(ctx) {
		return Confirmation{}, &ConfirmationError{Effects: effects, ConfirmInUI: true}
	}
	return r.issueConfirmation(ctx, candidate.Generation, candidate.Revision, effects)
}

// issueConfirmation stores a token for exactly these effects of the candidate generation and revision, bound to the
// caller (WithConfirmer): only the same signed-in user can use it, a retry after a failed commit included. No effects
// need no token.
func (r *RuntimeCoordinator) issueConfirmation(ctx context.Context, generation int64, revision string, effects []ConfirmationEffect) (Confirmation, error) {
	out := Confirmation{Generation: generation, Effects: effects}
	if len(effects) == 0 {
		return out, nil
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Confirmation{}, errors.New("settings: cannot create confirmation")
	}
	out.Token = base64.RawURLEncoding.EncodeToString(raw[:])
	r.confirmMu.Lock()
	now := time.Now()
	for key, value := range r.confirmations {
		if now.After(value.expires) {
			delete(r.confirmations, key)
		}
	}
	r.confirmations[out.Token] = confirmationToken{generation: generation, revision: revision, effects: slices.Clone(effects),
		confirmer: contextConfirmer(ctx), expires: now.Add(10 * time.Minute)}
	r.confirmMu.Unlock()
	return out, nil
}

// refuse answers an unconfirmed candidate: the effects, plus a token for them the caller may send back with the
// same request (the 422 the admin API answers carries both, so every editor can confirm without a separate call).
// A caller that may not confirm gets the effects only.
func (r *RuntimeCoordinator) refuse(ctx context.Context, generation int64, revision string, effects []ConfirmationEffect) error {
	if !MayConfirm(ctx) {
		return &ConfirmationError{Effects: effects, ConfirmInUI: true}
	}
	refused := &ConfirmationError{Effects: effects}
	if proof, err := r.issueConfirmation(ctx, generation, revision, effects); err == nil {
		refused.Confirmation = &proof
	}
	return refused
}

// ConfirmImport rereads the configured policy through the same strict import adapter used at apply time.
func (r *RuntimeCoordinator) ConfirmImport(ctx context.Context, req ImportRequest) (Confirmation, error) {
	req.Base = r.base
	write, err := r.store.importAt(ctx, req)
	if err != nil {
		return Confirmation{}, err
	}
	return r.Confirm(ctx, write)
}

func (r *RuntimeCoordinator) confirmationEffects(ctx context.Context, previous, next *Snapshot) ([]ConfirmationEffect, error) {
	effects, err := snapshotConfirmationEffects(ctx, r.store.db.Reader(), previous, next, r.store.lookup)
	if err != nil {
		return nil, err
	}
	// A startup fallback can leave the published runtime behind the stored head. Confirm both transitions; when the
	// runtime's full snapshot is the head, the second is the first.
	if previous != nil && previous.secrets != nil {
		gen, rev, err := generation(ctx, r.store.db.Reader())
		if err != nil {
			return nil, dbError(err)
		}
		if gen == previous.Generation && rev == previous.Revision {
			return effects, nil
		}
	}
	head, err := r.store.Load(ctx, r.base)
	if err != nil {
		return nil, err
	}
	stored, err := snapshotConfirmationEffects(ctx, r.store.db.Reader(), head, next, r.store.lookup)
	if err != nil {
		return nil, err
	}
	for _, effect := range stored {
		if !slices.Contains(effects, effect) {
			effects = append(effects, effect)
		}
	}
	return effects, nil
}

// snapshotConfirmationEffects are the effects of the whole candidate that ask for confirmation: what
// snapshotWriteEffects reports, and a stored credential going to a new address (credentialAddressEffects).
func snapshotConfirmationEffects(ctx context.Context, q rootQueryer, previous, next *Snapshot, lookup LookupSecret) ([]ConfirmationEffect, error) {
	effects, err := snapshotWriteEffects(ctx, q, previous, next)
	if err != nil || previous == nil || next == nil {
		return effects, err
	}
	return append(effects, credentialAddressEffects(previous, next, lookup)...), nil
}

// snapshotWriteEffects are snapshotConfirmationEffects without the credential addresses: a library going Live,
// Pause off, and removing a library (always: its rows go with it). Every other change saves at once after validation. A startup fallback checks only
// these (FallbackEffects): the last published generation already sent its credentials to its own addresses.
func snapshotWriteEffects(ctx context.Context, q rootQueryer, previous, next *Snapshot) ([]ConfirmationEffect, error) {
	if previous == nil || next == nil {
		return nil, nil
	}
	effects, err := versionConfirmationEffects(ctx, q, previous.Policy, next.Policy)
	if err != nil {
		return nil, err
	}
	return append(effects, globalSwitchEffects(previous, next)...), nil
}

func versionConfirmationEffects(ctx context.Context, q rootQueryer, previous, next *policy.Policy) ([]ConfirmationEffect, error) {
	if previous == nil || next == nil {
		return nil, nil
	}
	var effects []ConfirmationEffect
	for _, key := range sortedPolicyKeys(next.Versions) {
		before, after := previous.Versions[key], next.Versions[key]
		if after == nil {
			continue
		}
		root := strings.Join(configuredVersionRoots(next, key), ", ")
		if root == "" {
			root = "(no library root configured)"
		}
		if after.Mode != policy.ModeActive || before != nil && before.Mode == policy.ModeActive {
			continue
		}
		effect := fmt.Sprintf("Library %s becomes Live: it searches, downloads to %s and writes links under %s.",
			key, providerList(after.Providers), root)
		if next.Router == nil || !next.Router.Enabled {
			effect += " Pause is on, so nothing starts until you turn it off."
		}
		effects = append(effects, ConfirmationEffect{Version: key, Effect: effect})
	}
	for _, key := range sortedPolicyKeys(previous.Versions) {
		if next.Versions[key] != nil {
			continue
		}
		links, downloads, queued, err := libraryRowCounts(ctx, q, key)
		if err != nil {
			return nil, err
		}
		effect := fmt.Sprintf("Library %s is removed with its entries on every title (monitoring, wanted state, history).", key)
		if links > 0 {
			effect += fmt.Sprintf(" %d linked files stay on disk and are no longer tracked.", links)
		}
		if downloads > 0 {
			effect += fmt.Sprintf(" %d downloads in progress stay at the provider or client and are no longer tracked.", downloads)
		}
		if queued > 0 {
			effect += fmt.Sprintf(" %d queued downloads that were not sent yet are dropped.", queued)
		}
		effects = append(effects, ConfirmationEffect{Version: key, Effect: effect})
	}
	return effects, nil
}

func providerList(providers []string) string {
	if len(providers) == 0 {
		return "no providers"
	}
	return strings.Join(providers, ", ")
}

func (r *RuntimeCoordinator) takeConfirmation(ctx context.Context, token string, generation int64, revision string, effects []ConfirmationEffect) bool {
	_, ok := r.takeConfirmationValue(ctx, token, generation, revision, effects)
	return ok
}

// takeConfirmationValue removes the token and returns it, so a commit that fails can put it back (restoreConfirmation).
func (r *RuntimeCoordinator) takeConfirmationValue(ctx context.Context, token string, generation int64, revision string, effects []ConfirmationEffect) (confirmationToken, bool) {
	if token == "" {
		return confirmationToken{}, false
	}
	r.confirmMu.Lock()
	defer r.confirmMu.Unlock()
	value, ok := r.confirmations[token]
	if !ok || value.confirmer != contextConfirmer(ctx) {
		// Another caller's token stays theirs: it is neither used nor used up.
		return confirmationToken{}, false
	}
	delete(r.confirmations, token)
	return value, time.Now().Before(value.expires) && value.generation == generation && value.revision == revision && slices.Equal(value.effects, effects)
}

// restoreConfirmation returns a token whose commit failed: it stays bound to the same candidate and expiry, so the
// same request can be retried without confirming again.
func (r *RuntimeCoordinator) restoreConfirmation(token string, value confirmationToken) {
	r.confirmMu.Lock()
	defer r.confirmMu.Unlock()
	if time.Now().Before(value.expires) {
		r.confirmations[token] = value
	}
}
