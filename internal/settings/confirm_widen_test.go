package settings

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
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

// At the policy level: follows added on an active version asks; a profile, its cutoff or the indexers changed do not.
func TestAddFollowsOnActiveVersion(t *testing.T) {
	t.Parallel()
	_, db, _, initial := versionFixture(t)
	ctx := context.Background()
	before, _ := initial.Policy.Clone()
	before.Versions["movie-web"].Mode = policy.ModeActive
	before.Versions["movie-web"].Profile = "remux-2160p"
	before.Profiles["remux-2160p"].Cutoff = "WEB 2160p"
	for _, tc := range []struct {
		name, contains string
		change         func(*policy.Policy)
	}{
		{"add-follows", "starts following movie-remux", func(p *policy.Policy) { p.Versions["movie-web"].Follows = "movie-remux" }},
		{"profile", "", func(p *policy.Policy) { p.Versions["movie-web"].Profile = "web-2160p" }},
		{"indexers", "", func(p *policy.Policy) {
			p.Versions["movie-web"].Indexers = append(p.Versions["movie-web"].Indexers, "seadex")
		}},
		{"cutoff", "", func(p *policy.Policy) { p.Profiles["remux-2160p"].Cutoff = "Remux-2160p" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next, _ := before.Clone()
			tc.change(next)
			effects, err := versionConfirmationEffects(ctx, db.Reader(), before, next)
			if err != nil {
				t.Fatal(err)
			}
			if tc.contains == "" {
				if len(effects) != 0 {
					t.Fatalf("%s asked for confirmation: %+v", tc.name, effects)
				}
				return
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

// liveGrabFixture publishes movie-web Live with downloads on (confirmed), with quality records, the database-owned
// sections and the VPN guard on.
func liveGrabFixture(t *testing.T) (*RuntimeCoordinator, *policy.Policy, *Snapshot) {
	t.Helper()
	ctx := context.Background()
	s, base, _ := sectionsFixture(t, vpnGuardOn)
	initial, err := s.BootstrapQuality(ctx, base, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	m := versionCoordinator(t, s, base, initial)
	req := saveRequest(base, initial)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Mode = policy.ModeActive; v.Config.Grab = true })
	proof, err := m.Confirm(ctx, req)
	if err != nil || proof.Token == "" {
		t.Fatalf("confirm going Live: %+v %v", proof, err)
	}
	req.ConfirmationToken = proof.Token
	live, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	return m, base, live
}

// Everyday edits of a Live library with downloads on, and of what it uses, save at once without a token.
func TestEverydayLiveEditsSaveDirectly(t *testing.T) {
	t.Parallel()
	m, p, live := liveGrabFixture(t)
	profile := live.Policy.Versions["movie-web"].Profile
	newRoot := filepath.Join(filepath.Dir(live.Policy.Versions["movie-web"].Library), "movie-web-moved")
	if err := os.Mkdir(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*SaveRequest, *Snapshot)
	}{
		{"profile swap", func(req *SaveRequest, _ *Snapshot) {
			editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Profile = "remux-2160p" })
		}},
		{"profile swap back", func(req *SaveRequest, _ *Snapshot) {
			editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Profile = profile })
		}},
		{"profile cutoff, score and qualities", func(req *SaveRequest, _ *Snapshot) {
			editProfile(t, &req.Records, profile, func(c *policy.Profile) {
				c.Formats["WEB Tier 01"] = -10000
				c.Qualities = append([]policy.QualityItem{{Name: "Remux-2160p"}}, c.Qualities...)
				c.Cutoff = "Remux-2160p"
			})
		}},
		{"format specification", func(req *SaveRequest, _ *Snapshot) {
			var donor []byte
			for _, f := range req.Records.Formats {
				if f.Key == "HDR10+ Boost" {
					donor = append([]byte(nil), f.Specification...)
				}
			}
			for i := range req.Records.Formats {
				if req.Records.Formats[i].Key == "LQ" && donor != nil {
					req.Records.Formats[i].Specification = donor
					return
				}
			}
			t.Fatal("no LQ/HDR10+ Boost format records")
		}},
		{"naming", func(req *SaveRequest, _ *Snapshot) {
			for i := range req.Records.Naming {
				if req.Records.Naming[i].Key == "movie-web" {
					req.Records.Naming[i].Config.Folder = "{Movie Title} [{Release Year}]"
					return
				}
			}
			req.Records.Naming = append(req.Records.Naming, VersionNaming{Record: Record{Key: "movie-web"}, Config: policy.Naming{Folder: "{Movie Title} [{Release Year}]"}})
		}},
		{"indexers list", func(req *SaveRequest, _ *Snapshot) {
			editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Indexers = []string{"dmm", "torrentio"} })
		}},
		{"providers list", func(req *SaveRequest, _ *Snapshot) {
			editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Providers = []string{"rd"} })
		}},
		{"providers added", func(req *SaveRequest, _ *Snapshot) {
			editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Providers = []string{"rd", "ad", "tb"} })
		}},
		{"root without links", func(req *SaveRequest, _ *Snapshot) {
			editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Library = newRoot; v.Roots = []string{newRoot} })
		}},
		{"egress guard off", func(req *SaveRequest, snap *Snapshot) {
			req.Records.Sections = []Section{editSection(t, snap, "egress", func(c map[string]any) { c["require_vpn_for"] = []string{} })}
		}},
		{"cached_only off", func(req *SaveRequest, snap *Snapshot) {
			req.Records.Sections = []Section{editSection(t, snap, "router", func(c map[string]any) { c["cached_only"] = false })}
		}},
		{"view create", func(req *SaveRequest, _ *Snapshot) {
			req.Records.Views = append(req.Records.Views, ViewBinding{Record: Record{Key: "radarr2"}, Versions: []string{"movie-web"},
				Identity: &ViewIdentity{Flavour: "radarr", Path: "/radarr2"}})
			req.SecretWrites = map[string]Secret{"views/radarr2": NewSecret("invented-view-key-radarr2")}
		}},
		{"notify on", func(req *SaveRequest, _ *Snapshot) { req.Records.Switches.NotifyEnabled = true }},
	} {
		req := saveRequest(p, live)
		tc.change(&req, live)
		saved, err := m.SaveAndApply(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		live = saved
	}
	v := live.Policy.Versions["movie-web"]
	if v.Library != newRoot || live.Policy.Views["radarr2"] == nil || !live.Policy.Notify.Enabled || live.Policy.RouterSettings().CachedOnly {
		t.Fatalf("edits not applied: root %s", v.Library)
	}
}

// Going Live, a new Live library, downloads on and Pause off still refuse without a token and save with the token the
// refusal gave.
func TestKeptConfirmations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, p, live := liveGrabFixture(t)
	newRoot := filepath.Join(filepath.Dir(live.Policy.Versions["movie-web"].Library), "movie-new")
	if err := os.Mkdir(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, effect string
		change       func(*SaveRequest)
	}{
		{"watch only to Live", "Library movie-remux becomes Live", func(req *SaveRequest) {
			editVersion(&req.Records, "movie-remux", func(v *Version) { v.Config.Mode = policy.ModeActive })
		}},
		{"new Live library", "Library movie-new becomes Live", func(req *SaveRequest) {
			for _, v := range req.Records.Versions {
				if v.Key == "movie-web" {
					v.Record = Record{Key: "movie-new"}
					v.Config.Library, v.Roots, v.Config.Grab = newRoot, []string{newRoot}, false
					req.Records.Versions = append(req.Records.Versions, v)
					return
				}
			}
		}},
		{"downloads on", "Downloads are on for Live library movie-new", func(req *SaveRequest) {
			editVersion(&req.Records, "movie-new", func(v *Version) { v.Config.Grab = true })
		}},
		{"router on", "(router.enabled): ", func(req *SaveRequest) { req.Records.Switches.RouterEnabled = true }},
		{"library on", "(library.enabled): ", func(req *SaveRequest) { req.Records.Switches.LibraryEnabled = true }},
	} {
		req := saveRequest(p, live)
		tc.change(&req)
		_, err := m.SaveAndApply(ctx, req)
		var refused *ConfirmationError
		if !errors.As(err, &refused) || refused.Confirmation == nil {
			t.Fatalf("%s: saved without confirmation: %v", tc.name, err)
		}
		requireConfirmation(t, err, tc.effect)
		req.ConfirmationToken = refused.Confirmation.Token
		saved, err := m.SaveAndApply(ctx, req)
		if err != nil {
			t.Fatalf("%s: confirmed save: %v", tc.name, err)
		}
		live = saved
	}

	// Removing a library that has links asks too.
	_, db, _, initial := versionFixture(t)
	if _, err := db.Writer().Exec(`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Linked',0,0);
		INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(1,1,'movie-web',0,0);
		INSERT INTO link(id,path,target,title_id,created_at,updated_at) VALUES(1,'/x/Linked.mkv','fixture-target',1,0,0);
		INSERT INTO link_version(link_id,version_id) VALUES(1,1)`); err != nil {
		t.Fatal(err)
	}
	next, _ := initial.Policy.Clone()
	delete(next.Versions, "movie-web")
	effects, err := versionConfirmationEffects(ctx, db.Reader(), initial.Policy, next)
	if err != nil || len(effects) != 1 || !strings.Contains(effects[0].Effect, "Library movie-web stops being managed. 1 linked item(s)") {
		t.Fatalf("removal effects: %+v %v", effects, err)
	}
}
