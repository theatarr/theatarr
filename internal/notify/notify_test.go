package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

type sentReq struct {
	target gateway.Target
	method string
	url    string
	auth   string
	user   string
	pass   string
	body   map[string]any
}

// fakeDoer records requests and answers with status (200 by default).
type fakeDoer struct {
	mu     sync.Mutex
	reqs   []sentReq
	status int
}

func (f *fakeDoer) Do(_ context.Context, t gateway.Target, req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := sentReq{target: t, method: req.Method, url: req.URL.String(), auth: req.Header.Get("Authorization")}
	s.user, s.pass, _ = req.BasicAuth()
	b, _ := io.ReadAll(req.Body)
	_ = json.Unmarshal(b, &s.body)
	f.reqs = append(f.reqs, s)
	code := f.status
	if code == 0 {
		code = 200
	}
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

// fakePayloads gives one Radarr-shaped body per event for view "radarr".
type fakePayloads struct{}

func (fakePayloads) Payloads(_ context.Context, ev Event) ([]Payload, error) {
	flag := map[Kind]string{Grab: "onGrab", Download: "onDownload", Upgrade: "onUpgrade", Health: "onHealthIssue"}[ev.Kind]
	et := map[Kind]string{Grab: "Grab", Download: "Download", Upgrade: "Download", Health: "Health", Test: "Test"}[ev.Kind]
	if ev.View != "" && ev.View != "radarr" {
		return nil, nil
	}
	return []Payload{{View: "radarr", EventType: et, Flag: flag, Body: map[string]any{"eventType": et,
		"movie": map[string]any{"id": ev.TitleID}}}}, nil
}

type nenv struct {
	t    *testing.T
	st   *store.Store
	pol  *policy.Policy
	doer *fakeDoer
	n    *Notifier
	now  time.Time
	env  map[string]string
	hlth []HealthItem
}

func newNEnv(t *testing.T, enabled bool) *nenv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	e := &nenv{t: t, st: st, doer: &fakeDoer{}, now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC),
		env: map[string]string{"THEATARR_HOOK_AUTH": "Bearer secret"}}
	retries := 2
	e.pol = &policy.Policy{Versions: map[string]*policy.VersionPolicy{"movie-web": {Kind: policy.KindMovie}},
		Notify: &policy.Notify{Enabled: enabled, AllowHosts: []string{"notify.ruddarr.com"}, Retries: &retries,
			Targets: map[string]*policy.NotifyTarget{
				"automation": {URL: "http://192.168.1.50:8080/hook", Views: []string{"radarr"}, Native: true,
					AuthHeaderEnv: "THEATARR_HOOK_AUTH"},
				"grabs-only": {URL: "http://192.168.1.51/hook", Native: true, Events: []string{"grab"}}}}}
	e.n, err = New(Config{Store: st, Policy: e.pol, Doer: e.doer, Payloads: fakePayloads{},
		Health: func(context.Context) []HealthItem { return e.hlth },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.now },
		Getenv: func(k string) string { return e.env[k] }})
	if err != nil {
		t.Fatal(err)
	}
	now := store.Millis(e.now)
	e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES (1, 'movie', 'Alpha', 2020,
		101, ?, ?)`, now, now)
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(2, 1, 'movie-web', 1, 'wanted', ?, ?)`, now, now)
	return e
}

func (e *nenv) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *nenv) one(q string, args ...any) int64 {
	e.t.Helper()
	var n int64
	if err := e.st.Reader().QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

// install stores Ruddarr's webhook as POST notification would.
func (e *nenv) install(url string, on ...string) {
	res := map[string]any{"name": "Ruddarr", "implementation": "Webhook", "configContract": "WebhookSettings",
		"fields": []map[string]any{{"name": "url", "value": url}, {"name": "method", "value": "1"},
			{"name": "username", "value": ""}, {"name": "password", "value": "sig"}}}
	for _, o := range on {
		res["on"+o] = true
	}
	b, _ := json.Marshal(res)
	e.exec(`INSERT INTO arrview_notification (view, name, implementation, resource, created_at, updated_at)
		VALUES ('radarr', 'Ruddarr', 'Webhook', ?, 0, 0)`, string(b))
}

func TestDisabledSendsNothing(t *testing.T) {
	t.Parallel()
	e := newNEnv(t, false)
	e.install("https://notify.ruddarr.com/push/x", "Grab")
	e.n.Emit(context.Background(), Event{Kind: Grab, TitleID: 1, Version: "movie-web"})
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE state = 'dropped'`); n != 4 {
		t.Errorf("dropped %d (installed, automation arr and native, grabs-only native)", n)
	}
	if sent, _ := e.n.SendDue(context.Background(), 50); sent != 0 || len(e.doer.reqs) != 0 {
		t.Errorf("sent %d %v", sent, e.doer.reqs)
	}
	if err := e.n.TestNotification(context.Background(), "radarr", []byte(`{"fields":[{"name":"url","value":"https://notify.ruddarr.com/x"}]}`)); err == nil {
		t.Error("test while disabled")
	}
}

func TestEmitAndSend(t *testing.T) {
	t.Parallel()
	e := newNEnv(t, true)
	ctx := context.Background()
	e.install("https://notify.ruddarr.com/push/x", "Grab", "Download")
	e.install("https://evil.example/push", "Grab") // host not allowed: dropped at send
	e.n.Emit(ctx, Event{Kind: Grab, TitleID: 1, Version: "movie-web"})
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE state = 'pending'`); n != 5 {
		t.Fatalf("pending %d", n)
	}
	sent, err := e.n.SendDue(ctx, 50)
	if err != nil || sent != 4 {
		t.Fatalf("sent %d %v", sent, err)
	}
	byTarget := map[string]sentReq{}
	for _, r := range e.doer.reqs {
		byTarget[r.target.String()+" "+r.body["eventType"].(string)] = r
	}
	in := byTarget["webhook:installed Grab"]
	if in.user != "" || in.pass != "sig" || in.method != "POST" || !strings.HasPrefix(in.url, "https://notify.ruddarr.com/") {
		t.Errorf("installed %+v", in)
	}
	if h := byTarget["webhook:automation Grab"]; h.auth != "Bearer secret" {
		t.Errorf("automation arr %+v", h)
	}
	nat := byTarget["webhook:automation theatarr.grab"]
	if nat.body["version"] != "movie-web" || nat.body["title"].(map[string]any)["name"] != "Alpha" {
		t.Errorf("native %+v", nat.body)
	}
	if _, ok := byTarget["webhook:grabs-only theatarr.grab"]; !ok {
		t.Error("grabs-only target")
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE state = 'dropped' AND last_error LIKE '%allow_hosts%'`); n != 1 {
		t.Errorf("not-allowed host %d", n)
	}
	// A download: grabs-only does not want it; the installed one does.
	e.doer.reqs = nil
	e.n.Emit(ctx, Event{Kind: Download, TitleID: 1, Version: "movie-web"})
	if sent, _ := e.n.SendDue(ctx, 50); sent != 3 {
		t.Errorf("download sent %d", sent)
	}
}

func TestRetryBackoff(t *testing.T) {
	t.Parallel()
	e := newNEnv(t, true)
	ctx := context.Background()
	e.doer.status = 500
	e.n.Emit(ctx, Event{Kind: Grab, TitleID: 1, Version: "movie-web", Target: ""})
	e.exec(`DELETE FROM notify_delivery WHERE target <> 'policy:grabs-only'`)
	if sent, _ := e.n.SendDue(ctx, 50); sent != 0 {
		t.Fatal("sent on 500")
	}
	if e.one(`SELECT attempts FROM notify_delivery`) != 1 || e.one(`SELECT next_at FROM notify_delivery`) !=
		store.Millis(e.now.Add(30*time.Second)) {
		t.Error("first back-off")
	}
	e.n.SendDue(ctx, 50) // not due yet
	if len(e.doer.reqs) != 1 {
		t.Errorf("sent before due: %d", len(e.doer.reqs))
	}
	e.now = e.now.Add(31 * time.Second)
	e.n.SendDue(ctx, 50)
	if e.one(`SELECT next_at FROM notify_delivery`) != store.Millis(e.now.Add(time.Minute)) {
		t.Error("second back-off doubles")
	}
	e.now = e.now.Add(2 * time.Minute)
	e.n.SendDue(ctx, 50) // third attempt > retries (2): failed
	var state string
	_ = e.st.Reader().QueryRow(`SELECT state FROM notify_delivery`).Scan(&state)
	if state != "failed" || len(e.doer.reqs) != 3 {
		t.Errorf("state %s after %d requests", state, len(e.doer.reqs))
	}
}

func TestScanPipelineEvents(t *testing.T) {
	t.Parallel()
	e := newNEnv(t, true)
	ctx := context.Background()
	before := store.Millis(e.now.Add(-time.Hour))
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at, added_at, imported_at) VALUES (1, 1, 2, 'movie-web', 'aa', 'Old', 'dmm', 'imported', ?, ?,
		?, ?)`, before, before, before, before)
	if err := e.n.Scan(ctx); err != nil { // the first scan sets since: the old download is not announced
		t.Fatal(err)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery`); n != 0 {
		t.Fatalf("old download announced: %d", n)
	}
	e.now = e.now.Add(time.Minute)
	at := store.Millis(e.now)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at, added_at) VALUES (2, 1, 2, 'movie-web', 'bb', 'New', 'dmm', 'added', ?, ?, ?)`, at, at, at)
	if err := e.n.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.n.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE event = 'theatarr.grab' AND target = 'policy:automation'`); n != 1 {
		t.Errorf("grab deliveries %d", n)
	}
	// Imported with an older file moved aside: an upgrade.
	e.exec(`UPDATE download SET state = 'imported', imported_at = ? WHERE id = 2`, at)
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id) VALUES (?, 'replace', 'applied',
		'movie-web', 2)`, at)
	if err := e.n.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE event = 'theatarr.upgrade'`); n != 1 {
		t.Errorf("upgrade deliveries %d", n)
	}
	// Health: recorded at first sight, emitted on change only.
	e.hlth = []HealthItem{{Source: "ShadowModeCheck", Type: "notice", Message: "x"}}
	_ = e.n.Scan(ctx)
	_ = e.n.Scan(ctx)
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE event = 'Health'`); n != 1 {
		t.Errorf("health deliveries %d", n)
	}
	// A new message alone sends nothing; a check turning bad does, once.
	e.hlth = []HealthItem{{Source: "ShadowModeCheck", Type: "notice", Message: "y"}}
	_ = e.n.Scan(ctx)
	e.hlth = append(e.hlth, HealthItem{Source: "BackupCheck", Type: "warning", Message: "The newest backup is 2d old"})
	_ = e.n.Scan(ctx)
	e.hlth[1].Message = "The newest backup is 3d old"
	_ = e.n.Scan(ctx)
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE event = 'Health'`); n != 2 {
		t.Errorf("health deliveries after a check turned bad %d", n)
	}
}

func TestTestNotification(t *testing.T) {
	t.Parallel()
	e := newNEnv(t, true)
	raw := []byte(`{"name":"Ruddarr","fields":[{"name":"url","value":"https://notify.ruddarr.com/push/x"},
		{"name":"method","value":2},{"name":"password","value":"sig"},
		{"name":"headers","value":[{"key":"X-Test","value":"1"}]}]}`)
	if err := e.n.TestNotification(context.Background(), "radarr", raw); err != nil {
		t.Fatal(err)
	}
	if r := e.doer.reqs[0]; r.method != "PUT" || r.body["eventType"] != "Test" || r.pass != "sig" {
		t.Errorf("test %+v", r)
	}
	e.doer.status = 404
	if err := e.n.TestNotification(context.Background(), "radarr", raw); err == nil {
		t.Error("404 test passed")
	}
	if err := e.n.TestNotification(context.Background(), "radarr",
		[]byte(`{"fields":[{"name":"url","value":"https://evil.example/x"}]}`)); err == nil {
		t.Error("not-allowed host tested")
	}
	if err := e.n.Test(context.Background(), "automation"); err != nil {
		t.Fatal(err)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE target = 'policy:automation'`); n != 2 {
		t.Errorf("policy test deliveries %d", n)
	}
}

// Through the real gateway: an installed notification reaches an allowed host; another host is refused by the
// gateway's allowlist even when the notifier's own check is bypassed.
func TestThroughGateway(t *testing.T) {
	t.Parallel()
	e := newNEnv(t, true)
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Host+r.URL.Path)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, srv.Listener.Addr().String())
	}
	e.pol.Notify.AllowHosts = []string{"notify.ruddarr.com", "relay.test:8080"}
	gw, err := gateway.New(context.Background(), e.pol, gateway.Options{DialContext: dial,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	e.n.doer = gw
	if _, err := e.n.sendInstalled(context.Background(), &Notification{URL: "http://relay.test:8080/push/x",
		Method: "POST"}, []byte(`{}`)); err != nil {
		t.Fatalf("allowed host: %v", err)
	}
	req, _ := http.NewRequest("POST", "http://other.test/push", strings.NewReader("{}"))
	if _, err := gw.Do(context.Background(), gateway.Target{Class: gateway.ClassWebhook,
		Key: gateway.InstalledWebhookKey}, req); !errors.Is(err, gateway.ErrHostNotAllowed) {
		t.Errorf("other host: %v", err)
	}
	if len(hits) != 1 || hits[0] != "relay.test:8080/push/x" {
		t.Errorf("hits %v", hits)
	}
}

// Privacy is a label only: a queued delivery from before the change that names a private-labelled (or no longer
// listed) indexer in data_from goes out like any other.
func TestOldPrivateDeliverySent(t *testing.T) {
	t.Parallel()
	e := newNEnv(t, true)
	ctx := context.Background()
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(200)
	}))
	defer srv.Close()
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, srv.Listener.Addr().String())
	}
	e.pol.Indexers = map[string]*policy.Indexer{"pub": {Kind: "torznab", URL: "http://pub.test/api",
		Privacy: policy.PrivacyPublic}, "trk": {Kind: "torznab", URL: "http://trk.test/api", Privacy: "private"}}
	gw, err := gateway.New(ctx, e.pol, gateway.Options{DialContext: dial,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	e.n.doer = gw
	now := store.Millis(e.now)
	e.exec(`INSERT INTO notify_delivery (id, target, event, payload, state, next_at, created_at, data_from) VALUES
		(1, 'policy:grabs-only', 'theatarr.grab', '{}', 'pending', ?, ?, 'trk'),
		(2, 'policy:grabs-only', 'theatarr.grab', '{}', 'pending', ?, ?, 'gone'),
		(3, 'policy:grabs-only', 'theatarr.grab', '{}', 'pending', ?, ?, 'pub')`, now, now, now, now, now, now)
	if sent, err := e.n.SendDue(ctx, 50); err != nil || sent != 3 {
		t.Fatalf("sent %d %v", sent, err)
	}
	if hits != 3 {
		t.Errorf("hits %d", hits)
	}
}
