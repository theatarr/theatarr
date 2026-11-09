package settings_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/golive"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
	"go.yaml.in/yaml/v3"
)

func TestVersionsPolicyRuntimeEquivalence(t *testing.T) {
	t.Parallel()
	for _, live := range []bool{false, true} {
		name := "examples"
		if live {
			name = "household"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			p, err := policy.Load("../../examples/policy")
			if err != nil {
				t.Fatal(err)
			}
			if live {
				keys := make([]string, 0, len(p.Versions))
				for key := range p.Versions {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					v := *p.Versions[key]
					v.Mode = policy.ModeActive
					v.Follows = key
					v.Library += "-theatarr"
					v.Naming = policy.Naming{FromVersion: key}
					p.Versions[key+"-theatarr"] = &v
					p.Library.Versions[key+"-theatarr"] = &policy.LibraryVersion{Roots: []string{v.Library}}
				}
				for _, view := range p.Views {
					old := slices.Clone(view.Versions)
					for _, key := range old {
						view.Versions = append(view.Versions, key+"-theatarr")
					}
				}
				dir := t.TempDir()
				if err := filepath.Walk(p.Dir, func(path string, info os.FileInfo, err error) error {
					if err != nil {
						return err
					}
					rel, err := filepath.Rel(p.Dir, path)
					if err != nil {
						return err
					}
					if info.IsDir() {
						return os.MkdirAll(filepath.Join(dir, rel), 0700)
					}
					if filepath.Dir(rel) == "." {
						return nil
					}
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(dir, rel), data, 0600)
				}); err != nil {
					t.Fatal(err)
				}
				b, err := yaml.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(dir, "policy.yaml"), b, 0600); err != nil {
					t.Fatal(err)
				}
				p, err = policy.Load(dir)
				if err != nil {
					t.Fatal(err)
				}
			}
			db, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			st, err := settings.New(db)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.Bootstrap(ctx, p, settings.ImportRequest{Dir: p.Dir, Actor: "test", Lookup: func(string) (string, bool) { return "fixture", true }}); err != nil {
				t.Fatal(err)
			}
			snap, err := st.Load(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			got := snap.Policy
			if !reflect.DeepEqual(p.Versions, got.Versions) || !reflect.DeepEqual(p.Views, got.Views) || !reflect.DeepEqual(p.Library, got.Library) {
				t.Fatal("version records round-trip changed policy")
			}
			av, err := arrview.BuildViews(p)
			if err != nil {
				t.Fatal(err)
			}
			bv, err := arrview.BuildViews(got)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(av, bv) {
				t.Fatal("arr routes/bindings differ")
			}
			qa, qb := jobs.NewQueue(db), jobs.NewQueue(db)
			ra, err := router.New(router.Config{Store: db, Policy: p, Queue: qa})
			if err != nil {
				t.Fatal(err)
			}
			rb, err := router.New(router.Config{Store: db, Policy: got, Queue: qb})
			if err != nil {
				t.Fatal(err)
			}
			search := func(context.Context, decide.Title, decide.Version, *decide.ExistingFile, *decide.SeriesSearch) (*router.Searched, error) {
				t.Fatal("equivalence test searched")
				return nil, nil
			}
			aa, err := acquire.New(acquire.Config{Store: db, Policy: p, Queue: qa, Router: ra, Search: search})
			if err != nil {
				t.Fatal(err)
			}
			ab, err := acquire.New(acquire.Config{Store: db, Policy: got, Queue: qb, Router: rb, Search: search})
			if err != nil {
				t.Fatal(err)
			}
			ga, gb := library.NewGate(p), library.NewGate(got)
			keys := make([]string, 0, len(p.Versions))
			for key := range p.Versions {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			if !reflect.DeepEqual(aa.Active(keys), ab.Active(keys)) {
				t.Fatal("acquirer differs")
			}
			for _, key := range keys {
				ma, ea := ra.Gate(key)
				mb, eb := rb.Gate(key)
				if ma != mb || errString(ea) != errString(eb) {
					t.Fatalf("router gate differs for %s", key)
				}
				if ga.Active(key) != gb.Active(key) || !reflect.DeepEqual(ga.Roots(key), gb.Roots(key)) || errString(ga.CheckVersion(key)) != errString(gb.CheckVersion(key)) {
					t.Fatalf("library gate differs for %s", key)
				}
				opts := golive.Options{Policy: p, Version: key, Now: time.Unix(1800000000, 0)}
				ar, err := golive.Run(ctx, opts)
				if err != nil {
					t.Fatal(err)
				}
				opts.Policy = got
				br, err := golive.Run(ctx, opts)
				if err != nil {
					t.Fatal(err)
				}
				ar.PolicyRevision = ""
				br.PolicyRevision = ""
				if !reflect.DeepEqual(ar, br) {
					t.Fatalf("golive differs for %s", key)
				}
			}
		})
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
