package arrview

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/policy"
)

// hookDoer records what the notifier sends.
type hookDoer struct {
	mu   sync.Mutex
	sent []hookSent
}

type hookSent struct {
	target gateway.Target
	body   string
}

func (d *hookDoer) Do(_ context.Context, t gateway.Target, req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, _ := io.ReadAll(req.Body)
	d.sent = append(d.sent, hookSent{target: t, body: string(b)})
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

// Privacy is a label only: every grab and download webhook carries the release name and its indexer, a
// private-labelled tracker's and a push's included.
func TestWebhookReleaseDetails(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	e.pol.Notify = &policy.Notify{Enabled: true, Targets: map[string]*policy.NotifyTarget{
		"hook": {URL: "http://192.168.1.10:8080/hook", Views: []string{"radarr"}, Native: true}}}
	doer := &hookDoer{}
	n, err := notify.New(notify.Config{Store: e.st, Policy: e.pol, Doer: doer, Payloads: e.svc,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	now := ms(e.now)
	rel := func(indexer, title string, private bool) string {
		b, _ := json.Marshal(map[string]any{"indexer": indexer, "title": title, "private": private,
			"privacy": map[bool]string{true: "private", false: "public"}[private]})
		return string(b)
	}
	// 1: our grab from dmm (public). 2: from the private tracker. 3: a push naming dmm. 4: an import through the
	// arr-compatible API (no release data). 5: dmm, but no release data (older row).
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, source,
		created_at, updated_at, release_data) VALUES
		(1, 1, 2, 'movie-web', 'aa11', 'Alpha.2020.2160p.WEB-DL-PUB', 'dmm', 'added', 'rss', ?, ?, ?),
		(2, 1, 2, 'movie-web', 'bb22', 'Alpha.2020.2160p.WEB-DL-PRIV2', 'tracker-example', 'added', 'rss', ?, ?, ?),
		(3, 1, 2, 'movie-web', 'cc33', 'Alpha.2020.2160p.WEB-DL-PRIV3', 'dmm', 'added', 'api:push', ?, ?, ?),
		(4, 1, 2, 'movie-web', 'dd44', 'Alpha.2020.2160p.WEB-DL-PRIV4', 'import', 'ready', 'api:import', ?, ?, NULL),
		(5, 1, 2, 'movie-web', 'ee55', 'Alpha.2020.2160p.WEB-DL-PRIV5', 'dmm', 'added', 'rss', ?, ?, NULL)`,
		now, now, rel("dmm", "Alpha.2020.2160p.WEB-DL-PUB", false),
		now, now, rel("tracker-example", "Alpha.2020.2160p.WEB-DL-PRIV2", true),
		now, now, rel("dmm", "Alpha.2020.2160p.WEB-DL-PRIV3", true), now, now, now, now)
	for id := int64(1); id <= 5; id++ {
		n.Emit(ctx, notify.Event{Kind: notify.Grab, TitleID: 1, Version: "movie-web", DownloadID: id})
		n.Emit(ctx, notify.Event{Kind: notify.Download, TitleID: 1, Version: "movie-web", DownloadID: id,
			File: &notify.File{ID: 9, Path: "/media/movies/Alpha (2020)/Alpha.2020.2160p.WEB-DL-FILE" +
				itoa(id) + ".mkv", RelativePath: "Alpha.2020.2160p.WEB-DL-FILE" + itoa(id) + ".mkv", Size: 5,
				Quality: "WEBDL-2160p", SceneName: "Alpha.2020.2160p.WEB-DL-SCENE" + itoa(id), ReleaseGroup: "GRP" + itoa(id)}})
	}
	// An adopted arr file deleted through the arr-compatible API: no provenance at all.
	n.Emit(ctx, notify.Event{Kind: notify.FileDelete, View: "radarr", TitleID: 1, Version: "movie-remux",
		Reason: "manual", File: &notify.File{ID: 501, Path: "/media/movies/Alpha (2020)/" +
			"Alpha.2020.2160p.BluRay.REMUX-ADOPT.mkv", RelativePath: "Alpha.2020.2160p.BluRay.REMUX-ADOPT.mkv",
			SceneName: "Alpha.2020.2160p.BluRay.REMUX-ADOPT", ReleaseGroup: "ADOPT", Quality: "Remux-2160p"}})
	if _, err := n.SendDue(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if len(doer.sent) != 22 {
		t.Fatalf("sent %d, want 22 (5 grabs and 5 downloads, arr and native each, and 2 deletes)", len(doer.sent))
	}
	for _, want := range []string{"WEB-DL-PUB", "WEB-DL-PRIV2", "WEB-DL-PRIV3", "tracker-example", "SCENE2", "GRP2"} {
		n := 0
		for _, s := range doer.sent {
			if strings.Contains(s.body, want) {
				n++
			}
		}
		if n == 0 {
			t.Errorf("no delivery carries %q", want)
		}
	}
	for _, s := range doer.sent {
		if !strings.Contains(s.body, "Alpha") {
			t.Errorf("title identity lost: %s", s.body)
		}
	}
}
