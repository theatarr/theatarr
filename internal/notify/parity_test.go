package notify_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

type parityEnv struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	n      *notify.Notifier
	now    time.Time
	health []notify.HealthItem
	sent   []gateway.Target
}

func (e *parityEnv) Do(_ context.Context, target gateway.Target, _ *http.Request) (*http.Response, error) {
	e.sent = append(e.sent, target)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

func newParityEnv(t *testing.T) *parityEnv {
	t.Helper()
	e := &parityEnv{t: t, ctx: context.Background(), now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
	var err error
	e.st, err = store.Open(e.ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.st.Close() })
	if _, err := e.st.Migrate(e.ctx); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Versions: map[string]*policy.VersionPolicy{
		"movie": {Kind: policy.KindMovie}, "series": {Kind: policy.KindSeries}},
		Views: map[string]*policy.View{
			"radarr": {Flavour: "radarr", Path: "/radarr", Versions: []string{"movie"}},
			"sonarr": {Flavour: "sonarr", Path: "/sonarr", Versions: []string{"series"}}},
		Indexers: map[string]*policy.Indexer{"public": {Privacy: policy.PrivacyPublic}, "private": {Privacy: policy.PrivacyPrivate}},
		Notify: &policy.Notify{Enabled: true, AllowHosts: []string{"hook.example"}, Targets: map[string]*policy.NotifyTarget{
			"native": {URL: "https://hook.example/native", Native: true}}}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	views, err := arrview.New(e.ctx, arrview.Config{Store: e.st, Policy: p, Auth: &auth.Service{}, Logger: logger,
		Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	e.n, err = notify.New(notify.Config{Store: e.st, Policy: p, Payloads: views, Doer: e, Logger: logger,
		Now: func() time.Time { return e.now }, Health: func(context.Context) []notify.HealthItem { return e.health }})
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO title (id,kind,name,created_at,updated_at) VALUES (1,'movie','Film',0,0),(2,'series','Show',0,0)`)
	e.exec(`INSERT INTO version (id,title_id,policy_key,created_at,updated_at) VALUES (1,1,'movie',0,0),(2,2,'series',0,0)`)
	e.exec(`INSERT INTO episode (id,title_id,season,number,name,created_at,updated_at) VALUES (1,2,1,1,'Pilot',0,0)`)
	e.scan()
	e.now = e.now.Add(time.Second)
	return e
}

func (e *parityEnv) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().ExecContext(e.ctx, q, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *parityEnv) scan() {
	e.t.Helper()
	if err := e.n.Scan(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

func (e *parityEnv) install(view string, flags map[string]bool) int64 {
	e.t.Helper()
	res := map[string]any{"name": "Hook", "fields": []map[string]any{{"name": "url", "value": "https://hook.example/event"}}}
	for k, v := range flags {
		res[k] = v
	}
	b, _ := json.Marshal(res)
	r, err := e.st.Writer().ExecContext(e.ctx, `INSERT INTO arrview_notification (view,name,implementation,resource,created_at,updated_at)
		VALUES (?, 'Hook', 'Webhook', ?,0,0)`, view, string(b))
	if err != nil {
		e.t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	return id
}

func (e *parityEnv) bodies(target, event string) []map[string]any {
	e.t.Helper()
	rows, err := e.st.Reader().QueryContext(e.ctx, `SELECT payload,data_from FROM notify_delivery WHERE target = ? AND event = ? ORDER BY id`, target, event)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw, from string
		if err := rows.Scan(&raw, &from); err != nil {
			e.t.Fatal(err)
		}
		if from != "" {
			e.t.Fatalf("new event has DataFrom %q", from)
		}
		var b map[string]any
		if err := json.Unmarshal([]byte(raw), &b); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *parityEnv) download(id int, title int, indexer string, adopted bool) {
	version, episodes := "movie", "[]"
	if title == 2 {
		version, episodes = "series", "[1]"
	}
	var release any
	if !adopted {
		release = fmt.Sprintf(`{"indexer":%q,"private":%v}`, indexer, indexer == "private")
	}
	e.exec(`INSERT INTO download (id,title_id,version_id,version_key,info_hash,release_title,indexer,quality,state,episodes,
		provider,release_data,created_at,updated_at) VALUES (?,?,?,?, 'SECRET-HASH','SECRET-RELEASE',?,'WEBDL-1080p','ready',?,
		'SECRET-CLIENT',?,?,?)`, id, title, title, version, indexer, episodes, release, store.Millis(e.now), store.Millis(e.now))
}

func TestHealthChangesArrPayload(t *testing.T) {
	t.Parallel()
	e := newParityEnv(t)
	errorOnly := e.install("radarr", map[string]bool{"onHealthIssue": true, "onHealthRestored": true})
	warnings := e.install("radarr", map[string]bool{"onHealthIssue": true, "onHealthRestored": true, "includeHealthWarnings": true})
	restoredOnly := e.install("radarr", map[string]bool{"onHealthRestored": true})
	issuesOnly := e.install("radarr", map[string]bool{"onHealthIssue": true})
	off := e.install("radarr", nil)
	e.health = []notify.HealthItem{{Source: "MountCheck", Type: "error", Message: "Mount unavailable", WikiURL: "https://wiki.example/mount"},
		{Source: "BackupCheck", Type: "warning", Message: "Backup overdue"}}
	e.scan()
	e.scan()
	e.health[0].Message = "Mount still unavailable"
	e.scan()
	// Restore the error while the warning remains: recovery is per check, using the latest previous issue.
	e.health = e.health[1:]
	e.scan()
	e.scan()
	for _, tc := range []struct {
		id               int64
		issues, restored int
	}{{errorOnly, 1, 1}, {warnings, 2, 1}, {restoredOnly, 0, 1}, {issuesOnly, 1, 0}, {off, 0, 0}} {
		target := fmt.Sprintf("notification:%d", tc.id)
		issues, restored := e.bodies(target, "Health"), e.bodies(target, "HealthRestored")
		if len(issues) != tc.issues || len(restored) != tc.restored {
			t.Fatalf("%s: issues=%d restored=%d", target, len(issues), len(restored))
		}
		for _, b := range append(issues, restored...) {
			for _, field := range []string{"eventType", "instanceName", "level", "message", "type", "wikiUrl"} {
				if _, ok := b[field]; !ok {
					t.Errorf("missing arr field %s: %v", field, b)
				}
			}
		}
		if len(restored) > 0 && (restored[0]["level"] != "error" || restored[0]["type"] != "MountCheck" || restored[0]["message"] != "Mount still unavailable") {
			t.Errorf("restored previous check: %v", restored)
		}
	}
	e.health = nil
	e.scan()
	e.scan()
	if got := e.bodies(fmt.Sprintf("notification:%d", errorOnly), "HealthRestored"); len(got) != 1 {
		t.Fatal("warning restoration bypassed includeHealthWarnings")
	}
	if got := e.bodies(fmt.Sprintf("notification:%d", warnings), "HealthRestored"); len(got) != 2 || got[1]["level"] != "warning" {
		t.Fatal(got)
	}
}

func TestTitleAddedOncePerVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		view, version, event, flag, media string
		id                                int
	}{{"radarr", "movie", "MovieAdded", "onMovieAdded", "movie", 1}, {"sonarr", "series", "SeriesAdd", "onSeriesAdd", "series", 2}} {
		t.Run(tc.event, func(t *testing.T) {
			e := newParityEnv(t)
			id := e.install(tc.view, map[string]bool{tc.flag: true})
			off := e.install(tc.view, nil)
			kind := policy.KindMovie
			if tc.id == 2 {
				kind = policy.KindSeries
			}
			if err := e.st.WriteTx(e.ctx, func(tx *sql.Tx) error {
				_, err := catalog.Add(e.ctx, tx, catalog.Input{Kind: kind, TmdbID: 303, Title: "New title",
					Versions: func(bool) ([]string, error) { return []string{tc.version}, nil }}, e.now,
					slog.New(slog.NewTextHandler(io.Discard, nil)))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			e.scan()
			e.scan()
			// A follower import mirrors monitoring changes on the same title/version.
			e.exec(`UPDATE version SET monitored = 0, updated_at = ? WHERE id = 3`, store.Millis(e.now.Add(time.Minute)))
			e.scan()
			b := e.bodies(fmt.Sprintf("notification:%d", id), tc.event)
			if len(b) != 1 || b[0]["eventType"] != tc.event || b[0][tc.media] == nil {
				t.Fatalf("arr add: %v", b)
			}
			if tc.id == 1 && b[0]["addMethod"] != "manual" {
				t.Errorf("addMethod: %v", b)
			}
			if len(e.bodies(fmt.Sprintf("notification:%d", off), tc.event)) != 0 {
				t.Fatal("disabled add flag delivered")
			}
		})
	}
}

func TestRenameOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		view, version, files string
		id                   int
	}{{"radarr", "movie", "renamedMovieFiles", 1}, {"sonarr", "series", "renamedEpisodeFiles", 2}} {
		t.Run(tc.view, func(t *testing.T) {
			e := newParityEnv(t)
			id := e.install(tc.view, map[string]bool{"onRename": true})
			off := e.install(tc.view, nil)
			e.exec(`INSERT INTO notify_rename (title_id,version_key,link_id,previous_path,path,quality,created_at) VALUES (?,?,7,'/SECRET-OLD.mkv','/SECRET-NEW.mkv','WEBDL-1080p',?)`, tc.id, tc.version, store.Millis(e.now))
			e.scan()
			e.scan()
			b := e.bodies(fmt.Sprintf("notification:%d", id), "Rename")
			if len(b) != 1 {
				t.Fatal(b)
			}
			files, ok := b[0][tc.files].([]any)
			if !ok || len(files) != 1 {
				t.Fatal(b)
			}
			f := files[0].(map[string]any)
			if f["id"] != float64(arrview.LinkFileBase+7) || f["quality"] != "WEBDL-1080p" || f["qualityVersion"] != float64(1) {
				t.Fatal(f)
			}
			if len(e.bodies(fmt.Sprintf("notification:%d", off), "Rename")) != 0 {
				t.Fatal("disabled rename flag delivered")
			}
		})
	}
}

func TestManualHoldOncePerDownloadAndReason(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		indexer string
		adopted bool
	}{{"private", false}, {"public", true}, {"public", false}} {
		t.Run(fmt.Sprintf("%s/adopted=%v", tc.indexer, tc.adopted), func(t *testing.T) {
			e := newParityEnv(t)
			id := e.install("sonarr", map[string]bool{"onManualInteractionRequired": true})
			off := e.install("sonarr", nil)
			e.download(1, 2, tc.indexer, tc.adopted)
			e.exec(`INSERT INTO library_import (download_id,state,last_error,created_at,updated_at) VALUES (1,'rejected','held for review /SECRET-FILE',?,?)`, store.Millis(e.now), store.Millis(e.now))
			e.scan()
			e.scan()
			e.exec(`UPDATE library_import SET last_error = 'zurg does not confirm SECRET-HASH' WHERE download_id = 1`)
			e.scan()
			e.scan()
			// Revisiting an old reason is still one notification for this download/reason.
			e.exec(`UPDATE library_import SET last_error = 'held for review /SECRET-FILE' WHERE download_id = 1`)
			e.scan()
			b := e.bodies(fmt.Sprintf("notification:%d", id), "ManualInteractionRequired")
			if len(b) != 2 {
				t.Fatalf("hold deliveries %v", b)
			}
			for _, payload := range b {
				if payload["series"] == nil || len(payload["episodes"].([]any)) != 1 || payload["downloadStatus"] != "Warning" || payload["downloadStatusMessages"] == nil {
					t.Fatal(payload)
				}
				if info := payload["downloadInfo"].(map[string]any); info["quality"] != "WEBDL-1080p" || info["qualityVersion"] != float64(1) {
					t.Fatal(info)
				}
			}
			if len(e.bodies(fmt.Sprintf("notification:%d", off), "ManualInteractionRequired")) != 0 {
				t.Fatal("disabled manual flag delivered")
			}
			// Privacy is a label only: the download's details go out whatever the indexer's label.
			for _, payload := range b {
				if payload["downloadId"] != "SECRET-HASH" || payload["downloadClient"] == nil {
					t.Errorf("download details missing: %v", payload)
				}
			}
			if native := e.bodies("policy:native", "theatarr.manual_interaction_required"); len(native) == 0 ||
				!strings.Contains(fmt.Sprint(native[0]["download"]), "SECRET-RELEASE") {
				t.Errorf("native payload without the release: %v", native)
			}
			if _, err := e.n.SendDue(e.ctx, 100); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPolicyParityEventFilters(t *testing.T) {
	t.Parallel()
	e := newParityEnv(t)
	// Native target filters select only the named events.
	p := &policy.Policy{Notify: &policy.Notify{Targets: map[string]*policy.NotifyTarget{}}}
	for _, kind := range []notify.Kind{notify.HealthRestored, notify.SeriesAdd, notify.MovieAdded, notify.Rename, notify.ManualInteractionRequired} {
		p.Notify.Targets[string(kind)] = &policy.NotifyTarget{URL: "https://hook.example/event", Native: true, Events: []string{string(kind)}}
	}
	n, err := notify.New(notify.Config{Store: e.st, Policy: p, Doer: e, Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []notify.Kind{notify.HealthRestored, notify.SeriesAdd, notify.MovieAdded, notify.Rename, notify.ManualInteractionRequired} {
		n.Emit(e.ctx, notify.Event{Kind: kind})
		for target := range p.Notify.Targets {
			b := e.bodies("policy:"+target, "theatarr."+string(kind))
			want := 0
			if target == string(kind) {
				want = 1
			}
			if len(b) != want {
				t.Fatalf("%s to %s: %v", kind, target, b)
			}
		}
	}
}

func TestReviewHoldAndMovieManualPayload(t *testing.T) {
	t.Parallel()
	e := newParityEnv(t)
	id := e.install("radarr", map[string]bool{"onManualInteractionRequired": true})
	e.download(1, 1, "private", false)
	e.exec(`INSERT INTO library_import (download_id,state,last_error,plan,created_at,updated_at)
		VALUES (1,'rejected','SECRET-REVIEW','{}',?,?)`, store.Millis(e.now), store.Millis(e.now))
	e.scan()
	e.scan()
	b := e.bodies(fmt.Sprintf("notification:%d", id), "ManualInteractionRequired")
	if len(b) != 1 || b[0]["movie"] == nil || b[0]["downloadInfo"] == nil || b[0]["episodes"] != nil {
		t.Fatal(b)
	}
	// A dry run does not ask for manual interaction: an ordinary gated one, nor one an older Theatarr recorded for
	// an anime mapping review (unreviewed mappings no longer hold imports).
	e.exec(`UPDATE library_import SET state='dry_run', plan='{"refused":"MappingNotReviewed: SECRET-FILE"}', last_error='SECRET-MAPPING'`)
	e.scan()
	if got := e.bodies(fmt.Sprintf("notification:%d", id), "ManualInteractionRequired"); len(got) != 1 {
		t.Fatal(got)
	}
	e.exec(`UPDATE library_import SET plan='{}', last_error='library disabled'`)
	e.scan()
	if got := e.bodies(fmt.Sprintf("notification:%d", id), "ManualInteractionRequired"); len(got) != 1 {
		t.Fatal(got)
	}
}
