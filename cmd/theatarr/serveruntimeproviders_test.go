package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// A key change whose active downloads the new account cannot see is refused, naming the confirmation; with
// orphan_active_downloads those downloads are failed in the commit (not blocklisted) and the ones it sees stay.
func TestProviderKeyChangeFailsMissingDownloadsWhenConfirmed(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	base, err := policy.Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	s.SetTrashRoot("../../internal/settings/testdata/trash")
	initial, err := s.Bootstrap(ctx, base, settings.ImportRequest{Dir: base.Dir, Actor: "test",
		Lookup: func(name string) (string, bool) { return "fixture-" + name, true }})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Movie', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie-web', 0, 0)`,
		`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
			provider_torrent_id, state, created_at, updated_at)
			VALUES (1, 1, 1, 'movie-web', '` + strings.Repeat("a", 40) + `', 'Movie', 'dmm', 'rd', 'rd-1', 'downloading', 0, 0)`,
		`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
			provider_torrent_id, state, created_at, updated_at)
			VALUES (2, 1, 1, 'movie-web', '` + strings.Repeat("b", 40) + `', 'Movie', 'dmm', 'rd', 'rd-2', 'ready', 0, 0)`,
	} {
		if _, err := db.Writer().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	previous := &serveRuntime{Policy: initial.Policy, ProviderKeys: map[string]string{}}
	if err := initial.WithSecret("providers", "rd", nil, func(key string) error {
		previous.ProviderKeys["rd"] = key
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	candidate, err := s.Candidate(ctx, settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test",
		Base: base, Records: initial.Records,
		SecretWrites: map[string]settings.Secret{"providers/rd": settings.NewSecret("rotated-account")}})
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{"rd": "rotated-account"}
	// The new account sees download 2's torrent, not download 1's.
	clients := map[string]provider.Provider{"rd": continuityProvider{torrents: map[string]*provider.Torrent{
		"rd-2": {ID: "rd-2", Hash: strings.Repeat("B", 40)}}}}

	_, err = validateProviderContinuity(ctx, db, candidate, previous, keys, clients)
	if err == nil || !strings.Contains(err.Error(), "account change would orphan 1 active download(s)") ||
		!strings.Contains(err.Error(), "Fail its active downloads") {
		t.Fatalf("without the confirmation: %v", err)
	}

	octx := settings.WithOrphanActiveDownloads(ctx, true)
	c, err := validateProviderContinuity(octx, db, candidate, previous, keys, clients)
	if err != nil {
		t.Fatalf("with the confirmation: %v", err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error { return (&serveRuntime{Continuity: c}).CommitSettings(ctx, tx) }); err != nil {
		t.Fatalf("commit: %v", err)
	}
	state := func(id int) (st, why string) {
		_ = db.Reader().QueryRow(`SELECT state, COALESCE(error, '') FROM download WHERE id = ?`, id).Scan(&st, &why)
		return st, why
	}
	if st, why := state(1); st != "failed" || why != "provider account changed by settings" {
		t.Errorf("missing download: %s %q", st, why)
	}
	if st, _ := state(2); st != "ready" {
		t.Errorf("visible download: %s", st)
	}
	var blocked int
	if err := db.Reader().QueryRow(`SELECT COUNT(*) FROM router_blocklist`).Scan(&blocked); err != nil || blocked != 0 {
		t.Error("blocklisted")
	}

	// A missing download still adding is not failed: retry shortly, as on removal.
	if _, err := db.Writer().ExecContext(ctx, `UPDATE download SET state = 'adding', error = NULL WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := validateProviderContinuity(octx, db, candidate, previous, keys, clients); err == nil ||
		!strings.Contains(err.Error(), "add(s) in progress") {
		t.Fatalf("missing download adding: %v", err)
	}
}
