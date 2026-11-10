package settings

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestGlobalSwitchesSeedOnceAndWarn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, base, initial := versionFixture(t)
	// Startup marks the running generation published, which keeps its snapshot as the fallback.
	if err := s.MarkPublished(ctx, initial.Generation); err != nil {
		t.Fatal(err)
	}
	base.Router.Enabled, base.Library.Enabled = true, false
	base.Notify = &policy.Notify{Enabled: true}
	seeded, err := s.BootstrapSwitches(ctx, base, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !seeded.Policy.Router.Enabled || seeded.Policy.Library.Enabled || !seeded.Policy.Notify.Enabled {
		t.Fatal("seed did not use YAML")
	}
	var marker int64
	if err := db.Reader().QueryRowContext(ctx, "SELECT generation FROM settings_switches_bootstrap WHERE id=1").Scan(&marker); err != nil || marker != seeded.Generation {
		t.Fatalf("marker %d: %v", marker, err)
	}
	base.Router.Enabled, base.Library.Enabled, base.Notify.Enabled = false, true, false
	var logs bytes.Buffer
	again, err := s.BootstrapSwitches(ctx, base, "restart", slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if again.Generation != seeded.Generation || !again.Policy.Router.Enabled || again.Policy.Library.Enabled || !again.Policy.Notify.Enabled {
		t.Fatal("YAML overrode stored switches")
	}
	for _, key := range []string{"router.enabled", "library.enabled", "notify.enabled"} {
		if !strings.Contains(logs.String(), "key="+key) {
			t.Fatalf("missing warning for %s: %s", key, &logs)
		}
	}
	// A pre-switch fallback must not use the newly enabled YAML library switch.
	old, err := s.LoadGeneration(ctx, base, initial.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if old.Policy.Router.Enabled || old.Policy.Library.Enabled || old.Policy.Notify.Enabled {
		t.Fatal("old snapshot enabled a YAML switch")
	}
}

func TestGlobalSwitchesConfirmation(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"router", "library", "notify"} {
		t.Run(key, func(t *testing.T) {
			ctx := context.Background()
			s, _, base, _ := versionFixture(t)
			base.Notify = &policy.Notify{Targets: map[string]*policy.NotifyTarget{"household": {URL: "https://hook.example/secret", Native: true, Events: []string{"grab"}}}}
			initial, err := s.BootstrapSwitches(ctx, base, "test", nil)
			if err != nil {
				t.Fatal(err)
			}
			m := versionCoordinator(t, s, base, initial)
			req := saveRequest(base, initial)
			switch key {
			case "router":
				req.Records.Switches.RouterEnabled = true
			case "library":
				req.Records.Switches.LibraryEnabled = true
			case "notify":
				req.Records.Switches.NotifyEnabled = true
			}
			if _, err := s.Save(ctx, req); err == nil {
				t.Fatal("direct store save enabled a global switch")
			}
			_, err = m.SaveAndApply(ctx, req)
			var refused *ConfirmationError
			if !errors.As(err, &refused) || refused.Confirmation == nil {
				t.Fatalf("unconfirmed enable: %v", err)
			}
			if !strings.Contains(refused.Effects[0].Effect, "("+key+".enabled): ") || !strings.HasPrefix(refused.Effects[0].Effect, "Turn on ") {
				t.Fatalf("effects: %+v", refused.Effects)
			}
			stale := req
			stale.Records = saveRequest(base, initial).Records
			stale.Records.Switches.RouterEnabled, stale.Records.Switches.LibraryEnabled = true, true
			stale.Records.Switches.NotifyEnabled = true
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
			if saved.Generation != initial.Generation+1 {
				t.Fatal("missing generation")
			}
			// The anime playlist gate reads the same runtime policy.
			if _, why := saved.Policy.PlaylistGate(); key == "library" && why == "library.enabled is false" {
				t.Fatal("playlist gate kept library off")
			}
			off := saveRequest(base, saved)
			off.Records.Switches = &GlobalSwitches{}
			disabled, err := m.SaveAndApply(ctx, off)
			if err != nil {
				t.Fatalf("off needed token: %v", err)
			}
			if disabled.Policy.Router.Enabled || disabled.Policy.Library.Enabled || disabled.Policy.Notify.Enabled {
				t.Fatal("runtime kept enabled switch")
			}
			if _, why := disabled.Policy.PlaylistGate(); why != "library.enabled is false" {
				t.Fatalf("playlist gate after switch off: %s", why)
			}
			// The old token cannot enable it again after another generation.
			req.ExpectedGeneration = disabled.Generation
			if _, err := m.SaveAndApply(ctx, req); !errors.As(err, &refused) {
				t.Fatalf("stale token accepted: %v", err)
			}
		})
	}
}

func TestGlobalSwitchForceOff(t *testing.T) {
	ctx := context.Background()
	for _, prefix := range []string{"THEATARR_", "THEATARR_"} {
		t.Run(prefix, func(t *testing.T) {
			s, _, base, _ := versionFixture(t)
			base.Router.Enabled, base.Library.Enabled = true, true
			base.Notify = &policy.Notify{Enabled: true}
			seed, err := s.BootstrapSwitches(ctx, base, "test", nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(prefix+"FORCE_OFF", "router, library,notify,on")
			loaded, err := s.Load(ctx, base)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Policy.Router.Enabled || loaded.Policy.Library.Enabled || loaded.Policy.Notify.Enabled || len(loaded.Policy.ForcedOff) != 3 {
				t.Fatal("override did not force off")
			}
			if *loaded.Records.Switches != *seed.Records.Switches {
				t.Fatal("override changed DB state")
			}
		})
	}
}

func TestGlobalSwitchOmittedAndImportPreserveDB(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, base, _ := versionFixture(t)
	initial, err := s.BootstrapSwitches(ctx, base, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	base.Router.Enabled, base.Library.Enabled = true, true
	base.Notify = &policy.Notify{Enabled: true}
	req := saveRequest(base, initial)
	req.Records.Switches = nil
	m := versionCoordinator(t, s, base, initial)
	saved, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Policy.Router.Enabled || saved.Policy.Library.Enabled || saved.Policy.Notify.Enabled || saved.Records.Switches == nil {
		t.Fatal("omission restored YAML")
	}
}

func TestGlobalSwitchEffectsNameDestinations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, base, _ := versionFixture(t)
	base.Notify = &policy.Notify{Targets: map[string]*policy.NotifyTarget{"household": {URL: "https://hook.example/private-token", Native: true}}}
	initial, err := s.BootstrapSwitches(ctx, base, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := versionCoordinator(t, s, base, initial)
	req := saveRequest(base, initial)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Mode = policy.ModeActive; v.Config.Grab = true })
	proof, err := m.Confirm(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ConfirmationToken = proof.Token
	active, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().ExecContext(ctx, `INSERT INTO arrview_notification (view,name,implementation,resource,created_at,updated_at) VALUES ('radarr','Phone','Webhook','{}',0,0)`); err != nil {
		t.Fatal(err)
	}
	req = saveRequest(base, active)
	req.Records.Switches = &GlobalSwitches{RouterEnabled: true, LibraryEnabled: true, NotifyEnabled: true}
	proof, err = m.Confirm(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, effect := range proof.Effects {
		text += effect.Effect
	}
	for _, expected := range []string{"Turn on Allow new downloads (router.enabled)", "Turn on Write library links (library.enabled)", "Turn on Send notifications (notify.enabled)", "movie-web (downloads on)", "rd, ad, tb", "movie-web: roots", active.Policy.Versions["movie-web"].Library, "household", "radarr/Phone"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("effect lacks %s: %s", expected, text)
		}
	}
	if strings.Contains(text, "private-token") {
		t.Fatal("confirmation leaked webhook URL")
	}
	// Changing installed destinations invalidates an outstanding enable token too.
	if _, err := db.Writer().ExecContext(ctx, "UPDATE arrview_notification SET name = 'Tablet'"); err != nil {
		t.Fatal(err)
	}
	req.ConfirmationToken = proof.Token
	var refused *ConfirmationError
	if _, err := m.SaveAndApply(ctx, req); !errors.As(err, &refused) {
		t.Fatalf("changed targets accepted: %v", err)
	}
}

func TestGlobalSwitchFallbackCannotReenableHeadOff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, base, _ := versionFixture(t)
	base.Router.Enabled, base.Library.Enabled = true, true
	base.Notify = &policy.Notify{Enabled: true}
	enabled, err := s.BootstrapSwitches(ctx, base, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPublished(ctx, enabled.Generation); err != nil {
		t.Fatal(err)
	}
	m := versionCoordinator(t, s, base, enabled)
	off := saveRequest(base, enabled)
	off.Records.Switches = &GlobalSwitches{}
	if _, err := m.SaveAndApply(ctx, off); err != nil {
		t.Fatal(err)
	}
	fallback, err := s.LoadGeneration(ctx, base, enabled.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Policy.Router.Enabled || fallback.Policy.Library.Enabled || fallback.Policy.Notify.Enabled {
		t.Fatal("fallback re-enabled switches")
	}
}

func TestGlobalSwitchImportPreservesDB(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, base, _ := versionFixture(t)
	if _, err := s.BootstrapQuality(ctx, base, "test"); err != nil {
		t.Fatal(err)
	}
	initial, err := s.BootstrapSwitches(ctx, base, "test", nil)
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
	imported.Notify = &policy.Notify{Enabled: true}
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
	if saved.Policy.Router.Enabled || saved.Policy.Library.Enabled || saved.Policy.Notify.Enabled {
		t.Fatal("import enabled global switches")
	}
}

func TestVersionConfirmationReportsDBSwitches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, base, _ := versionFixture(t)
	base.Router.Enabled, base.Library.Enabled = true, false
	seeded, err := s.BootstrapSwitches(ctx, base, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The YAML changes after the seed; the confirmations still state the stored values.
	base.Router.Enabled, base.Library.Enabled = false, true
	m := versionCoordinator(t, s, base, seeded)
	req := saveRequest(base, seeded)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Mode = policy.ModeActive; v.Config.Grab = true })
	proof, err := m.Confirm(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, effect := range proof.Effects {
		text += effect.Effect + "\n"
	}
	if !strings.Contains(text, "router.enabled is on (currently true)") || !strings.Contains(text, "library.enabled is on (currently false)") ||
		strings.Contains(text, "router.enabled is on (currently false)") || strings.Contains(text, "library.enabled is on (currently true)") {
		t.Fatalf("confirmation does not report the DB switches:\n%s", text)
	}
}

func TestForceOffUnknownNames(t *testing.T) {
	// The override can only turn switches off; an unknown name is recorded and changes nothing.
	t.Setenv("THEATARR_FORCE_OFF", "router, routr")
	p := &policy.Policy{Router: &policy.Router{Enabled: true}, Library: &policy.Library{Enabled: true}, Notify: &policy.Notify{Enabled: true}}
	ApplyForceOff(p)
	if p.Router.Enabled || !p.Library.Enabled || !p.Notify.Enabled {
		t.Fatalf("override: %+v %+v %+v", p.Router, p.Library, p.Notify)
	}
	t.Setenv("THEATARR_FORCE_OFF", "library,notifications,router,routr")
	p = &policy.Policy{Router: &policy.Router{Enabled: true}, Library: &policy.Library{Enabled: true}, Notify: &policy.Notify{Enabled: true}}
	ApplyForceOff(p)
	if p.Router.Enabled || p.Library.Enabled || !p.Notify.Enabled || len(p.ForcedOff) != 2 {
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

// Startup fallback cannot bring back a version's active mode or grab that the stored head no longer has.
func TestVersionFallbackCannotReactivateHeadShadow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, base, _ := versionFixture(t)
	initial, err := s.BootstrapSwitches(ctx, base, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.Reader().QueryRowContext(ctx, "SELECT snapshot FROM settings_generation WHERE generation = ?",
		initial.Generation).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored generationSnapshot
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range stored.Records.Versions {
		if stored.Records.Versions[i].Key == "movie-web" {
			stored.Records.Versions[i].Config.Mode = policy.ModeActive
			stored.Records.Versions[i].Config.Grab = true
			found = true
		}
	}
	if !found {
		t.Fatal("fixture has no movie-web version")
	}
	edited, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().ExecContext(ctx, "UPDATE settings_generation SET snapshot = ? WHERE generation = ?",
		string(edited), initial.Generation); err != nil {
		t.Fatal(err)
	}
	fallback, err := s.LoadGeneration(ctx, base, initial.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if v := fallback.Policy.Versions["movie-web"]; v.Mode == policy.ModeActive || v.Grab {
		t.Fatalf("fallback kept mode %q grab %v over a shadow head", v.Mode, v.Grab)
	}
	// A version the head deleted is recovered as shadow without grab.
	if _, err := db.Writer().ExecContext(ctx, "DELETE FROM setting_version WHERE key = 'movie-web'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().ExecContext(ctx, "UPDATE settings_generation SET snapshot = ? WHERE generation = ?",
		string(edited), initial.Generation); err != nil {
		t.Fatal(err)
	}
	fallback, err = s.LoadGeneration(ctx, base, initial.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if v := fallback.Policy.Versions["movie-web"]; v.Mode == policy.ModeActive || v.Grab {
		t.Fatalf("fallback kept mode %q grab %v for a version the head removed", v.Mode, v.Grab)
	}
}

func TestRouterSwitchWarnsWhenLibraryOff(t *testing.T) {
	t.Parallel()
	before := &Snapshot{Policy: &policy.Policy{}}
	next := &Snapshot{Policy: &policy.Policy{}, Records: Records{Switches: &GlobalSwitches{RouterEnabled: true}}}
	effects, err := globalSwitchEffects(context.Background(), nil, before, next)
	if err != nil || len(effects) != 1 || !strings.Contains(effects[0].Effect, "Warning: Write library links (library.enabled) is off") {
		t.Fatalf("missing library warning: %+v %v", effects, err)
	}
}

// A client-routed version's adds go to its client: the router switch's effect names the client, not providers.
func TestRouterSwitchNamesTheClientOfAClientRoutedVersion(t *testing.T) {
	t.Parallel()
	pol := &policy.Policy{Versions: map[string]*policy.VersionPolicy{
		"movie": {Mode: policy.ModeActive, Grab: true, Storage: policy.StorageDebrid, AddVia: policy.AddViaClient,
			Client: "decypharr"}}}
	before := &Snapshot{Policy: &policy.Policy{}}
	next := &Snapshot{Policy: pol, Records: Records{Switches: &GlobalSwitches{RouterEnabled: true}}}
	effects, err := globalSwitchEffects(context.Background(), nil, before, next)
	if err != nil || len(effects) != 1 || !strings.Contains(effects[0].Effect, "movie (downloads on): client decypharr") {
		t.Fatalf("effects %+v %v", effects, err)
	}
}
