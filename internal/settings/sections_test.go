package settings

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

const inventedPlexToken = "invented-plex-token-for-section-tests"

func sectionsFixture(t *testing.T, edits ...func(*policy.Policy)) (*Store, *policy.Policy, *Snapshot) {
	t.Helper()
	s, _, base, _ := versionFixture(t, edits...)
	s.SetLookup(fixtureLookup)
	if _, err := s.BootstrapSwitches(context.Background(), base, "test", nil); err != nil {
		t.Fatal(err)
	}
	snap, err := s.BootstrapSections(context.Background(), base, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, base, snap
}

func sectionRecord(t *testing.T, snap *Snapshot, key string) Section {
	t.Helper()
	for _, r := range snap.Records.Sections {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("no section %s", key)
	return Section{}
}

// vpnGuardOn gives a fixture policy the VPN egress guard on indexers (the example ships it off).
func vpnGuardOn(p *policy.Policy) {
	p.Egress = &policy.Egress{CheckURL: "https://api.ipify.org", VPNIPs: []string{"198.51.100.7"},
		RequireVPNFor: []string{policy.EgressIndexers}}
}

// editSection decodes a section's stored config (empty for a section the settings do not have), lets change edit it
// and returns the patch.
func editSection(t *testing.T, snap *Snapshot, key string, change func(map[string]any)) Section {
	t.Helper()
	r := sectionRecord(t, snap, key)
	var m map[string]any
	if err := json.Unmarshal(r.Config, &m); err != nil {
		t.Fatal(err)
	}
	if m == nil {
		m = map[string]any{}
	}
	change(m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	r.Config = b
	return r
}

func TestSectionsSeedOnceFromYAML(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, base, before := versionFixture(t)
	s.SetLookup(fixtureLookup)
	seeded, err := s.BootstrapSections(ctx, base, "serve", nil)
	if err != nil {
		t.Fatal(err)
	}
	if seeded.Generation != before.Generation+1 {
		t.Fatalf("generation %d after %d", seeded.Generation, before.Generation)
	}
	// Live installs come up with exactly their values: only the views' key names change, to the database's.
	want, err := before.Policy.Clone()
	if err != nil {
		t.Fatal(err)
	}
	got, err := seeded.Policy.Clone()
	if err != nil {
		t.Fatal(err)
	}
	for key, view := range got.Views {
		if !strings.HasPrefix(view.APIKeyEnv, policy.SettingsSecretPrefix) {
			t.Fatalf("view %s key not in the database: %s", key, view.APIKeyEnv)
		}
		if v := got.Getenv(view.APIKeyEnv); v != inventedSecret+"-"+want.Views[key].APIKeyEnv {
			t.Fatalf("view %s key not seeded from its variable", key)
		}
		view.APIKeyEnv = want.Views[key].APIKeyEnv
	}
	got.Revision, want.Revision = "", ""
	got.WithSettingsSecrets(nil)
	want.WithSettingsSecrets(nil)
	if !reflect.DeepEqual(got, want) {
		a, _ := json.Marshal(want)
		b, _ := json.Marshal(got)
		t.Fatalf("seed changed the policy\nwant %s\ngot  %s", a, b)
	}
	if len(seeded.Records.Sections) != len(Sections) {
		t.Fatalf("seeded %d sections", len(seeded.Records.Sections))
	}
	// An omitted list stays omitted: egress.require_vpn_for's default differs from an empty list.
	if base.Egress != nil && base.Egress.RequireVPNFor == nil && got.Egress.RequireVPNFor != nil {
		t.Fatal("seed turned an omitted require_vpn_for into an empty one")
	}
	// Later starts keep the database and warn about each YAML section that differs.
	base.Notify.AllowHosts = []string{"elsewhere.example"}
	base.Views["radarr"].Path = "/elsewhere"
	var logs bytes.Buffer
	again, err := s.BootstrapSections(ctx, base, "restart", slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if again.Generation != seeded.Generation || slices.Contains(again.Policy.Notify.AllowHosts, "elsewhere.example") ||
		again.Policy.Views["radarr"].Path == "/elsewhere" {
		t.Fatal("YAML overrode the database")
	}
	for _, key := range []string{"key=notify", "key=views.radarr"} {
		if !strings.Contains(logs.String(), key) {
			t.Fatalf("no warning for %s: %s", key, &logs)
		}
	}
}

func TestSectionsDatabaseWinsAndApplies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, base, snap := sectionsFixture(t)
	m := versionCoordinator(t, s, base, snap)
	req := saveRequest(base, snap)
	req.Records.Sections = []Section{editSection(t, snap, "notify", func(c map[string]any) { c["poll"] = "45s" })}
	saved, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Policy.Notify.Poll != policy.Duration(45e9) || m.Current().Policy.Notify.Poll != policy.Duration(45e9) {
		t.Fatal("tuning change not applied")
	}
	// Leaving a section out of a save keeps it; it never falls back to the files.
	req = saveRequest(base, saved)
	req.Records.Sections = nil
	kept, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Policy.Notify.Poll != policy.Duration(45e9) || len(kept.Records.Sections) != len(Sections) {
		t.Fatal("omitted sections dropped")
	}
	// A section the database says is absent stays absent whatever the files hold.
	req = saveRequest(base, kept)
	req.Records.Sections = []Section{{Record: Record{Key: "playlists"}, Config: json.RawMessage("null")}}
	cleared, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Policy.Playlists != nil || base.Playlists == nil {
		t.Fatal("null section not applied")
	}
	// The switches stay with their own record.
	req = saveRequest(base, cleared)
	req.Records.Sections = []Section{editSection(t, cleared, "library", func(c map[string]any) { c["enabled"] = true })}
	if _, err := s.Save(ctx, req); err == nil {
		t.Fatal("library.enabled accepted from a section")
	}
	if err := SectionOwnedElsewhere("library", json.RawMessage(`{"enabled": true, "versions": {"movies": {"roots": ["/x"]}}}`)); err == nil ||
		!strings.Contains(err.Error(), "global switch") || !strings.Contains(err.Error(), "roots") {
		t.Fatalf("owned fields: %v", err)
	}
	// Validation is the policy files' own.
	req = saveRequest(base, cleared)
	req.Records.Sections = []Section{editSection(t, cleared, "egress", func(c map[string]any) { c["require_vpn_for"] = []string{"everything"} })}
	var verr *policy.Errors
	if _, err := s.Save(ctx, req); !errors.As(err, &verr) {
		t.Fatalf("invalid egress accepted: %v", err)
	}
	req.Records.Sections = []Section{editSection(t, cleared, "egress", func(c map[string]any) { c["no_such_field"] = 1 })}
	if _, err := s.Save(ctx, req); !errors.As(err, &verr) || !strings.Contains(err.Error(), "no_such_field") {
		t.Fatalf("unknown field accepted: %v", err)
	}
}

func TestSectionsRiskyChangesNeedConfirmation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		section, effect string
		change          func(map[string]any)
	}{
		{"library", "Change where files are read or written: library.mount.path", func(c map[string]any) {
			c["mount"].(map[string]any)["path"] = "/mnt/elsewhere"
		}},
		{"library", "Send requests to another host: library.plex.url", func(c map[string]any) {
			c["plex"].(map[string]any)["url"] = "http://plex.elsewhere:32400"
		}},
		{"metadata", "Send requests to another host: metadata.sources.tmdb.url", func(c map[string]any) {
			c["sources"].(map[string]any)["tmdb"].(map[string]any)["url"] = "https://tmdb.elsewhere/3"
		}},
		{"notify", "Send requests to another host: notify.targets.new.url (none) → https://hook.example/[redacted]", func(c map[string]any) {
			targets, _ := c["targets"].(map[string]any)
			if targets == nil {
				targets = map[string]any{}
				c["targets"] = targets
			}
			targets["new"] = map[string]any{"url": "https://hook.example/token-in-path", "native": true}
		}},
		{"egress", "Change the VPN guard (what it requires or accepts): egress.require_vpn_for", func(c map[string]any) {
			c["require_vpn_for"] = []string{}
		}},
		// Loosening safety settings and repointing a credential name need confirmation too.
		{"router", "Let the router add releases not known to be cached: router.cached_only true → false", func(c map[string]any) {
			c["cached_only"] = false
		}},
		{"library", "Use another credential: library.plex.token_env THEATARR_PLEX_TOKEN → THEATARR_TVDB_KEY", func(c map[string]any) {
			c["plex"].(map[string]any)["token_env"] = "THEATARR_TVDB_KEY"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.effect, func(t *testing.T) {
			ctx := context.Background()
			var edits []func(*policy.Policy)
			if tc.section == "egress" {
				edits = append(edits, vpnGuardOn)
			}
			s, base, snap := sectionsFixture(t, edits...)
			m := versionCoordinator(t, s, base, snap)
			req := saveRequest(base, snap)
			req.Records.Sections = []Section{editSection(t, snap, tc.section, tc.change)}
			if _, err := s.Save(ctx, req); err == nil {
				t.Fatal("direct save made a risky change")
			}
			_, err := m.SaveAndApply(ctx, req)
			var refused *ConfirmationError
			if !errors.As(err, &refused) || refused.Confirmation == nil {
				t.Fatalf("unconfirmed: %v", err)
			}
			found := false
			for _, e := range refused.Effects {
				found = found || strings.Contains(e.Effect, tc.effect)
				if strings.Contains(e.Effect, "token-in-path") {
					t.Fatalf("effect repeats a URL path: %s", e.Effect)
				}
			}
			if !found {
				t.Fatalf("effects: %+v", refused.Effects)
			}
			req.ConfirmationToken = refused.Confirmation.Token
			if _, err := m.SaveAndApply(ctx, req); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSectionSecretsWriteOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, base, snap := sectionsFixture(t)
	m := versionCoordinator(t, s, base, snap)
	req := saveRequest(base, snap)
	req.SectionSecretWrites = map[string]map[string]Secret{"library": {"plex.token": NewSecret(inventedPlexToken)}}
	saved, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if r := sectionRecord(t, saved, "library"); !reflect.DeepEqual(r.SecretsSet, []string{"plex.token"}) {
		t.Fatalf("secrets_set %v", r.SecretsSet)
	}
	env := saved.Policy.Library.Plex.TokenEnv
	if !strings.HasPrefix(env, policy.SettingsSecretPrefix) || saved.Policy.Getenv(env) != inventedPlexToken {
		t.Fatalf("runtime does not resolve the stored token (%s)", env)
	}
	records, _ := json.Marshal(saved.Records)
	exported, err := saved.Export()
	if err != nil {
		t.Fatal(err)
	}
	var trail bytes.Buffer
	for _, a := range audit(t, s) {
		b, _ := json.Marshal(a)
		trail.Write(b)
	}
	for name, text := range map[string]string{"records": string(records), "export": string(exported), "audit": trail.String(),
		"snapshot": saved.String()} {
		if strings.Contains(text, inventedPlexToken) {
			t.Fatalf("%s leaks the token", name)
		}
	}
	if !strings.Contains(string(exported), "sections/library plex.token credential: set") {
		t.Fatalf("export does not list the credential:\n%s", exported)
	}
	// An unknown path is refused; a cleared one falls back to the environment name.
	req = saveRequest(base, saved)
	req.SectionSecretWrites = map[string]map[string]Secret{"library": {"plex.nothing": NewSecret("x")}}
	if _, err := m.SaveAndApply(ctx, req); err == nil {
		t.Fatal("unknown credential path accepted")
	}
	req.SectionSecretWrites = map[string]map[string]Secret{"library": {"plex.token": NewSecret("")}}
	cleared, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Policy.Library.Plex.TokenEnv != base.Library.Plex.TokenEnv || len(sectionRecord(t, cleared, "library").SecretsSet) != 0 {
		t.Fatal("clear did not fall back to the environment name")
	}
}

func TestViewsInDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, base, snap := sectionsFixture(t)
	m := versionCoordinator(t, s, base, snap)
	req := saveRequest(base, snap)
	var radarr ViewBinding
	for _, v := range req.Records.Views {
		if v.Key == "radarr" {
			radarr = v
		}
	}
	created := ViewBinding{Record: Record{Key: "radarr2"}, Versions: radarr.Versions, Profiles: radarr.Profiles,
		Identity: &ViewIdentity{Flavour: "radarr", Path: "/radarr2"}}
	req.Records.Views = append(req.Records.Views, created)
	req.SecretWrites = map[string]Secret{"views/radarr2": NewSecret("invented-view-key-radarr2")}
	_, err := m.SaveAndApply(ctx, req)
	var refused *ConfirmationError
	if !errors.As(err, &refused) || !strings.Contains(refused.Effects[len(refused.Effects)-1].Effect, "Create view radarr2 at /radarr2") {
		t.Fatalf("create: %v", err)
	}
	req.ConfirmationToken = refused.Confirmation.Token
	saved, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	view := saved.Policy.Views["radarr2"]
	if view == nil || view.Path != "/radarr2" || saved.Policy.Getenv(view.APIKeyEnv) != "invented-view-key-radarr2" {
		t.Fatal("view not created with its key")
	}
	// A new key is confirmed by name, never by value.
	req = saveRequest(base, saved)
	req.SecretWrites = map[string]Secret{"views/radarr2": NewSecret("invented-view-key-rotated")}
	_, err = m.SaveAndApply(ctx, req)
	if !errors.As(err, &refused) || !strings.Contains(refused.Effects[0].Effect, "View radarr2 answers a different API key") ||
		strings.Contains(refused.Effects[0].Effect, "rotated") {
		t.Fatalf("key change: %v", err)
	}
	// Deleting a view narrows what is exposed and needs no confirmation.
	req = saveRequest(base, saved)
	req.Records.Views = slices.DeleteFunc(req.Records.Views, func(v ViewBinding) bool { return v.Key == "radarr2" })
	gone, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if gone.Policy.Views["radarr2"] != nil {
		t.Fatal("view not deleted")
	}
}

// An omitted require_vpn_for guards indexers, an explicit empty list guards nothing; going from one to the
// other is a confirmed change even though both flatten to no entries. Turning router.cached_only off from its
// omitted default (on) is confirmed as well.
func TestSectionEffectsCompareEffectiveValues(t *testing.T) {
	t.Parallel()
	p, err := policy.Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Router == nil {
		t.Fatal("example policy has no router section")
	}
	vpnGuardOn(p)
	p.Egress.RequireVPNFor = nil
	p.Router.CachedOnly = nil
	monitor, _ := p.Clone()
	monitor.Egress.RequireVPNFor = []string{}
	effects := sectionEffects(p, monitor)
	if len(effects) != 1 || !strings.Contains(effects[0].Effect, "egress.require_vpn_for indexers → (none)") {
		t.Fatalf("omitted → [] effects: %+v", effects)
	}
	if effects := sectionEffects(monitor, p); len(effects) != 1 {
		t.Fatalf("[] → omitted effects: %+v", effects)
	}
	explicit, _ := p.Clone()
	explicit.Egress.RequireVPNFor = []string{"indexers"}
	if effects := sectionEffects(p, explicit); len(effects) != 0 {
		t.Fatalf("same classes asked for confirmation: %+v", effects)
	}
	uncached, _ := p.Clone()
	off := false
	uncached.Router.CachedOnly = &off
	if effects := sectionEffects(p, uncached); len(effects) != 1 || !strings.Contains(effects[0].Effect, "router.cached_only true → false") {
		t.Fatalf("cached_only off effects: %+v", effects)
	}
	if effects := sectionEffects(uncached, p); len(effects) != 0 {
		t.Fatalf("cached_only back on asked for confirmation: %+v", effects)
	}
}

// A credential name changed while the section's own value is stored is confirmed when written, so clearing
// the value later cannot quietly switch to it.
func TestSectionCredentialRepointConfirmed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, base, snap := sectionsFixture(t)
	m := versionCoordinator(t, s, base, snap)
	req := saveRequest(base, snap)
	req.SectionSecretWrites = map[string]map[string]Secret{"library": {"plex.token": NewSecret(inventedPlexToken)}}
	saved, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req = saveRequest(base, saved)
	req.Records.Sections = []Section{editSection(t, saved, "library", func(c map[string]any) {
		c["plex"].(map[string]any)["token_env"] = "THEATARR_TVDB_KEY"
	})}
	_, err = m.SaveAndApply(ctx, req)
	var refused *ConfirmationError
	if !errors.As(err, &refused) || len(refused.Effects) != 1 ||
		!strings.Contains(refused.Effects[0].Effect, "Use another credential: library.plex.token_env THEATARR_PLEX_TOKEN → THEATARR_TVDB_KEY") {
		t.Fatalf("repoint behind a stored value: %v", err)
	}
	if strings.Contains(refused.Effects[0].Effect, inventedPlexToken) {
		t.Fatal("effect shows the credential value")
	}
}

// Each generation resolves its own stored credentials. Preparing a candidate with a new token (and a
// candidate that fails to prepare) leaves the active generation resolving the old one.
func TestSecretsBelongToTheirGeneration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, base, snap := sectionsFixture(t)
	m := versionCoordinator(t, s, base, snap)
	req := saveRequest(base, snap)
	req.SectionSecretWrites = map[string]map[string]Secret{"library": {"plex.token": NewSecret("token-old")}}
	old, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	env := old.Policy.Library.Plex.TokenEnv
	var during, candidate string
	fail := errors.New("prepare refused")
	m.prepare = func(_ context.Context, next *Snapshot, _ *Runtime) (any, error) {
		during, candidate = m.Current().Policy.Getenv(env), next.Policy.Getenv(env)
		return nil, fail
	}
	req = saveRequest(base, old)
	req.SectionSecretWrites = map[string]map[string]Secret{"library": {"plex.token": NewSecret("token-new")}}
	if _, err := m.SaveAndApply(ctx, req); !errors.Is(err, fail) {
		t.Fatalf("prepare: %v", err)
	}
	if during != "token-old" || candidate != "token-new" {
		t.Fatalf("while preparing: active %q, candidate %q", during, candidate)
	}
	if got := m.Current().Policy.Getenv(env); got != "token-old" {
		t.Fatalf("after a refused candidate the active generation resolves %q", got)
	}
	var other policy.Policy
	if _, ok := other.LookupEnv(env); ok {
		t.Fatal("a policy outside the settings generation resolves a settings credential")
	}
}

// A *_env field may not name a settings credential by its derived name. Pointing a webhook header or the
// Plex token at THEATARR_SETTINGS_SECRET_<hash> would send another record's stored secret; save and preview refuse.
func TestSectionRefusesDerivedCredentialName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, base, snap := sectionsFixture(t)
	req := saveRequest(base, snap)
	req.Records.Sections = []Section{editSection(t, snap, "library", func(c map[string]any) {
		c["plex"].(map[string]any)["token_env"] = policy.SettingsSecretPrefix + "0123456789ABCDEF"
	})}
	for name, run := range map[string]func() error{
		"save":    func() error { _, err := s.Save(ctx, req); return err },
		"preview": func() error { _, err := s.Candidate(ctx, req); return err },
	} {
		err := run()
		if err == nil || !strings.Contains(err.Error(), "sections[library].plex.token_env") || !strings.Contains(err.Error(), "stored credential") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// The DB-owned router section carries the opt-in into every published generation, including turning it off.
func TestRouterSectionAllDebridDeleteUncached(t *testing.T) {
	t.Parallel()
	s, base, snap := sectionsFixture(t)
	m := versionCoordinator(t, s, base, snap)
	for _, enabled := range []bool{false, true, false} {
		req := saveRequest(base, snap)
		req.Records.Sections = []Section{editSection(t, snap, "router", func(c map[string]any) {
			c["alldebrid_delete_uncached"] = enabled
		})}
		saved, err := m.SaveAndApply(context.Background(), req)
		if enabled {
			var refused *ConfirmationError
			if !errors.As(err, &refused) || refused.Confirmation == nil {
				t.Fatalf("opt-in did not require confirmation: %v", err)
			}
			req.ConfirmationToken = refused.Confirmation.Token
			saved, err = m.SaveAndApply(context.Background(), req)
		}
		if err != nil {
			t.Fatal(err)
		}
		if saved.Policy.RouterSettings().AllDebridDeleteUncached != enabled ||
			m.Current().Policy.RouterSettings().AllDebridDeleteUncached != enabled {
			t.Fatalf("AllDebrid delete opt-in did not apply: want %v", enabled)
		}
		if got := sectionRecord(t, saved, "router").Config; !bytes.Contains(got, []byte(`"alldebrid_delete_uncached":`)) {
			t.Fatalf("setting missing from saved section: %s", got)
		}
		snap = saved
	}
}

func TestAllDebridDeleteOptInConfirmationAndFallback(t *testing.T) {
	t.Parallel()
	s, _, head := sectionsFixture(t)
	p, _ := head.Policy.Clone()
	p.Router.AllDebridDeleteUncached = false
	fallbackPolicy, _ := p.Clone()
	fallbackPolicy.Router.AllDebridDeleteUncached = true
	effects := sectionEffects(p, fallbackPolicy)
	if len(effects) != 1 || !strings.Contains(effects[0].Effect, "router.alldebrid_delete_uncached false → true") {
		t.Fatalf("opt-in effects: %+v", effects)
	}
	if got := sectionEffects(fallbackPolicy, p); len(got) != 0 {
		t.Fatalf("opt-out effects: %+v", got)
	}
	// With the router section omitted, the opt-in is still effectively false.
	omitted, _ := p.Clone()
	omitted.Router = nil
	if got := sectionEffects(omitted, fallbackPolicy); len(got) == 0 {
		t.Fatal("omitted opt-in treated as enabled")
	}
	a, b := *head, *head
	a.Policy, b.Policy = p, fallbackPolicy
	effects, err := s.FallbackEffects(context.Background(), &a, &b)
	if err != nil || len(effects) != 1 {
		t.Fatalf("fallback effects: %+v %v", effects, err)
	}
}

func TestArrImportIntoGrabbingVersionNeedsConfirmation(t *testing.T) {
	t.Parallel()
	p, err := policy.Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	p.Router.Enabled = true
	for _, v := range p.Versions {
		v.Mode = policy.ModeActive
		v.Grab = true
	}
	const instance = "radarr_4k_debrid"
	src := p.Import.Arr[instance]
	if src == nil {
		t.Fatal("fixture missing arr source")
	}
	for _, change := range []string{"version", "new"} {
		t.Run(change, func(t *testing.T) {
			before, _ := p.Clone()
			after, _ := p.Clone()
			switch change {
			case "version":
				before.Import.Arr[instance].Version = "other"
			case "new":
				delete(before.Import.Arr, instance)
			}
			effects := sectionEffects(before, after)
			found := false
			for _, e := range effects {
				found = found || strings.Contains(e.Effect, "Import titles into grabbing library")
			}
			if !found {
				t.Fatalf("import remap effects: %+v", effects)
			}
			// The router switch does not matter: turning it on later names no import source.
			after.Router.Enabled = false
			found = false
			for _, e := range sectionEffects(before, after) {
				found = found || strings.Contains(e.Effect, "Import titles into grabbing library")
			}
			if !found {
				t.Fatal("router off: import remap onto a grabbing version needs no confirmation")
			}
			after.Versions[src.Version].Grab = false
			for _, e := range sectionEffects(before, after) {
				if strings.Contains(e.Effect, "Import titles into grabbing library") {
					t.Fatalf("grab-off effect: %+v", e)
				}
			}
		})
	}
}

// Section reads, the MCP tools and the audit (MaskSectionConfig) mask a credential in any URL of a
// section, not only notify targets; the mask sent back keeps the stored URL, and a mask moved to another host is
// refused.
func TestSectionURLQueryCredentialMasked(t *testing.T) {
	t.Parallel()
	stored := json.RawMessage(`{"plex":{"url":"http://plex.lan:32400/?X-Plex-Token=SECRET1","timeout":30}}`)
	masked := MaskSectionConfig("library", stored)
	if strings.Contains(string(masked), "SECRET1") || !strings.Contains(string(masked), "plex.lan:32400") || !strings.Contains(string(masked), `"timeout":30`) {
		t.Fatalf("masked: %s", masked)
	}
	back, err := UnmaskSectionConfig("library", masked, stored)
	if err != nil || !strings.Contains(string(back), "SECRET1") {
		t.Fatalf("mask sent back: %s %v", back, err)
	}
	moved := json.RawMessage(strings.Replace(string(masked), "plex.lan", "collector.invalid", 1))
	if _, err := UnmaskSectionConfig("library", moved, stored); err == nil || !strings.Contains(err.Error(), "library.plex.url is masked") {
		t.Fatalf("mask moved to another host: %v", err)
	}
	if got := auditValue(row{Entity: "sections", Key: "library", Config: stored}).(row); strings.Contains(string(got.Config), "SECRET1") {
		t.Fatalf("audit: %s", got.Config)
	}
	plain := json.RawMessage(`{"plex":{"url":"http://plex.lan:32400/"}}`)
	if got := MaskSectionConfig("library", plain); string(got) != string(plain) {
		t.Fatalf("plain URL changed: %s", got)
	}
}

// A stored row of a removed section (release_push, from before privacy became a label) must not block a save of
// another section; the save drops the row.
func TestSectionSaveDropsRemovedSectionRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, base, snap := sectionsFixture(t)
	if err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO setting_section (key, config, created_at, updated_at) VALUES ('release_push', '{"public":["release-pusher"]}', 1, 1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	req := saveRequest(base, snap)
	req.Records.Sections = []Section{editSection(t, snap, "notify", func(c map[string]any) { c["poll"] = "45s" })}
	saved, err := s.Save(ctx, req)
	if err != nil {
		t.Fatalf("save refused with a removed section stored: %v", err)
	}
	if saved.Policy.Notify.Poll != policy.Duration(45e9) {
		t.Fatal("edit not saved")
	}
	var n int
	if err := s.db.Reader().QueryRowContext(ctx, "SELECT count(*) FROM setting_section WHERE key = 'release_push'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("removed section row kept: %d", n)
	}
}
