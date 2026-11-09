package router

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

func TestPollProgressWindow(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
		provider, provider_torrent_id, state, created_at, updated_at)
		VALUES (1, 1, 10, 'movie', ?, 'Release', 'pub', 'ad', 'A1', 'added', 0, 0)`, h("a"))
	poll := func(p float64, state provider.State) {
		t.Helper()
		e.pr["ad"].set("A1", provider.Torrent{State: state, Progress: p})
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	estimate := func(want time.Duration, known bool) {
		t.Helper()
		got, ok, err := EstimateRemaining(e.ctx, e.st.Reader(), 1, e.clock)
		if err != nil || ok != known || known && got != want {
			t.Fatalf("estimate %s/%v: %v, want %s/%v", got, ok, err, want, known)
		}
	}
	poll(20, provider.StateDownloading)
	estimate(0, false)
	e.clock = e.clock.Add(time.Minute)
	poll(40, provider.StateDownloading)
	estimate(3*time.Minute, true)
	// Reading between polls reduces the remaining time without moving the completion timestamp.
	e.clock = e.clock.Add(30 * time.Second)
	estimate(150*time.Second, true)
	e.clock = e.clock.Add(30 * time.Second)
	poll(40, provider.StateDownloading)
	estimate(0, false)
	e.clock = e.clock.Add(time.Minute)
	poll(30, provider.StateDownloading) // regression discards the whole old window, including its 20% sample
	estimate(0, false)
	e.clock = e.clock.Add(time.Minute)
	poll(40, provider.StateDownloading)
	estimate(6*time.Minute, true)
	for i := 0; i < 10; i++ {
		e.clock = e.clock.Add(15 * time.Second)
		poll(float64(41+i), provider.StateDownloading)
	}
	var count int
	if err := e.st.Reader().QueryRow(`SELECT count(*) FROM download_progress WHERE download_id = 1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 6 {
		t.Fatalf("window has %d samples, want 6", count)
	}
	e.clock = e.clock.Add(6 * time.Minute)
	estimate(0, false)
	poll(60, provider.StateDownloading)
	estimate(0, false)
	e.clock = e.clock.Add(time.Minute)
	poll(100, provider.StateDownloaded)
	if err := e.st.Reader().QueryRow(`SELECT count(*) FROM download_progress WHERE download_id = 1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("completed download has %d samples", count)
	}
}

func TestPollProgressResets(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"provider", "torrent", "clock", "queued"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
				provider, provider_torrent_id, state, created_at, updated_at)
				VALUES (1, 1, 10, 'movie', ?, 'Release', 'pub', 'ad', 'A1', 'added', 0, 0)`, h("a"))
			pk, tid := "ad", "A1"
			poll := func(p float64, state provider.State) {
				t.Helper()
				e.pr[pk].set(tid, provider.Torrent{State: state, Progress: p})
				if _, err := e.r.Poll(e.ctx); err != nil {
					t.Fatal(err)
				}
			}
			poll(20, provider.StateDownloading)
			e.clock = e.clock.Add(time.Minute)
			poll(40, provider.StateDownloading)
			e.clock = e.clock.Add(time.Minute)
			state := provider.StateDownloading
			switch kind {
			case "provider":
				pk = "tb"
				e.exec(`UPDATE download SET provider = 'tb' WHERE id = 1`)
			case "torrent":
				tid = "A2"
				e.exec(`UPDATE download SET provider_torrent_id = 'A2' WHERE id = 1`)
			case "clock":
				e.clock = e.clock.Add(-90 * time.Second)
			case "queued":
				state = provider.StateQueued
			}
			poll(50, state)
			if d, ok, err := EstimateRemaining(e.ctx, e.st.Reader(), 1, e.clock); err != nil || ok {
				t.Fatalf("estimate after %s reset: %s/%v: %v", kind, d, ok, err)
			}
		})
	}
}
