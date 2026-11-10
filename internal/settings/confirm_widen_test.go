package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// activeWebFixture publishes movie-web as active (confirmed) and returns the coordinator and that snapshot.
func activeWebFixture(t *testing.T) (*RuntimeCoordinator, *policy.Policy, *Snapshot) {
	t.Helper()
	s, _, p, _ := versionFixture(t)
	ctx := context.Background()
	// quality records too, so profile edits are record edits as in the editor.
	initial, err := s.BootstrapQuality(ctx, p, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	m := versionCoordinator(t, s, p, initial)
	req := saveRequest(p, initial)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Mode = policy.ModeActive })
	proof, err := m.Confirm(ctx, req)
	if err != nil || proof.Token == "" {
		t.Fatalf("confirm activation: %+v %v", proof, err)
	}
	req.ConfirmationToken = proof.Token
	active, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	return m, p, active
}

func requireConfirmation(t *testing.T, err error, contains string) {
	t.Helper()
	var refused *ConfirmationError
	if !errors.As(err, &refused) {
		t.Fatalf("saved without confirmation (want effect %q): %v", contains, err)
	}
	for _, effect := range refused.Effects {
		if strings.Contains(effect.Effect, contains) {
			return
		}
	}
	t.Fatalf("confirmation lacks %q: %+v", contains, refused.Effects)
}

func editProvider(records *Records, key string, change func(*Provider)) {
	for i := range records.Providers {
		if records.Providers[i].Key == key {
			change(&records.Providers[i])
		}
	}
}

func editProfile(t *testing.T, records *Records, key string, change func(*policy.Profile)) {
	t.Helper()
	for i := range records.Profiles {
		if records.Profiles[i].Key == key {
			change(&records.Profiles[i].Config)
			return
		}
	}
	t.Fatalf("no profile record %s", key)
}

// Enabling a provider an active version lists, or giving it a credential, makes the adder appear.
func TestEnableProviderOnActiveVersion(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	ctx := context.Background()
	req := saveRequest(p, active)
	editProvider(&req.Records, "rd", func(v *Provider) { v.Enabled = false })
	disabled, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatalf("disabling is narrowing: %v", err)
	}
	req = saveRequest(p, disabled)
	editProvider(&req.Records, "rd", func(v *Provider) { v.Enabled = true })
	_, err = m.SaveAndApply(ctx, req)
	requireConfirmation(t, err, "rd")

	req = saveRequest(p, disabled)
	req.SecretWrites = map[string]Secret{"providers/ad": NewSecret("")}
	keyless, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatalf("clearing a credential is narrowing: %v", err)
	}
	req = saveRequest(p, keyless)
	req.SecretWrites = map[string]Secret{"providers/ad": NewSecret("invented-new-ad-credential")}
	_, err = m.SaveAndApply(ctx, req)
	requireConfirmation(t, err, "ad")
}

// Widening the profile an active version uses (quality records) needs the same confirmation as the version level.
func TestProfileUpgradesOnActiveVersion(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	ctx := context.Background()
	profile := active.Policy.Versions["movie-web"].Profile
	req := saveRequest(p, active)
	editProfile(t, &req.Records, profile, func(c *policy.Profile) { off := false; c.Upgrades = &off })
	narrowed, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatalf("turning upgrades off is narrowing: %v", err)
	}
	req = saveRequest(p, narrowed)
	editProfile(t, &req.Records, profile, func(c *policy.Profile) { on := true; c.Upgrades = &on })
	_, err = m.SaveAndApply(ctx, req)
	requireConfirmation(t, err, "turns upgrades on")
	// The refusal carries a token for exactly these effects; the same request with it saves.
	var refused *ConfirmationError
	if !errors.As(err, &refused) || refused.Confirmation == nil || refused.Confirmation.Token == "" {
		t.Fatalf("refusal without token: %v", err)
	}
	req.ConfirmationToken = refused.Confirmation.Token
	widened, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req = saveRequest(p, widened)
	editProfile(t, &req.Records, profile, func(c *policy.Profile) { c.CutoffScore += 500 })
	_, err = m.SaveAndApply(ctx, req)
	requireConfirmation(t, err, "raises cutoff_score")
}

// At the policy level: follows added, profile and indexers changed on an active version.
func TestAddFollowsOnActiveVersion(t *testing.T) {
	t.Parallel()
	_, db, _, initial := versionFixture(t)
	ctx := context.Background()
	before, _ := initial.Policy.Clone()
	before.Versions["movie-web"].Mode = policy.ModeActive
	for _, tc := range []struct {
		name, contains string
		change         func(*policy.VersionPolicy)
	}{
		{"add-follows", "starts following movie-remux", func(v *policy.VersionPolicy) { v.Follows = "movie-remux" }},
		{"profile", "changes profile", func(v *policy.VersionPolicy) { v.Profile = "remux-2160p" }},
		{"indexers", "indexers", func(v *policy.VersionPolicy) { v.Indexers = append(v.Indexers, "seadex") }},
		{"cutoff", "raises its cutoff", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next, _ := before.Clone()
			if tc.change != nil {
				tc.change(next.Versions["movie-web"])
			} else {
				before, _ := before.Clone()
				before.Versions["movie-web"].Profile = "remux-2160p"
				before.Profiles["remux-2160p"].Cutoff = "WEB 2160p"
				next, _ = before.Clone()
				next.Profiles["remux-2160p"].Cutoff = "Remux-2160p"
				effects, err := versionConfirmationEffects(ctx, db.Reader(), before, next)
				if err != nil || len(effects) != 1 || !strings.Contains(effects[0].Effect, tc.contains) {
					t.Fatalf("cutoff: %+v %v", effects, err)
				}
				// Lowering it back is narrowing.
				if effects, _ := versionConfirmationEffects(ctx, db.Reader(), next, before); len(effects) != 0 {
					t.Fatalf("lowered cutoff confirmed: %+v", effects)
				}
				return
			}
			effects, err := versionConfirmationEffects(ctx, db.Reader(), before, next)
			if err != nil {
				t.Fatal(err)
			}
			for _, effect := range effects {
				if strings.Contains(effect.Effect, tc.contains) {
					return
				}
			}
			t.Fatalf("missing %q: %+v", tc.contains, effects)
		})
	}
}

// A failed commit leaves the confirmation usable for the same candidate.
func TestConfirmationSurvivesFailedCommit(t *testing.T) {
	t.Parallel()
	s, _, p, initial := versionFixture(t)
	m := versionCoordinator(t, s, p, initial)
	ctx := context.Background()
	req := saveRequest(p, initial)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Mode = policy.ModeActive })
	proof, err := m.Confirm(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ConfirmationToken = proof.Token
	req.Base = m.base
	if _, err := m.saveAndApplyWithHook(ctx, req, "save", func(context.Context, *sql.Tx) error {
		return errors.New("fixture commit failure")
	}); err == nil {
		t.Fatal("commit failure ignored")
	}
	if _, err := m.SaveAndApply(ctx, req); err != nil {
		t.Fatalf("token used up by a failed commit: %v", err)
	}
}

// After a startup fallback the reload of the head carries a confirmation token, as saves do.
func TestFallbackReloadTakesConfirmationToken(t *testing.T) {
	t.Parallel()
	s, _, p, initial := versionFixture(t)
	ctx := context.Background()
	first := versionCoordinator(t, s, p, initial)
	req := saveRequest(p, initial)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Mode = policy.ModeActive })
	proof, err := first.Confirm(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ConfirmationToken = proof.Token
	head, err := first.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	// The next start fell back to the initial generation.
	fallback := versionCoordinator(t, s, p, initial)
	err = fallback.Reload(ctx, head.Generation)
	requireConfirmation(t, err, "becomes Live")
	var refused *ConfirmationError
	errors.As(err, &refused)
	if refused.Confirmation == nil || refused.Confirmation.Generation != head.Generation {
		t.Fatalf("reload refusal without token: %+v", refused.Confirmation)
	}
	if err := fallback.Reload(WithConfirmationToken(ctx, "not-the-token"), head.Generation); !errors.As(err, &refused) {
		t.Fatalf("wrong token reloaded: %v", err)
	}
	if err := fallback.Reload(WithConfirmationToken(ctx, refused.Confirmation.Token), head.Generation); err != nil {
		t.Fatal(err)
	}
	if fallback.Current().Generation != head.Generation {
		t.Fatal("reload did not publish the head")
	}

	// A source-only save over a fallback runtime confirms the head's activation too, and its token is accepted.
	again := versionCoordinator(t, s, p, initial)
	req = saveRequest(p, head)
	for i := range req.Records.Budgets {
		req.Records.Budgets[i].Config.Burst++
	}
	_, err = again.SaveAndApply(ctx, req)
	requireConfirmation(t, err, "becomes Live")
	errors.As(err, &refused)
	req.ConfirmationToken = refused.Confirmation.Token
	if _, err := again.SaveAndApply(ctx, req); err != nil {
		t.Fatal(err)
	}
}

// The provider gate through the store's own commit check: a direct store save cannot make a listed provider usable.
func TestDirectStoreSaveCannotEnableProvider(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	ctx := context.Background()
	req := saveRequest(p, active)
	editProvider(&req.Records, "tb", func(v *Provider) { v.Enabled = false })
	disabled, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req = saveRequest(p, disabled)
	editProvider(&req.Records, "tb", func(v *Provider) { v.Enabled = true })
	_, err = m.store.Save(ctx, req)
	requireConfirmation(t, err, "Provider tb becomes usable")
}

// On the quality-sync path, approving a review that widens an active version's profile needs the token (carried
// on the context), and an automatic run leaves such a review pending instead of applying it.
func TestQualitySyncApplyWideningNeedsConfirmation(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	ctx := context.Background()
	profile := active.Policy.Versions["movie-web"].Profile
	req := saveRequest(p, active)
	editProfile(t, &req.Records, profile, func(c *policy.Profile) { off := false; c.Upgrades = &off })
	narrowed, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	candidate := saveRequest(p, narrowed).Records
	editProfile(t, &candidate, profile, func(c *policy.Profile) { on := true; c.Upgrades = &on })
	raw, err := json.Marshal(Records{Profiles: candidate.Profiles, Formats: candidate.Formats, FormatSets: candidate.FormatSets, Naming: candidate.Naming})
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := m.store.db.Writer().QueryRow(`INSERT INTO settings_quality_review(source_key,base_generation,commit_id,preview,candidate,created_at) VALUES('guides',?,'content:x','{}',?,0) RETURNING id`,
		narrowed.Generation, string(raw)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	svc := &QualitySync{Store: m.store, Coordinator: m}
	_, err = svc.Apply(ctx, id, narrowed.Generation, "admin")
	requireConfirmation(t, err, "turns upgrades on")
	var refused *ConfirmationError
	errors.As(err, &refused)
	saved, err := svc.Apply(WithConfirmationToken(ctx, refused.Confirmation.Token), id, narrowed.Generation, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if up := saved.Policy.Profiles[profile].Upgrades; up == nil || !*up {
		t.Fatal("confirmed review not applied")
	}
}
