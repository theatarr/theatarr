package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/autobuild"
)

// animeBuildEnv serves the build endpoints with a real builder whose sources are never read (no worker runs the
// jobs it queues).
func animeBuildEnv(t *testing.T, off bool) *env {
	return newEnvWith(t, func(c *Config) {
		var s *autobuild.Service
		c.AnimeBuilds = func(context.Context) AnimeBuilder {
			if s == nil {
				cfg := autobuild.Config{Policy: c.Policy, Store: c.Store, Queue: jobs.NewQueue(c.Store), Now: c.Now,
					Sources: func(context.Context) (*anime.Sources, error) { return nil, errors.New("not in tests") }}
				if off {
					cfg.Sources, cfg.Off = nil, "metadata.sources lacks anilist"
				}
				s = autobuild.New(cfg)
			}
			return s
		}
	})
}

func (e *env) postKeyJSON(path, key, body string) (int, map[string]any) {
	e.t.Helper()
	r := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
	r.Header.Set(auth.APIKeyHeader, key)
	r.Header.Set("Content-Type", "application/json")
	rec := e.do(r)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (e *env) buildJobs() int {
	e.t.Helper()
	var n int
	if err := e.st.Reader().QueryRow(`SELECT count(*) FROM job WHERE kind = ?`, autobuild.JobKind).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// Every build endpoint is admin-only; a browsing account reads the franchise list (with its build state) only.
func TestAnimeBuildPerms(t *testing.T) {
	t.Parallel()
	e := animeBuildEnv(t, false)
	e.seedAnime()
	keys := map[string]string{}
	for _, role := range []string{auth.RoleUser, auth.RoleAdmin} {
		uid := e.account(role+"-acct", role, auth.StatusActive, true)
		key, _, err := e.auth.CreateKey(context.Background(), role+"-key", uid)
		if err != nil {
			t.Fatal(err)
		}
		keys[role] = key
	}
	if code := e.getKey(APIBase+"/anime/uncovered", keys[auth.RoleUser], nil); code != 403 {
		t.Errorf("user uncovered: %d", code)
	}
	for _, c := range []struct{ path, body string }{
		{APIBase + "/anime/franchises/mushoku-tensei/build", ""},
		{APIBase + "/anime/build", `{"anilist_id": 108465}`},
	} {
		if code, _ := e.postKeyJSON(c.path, keys[auth.RoleUser], c.body); code != 403 {
			t.Errorf("user %s: %d", c.path, code)
		}
		r := httptest.NewRequest("POST", c.path, bytes.NewBufferString(c.body))
		if code := e.do(r).Code; code != 401 {
			t.Errorf("anonymous %s: %d", c.path, code)
		}
	}
	if n := e.buildJobs(); n != 0 {
		t.Fatalf("a refused request queued %d jobs", n)
	}
	if code := e.getKey(APIBase+"/anime/franchises", keys[auth.RoleUser], nil); code != 200 {
		t.Errorf("user franchises: %d", code)
	}
	if code := e.getKey(APIBase+"/anime/uncovered", keys[auth.RoleAdmin], nil); code != 200 {
		t.Errorf("admin uncovered: %d", code)
	}
	if code, _ := e.postKeyJSON(APIBase+"/anime/franchises/mushoku-tensei/build", keys[auth.RoleAdmin], ""); code != 202 {
		t.Errorf("admin rebuild: %d", code)
	}
}

func TestAnimeBuildAPI(t *testing.T) {
	t.Parallel()
	e := animeBuildEnv(t, false)
	_, tvdb := e.seedAnime()
	key := e.apiKey()
	// another anime series, not mapped by any franchise
	e.exec(`INSERT INTO title (id, kind, name, tvdb_id, anime, created_at, updated_at) VALUES (10, 'series', 'Frieren', 424536, 1, 0, 0)`)
	e.exec(`INSERT INTO title (id, kind, name, tvdb_id, anime, created_at, updated_at) VALUES (11, 'series', 'Not Anime', 5000, 0, 0, 0)`)

	var un AnimeUncovered
	if code := e.getKey(APIBase+"/anime/uncovered", key, &un); code != 200 || !un.Builds.Ready || !un.Builds.AutoBuild ||
		un.Builds.RefreshInterval != "168h0m0s" || len(un.Items) != 1 || un.Items[0].TVDBID != 424536 || un.Items[0].Outcome != "" || un.Items[0].Job != nil {
		t.Fatalf("uncovered %d %+v", code, un)
	}

	// rebuild a franchise: 202 with the job; the same request again is the same job
	code, out := e.postKeyJSON(APIBase+"/anime/franchises/mushoku-tensei/build", key, "")
	if code != 202 || out["job_id"] == nil || out["slug"] != "mushoku-tensei" {
		t.Fatalf("rebuild %d %v", code, out)
	}
	if code, again := e.postKeyJSON(APIBase+"/anime/franchises/mushoku-tensei/build", key, ""); code != 202 || again["job_id"] != out["job_id"] {
		t.Errorf("rebuild again %d %v, first %v", code, again, out)
	}
	if code, _ := e.postKeyJSON(APIBase+"/anime/franchises/nope/build", key, ""); code != 404 {
		t.Errorf("unknown franchise %d", code)
	}
	var fl struct {
		Items []AnimeFranchise `json:"items"`
	}
	if code := e.getKey(APIBase+"/anime/franchises", key, &fl); code != 200 || len(fl.Items) != 1 || fl.Items[0].Job == nil ||
		fl.Items[0].Job.State != "queued" || fl.Items[0].NextRefresh == nil || fl.Items[0].BuiltAt == nil ||
		fl.Items[0].NextRefresh.Sub(*fl.Items[0].BuiltAt).Hours() != 168 {
		t.Fatalf("franchises %d %+v", code, fl.Items)
	}
	var one AnimeFranchiseDetail
	if code := e.getKey(APIBase+"/anime/franchises/mushoku-tensei", key, &one); code != 200 || one.Job == nil ||
		one.Job.State != "queued" || one.NextRefresh == nil {
		t.Fatalf("franchise %d job %+v next %v", code, one.Job, one.NextRefresh)
	}

	// build for an anime series: queued, recorded, and shown with its job
	if code, out := e.postKeyJSON(APIBase+"/anime/build", key, `{"tvdb_id": 424536}`); code != 202 || out["job_id"] == nil {
		t.Fatalf("build tvdb %d %v", code, out)
	}
	if code := e.getKey(APIBase+"/anime/uncovered", key, &un); code != 200 || len(un.Items) != 1 || un.Items[0].Outcome != "queued" ||
		un.Items[0].Job == nil || un.Items[0].Job.State != "queued" || un.Items[0].TriedAt == nil {
		t.Fatalf("uncovered after build %d %+v", code, un.Items)
	}
	// a failed discovery shows its outcome and the job's error
	e.exec(`UPDATE anime_discovery SET outcome = 'failed', detail = 'anilist: down' WHERE title_id = 10`)
	e.exec(`UPDATE job SET state = 'failed', last_error = 'anilist: down' WHERE kind = 'anime_build' AND dedupe_key = ?`, autobuild.TVDBKey(424536))
	if code := e.getKey(APIBase+"/anime/uncovered", key, &un); code != 200 || un.Items[0].Outcome != "failed" ||
		un.Items[0].Detail != "anilist: down" || un.Items[0].Job.State != "failed" || un.Items[0].Job.Error != "anilist: down" {
		t.Fatalf("failed %d %+v", code, un.Items)
	}
	for _, c := range []struct {
		body string
		code int
	}{
		{`{"tvdb_id": 5000}`, 404}, // not an anime series
		{`{"tvdb_id": ` + itoa(int64(tvdb)) + `}`, 202},
		{`{"anilist_id": 154587, "name": "Frieren: Beyond Journey's End"}`, 202},
		{`{"anilist_id": 1, "slug": "mushoku-tensei"}`, 409}, // another seed has the slug
		{`{"anilist_id": 1, "slug": "Bad Slug"}`, 400},
		{`{"tvdb_id": 1, "anilist_id": 1}`, 400},
		{`{"tvdb_id": 1, "slug": "x"}`, 400},
		{`{}`, 400},
		{`{"anilist_id": -3}`, 400},
		{`not json`, 400},
	} {
		code, out := e.postKeyJSON(APIBase+"/anime/build", key, c.body)
		if code != c.code {
			t.Errorf("%s: %d %v, want %d", c.body, code, out, c.code)
		}
		if c.body == `{"anilist_id": 154587, "name": "Frieren: Beyond Journey's End"}` && out["slug"] != "frieren-beyond-journeys-end" {
			t.Errorf("seed slug %v", out)
		}
	}
}

// Without the anime sources, or without a builder, the build endpoints answer 503 and queue nothing; the lists
// still answer, with the reason.
func TestAnimeBuildOff(t *testing.T) {
	t.Parallel()
	e := animeBuildEnv(t, true)
	e.seedAnime()
	key := e.apiKey()
	if code, out := e.postKeyJSON(APIBase+"/anime/franchises/mushoku-tensei/build", key, ""); code != 503 || out["error"] == nil {
		t.Errorf("rebuild %d %v", code, out)
	}
	if code, _ := e.postKeyJSON(APIBase+"/anime/build", key, `{"anilist_id": 5}`); code != 503 {
		t.Errorf("build %d", code)
	}
	var un AnimeUncovered
	if code := e.getKey(APIBase+"/anime/uncovered", key, &un); code != 200 || un.Builds.Ready || un.Builds.Off == "" {
		t.Errorf("uncovered %d %+v", code, un.Builds)
	}
	var fl struct {
		Items []AnimeFranchise `json:"items"`
	}
	if code := e.getKey(APIBase+"/anime/franchises", key, &fl); code != 200 || fl.Items[0].NextRefresh != nil {
		t.Errorf("franchises %d %+v", code, fl.Items)
	}
	if n := e.buildJobs(); n != 0 {
		t.Errorf("%d jobs queued", n)
	}

	none := newEnv(t)
	if code, _ := none.postKeyJSON(APIBase+"/anime/build", none.apiKey(), `{"anilist_id": 5}`); code != 503 {
		t.Errorf("no builder: %d", code)
	}
}
