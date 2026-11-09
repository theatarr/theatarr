package qbit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/qbit"
	"github.com/theatarr/theatarr/internal/qbit/qbittest"
)

const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newClient(t *testing.T, url, user, pass string) *qbit.Client {
	t.Helper()
	c, err := qbit.New("decypharr-movies", &policy.Client{Kind: "decypharr", URL: url, Category: "theatarr-movies"},
		user, pass, qbittest.Doer{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoginCookieAndRelogin(t *testing.T) {
	t.Parallel()
	f := qbittest.New()
	defer f.Close()
	f.User, f.Pass = "http://theatarr:8080/radarr", "view-key-1"
	c := newClient(t, f.URL(), f.User, f.Pass)
	ctx := context.Background()
	for range 2 {
		if _, err := c.Info(ctx, []string{hashA}); err != nil {
			t.Fatal(err)
		}
	}
	if f.Logins != 1 {
		t.Fatalf("logins %d: the SID cookie was not kept", f.Logins)
	}
	f.Expire()
	if _, err := c.Info(ctx, []string{hashA}); err != nil {
		t.Fatalf("after the session expired: %v", err)
	}
	if f.Logins != 2 {
		t.Fatalf("logins %d: no re-login after 403", f.Logins)
	}
	bad := newClient(t, f.URL(), "someone", "wrong")
	if _, err := bad.Info(ctx, []string{hashA}); err == nil || !strings.Contains(err.Error(), "login refused") {
		t.Fatalf("bad credentials: %v", err)
	}
}

func TestAddForm(t *testing.T) {
	t.Parallel()
	var form map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/api/v2/auth/login":
			if r.PostForm.Get("category") != "theatarr-movies" {
				t.Errorf("login form %v", r.PostForm)
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "s1"})
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			if ck, err := r.Cookie("SID"); err != nil || ck.Value != "s1" {
				t.Errorf("add without the SID cookie")
			}
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Errorf("content type %q", r.Header.Get("Content-Type"))
			}
			form = r.PostForm
			_, _ = w.Write([]byte("Ok."))
		}
	}))
	defer srv.Close()
	c := newClient(t, srv.URL+"/", "u", "p")
	if err := c.Add(context.Background(), strings.ToUpper(hashA)); err != nil {
		t.Fatal(err)
	}
	if len(form) != 2 || form["urls"][0] != "magnet:?xt=urn:btih:"+hashA || form["category"][0] != "theatarr-movies" {
		t.Fatalf("add form %v", form)
	}
}

func TestAddErrors(t *testing.T) {
	t.Parallel()
	f := qbittest.New()
	defer f.Close()
	c := newClient(t, f.URL(), "u", "p")
	ctx := context.Background()
	for _, tc := range []struct {
		e    qbittest.AddError
		want qbit.Verdict
	}{
		{qbittest.AddError{Status: 404, Code: "torrent_not_cached", Retryable: true}, qbit.Miss},
		{qbittest.AddError{Status: 451, Code: "torrent_blocked", Permanent: true}, qbit.Refused},
		{qbittest.AddError{Status: 400, Code: "invalid_magnet", Permanent: true}, qbit.Refused},
		{qbittest.AddError{Status: 500, Code: "provider_error", Permanent: true}, qbit.Refused},
		{qbittest.AddError{Status: 503, Code: "traffic_exceeded"}, qbit.Deferred},
		{qbittest.AddError{Status: 509, Code: "too_many_active_downloads", Retryable: true}, qbit.Deferred},
		{qbittest.AddError{Status: 502, Code: "provider_error", Retryable: true}, qbit.Deferred},
	} {
		f.Mu.Lock()
		f.AddErr[hashA] = []qbittest.AddError{tc.e}
		f.Mu.Unlock()
		err := c.Add(ctx, hashA)
		var qe *qbit.Error
		if !errors.As(err, &qe) || qe.Code != tc.e.Code || qe.Status != tc.e.Status {
			t.Fatalf("%s: %v", tc.e.Code, err)
		}
		if got := qbit.Classify(err); got != tc.want {
			t.Errorf("%s (%d): verdict %d, want %d", tc.e.Code, tc.e.Status, got, tc.want)
		}
	}
	// No JSON, a 5xx and a transport error: deferred.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "s"})
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	plain := newClient(t, srv.URL, "u", "p")
	if err := plain.Add(ctx, hashA); err == nil || qbit.Classify(err) != qbit.Deferred {
		t.Fatalf("plain 400: %v", err)
	}
	srv.Close()
	if err := plain.Add(ctx, hashA); err == nil || qbit.Classify(err) != qbit.Deferred {
		t.Fatalf("transport error: %v", err)
	}
	f.Mu.Lock()
	f.Down = true
	f.Mu.Unlock()
	if err := c.Add(ctx, hashA); err == nil || qbit.Classify(err) != qbit.Deferred {
		t.Fatalf("503: %v", err)
	}
}

// decypharrInfo is torrents/info as decypharr v2.7.1 answers it (pkg/server/qbit/types.go Torrent): a finished
// symlinked torrent in our category, one still downloading, and another category's.
const decypharrInfo = `[
 {"hash":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","name":"Movie.2024.2160p.WEB.H265-GRP","size":1000,"progress":1,
  "dlspeed":0,"eta":0,"num_seeds":0,"state":"pausedUP","category":"theatarr-movies",
  "save_path":"/data/symlinks/theatarr-movies","content_path":"/data/symlinks/theatarr-movies/Movie.2024.2160p.WEB.H265-GRP",
  "added_on":1,"completion_on":2,"debrid":"realdebrid","debrid_id":"X1","amount_left":0,"downloaded":1000,
  "magnet_uri":"magnet:?xt=urn:btih:aaaa","files":[]},
 {"hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","name":"Other.2024","size":10,"progress":0.25,"state":"downloading",
  "category":"theatarr-movies","save_path":"/data/symlinks/theatarr-movies","content_path":"","files":null},
 {"hash":"cccccccccccccccccccccccccccccccccccccccc","name":"Theirs","progress":1,"state":"pausedUP","category":"radarr"}
]`

// qbittorrentInfo is a qBittorrent 4.6 answer: more fields, an errored torrent.
const qbittorrentInfo = `[
 {"added_on":1,"amount_left":0,"auto_tmm":false,"availability":-1,"category":"theatarr-movies","completed":5,
  "content_path":"/downloads/Movie.2024.mkv","dlspeed":0,"hash":"dddddddddddddddddddddddddddddddddddddddd",
  "name":"Movie.2024.mkv","num_complete":0,"progress":0.5,"ratio":0,"save_path":"/downloads","size":10,
  "state":"missingFiles","tags":"","total_size":10,"tracker":"","uploaded":0},
 {"category":"theatarr-movies","content_path":"/downloads/Show","hash":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
  "name":"Show","progress":1,"state":"stalledDL","save_path":"/downloads"}
]`

func TestInfoParsing(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body   string
		hashes []string
		want   map[string]qbit.Status
	}{
		// Another category's torrent is read too (a hash an arr added before the handover).
		"decypharr": {decypharrInfo, []string{hashA, strings.Repeat("b", 40), strings.Repeat("c", 40)},
			map[string]qbit.Status{hashA: qbit.StatusReady, strings.Repeat("b", 40): qbit.StatusDownloading,
				strings.Repeat("c", 40): qbit.StatusReady}},
		// stalledDL at progress 1 is still downloading.
		"qbittorrent": {qbittorrentInfo, []string{strings.Repeat("d", 40), strings.Repeat("e", 40)},
			map[string]qbit.Status{strings.Repeat("d", 40): qbit.StatusFailed, strings.Repeat("e", 40): qbit.StatusDownloading}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var queries []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/auth/login" {
					http.SetCookie(w, &http.Cookie{Name: "SID", Value: "s"})
					return
				}
				queries = append(queries, r.URL.RawQuery)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			got, err := newClient(t, srv.URL, "u", "p").Info(context.Background(), tc.hashes)
			if err != nil {
				t.Fatal(err)
			}
			// One hash per request, as decypharr reads hashes (pkg/server/qbit/context.go), and no category.
			if len(queries) != len(tc.hashes) {
				t.Errorf("%d requests for %d hashes", len(queries), len(tc.hashes))
			}
			for i, q := range queries {
				if q != "hashes="+strings.ToLower(tc.hashes[i]) {
					t.Errorf("query %q", q)
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d torrents: %+v", len(got), got)
			}
			for _, tor := range got {
				if st, ok := tc.want[tor.Hash]; !ok || tor.Status() != st {
					t.Errorf("%s: status %d (%s)", tor.Hash, tor.Status(), tor.State)
				}
			}
			if name == "decypharr" && (got[0].Name != "Movie.2024.2160p.WEB.H265-GRP" ||
				got[0].ContentPath != "/data/symlinks/theatarr-movies/Movie.2024.2160p.WEB.H265-GRP") {
				t.Errorf("fields: %+v", got[0])
			}
		})
	}
}

func TestStatusMapping(t *testing.T) {
	t.Parallel()
	for state, want := range map[string]qbit.Status{
		"metaDL": qbit.StatusDownloading, "queuedDL": qbit.StatusDownloading, "downloading": qbit.StatusDownloading,
		"stalledDL": qbit.StatusDownloading, "pausedDL": qbit.StatusDownloading, "checkingDL": qbit.StatusDownloading,
		"forcedDL": qbit.StatusDownloading, "pausedUP": qbit.StatusReady, "stoppedUP": qbit.StatusReady,
		"uploading": qbit.StatusReady, "stalledUP": qbit.StatusReady, "queuedUP": qbit.StatusReady,
		"forcedUP": qbit.StatusReady, "checkingUP": qbit.StatusReady, "error": qbit.StatusFailed,
		"missingFiles": qbit.StatusFailed, "unknown": qbit.StatusDownloading,
	} {
		if got := (qbit.Torrent{State: state, Progress: 0.5}).Status(); got != want {
			t.Errorf("%s: %d, want %d", state, got, want)
		}
	}
	if (qbit.Torrent{State: "error", Progress: 1}).Status() != qbit.StatusFailed {
		t.Error("error at progress 1 is not failed")
	}
	// decypharr reports progress 1 while still downloading, before its links are made: not ready yet.
	for _, state := range []string{"downloading", "unknown"} {
		if (qbit.Torrent{State: state, Progress: 1}).Status() != qbit.StatusDownloading {
			t.Errorf("%s at progress 1 is ready", state)
		}
	}
}
