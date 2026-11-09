package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer/cardigann"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func TestCardigannCatalogOffWithoutDefinitions(t *testing.T) {
	t.Parallel()
	c, err := newCardigannCatalog(&policy.Policy{}, t.TempDir(), nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if c != nil || err != nil {
		t.Fatalf("catalog %v %v", c, err)
	}
}

// The migration reader reads Prowlarr's indexer list with a GET and proposes; the catalog lives in the data dir.
func TestCardigannCatalogProwlarrProposals(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(dir, "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		w.Write([]byte(`[{"id":1,"name":"X","definitionName":"unknownsite","implementation":"Cardigann","protocol":"torrent","privacy":"public"}]`))
	}))
	defer srv.Close()
	t.Setenv("THEATARR_TEST_PROWLARR_KEY", "invented-key")
	pol := &policy.Policy{Cardigann: &policy.Cardigann{Definitions: &policy.CardigannDefinitions{},
		Prowlarr: &policy.CardigannProwlarr{URL: srv.URL, APIKeyEnv: "THEATARR_TEST_PROWLARR_KEY"}}}
	none := func(context.Context) (*gateway.Gateway, *policy.Policy, error) { return nil, nil, context.Canceled }
	c, err := newCardigannCatalog(pol, dir, st, none, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || c == nil {
		t.Fatalf("catalog %v %v", c, err)
	}
	if c.store.Dir != filepath.Join(dir, "cardigann", "definitions") || c.manager.Sessions == nil {
		t.Errorf("store %q sessions %v", c.store.Dir, c.manager.Sessions)
	}
	ps, err := c.ProwlarrProposals(ctx)
	if err != nil || len(ps) != 1 || ps[0].Status != cardigann.ProposalNoDefinition {
		t.Fatalf("proposals %+v %v", ps, err)
	}
	if len(seen) != 1 || seen[0] != "GET /api/v1/indexer" {
		t.Errorf("Prowlarr saw %v", seen)
	}
	// A refresh without a gateway fails and keeps the (empty) catalog.
	if err := c.Refresh(ctx); err == nil || c.Status().LastError == "" {
		t.Errorf("refresh %v status %+v", err, c.Status())
	}
}

func TestCardigannSecretSettingsDeclaresPlainSettings(t *testing.T) {
	raw, err := os.ReadFile("../../internal/indexer/cardigann/testdata/fixturepublic.yml")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "settings:\n", "settings:\n  - name: pass\n    type: password\n", 1))
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "current"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "current", "fixturepublic.yml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	prior := serveCardigann.Swap(&cardigannCatalog{store: &cardigann.Store{Dir: dir}})
	t.Cleanup(func() { serveCardigann.Store(prior) })
	got, known := cardigannSecretSettings("fixturepublic")
	plain, declared := got["sort"]
	if !known || !declared || plain || !got["pass"] {
		t.Fatalf("setting classification: %v %t", got, known)
	}
	if _, known := cardigannSecretSettings("missing"); known {
		t.Fatal("unknown definition reported as known")
	}
}
