package library_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/library/naming"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
	"go.yaml.in/yaml/v3"
)

func copyQualityFixture(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	err := filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(source, path)
		target := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSettingsQualityDecisionAndNamingEquivalence(t *testing.T) {
	t.Parallel()
	for _, fixture := range []string{"../../examples/policy", "testdata/recorded"} {
		t.Run(filepath.Base(filepath.Dir(fixture)), func(t *testing.T) {
			ctx := context.Background()
			dir := copyQualityFixture(t, fixture)
			before, err := policy.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			// Exercise a second library with a copy of the first one's naming alongside each fixture's explicit movie
			// and episode templates.
			keys := []string{}
			for key := range before.Versions {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			followed := keys[0]
			parallel := *before.Versions[followed]
			before.Versions["quality-naming-copy"] = &parallel
			parallel.Library = "/quality-naming-copy"
			if before.Library == nil {
				before.Library = &policy.Library{}
			}
			if before.Library.Versions == nil {
				before.Library.Versions = map[string]*policy.LibraryVersion{}
			}
			before.Library.Versions["quality-naming-copy"] = &policy.LibraryVersion{Roots: []string{"/quality-naming-copy"}}
			if len(before.Budgets) == 0 {
				before.Budgets = map[string]*policy.Budget{"fixture": {Requests: 1, Per: policy.Duration(time.Hour), Burst: 1}}
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if filepath.Ext(entry.Name()) == ".yaml" {
					if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
						t.Fatal(err)
					}
				}
			}
			doc, err := yaml.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "quality-copy.yaml"), doc, 0600); err != nil {
				t.Fatal(err)
			}
			before, err = policy.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			st, err := settings.New(db)
			if err != nil {
				t.Fatal(err)
			}
			trash := "../settings/testdata/trash"
			st.SetTrashRoot(trash)
			if _, err := st.Bootstrap(ctx, before, settings.ImportRequest{Dir: dir, Actor: "fixture", Lookup: func(string) (string, bool) { return "invented-quality-fixture", true }}); err != nil {
				t.Fatal(err)
			}
			snap, err := st.BootstrapQuality(ctx, before, "upgrade")
			if err != nil {
				t.Fatal(err)
			}
			after := snap.Policy
			if !reflect.DeepEqual(before.Profiles, after.Profiles) {
				t.Fatal("profile fields changed")
			}
			compareRef := func(a, b *policy.FormatRef) {
				t.Helper()
				aa, bb := *a, *b
				if aa.File != "" {
					raw, err := os.ReadFile(filepath.Join(dir, aa.File))
					if err != nil {
						t.Fatal(err)
					}
					var x, y any
					_ = json.Unmarshal(raw, &x)
					_ = json.Unmarshal(bb.Specification, &y)
					if !reflect.DeepEqual(x, y) {
						t.Fatal("file specification changed")
					}
				}
				aa.File, bb.File = "", ""
				aa.Specification, bb.Specification = nil, nil
				if !reflect.DeepEqual(aa, bb) {
					t.Fatal("format reference metadata changed")
				}
			}
			for key, ref := range before.Formats {
				compareRef(ref, after.Formats[key])
			}
			for key, set := range before.FormatSets {
				if set.Flavour != after.FormatSets[key].Flavour {
					t.Fatal("set flavour changed")
				}
				for name, ref := range set.Formats {
					compareRef(ref, after.FormatSets[key].Formats[name])
				}
			}
			now := func() time.Time { return time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC) }
			oldEngine, err := decide.New(before, decide.Options{TrashRoot: trash, Now: now})
			if err != nil {
				t.Fatal(err)
			}
			// Removing every JSON file proves the DB engine and naming path no longer consult the import directory.
			if err := os.RemoveAll(filepath.Join(dir, "formats")); err != nil {
				t.Fatal(err)
			}
			newEngine, err := decide.New(after, decide.Options{Now: now})
			if err != nil {
				t.Fatal(err)
			}
			for key, v := range before.Versions {
				if !reflect.DeepEqual(v.Naming, after.Versions[key].Naming) {
					t.Fatal("naming fields changed", key)
				}
				title := decide.Title{ID: 1, Kind: v.Kind, Name: "Dune Part Two", Year: 2024, TMDb: 693134, IMDb: "tt15239678", Runtime: 166, OriginalLanguage: "en", Monitored: true}
				releases := []string{"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HEVC.TrueHD.Atmos-FraMeSToR", "Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1.H.265-NTb", "Dune.Part.Two.2024.1080p.WEB-DL.x264-YIFY", "Dune.Part.Two.2024.2160p.WEB-DL.DV.H.265-Unknown", "unparseable"}
				quality := "WEBDL-2160p"
				if v.Kind == policy.KindSeries {
					title.Name = "Example Series"
					title.Year = 2020
					title.TVDB = 123
					title.Runtime = 45
					title.Episodes = []decide.Episode{{ID: 1, Season: 1, Number: 1, Absolute: 1, Monitored: true, AirDate: "2020-01-01"}}
					releases = []string{"Example.Series.S01E01.2160p.WEB-DL.DDP5.1.H.265-NTb", "Example.Series.S01E01.1080p.BluRay.x264-FraMeSToR", "Example.Series.S01E01.2160p.WEB-DL.x265-YIFY", "Example.Series.S01.2160p.WEB-DL.H265-NTb", "unparseable"}
				}
				oldV, newV := decide.Version{Key: key, Policy: v}, decide.Version{Key: key, Policy: after.Versions[key]}
				existing := decide.ExistingFile{Quality: quality, Revision: 1, SceneName: releases[1], Languages: []string{"English"}, Size: 20 << 30}
				oldCut, err := oldEngine.FileCutoffNotMet(&title, oldV, &existing)
				if err != nil {
					t.Fatal(err)
				}
				newCut, err := newEngine.FileCutoffNotMet(&title, newV, &existing)
				if err != nil || oldCut != newCut {
					t.Fatal("cutoff changed", key, err)
				}
				a, b := existing, existing
				if err := oldEngine.ScoreFile(&title, oldV, &a); err != nil {
					t.Fatal(err)
				}
				if err := newEngine.ScoreFile(&title, newV, &b); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(a.Score, b.Score) || !reflect.DeepEqual(a.Formats, b.Formats) {
					t.Fatal("held score changed", key)
				}
				var oldDecisions, newDecisions []*decide.Decision
				for _, release := range releases {
					r := decide.Release{Release: indexer.Release{Title: release, Indexer: v.Indexers[0], Privacy: policy.PrivacyPublic, Size: 30 << 30, Seeders: 20}}
					for _, held := range []*decide.ExistingFile{nil, &existing} {
						a, err := oldEngine.Decide(ctx, r, title, oldV, held)
						if err != nil {
							t.Fatal(err)
						}
						b, err := newEngine.Decide(ctx, r, title, newV, held)
						if err != nil {
							t.Fatal(err)
						}
						aj, _ := json.Marshal(a)
						bj, _ := json.Marshal(b)
						if string(aj) != string(bj) {
							t.Fatalf("decision changed %s %s\n%s\n%s", key, release, aj, bj)
						}
						oldDecisions = append(oldDecisions, a)
						newDecisions = append(newDecisions, b)
					}
					incoming := existing
					incoming.SceneName = release
					incoming.Revision = 2
					a, whyA, errA := oldEngine.ImportUpgrade(&title, oldV, &incoming, &existing)
					b, whyB, errB := newEngine.ImportUpgrade(&title, newV, &incoming, &existing)
					if a != b || whyA != whyB || !reflect.DeepEqual(errA, errB) {
						t.Fatal("upgrade changed", key)
					}
				}
				aj, _ := json.Marshal(decide.Rank(oldDecisions))
				bj, _ := json.Marshal(decide.Rank(newDecisions))
				if string(aj) != string(bj) {
					t.Fatal("ranking changed", key)
				}
				oldName, err := library.NamingFor(before, key)
				if err != nil {
					t.Fatal(err)
				}
				newName, err := library.NamingFor(after, key)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(oldName, newName) {
					t.Fatal("resolved naming changed", key)
				}
				if v.Kind == policy.KindMovie {
					m := naming.Movie{Title: title.Name, Year: title.Year}
					f := naming.MovieFile{Quality: naming.Quality{Title: "WEBDL-2160p", Version: 1}, CustomFormats: []string{"WEB Tier 01"}}
					a, ea := naming.MovieFileName(&oldName.Config, &m, &f)
					b, eb := naming.MovieFileName(&newName.Config, &m, &f)
					fa, efa := naming.MovieFolder(&oldName.Config, &m)
					fb, efb := naming.MovieFolder(&newName.Config, &m)
					if a != b || !reflect.DeepEqual(ea, eb) || fa != fb || efa != nil || efb != nil {
						t.Fatal("movie name changed")
					}
				} else {
					s := naming.Series{Title: title.Name, Year: title.Year}
					eps := []naming.Episode{{Season: 1, Number: 1, Title: "Pilot"}, {Season: 1, Number: 2, Title: "Next"}}
					f := naming.EpisodeFile{Quality: naming.Quality{Title: "WEBDL-2160p", Version: 1}}
					a, ea := naming.SeriesFileName(&oldName.Config, &s, eps, &f, ".mkv", "")
					b, eb := naming.SeriesFileName(&newName.Config, &s, eps, &f, ".mkv", "")
					fa, efa := naming.SeriesFolder(&oldName.Config, &s)
					fb, efb := naming.SeriesFolder(&newName.Config, &s)
					if a != b || !reflect.DeepEqual(ea, eb) || fa != fb || efa != nil || efb != nil {
						t.Fatal("episode name changed")
					}
				}
			}
		})
	}
}

// TestDetachedFollowerKeepsNaming: rows as the settings store writes them (a naming row is policy.Naming as JSON, Go
// field names at the top level; a library row is its YAML fields under config), given the old follower shape (a
// naming row with FromVersion, a library row with follows), migrated by 00121 and loaded: each former follower names
// its files as the library it followed did, for templates, empty templates and no naming row at all.
func TestDetachedFollowerKeepsNaming(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := copyQualityFixture(t, "../../examples/policy")
	pol, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	st.SetTrashRoot("../settings/testdata/trash")
	if _, err := st.Bootstrap(ctx, pol, settings.ImportRequest{Dir: dir, Actor: "fixture",
		Lookup: func(string) (string, bool) { return "invented-quality-fixture", true }}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BootstrapQuality(ctx, pol, "upgrade"); err != nil {
		t.Fatal(err)
	}
	one := func(q string, args ...any) string {
		t.Helper()
		var s string
		if err := db.Reader().QueryRowContext(ctx, q, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return s
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Writer().ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// The key names the migration reads: FromVersion sat beside Folder, follows beside mode.
	if got := one(`SELECT json_type(config, '$.Folder') || ' ' || json_extract(config, '$.Folder')
		FROM setting_version_naming WHERE key = 'movie-remux'`); got != "text "+pol.Versions["movie-remux"].Naming.Folder {
		t.Fatalf("naming row: %s", got)
	}
	if got := one(`SELECT json_extract(config, '$.config.mode') || ' ' || json_type(config, '$.roots')
		FROM setting_version WHERE key = 'movie-remux'`); got != "shadow array" {
		t.Fatalf("library row: %s", got)
	}

	p, err := goose.NewProvider(database.DialectSQLite3, db.Writer(), os.DirFS("../store/migrations"),
		goose.WithTableName(store.MigrationTable), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 120); err != nil {
		t.Fatal(err)
	}
	// movie-remux: folder and file templates, the rest empty. tv-2160p: its own templates. anime-movie: no naming row.
	exec(`DELETE FROM setting_version_naming WHERE key = 'anime-movie'`)
	followed := []string{"movie-remux", "movie-web", "tv-2160p", "anime-movie"}
	for _, f := range followed {
		k := f + "-copy"
		exec(`INSERT INTO setting_version (key, config, created_at, updated_at) SELECT ?, json_set(config,
			'$.config.follows', key, '$.config.library', '/copies/' || key, '$.roots', json_array('/copies/' || key)), 1, 1
			FROM setting_version WHERE key = ?`, k, f)
		exec(`INSERT INTO setting_version_naming (key, config, created_at, updated_at) VALUES (?, json_object('Folder', '',
			'File', '', 'Season', '', 'Specials', '', 'Daily', '', 'Anime', '', 'Colon', '', 'MultiEpisode', '', 'FromArr', '',
			'FromVersion', ?), 1, 1)`, k, f)
	}
	if _, err := p.UpTo(ctx, 121); err != nil {
		t.Fatal(err)
	}
	if n := one(`SELECT COUNT(*) FROM setting_version_naming WHERE json_type(config, '$.FromVersion') IS NOT NULL`) +
		one(`SELECT COUNT(*) FROM setting_version WHERE json_type(config, '$.config.follows') IS NOT NULL`); n != "00" {
		t.Fatalf("FromVersion and follows left: %s", n)
	}
	snap, err := st.Load(ctx, pol)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range followed {
		want, err := library.NamingFor(snap.Policy, f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := library.NamingFor(snap.Policy, f+"-copy")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Config, want.Config) ||
			got.Source != want.Source {
			t.Errorf("%s-copy names as %+v (%s), %s as %+v (%s)", f, got, got.Source, f, want, want.Source)
		}
	}
}
