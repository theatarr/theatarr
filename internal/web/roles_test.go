package web

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/store"
)

// account makes an account with a role and status (and a password unless plex), and returns its id.
func (e *env) account(name, role, status string, plex bool) int64 {
	e.t.Helper()
	now := store.Millis(testNow)
	hash := ""
	if !plex {
		var err error
		if hash, err = auth.HashPassword("password-of-" + name); err != nil {
			e.t.Fatal(err)
		}
	}
	res, err := e.st.Writer().Exec(`INSERT INTO web_user (username, password_hash, created_at, updated_at,
		password_changed_at, role, status) VALUES (?, ?, ?, ?, ?, ?, ?)`, name, hash, now, now, now, role, status)
	if err != nil {
		e.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// session starts a session for an account (whatever its status) and returns the cookie and its CSRF token.
func (e *env) session(uid int64) (*http.Cookie, string) {
	e.t.Helper()
	tok, err := e.auth.StartSession(context.Background(), uid, "192.0.2.1", "test")
	if err != nil {
		e.t.Fatal(err)
	}
	return &http.Cookie{Name: auth.SessionCookie, Value: tok}, e.auth.SessionCSRF(tok)
}

// TestEveryRoutePerRole walks every route as admin, manager, user, a pending and a disabled account, with a session
// and with an API key: a route's declared permission decides (403 without it), pending and disabled accounts are not
// signed in at all, and the admin keeps every route. Admin-only routes are pinned so a user never reaches one.
func TestEveryRoutePerRole(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	type acct struct {
		name, role, status string
		id                 int64
		key                string
	}
	accts := []*acct{
		{name: "admin", role: auth.RoleAdmin, status: auth.StatusActive, id: 1},
		{name: "mgr", role: auth.RoleManager, status: auth.StatusActive},
		{name: "usr", role: auth.RoleUser, status: auth.StatusActive},
		{name: "wait", role: auth.RoleUser, status: auth.StatusPending},
		{name: "gone", role: auth.RoleUser, status: auth.StatusDisabled},
	}
	for _, a := range accts {
		if a.id == 0 {
			a.id = e.account(a.name, a.role, auth.StatusActive, a.name != "mgr")
		}
		k, _, err := e.auth.CreateKey(ctx, "k-"+a.name, a.id)
		if err != nil {
			t.Fatal(err)
		}
		a.key = k
		if a.status != auth.StatusActive {
			s := a.status
			if err := e.auth.UpdateUser(ctx, a.id, auth.UserPatch{Status: &s}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// What a user may never reach, whatever else changes: the admin panel and the device routes.
	adminOnly := []string{"/status", "/health", "/health/dangling", "/jobs", "/gateway", "/policy", "/registry", "/imports", "/shadow/summary",
		"/journal", "/logs", "/sources", "/anime/review", "/anime/build", "/anime/uncovered", "/users", "/decide", "/import", "/setup", "/preflight"}
	for _, p := range adminOnly {
		found := false
		for _, rt := range e.web.Routes() {
			if strings.HasSuffix(rt.Pattern, APIBase+p) {
				found = true
				if rt.Perm&(auth.PermAdmin|auth.PermUsers) == 0 {
					t.Errorf("%s: needs %v, want admin or users", rt.Pattern, rt.Perm)
				}
			}
		}
		if !found {
			t.Errorf("no route %s", p)
		}
	}

	checked := 0
	for _, rt := range e.web.Routes() {
		if rt.Access == Public {
			continue
		}
		method, path := concrete(rt.Pattern)
		// Never sign the test's accounts out or revoke their keys on the way.
		path = strings.Replace(path, "/users/1", "/users/999", 1)
		path = strings.Replace(path, "/keys/1", "/keys/999", 1)
		for _, a := range accts {
			for _, viaKey := range []bool{false, true} {
				if viaKey && rt.Access == SessionOnly && a.status == auth.StatusActive {
					continue // logout, the one session-only route: TestEveryRouteNeedsAuth covers keys on it
				}
				rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
				if strings.HasSuffix(path, "/events") {
					cancel()
					rctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				}
				r := httptest.NewRequest(method, path, nil).WithContext(rctx)
				if viaKey {
					r.Header.Set(auth.APIKeyHeader, a.key)
				} else {
					c, csrf := e.session(a.id)
					r.AddCookie(c)
					r.Header.Set(auth.CSRFHeader, csrf)
				}
				rec := e.do(r)
				cancel()
				checked++
				label := fmt.Sprintf("%s %s as %s (key=%v)", method, path, a.name, viaKey)
				allowed := auth.RolePerms(a.role).Has(rt.Perm)
				switch {
				case a.status != auth.StatusActive:
					if rt.API && rec.Code != http.StatusUnauthorized || !rt.API && rec.Code != http.StatusSeeOther {
						t.Errorf("%s: %d, want not signed in", label, rec.Code)
					}
				case allowed && (rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden):
					t.Errorf("%s: %d %s, want through (needs %v)", label, rec.Code, rec.Body.String(), rt.Perm)
				case !allowed && rec.Code != http.StatusForbidden:
					t.Errorf("%s: %d, want 403 (needs %v)", label, rec.Code, rt.Perm)
				}
			}
		}
	}
	if checked < 500 {
		t.Errorf("only %d requests checked", checked)
	}
}

// TestRolePresets pins the permission matrix (docs/design/users.md).
func TestRolePresets(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		auth.RoleAdmin:   "[browse request auto_approve add manage_requests see_files manage own_keys admin users]",
		auth.RoleManager: "[browse request auto_approve add manage_requests see_files manage own_keys]",
		auth.RoleUser:    "[browse request]",
	}
	for role, w := range want {
		if got := fmt.Sprint(auth.RolePerms(role).Names()); got != w {
			t.Errorf("%s: %s, want %s", role, got, w)
		}
	}
}

const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// TestUserSeesAvailabilityOnly calls every route a user may read, with the library, a download, a blocklist entry
// and the user's own request for the downloading title seeded, and checks the JSON: no paths, hashes, release names,
// release groups, sizes, scores, providers, indexers or arr instances (users see availability only), and (plainLeaks) no quality source, cutoff, file counts or dates, history, upgrade
// explanation, or other requesters. The same calls as an admin show the markers, so the fixture does reach those
// fields; TestManagerSeesFileDetail checks the rest is still there for those who see files. The request event on the
// stream carries no requester ids either.
func TestUserSeesAvailabilityOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	ctx := context.Background()
	now := store.Millis(testNow)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		quality, progress, state, created_at, updated_at) VALUES (1, 1, 2, 'movie-web', ?, 'Alpha.Secret.Release-SECRETGRP',
		'secretindexer', 'rd', 'WEBDL-2160p', 10, 'downloading', ?, ?)`, hashB, now, now)
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, created_at)
		VALUES ('movie-web', ?, 1, 'Alpha.Blocked.Release-SECRETGRP', 'bad', 'router', ?)`, hashB, now)
	uid := e.account("usr", auth.RoleUser, auth.StatusActive, true)
	other := e.account("other", auth.RoleUser, auth.StatusActive, true)
	e.exec(`INSERT INTO request (user_id, kind, tmdb_id, title_id, name, versions, status, created_at, updated_at)
		VALUES (?, 'movie', 101, 1, 'Alpha', '["movie-web","movie-remux"]', 'approved', ?, ?)`, uid, now, now)
	e.exec(`INSERT INTO request_user (request_id, user_id, created_at) VALUES (1, ?, ?)`, other, now)
	ukey, _, err := e.auth.CreateKey(ctx, "k-usr", uid)
	if err != nil {
		t.Fatal(err)
	}
	akey := e.apiKey()
	// Values only file detail carries.
	markers := []string{hashA, hashB, "/movies/", "/anime/", "REMUX-GRP", "Secret", "SECRETGRP", "SubsPlease",
		"radarr_4k_debrid", "sonarr_anime_debrid", "secretindexer", "50000000000", "Alpha.Old"}
	keys := []string{"size_bytes", "providers", "provider", "info_hash", "path", "release_title", "release_group",
		"score", "instance", "indexer", "arr_records", "links", "registry", "hash"}
	var paths []string
	for _, rt := range e.web.Routes() {
		method, path := concrete(rt.Pattern)
		if method != "GET" || rt.Access == Public || !auth.RolePerms(auth.RoleUser).Has(rt.Perm) ||
			!strings.HasPrefix(path, APIBase) || strings.HasSuffix(path, "/events") || strings.HasSuffix(path, "/openapi.json") {
			continue
		}
		paths = append(paths, path)
	}
	b := APIBase
	paths = append(paths, b+"/titles/4", b+"/titles/4/episodes", b+"/versions/4", b+"/activity?state=now",
		b+"/activity?state=history", b+"/activity?state=now&as=admin", b+"/search?q=alpha&scope=library",
		b+"/search?q=delta&scope=library", b+"/titles?kind=series", b+"/requests?status=all", b+"/requests/1",
		b+"/titles/4/episodes/41", b+"/titles/4/episodes/42", b+"/titles/facets?kind=movie",
		b+"/titles/facets?kind=series", b+"/titles/2", b+"/versions?state=filled", b+"/titles?state=filled",
		b+"/titles?availability=available", b+"/titles?quality=2160p", b+"/discover/recent")
	adminSaw := map[string]bool{}
	get := func(path, key string) (int, string) {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set(auth.APIKeyHeader, key)
		rec := e.do(r)
		return rec.Code, rec.Body.String()
	}
	for _, path := range paths {
		_, body := get(path, akey)
		for _, m := range markers {
			if strings.Contains(body, m) {
				adminSaw[m] = true
			}
		}
		code, body := get(path, ukey)
		if code != http.StatusOK {
			if code != http.StatusNotFound && code != http.StatusBadRequest {
				t.Errorf("GET %s as user: %d %s", path, code, body)
			}
			continue
		}
		for _, m := range markers {
			if strings.Contains(body, m) {
				t.Errorf("GET %s as user shows %q: %s", path, m, body)
			}
		}
		var v any
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Errorf("GET %s: not JSON: %v", path, err)
			continue
		}
		for _, k := range leakedKeys(v, keys) {
			t.Errorf("GET %s as user has %q set: %s", path, k, body)
		}
		for _, l := range plainLeaks(v) {
			t.Errorf("GET %s as user: %s: %s", path, l, body)
		}
	}
	// What a user does see: the resolution, and below-cutoff titles as available.
	for path, want := range map[string]string{
		b + "/titles/1":                         `"quality":"2160p"`,
		b + "/titles?kind=series":               `"quality":"1080p"`,
		b + "/activity?state=now":               `"quality":"2160p"`,
		b + "/titles/facets?kind=series":        `{"value":"1080p","count":1}`,
		b + "/titles?availability=available":    `"name":"Beta"`,
		b + "/titles?quality=1080p&kind=series": `"name":"Delta"`,
	} {
		if _, body := get(path, ukey); !strings.Contains(body, want) {
			t.Errorf("GET %s as user: want %s: %s", path, want, body)
		}
	}
	// Source qualities and cutoff filters are refused.
	for _, path := range []string{b + "/titles?quality=Bluray-1080p", b + "/titles?availability=upgrade",
		b + "/titles?state=cutoff_met", b + "/versions?state=cutoff_met"} {
		if code, _ := get(path, ukey); code != http.StatusForbidden {
			t.Errorf("GET %s as user: %d, want 403", path, code)
		}
	}
	for _, m := range []string{hashA, "/movies/", "REMUX-GRP", "SECRETGRP", "radarr_4k_debrid", "50000000000"} {
		if !adminSaw[m] {
			t.Errorf("no admin answer shows %q: the fixture does not reach it", m)
		}
	}
	// A user cannot sort by size, filter by provider or see blocked releases.
	for _, path := range []string{b + "/titles?sort=size", b + "/titles?provider=rd", b + "/activity?state=blocked"} {
		if code, _ := get(path, ukey); code != http.StatusForbidden {
			t.Errorf("GET %s as user: %d, want 403", path, code)
		}
	}
	// ?as= never widens: a user asking for the admin view gets the user view, of the requested title only.
	var page ActivityPage
	_, body := get(b+"/activity?as=admin", ukey)
	if err := json.Unmarshal([]byte(body), &page); err != nil || page.Admin || len(page.Now) != 1 {
		t.Errorf("activity as user with ?as=admin: admin=%v, %d rows (%v)", page.Admin, len(page.Now), err)
	}
	// ?as=user narrows an admin's view.
	if _, body := get(b+"/activity?as=user", akey); strings.Contains(body, "SECRETGRP") {
		t.Errorf("activity as admin with ?as=user shows release names: %s", body)
	}

	// The request event: addressed to the requesters, with no ids in any listener's payload.
	ch := e.web.hub.subscribe()
	defer e.web.hub.unsubscribe(ch)
	e.web.requestChanged(ctx, RequestEvent{ID: 1, Status: ReqApproved, Name: "Alpha", Change: "status"})
	ev := <-ch
	user := PermsOf(auth.RolePerms(auth.RoleUser))
	if !ev.For(&auth.Identity{UserID: uid}, user) || !ev.For(&auth.Identity{UserID: other}, user) {
		t.Fatalf("request event misses a requester: %+v", ev)
	}
	for _, c := range []struct {
		id   int64
		mine bool
	}{{uid, true}, {other, true}, {1, false}} {
		var got map[string]any
		data := ev.payload(&auth.Identity{UserID: c.id})
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if _, ok := got["users"]; ok || got["mine"] == true != c.mine {
			t.Errorf("request event for account %d: %s", c.id, data)
		}
		for _, l := range plainLeaks(got) {
			t.Errorf("request event: %s: %s", l, data)
		}
	}
}

// TestUserAvailabilityIsWatchability: without "see files" a title's availability says whether it can be watched,
// in rows, the title page, the filters and the facets: Alpha, a movie with its Remux and
// no WEB copy, is available (a manager sees partial); Delta, a series part-way with no complete version, is partial
// ("some episodes available"); Epsilon, a movie with no file, is missing (acquiring while it downloads). Home leaves
// out the copy-shortfall rows.
func TestUserAvailabilityIsWatchability(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	now := store.Millis(testNow)
	e.exec(`UPDATE version SET state = 'wanted' WHERE id = 4`)
	e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES
		(5, 'movie', 'Epsilon', 2024, 505, ?, ?)`, now, now)
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES
		(5, 5, 'movie-web', 'wanted', ?, ?)`, now, now)
	uid := e.account("usr", auth.RoleUser, auth.StatusActive, true)
	ukey, _, err := e.auth.CreateKey(context.Background(), "k-usr", uid)
	if err != nil {
		t.Fatal(err)
	}
	akey := e.apiKey()
	b := APIBase
	badge := func(key string) map[string]string {
		var p TitlePage
		if code := e.getKey(b+"/titles", key, &p); code != 200 {
			t.Fatalf("titles: %d", code)
		}
		out := map[string]string{}
		for _, it := range p.Items {
			out[it.Name] = it.Availability
		}
		return out
	}
	if got, want := badge(ukey), map[string]string{"Alpha": "available", "Beta": "available", "Delta": "partial",
		"Epsilon": "missing", "Gamma": "none"}; !maps.Equal(got, want) {
		t.Errorf("user badges %v, want %v", got, want)
	}
	if got := badge(akey); got["Alpha"] != "partial" || got["Beta"] != "upgrade" {
		t.Errorf("admin badges %v: Alpha partial and Beta upgrade expected", got)
	}
	// Epsilon downloading: still missing, and acquiring for both (the one flag a user gets of it).
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		progress, state, created_at, updated_at) VALUES (1, 5, 5, 'movie-web', 'ee', 'Epsilon.2024.WEB', 'idx',
		'WEBDL-1080p', 10, 'downloading', ?, ?)`, now, now)
	for _, key := range []string{ukey, akey} {
		var p TitlePage
		e.getKey(b+"/titles", key, &p)
		for _, it := range p.Items {
			if it.Acquiring != (it.Name == "Epsilon") || it.Name == "Epsilon" && it.Availability != "missing" {
				t.Errorf("%s: acquiring %v, %s", it.Name, it.Acquiring, it.Availability)
			}
		}
	}
	var d TitleDetail
	if e.getKey(b+"/titles/1", ukey, &d); d.Availability != "available" {
		t.Errorf("title 1 as user: %q, want available", d.Availability)
	}
	// The filters agree with the badges.
	for q, want := range map[string]string{"available": "Alpha,Beta", "partial": "Delta", "missing": "Epsilon"} {
		var p TitlePage
		if code := e.getKey(b+"/titles?availability="+q, ukey, &p); code != 200 || names(p) != want {
			t.Errorf("availability=%s as user: %d %q, want %q", q, code, names(p), want)
		}
	}
	var f TitleFacets
	e.getKey(b+"/titles/facets", ukey, &f)
	if want := map[string]int{"available": 2, "partial": 1, "missing": 1, "none": 1}; !maps.Equal(f.Availability, want) {
		t.Errorf("facets as user: %v, want %v", f.Availability, want)
	}
	// Home: no Missing or Below cutoff row for a user; a manager keeps them.
	var home Discover
	e.getKey(b+"/discover", ukey, &home)
	if len(home.Missing.Items) != 0 || len(home.Upgrades.Items) != 0 || len(home.Recent.Items) == 0 {
		t.Errorf("user discover: missing %d, upgrades %d, recent %d", len(home.Missing.Items), len(home.Upgrades.Items),
			len(home.Recent.Items))
	}
	for _, row := range []string{"missing", "upgrades"} {
		var r DiscoverRow
		if code := e.getKey(b+"/discover/"+row, ukey, &r); code != 200 || len(r.Items) != 0 || r.Total != 0 {
			t.Errorf("discover/%s as user: %d, %d items", row, code, len(r.Items))
		}
	}
	e.getKey(b+"/discover", akey, &home)
	if len(home.Missing.Items) == 0 {
		t.Error("admin discover lost the missing row")
	}
}

// plainSource matches what a quality says about the file's source, and the cutoff.
var plainSource = regexp.MustCompile(`(?i)web-?dl|webrip|bluray|remux|hdtv|cutoff`)

// plainLeaks lists what a user-visible answer must not carry beyond leakedKeys: a quality other than a resolution;
// a source label or "cutoff" in any value (version names and keys aside: "4K Remux" names a version); a key about the
// cutoff, upgrades or history with a value; a file count; a version's or file's added date; how many others asked.
func plainLeaks(v any) []string {
	var out []string
	named := map[string]bool{"label": true, "key": true, "version": true, "name": true, "title_name": true}
	var walk func(v any, path []string)
	walk = func(v any, path []string) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				p := append(append([]string{}, path...), k)
				where := strings.Join(p, ".")
				switch {
				case k == "quality":
					if s, _ := val.(string); s != "" && Resolution(s) != s {
						out = append(out, where+" is "+s)
					}
				case strings.Contains(k, "cutoff") || k == "history" && len(path) > 0 || k == "upgrade" || k == "upgrade_seen" ||
					k == "others" || k == "other_users" || k == "users" || k == "below" || k == "seen":
					if !emptyJSON(val) {
						out = append(out, where+" is set")
					}
				case k == "files":
					if _, isNum := val.(float64); isNum && !emptyJSON(val) {
						out = append(out, where+" counts files")
					}
				case k == "added_at" && slices.ContainsFunc(path, func(s string) bool { return s == "versions" || s == "current" }):
					if !emptyJSON(val) {
						out = append(out, where+" dates a file")
					}
				}
				if s, ok := val.(string); ok && !named[k] && plainSource.MatchString(s) {
					out = append(out, where+" says "+s)
				}
				walk(val, p)
			}
		case []any:
			for _, val := range x {
				walk(val, path)
			}
		}
	}
	walk(v, nil)
	return out
}

// TestManagerSeesFileDetail: what TestUserSeesAvailabilityOnly takes from users is still there for those who see
// files: the quality with its source, file counts and dates, history, the cutoff, cutoff states, facets by quality,
// how many others asked, and the source-quality filter.
func TestManagerSeesFileDetail(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	now := store.Millis(testNow)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		quality, progress, state, created_at, updated_at) VALUES (1, 1, 2, 'movie-web', ?, 'Alpha.Release-GRP',
		'idx', 'rd', 'WEBDL-2160p', 10, 'downloading', ?, ?)`, hashB, now, now)
	asker := e.account("asker", auth.RoleUser, auth.StatusActive, true)
	other := e.account("other", auth.RoleUser, auth.StatusActive, true)
	e.exec(`INSERT INTO request (user_id, kind, tmdb_id, title_id, name, versions, status, created_at, updated_at)
		VALUES (?, 'movie', 101, 1, 'Alpha', '["movie-web"]', 'approved', ?, ?)`, asker, now, now)
	e.exec(`INSERT INTO request_user (request_id, user_id, created_at) VALUES (1, ?, ?)`, other, now)
	mgr := e.account("mgr", auth.RoleManager, auth.StatusActive, true)
	key, _, err := e.auth.CreateKey(context.Background(), "k-mgr", mgr)
	if err != nil {
		t.Fatal(err)
	}
	b := APIBase
	for path, want := range map[string][]string{
		b + "/titles/1":                    {"Remux-2160p", `"history":[`, `"files":1`, `"event":"imported"`},
		b + "/titles?kind=series":          {"Bluray-1080p", `"files":2`, `"availability":"upgrade"`},
		b + "/titles/4/episodes":           {`"state":"cutoff_met"`, "Bluray-1080p"},
		b + "/titles/4/episodes/41":        {`"history":`, `"state":"cutoff_met"`},
		b + "/titles/facets?kind=series":   {`{"value":"Bluray-1080p","count":1}`, `"upgrade":`},
		b + "/activity?state=now":          {`"quality":"WEBDL-2160p"`},
		b + "/requests/1":                  {`"others":1`},
		b + "/titles?quality=Bluray-1080p": {`"name":"Delta"`},
	} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set(auth.APIKeyHeader, key)
		rec := e.do(r)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s as manager: %d %s", path, rec.Code, rec.Body.String())
			continue
		}
		for _, w := range want {
			if !strings.Contains(rec.Body.String(), w) {
				t.Errorf("GET %s as manager: want %s: %s", path, w, rec.Body.String())
			}
		}
	}
}

// leakedKeys lists the keys among names that hold a non-empty value anywhere in v.
func leakedKeys(v any, names []string) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if slices.Contains(names, k) && !emptyJSON(val) {
					out = append(out, k)
				}
				walk(val)
			}
		case []any:
			for _, val := range x {
				walk(val)
			}
		}
	}
	walk(v)
	return out
}

func emptyJSON(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case float64:
		return x == 0
	case bool:
		return !x
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// TestDownloadEventsReachRequestersOnly: a download event goes to those who see files and to the requesters of its
// title, not to other users.
func TestDownloadEventsReachRequestersOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	now := store.Millis(testNow)
	a := e.account("asker", auth.RoleUser, auth.StatusActive, true)
	o := e.account("other", auth.RoleUser, auth.StatusActive, true)
	m := e.account("joined", auth.RoleUser, auth.StatusActive, true)
	e.exec(`INSERT INTO request (id, user_id, kind, tmdb_id, title_id, name, status, created_at, updated_at)
		VALUES (7, ?, 'movie', 101, 1, 'Alpha', 'processing', ?, ?)`, a, now, now)
	e.exec(`INSERT INTO request_user (request_id, user_id, created_at) VALUES (7, ?, ?)`, m, now)
	ev, err := e.web.downloadEvent(context.Background(), &activity.Download{TitleID: 1, Version: "movie-web"},
		[]byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	user := PermsOf(auth.RolePerms(auth.RoleUser))
	mgr := PermsOf(auth.RolePerms(auth.RoleManager))
	if !ev.For(&auth.Identity{UserID: a}, user) || !ev.For(&auth.Identity{UserID: m}, user) ||
		ev.For(&auth.Identity{UserID: o}, user) || !ev.For(&auth.Identity{UserID: o}, mgr) {
		t.Errorf("download event audience: %+v", ev)
	}
}

func TestResolution(t *testing.T) {
	t.Parallel()
	for q, want := range map[string]string{"Bluray-1080p": "1080p", "Remux-2160p": "2160p", "WEBDL-2160p": "2160p",
		"WEBRip-720p": "720p", "HDTV-720p": "720p", "Bluray-2160p Remux": "2160p", "Bluray-576p": "576p", "DVD": "",
		"SDTV": "", "Raw-HD": "", "Unknown": "", "": "", "1080p": "1080p"} {
		if got := Resolution(q); got != want {
			t.Errorf("Resolution(%q) = %q, want %q", q, got, want)
		}
	}
}

// TestJoinedUserSeesNoOtherRequester: a household user who joined someone else's request sees neither that member's
// name nor id, as requester or, after auto-approval, as decider, in the request list and the
// detail; the joined request still reads as theirs and approved at creation. The member who asked first still sees
// themselves, and a manager still sees everyone.
func TestJoinedUserSeesNoOtherRequester(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	ctx := context.Background()
	now := store.Millis(testNow)
	first := e.account("firstasker", auth.RoleUser, auth.StatusActive, true)
	joiner := e.account("joiner", auth.RoleUser, auth.StatusActive, true)
	e.exec(`UPDATE web_user SET display_name = 'Zelda Firstasker' WHERE id = ?`, first)
	e.exec(`INSERT INTO request (user_id, kind, tmdb_id, title_id, name, versions, status, decided_by, decided_at,
		created_at, updated_at) VALUES (?, 'movie', 101, 1, 'Alpha', '["movie-web"]', 'approved', ?, ?, ?, ?)`,
		first, first, now, now, now)
	e.exec(`INSERT INTO request_user (request_id, user_id, created_at) VALUES (1, ?, ?)`, joiner, now)
	key := func(name string, id int64) string {
		k, _, err := e.auth.CreateKey(ctx, "k-"+name, id)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	jkey, fkey, akey := key("joiner", joiner), key("first", first), e.apiKey()
	b := APIBase
	for _, path := range []string{b + "/requests?status=all", b + "/requests/1"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set(auth.APIKeyHeader, jkey)
		body := e.do(r).Body.String()
		for _, m := range []string{"firstasker", "Zelda"} {
			if strings.Contains(body, m) {
				t.Errorf("GET %s as the joined user shows %q: %s", path, m, body)
			}
		}
	}
	var d RequestDetail
	if code := e.getKey(b+"/requests/1", jkey, &d); code != 200 || d.User != nil || d.DecidedBy != nil ||
		len(d.OtherUser) != 0 || !d.Mine || !d.ApprovedAtCreation {
		t.Errorf("detail as the joined user: %d user %+v decided_by %+v others %v mine %v at creation %v", code, d.User,
			d.DecidedBy, d.OtherUser, d.Mine, d.ApprovedAtCreation)
	}
	var page RequestPage
	if e.getKey(b+"/requests?status=all", jkey, &page); len(page.Items) != 1 || page.Items[0].User != nil ||
		page.Items[0].DecidedBy != nil {
		t.Errorf("list as the joined user: %+v", page.Items)
	}
	d = RequestDetail{}
	if e.getKey(b+"/requests/1", fkey, &d); d.User == nil || d.User.ID != first || d.DecidedBy == nil ||
		d.DecidedBy.ID != first || !d.ApprovedAtCreation || len(d.OtherUser) != 0 {
		t.Errorf("detail as the first requester: user %+v decided_by %+v others %v", d.User, d.DecidedBy, d.OtherUser)
	}
	d = RequestDetail{}
	if e.getKey(b+"/requests/1", akey, &d); d.User == nil || d.User.DisplayName != "Zelda Firstasker" ||
		d.DecidedBy == nil || d.DecidedBy.ID != first || len(d.OtherUser) != 1 || d.OtherUser[0].ID != joiner ||
		d.Others != 1 {
		t.Errorf("detail as admin: user %+v decided_by %+v others %v (%d)", d.User, d.DecidedBy, d.OtherUser, d.Others)
	}
}
