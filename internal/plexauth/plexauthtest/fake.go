// Package plexauthtest is a fake plex.tv for tests: pins, the account and its resources, answered from fixtures in
// plex.tv's shapes (testdata, hand-written from plex.tv's documented answers; no live data). It serves as an
// http.Handler and as the gateway Doer plexauth uses, so tests send nothing off the machine.
package plexauthtest

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/plexauth"
)

//go:embed testdata/*.json
var fixtures embed.FS

// Account is a plex.tv account the fake knows.
type Account struct {
	ID       int64
	Username string
	Title    string
	Email    string
	// Servers are the machineIdentifiers in the account's resources.
	Servers []string
}

// Fake is a fake plex.tv. Safe for concurrent use.
type Fake struct {
	mu       sync.Mutex
	next     int64
	pins     map[int64]*pin
	accounts map[string]Account // by token
	// Calls counts requests by "METHOD path".
	Calls map[string]int
	// Leaks records any request that carried a token outside the X-Plex-Token header (it must never happen).
	Leaks []string
}

type pin struct {
	code  string
	token string
}

// New makes an empty fake.
func New() *Fake {
	return &Fake{next: 1000, pins: map[int64]*pin{}, accounts: map[string]Account{}, Calls: map[string]int{}}
}

// Confirm stands for the person confirming the pin with code at app.plex.tv as account a: the next poll of that pin
// gets a token for a.
func (f *Fake) Confirm(code string, a Account) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, p := range f.pins {
		if p.code == code {
			tok := fmt.Sprintf("tok-%d-%d", id, a.ID)
			p.token = tok
			f.accounts[tok] = a
			return nil
		}
	}
	return fmt.Errorf("no pin with code %q", code)
}

// ConfirmLatest confirms the newest pin.
func (f *Fake) ConfirmLatest(a Account) error {
	f.mu.Lock()
	var code string
	var best int64
	for id, p := range f.pins {
		if id > best {
			best, code = id, p.code
		}
	}
	f.mu.Unlock()
	return f.Confirm(code, a)
}

func fixture(name string) map[string]any {
	b, err := fixtures.ReadFile("testdata/" + name)
	if err != nil {
		panic(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		panic(err)
	}
	return v
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls[r.Method+" "+pathKey(r.URL.Path)]++
	if strings.Contains(strings.ToLower(r.URL.RawQuery), "token") {
		f.Leaks = append(f.Leaks, r.URL.String())
	}
	w.Header().Set("Content-Type", "application/json")
	cid := r.Header.Get("X-Plex-Client-Identifier")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v2/pins":
		if cid == "" {
			http.Error(w, `{"errors":[{"code":1000,"message":"X-Plex-Client-Identifier is missing"}]}`, http.StatusBadRequest)
			return
		}
		f.next++
		code := "code" + strconv.FormatInt(f.next, 36)
		f.pins[f.next] = &pin{code: code}
		v := fixture("pin.json")
		v["id"], v["code"], v["clientIdentifier"] = f.next, code, cid
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(v)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v2/pins/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/v2/pins/"), 10, 64)
		p := f.pins[id]
		if p == nil {
			http.Error(w, `{"errors":[{"code":1020,"message":"Resource not found"}]}`, http.StatusNotFound)
			return
		}
		v := fixture("pin.json")
		v["id"], v["code"], v["clientIdentifier"] = id, p.code, cid
		if p.token != "" {
			v["authToken"] = p.token
		}
		json.NewEncoder(w).Encode(v)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v2/user":
		a, ok := f.accounts[r.Header.Get("X-Plex-Token")]
		if !ok {
			http.Error(w, `{"errors":[{"code":1001,"message":"User could not be authenticated"}]}`, http.StatusUnauthorized)
			return
		}
		v := fixture("user.json")
		v["id"], v["username"], v["title"], v["email"] = a.ID, a.Username, a.Title, a.Email
		v["uuid"] = fmt.Sprintf("uuid%d", a.ID)
		v["thumb"] = fmt.Sprintf("https://plex.tv/users/uuid%d/avatar?c=1", a.ID)
		v["authToken"] = r.Header.Get("X-Plex-Token")
		json.NewEncoder(w).Encode(v)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v2/resources":
		a, ok := f.accounts[r.Header.Get("X-Plex-Token")]
		if !ok {
			http.Error(w, `{"errors":[{"code":1001,"message":"User could not be authenticated"}]}`, http.StatusUnauthorized)
			return
		}
		b, _ := fixtures.ReadFile("testdata/resources.json")
		var all []map[string]any
		json.Unmarshal(b, &all)
		out := []map[string]any{all[1]}
		for _, s := range a.Servers {
			srv := map[string]any{}
			for k, v := range all[0] {
				srv[k] = v
			}
			srv["clientIdentifier"] = s
			out = append(out, srv)
		}
		json.NewEncoder(w).Encode(out)
	default:
		http.NotFound(w, r)
	}
}

func pathKey(p string) string {
	if strings.HasPrefix(p, "/api/v2/pins/") {
		return "/api/v2/pins/{id}"
	}
	return p
}

// Doer is the fake as the gateway's Do: it answers requests for plexauth's target and refuses any other.
func (f *Fake) Do(ctx context.Context, t gateway.Target, req *http.Request) (*http.Response, error) {
	if t != plexauth.Target {
		return nil, fmt.Errorf("plexauthtest: request for target %v", t)
	}
	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, req)
	return rec.Result(), nil
}
