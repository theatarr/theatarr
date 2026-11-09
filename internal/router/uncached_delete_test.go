package router

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

type deleteErrorAdder struct {
	*fakeAdder
	after func()
	err   error
}

func (a deleteErrorAdder) DeleteOwnUncachedAdd(context.Context, *adder.Added, adder.OwnAdd) error {
	a.after()
	return a.err
}

func TestUncachedDeleteErrorReadBack(t *testing.T) {
	// transient: the first confirmation read answers not found, the second shows the torrent.
	for _, outcome := range []string{"gone", "present", "unreadable", "transient"} {
		t.Run(outcome, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.pol.Versions["movie"].Providers = []string{"ad", "tb"}
			e.pol.Router.AllDebridDeleteUncached = true
			e.r.set = e.pol.RouterSettings()
			e.pr["ad"].set("ad-aaaa", provider.Torrent{Hash: h("a"), State: provider.StateDownloading, Status: "Downloading"})
			reads := 0
			e.pr["ad"].read = func(context.Context, string) {
				reads++
				if outcome == "transient" {
					switch reads {
					case 3:
						e.pr["ad"].remove("ad-aaaa")
					case 4:
						e.pr["ad"].set("ad-aaaa", provider.Torrent{Hash: h("a"), State: provider.StateDownloading,
							Status: "Downloading"})
					}
				}
			}
			e.r.adders["ad"] = deleteErrorAdder{fakeAdder: e.ad["ad"], err: context.DeadlineExceeded, after: func() {
				switch outcome {
				case "gone":
					e.pr["ad"].remove("ad-aaaa")
				case "unreadable":
					e.pr["ad"].readErr = map[string]error{"ad-aaaa": errors.New("connection reset")}
				}
			}}
			res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
			if err != nil {
				t.Fatal(err)
			}
			if outcome == "gone" {
				if res.Provider != "tb" || res.Outcome != OutcomeAdded {
					t.Fatalf("deleted add was kept: %+v", res)
				}
			} else if res.Provider != "ad" || res.Outcome != OutcomeAdded {
				t.Fatalf("unproven deletion moved on: %+v", res)
			}
			want := map[string]int{"gone": 4, "present": 3, "unreadable": 3, "transient": 4}[outcome]
			if reads != want {
				t.Fatalf("status reads %d, want two settle reads and %d delete confirmations", reads, want-2)
			}
		})
	}
}

type accountStatusProvider struct {
	*fakeProvider
	err   error
	reads int
}

func (p *accountStatusProvider) Account(context.Context) (*provider.Account, error) {
	p.reads++
	return &provider.Account{}, p.err
}

// A torrent-specific 5xx does not freeze the stall clock: one or two failing torrents stall and are blocklisted
// once the account answers, or once other torrents of the provider answer. Most of the provider's
// torrents failing at once is its outage, even with the account answering (the torrent endpoint can
// fail alone), and so is a failing or refused account check: zurg-style, those downloads also
// reach the stall limit, but end as provider unavailable: routed again, not blocklisted.
func TestTorrent5xxDoesNotFreezeStallClock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		broken, healthy    int
		accountDown        bool
		want, unavailable  int
		minReads, maxReads int
	}{
		{name: "two broken, account healthy", broken: 2, want: 2, minReads: 1, maxReads: 6},
		{name: "two broken, account down", broken: 2, accountDown: true, unavailable: 2, minReads: 1, maxReads: 6},
		{name: "three broken of three, account healthy", broken: 3, unavailable: 3, maxReads: 0},
		{name: "three broken among five answering", broken: 3, healthy: 5, want: 3, maxReads: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			withQueue(e)
			e.r.set.DownloadingTimeout = 0
			p := &accountStatusProvider{fakeProvider: e.pr["rd"]}
			if tc.accountDown {
				p.err = &provider.APIError{Status: 503}
			}
			e.r.provs["rd"] = p
			e.pr["rd"].readErr = map[string]error{}
			for i := 1; i <= tc.broken+tc.healthy; i++ {
				id := fmt.Sprintf("t%d", i)
				hash := fmt.Sprintf("%040x", i)
				state := "downloading"
				if i <= tc.broken {
					e.pr["rd"].readErr[id] = &provider.APIError{Status: 503}
					state = "added"
				} else {
					e.pr["rd"].set(id, provider.Torrent{Hash: hash, State: provider.StateDownloading,
						Status: "downloading", Progress: 50})
				}
				e.exec(`INSERT INTO download (id,title_id,version_id,version_key,info_hash,release_title,indexer,provider,
					provider_torrent_id,state,added_at,progress,created_at,updated_at)
					VALUES (?,1,10,'movie',?,'Movie','pub','rd',?,?,?,50,0,0)`, i, hash, id, state, store.Millis(e.clock))
			}
			for i := 0; i < 6; i++ {
				if _, err := e.r.Poll(e.ctx); err != nil {
					t.Fatal(err)
				}
				e.clock = e.clock.Add(20 * time.Minute)
			}
			if got := e.count(`SELECT COUNT(*) FROM download WHERE state='failed' AND error LIKE 'stalled%'`); got != tc.want {
				t.Fatalf("stalled %d want %d", got, tc.want)
			}
			if got := e.count(`SELECT COUNT(*) FROM router_blocklist`); got != tc.want {
				t.Fatalf("blocklisted %d want %d", got, tc.want)
			}
			if got := e.count(`SELECT COUNT(*) FROM download WHERE state='queued' AND error LIKE 'provider unavailable%'`); got != tc.unavailable {
				t.Fatalf("ended as provider unavailable %d want %d", got, tc.unavailable)
			}
			if p.reads < tc.minReads || p.reads > tc.maxReads {
				t.Fatalf("account reads %d, want %d to %d", p.reads, tc.minReads, tc.maxReads)
			}
		})
	}
}

func TestSelectionInferenceWithoutDeleteOwnership(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	off := false
	e.pol.Router.CachedOnly = &off
	zero := policy.Duration(0)
	e.pol.Router.StalledAfter = &zero
	e.r.set = e.pol.RouterSettings()
	e.ad["rd"].onSelect = func(context.Context) error {
		e.pr["rd"].set("rd-aaaa", provider.Torrent{Hash: h("a"), State: provider.StateDownloading, Status: "downloading", Progress: 1})
		return context.DeadlineExceeded
	}
	e.pr["rd"].set("rd-aaaa", provider.Torrent{Hash: h("a"), State: provider.StateQueued, Status: rdWaitingFiles,
		Files: []provider.File{{ID: "1", Path: "Movie.mkv", Bytes: 1000}}})
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Outcome != OutcomeAdded {
		t.Fatalf("route %+v %v", res, err)
	}
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT COUNT(*) FROM download WHERE id=? AND state='downloading' AND files_selected=1`, res.DownloadID) != 1 {
		t.Fatal("lost selection answer without own_add left the download added")
	}
	e.clock = e.clock.Add(e.r.set.DownloadingTimeout + time.Minute)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT COUNT(*) FROM download WHERE id=? AND state='failed' AND error LIKE 'stalled%'`, res.DownloadID) != 1 {
		t.Fatal("no-progress timeout did not apply")
	}
	if len(e.ad["rd"].deletes) != 0 {
		t.Fatal("inference gave deletion ownership")
	}
}
