// Package qbittest is a fake qBittorrent Web API for tests: login with a SID cookie, torrents/add and
// torrents/info, with decypharr's JSON errors on demand. It records every request.
package qbittest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/theatarr/theatarr/internal/gateway"
)

// AddError is a refused add as decypharr answers it.
type AddError struct {
	Status    int
	Code      string
	Retryable bool
	Permanent bool
}

// Fake is the fake client. Its fields are guarded by Mu.
type Fake struct {
	Mu sync.Mutex
	// User and Pass are the credentials login accepts; empty accepts any (decypharr without use_auth).
	User, Pass string
	// Torrents are what torrents/info lists, by hash (lower case).
	Torrents map[string]map[string]any
	// AddErr refuses the add of a hash (lower case) once per entry, in order.
	AddErr map[string][]AddError
	// OnAdd, when set, makes the torrent an add creates (hash, category); nil leaves the list unchanged.
	OnAdd func(hash, category string) map[string]any
	// Down answers every request with 503 (an outage).
	Down bool
	// InfoDown answers torrents/info for these hashes (lower case) with 503: a read that fails mid-pass.
	InfoDown map[string]bool
	// Adds are the magnets added, Calls every request path, Logins the logins accepted.
	Adds   []string
	Calls  []string
	Logins int
	// expire makes the next authenticated request answer 403 (a session the client dropped).
	expire bool
	sid    int

	Server *httptest.Server
}

// New starts a fake; it is closed when the test ends (t.Cleanup is the caller's: call Close).
func New() *Fake {
	f := &Fake{Torrents: map[string]map[string]any{}, AddErr: map[string][]AddError{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// Close stops the server.
func (f *Fake) Close() { f.Server.Close() }

// URL is the server's base URL.
func (f *Fake) URL() string { return f.Server.URL }

// Expire drops the current session: the next request answers 403 and the client must log in again.
func (f *Fake) Expire() {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.expire = true
}

// Set lists a torrent in torrents/info.
func (f *Fake) Set(hash string, t map[string]any) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	t["hash"] = strings.ToLower(hash)
	f.Torrents[strings.ToLower(hash)] = t
}

// Remove drops a torrent from torrents/info.
func (f *Fake) Remove(hash string) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	delete(f.Torrents, strings.ToLower(hash))
}

// CallCount counts the requests to a path (e.g. "/api/v2/torrents/add").
func (f *Fake) CallCount(path string) int {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	n := 0
	for _, c := range f.Calls {
		if c == path {
			n++
		}
	}
	return n
}

// AllCalls is a copy of every request path so far.
func (f *Fake) AllCalls() []string {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return append([]string(nil), f.Calls...)
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.Calls = append(f.Calls, r.URL.Path)
	if f.Down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	_ = r.ParseForm()
	if r.URL.Path == "/api/v2/auth/login" {
		if (f.User != "" || f.Pass != "") && (r.PostForm.Get("username") != f.User || r.PostForm.Get("password") != f.Pass) {
			_, _ = w.Write([]byte("Fails."))
			return
		}
		f.Logins++
		f.sid++
		f.expire = false
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: sid(f.sid), Path: "/"})
		_, _ = w.Write([]byte("Ok."))
		return
	}
	if ck, err := r.Cookie("SID"); err != nil || ck.Value != sid(f.sid) || f.expire {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/api/v2/torrents/add":
		u := r.PostForm.Get("urls")
		hash := strings.ToLower(strings.TrimPrefix(u, "magnet:?xt=urn:btih:"))
		if errs := f.AddErr[hash]; len(errs) > 0 {
			e := errs[0]
			f.AddErr[hash] = errs[1:]
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(e.Status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "refused: " + e.Code, "code": e.Code,
				"retryable": e.Retryable, "permanent": e.Permanent})
			return
		}
		f.Adds = append(f.Adds, u)
		if f.OnAdd != nil {
			if t := f.OnAdd(hash, r.PostForm.Get("category")); t != nil {
				t["hash"] = hash
				f.Torrents[hash] = t
			}
		}
		_, _ = w.Write([]byte("Ok."))
	case "/api/v2/torrents/info":
		var out []map[string]any
		cat := r.URL.Query().Get("category")
		// As decypharr 2.7.1 reads them (pkg/server/qbit/context.go hashesContext): each hashes value is one hash,
		// not split on "|".
		for _, h := range r.URL.Query()["hashes"] {
			if f.InfoDown[strings.ToLower(strings.TrimSpace(h))] {
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
			if t := f.Torrents[strings.ToLower(strings.TrimSpace(h))]; t != nil && (cat == "" || t["category"] == cat) {
				out = append(out, t)
			}
		}
		if out == nil {
			out = []map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func sid(n int) string { return "sid-" + strings.Repeat("x", n) }

// Doer sends requests straight out, as the gateway would to a client target.
type Doer struct{}

// Do sends req with the default client.
func (Doer) Do(_ context.Context, _ gateway.Target, req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}
