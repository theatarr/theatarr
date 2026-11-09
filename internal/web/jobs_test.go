package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/store"
)

func TestJobsManageAPI(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	now := store.Millis(testNow)
	// The seed has job 1 (db.nightly, queued) and job 2 (db.nightly, failed: disk full). Add an AllDebrid failure
	// that a later sync resolved, and a failure that stays open.
	e.exec(`INSERT INTO job (id, kind, payload, state, run_at, last_error, created_at, updated_at, finished_at) VALUES
		(3, 'provider.sync', '{}', 'failed', ?, 'alldebrid: AUTH_BLOCKED', ?, ?, ?),
		(4, 'provider.sync', '{}', 'done', ?, NULL, ?, ?, ?),
		(5, 'meta.art', '{"title_id":1}', 'failed', ?, 'tmdb 500', ?, ?, ?)`,
		now, now, now, now, now, now, now, now, now, now, now, now)
	key := e.apiKey()

	var jl JobList
	if code := e.getKey(APIBase+"/jobs?state=failed", key, &jl); code != 200 || jl.Counts["failed"] != 3 ||
		jl.Counts["attention"] != 2 || len(jl.Items) != 3 {
		t.Fatalf("failed jobs: %d %+v", code, jl)
	}
	byID := map[int64]Job{}
	for _, j := range jl.Items {
		byID[j.ID] = j
	}
	if j := byID[3]; j.ResolvedBy != 4 || j.Attention {
		t.Errorf("AD failure: %+v", j)
	}
	if j := byID[2]; j.ResolvedBy != 0 || !j.Attention {
		t.Errorf("nightly failure: %+v", j)
	}
	if code := e.getKey(APIBase+"/jobs?attention=true", key, &jl); code != 200 || len(jl.Items) != 2 {
		t.Errorf("attention: %d %+v", code, jl.Items)
	}
	for _, q := range []string{"attention=maybe", "dismissed=x", "state=attention"} {
		if code := e.getKey(APIBase+"/jobs?"+q, key, nil); code != 400 {
			t.Errorf("%s: %d", q, code)
		}
	}

	post := func(path string, c *http.Cookie, csrf, apiKey string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", APIBase+path, nil)
		if c != nil {
			r.AddCookie(c)
		}
		if csrf != "" {
			r.Header.Set(auth.CSRFHeader, csrf)
		}
		if apiKey != "" {
			r.Header.Set(auth.APIKeyHeader, apiKey)
		}
		return e.do(r)
	}

	// Permissions and CSRF on every write.
	sess := e.login()
	for _, p := range []string{"/jobs/2/retry", "/jobs/2/dismiss", "/jobs/dismiss-resolved"} {
		if rec := post(p, nil, "", ""); rec.Code != 401 {
			t.Errorf("%s without auth: %d", p, rec.Code)
		}
		if rec := post(p, sess, "", ""); rec.Code != 403 {
			t.Errorf("%s with a session and no CSRF token: %d", p, rec.Code)
		}
		if rec := post(p, sess, "forged", ""); rec.Code != 403 {
			t.Errorf("%s with a bad CSRF token: %d", p, rec.Code)
		}
		if rec := post(p, nil, "", "not-a-key"); rec.Code != 401 {
			t.Errorf("%s with a bad key: %d", p, rec.Code)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM job`); n != 5 {
		t.Fatalf("refused writes changed the queue: %d jobs", n)
	}

	// Retry with an API key.
	rec := post("/jobs/2/retry", nil, "", key)
	var rr JobRetry
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &rr) != nil || !rr.Created || rr.RetryOf != 2 ||
		rr.Job.Kind != "db.nightly" || rr.Job.State != "queued" || rr.Job.RetryOf != 2 {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/jobs/2/retry", nil, "", key); rec.Code != 200 {
		t.Errorf("retry while the retry is queued: %d", rec.Code)
	}
	if rec := post("/jobs/4/retry", nil, "", key); rec.Code != 409 {
		t.Errorf("retry a done job: %d", rec.Code)
	}
	if rec := post("/jobs/99/retry", nil, "", key); rec.Code != 404 {
		t.Errorf("retry a missing job: %d", rec.Code)
	}

	// Dismiss with a session and its CSRF token.
	csrf := e.csrfFor(sess)
	rec = post("/jobs/5/dismiss", sess, csrf, "")
	var dj Job
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &dj) != nil || dj.DismissedAt == nil ||
		dj.DismissedBy != "admin" || dj.Attention {
		t.Fatalf("dismiss: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/jobs/1/dismiss", nil, "", key); rec.Code != 409 {
		t.Errorf("dismiss a queued job: %d", rec.Code)
	}
	if code := e.getKey(APIBase+"/jobs?state=failed&dismissed=false", key, &jl); code != 200 || len(jl.Items) != 2 ||
		jl.Counts["attention"] != 0 {
		t.Errorf("after retry and dismiss: %d %+v", code, jl)
	}

	// Dismiss all resolved: job 3 only.
	rec = post("/jobs/dismiss-resolved", nil, "", key)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"dismissed":1}` {
		t.Fatalf("dismiss resolved: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE id = 3 AND dismissed_by = 'key:test-client'`); n != 1 {
		t.Error("job 3 not dismissed by the key")
	}
	// Nothing was deleted.
	if n := e.count(`SELECT COUNT(*) FROM job`); n != 6 {
		t.Errorf("jobs: %d, want 6", n)
	}
}

func (e *env) count(q string) int {
	e.t.Helper()
	var n int
	if err := e.st.Reader().QueryRow(q).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// csrfFor returns the CSRF token of a session, from GET /auth/me.
func (e *env) csrfFor(c *http.Cookie) string {
	e.t.Helper()
	rec := e.get(APIBase+"/auth/me", c)
	var me struct {
		CSRF string `json:"csrf_token"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &me) != nil || me.CSRF == "" {
		e.t.Fatalf("auth/me: %d %s", rec.Code, rec.Body.String())
	}
	return me.CSRF
}

// A user's API key retries a confirmed restore job as its owner would (docs/mcp.md "Confirmation and limits").
func TestConfirmedJobRetryWithKey(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.exec(`INSERT INTO job (id, kind, payload, state, run_at, created_at, updated_at)
  VALUES (3, 'torrents.restore', '{"confirmed":["rd|hash"]}', 'failed', 0, 0, 0)`)
	r := httptest.NewRequest("POST", APIBase+"/jobs/3/retry", nil)
	r.Header.Set(auth.APIKeyHeader, e.apiKey())
	if rec := e.do(r); rec.Code != 201 {
		t.Fatalf("key retry: %d %s", rec.Code, rec.Body.String())
	}
	if e.count(`SELECT COUNT(*) FROM job WHERE retry_of = 3`) != 1 {
		t.Fatal("key retry queued no restore")
	}
}
