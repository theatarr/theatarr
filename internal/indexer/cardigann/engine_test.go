// Port of Mediarium internal/indexers/cardigann_engine_test.go @ f7d1e26 (github.com/rdborg/Mediarium, AGPL-3.0,
// Copyright (C) Ryan Borg and the Mediarium contributors). Changes are listed in the package doc.

package cardigann

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- a fake public site ---

const publicResults = `<html><body>
<table class="results"><thead><tr><th>Name</th></tr></thead><tbody>
<tr>
  <td class="name"><a href="/sub/42/">HD</a> <a href="/torrent/101/The-Fixture-Movie-2024-1080p-WEB-DL-x264-GRP/">The Fixture Movie 2024 1080p...</a></td>
  <td class="seeds">1,234</td><td class="leeches">56</td><td class="date">3 hours ago</td><td class="size">1.5 GB</td>
</tr>
<tr>
  <td class="name"><a href="/sub/1/">Movies</a> <a href="/torrent/102/other/">The Fixture Movie 2024 720p WEB x264-GRP</a></td>
  <td class="seeds">10</td><td class="leeches">2</td><td class="date">2 days ago</td><td class="size">700 MB</td>
</tr>
<tr>
  <td class="name"><a href="/sub/22/">Music</a> <a href="/torrent/103/soundtrack/">The Fixture Movie Soundtrack MP3</a></td>
  <td class="seeds">5</td><td class="leeches">1</td><td class="date">1 week ago</td><td class="size">100 MB</td>
</tr>
</tbody></table></body></html>`

const publicDetails = `<html><body><h1>Details</h1><ul>
<li><a href="https://itorrents.example/torrent/abc.torrent">Torrent file</a></li>
<li><a href="magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=fixture">Magnet</a></li>
</ul></body></html>`

type publicSite struct {
	*httptest.Server
	paths []string
	mu    sync.Mutex
}

func newPublicSite(t *testing.T) *publicSite {
	s := &publicSite{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.EscapedPath())
		s.mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/search/"), strings.HasPrefix(r.URL.Path, "/cat/"):
			io.WriteString(w, publicResults)
		case strings.HasPrefix(r.URL.Path, "/torrent/"):
			io.WriteString(w, publicDetails)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func TestCardigannPublicSearchAndDownload(t *testing.T) {
	site := newPublicSite(t)
	m := fixtureManager(t, "fixturepublic")
	inst := Instance{Key: "ix7", Transport: direct, Name: "Fixture Public", DefinitionID: "fixturepublic", BaseURL: site.URL,
		Settings: map[string]string{"sort": "seeders"}}

	results, err := m.Search(context.Background(), inst, "the fixture movie S2024", []int{2000})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := site.paths[0]; got != "/search/the+fixture+movie+2024/seeders/1/" {
		t.Errorf("search path = %q (keywords filter, URL encoding and config not applied?)", got)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 movie results (music row filtered out), got %d: %+v", len(results), results)
	}
	r := results[0]
	if r.Title != "The Fixture Movie 2024 1080p WEB DL x264 GRP" {
		t.Errorf("title from the abbreviated-title fallback = %q", r.Title)
	}
	if results[1].Title != "The Fixture Movie 2024 720p WEB x264-GRP" {
		t.Errorf("title = %q", results[1].Title)
	}
	if r.SizeBytes != 1610612736 || r.Seeders != 1234 || r.Peers != 1290 {
		t.Errorf("size/seeders/peers = %d/%d/%d", r.SizeBytes, r.Seeders, r.Peers)
	}
	if len(r.Categories) != 1 || r.Categories[0] != 2040 {
		t.Errorf("categories = %v, want [2040]", r.Categories)
	}
	if age := time.Since(r.PublishDate); age < 2*time.Hour || age > 4*time.Hour {
		t.Errorf("publish date %v not about 3 hours ago", r.PublishDate)
	}
	if r.InfoURL != site.URL+"/torrent/101/The-Fixture-Movie-2024-1080p-WEB-DL-x264-GRP/" {
		t.Errorf("info url = %q", r.InfoURL)
	}
	link := r.Link
	if link != r.InfoURL {
		t.Fatalf("the link should be the details page, got %q", r.Link)
	}

	dl, err := m.Download(context.Background(), inst, link)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !strings.HasPrefix(dl.Magnet, "magnet:?xt=urn:btih:0123456789abcdef") {
		t.Errorf("expected the magnet from the details page, got %+v", dl)
	}

	// no keywords: the "latest" path
	if _, err := m.Search(context.Background(), inst, "", []int{2000}); err != nil {
		t.Fatalf("empty search: %v", err)
	}
	if got := site.paths[len(site.paths)-1]; got != "/cat/Movies/seeders/1/" {
		t.Errorf("empty search path = %q", got)
	}
}

func TestCardigannCheckboxSetting(t *testing.T) {
	t.Parallel()
	site := newPublicSite(t)
	m := fixtureManager(t, "fixturepublic")
	inst := Instance{Key: "ix1", Transport: direct, DefinitionID: "fixturepublic", BaseURL: site.URL,
		Settings: map[string]string{"disablesort": "true"}}
	if _, err := m.Search(context.Background(), inst, "x", []int{2000}); err != nil {
		t.Fatal(err)
	}
	if got := site.paths[0]; got != "/search/x/1/" {
		t.Errorf("with sorting disabled the path should skip the sort, got %q", got)
	}
}

// --- a fake private site with a login form ---

type privateSite struct {
	*httptest.Server
	logins   atomic.Int32
	sessions sync.Map // session id -> true
	lastQS   atomic.Value
}

const torrentBytes = "d8:announce30:http://tracker.test/announce4:infod6:lengthi1e4:name1:x12:piece lengthi16384e6:pieces0:ee"

func newPrivateSite(t *testing.T) *privateSite {
	s := &privateSite{}
	loggedIn := func(r *http.Request) bool {
		c, err := r.Cookie("sid")
		if err != nil {
			return false
		}
		_, ok := s.sessions.Load(c.Value)
		return ok
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login.php":
			http.SetCookie(w, &http.Cookie{Name: "pre", Value: "1", Path: "/"})
			io.WriteString(w, `<html><body><form id="loginform" action="takelogin.php" method="post">
				<input type="hidden" name="csrf" value="tok-123"><input name="username"><input type="password" name="password">
				<input type="submit" name="login" value="Log in"></form></body></html>`)
		case "/takelogin.php":
			r.ParseForm()
			if r.PostForm.Get("csrf") != "tok-123" || r.PostForm.Get("keeplogged") != "1" {
				io.WriteString(w, `<div class="error">Bad form token</div>`)
				return
			}
			if r.PostForm.Get("username") != "alice" || r.PostForm.Get("password") != "s3cret" {
				io.WriteString(w, `<html><body><div class="error">Invalid username or password</div></body></html>`)
				return
			}
			n := s.logins.Add(1)
			sid := fmt.Sprintf("session-%d", n)
			s.sessions.Store(sid, true)
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: sid, Path: "/"})
			http.Redirect(w, r, "/index.php", http.StatusFound)
		case "/index.php":
			if loggedIn(r) {
				io.WriteString(w, `<a href="logout.php?x=1">Logout</a>`)
			} else {
				io.WriteString(w, `<a href="login.php">Login</a>`)
			}
		case "/browse.php":
			if !loggedIn(r) {
				http.Redirect(w, r, "/login.php", http.StatusFound)
				return
			}
			s.lastQS.Store(r.URL.RawQuery)
			io.WriteString(w, `<html><body><a href="logout.php">Logout</a><table id="torrents">
<tr class="torrent"><td><a href="browse.php?cat=10">HD</a></td><td><a class="title" href="details.php?id=1">Private.Movie.2023.1080p.BluRay.x264-PRV<span class="ad">AD</span></a></td>
  <td class="size">8.5 GB</td><td class="seeders">12</td><td class="leechers">3</td><td class="snatched">40</td></tr>
<tr class="extra"><td><a href="download.php?id=1">DL</a></td><td class="added">2024-05-01 10:00</td><td class="tags">action</td><td><img class="free" src="free.png"></td></tr>
<tr class="torrent"><td><a href="browse.php?cat=11">UHD</a></td><td><a class="title" href="details.php?id=2">Private.Movie.2023.2160p.UHD.BluRay.x265-PRV</a></td>
  <td class="size">40 GB</td><td class="seeders">4</td><td class="leechers">0</td><td class="snatched">9</td></tr>
<tr class="extra"><td><a href="download.php?id=2">DL</a></td><td class="added">2024-05-02 11:30</td></tr>
</table></body></html>`)
		case "/download.php":
			if !loggedIn(r) {
				io.WriteString(w, `<html><body>Please log in</body></html>`)
				return
			}
			w.Header().Set("Content-Type", "application/x-bittorrent")
			io.WriteString(w, torrentBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func TestCardigannPrivateLoginSearchDownload(t *testing.T) {
	t.Parallel()
	site := newPrivateSite(t)
	m := fixtureManager(t, "fixtureprivate")
	inst := Instance{Key: "ix3", Transport: direct, Name: "Fixture Private", DefinitionID: "fixtureprivate", BaseURL: site.URL + "/",
		Settings: map[string]string{"username": "alice", "password": "s3cret", "freeleech": "true"}}

	results, err := m.Search(context.Background(), inst, "private movie", []int{2000})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if site.logins.Load() != 1 {
		t.Fatalf("expected one login, got %d", site.logins.Load())
	}
	qs, _ := url.ParseQuery(site.lastQS.Load().(string))
	if qs.Get("search") != "private movie" || qs.Get("freeleech") != "1" || qs.Get("c10") != "1" || qs.Get("c11") != "1" {
		t.Errorf("unexpected query %v", qs)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(results), results)
	}
	r := results[0]
	if r.Title != "Private.Movie.2023.1080p.BluRay.x264-PRV" {
		t.Errorf("title = %q (rows.remove not applied?)", r.Title)
	}
	if want := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC); !r.PublishDate.Equal(want) {
		t.Errorf("date = %v, want %v (rows.after merge?)", r.PublishDate, want)
	}
	if len(r.Categories) != 1 || r.Categories[0] != 2040 || results[1].Categories[0] != 2045 {
		t.Errorf("categories = %v / %v", r.Categories, results[1].Categories)
	}
	link := r.Link
	if link != site.URL+"/download.php?id=1" {
		t.Fatalf("link = %q", r.Link)
	}

	// a second search reuses the session
	if _, err := m.Search(context.Background(), inst, "private movie", []int{2000}); err != nil {
		t.Fatal(err)
	}
	if site.logins.Load() != 1 {
		t.Errorf("session not reused: %d logins", site.logins.Load())
	}

	dl, err := m.Download(context.Background(), inst, link)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(dl.Data) != torrentBytes {
		t.Errorf("unexpected download %+v", dl)
	}

	// the site forgets the session: the next search logs in again by itself
	site.sessions.Range(func(k, _ any) bool { site.sessions.Delete(k); return true })
	if _, err := m.Search(context.Background(), inst, "private movie", []int{2000}); err != nil {
		t.Fatalf("search after session loss: %v", err)
	}
	if site.logins.Load() != 2 {
		t.Errorf("expected a re-login, got %d logins", site.logins.Load())
	}
	// ...and so does a download
	site.sessions.Range(func(k, _ any) bool { site.sessions.Delete(k); return true })
	if dl, err := m.Download(context.Background(), inst, link); err != nil || string(dl.Data) != torrentBytes {
		t.Fatalf("download after session loss: %v", err)
	}
	if site.logins.Load() != 3 {
		t.Errorf("expected another re-login, got %d", site.logins.Load())
	}

	// Test() logs in afresh and counts results
	res, err := m.Test(context.Background(), inst)
	if err != nil || res.Releases != 2 {
		t.Errorf("test = %d, %v", res.Releases, err)
	}
	if res.ViaFlareSolverr {
		t.Error("no Cloudflare check was passed, so FlareSolverr should not be reported")
	}
}

func TestCardigannLoginErrors(t *testing.T) {
	t.Parallel()
	site := newPrivateSite(t)
	m := fixtureManager(t, "fixtureprivate")
	inst := Instance{Key: "ix4", Transport: direct, Name: "Fixture Private", DefinitionID: "fixtureprivate", BaseURL: site.URL,
		Settings: map[string]string{"username": "alice", "password": "wrong"}}
	_, err := m.Search(context.Background(), inst, "x", []int{2000})
	if err == nil || !strings.Contains(err.Error(), "login failed: Invalid username or password") {
		t.Fatalf("expected the site's login error, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatalf("error leaks the password: %v", err)
	}
}

// --- cookie login ---

func TestCardigannCookieLogin(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("uid")
		ok := err == nil && c.Value == "42"
		if p, err := r.Cookie("pass"); err != nil || p.Value != "abc" {
			ok = false
		}
		switch r.URL.Path {
		case "/index.php":
			if ok {
				io.WriteString(w, `<a href="logout.php">out</a>`)
			} else {
				io.WriteString(w, `<form>login</form>`)
			}
		case "/torrents.php":
			io.WriteString(w, `<a href="logout.php">out</a><table><tr class="t"><td class="n">Cookie.Movie.2020.720p</td><td class="s">1 GB</td><td><a class="dl" href="/dl/1.torrent">d</a></td></tr></table>`)
		}
	}))
	defer srv.Close()
	m := fixtureManager(t, "fixturecookie")

	inst := Instance{Key: "ix5", Transport: direct, Name: "Cookie Site", DefinitionID: "fixturecookie", BaseURL: srv.URL,
		Settings: map[string]string{"cookie": "uid=42; pass=abc"}}
	res, err := m.Search(context.Background(), inst, "cookie", nil)
	if err != nil || len(res) != 1 || res[0].Title != "Cookie.Movie.2020.720p" {
		t.Fatalf("cookie search: %v %+v", err, res)
	}

	inst.Key = "ix6"
	inst.Settings = map[string]string{"cookie": "uid=42; pass=expired"}
	_, err = m.Search(context.Background(), inst, "cookie", nil)
	if err == nil || !strings.Contains(err.Error(), "cookie was not accepted") {
		t.Fatalf("expected a clear cookie error, got %v", err)
	}
	inst.Key = "ix8"
	inst.Settings = map[string]string{}
	if _, err = m.Search(context.Background(), inst, "cookie", nil); err == nil || !strings.Contains(err.Error(), "enter the cookie") {
		t.Fatalf("expected a missing-cookie error, got %v", err)
	}
}

// --- JSON API ---

func TestCardigannJSONSearch(t *testing.T) {
	t.Parallel()
	var auth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		if r.URL.Path != "/api/torrents" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[
			{"id":11,"attributes":{"name":"Json.Movie.2022.1080p.WEB-DL","category_id":1,"info_hash":"ABCDEF0123456789ABCDEF0123456789ABCDEF01","size":2147483648,"seeders":9,"leechers":1,"created_at":"2024-02-03T04:05:06Z","freeleech":true}},
			{"id":12,"attributes":{"name":"Json.Show.S01E01.720p","category_id":2,"info_hash":"1111111111111111111111111111111111111111","size":"500 MB","seeders":"3","leechers":"0","created_at":"2024-02-04 04:05:06","freeleech":false}},
			{"id":13}
		],"meta":{"total":3}}`)
	}))
	defer srv.Close()
	m := fixtureManager(t, "fixturejson")
	inst := Instance{Key: "ix9", Transport: direct, Name: "Json", DefinitionID: "fixturejson", BaseURL: srv.URL,
		Settings: map[string]string{"apikey": "key-xyz"}}
	res, err := m.Search(context.Background(), inst, "json", []int{2000, 5000})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if auth.Load() != "Bearer key-xyz" {
		t.Errorf("authorization header = %v", auth.Load())
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(res), res)
	}
	r := res[0]
	if r.Title != "Json.Movie.2022.1080p.WEB-DL" || r.SizeBytes != 2147483648 || r.Seeders != 9 || r.Peers != 10 {
		t.Errorf("unexpected result %+v", r)
	}
	if !strings.HasPrefix(r.Magnet, "magnet:?xt=urn:btih:ABCDEF0123456789") || r.InfoHash != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Errorf("magnet/infohash = %q / %q", r.Magnet, r.InfoHash)
	}
	if r.InfoURL != srv.URL+"/torrents/11" {
		t.Errorf("details from the parent row id = %q", r.InfoURL)
	}
	if !r.PublishDate.Equal(time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC)) {
		t.Errorf("date = %v", r.PublishDate)
	}
	if res[1].SizeBytes != 524288000 || res[1].Categories[0] != 5000 {
		t.Errorf("second result %+v", res[1])
	}
}

// --- rate limiting ---

func TestCardigannRequestDelay(t *testing.T) {
	// Not parallel: it measures wall-clock gaps, which other tests' load can shrink (as in 93e309cb).
	var (
		mu    sync.Mutex
		times []time.Time
	)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		io.WriteString(w, publicResults)
	}))
	defer site.Close()
	m := fixtureManager(t, "fixturepublic")
	m.DefaultDelay = 150 * time.Millisecond
	inst := Instance{Key: "ix1", Transport: direct, DefinitionID: "fixturepublic", BaseURL: site.URL}
	for i := 0; i < 3; i++ {
		if _, err := m.Search(context.Background(), inst, "x", []int{2000}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap < 140*time.Millisecond {
			t.Errorf("requests %d and %d only %v apart", i-1, i, gap)
		}
	}
}

// --- Cloudflare and FlareSolverr ---

const cfChallenge = `<!DOCTYPE html><html><head><title>Just a moment...</title></head><body><div id="challenge-platform"></div></body></html>`

func cloudflareSite(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("cf_clearance")
		if err != nil || c.Value != "cleared" || r.UserAgent() != "FlareSolverr-Browser/1.0" {
			w.Header().Set("Server", "cloudflare")
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, cfChallenge)
			return
		}
		io.WriteString(w, publicResults)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCardigannCloudflareWithoutFlareSolverr(t *testing.T) {
	t.Parallel()
	site := cloudflareSite(t)
	m := fixtureManager(t, "fixturepublic")
	inst := Instance{Key: "ix1", Transport: direct, Name: "CF Site", DefinitionID: "fixturepublic", BaseURL: site.URL, Solver: direct}
	_, err := m.Search(context.Background(), inst, "x", []int{2000})
	if !errors.Is(err, ErrCloudflare) {
		t.Fatalf("expected ErrCloudflare, got %v", err)
	}
	if !strings.Contains(err.Error(), "set cardigann.flaresolverr_url (trawl)") {
		t.Errorf("message should tell the user what to do: %v", err)
	}
}

func TestCardigannCloudflareViaFlareSolverr(t *testing.T) {
	t.Parallel()
	site := cloudflareSite(t)
	var calls atomic.Int32
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Cmd        string `json:"cmd"`
			URL        string `json:"url"`
			MaxTimeout int    `json:"maxTimeout"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		calls.Add(1)
		if req.Cmd != "request.get" || req.MaxTimeout <= 0 {
			t.Errorf("bad FlareSolverr request %+v", req)
		}
		u, _ := url.Parse(req.URL)
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "message": "Challenge solved!",
			"solution": map[string]any{
				"url": req.URL, "status": 200, "userAgent": "FlareSolverr-Browser/1.0",
				"cookies":  []map[string]any{{"name": "cf_clearance", "value": "cleared", "domain": u.Hostname(), "path": "/"}},
				"response": publicResults,
			},
		})
	}))
	defer fs.Close()

	m := fixtureManager(t, "fixturepublic")
	m.FlareSolverrURL = func() string { return fs.URL + "/" }
	inst := Instance{Key: "ix1", Transport: direct, Name: "CF Site", DefinitionID: "fixturepublic", BaseURL: site.URL, Solver: direct}
	res, err := m.Search(context.Background(), inst, "the fixture movie", []int{2000})
	if err != nil {
		t.Fatalf("search via FlareSolverr: %v", err)
	}
	if len(res) != 2 || calls.Load() != 1 {
		t.Fatalf("results=%d flaresolverr calls=%d", len(res), calls.Load())
	}
	// the clearance cookie and user agent are reused: no second FlareSolverr call
	if _, err := m.Search(context.Background(), inst, "the fixture movie", []int{2000}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Errorf("expected the clearance to be reused, FlareSolverr called %d times", calls.Load())
	}
	tr, err := m.Test(context.Background(), inst)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.ViaFlareSolverr {
		t.Fatal("the test should say FlareSolverr passed the Cloudflare check")
	}
}

func TestCardigannFlareSolverrFailure(t *testing.T) {
	t.Parallel()
	site := cloudflareSite(t)
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "error", "message": "Error solving the challenge. Timeout after 60.0 seconds."})
	}))
	defer fs.Close()
	m := fixtureManager(t, "fixturepublic")
	m.FlareSolverrURL = func() string { return fs.URL }
	inst := Instance{Key: "ix1", Transport: direct, Name: "CF Site", DefinitionID: "fixturepublic", BaseURL: site.URL, Solver: direct}
	_, err := m.Search(context.Background(), inst, "x", []int{2000})
	if err == nil || !strings.Contains(err.Error(), "within 60 seconds") {
		t.Fatalf("expected a plain FlareSolverr timeout message, got %v", err)
	}
}

// FlareSolverr's browser follows redirects by itself: a page that ended on
// the cloud metadata address is thrown away, not read as search results.
func TestCardigannFlareSolverrEndingOnABlockedAddress(t *testing.T) {
	t.Parallel()
	site := cloudflareSite(t)
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"solution": map[string]any{
				"url": "http://169.254.169.254/latest/meta-data/", "status": 200, "userAgent": "FlareSolverr-Browser/1.0",
				"cookies": []map[string]any{}, "response": publicResults,
			},
		})
	}))
	defer fs.Close()
	m := fixtureManager(t, "fixturepublic")
	m.FlareSolverrURL = func() string { return fs.URL }
	inst := Instance{Key: "ix1", Transport: direct, Name: "CF Site", DefinitionID: "fixturepublic", BaseURL: site.URL, Solver: direct}
	res, err := m.Search(context.Background(), inst, "x", []int{2000})
	if err == nil || !strings.Contains(err.Error(), "sent elsewhere") || len(res) != 0 {
		t.Fatalf("expected the page to be refused, got %d results, %v", len(res), err)
	}
}

func TestIsCloudflareChallenge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status int
		server string
		mitig  string
		body   string
		want   bool
	}{
		{503, "cloudflare", "", cfChallenge, true},
		{403, "", "", `<title>Attention Required! | Cloudflare</title>`, true},
		{403, "cloudflare", "challenge", `<html></html>`, true},
		{200, "cloudflare", "", cfChallenge, false},
		{403, "nginx", "", `<html>Forbidden</html>`, false},
		{503, "cloudflare", "", `<html>Service down for maintenance</html>`, false},
	}
	for i, tt := range tests {
		h := http.Header{}
		h.Set("Server", tt.server)
		if tt.mitig != "" {
			h.Set("cf-mitigated", tt.mitig)
		}
		if got := isCloudflareChallenge(&cgResponse{status: tt.status, header: h, body: []byte(tt.body)}); got != tt.want {
			t.Errorf("case %d: got %v, want %v", i, got, tt.want)
		}
	}
}

type memSessions struct {
	mu    sync.Mutex
	saved map[string]SavedSession
}

func (m *memSessions) LoadSession(_ context.Context, ix string) (*SavedSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.saved[ix]
	if !ok {
		return nil, nil
	}
	return &s, nil
}

func (m *memSessions) SaveSession(_ context.Context, ix string, s SavedSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saved[ix] = s
	return nil
}

// A login survives a restart: the cookies are saved and a new manager uses them without signing in again. Changed
// settings (another password) start afresh.
func TestCardigannSessionIsSavedAndRestored(t *testing.T) {
	t.Parallel()
	site := newPrivateSite(t)
	store := &memSessions{saved: map[string]SavedSession{}}
	inst := Instance{Key: "ix3", Transport: direct, Name: "Fixture Private", DefinitionID: "fixtureprivate", BaseURL: site.URL + "/",
		Settings: map[string]string{"username": "alice", "password": "s3cret"}}
	m := fixtureManager(t, "fixtureprivate")
	m.Sessions = store
	if _, err := m.Search(context.Background(), inst, "private movie", []int{2000}); err != nil {
		t.Fatal(err)
	}
	if site.logins.Load() != 1 || len(store.saved["ix3"].Cookies) == 0 {
		t.Fatalf("logins=%d saved=%+v", site.logins.Load(), store.saved)
	}
	restarted := fixtureManager(t, "fixtureprivate")
	restarted.Sessions = store
	if _, err := restarted.Search(context.Background(), inst, "private movie", []int{2000}); err != nil {
		t.Fatal(err)
	}
	if site.logins.Load() != 1 {
		t.Fatalf("the saved session was not used: %d logins", site.logins.Load())
	}
	other := inst
	other.Settings = map[string]string{"username": "alice", "password": "s3cret", "freeleech": "true"}
	again := fixtureManager(t, "fixtureprivate")
	again.Sessions = store
	if _, err := again.Search(context.Background(), other, "private movie", []int{2000}); err != nil {
		t.Fatal(err)
	}
	if site.logins.Load() != 2 {
		t.Fatalf("changed settings should sign in afresh: %d logins", site.logins.Load())
	}
}

// --- query variables ---

const queryVarsDef = `---
id: queryvars
name: Query Vars
type: public
links:
  - https://queryvars.test/
caps:
  categorymappings:
    - {id: 1, cat: Movies, desc: "Movies"}
  modes:
    search: [q]
    movie-search: [q, imdbid, tmdbid, year]
    tv-search: [q, season, ep, tvdbid]
search:
  paths:
    - path: api
      response:
        type: json
  inputs:
    q: "{{ .Query.Q }}"
    kw: "{{ .Keywords }}"
    imdb: "{{ .Query.IMDBID }}"
    imdbshort: "{{ .Query.IMDBIDShort }}"
    tmdb: "{{ .Query.TMDBID }}"
    tvdb: "{{ .Query.TVDBID }}"
    season: "{{ .Query.Season }}"
    ep: "{{ .Query.Ep }}"
    episode: "{{ .Query.Episode }}"
    year: "{{ .Query.Year }}"
  rows:
    selector: results
    filters:
      - name: andmatch
  fields:
    title:
      selector: name
    infohash:
      selector: hash
`

// The query variables reach the definition's templates as Prowlarr's generator sets them; an id search has no
// .Query.Q and skips the andmatch filter, a keyword search keeps it.
func TestSearchQueryVariables(t *testing.T) {
	var (
		mu  sync.Mutex
		got url.Values
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.URL.Query()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[{"name":"The Matrix 1999 1080p","hash":"1111111111111111111111111111111111111111"},
			{"name":"Something Else 720p","hash":"2222222222222222222222222222222222222222"}]}`)
	}))
	defer srv.Close()
	m := NewManager(memDefs{"queryvars": []byte(queryVarsDef)}, nil)
	m.DefaultDelay = 0
	inst := Instance{Key: "qv", Transport: direct, DefinitionID: "queryvars", BaseURL: srv.URL}

	res, err := m.SearchQuery(context.Background(), inst, Query{ByID: true, Keywords: "S01E02", IMDBID: "tt133093",
		TMDBID: 603, TVDBID: 12345, Season: "1", Ep: "2", Episode: "S01E02", Year: "1999"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"q": "", "kw": "S01E02", "imdb": "tt0133093", "imdbshort": "0133093", "tmdb": "603",
		"tvdb": "12345", "season": "1", "ep": "2", "episode": "S01E02", "year": "1999"}
	mu.Lock()
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("%s = %q, want %q (%s)", k, got.Get(k), v, got.Encode())
		}
	}
	mu.Unlock()
	if len(res) != 2 {
		t.Errorf("an id search filtered rows by andmatch: %d results", len(res))
	}

	res, err = m.SearchQuery(context.Background(), inst, Query{Keywords: "The Matrix", Year: "1999"})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if got.Get("q") != "The Matrix" || got.Get("year") != "1999" || got.Get("imdb") != "" || got.Get("tvdb") != "" {
		t.Errorf("keyword search sent %s", got.Encode())
	}
	mu.Unlock()
	if len(res) != 1 || res[0].Title != "The Matrix 1999 1080p" {
		t.Errorf("andmatch on a keyword search: %+v", res)
	}
}
