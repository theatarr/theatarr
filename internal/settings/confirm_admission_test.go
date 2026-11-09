package settings

import (
	"context"
	"errors"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

func editIndexer(records *Records, key string, change func(*Indexer)) {
	for i := range records.Indexers {
		if records.Indexers[i].Key == key {
			change(&records.Indexers[i])
		}
	}
}

// Review fix: an indexer an active version already lists becoming usable (enabled; given its key) adds a searcher,
// so its results can reach the router: confirmed like a provider becoming usable.
func TestActiveVersionIndexerBecomesUsable(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	ctx := context.Background()
	req := saveRequest(p, active)
	editIndexer(&req.Records, "torrentio", func(v *Indexer) { v.Enabled = false })
	disabled, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatalf("disabling is narrowing: %v", err)
	}
	req = saveRequest(p, disabled)
	editIndexer(&req.Records, "torrentio", func(v *Indexer) { v.Enabled = true })
	_, err = m.SaveAndApply(ctx, req)
	requireConfirmation(t, err, "Indexer torrentio becomes usable")

	req = saveRequest(p, disabled)
	req.SecretWrites = map[string]Secret{"indexers/dmm": NewSecret("")}
	keyless, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatalf("clearing a key is narrowing: %v", err)
	}
	req = saveRequest(p, keyless)
	req.SecretWrites = map[string]Secret{"indexers/dmm": NewSecret("invented-new-dmm-key")}
	_, err = m.SaveAndApply(ctx, req)
	requireConfirmation(t, err, "Indexer dmm becomes usable")

	// An indexer no active version lists needs nothing.
	req = saveRequest(p, keyless)
	editIndexer(&req.Records, "seadex", func(v *Indexer) { v.Enabled = false })
	off, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req = saveRequest(p, off)
	editIndexer(&req.Records, "seadex", func(v *Indexer) { v.Enabled = true })
	if _, err := m.SaveAndApply(ctx, req); err != nil {
		t.Fatalf("an unlisted indexer needed confirmation: %v", err)
	}
}

// Review fix: a profile change that admits releases the active version's profile rejected before (a lower
// min_score, another allowed quality, a higher format score, a wider size limit, another language) is confirmed;
// the opposite change is narrowing and saves.
func TestActiveVersionProfileAdmissionWidening(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, contains string
		narrow, widen  func(*policy.Profile)
		// narrowConfirms is the effect the narrowing step itself must be confirmed with, if any.
		narrowConfirms string
	}{
		{"min_score", "lowers min_score", func(c *policy.Profile) { c.MinScore = 500 }, func(c *policy.Profile) { c.MinScore = 0 }, ""},
		{"qualities", "allows more qualities", func(c *policy.Profile) {
			c.Qualities[0].Qualities = []string{"WEBDL-2160p"}
		}, func(c *policy.Profile) { c.Qualities[0].Qualities = []string{"WEBDL-2160p", "WEBRip-2160p"} },
			// Dropping WEBRip-2160p from the only group admits less, but with no quality_order
			// held WEBRip files then rank below WEBDL-2160p and become upgradable: confirmed.
			"removes quality group members (WEBRip-2160p)"},
		{"score", "raises format scores", func(c *policy.Profile) { c.Formats["LQ"] = -10000 }, func(c *policy.Profile) { c.Formats["LQ"] = 0 }, ""},
		{"sizes", "widens size limits", func(c *policy.Profile) {
			c.Sizes = map[string]policy.SizeLimit{"WEBDL-2160p": {Min: 10, Max: 100}}
		}, func(c *policy.Profile) { c.Sizes = map[string]policy.SizeLimit{"WEBDL-2160p": {Min: 10, Max: 200}} }, ""},
		{"language", "changes language", func(c *policy.Profile) { c.Language = "original" }, func(c *policy.Profile) { c.Language = "any" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, p, active := activeWebFixture(t)
			ctx := context.Background()
			profile := active.Policy.Versions["movie-web"].Profile
			req := saveRequest(p, active)
			editProfile(t, &req.Records, profile, tc.narrow)
			narrowed, err := m.SaveAndApply(ctx, req)
			if tc.narrowConfirms != "" {
				requireConfirmation(t, err, tc.narrowConfirms)
			}
			if refused := (*ConfirmationError)(nil); errors.As(err, &refused) && (tc.name == "sizes" || tc.narrowConfirms != "") {
				// A first size limit replaces the quality's default, which may be narrower: confirmed too.
				req.ConfirmationToken = refused.Confirmation.Token
				narrowed, err = m.SaveAndApply(ctx, req)
			}
			if err != nil {
				t.Fatalf("narrowing needed confirmation: %v", err)
			}
			req = saveRequest(p, narrowed)
			editProfile(t, &req.Records, profile, tc.widen)
			_, err = m.SaveAndApply(ctx, req)
			requireConfirmation(t, err, tc.contains)
		})
	}
}

// Review fix: binding an active version to a view (its versions or one of its profiles) lets arr clients add titles
// to it and search: confirmed.
func TestViewBindingExposesActiveVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		bind func(*ViewBinding)
	}{
		{"versions", func(v *ViewBinding) { v.Versions = append(v.Versions, "movie-web") }},
		{"profile", func(v *ViewBinding) {
			v.Versions = append(v.Versions, "movie-web")
			v.Profiles["WEB only"] = []string{"movie-web"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, p, active := activeWebFixture(t)
			ctx := context.Background()
			req := saveRequest(p, active)
			for i := range req.Records.Views {
				if req.Records.Views[i].Key == "radarr" {
					req.Records.Views[i].Versions = []string{"movie-remux", "anime-movie"}
					req.Records.Views[i].Profiles = map[string][]string{"Anime": {"anime-movie"}, "Rules default": {"movie-remux"}}
				}
			}
			unbound, err := m.SaveAndApply(ctx, req)
			if err != nil {
				t.Fatalf("unbinding is narrowing: %v", err)
			}
			req = saveRequest(p, unbound)
			for i := range req.Records.Views {
				if req.Records.Views[i].Key == "radarr" {
					tc.bind(&req.Records.Views[i])
				}
			}
			_, err = m.SaveAndApply(ctx, req)
			requireConfirmation(t, err, "through view radarr")
		})
	}
}
