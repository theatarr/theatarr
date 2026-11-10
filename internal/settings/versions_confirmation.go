package settings

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// ConfirmationEffect describes the exact write or ownership change a candidate would enable.
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

// snapshotConfirmationEffects are the effects of the whole candidate: versions, roots and profiles (version and
// quality records, through the assembled policy), the providers an active version can newly use (source records and
// credentials), and a stored credential going to a new address (credentialAddressEffects).
func snapshotConfirmationEffects(ctx context.Context, q rootQueryer, previous, next *Snapshot, lookup LookupSecret) ([]ConfirmationEffect, error) {
	effects, err := snapshotWriteEffects(ctx, q, previous, next, lookup)
	if err != nil || previous == nil || next == nil {
		return effects, err
	}
	return append(effects, credentialAddressEffects(previous, next, lookup)...), nil
}

// snapshotWriteEffects are snapshotConfirmationEffects without the credential addresses: what next allows that
// previous does not. A startup fallback checks only these (FallbackEffects): the last published generation already
// sent its credentials to its own addresses.
func snapshotWriteEffects(ctx context.Context, q rootQueryer, previous, next *Snapshot, lookup LookupSecret) ([]ConfirmationEffect, error) {
	if previous == nil || next == nil {
		return nil, nil
	}
	effects, err := versionConfirmationEffects(ctx, q, previous.Policy, next.Policy)
	if err != nil {
		return nil, err
	}
	switches, err := globalSwitchEffects(ctx, q, previous, next)
	if err != nil {
		return nil, err
	}
	effects = append(effects, switches...)
	effects = append(effects, providerUsableEffects(previous, next, lookup)...)
	effects = append(effects, indexerUsableEffects(previous, next, lookup)...)
	effects = append(effects, viewExposureEffects(previous.Policy, next.Policy)...)
	effects = append(effects, viewIdentityEffects(previous, next)...)
	effects = append(effects, sectionCredentialEffects(previous, next)...)
	return append(effects, sectionEffects(previous.Policy, next.Policy)...), nil
}

// indexerUsableEffects reports each indexer an active version already lists that goes from disabled to enabled, or
// gets a key while enabled: a searcher appears (or starts answering) in the next runtime, and its results can reach
// the router. An enabled one whose source changes (indexerSourceChanges) is reported too. A previous view without
// records has no indexer state to compare.
func indexerUsableEffects(previous, next *Snapshot, lookup LookupSecret) []ConfirmationEffect {
	if previous.secrets == nil {
		return nil
	}
	state := func(s *Snapshot, key string) (enabled, keyed bool) {
		for _, record := range s.Records.Indexers {
			if record.Key == key {
				return record.Enabled, s.CredentialSet("indexers", key, lookup)
			}
		}
		return false, false
	}
	routerEnabled := next.Policy.Router != nil && next.Policy.Router.Enabled
	var effects []ConfirmationEffect
	for _, key := range sortedPolicyKeys(next.Policy.Versions) {
		before, after := previous.Policy.Versions[key], next.Policy.Versions[key]
		// Newly active versions and newly listed indexers have their own effects.
		if before == nil || before.Mode != policy.ModeActive || after.Mode != policy.ModeActive {
			continue
		}
		for _, indexer := range after.Indexers {
			if !slices.Contains(before.Indexers, indexer) {
				continue
			}
			wasOn, wasKeyed := state(previous, indexer)
			on, keyed := state(next, indexer)
			if on && (!wasOn || (keyed && !wasKeyed)) {
				effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
					"Indexer %s becomes usable (enabled%s): active library %s searches it, and its releases may be grabbed when Allow new downloads (router.enabled) is on (currently %t).",
					indexer, map[bool]string{true: " with a key", false: ""}[keyed], key, routerEnabled)})
			} else if on {
				if fields := indexerSourceChanges(previous.Policy.Indexers[indexer], next.Policy.Indexers[indexer]); len(fields) > 0 {
					effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
						"Indexer %s, searched by active library %s, changes %s: its releases, and what may be grabbed from them, change when Allow new downloads (router.enabled) is on (currently %t).",
						indexer, key, strings.Join(fields, ", "), routerEnabled)})
				}
			}
		}
	}
	return effects
}

// indexerSourceChanges lists what changes where an indexer's releases come from and how they are treated:
// its kind, url, proxy or Cardigann definition, and the providers its releases may go to (indexers.K.providers).
func indexerSourceChanges(before, after *policy.Indexer) []string {
	if before == nil || after == nil {
		return nil
	}
	var out []string
	if before.Kind != after.Kind {
		out = append(out, "kind")
	}
	if before.URL != after.URL {
		out = append(out, "url")
	}
	if sourceOrigin(before.Proxy) != sourceOrigin(after.Proxy) {
		out = append(out, "proxy")
	}
	if before.Definition != after.Definition {
		out = append(out, "definition")
	}
	if !slices.Equal(before.Providers, after.Providers) {
		out = append(out, "providers")
	}
	return out
}

// viewExposureEffects reports each version active in next that a view newly serves (its versions or one of its
// profiles' sets): arr clients of that view can then add titles to it and search for them.
func viewExposureEffects(previous, next *policy.Policy) []ConfirmationEffect {
	served := func(p *policy.Policy, view string) []string {
		v := p.Views[view]
		if v == nil {
			return nil
		}
		out := slices.Clone(v.Versions)
		for _, name := range sortedPolicyKeys(v.Profiles) {
			out = append(out, v.Profiles[name]...)
		}
		return out
	}
	var effects []ConfirmationEffect
	for _, view := range sortedPolicyKeys(next.Views) {
		before := served(previous, view)
		for _, key := range addedValues(before, served(next, view)) {
			if v := next.Versions[key]; v == nil || v.Mode != policy.ModeActive || slices.Contains(before, key) {
				continue
			}
			before = append(before, key) // once per view
			effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
				"Active library %s is exposed through view %s: its arr clients can add titles to %s and search for them.",
				key, view, key)})
		}
	}
	return effects
}

// providerUsableEffects reports each provider an active version already lists that goes from disabled or without
// credential to enabled with one: its adder appears in the next runtime. A previous view without records (a runtime
// published without its snapshot) has no provider state to compare.
func providerUsableEffects(previous, next *Snapshot, lookup LookupSecret) []ConfirmationEffect {
	if previous.secrets == nil {
		return nil
	}
	usable := func(s *Snapshot, key string) bool {
		for _, record := range s.Records.Providers {
			if record.Key == key {
				return record.Enabled && s.CredentialSet("providers", key, lookup)
			}
		}
		return false
	}
	routerEnabled := next.Policy.Router != nil && next.Policy.Router.Enabled
	var effects []ConfirmationEffect
	for _, key := range sortedPolicyKeys(next.Policy.Versions) {
		before, after := previous.Policy.Versions[key], next.Policy.Versions[key]
		// Newly active versions and newly listed providers have their own effects above.
		if before == nil || before.Mode != policy.ModeActive || after.Mode != policy.ModeActive {
			continue
		}
		for _, provider := range after.Providers {
			if slices.Contains(before.Providers, provider) && !usable(previous, provider) && usable(next, provider) {
				effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
					"Provider %s becomes usable (enabled with a credential): active library %s may add torrents to it when Allow new downloads (router.enabled) is on (currently %t).",
					provider, key, routerEnabled)})
			}
		}
	}
	return effects
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
		libraryEnabled := next.Library != nil && next.Library.Enabled
		routerEnabled := next.Router != nil && next.Router.Enabled
		if before == nil && after.Mode == policy.ModeActive {
			effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
				"Library %s becomes active and starts searching. Links may be written under %s when Write library links (library.enabled) is on (currently %t). Torrent adds may go to providers %s when Allow new downloads (router.enabled) is on (currently %t).",
				key, root, libraryEnabled, providerList(after.Providers), routerEnabled)})
		}
		if after.Mode == policy.ModeActive && after.Follows != "" && (before == nil || before.Follows != after.Follows || before.Mode != policy.ModeActive) {
			// Starting to follow (or activating a follower) inherits every title and monitoring choice of the followed
			// version at once: a mass-search trigger.
			effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
				"Active library %s starts following %s: it inherits every arr title and monitoring choice of %s at once and searches for all of them; adds may go to providers %s when Allow new downloads (router.enabled) is on (currently %t) and links under %s when Write library links (library.enabled) is on (currently %t).",
				key, after.Follows, after.Follows, providerList(after.Providers), routerEnabled, root, libraryEnabled)})
		}
		if after.Mode == policy.ModeActive && after.Grab && (before == nil || before.Mode != policy.ModeActive || !before.Grab) {
			// The per-version grab switch: turning it on for an active version, or activating a version
			// with it on, lets the router add torrents for it. Turning it off needs no confirmation.
			effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
				"Downloads are on for Live library %s: torrent adds go to providers %s when Allow new downloads (router.enabled) is on (currently %t).",
				key, providerList(after.Providers), routerEnabled)})
		}
		if before == nil {
			continue
		}
		if before.Mode != policy.ModeActive && after.Mode == policy.ModeActive {
			effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
				"Library %s becomes active and starts searching. Links may be written under %s when Write library links (library.enabled) is on (currently %t). Torrent adds may go to providers %s when Allow new downloads (router.enabled) is on (currently %t).",
				key, root, libraryEnabled, providerList(after.Providers), routerEnabled)})
		}
		rootsChanged := versionRootsChanged(previous, next, key)
		if before.Mode == policy.ModeActive && rootsChanged {
			effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
				"Active library %s changes its library root from %s to %s. Mode becomes %s. New links may use the new root when the library is active and Write library links (library.enabled) is on (currently %t).",
				key, strings.Join(configuredVersionRoots(previous, key), ", "), root, after.Mode, libraryEnabled)})
		}
		if before.Mode == policy.ModeActive || after.Mode == policy.ModeActive {
			added := addedValues(before.Providers, after.Providers)
			if len(added) > 0 {
				effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
					"Library %s (mode %s) may add torrents to providers %s when its mode is active and Allow new downloads (router.enabled) is on (currently %t).",
					key, after.Mode, strings.Join(added, ", "), routerEnabled)})
			}
		}
		if after.Mode == policy.ModeActive && (before.PreferSeaDex != after.PreferSeaDex || before.Mode != policy.ModeActive && after.PreferSeaDex) {
			effect := fmt.Sprintf("Active library %s enables Prefer SeaDex: curated releases may replace existing higher-resolution files within allowed qualities and size limits.", key)
			if !after.PreferSeaDex {
				effect = fmt.Sprintf("Active library %s disables Prefer SeaDex: ordinary profile quality, revision and score ordering resumes.", key)
			}
			effects = append(effects, ConfirmationEffect{Version: key, Effect: effect})
		}
		if before.Mode == policy.ModeActive && after.Mode == policy.ModeActive {
			// Changes to where and how an active version writes, beyond the root and providers above.
			for _, c := range []struct{ field, from, to string }{
				{"storage", string(before.Storage), string(after.Storage)},
				{"client", before.Client, after.Client},
				{"import", before.Import, after.Import},
			} {
				if c.from != c.to {
					effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
						"Active library %s changes %s from %q to %q: new downloads and imports use it when Allow new downloads (router.enabled) (currently %t) and Write library links (library.enabled) (currently %t) are on.",
						key, c.field, c.from, c.to, routerEnabled, libraryEnabled)})
				}
			}
			if upgradeRank(after) > upgradeRank(before) {
				effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
					"Active library %s changes upgrades from %q to %q: Theatarr may replace existing links with better releases%s.",
					key, before.UpgradeRule(), after.UpgradeRule(), map[bool]string{true: " without approval", false: " after approval"}[after.UpgradeRule() == policy.UpgradesAuto])})
			}
			if before.Profile != after.Profile {
				effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
					"Active library %s changes profile from %q to %q: searches, grabs and upgrades follow the new profile's qualities, cutoff and scores when Allow new downloads (router.enabled) is on (currently %t).",
					key, before.Profile, after.Profile, routerEnabled)})
			} else {
				effects = append(effects, profileWideningEffects(key, after, previous.Profiles[after.Profile], next.Profiles[after.Profile])...)
				effects = append(effects, profileAdmissionEffects(key, after, previous.Profiles[after.Profile], next.Profiles[after.Profile])...)
				effects = append(effects, profileOtherEffects(key, after, previous.Profiles[after.Profile], next.Profiles[after.Profile])...)
				if !definitionSync(ctx) {
					effects = append(effects, formatSpecEffects(key, after, previous, next)...)
				}
			}
			effects = append(effects, versionOtherEffects(key, before, after, routerEnabled, libraryEnabled)...)
			if added, removed := addedValues(before.Indexers, after.Indexers), addedValues(after.Indexers, before.Indexers); len(added)+len(removed) > 0 {
				effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
					"Active library %s changes the indexers it searches (added: %s; removed: %s): grabs may come from different releases when Allow new downloads (router.enabled) is on (currently %t).",
					key, noneIfEmpty(added), noneIfEmpty(removed), routerEnabled)})
			}
			if before.Enable == "request" && after.Enable != "request" {
				effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
					"Active library %s changes enable from \"request\" to %q: every matching title gets this library, not only requested ones.",
					key, after.Enable)})
			}
		}
		if before.Follows != "" && before.Follows != after.Follows {
			effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
				"Library %s stops following %s and no longer inherits its arr titles or monitoring. Mode %s: library root %s; providers %s may receive adds when Allow new downloads (router.enabled) is on (currently %t). No arr records or files are deleted.",
				key, before.Follows, after.Mode, root, providerList(after.Providers), routerEnabled)})
		}
	}
	for _, key := range sortedPolicyKeys(previous.Versions) {
		if next.Versions[key] != nil {
			continue
		}
		before := previous.Versions[key]
		if before.Follows != "" {
			effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
				"Removing library %s also removes its follows relationship to %s; no arr records or files are deleted.", key, before.Follows)})
		}
		links, err := versionLinks(ctx, q, key)
		if err != nil {
			return nil, err
		}
		if links > 0 {
			effects = append(effects, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
				"Library %s stops being managed. %d linked item(s), link rows and trash entries remain where they are; nothing is removed.", key, links)})
		}
	}
	return effects, nil
}

// profileWideningEffects lists what lets the active version key replace more existing files through its unchanged
// profile name: upgrades turned on, a better cutoff or a higher cutoff_score. A version whose own upgrades are off
// upgrades nothing, so only turning its profile's upgrades on is not reported for it either.
func profileWideningEffects(key string, version *policy.VersionPolicy, before, after *policy.Profile) []ConfirmationEffect {
	if before == nil || after == nil || version.UpgradeRule() == policy.UpgradesOff {
		return nil
	}
	upgrades := func(p *policy.Profile) bool { return p.Upgrades == nil || *p.Upgrades }
	var out []ConfirmationEffect
	if !upgrades(before) && upgrades(after) {
		out = append(out, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
			"Profile %s, used by active library %s, turns upgrades on: Theatarr may replace existing links with better releases up to the cutoff %q and cutoff_score %d.",
			version.Profile, key, after.Cutoff, after.CutoffScore)})
	}
	if !upgrades(after) || !upgrades(before) {
		return out
	}
	if before.Cutoff != after.Cutoff && cutoffRaised(after, before.Cutoff, after.Cutoff) {
		out = append(out, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
			"Profile %s, used by active library %s, raises its cutoff from %q to %q: Theatarr may upgrade existing files until they reach it.",
			version.Profile, key, before.Cutoff, after.Cutoff)})
	}
	if after.CutoffScore > before.CutoffScore {
		out = append(out, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
			"Profile %s, used by active library %s, raises cutoff_score from %d to %d: Theatarr may upgrade existing files until their format score reaches it.",
			version.Profile, key, before.CutoffScore, after.CutoffScore)})
	}
	// A lower (or removed positive) format score lowers the score of the existing files that have the format: they
	// may fall below cutoff_score and become upgrade targets. Raised scores are admission effects.
	var lowered []string
	for _, name := range sortedPolicyKeys(before.Formats) {
		if after.Formats[name] < before.Formats[name] {
			lowered = append(lowered, name)
		}
	}
	if len(lowered) > 0 {
		out = append(out, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
			"Profile %s, used by active library %s, lowers or removes format scores (%s): existing files with them may fall below cutoff_score %d and be upgraded.",
			version.Profile, key, strings.Join(lowered, ", "), after.CutoffScore)})
	}
	// Without an unchanged quality_order a removed quality ranks below every allowed one: files at it become
	// upgradable to the qualities that ranked below it. Removing only the lowest items widens nothing.
	orderKept := len(after.QualityOrder) > 0 && reflect.DeepEqual(before.QualityOrder, after.QualityOrder)
	if !orderKept && qualitiesRemovedOnly(before.Qualities, after.Qualities) {
		if removed := qualitiesRemovedAboveKept(before.Qualities, after.Qualities); len(removed) > 0 {
			out = append(out, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
				"Profile %s, used by active library %s, removes qualities ranked above ones it keeps (%s): with no quality_order, files at them rank below every allowed quality and may be upgraded.",
				version.Profile, key, strings.Join(removed, ", "))})
		}
		if dropped := groupMembersDropped(before.Qualities, after.Qualities); len(dropped) > 0 {
			out = append(out, ConfirmationEffect{Version: key, Effect: fmt.Sprintf(
				"Profile %s, used by active library %s, removes quality group members (%s) while keeping the rest of their group: with no quality_order, files at them rank below the kept members and may be upgraded.",
				version.Profile, key, strings.Join(dropped, ", "))})
		}
	}
	return out
}

// groupMembersDropped lists the members a group of before loses while the group stays in after with other members:
// files at a dropped member become upgradable to the kept ones, even in the lowest group.
func groupMembersDropped(before, after []policy.QualityItem) []string {
	kept := map[string]policy.QualityItem{}
	for _, item := range after {
		kept[item.Name] = item
	}
	var out []string
	for _, item := range before {
		now, ok := kept[item.Name]
		if !ok || len(now.Qualities) == 0 {
			continue
		}
		for _, member := range item.Qualities {
			if !slices.Contains(now.Qualities, member) {
				out = append(out, member)
			}
		}
	}
	return out
}

// qualitiesRemovedAboveKept lists the items of before that after drops while it keeps an item ranked below them.
// A member dropped from a group that stays is groupMembersDropped's.
func qualitiesRemovedAboveKept(before, after []policy.QualityItem) []string {
	kept := map[string]bool{}
	for _, item := range after {
		kept[item.Name] = true
	}
	var out []string
	keptBelow := false
	for i := len(before) - 1; i >= 0; i-- {
		if kept[before[i].Name] {
			keptBelow = true
		} else if keptBelow {
			out = append(out, before[i].Name)
		}
	}
	slices.Reverse(out)
	return out
}

// profileAdmissionEffects lists what lets the active version key accept releases its unchanged profile name rejected
// before, on a first grab as on an upgrade: a lower min_score, another allowed quality, a higher format score (a
// format added with a positive score or a negative one removed counts), another format set, a wider or removed size
// limit (a new one replaces the quality's default and may be wider), another language (except to a specific one
// from any), or another arr profile source.
func profileAdmissionEffects(key string, version *policy.VersionPolicy, before, after *policy.Profile) []ConfirmationEffect {
	if before == nil || after == nil {
		return nil
	}
	var out []ConfirmationEffect
	add := func(format string, args ...any) {
		out = append(out, ConfirmationEffect{Version: key, Effect: fmt.Sprintf("Profile %s, used by active library %s, ", version.Profile, key) +
			fmt.Sprintf(format, args...) + ": releases it rejected before may be grabbed."})
	}
	if after.MinScore < before.MinScore {
		add("lowers min_score from %d to %d", before.MinScore, after.MinScore)
	}
	if more := addedValues(allowedQualities(before), allowedQualities(after)); len(more) > 0 {
		add("allows more qualities (%s)", strings.Join(more, ", "))
	}
	var raised []string
	for _, name := range sortedPolicyKeys(after.Formats) {
		if after.Formats[name] > before.Formats[name] {
			raised = append(raised, name)
		}
	}
	for _, name := range sortedPolicyKeys(before.Formats) {
		if _, kept := after.Formats[name]; !kept && before.Formats[name] < 0 {
			raised = append(raised, name)
		}
	}
	if len(raised) > 0 {
		add("raises format scores (%s)", strings.Join(raised, ", "))
	}
	if before.FormatSet != after.FormatSet {
		add("changes format_set from %q to %q", before.FormatSet, after.FormatSet)
	}
	var wider []string
	for _, name := range sortedPolicyKeys(after.Sizes) {
		old, had := before.Sizes[name]
		next := after.Sizes[name]
		if !had || next.Min < old.Min || (old.Max > 0 && (next.Max == 0 || next.Max > old.Max)) {
			wider = append(wider, name)
		}
	}
	for _, name := range sortedPolicyKeys(before.Sizes) {
		if _, kept := after.Sizes[name]; !kept {
			wider = append(wider, name)
		}
	}
	if len(wider) > 0 {
		add("widens size limits (%s)", strings.Join(wider, ", "))
	}
	lang := func(p *policy.Profile) string {
		if p.Language == "" {
			return "any"
		}
		return strings.ToLower(p.Language)
	}
	if lang(before) != lang(after) && lang(before) != "any" {
		add("changes language from %q to %q", lang(before), lang(after))
	}
	if before.FromArr != after.FromArr {
		add("takes the whole profile from %q instead of %q", after.FromArr, before.FromArr)
	}
	return out
}

// Deny by default: every change to an active version, to its unchanged profile or to a format that profile scores
// needs confirmation. The effects above describe the changes they judge field by field; versionOtherEffects,
// profileOtherEffects and formatSpecEffects report every other change, except these narrowing ones:
//   - version anime from unset (any title) to true or false;
//   - version packs to all (the narrowest rule), or a higher packs_threshold;
//   - version golive_done, which only records a cutover step checked outside Theatarr;
//   - profile qualities only removed, the rest kept in order (a removal that widens upgrades is a widening effect);
//   - profile min_upgrade_step raised (unset is 1);
//   - profile propers to do_not_upgrade.

// versionJudged are the version fields versionConfirmationEffects judges one by one (mode stays active here; grab
// needs confirmation when turned on, never when turned off).
var versionJudged = []string{"mode", "grab", "library", "storage", "indexers", "providers", "client", "import", "upgrades", "profile", "enable", "follows", "prefer_seadex"}

// profileJudged are the profile fields profileWideningEffects and profileAdmissionEffects judge one by one;
// qualities only for additions (profileOtherEffects reports a reorder).
var profileJudged = []string{"upgrades", "cutoff", "cutoff_score", "min_score", "formats", "format_set", "sizes", "language", "from_arr"}

// versionOtherEffects reports the fields of the active version key that change outside the judged ones and the
// narrowing list, for example kind, anime widened, naming or packs.
func versionOtherEffects(key string, before, after *policy.VersionPolicy, routerEnabled, libraryEnabled bool) []ConfirmationEffect {
	var fields []string
	for _, field := range changedFields(*before, *after, versionJudged) {
		switch {
		case field == "golive_done":
		case field == "name":
		case field == "anime" && before.Anime == nil:
		case field == "packs" && (after.Packs == "" || after.Packs == "all"):
		case field == "packs_threshold" && (after.PacksThreshold == nil || (before.PacksThreshold != nil && *after.PacksThreshold > *before.PacksThreshold)):
		default:
			fields = append(fields, field)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return []ConfirmationEffect{{Version: key, Effect: fmt.Sprintf(
		"Active library %s changes %s: the titles it takes, the releases it grabs or the links it writes may change when Allow new downloads (router.enabled) (currently %t) and Write library links (library.enabled) (currently %t) are on.",
		key, strings.Join(fields, ", "), routerEnabled, libraryEnabled)}}
}

// profileOtherEffects reports the fields of the active version key's unchanged profile that change outside the
// judged ones and the narrowing list, for example a quality reorder, min_upgrade_step lowered or propers.
func profileOtherEffects(key string, version *policy.VersionPolicy, before, after *policy.Profile) []ConfirmationEffect {
	if before == nil || after == nil {
		return nil
	}
	step := func(p *policy.Profile) int {
		if p.MinUpgradeStep == nil {
			return 1
		}
		return *p.MinUpgradeStep
	}
	var fields []string
	for _, field := range changedFields(*before, *after, profileJudged) {
		switch {
		case field == "qualities" && (len(addedValues(allowedQualities(before), allowedQualities(after))) > 0 || qualitiesRemovedOnly(before.Qualities, after.Qualities)):
			// Additions have their own effect above.
		case field == "min_upgrade_step" && step(after) > step(before):
		case field == "propers" && after.Propers == "do_not_upgrade":
		default:
			fields = append(fields, field)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return []ConfirmationEffect{{Version: key, Effect: fmt.Sprintf(
		"Profile %s, used by active library %s, changes %s: releases it rejected before may be grabbed, or existing files upgraded.",
		version.Profile, key, strings.Join(fields, ", "))}}
}

// formatSpecEffects reports each format the active version key's unchanged profile scores (not zero) whose
// definition changes: the releases it matches, and so what is grabbed, change. A quality-sync review apply
// (definitionSync) skips this: definitions from the source apply as Recyclarr's did.
func formatSpecEffects(key string, version *policy.VersionPolicy, previous, next *policy.Policy) []ConfirmationEffect {
	before, after := previous.Profiles[version.Profile], next.Profiles[version.Profile]
	if before == nil || after == nil || before.FormatSet != after.FormatSet {
		return nil
	}
	var changed []string
	for _, name := range sortedPolicyKeys(after.Formats) {
		if after.Formats[name] == 0 {
			continue
		}
		_, old := previous.ProfileFormat(before, name)
		_, now := next.ProfileFormat(after, name)
		if old != nil && now != nil && !reflect.DeepEqual(formatDefinition(old), formatDefinition(now)) {
			changed = append(changed, name)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	return []ConfirmationEffect{{Version: key, Effect: fmt.Sprintf(
		"Formats %s, scored by profile %s of active library %s, change their definition: the releases they match change, and with them what may be grabbed.",
		strings.Join(changed, ", "), version.Profile, key)}}
}

// formatDefinition is what decides the releases a format matches: its source and specification, compacted.
func formatDefinition(f *policy.FormatRef) policy.FormatRef {
	c := *f
	c.IncludeWhenRenaming = nil
	if len(c.Specification) > 0 {
		var out bytes.Buffer
		if json.Compact(&out, c.Specification) == nil {
			c.Specification = out.Bytes()
		}
	}
	return c
}

// changedFields lists the yaml names of the fields of two structs of one type that differ, except skip.
func changedFields(before, after any, skip []string) []string {
	b, a := reflect.ValueOf(before), reflect.ValueOf(after)
	var out []string
	for i := 0; i < b.NumField(); i++ {
		name, _, _ := strings.Cut(b.Type().Field(i).Tag.Get("yaml"), ",")
		if name == "" || name == "-" || slices.Contains(skip, name) {
			continue
		}
		if !reflect.DeepEqual(b.Field(i).Interface(), a.Field(i).Interface()) {
			out = append(out, name)
		}
	}
	return out
}

// qualitiesRemovedOnly reports whether after is before with some items or group members removed, the rest in order.
func qualitiesRemovedOnly(before, after []policy.QualityItem) bool {
	i := 0
	for _, item := range after {
		for i < len(before) && before[i].Name != item.Name {
			i++
		}
		if i == len(before) || before[i].Group != item.Group || len(addedValues(before[i].Qualities, item.Qualities)) > 0 {
			return false
		}
		i++
	}
	return true
}

// allowedQualities is every quality name p allows, groups expanded.
func allowedQualities(p *policy.Profile) []string {
	var out []string
	for _, item := range p.Qualities {
		if item.Group {
			out = append(out, item.Qualities...)
		} else {
			out = append(out, item.Name)
		}
	}
	return out
}

// cutoffRaised reports whether to is a better quality than from in p's best-first list; a previous cutoff the list
// no longer has counts as raised.
func cutoffRaised(p *policy.Profile, from, to string) bool {
	rank := func(name string) int {
		for i, item := range p.Qualities {
			if item.Name == name {
				return i
			}
		}
		return -1
	}
	old, next := rank(from), rank(to)
	return old < 0 || (next >= 0 && next < old)
}

func noneIfEmpty(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func providerList(providers []string) string {
	if len(providers) == 0 {
		return "no providers"
	}
	return strings.Join(providers, ", ")
}

func addedValues(before, after []string) []string {
	var out []string
	for _, value := range after {
		if !slices.Contains(before, value) {
			out = append(out, value)
		}
	}
	return out
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

// upgradeRank orders the upgrade rule by how much Theatarr may replace on its own: off, approve, auto (also when unset).
func upgradeRank(v *policy.VersionPolicy) int {
	switch v.UpgradeRule() {
	case policy.UpgradesOff:
		return 0
	case policy.UpgradesApprove:
		return 1
	}
	return 2
}
