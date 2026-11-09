package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func openStore(t *testing.T, migrate bool) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "theatarr.db")
	if migrate {
		storetest.Seed(t, path)
	}
	st, err := store.Open(context.Background(), path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if migrate {
		if _, err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Code, rec.Body.String()
}

func TestEndpoints(t *testing.T) {
	st := openStore(t, true)
	reg := NewRegistry("test")
	q := jobs.NewQueue(st)
	RegisterJobGauges(reg, q)
	if _, _, err := q.Enqueue(context.Background(), "meta.art", nil, jobs.EnqueueOptions{}); err != nil {
		t.Fatal(err)
	}
	s := New(Config{Logger: quiet, Registry: reg, Ready: StoreReadiness(st), Version: "test"})
	h := s.Handler()

	if code, body := get(t, h, "/healthz"); code != 200 || body != "ok\n" {
		t.Errorf("/healthz: %d %q", code, body)
	}
	code, body := get(t, h, "/readyz")
	if code != 200 {
		t.Fatalf("/readyz: %d %s", code, body)
	}
	var ready map[string]any
	if err := json.Unmarshal([]byte(body), &ready); err != nil || ready["status"] != "ready" {
		t.Errorf("/readyz body %s (%v)", body, err)
	}
	get(t, h, "/nope")
	code, body = get(t, h, "/metrics")
	if code != 200 {
		t.Fatalf("/metrics: %d", code)
	}
	for _, want := range []string{
		`theatarr_build_info{version="test"} 1`,
		`theatarr_http_requests_total{code="200",method="GET",route="GET /healthz"} 1`,
		`theatarr_http_requests_total{code="404",method="GET",route="unmatched"} 1`,
		`theatarr_jobs{state="queued"} 1`,
		`theatarr_jobs_ready{kind="meta.art"} 1`,
		`theatarr_jobs_oldest_ready_seconds{kind="meta.art"}`,
		"go_goroutines",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
}

func TestNotReadyWithoutMigrations(t *testing.T) {
	st := openStore(t, false)
	s := New(Config{Logger: quiet, Ready: StoreReadiness(st)})
	code, body := get(t, s.Handler(), "/readyz")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "not ready") {
		t.Errorf("/readyz on an unmigrated database: %d %s", code, body)
	}
}

func TestNotReadyWhenClosed(t *testing.T) {
	st := openStore(t, true)
	s := New(Config{Logger: quiet, Ready: StoreReadiness(st)})
	st.Close()
	if code, _ := get(t, s.Handler(), "/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz with a closed database: %d", code)
	}
}

func TestGracefulShutdown(t *testing.T) {
	var logs bytes.Buffer
	log, err := NewLogger(&logs, "debug")
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Logger: log, ShutdownTimeout: 5 * time.Second})
	release := make(chan struct{})
	s.Mux().HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		<-release
		io.WriteString(w, "done")
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx, ln) }()
	base := "http://" + ln.Addr().String()

	slow := make(chan string, 1)
	go func() {
		resp, err := http.Get(base + "/slow")
		if err != nil {
			slow <- "error: " + err.Error()
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		slow <- string(b)
	}()
	time.Sleep(100 * time.Millisecond) // let the slow request arrive
	cancel()
	time.Sleep(50 * time.Millisecond)
	if code, _ := get(t, s.Handler(), "/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz while stopping: %d", code)
	}
	close(release)
	if got := <-slow; got != "done" {
		t.Errorf("in-flight request: %q", got)
	}
	if err := <-served; err != nil {
		t.Errorf("Serve: %v", err)
	}
	// JSON logs.
	line := strings.SplitN(strings.TrimSpace(logs.String()), "\n", 2)[0]
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil || m["msg"] == nil {
		t.Errorf("log line is not JSON: %q", line)
	}
}

func TestRequestLogOmitsQuery(t *testing.T) {
	var logs bytes.Buffer
	log, _ := NewLogger(&logs, "info")
	s := New(Config{Logger: log})
	get(t, s.Handler(), "/api/v3/movie?apikey=supersecret")
	if strings.Contains(logs.String(), "supersecret") {
		t.Errorf("query string logged: %s", logs.String())
	}
}

func TestNewLoggerLevels(t *testing.T) {
	for _, l := range []string{"debug", "info", "warn", "error", "INFO"} {
		if _, err := NewLogger(io.Discard, l); err != nil {
			t.Errorf("%s: %v", l, err)
		}
	}
	if _, err := NewLogger(io.Discard, "loud"); err == nil {
		t.Error("bad level accepted")
	}
}

// Requests the settings runtime's own mux serves (mounted on "/") are labelled with that mux's pattern, not "/".
func TestInnerMuxRouteLabel(t *testing.T) {
	reg := NewRegistry("test")
	s := New(Config{Logger: quiet, Registry: reg, Version: "test"})
	type innerKey struct{}
	inner := http.NewServeMux()
	inner.HandleFunc("GET /api/theatarr/v1/titles/{id}", func(w http.ResponseWriter, r *http.Request) {})
	s.Mux().Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := inner.Handler(r); pattern != "" {
			SetRoute(r, pattern)
		}
		inner.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), innerKey{}, 1)))
	}))
	h := s.Handler()
	get(t, h, "/api/theatarr/v1/titles/7")
	_, body := get(t, h, "/metrics")
	if want := `theatarr_http_requests_total{code="200",method="GET",route="GET /api/theatarr/v1/titles/{id}"} 1`; !strings.Contains(body, want) {
		t.Fatalf("/metrics lacks %q", want)
	}
}

// A request a handler serves in-process on the same handler, with its own request's context (the preflight probing
// an arr view), does not relabel the outer request.
func TestInProcessRequestKeepsRouteLabel(t *testing.T) {
	reg := NewRegistry("test")
	s := New(Config{Logger: quiet, Registry: reg, Version: "test"})
	inner := http.NewServeMux()
	var catchAll http.Handler
	inner.HandleFunc("GET /v/movies/radarr/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	inner.HandleFunc("POST /api/theatarr/v1/preflight", func(w http.ResponseWriter, r *http.Request) {
		probe := httptest.NewRequestWithContext(r.Context(), "GET", "/v/movies/radarr/api/v3/system/status", nil)
		catchAll.ServeHTTP(httptest.NewRecorder(), probe)
	})
	catchAll = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := inner.Handler(r); pattern != "" {
			SetRoute(r, pattern)
		}
		inner.ServeHTTP(w, r)
	})
	s.Mux().Handle("/", catchAll)
	h := s.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/theatarr/v1/preflight", nil))
	_, body := get(t, h, "/metrics")
	if want := `theatarr_http_requests_total{code="200",method="POST",route="POST /api/theatarr/v1/preflight"} 1`; !strings.Contains(body, want) {
		t.Fatalf("/metrics lacks %q:\n%s", want, body)
	}
}

// TestErrorInboxCapture: a handler panic is recorded with its stack, answers 500 and the server keeps serving; a 500
// is recorded by route pattern; 4xx, 503, the panic's own 500 and a 500 after the client went away are not recorded
// again.
func TestErrorInboxCapture(t *testing.T) {
	st := openStore(t, true)
	in := errinbox.New(st, errinbox.Options{Logger: quiet})
	errinbox.Install(in)
	t.Cleanup(func() { errinbox.Install(nil) })
	var logs bytes.Buffer
	log, _ := NewLogger(&logs, "info")
	s := New(Config{Logger: log})
	s.Mux().HandleFunc("GET /boom/{id}", func(w http.ResponseWriter, r *http.Request) {
		var s []int
		_ = s[len(r.PathValue("id"))] // index out of range
	})
	s.Mux().HandleFunc("GET /fail/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	})
	s.Mux().HandleFunc("GET /off", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not running", http.StatusServiceUnavailable)
	})
	s.Mux().HandleFunc("GET /gone", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError) // as w.internal on a cancelled query
	})
	s.Mux().HandleFunc("GET /bad", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusBadRequest)
	})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	status := func(path string) int {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c := status("/boom/1"); c != http.StatusInternalServerError {
		t.Errorf("panic answered %d", c)
	}
	if c := status("/healthz"); c != http.StatusOK {
		t.Errorf("after a panic /healthz answered %d", c)
	}
	status("/boom/2")
	status("/fail/1")
	status("/fail/2")
	status("/off")
	status("/bad")
	status("/nothing-here")
	gone, cancel := context.WithCancel(context.Background())
	cancel() // the client closed the connection
	s.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(gone, "GET", "/gone", nil))
	if !strings.Contains(logs.String(), `"level":"INFO","msg":"request","component":"http","method":"GET","path":"/gone","route":"GET /gone","code":499`) {
		t.Errorf("client-closed request not logged as INFO 499: %s", logs.String())
	}
	if err := in.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, err := errinbox.List(context.Background(), st, errinbox.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]errinbox.Row{}
	for _, r := range p.Items {
		got[r.Source+" "+r.Component] = r
	}
	if len(got) != 2 {
		t.Fatalf("rows %v", got)
	}
	pr, ok := got["panic http"]
	if !ok || pr.Count != 2 {
		t.Fatalf("panic row = %+v", pr)
	}
	full, _ := errinbox.Get(context.Background(), st, pr.ID)
	if !strings.Contains(full.Stack, "TestErrorInboxCapture") || !strings.Contains(string(full.Context), "GET /boom/{id}") {
		t.Errorf("panic row lacks stack or route: %+v", full)
	}
	if hr, ok := got["http GET /fail/{id}"]; !ok || hr.Count != 2 || hr.Message != "answered <n>" {
		t.Errorf("http row = %+v", hr)
	}
	if !strings.Contains(logs.String(), "handler panicked") {
		t.Errorf("panic not logged: %s", logs.String())
	}
}
