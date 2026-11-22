package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/plex"
	"github.com/theatarr/theatarr/internal/policy"
)

type plexDoer struct{}

func (plexDoer) Do(ctx context.Context, _ gateway.Target, req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

// Watch in Plex: the title's file in the library's section gives the item (a series its show), with a Plex Web and
// a Plex app link; a title Plex has not listed yet, and a Plex that does not answer, give found false. A section of
// a few thousand items is listed once per lookup.
func TestPlexLink(t *testing.T) {
	t.Parallel()
	const items = 5000
	var listings int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/identity":
			_, _ = io.WriteString(w, `{"MediaContainer":{"machineIdentifier":"abc123"}}`)
		case r.URL.Path == "/library/sections/7/all" && r.URL.Query().Get("type") == "1":
			listings++
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"55","title":"Film","Media":[{"Part":[{"file":"/lib/movies/Film (2020)/Film.mkv"}]}]}]}}`)
		case r.URL.Path == "/library/sections/8/all" && r.URL.Query().Get("type") == "4":
			listings++
			var b strings.Builder
			b.WriteString(`{"MediaContainer":{"Metadata":[`)
			for i := range items {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"ratingKey":"%d","grandparentRatingKey":"%d","title":"E%d","Media":[{"Part":[{"file":"/lib/tv/Show %d/S01E01.mkv"}]}]}`,
					10000+i, 900+i%50, i, i)
			}
			b.WriteString(`]}}`)
			_, _ = io.WriteString(w, b.String())
		default:
			http.Error(w, "no", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	pol := &policy.Policy{Library: &policy.Library{Plex: &policy.Plex{URL: srv.URL, TokenEnv: "T"},
		Versions: map[string]*policy.LibraryVersion{"movie-pl": {PlexSection: 7}, "tv-pl": {PlexSection: 8}, "none-pl": {}}}}
	c, err := plex.New(plexDoer{}, srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	var getAs func(key, path string) PlexLink
	e := newEnvWith(t, func(cfg *Config) {
		cfg.PlexItems = func(context.Context) (PlexItems, *policy.Policy, error) { return c, pol, nil }
	})
	now := time.Now().UnixMilli()
	e.exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (801, 'movie', 'Film', ?, ?),
		(802, 'series', 'Show', ?, ?), (803, 'movie', 'Later', ?, ?)`, now, now, now, now, now, now)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (811, 801, 'movie-pl', ?, ?),
		(812, 802, 'tv-pl', ?, ?), (813, 803, 'movie-pl', ?, ?), (814, 801, 'none-pl', ?, ?)`, now, now, now, now, now, now, now, now)
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES
		(821, '/lib/movies/Film (2020)/Film.mkv', '/m/a', 801, 'active', ?, ?),
		(822, '/lib/tv/Show 4321/S01E01.mkv', '/m/b', 802, 'active', ?, ?),
		(823, '/lib/movies/Later (2021)/Later.mkv', '/m/c', 803, 'active', ?, ?)`, now, now, now, now, now, now)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (821, 811), (822, 812), (823, 813), (821, 814)`)
	key := e.apiKey()
	uid := e.account("member", auth.RoleUser, auth.StatusActive, true)
	memberKey, _, err := e.auth.CreateKey(context.Background(), "k-member", uid)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) PlexLink { return getAs(key, path) }
	getAs = func(key, path string) PlexLink {
		t.Helper()
		r := httptest.NewRequest("GET", APIBase+path, nil)
		r.Header.Set(auth.APIKeyHeader, key)
		rec := e.do(r)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		var out PlexLink
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	film := get("/titles/801/plex?version=movie-pl")
	if m := getAs(memberKey, "/titles/801/plex?version=movie-pl"); m != film {
		t.Errorf("a member's link %+v", m)
	}
	if !film.Found || film.WebURL != "https://app.plex.tv/desktop/#!/server/abc123/details?key=%2Flibrary%2Fmetadata%2F55" ||
		film.AppURL != "plex://preplay/?metadataKey=%2Flibrary%2Fmetadata%2F55&server=abc123" {
		t.Errorf("film %+v", film)
	}
	start := time.Now()
	show := get("/titles/802/plex?version=tv-pl")
	t.Logf("a lookup in a section of %d episodes took %v", items, time.Since(start))
	if !show.Found || !strings.HasSuffix(show.WebURL, "key=%2Flibrary%2Fmetadata%2F"+fmt.Sprint(900+4321%50)) {
		t.Errorf("show %+v", show)
	}
	if got := get("/titles/803/plex?version=movie-pl"); got.Found || got.Message != "Not found in Plex yet" {
		t.Errorf("not listed %+v", got)
	}
	if got := get("/titles/801/plex?version=none-pl"); got.Found || got.Message != "This library has no Plex section." {
		t.Errorf("no section %+v", got)
	}
	if listings != 4 {
		t.Errorf("%d listings, want one per lookup with a section and a file", listings)
	}
	srv.Close()
	if got := get("/titles/801/plex?version=movie-pl"); got.Found ||
		got.Message != "Plex does not answer at "+srv.URL+": check the address under Settings › Plex & playback" {
		t.Errorf("down %+v", got)
	}
	// A member reads no address and no settings page they cannot open.
	if got := getAs(memberKey, "/titles/801/plex?version=movie-pl"); got.Found || got.Message != "Plex does not answer" {
		t.Errorf("member down %+v", got)
	}
}

// With Plex not set up (no address, or an address without a token), a library with no Plex section still says so;
// one with a section says Plex is not set up and where to set it, a member only that it is not set up.
func TestPlexLinkNoSectionWithoutPlex(t *testing.T) {
	t.Parallel()
	noAddress := &policy.Policy{Library: &policy.Library{
		Versions: map[string]*policy.LibraryVersion{"movie-pl": {PlexSection: 7}, "none-pl": {}}}}
	noToken := &policy.Policy{Library: &policy.Library{Plex: &policy.Plex{URL: "http://plex.invalid:32400", TokenEnv: "T"},
		Versions: map[string]*policy.LibraryVersion{"movie-pl": {PlexSection: 7}, "none-pl": {}}}}
	for name, pol := range map[string]*policy.Policy{"no address": noAddress, "no token": noToken} {
		e := newEnvWith(t, func(cfg *Config) {
			cfg.PlexItems = func(context.Context) (PlexItems, *policy.Policy, error) { return nil, pol, nil }
		})
		now := time.Now().UnixMilli()
		e.exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (801, 'movie', 'Film', ?, ?)`, now, now)
		key := e.apiKey()
		uid := e.account("member", auth.RoleUser, auth.StatusActive, true)
		memberKey, _, err := e.auth.CreateKey(context.Background(), "k-member", uid)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ key, path, want string }{
			{key, "/titles/801/plex?version=none-pl", "This library has no Plex section."},
			{key, "/titles/801/plex?version=movie-pl", "Plex is not set up: add its address and token under Settings › Plex & playback."},
			{memberKey, "/titles/801/plex?version=movie-pl", "Plex is not set up."},
		} {
			r := httptest.NewRequest("GET", APIBase+c.path, nil)
			r.Header.Set(auth.APIKeyHeader, c.key)
			rec := e.do(r)
			var out PlexLink
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 || out.Found || out.Message != c.want {
				t.Errorf("%s %s: %d %+v, want %q", name, c.path, rec.Code, out, c.want)
			}
		}
	}
}
