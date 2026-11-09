package settings

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

func saveUnconfirmed(t *testing.T, m *RuntimeCoordinator, req SaveRequest) (*Snapshot, bool) {
	t.Helper()
	snap, err := m.SaveAndApply(context.Background(), req)
	var refused *ConfirmationError
	if errors.As(err, &refused) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	return snap, true
}

// Active version movie-web: anime false -> unset makes it apply to every anime movie too (planner.go:57).
func TestActiveVersionAnimeWidenNeedsConfirmation(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	req := saveRequest(p, active)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Anime = nil })
	if snap, ok := saveUnconfirmed(t, m, req); ok {
		t.Errorf("active version widened to anime titles without confirmation (gen %d, anime=%v)", snap.Generation, snap.Policy.Versions["movie-web"].Anime)
	}
}

// Profile propers do_not_upgrade -> prefer_and_upgrade and a lower min_upgrade_step widen upgrades of the active version.
func TestActiveProfileUpgradeKnobsNeedConfirmation(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	profile := active.Policy.Versions["movie-web"].Profile
	req := saveRequest(p, active)
	editProfile(t, &req.Records, profile, func(c *policy.Profile) { step := 500; c.MinUpgradeStep = &step; c.Propers = "do_not_upgrade" })
	narrowed, ok := saveUnconfirmed(t, m, req)
	if !ok {
		t.Fatal("narrowing asked for confirmation")
	}
	req = saveRequest(p, narrowed)
	editProfile(t, &req.Records, profile, func(c *policy.Profile) { step := 1; c.MinUpgradeStep = &step; c.Propers = "prefer_and_upgrade" })
	if _, ok := saveUnconfirmed(t, m, req); ok {
		t.Errorf("min_upgrade_step 500->1 and propers do_not_upgrade->prefer_and_upgrade on an active profile saved unconfirmed")
	}
}

// A manual format editor change of a negative-scored format's specification (BR-DISK/LQ) widens admission.
func TestActiveProfileFormatSpecEditNeedsConfirmation(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	req := saveRequest(p, active)
	var donor []byte
	for _, f := range req.Records.Formats {
		if f.Key == "HDR10+ Boost" {
			donor = append([]byte(nil), f.Specification...)
		}
	}
	changed := false
	for i := range req.Records.Formats {
		if req.Records.Formats[i].Key == "LQ" && donor != nil {
			req.Records.Formats[i].Specification = donor
			changed = true
		}
	}
	if !changed {
		var keys []string
		for _, f := range req.Records.Formats {
			keys = append(keys, f.Key)
		}
		t.Fatalf("no LQ/HDR10+ records: %v", keys)
	}
	if _, ok := saveUnconfirmed(t, m, req); ok {
		t.Errorf("LQ (-10000 in the active profile) respecified through the editor without confirmation")
	}
}

// Changing an active version's kind, or reordering its profile's qualities, needs confirmation; narrowing anime
// from unset and removing a quality do not.
func TestActiveVersionDenyByDefault(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	req := saveRequest(p, active)
	naming := false
	for i := range req.Records.Naming {
		if req.Records.Naming[i].Key == "movie-web" {
			req.Records.Naming[i].Config.Folder = "{Movie Title} [{Release Year}]"
			naming = true
		}
	}
	if !naming {
		req.Records.Naming = append(req.Records.Naming, VersionNaming{Record: Record{Key: "movie-web"}, Config: policy.Naming{Folder: "{Movie Title} [{Release Year}]"}})
	}
	_, err := m.SaveAndApply(context.Background(), req)
	requireConfirmation(t, err, "changes naming")
	profile := active.Policy.Versions["movie-web"].Profile
	// Three allowed qualities first (a confirmed widening), then a reorder.
	req = saveRequest(p, active)
	editProfile(t, &req.Records, profile, func(c *policy.Profile) {
		for _, name := range []string{"Bluray-1080p", "WEBDL-1080p", "HDTV-1080p"} {
			if c.Qualities[0].Name != name {
				c.Qualities = append(c.Qualities, policy.QualityItem{Name: name})
			}
		}
	})
	_, err = m.SaveAndApply(context.Background(), req)
	var refused *ConfirmationError
	if !errors.As(err, &refused) || refused.Confirmation == nil {
		t.Fatalf("adding qualities: %v", err)
	}
	req.ConfirmationToken = refused.Confirmation.Token
	active, err = m.SaveAndApply(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req = saveRequest(p, active)
	editProfile(t, &req.Records, profile, func(c *policy.Profile) {
		c.Qualities = append([]policy.QualityItem{c.Qualities[1], c.Qualities[0]}, c.Qualities[2:]...)
	})
	_, err = m.SaveAndApply(context.Background(), req)
	requireConfirmation(t, err, "changes qualities")

	req = saveRequest(p, active)
	editProfile(t, &req.Records, profile, func(c *policy.Profile) {
		c.Qualities = c.Qualities[:len(c.Qualities)-1]
	})
	if _, ok := saveUnconfirmed(t, m, req); !ok {
		t.Error("removing the last quality asked for confirmation")
	}
}

// A quality-sync review apply takes format definitions from its source without confirmation, unlike the editor.
func TestQualitySyncApplyDefinitionNeedsNoConfirmation(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	candidate := saveRequest(p, active).Records
	var donor []byte
	for _, f := range candidate.Formats {
		if f.Key == "HDR10+ Boost" {
			donor = append([]byte(nil), f.Specification...)
		}
	}
	for i := range candidate.Formats {
		if candidate.Formats[i].Key == "LQ" {
			candidate.Formats[i].Specification = donor
		}
	}
	raw, err := json.Marshal(Records{Profiles: candidate.Profiles, Formats: candidate.Formats, FormatSets: candidate.FormatSets, Naming: candidate.Naming})
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := m.store.db.Writer().QueryRow(`INSERT INTO settings_quality_review(source_key,base_generation,commit_id,preview,candidate,created_at) VALUES('guides',?,'content:x','{}',?,0) RETURNING id`,
		active.Generation, string(raw)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	svc := &QualitySync{Store: m.store, Coordinator: m}
	if _, err := svc.Apply(context.Background(), id, active.Generation, "sync:guides"); err != nil {
		t.Fatalf("definition change from the source: %v", err)
	}
}
