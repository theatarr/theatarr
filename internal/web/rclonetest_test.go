package web

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/nowplaying"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestRcloneConnectionTest(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"ok", "auth", "timeout", "echo"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			done := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/core/version" || r.Method != "POST" {
					t.Errorf("request %s %s", r.Method, r.URL.Path)
				}
				user, pass, _ := r.BasicAuth()
				if user != "fixture-user" || pass != "fixture-password" {
					t.Error("missing auth")
				}
				switch mode {
				case "auth":
					http.Error(w, "fixture-user fixture-password", http.StatusUnauthorized)
				case "timeout":
					select {
					case <-r.Context().Done():
					case <-done:
					}
				case "echo":
					_, _ = w.Write([]byte(`{"version":"fixture-user fixture-password"}`))
				default:
					_, _ = w.Write([]byte(`{"version":"v1.70.0"}`))
				}
			}))
			defer server.Close()
			defer close(done)
			e := newEnvWith(t, func(c *Config) {
				c.RcloneTestGateway = func(ctx context.Context, p *policy.Policy, secrets []string) (nowplaying.Doer, error) {
					return gateway.New(ctx, p, gateway.Options{Logger: slog.New(slog.NewTextHandler(&logs, nil)), Secrets: secrets})
				}
			})
			payload, _ := json.Marshal(map[string]any{"url": server.URL, "username": "fixture-user", "password": "fixture-password"})
			r := httptest.NewRequest("POST", APIBase+"/nowplaying/rclone/test", bytes.NewReader(payload))
			r.Header.Set(auth.APIKeyHeader, e.apiKey())
			rec := e.do(r)
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			var result struct {
				OK      bool   `json:"ok"`
				Version string `json:"version"`
				Error   string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.OK != (mode == "ok" || mode == "echo") {
				t.Fatalf("%+v", result)
			}
			if mode == "ok" && result.Version != "v1.70.0" {
				t.Fatalf("%+v", result)
			}
			if mode == "timeout" && !strings.Contains(result.Error, "5 seconds") {
				t.Fatalf("%+v", result)
			}
			for _, secret := range []string{"fixture-user", "fixture-password"} {
				if strings.Contains(rec.Body.String()+logs.String(), secret) {
					t.Fatal("credential leaked")
				}
			}
		})
	}
}
func TestRcloneTestRequiresAdmin(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	uid := e.account("viewer", auth.RoleUser, auth.StatusActive, true)
	key, _, err := e.auth.CreateKey(context.Background(), "viewer", uid)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", APIBase+"/nowplaying/rclone/test", strings.NewReader(`{"url":"http://127.0.0.1:5572"}`))
	r.Header.Set(auth.APIKeyHeader, key)
	if got := e.do(r); got.Code != 403 {
		t.Fatalf("%d", got.Code)
	}
}
func TestRcloneSectionSaveSecretsWithoutEnvNames(t *testing.T) {
	t.Parallel()
	se := seededSectionsEnv(t)
	read := se.want(se.call("GET", "/sections", nil), 200, "read")
	config := findSection(t, read, "library")["config"].(map[string]any)
	config["mount"].(map[string]any)["rclone_rc"] = map[string]any{"url": "http://127.0.0.1:5572"}
	body := map[string]any{"generation": read["generation"], "config": config, "secrets": map[string]string{"mount.rclone_rc.username": "fixture-user", "mount.rclone_rc.password": "fixture-password"}}
	res := se.call("PUT", "/sections/library", body)
	if res.Code == 422 {
		body["confirmation_token"] = confirmRefusal(t, se.want(res, 422, "confirm"), "")
		res = se.call("PUT", "/sections/library", body)
	}
	se.want(res, 200, "saved")
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rc := snap.Policy.Library.Mount.RcloneRC
	if snap.Policy.Getenv(rc.UsernameEnv) != "fixture-user" || snap.Policy.Getenv(rc.PasswordEnv) != "fixture-password" {
		t.Fatal("credentials not applied")
	}
	read = se.want(se.call("GET", "/sections", nil), 200, "read saved")
	b, _ := json.Marshal(read)
	if strings.Contains(string(b), "fixture-password") || strings.Contains(string(b), "fixture-user") {
		t.Fatal("credential in read")
	}
}

func TestRcloneTestStoredCredentialsStayAtSavedURL(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		user, pass, _ := r.BasicAuth()
		if user != "saved-user" || pass != "saved-password" {
			t.Error("stored credentials missing")
		}
		_, _ = w.Write([]byte(`{"version":"v1.70.0"}`))
	}))
	defer server.Close()
	se := seededSectionsEnv(t)
	se.web.rcloneTestGateway = func(ctx context.Context, p *policy.Policy, secrets []string) (nowplaying.Doer, error) {
		return gateway.New(ctx, p, gateway.Options{Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), Secrets: secrets})
	}
	read := se.want(se.call("GET", "/sections", nil), 200, "read")
	config := findSection(t, read, "library")["config"].(map[string]any)
	config["mount"].(map[string]any)["rclone_rc"] = map[string]any{"url": server.URL}
	body := map[string]any{"generation": read["generation"], "config": config, "secrets": map[string]string{"mount.rclone_rc.username": "saved-user", "mount.rclone_rc.password": "saved-password"}}
	res := se.call("PUT", "/sections/library", body)
	if res.Code == 422 {
		body["confirmation_token"] = confirmRefusal(t, se.want(res, 422, "confirm"), "")
		res = se.call("PUT", "/sections/library", body)
	}
	se.want(res, 200, "save")
	key := se.apiKey()
	for _, url := range []string{server.URL, server.URL + "/different"} {
		payload, _ := json.Marshal(map[string]any{"url": url, "use_stored": true})
		req := httptest.NewRequest("POST", APIBase+"/nowplaying/rclone/test", bytes.NewReader(payload))
		req.Header.Set(auth.APIKeyHeader, key)
		result := se.do(req)
		if result.Code != 200 {
			t.Fatalf("%d: %s", result.Code, result.Body)
		}
		if url == server.URL && !strings.Contains(result.Body.String(), `"ok":true`) {
			t.Fatalf("%s", result.Body)
		}
		if strings.Contains(result.Body.String(), "saved-user") || strings.Contains(result.Body.String(), "saved-password") {
			t.Fatal("credential leaked")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("stored credential sent to changed URL: %d calls", calls.Load())
	}
}

func TestRclonePasswordOnlyUsesStoredUsername(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		user, pass, _ := r.BasicAuth()
		if user != "saved-user" || pass != "new-password" {
			t.Error("stored credentials missing")
		}
		_, _ = w.Write([]byte(`{"version":"v1.70.0"}`))
	}))
	defer server.Close()
	se := seededSectionsEnv(t)
	se.web.rcloneTestGateway = func(ctx context.Context, p *policy.Policy, secrets []string) (nowplaying.Doer, error) {
		return gateway.New(ctx, p, gateway.Options{Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), Secrets: secrets})
	}
	read := se.want(se.call("GET", "/sections", nil), 200, "read")
	config := findSection(t, read, "library")["config"].(map[string]any)
	config["mount"].(map[string]any)["rclone_rc"] = map[string]any{"url": server.URL}
	body := map[string]any{"generation": read["generation"], "config": config, "secrets": map[string]string{"mount.rclone_rc.username": "saved-user", "mount.rclone_rc.password": "saved-password"}}
	res := se.call("PUT", "/sections/library", body)
	if res.Code == 422 {
		body["confirmation_token"] = confirmRefusal(t, se.want(res, 422, "confirm"), "")
		res = se.call("PUT", "/sections/library", body)
	}
	se.want(res, 200, "save")
	key := se.apiKey()
	for _, url := range []string{server.URL, server.URL + "/different"} {
		payload, _ := json.Marshal(map[string]any{"url": url, "password": "new-password", "use_stored_username": true})
		req := httptest.NewRequest("POST", APIBase+"/nowplaying/rclone/test", bytes.NewReader(payload))
		req.Header.Set(auth.APIKeyHeader, key)
		result := se.do(req)
		if result.Code != 200 {
			t.Fatalf("%d: %s", result.Code, result.Body)
		}
		if url == server.URL && !strings.Contains(result.Body.String(), `"ok":true`) {
			t.Fatalf("%s", result.Body)
		}
		if strings.Contains(result.Body.String(), "saved-user") || strings.Contains(result.Body.String(), "saved-password") {
			t.Fatal("credential leaked")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("stored credential sent to changed URL: %d calls", calls.Load())
	}
}
