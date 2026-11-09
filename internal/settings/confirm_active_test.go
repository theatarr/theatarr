package settings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// Lowering (or removing) a positive format score on an active profile lowers existing files' scores, so
// they may fall below cutoff_score and be upgraded: confirmed like a raised cutoff_score.
func TestActiveProfileLowerScoreNeedsConfirmation(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	profile := active.Policy.Versions["movie-web"].Profile
	req := saveRequest(p, active)
	editProfile(t, &req.Records, profile, func(c *policy.Profile) { c.Formats["WEB Tier 01"] = -10000 })
	_, err := m.SaveAndApply(context.Background(), req)
	requireConfirmation(t, err, "lowers or removes format scores (WEB Tier 01)")

	req = saveRequest(p, active)
	editProfile(t, &req.Records, profile, func(c *policy.Profile) { delete(c.Formats, "HDR10+ Boost") })
	_, err = m.SaveAndApply(context.Background(), req)
	requireConfirmation(t, err, "lowers or removes format scores (HDR10+ Boost)")
}

// With no quality_order, a removed quality ranks below every allowed one, so removing one ranked above
// a kept quality makes its files upgradable. Removing the lowest stays unconfirmed (TestActiveVersionDenyByDefault).
func TestActiveProfileRemoveTopQualityNeedsConfirmation(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	ctx := context.Background()
	profile := active.Policy.Versions["movie-web"].Profile
	req := saveRequest(p, active)
	editProfile(t, &req.Records, profile, func(c *policy.Profile) {
		if len(c.QualityOrder) > 0 {
			t.Fatalf("fixture profile has a quality_order: %v", c.QualityOrder)
		}
		c.Qualities = append([]policy.QualityItem{{Name: "Remux-2160p"}}, c.Qualities...)
	})
	_, err := m.SaveAndApply(ctx, req)
	var refused *ConfirmationError
	if !errors.As(err, &refused) || refused.Confirmation == nil {
		t.Fatalf("adding a quality: %v", err)
	}
	req.ConfirmationToken = refused.Confirmation.Token
	wider, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req = saveRequest(p, wider)
	editProfile(t, &req.Records, profile, func(c *policy.Profile) { c.Qualities = c.Qualities[1:] })
	_, err = m.SaveAndApply(ctx, req)
	requireConfirmation(t, err, "removes qualities ranked above ones it keeps (Remux-2160p)")
}

// An enabled indexer an active version searches changing where its releases come from (url, kind,
// definition) is confirmed like one becoming usable. (Privacy to public is reported too; a debrid version cannot
// list a private indexer to begin with, so the fixture has no case for it.)
func TestActiveVersionIndexerSourceChangeNeedsConfirmation(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(*Indexer){
		"url": func(v *Indexer) { v.Config.URL = "https://elsewhere.invalid" },
	} {
		t.Run(name, func(t *testing.T) {
			m, p, active := activeWebFixture(t)
			req := saveRequest(p, active)
			found := false
			for _, ix := range req.Records.Indexers {
				found = found || (ix.Key == "torrentio" && ix.Enabled)
			}
			if !found {
				t.Fatal("fixture torrentio not enabled")
			}
			editIndexer(&req.Records, "torrentio", change)
			_, err := m.SaveAndApply(context.Background(), req)
			requireConfirmation(t, err, "Indexer torrentio, searched by active library movie-web, changes "+name)
		})
	}
}

// Deleting a version and re-creating its key with another root must not skip the refusal a root change
// gets while active links of the key stay at the old root.
func TestRecreatedVersionWithOtherRootChecksActiveLinks(t *testing.T) {
	t.Parallel()
	s, db, p, initial := versionFixture(t)
	ctx := context.Background()
	oldRoot := initial.Policy.Versions["movie-web"].Library
	for _, query := range []string{
		`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1002,'movie','Recreated',0,0)`,
		`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(1002,1002,'movie-web',0,0)`,
		`INSERT INTO link(id,path,target,title_id,created_at,updated_at) VALUES(1002,'` + filepath.Join(oldRoot, "Recreated (2020)", "Recreated.mkv") + `','fixture-target',1002,0,0)`,
		`INSERT INTO link_version(link_id,version_id) VALUES(1002,1002)`,
	} {
		if _, err := db.Writer().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for key, src := range p.Import.Arr {
		if src.Version == "movie-web" {
			delete(p.Import.Arr, key)
		}
	}
	req := saveRequest(p, initial)
	for i, v := range req.Records.Views {
		if v.Key == "radarr" {
			v.Versions = []string{"movie-remux", "anime-movie"}
			v.Profiles = nil
			req.Records.Views[i] = v
		}
	}
	var removed Version
	req.Records.Versions = slices.DeleteFunc(req.Records.Versions, func(v Version) bool {
		if v.Key == "movie-web" {
			removed = v
			return true
		}
		return false
	})
	if removed.Key == "" {
		t.Fatal("no movie-web record")
	}
	m := versionCoordinator(t, s, p, initial)
	proof, err := m.Confirm(ctx, req)
	if err != nil {
		t.Fatalf("confirm deletion: %v", err)
	}
	req.ConfirmationToken = proof.Token
	deleted, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatalf("delete version: %v", err)
	}
	newRoot := filepath.Join(p.Library.Dangling.Roots[0], "recreated-root")
	if err := os.Mkdir(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	req = saveRequest(p, deleted)
	removed.Config.Library = newRoot
	removed.Roots = []string{newRoot}
	req.Records.Versions = append(req.Records.Versions, removed)
	if _, err := m.Confirm(ctx, req); err == nil || !strings.Contains(err.Error(), "active link") {
		t.Fatalf("re-created with another root: %v", err)
	}
	// The same root again keeps its links where they are: allowed.
	req = saveRequest(p, deleted)
	removed.Config.Library = oldRoot
	removed.Roots = []string{oldRoot}
	req.Records.Versions = append(req.Records.Versions, removed)
	if _, err := m.Confirm(ctx, req); err != nil {
		t.Fatalf("re-created at its old root: %v", err)
	}
}
