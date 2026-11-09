package router

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

type runtimeCheckingProvider struct {
	provider.Provider
	started chan int64
	release <-chan struct{}
}

func (p runtimeCheckingProvider) Torrent(ctx context.Context, id string) (*provider.Torrent, error) {
	p.started <- settings.ContextGeneration(ctx)
	<-p.release
	return p.Provider.Torrent(ctx, id)
}

func addActiveDownload(e *env, id int64, hash string) {
	e.t.Helper()
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
		provider, provider_torrent_id, state, episodes, added_at, own_add, created_at, updated_at)
		VALUES (?, 2, 20, 'series', ?, 'Show', 'pub', 'rd', ?, 'added', '[7,8]', ?, 1, 0, 0)`,
		id, hash, fmt.Sprintf("RD%d", id), store.Millis(e.clock))
}

func TestPollLeavesUnavailableProvidersUnpolled(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	var logs bytes.Buffer
	e.r.log = slog.New(slog.NewTextHandler(&logs, nil))
	for i, state := range []string{"adding", "added", "downloading", "ready"} {
		addActiveDownload(e, int64(5+i), h(string(rune('a'+i))))
		e.exec("UPDATE download SET state = ?, ready_at = 1 WHERE id = ?", state, 5+i)
	}
	delete(e.r.provs, "rd")
	sum, err := e.r.Poll(e.ctx)
	if err != nil || sum.Unpolled != 4 || sum.Failed != 0 || sum.Read != 0 {
		t.Fatalf("poll summary %+v, %v", sum, err)
	}
	if e.count("SELECT COUNT(*) FROM download WHERE state = 'failed'") != 0 || e.count("SELECT COUNT(*) FROM router_blocklist") != 0 {
		t.Fatal("unavailable provider changed downloads or blocklist")
	}
	if e.count("SELECT COUNT(*) FROM download WHERE error IS NOT NULL OR updated_at != 0") != 0 {
		t.Fatal("unavailable provider changed an unsettled download")
	}
	if strings.Count(logs.String(), "provider unavailable") != 1 || !strings.Contains(logs.String(), "provider=rd count=4") {
		t.Fatalf("expected one warning with provider and count: %s", &logs)
	}
}

func TestProviderGraceStartsAtPublicationAndSurvivesReload(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	e.clock = e.clock.Add(time.Hour)
	e.r.PublishProviderChanges([]string{"rd"})
	deadline := e.r.ignoreMissingUntil["rd"]
	if !deadline.Equal(e.clock.Add(2 * time.Minute)) {
		t.Fatalf("deadline %v", deadline)
	}
	e.clock = e.clock.Add(time.Minute)
	next, err := New(Config{Store: e.st, Policy: e.pol, Previous: e.r, Now: func() time.Time { return e.clock }})
	if err != nil {
		t.Fatal(err)
	}
	if !next.ignoreMissingUntil["rd"].Equal(deadline) {
		t.Fatal("reload lost unexpired grace")
	}
	e.clock = deadline.Add(time.Second)
	last, err := New(Config{Store: e.st, Policy: e.pol, Previous: next, Now: func() time.Time { return e.clock }})
	if err != nil {
		t.Fatal(err)
	}
	if !last.ignoreMissingUntil["rd"].IsZero() {
		t.Fatal("expired grace carried forward")
	}
}

func TestPollDoesNotBlocklistNotFoundImmediatelyAfterSettingsChange(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.clock = time.Now().UTC()
	addActiveDownload(e, 5, h("a"))
	e.r.ignoreMissingUntil = map[string]time.Time{"rd": e.clock.Add(time.Minute)}
	if sum, err := e.r.Poll(e.ctx); err != nil || sum.Failed != 1 {
		t.Fatalf("poll after account change: %+v, %v", sum, err)
	}
	if e.count(`SELECT COUNT(*) FROM router_blocklist`) != 0 {
		t.Fatal("transient provider not-found after settings change was blocklisted")
	}

	e.clock = e.clock.Add(2 * time.Minute)
	addActiveDownload(e, 6, h("b"))
	// Past the settings-change window a miss counts as usual: blocklisted once it stays missing for missingGrace.
	if sum, err := e.r.Poll(e.ctx); err != nil || sum.Failed != 0 {
		t.Fatalf("first miss after grace period: %+v, %v", sum, err)
	}
	e.clock = e.clock.Add(missingGrace)
	if sum, err := e.r.Poll(e.ctx); err != nil || sum.Failed != 1 {
		t.Fatalf("poll after grace period: %+v, %v", sum, err)
	}
	if e.count(`SELECT COUNT(*) FROM router_blocklist WHERE info_hash = ?`, h("b")) != 1 {
		t.Fatal("a real missing torrent after the grace period was not blocklisted")
	}
}

func TestPollPassUsesRuntimeCapturedAtStart(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	addActiveDownload(e, 5, h("a"))
	e.pr["rd"].set("RD5", provider.Torrent{Status: "downloading", State: provider.StateDownloading})
	started, release := make(chan int64, 1), make(chan struct{})
	e.r.provs["rd"] = runtimeCheckingProvider{Provider: e.pr["rd"], started: started, release: release}
	current := &settings.Runtime{Generation: 1}
	e.r.SetRuntimeResolver(func(ctx context.Context) (context.Context, *Router) {
		return settings.WithRuntime(ctx, current), e.r
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	result := make(chan error, 1)
	go func() {
		_, _, err := e.r.pollOnce(e.ctx)
		result <- err
	}()
	if got := <-started; got != 1 {
		t.Fatalf("poll started with runtime %d, want 1", got)
	}
	current = &settings.Runtime{Generation: 2}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
