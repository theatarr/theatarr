package arrview

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
)

// blockBoth puts a release on version's blocklist by its hash and by its title key, and on tv-2160p's by its hash
// (another library's block, which a grab for version leaves alone).
func blockBoth(e *devEnv, version, hash, title string) {
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, created_at)
		VALUES (?, ?, 1, ?, 'sample', 'library', 0), (?, ?, 1, ?, 'sample', 'library', 0),
		('tv-2160p', ?, 1, ?, 'sample', 'library', 0)`,
		version, hash, title, version, router.BlocklistKey("", title), title, hash, title)
}

func blockedRefusal(err error) *Block {
	var r *Refused
	if errors.As(err, &r) {
		return r.Block
	}
	return nil
}

// TestManualGrabUnblocks: a grab by hand (Grab, Add by hash, Retry) of a release on the library's blocklist is refused
// with the block (library and reason) and changes nothing; sent again with who confirmed, it is routed and both of
// the library's rows (hash and title key) go, with one unblock in the journal; another library's block stays.
func TestManualGrabUnblocks(t *testing.T) {
	t.Parallel()
	rel := indexer.Release{Indexer: "prowlarr-public", Kind: "torznab", Privacy: policy.PrivacyPublic,
		Title: "Alpha.2020.2160p.WEB-DL.H265-GRP", InfoHash: strings.Repeat("9", 40), Size: 10 << 30}
	retryTitle := "Alpha.2020.2160p.WEB-DL-RETRY"
	routes := map[string]func(e *devEnv, unblockBy string) error{
		"grab": func(e *devEnv, by string) error {
			_, err := e.svc.GrabRelease(context.Background(), 1, "movie-web", rel, false, by, "ui:grab:test")
			return err
		},
		"add": func(e *devEnv, by string) error {
			_, err := e.svc.AddRelease(context.Background(), 1, "movie-web", rel, false, by, "ui:grab:test")
			return err
		},
		"retry": func(e *devEnv, by string) error {
			_, err := e.svc.RetryDownload(context.Background(), 50, by, "ui:retry:50")
			return err
		},
	}
	for name, run := range routes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newDevEnv(t)
			title := rel.Title
			if name == "retry" {
				title = retryTitle
				e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
					quality, score, episodes, state, error, created_at, updated_at) VALUES (50, 1, 2, 'movie-web', ?, ?,
					'prowlarr-public', 'WEBDL-2160p', 50, '[]', 'failed', 'stalled', 0, 0)`, rel.InfoHash, title)
			}
			blockBoth(e, "movie-web", rel.InfoHash, title)
			lib := policy.LibraryNames(e.pol)["movie-web"]

			err := run(e, "")
			if b := blockedRefusal(err); b == nil || b.Library != lib || b.Reason != "sample" || b.Source != "library" {
				t.Fatalf("blocked without unblock: %v %+v", err, b)
			}
			if n := e.str(`SELECT COUNT(*) FROM router_blocklist`); n != "3" {
				t.Errorf("a refused grab left %s blocklist rows, want 3", n)
			}
			if n := e.str(`SELECT COUNT(*) FROM job WHERE kind = 'router.route'`); n != "0" {
				t.Errorf("a refused grab queued %s routes", n)
			}

			// An automatic route (a search, RSS) of the same release still skips it: only a grab by hand unblocks.
			auto := router.Request{TitleID: 1, VersionID: 2, Version: "movie-web", Source: "rss",
				Candidates: []router.Candidate{{Release: rel}}}
			if _, err := e.svc.router.Enqueue(context.Background(), auto, time.Time{}); err != nil {
				t.Fatal(err)
			}
			e.drain()
			if slices.Contains(e.adds(), rel.InfoHash) || e.str(`SELECT COUNT(*) FROM router_blocklist`) != "3" {
				t.Fatalf("an automatic route placed a blocklisted release: %v", e.adds())
			}

			if err := run(e, "user:admin"); err != nil {
				t.Fatalf("with unblock: %v", err)
			}
			if n := e.str(`SELECT COUNT(*) FROM router_blocklist WHERE version_key = 'movie-web'`); n != "0" {
				t.Errorf("%s movie-web rows left after the unblocking grab", n)
			}
			if blocked, _ := router.Blocklisted(context.Background(), e.st.Reader(), "tv-2160p", rel.InfoHash, title); !blocked {
				t.Error("another library's block went with the grab")
			}
			if n := e.str(`SELECT COUNT(*) FROM journal_action WHERE action = 'unblock' AND by = 'user:admin'
				AND version_key = 'movie-web'`); n != "1" {
				t.Errorf("%s unblock entries, want 1", n)
			}
			e.drain()
			if !slices.Contains(e.adds(), rel.InfoHash) {
				t.Errorf("the unblocked release was not added: %v", e.adds())
			}
		})
	}
}

// TestManualGrabRefusedKeepsBlock: with unblock set, every other refusal is said first and the block stays, for a
// grab, an add by hash and a retry alike: Pause, in flight, upgrades off, the client-indexer rule; and a retry of a
// download whose copy was removed since (the web layer refuses a grab or an add for a removed copy before Ops).
func TestManualGrabRefusedKeepsBlock(t *testing.T) {
	t.Parallel()
	rel := indexer.Release{Indexer: "prowlarr-public", Kind: "torznab", Privacy: policy.PrivacyPublic,
		Title: "Alpha.2020.2160p.WEB-DL.H265-GRP", InfoHash: strings.Repeat("9", 40), Size: 10 << 30}
	cases := map[string]func(e *devEnv){
		"pause": func(e *devEnv) { e.pol.Router.Enabled = false },
		"in flight": func(e *devEnv) {
			e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
				quality, score, episodes, state, created_at, updated_at) VALUES (70, 1, 2, 'movie-web', ?, 'held',
				'prowlarr-public', 'WEBDL-2160p', 50, '[]', 'downloading', 0, 0)`, rel.InfoHash)
		},
		"upgrades off": func(e *devEnv) {
			e.seedFiles()
			e.pol.Versions["movie-web"].Upgrades = policy.UpgradesOff
		},
		"client indexer": func(e *devEnv) {
			v := e.pol.Versions["movie-web"]
			v.AddVia, v.Client = policy.AddViaClient, "dc"
			e.pol.Indexers["prowlarr-public"].Providers = []string{"rd"}
		},
		"removed copy": func(e *devEnv) {
			e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (2, 'radarr', 0)`)
		},
	}
	routes := map[string]func(e *devEnv) error{
		"grab": func(e *devEnv) error {
			_, err := e.svc.GrabRelease(context.Background(), 1, "movie-web", rel, false, "user:admin", "ui:grab:test")
			return err
		},
		"add": func(e *devEnv) error {
			_, err := e.svc.AddRelease(context.Background(), 1, "movie-web", rel, false, "user:admin", "ui:grab:test")
			return err
		},
		"retry": func(e *devEnv) error {
			e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
				quality, score, episodes, state, error, created_at, updated_at) VALUES (50, 1, 2, 'movie-web', ?, ?,
				'prowlarr-public', 'WEBDL-2160p', 50, '[]', 'failed', 'stalled', 0, 0)`, rel.InfoHash, rel.Title)
			_, err := e.svc.RetryDownload(context.Background(), 50, "user:admin", "ui:retry:50")
			return err
		},
	}
	for name, set := range cases {
		for route, run := range routes {
			if name == "client indexer" && route == "add" {
				continue // a release added by hand has no indexer: the client-indexer rule never applies to it
			}
			if name == "removed copy" && route != "retry" {
				continue // the web layer refuses these before Ops (refuseRemovedCopy)
			}
			t.Run(name+"/"+route, func(t *testing.T) {
				t.Parallel()
				e := newDevEnv(t)
				blockBoth(e, "movie-web", rel.InfoHash, rel.Title)
				set(e)
				err := run(e)
				t.Logf("refused: %v", err)
				if !isRefused(err) || blockedRefusal(err) != nil {
					t.Fatalf("want another refusal than the block: %v", err)
				}
				if name == "removed copy" && !strings.Contains(err.Error(), `removed from library "movie-web": add it there again first`) {
					t.Errorf("removed copy refusal: %v", err)
				}
				if n := e.str(`SELECT COUNT(*) FROM router_blocklist`); n != "3" {
					t.Errorf("a refused grab left %s blocklist rows, want 3", n)
				}
				if n := e.str(`SELECT COUNT(*) FROM journal_action WHERE action = 'unblock'`); n != "0" {
					t.Errorf("a refused grab recorded %s unblocks", n)
				}
				if n := e.str(`SELECT COUNT(*) FROM job WHERE kind = 'router.route'`); n != "0" {
					t.Errorf("a refused grab queued %s routes", n)
				}
			})
		}
	}
}
