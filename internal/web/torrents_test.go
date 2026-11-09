package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/torrents"
)

// torrentProvider is a read-only provider with a fixed list (invented torrents).
type torrentProvider struct{ list []provider.Torrent }

func (p *torrentProvider) Key() string  { return "rd" }
func (p *torrentProvider) Kind() string { return provider.KindRealDebrid }
func (p *torrentProvider) Account(context.Context) (*provider.Account, error) {
	return &provider.Account{}, nil
}
func (p *torrentProvider) Torrents(context.Context) ([]provider.Torrent, error) { return p.list, nil }
func (p *torrentProvider) Torrent(_ context.Context, id string) (*provider.Torrent, error) {
	for _, t := range p.list {
		if t.ID == id {
			c := t
			c.Files = []provider.File{{ID: "1", Path: "/" + t.Name + "/f.mkv", Selected: true}}
			return &c, nil
		}
	}
	return nil, provider.ErrIncomplete
}
func (p *torrentProvider) Cache() provider.CacheSupport { return provider.CacheSupport{} }
func (p *torrentProvider) CheckCached(context.Context, provider.CacheQuery) (map[string]bool, error) {
	return nil, provider.ErrCacheUnsupported
}

type torrentSource struct{ m *torrents.Manager }

func (s torrentSource) Manager(context.Context) (*torrents.Manager, error) { return s.m, nil }

func TestTorrentsAPI(t *testing.T) {
	t.Parallel()
	const h1, h2 = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	old := testNow.Add(-72 * time.Hour)
	prov := &torrentProvider{list: []provider.Torrent{
		{ID: "10", Hash: h1, Name: "Unused.Movie.2020.1080p", Bytes: 100, Added: old, State: provider.StateDownloaded},
		{ID: "11", Hash: h2, Name: "Other.Movie.2021.1080p", Bytes: 200, Added: old, State: provider.StateDownloaded},
	}}
	pol := &policy.Policy{Providers: map[string]*policy.Provider{"rd": {Kind: provider.KindRealDebrid, Order: 1}}}
	m := &torrents.Manager{Policy: pol, Providers: map[string]provider.Provider{"rd": prov},
		Dir: filepath.Join(t.TempDir(), "torrent-backups"), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return testNow }}
	e := newEnvWith(t, func(c *Config) { c.Torrents = torrentSource{m} })
	m.Store = e.st
	e.exec(`INSERT INTO content (info_hash, first_seen_at, last_seen_at) VALUES (?, 0, 0), (?, 0, 0)`, h1, h2)
	e.exec(`INSERT INTO provider_sync_run (provider, started_at, finished_at, ok) VALUES ('rd', 0, 0, 1)`)
	sess := e.login()
	csrf := e.csrfFor(sess)
	key := e.apiKey()

	var rep torrents.Report
	if code := e.getKey(APIBase+"/torrents/report", key, &rep); code != 200 || len(rep.Unused) != 2 ||
		rep.Providers[0].Usage.Unused != 2 {
		t.Fatalf("report: %d %+v", code, rep)
	}

	// Backups: none yet; an import of a DMM backup needs the provider.
	var bl struct {
		Backups []torrents.BackupInfo `json:"backups"`
		Keep    int                   `json:"keep"`
	}
	if code := e.getKey(APIBase+"/torrents/backups", key, &bl); code != 200 || len(bl.Backups) != 0 ||
		bl.Keep != policy.DefaultTorrentBackupKeep {
		t.Fatalf("backups: %d %+v", code, bl)
	}
	dmm := `[{"filename":"Unused.Movie.2020.1080p","hash":"` + h1 + `"}]`
	importReq := func(q string) int {
		r := httptest.NewRequest("POST", APIBase+"/torrents/backups/import"+q, strings.NewReader(dmm))
		r.AddCookie(sess)
		r.Header.Set(auth.CSRFHeader, csrf)
		return e.do(r).Code
	}
	if code := importReq(""); code != 400 {
		t.Errorf("a DMM import without a provider: %d", code)
	}
	if code := importReq("?provider=rd"); code != 201 {
		t.Fatalf("import: %d", code)
	}
	if code := e.getKey(APIBase+"/torrents/backups", key, &bl); code != 200 || len(bl.Backups) != 1 {
		t.Fatalf("backups after the import: %+v", bl)
	}
	id := bl.Backups[0].ID
	r := httptest.NewRequest("GET", APIBase+"/torrents/backups/"+id+"?format=dmm", nil)
	r.Header.Set(auth.APIKeyHeader, key)
	got := e.do(r)
	if got.Code != 200 || !strings.Contains(got.Header().Get("Content-Disposition"), "attachment") ||
		!strings.Contains(got.Body.String(), h1) {
		t.Fatalf("download: %d %s %s", got.Code, got.Header(), got.Body)
	}
	if code := e.getKey(APIBase+"/torrents/backups/20260101T000000.000Z-manual", key, nil); code != 404 {
		t.Errorf("a missing backup: %d", code)
	}

	// Restore preview: the hash is still on the account.
	var plan torrents.RestorePlan
	if code := e.jsonCall("POST", APIBase+"/torrents/restore/preview", map[string]any{"backup_id": id}, sess, csrf,
		&plan); code != 200 || plan.Counts[torrents.ActPresent] != 1 {
		t.Fatalf("restore preview: %d %+v", code, plan)
	}
	var unconf struct {
		Error string `json:"error"`
	}
	if code := e.jsonCall("POST", APIBase+"/torrents/restore", map[string]any{"backup_id": id,
		"confirmation_token": "r1-9-nope"}, sess, csrf, &unconf); code != 422 {
		t.Errorf("restore with a wrong token: %d", code)
	}

	// Delete: preview, a wrong token, then the confirmed set. No adder here, so the delete fails after the backup.
	sel := map[string]any{"items": []map[string]any{{"provider": "rd", "id": "10", "hash": h1}}}
	var dp torrents.DeletePlan
	if code := e.jsonCall("POST", APIBase+"/torrents/delete/preview", sel, sess, csrf, &dp); code != 200 ||
		dp.Count != 1 || dp.Token == "" || dp.Warning == "" {
		t.Fatalf("delete preview: %d %+v", code, dp)
	}
	wrong := map[string]any{"items": sel["items"], "confirmation_token": "d1-1-100-x"}
	if code := e.jsonCall("POST", APIBase+"/torrents/delete", wrong, sess, csrf, nil); code != 422 {
		t.Errorf("delete with a wrong token: %d", code)
	}
	if code := e.jsonCall("POST", APIBase+"/torrents/delete", wrong, sess, "", nil); code != 403 {
		t.Errorf("delete without CSRF: %d", code)
	}
	ok := map[string]any{"items": sel["items"], "confirmation_token": dp.Token}
	// The token is bound to the caller: the same admin's API key gets its own, and the session's is refused to it.
	keyCall := func(path string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", APIBase+path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(auth.APIKeyHeader, key)
		return e.do(r)
	}
	var kp torrents.DeletePlan
	if got := keyCall("/torrents/delete/preview", sel); got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &kp) != nil ||
		kp.Token == "" || kp.Token == dp.Token {
		t.Fatalf("key delete preview: %d %s", got.Code, got.Body)
	}
	if got := keyCall("/torrents/delete", ok); got.Code != 422 {
		t.Errorf("the session's token from a key: %d", got.Code)
	}
	var done torrents.DeletePlan
	if code := e.jsonCall("POST", APIBase+"/torrents/delete", ok, sess, csrf, &done); code != 200 ||
		done.BackupID == "" || done.Failed != 1 {
		t.Fatalf("delete: %d %+v", code, done)
	}
	var j struct {
		Entries []torrents.JournalEntry `json:"entries"`
	}
	if code := e.getKey(APIBase+"/torrents/journal", key, &j); code != 200 || len(j.Entries) < 2 ||
		j.Entries[0].Action != "delete" || j.Entries[0].By == "" {
		t.Fatalf("journal: %d %+v", code, j)
	}
	body, _ := json.Marshal(j)
	if bytes.Contains(body, []byte("api_key")) {
		t.Error("no keys in the journal")
	}
}

func TestTorrentsAPINotRunning(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if code := e.getKey(APIBase+"/torrents/report", e.apiKey(), nil); code != 503 {
		t.Errorf("no torrent manager: %d", code)
	}
}

// A user's API key reaches delete and restore as its owner would (no torrent manager here: 503, not 403).
func TestTorrentsDeleteAndRestoreWithKey(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	for _, path := range []string{"/torrents/delete", "/torrents/restore"} {
		r := httptest.NewRequest("POST", APIBase+path, strings.NewReader(`{"confirmation_token":"t"}`))
		r.Header.Set(auth.APIKeyHeader, key)
		if got := e.do(r); got.Code != 503 {
			t.Errorf("%s with an API key: %d %s", path, got.Code, got.Body)
		}
	}
	for _, rt := range e.web.Routes() {
		if strings.HasPrefix(rt.Pattern, "POST "+APIBase+"/torrents/") && rt.Access != SignedIn {
			t.Errorf("%s should be open to keys: %v", rt.Pattern, rt.Access)
		}
	}
}
