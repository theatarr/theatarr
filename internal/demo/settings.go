package demo

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/settings"
)

//go:embed formats
var demoFormats embed.FS

// The demo carries the example policy's format specifications for offline seeding.
func demoTrash() (string, error) {
	dir, err := os.MkdirTemp("", "theatarr-demo-formats-")
	if err != nil {
		return "", err
	}
	err = fs.WalkDir(demoFormats, "formats", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel("formats", path)
		if err != nil {
			return err
		}
		dest := filepath.Join(dir, rel)
		if entry.IsDir() {
			return os.MkdirAll(dest, 0700)
		}
		data, err := demoFormats.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0600)
	})
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// DemoCredential is the invented credential every demo source gets for the policy's *_env name: theatarr serve runs on
// database settings and its first start imports the policy's sources with their
// credentials, so a demo database is seeded with that import already done. Public, like Password.
func DemoCredential(envName string) string {
	return "demo-only-" + strings.ToLower(strings.ReplaceAll(envName, "_", "-"))
}

// settings seeds the settings database (docs/design/settings.md): the first-start import of the policy's indexers,
// providers and budgets with invented credentials, then a few admin edits so the settings pages have something to
// show: a disabled indexer whose credential was cleared, a changed priority, a changed budget cap and two source
// tests in the audit. Providers are left as imported (their order must agree with the versions' provider lists).
// Times are anchored at the seed's now, so the same seed gives the same rows.
func (s *seeder) settings() error {
	// The import wants an absolute overrides path; change a copy, the caller's policy may be shared.
	pol := s.pol
	if pol.Metadata != nil && pol.Metadata.OverridesDir != "" {
		cp, md := *pol, *pol.Metadata
		md.OverridesDir = pol.OverridesPath()
		cp.Metadata = &md
		pol = &cp
	}
	st, err := settings.New(s.st)
	if err != nil {
		return err
	}
	at := s.ago(days(9))
	st.SetClock(func() time.Time { return at })
	lookup := func(name string) (string, bool) { return DemoCredential(name), true }
	_, err = st.Bootstrap(s.ctx, pol, settings.ImportRequest{Dir: pol.Dir, Actor: "serve", Lookup: lookup})
	if err != nil {
		return fmt.Errorf("demo: settings import: %w", err)
	}

	trash, err := demoTrash()
	if err != nil {
		return err
	}
	defer os.RemoveAll(trash)
	st.SetTrashRoot(trash)
	if _, err = st.BootstrapQuality(s.ctx, pol, "demo"); err != nil {
		return err
	}
	if _, err = st.BootstrapSwitches(s.ctx, pol, "demo", nil); err != nil {
		return err
	}
	snap, err := st.BootstrapSections(s.ctx, pol, "demo", nil)
	if err != nil {
		return err
	}

	at = s.ago(days(6) + 3*time.Hour)
	records := clone(snap.Records)
	writes := map[string]settings.Secret{}
	for i := range records.Indexers {
		switch records.Indexers[i].Key {
		case "tracker-example":
			records.Indexers[i].Enabled = false
			writes["indexers/tracker-example"] = settings.NewSecret("")
		case "zilean":
			records.Indexers[i].Priority = 10
		}
	}
	if snap, err = st.Save(s.ctx, settings.SaveRequest{ExpectedGeneration: snap.Generation, Actor: Username, Base: pol,
		Records: records, SecretWrites: writes}); err != nil {
		return fmt.Errorf("demo: settings edit: %w", err)
	}

	at = s.ago(days(2) + 5*time.Hour)
	records = clone(snap.Records)
	for i := range records.Budgets {
		if records.Budgets[i].Key == "seadex" {
			records.Budgets[i].Config.Hourly = 500
		}
	}
	if _, err = st.Save(s.ctx, settings.SaveRequest{ExpectedGeneration: snap.Generation, Actor: Username, Base: pol,
		Records: records}); err != nil {
		return fmt.Errorf("demo: settings edit: %w", err)
	}

	at = s.ago(days(1) + 2*time.Hour)
	if err := st.RecordAction(s.ctx, Username, "providers", "rd", "test", map[string]any{"status": "ok"}); err != nil {
		return err
	}
	at = s.ago(days(1) + time.Hour)
	return st.RecordAction(s.ctx, Username, "indexers", "dmm", "test", map[string]any{"status": "failed"})
}

func clone(in settings.Records) settings.Records {
	return settings.Records{Indexers: append([]settings.Indexer(nil), in.Indexers...),
		Providers: append([]settings.Provider(nil), in.Providers...), Budgets: append([]settings.Budget(nil), in.Budgets...),
		Versions: append([]settings.Version(nil), in.Versions...), Views: append([]settings.ViewBinding(nil), in.Views...),
		Profiles: append(in.Profiles[:0:0], in.Profiles...), Formats: append(in.Formats[:0:0], in.Formats...),
		FormatSets: append(in.FormatSets[:0:0], in.FormatSets...), Naming: append(in.Naming[:0:0], in.Naming...),
		SyncSources: append(in.SyncSources[:0:0], in.SyncSources...),
		Sections:    append(in.Sections[:0:0], in.Sections...), Switches: in.Switches}
}
