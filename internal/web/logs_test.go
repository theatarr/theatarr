package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/logring"
	"github.com/theatarr/theatarr/internal/server"
)

func TestAdminLogFiles(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	dir := t.TempDir()
	e.web.logDir = dir
	for _, name := range []string{"theatarr.log", "theatarr.log.1", "theatarr.log.10", "theatarr.log.2", "theatarr.log.secret", "theatarr.log.01", "other"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{\"msg\":\""+name+"\"}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "other"), filepath.Join(dir, "theatarr.log.3")); err != nil {
		t.Fatal(err)
	}
	var files struct {
		Items []LogFile `json:"items"`
	}
	e.call("GET", APIBase+"/logs/files", nil, 200, &files)
	if len(files.Items) != 4 || files.Items[2].Name != "theatarr.log.2" || files.Items[3].Name != "theatarr.log.10" || files.Items[0].Size == 0 || files.Items[0].Modified.IsZero() {
		t.Fatalf("files: %+v", files.Items)
	}
	r := httptest.NewRequest("GET", APIBase+"/logs/files/theatarr.log.1", nil)
	r.Header.Set(auth.APIKeyHeader, e.key)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, r)
	if rec.Code != 200 || rec.Body.String() != "{\"msg\":\"theatarr.log.1\"}\n" || rec.Header().Get("Content-Disposition") != `attachment; filename="theatarr.log.1"` {
		t.Fatalf("download: %d %s", rec.Code, rec.Body.String())
	}
	for _, name := range []string{"theatarr.log.3", "theatarr.log.secret", "theatarr.log.01", "other", "theatarr.log.99", "theatarr.log.%22bad"} {
		e.call("GET", APIBase+"/logs/files/"+name, nil, 404, nil)
	}
	for _, role := range []string{auth.RoleUser, auth.RoleManager} {
		_, key := e.user(role, role)
		for _, path := range []string{"/logs/files", "/logs/files/theatarr.log", "/logs/level"} {
			e.callAs(key, "GET", APIBase+path, nil, 403, nil)
		}
		e.callAs(key, "PUT", APIBase+"/logs/level", map[string]string{"level": "debug"}, 403, nil)
	}
	e.web.logDir = ""
	e.call("GET", APIBase+"/logs/files", nil, 200, &files)
	if len(files.Items) != 0 {
		t.Errorf("unconfigured files: %+v", files)
	}
	e.call("GET", APIBase+"/logs/files/theatarr.log", nil, 404, nil)
	e.call("GET", APIBase+"/logs/level", nil, 503, nil)
}

func TestAdminRuntimeLogLevel(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	var logs bytes.Buffer
	log, level, err := server.NewRuntimeLogger(&logs, "info")
	if err != nil {
		t.Fatal(err)
	}
	e.web.log, e.web.logLevel = log.With("component", "web"), level
	worker := log.With("component", "worker")
	var response struct {
		Level string `json:"level"`
	}
	e.call("GET", APIBase+"/logs/level", nil, 200, &response)
	if response.Level != "INFO" {
		t.Fatalf("default: %+v", response)
	}
	for _, l := range []string{"debug", "warn", "error", "info"} {
		e.call("PUT", APIBase+"/logs/level", map[string]string{"level": l}, 200, &response)
		if response.Level != strings.ToUpper(l) {
			t.Errorf("level: %+v", response)
		}
	}
	e.call("PUT", APIBase+"/logs/level", map[string]string{"level": "loud"}, 400, nil)
	e.call("PUT", APIBase+"/logs/level", map[string]string{"level": "INFO+3"}, 400, nil)
	e.call("PUT", APIBase+"/logs/level", map[string]string{"level": "debug"}, 200, nil)
	worker.Debug("worker changed immediately")
	if !strings.Contains(logs.String(), "worker changed immediately") || strings.Count(logs.String(), "log level changed") != 5 || !strings.Contains(logs.String(), `"to":"ERROR","by":1`) {
		t.Fatalf("audit: %s", logs.String())
	}
	// Cookie-authenticated mutations still require CSRF.
	session := newEnvWith(t, func(c *Config) { c.Logger = log; c.LogLevel = level })
	cookie := session.login()
	if code := session.jsonCall("PUT", APIBase+"/logs/level", map[string]string{"level": "error"}, cookie, "", nil); code != 403 {
		t.Errorf("without CSRF: %d", code)
	}
}

// Sequence cursors drain bursts without depending on wall time; a boot id resets an old cursor on restart.
func TestLogCursor(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	e.web.logs = logring.New(2000)
	handler := e.web.logs.Handler(slog.NewTextHandler(io.Discard, nil))
	add := func(n int, at time.Time) {
		t.Helper()
		rec := slog.NewRecord(at, slog.LevelInfo, fmt.Sprintf("line %d", n), 0)
		if err := handler.Handle(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
	type page struct {
		Items   []logring.Entry `json:"items"`
		BootID  string          `json:"boot_id"`
		After   int64           `json:"after"`
		More    bool            `json:"more"`
		Skipped int64           `json:"skipped"`
	}
	var p page
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	add(1, at)
	e.call("GET", APIBase+"/logs?limit=500", nil, 200, &p)
	if p.BootID == "" || p.After != 1 {
		t.Fatalf("initial cursor: %+v", p)
	}
	boot := p.BootID
	for i := 2; i <= 751; i++ {
		add(i, at.Add(-time.Hour))
	}
	e.call("GET", APIBase+"/logs?limit=500&after=1&boot_id="+boot, nil, 200, &p)
	if len(p.Items) != 500 || p.Items[0].Seq != 2 || p.After != 501 || !p.More || p.Skipped != 0 {
		t.Fatalf("first burst page: %+v", p)
	}
	e.call("GET", APIBase+"/logs?limit=500&after=501&boot_id="+boot, nil, 200, &p)
	if len(p.Items) != 250 || p.Items[0].Seq != 502 || p.After != 751 || p.More {
		t.Fatalf("last burst page: %+v", p)
	}
	// Overflow excluded by the filters advances the cursor without claiming matching lines were lost.
	for i := 752; i <= 3000; i++ {
		add(i, at)
	}
	e.call("GET", APIBase+"/logs?component=absent&after=751&boot_id="+boot, nil, 200, &p)
	if p.Skipped != 0 || len(p.Items) != 0 || p.After != 3000 {
		t.Fatalf("overflow: %+v", p)
	}
	// The new process can be empty or already beyond the old sequence. Either way it must reset by identity.
	e.web.logs = logring.New(2000)
	e.call("GET", APIBase+"/logs?after=3000&boot_id="+boot, nil, 200, &p)
	if p.BootID == boot || p.BootID == "" || p.After != 0 || len(p.Items) != 0 {
		t.Fatalf("restart: %+v", p)
	}

	// A restart already beyond an old low cursor must include its earliest lines, too.
	handler = e.web.logs.Handler(slog.NewTextHandler(io.Discard, nil))
	add(1, at.Add(-2*time.Hour))
	add(2, at.Add(-2*time.Hour))
	e.call("GET", APIBase+"/logs?after=1&boot_id="+boot, nil, 200, &p)
	if len(p.Items) != 2 || p.Items[0].Seq != 1 || p.After != 2 {
		t.Fatalf("nonempty restart: %+v", p)
	}
	for _, bad := range []string{"after=-1", "after=nope"} {
		e.call("GET", APIBase+"/logs?"+bad, nil, 400, nil)
	}
}
