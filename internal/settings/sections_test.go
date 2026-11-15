package settings

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

const inventedPlexToken = "invented-plex-token-for-section-tests"

func sectionsFixture(t *testing.T, edits ...func(*policy.Policy)) (*Store, *policy.Policy, *Snapshot) {
	t.Helper()
	s, _, base, _ := versionFixture(t, edits...)
	s.SetLookup(fixtureLookup)
	if _, err := s.BootstrapSwitches(context.Background(), base, "test"); err != nil {
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
		!strings.Contains(err.Error(), "set by Pause") || !strings.Contains(err.Error(), "roots") {
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
	saved, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	view := saved.Policy.Views["radarr2"]
	if view == nil || view.Path != "/radarr2" || saved.Policy.Getenv(view.APIKeyEnv) != "invented-view-key-radarr2" {
		t.Fatal("view not created with its key")
	}
	// Deleting a view needs no confirmation either (the UI asks before it sends the delete).
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
		// The opt-in saves at once: it only deletes Theatarr's own uncached adds.
		saved, err := m.SaveAndApply(context.Background(), req)
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

// Settings stored before the periodic arr import went still load: the import section's row, a view's alias_from and
// an empty FromArr in a profile or naming row are ignored when read, and the next save drops the import row.
func TestStoredArrSettingsLoad(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, base, _ := sectionsFixture(t)
	if _, err := s.BootstrapQuality(ctx, base, "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO setting_section (key, config, created_at, updated_at) VALUES ('import',
				'{"arr":{"radarr_4k":{"flavour":"radarr","url":"http://127.0.0.1:7878","version":"movie-remux"}}}', 1, 1)`,
			`UPDATE setting_view_binding SET config = json_set(config, '$.identity.alias_from', json_array('radarr_4k'))
				WHERE key = 'radarr'`,
			`UPDATE setting_quality_profile SET config = json_set(config, '$.FromArr', '')`,
			`UPDATE setting_version_naming SET config = json_set(config, '$.FromArr', '')`,
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.Reader().QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM setting_view_binding
		WHERE json_type(config, '$.identity.alias_from') = 'array') + (SELECT COUNT(*) FROM setting_quality_profile
		WHERE json_type(config, '$.FromArr') = 'text') + (SELECT COUNT(*) FROM setting_version_naming
		WHERE json_type(config, '$.FromArr') = 'text')`).Scan(&n); err != nil || n < 3 {
		t.Fatalf("fixture rows %d %v", n, err)
	}
	loaded, err := s.Load(ctx, base)
	if err != nil {
		t.Fatalf("stored arr settings do not load: %v", err)
	}
	for _, r := range loaded.Records.Sections {
		if r.Key == "import" {
			t.Fatal("the import section loaded")
		}
	}
	if v := loaded.Policy.Views["radarr"]; v == nil || v.Path != base.Views["radarr"].Path {
		t.Fatalf("view radarr %+v", v)
	}
	req := saveRequest(base, loaded)
	req.Records.Sections = []Section{editSection(t, loaded, "notify", func(c map[string]any) { c["poll"] = "45s" })}
	if _, err := s.Save(ctx, req); err != nil {
		t.Fatalf("save refused with arr settings stored: %v", err)
	}
	if err := s.db.Reader().QueryRowContext(ctx, "SELECT count(*) FROM setting_section WHERE key = 'import'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("import section row kept: %d %v", n, err)
	}
}

// A section is stored without an optional field that is unset, so a build from before the field existed (it decodes
// sections strictly) still loads it: notify.targets.*.warnings appears only where it is on.
func TestSectionConfigOmitsUnsetOptionalField(t *testing.T) {
	t.Parallel()
	p := &policy.Policy{Notify: &policy.Notify{Targets: map[string]*policy.NotifyTarget{
		"chan": {URL: "https://discord.example/api/webhooks/1/x", Format: policy.NotifyFormatDiscord},
		"hook": {URL: "http://192.168.1.50:8080/hook", Native: true},
	}}}
	raw, err := SectionConfig(p, "notify")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Targets map[string]map[string]any `json:"targets"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"auth_header_env", "events", "format", "native", "url", "versions", "views"}
	for name, target := range got.Targets {
		if keys := sortedPolicyKeys(target); !slices.Equal(keys, want) {
			t.Errorf("target %s stored with keys %v, want %v", name, keys, want)
		}
	}
	if len(got.Targets) != 2 {
		t.Fatalf("targets: %s", raw)
	}
	p.Notify.Targets["chan"].Warnings = true
	if raw, err = SectionConfig(p, "notify"); err != nil || !strings.Contains(string(raw), `"warnings":true`) {
		t.Fatalf("warnings on is not stored: %s %v", raw, err)
	}
	back := &policy.Policy{}
	if err := applySection(back, Section{Record: Record{Key: "notify"}, Config: raw}); err != nil {
		t.Fatal(err)
	}
	if !back.Notify.Targets["chan"].Warnings || back.Notify.Targets["hook"].Warnings {
		t.Fatalf("warnings read back wrong: %+v", back.Notify.Targets)
	}

	// A submitted section: warnings sent as false or null is dropped and nothing else is; warnings on, a section
	// without the field and one that is not a mapping come back as they were sent.
	for sent, want := range map[string]string{
		`{"retries":0,"budget":"","targets":{"a":{"native":false,"url":"","views":null,"warnings":false},"b":{"warnings":null,"events":[]}}}`: `{"budget":"","retries":0,"targets":{"a":{"native":false,"url":"","views":null},"b":{"events":[]}}}`,
		`{ "targets": {"a": {"warnings": true, "native": false}} }`:                                                                           `{ "targets": {"a": {"warnings": true, "native": false}} }`,
		`{ "retries": 5.0, "targets": {"a": {"native": false}} }`:                                                                             `{ "retries": 5.0, "targets": {"a": {"native": false}} }`,
		`{"targets":{"a":{"warnings":"maybe"}}}`:                                                                                              `{"targets":{"a":{"warnings":"maybe"}}}`,
		`{"targets":[{"warnings":false}]}`:                                                                                                    `{"targets":[{"warnings":false}]}`,
		`null`:                                                                                                                                `null`,
	} {
		if got := string(DropUnsetOptional("notify", json.RawMessage(sent))); got != want {
			t.Errorf("DropUnsetOptional(%s) = %s, want %s", sent, got, want)
		}
	}
}

// library.mount.decypharr is optional as a whole: the stored library section has the key only when the block is set.
// A submitted section without the key comes back as the bytes sent; one that names it as null loses the key but is
// encoded again, with its keys reordered, which is why a form leaves the key out rather than sending null.
func TestSectionConfigOmitsUnsetDecypharr(t *testing.T) {
	t.Parallel()
	p := &policy.Policy{Library: &policy.Library{Mount: policy.LibraryMount{Path: "/mnt/debrid"}}}
	raw, err := SectionConfig(p, "library")
	if err != nil || strings.Contains(string(raw), "decypharr") || !strings.Contains(string(raw), `"path":"/mnt/debrid"`) {
		t.Fatalf("unset: %s %v", raw, err)
	}
	p.Library.Mount.Decypharr = &policy.LibraryDecypharr{URL: "http://decypharr:8282", APITokenEnv: "THEATARR_DECYPHARR_TOKEN",
		Interval: policy.Duration(2 * time.Minute)}
	const block = `"decypharr":{"api_token_env":"THEATARR_DECYPHARR_TOKEN","budget":"","direct":false,"interval":"2m0s","password_env":"","url":"http://decypharr:8282","username_env":""}`
	if raw, err = SectionConfig(p, "library"); err != nil || !strings.Contains(string(raw), block) {
		t.Fatalf("set: %s %v", raw, err)
	}
	back := &policy.Policy{}
	if err := applySection(back, Section{Record: Record{Key: "library"}, Config: raw}); err != nil {
		t.Fatal(err)
	}
	if d := back.Library.Mount.Decypharr; d == nil || *d != *p.Library.Mount.Decypharr {
		t.Fatalf("read back: %+v", d)
	}
	again, err := SectionConfig(back, "library")
	if err != nil || string(again) != string(raw) {
		t.Fatalf("stored again: %s %v, want %s", again, err, raw)
	}
	paths, err := SectionSecretPaths("library", raw)
	if want := []string{"mount.decypharr.api_token", "mount.decypharr.password", "mount.decypharr.username"}; err != nil || !slices.Equal(paths, want) {
		t.Fatalf("credential paths %v %v, want %v", paths, err, want)
	}
	for sent, want := range map[string]string{
		`{"trash":{"keep_days":30},"mount":{"path":"/mnt/debrid","all_dir":"__all__"}}`:                                     `{"trash":{"keep_days":30},"mount":{"path":"/mnt/debrid","all_dir":"__all__"}}`,
		`{"trash":{"keep_days":30},"mount":{"path":"/mnt/debrid","decypharr":null,"all_dir":"__all__"}}`:                    `{"mount":{"all_dir":"__all__","path":"/mnt/debrid"},"trash":{"keep_days":30}}`,
		`{"trash":{"keep_days":30},"mount":{"path":"/mnt/debrid","decypharr":{"url":"http://d:8282"},"all_dir":"__all__"}}`: `{"trash":{"keep_days":30},"mount":{"path":"/mnt/debrid","decypharr":{"url":"http://d:8282"},"all_dir":"__all__"}}`,
	} {
		if got := string(DropUnsetOptional("library", json.RawMessage(sent))); got != want {
			t.Errorf("DropUnsetOptional(%s) = %s, want %s", sent, got, want)
		}
	}
}

// A stored shadow section with the six comparison fields stops the load (sections decode strictly); after migration
// 00122 the same row loads and keeps its RSS values.
func TestSectionShadowLoadsAfterComparisonDrop(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, base, _ := versionFixture(t)
	s.SetLookup(fixtureLookup)
	if _, err := s.BootstrapSwitches(ctx, base, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BootstrapSections(ctx, base, "test", nil); err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(database.DialectSQLite3, db.Writer(), os.DirFS("../store/migrations"),
		goose.WithTableName(store.MigrationTable), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 121); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().ExecContext(ctx, `UPDATE setting_section SET config = '{"import_interval":"10m",
		"rss_interval":"20m","rss":false,"search":true,"searches_per_hour":5,"search_reuse":"1h","keep_results":3,
		"link_window":"2h"}' WHERE key = 'shadow'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(ctx, base); err == nil {
		t.Fatal("a stored comparison field loaded")
	}
	if _, err := p.UpTo(ctx, 122); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Policy.ShadowSettings(); got.RSSInterval != 20*time.Minute || got.RSS {
		t.Fatalf("shadow section %+v", got)
	}
}
