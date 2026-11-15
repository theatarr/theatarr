package settings

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"go.yaml.in/yaml/v3"
)

// The switches record is seeded once, paused, whatever the YAML says; a restart does not change it.
func TestGlobalSwitchesSeedOncePaused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, base, initial := versionFixture(t)
	// Startup marks the running generation published, which keeps its snapshot as the fallback.
	if err := s.MarkPublished(ctx, initial.Generation); err != nil {
		t.Fatal(err)
	}
	base.Router.Enabled, base.Library.Enabled = true, true
	seeded, err := s.BootstrapSwitches(ctx, base, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !seeded.Records.Switches.Paused || seeded.Policy.Router.Enabled || seeded.Policy.Library.Enabled {
		t.Fatal("seed is not paused")
	}
	var marker int64
	if err := db.Reader().QueryRowContext(ctx, "SELECT generation FROM settings_switches_bootstrap WHERE id=1").Scan(&marker); err != nil || marker != seeded.Generation {
		t.Fatalf("marker %d: %v", marker, err)
	}
	again, err := s.BootstrapSwitches(ctx, base, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if again.Generation != seeded.Generation || again.Policy.Router.Enabled || again.Policy.Library.Enabled {
		t.Fatal("restart changed the stored switches")
	}
	// A pre-switch fallback must not use the YAML switches.
	old, err := s.LoadGeneration(ctx, base, initial.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if old.Policy.Router.Enabled || old.Policy.Library.Enabled {
		t.Fatal("old snapshot enabled a YAML switch")
	}
}

// Turning Pause off asks for confirmation, turning it on does not; both runtime switches follow it.
func TestGlobalSwitchesConfirmation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, base, _ := versionFixture(t)
	initial, err := s.BootstrapSwitches(ctx, base, "test")
	if err != nil {
		t.Fatal(err)
	}
	m := versionCoordinator(t, s, base, initial)
	req := saveRequest(base, initial)
	req.Records.Switches = &GlobalSwitches{Paused: false}
	if _, err := s.Save(ctx, req); err == nil {
		t.Fatal("direct store save turned Pause off")
	}
	_, err = m.SaveAndApply(ctx, req)
	var refused *ConfirmationError
	if !errors.As(err, &refused) || refused.Confirmation == nil {
		t.Fatalf("unconfirmed Pause off: %v", err)
	}
	if len(refused.Effects) != 1 || !strings.HasPrefix(refused.Effects[0].Effect, "Turn Pause off: ") {
		t.Fatalf("effects: %+v", refused.Effects)
	}
	stale := req
	stale.Records = saveRequest(base, initial).Records
	stale.Records.Switches = &GlobalSwitches{Paused: false}
	editVersion(&stale.Records, "movie-web", func(v *Version) { v.Config.Edition = "changed" })
	stale.ConfirmationToken = refused.Confirmation.Token
	if _, err := m.SaveAndApply(ctx, stale); !errors.As(err, &refused) {
		t.Fatalf("changed candidate accepted: %v", err)
	}
	proof, err := m.Confirm(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ConfirmationToken = proof.Token
	saved, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Generation != initial.Generation+1 || !saved.Policy.Router.Enabled || !saved.Policy.Library.Enabled {
		t.Fatal("Pause off did not open both switches")
	}
	// The anime playlist gate reads the same runtime policy.
	if _, why := saved.Policy.PlaylistGate(); why == "library.enabled is false" {
		t.Fatal("playlist gate kept library off")
	}
	on := saveRequest(base, saved)
	on.Records.Switches = &GlobalSwitches{Paused: true}
	paused, err := m.SaveAndApply(ctx, on)
	if err != nil {
		t.Fatalf("Pause on needed a token: %v", err)
	}
	if paused.Policy.Router.Enabled || paused.Policy.Library.Enabled {
		t.Fatal("runtime kept a switch open")
	}
	if _, why := paused.Policy.PlaylistGate(); why != "library.enabled is false" {
		t.Fatalf("playlist gate after Pause on: %s", why)
	}
	// The old token cannot turn Pause off again after another generation.
	req.ExpectedGeneration = paused.Generation
	if _, err := m.SaveAndApply(ctx, req); !errors.As(err, &refused) {
		t.Fatalf("stale token accepted: %v", err)
	}
}

// THEATARR_FORCE_OFF: router, library and pause each force Pause (both runtime switches closed, recorded as "pause");
// notify is separate. The stored record does not change.
func TestForceOffForcesPause(t *testing.T) {
	ctx := context.Background()
	for _, names := range []string{"pause", "router", "library", " router, library,pause"} {
		t.Run(names, func(t *testing.T) {
			s, _, base, initial := versionFixture(t)
			if _, err := s.BootstrapSwitches(ctx, base, "test"); err != nil {
				t.Fatal(err)
			}
			m := versionCoordinator(t, s, base, initial)
			head, err := s.Load(ctx, base)
			if err != nil {
				t.Fatal(err)
			}
			req := saveRequest(base, head)
			req.Records.Switches = &GlobalSwitches{Paused: false}
			proof, err := m.Confirm(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			req.ConfirmationToken = proof.Token
			if _, err := m.SaveAndApply(ctx, req); err != nil {
				t.Fatal(err)
			}
			t.Setenv("THEATARR_FORCE_OFF", names)
			loaded, err := s.Load(ctx, base)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Policy.Router.Enabled || loaded.Policy.Library.Enabled || !slices.Equal(loaded.Policy.ForcedOff, []string{"pause"}) {
				t.Fatalf("override did not force Pause: forced %v", loaded.Policy.ForcedOff)
			}
			if loaded.Records.Switches.Paused {
				t.Fatal("override changed DB state")
			}
		})
	}
	t.Run("notify", func(t *testing.T) {
		t.Setenv("THEATARR_FORCE_OFF", "notify")
		p := &policy.Policy{Router: &policy.Router{Enabled: true}, Library: &policy.Library{Enabled: true}, Notify: &policy.Notify{Enabled: true}}
		ApplyForceOff(p)
		if !p.Router.Enabled || !p.Library.Enabled || p.Notify.Enabled || !slices.Equal(p.ForcedOff, []string{"notify"}) {
			t.Fatalf("notify override: %+v", p.ForcedOff)
		}
	})
}

func TestGlobalSwitchOmittedAndImportPreserveDB(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, base, _ := versionFixture(t)
	initial, err := s.BootstrapSwitches(ctx, base, "test")
	if err != nil {
		t.Fatal(err)
	}
	base.Router.Enabled, base.Library.Enabled = true, true
	req := saveRequest(base, initial)
	req.Records.Switches = nil
	m := versionCoordinator(t, s, base, initial)
	saved, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Policy.Router.Enabled || saved.Policy.Library.Enabled || saved.Records.Switches == nil || !saved.Records.Switches.Paused {
		t.Fatal("omission restored YAML")
	}
}

func TestGlobalSwitchEffectsNameLibraries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, base, _ := versionFixture(t)
	base.Notify = &policy.Notify{Targets: map[string]*policy.NotifyTarget{"household": {URL: "https://hook.example/private-token", Native: true}}}
	initial, err := s.BootstrapSwitches(ctx, base, "test")
	if err != nil {
		t.Fatal(err)
	}
	m := versionCoordinator(t, s, base, initial)
	req := saveRequest(base, initial)
	req.Records.Switches = &GlobalSwitches{Paused: false}
	proof, err := m.Confirm(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, effect := range proof.Effects {
		text += effect.Effect
	}
	for _, expected := range []string{"Turn Pause off: libraries search, download and link again: ", "4K WEB", "Waiting downloads are imported."} {
		if !strings.Contains(text, expected) {
			t.Fatalf("effect lacks %s: %s", expected, text)
		}
	}
}

// Pause is not part of history: a recovered generation takes it from the stored head, so it never turns Pause off.
func TestRestoreKeepsPause(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, base, initial := versionFixture(t)
	seeded, err := s.BootstrapSwitches(ctx, base, "test")
	if err != nil {
		t.Fatal(err)
	}
	m := versionCoordinator(t, s, base, initial)
	req := saveRequest(base, seeded)
	req.Records.Switches = &GlobalSwitches{Paused: false}
	proof, err := m.Confirm(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ConfirmationToken = proof.Token
	open, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPublished(ctx, open.Generation); err != nil {
		t.Fatal(err)
	}
	paused := saveRequest(base, open)
	paused.Records.Switches = &GlobalSwitches{Paused: true}
	if _, err := m.SaveAndApply(ctx, paused); err != nil {
		t.Fatal(err)
	}
	fallback, err := s.LoadGeneration(ctx, base, open.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if !fallback.Records.Switches.Paused || fallback.Policy.Router.Enabled || fallback.Policy.Library.Enabled {
		t.Fatal("fallback turned Pause off")
	}
}

func TestGlobalSwitchImportPreservesDB(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, base, _ := versionFixture(t)
	if _, err := s.BootstrapQuality(ctx, base, "test"); err != nil {
		t.Fatal(err)
	}
	initial, err := s.BootstrapSwitches(ctx, base, "test")
	if err != nil {
		t.Fatal(err)
	}
	// A whole-policy import must not replace switch state, even when its YAML now says on.
	raw, err := os.ReadFile(filepath.Join(base.Dir, "policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var imported policy.Policy
	if err := yaml.Unmarshal(raw, &imported); err != nil {
		t.Fatal(err)
	}
	imported.Router.Enabled, imported.Library.Enabled = true, true
	raw, err = yaml.Marshal(&imported)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base.Dir, "policy.yaml"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	m := versionCoordinator(t, s, base, initial)
	req := ImportRequest{Dir: base.Dir, ExpectedGeneration: initial.Generation, Actor: "test", Lookup: fixtureLookup}
	approval, err := m.ConfirmImport(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ConfirmationToken = approval.Token
	saved, err := m.ImportAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Policy.Router.Enabled || saved.Policy.Library.Enabled || !saved.Records.Switches.Paused {
		t.Fatal("import turned Pause off")
	}
}

// Only turning Pause off asks: Pause on and notifications do not.
func TestControlConfirmations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	open := func(p *policy.Policy) *policy.Policy {
		c, _ := p.Clone()
		c.Router, c.Library = &policy.Router{Enabled: true}, &policy.Library{Enabled: true}
		return c
	}
	_, _, base, _ := versionFixture(t)
	notified, _ := base.Clone()
	notified.Notify = &policy.Notify{Enabled: true}
	snap := func(p *policy.Policy, paused bool) *Snapshot {
		return &Snapshot{Policy: p, Records: Records{Switches: &GlobalSwitches{Paused: paused}}}
	}
	for _, c := range []struct {
		name         string
		before, next *Snapshot
		asks         bool
	}{
		{"Pause off", snap(base, true), snap(open(base), false), true},
		{"Pause on", snap(open(base), false), snap(base, true), false},
		{"notifications on", snap(base, true), snap(notified, true), false},
	} {
		effects, err := snapshotWriteEffects(ctx, nil, c.before, c.next)
		if err != nil {
			t.Fatal(err)
		}
		if (len(effects) > 0) != c.asks || len(effects) > 1 {
			t.Errorf("%s: effects %+v", c.name, effects)
		}
	}
}

func TestForceOffUnknownNames(t *testing.T) {
	// The override can only force Pause or notifications; an unknown name is recorded and changes nothing.
	t.Setenv("THEATARR_FORCE_OFF", "library,notifications,router,routr")
	p := &policy.Policy{Router: &policy.Router{Enabled: true}, Library: &policy.Library{Enabled: true}, Notify: &policy.Notify{Enabled: true}}
	ApplyForceOff(p)
	if p.Router.Enabled || p.Library.Enabled || !p.Notify.Enabled || !slices.Equal(p.ForcedOff, []string{"pause"}) {
		t.Fatalf("override list: forced %v", p.ForcedOff)
	}
	want := []string{"THEATARR_FORCE_OFF: notifications", "THEATARR_FORCE_OFF: routr"}
	if !slices.Equal(p.ForceOffUnknown, want) {
		t.Fatalf("unknown %v, want %v", p.ForceOffUnknown, want)
	}
	var logs bytes.Buffer
	WarnForceOffUnknown(p, slog.New(slog.NewTextHandler(&logs, nil)))
	for _, entry := range want {
		if !strings.Contains(logs.String(), entry) || !strings.Contains(logs.String(), "level=WARN") {
			t.Fatalf("missing warning for %s: %s", entry, &logs)
		}
	}
}
