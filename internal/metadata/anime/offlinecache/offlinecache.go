// Package offlinecache reads an offline cache of the open-data responses the anime pipeline uses and serves it
// through the pipeline's source interfaces. It is the backend of the golden fixture tests (testdata/offline) and of
// `theatarr anime build -cache DIR`.
//
// Layout: <dir>/<namespace>/<sha1(key)[:20]>.json holding {"key", "fetched", "response"}, keyed on the requests
// the pipeline makes (anime/urls.go): the AniList GraphQL body (query whitespace-collapsed, sorted keys), the Kitsu
// and SkyHook URLs, the TheXEM URL, and "<instance> GET <path>" for the arr GETs. The four static lists are in <dir>/static.
// A key that is not cached is an error: the cache is never filled from the network.
package offlinecache

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/metadata/anime"
)

// Cache is an opened cache directory.
type Cache struct {
	Dir string
}

// Open returns a cache over dir (a cache/ directory in the layout above).
func Open(dir string) (*Cache, error) {
	st, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	return &Cache{Dir: dir}, nil
}

// Path is where the cache stores key under ns.
func (c *Cache) Path(ns, key string) string {
	h := sha1.Sum([]byte(key))
	return filepath.Join(c.Dir, ns, hex.EncodeToString(h[:])[:20]+".json")
}

// ErrMiss is returned for keys the cache doesn't hold.
type ErrMiss struct{ NS, Key string }

func (e *ErrMiss) Error() string {
	return fmt.Sprintf("cache miss in offline mode: %s %s", e.NS, e.Key)
}

func (c *Cache) load(ns, key string, v any) error {
	p := c.Path(ns, key)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &ErrMiss{ns, key}
		}
		return err
	}
	var doc struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	return json.Unmarshal(doc.Response, v)
}

// AniListKey is the cache key for one batch: json.dumps({"q": ..., "v": {"ids": [...]}}, sort_keys=True).
func AniListKey(ids []int) string {
	parts := make([]string, len(ids))
	for i, x := range ids {
		parts[i] = strconv.Itoa(x)
	}
	q, _ := json.Marshal(anime.AniListQuery)
	return `{"q": ` + string(q) + `, "v": {"ids": [` + strings.Join(parts, ", ") + `]}}`
}

// Batches splits ids as the AniList sources do: sorted, de-duplicated, 50 per request.
func Batches(ids []int) [][]int {
	m := map[int]bool{}
	var u []int
	for _, x := range ids {
		if !m[x] {
			m[x] = true
			u = append(u, x)
		}
	}
	sort.Ints(u)
	var out [][]int
	for i := 0; i < len(u); i += 50 {
		out = append(out, u[i:min(i+50, len(u))])
	}
	return out
}

// AniList serves cached AniList batches.
type AniList struct{ C *Cache }

// Media implements anime.AniList.
func (a AniList) Media(_ context.Context, ids []int) ([]*anime.Media, error) {
	var out []*anime.Media
	for _, chunk := range Batches(ids) {
		var resp struct {
			Page struct {
				Media []*anime.Media `json:"media"`
			} `json:"Page"`
		}
		if err := a.C.load("anilist", AniListKey(chunk), &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Page.Media...)
	}
	return out, nil
}

// Kitsu serves cached Kitsu documents.
type Kitsu struct{ C *Cache }

// Anime implements anime.Kitsu.
func (k Kitsu) Anime(_ context.Context, id int) (*anime.KitsuDoc, error) {
	var d anime.KitsuDoc
	if err := k.C.load("kitsu", anime.KitsuURL(id), &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// SkyHook serves cached SkyHook series: the TVDB backend shaped like Sonarr's metadata.
type SkyHook struct{ C *Cache }

// Show implements anime.TVDB.
func (s SkyHook) Show(_ context.Context, id int) (*anime.Show, error) {
	var d anime.Show
	if err := s.C.load("skyhook", anime.SkyHookURL(id), &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// XEM serves cached TheXEM maps.
type XEM struct{ C *Cache }

// Map implements anime.XEM.
func (x XEM) Map(_ context.Context, id int) (*anime.XEMMap, error) {
	var d anime.XEMMap
	if err := x.C.load("xem", anime.XEMURL(id), &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Arr serves the cached GETs against a Sonarr (sonarr_anime) and a Radarr (radarr_anime).
type Arr struct{ C *Cache }

// LookupTMDb implements anime.Movies (Radarr's movie/lookup/tmdb). A miss is "lookup failed" (nil).
func (a Arr) LookupTMDb(_ context.Context, id int) (*anime.MovieInfo, error) {
	var m anime.MovieInfo
	if err := a.C.load("arr", fmt.Sprintf("radarr_anime GET movie/lookup/tmdb?tmdbId=%d", id), &m); err != nil {
		return nil, nil
	}
	return &m, nil
}

// Search implements anime.Movies (Radarr's movie/lookup?term=).
func (a Arr) Search(_ context.Context, term string) ([]anime.MovieInfo, error) {
	var res []anime.MovieInfo
	if err := a.C.load("arr", "radarr_anime GET movie/lookup?term="+quote(term), &res); err != nil {
		return nil, nil
	}
	return res, nil
}

// SonarrEpisodes implements anime.Library from Sonarr's series and episode lists.
func (a Arr) SonarrEpisodes(_ context.Context, tvdbIDs []int) (map[int]map[anime.SE]bool, error) {
	var series []struct {
		ID     int `json:"id"`
		TVDBID int `json:"tvdbId"`
	}
	if err := a.C.load("arr", "sonarr_anime GET series", &series); err != nil {
		return nil, err
	}
	want := map[int]bool{}
	for _, t := range tvdbIDs {
		want[t] = true
	}
	out := map[int]map[anime.SE]bool{}
	for _, s := range series {
		if !want[s.TVDBID] {
			continue
		}
		var eps []struct {
			Season  int `json:"seasonNumber"`
			Episode int `json:"episodeNumber"`
		}
		if err := a.C.load("arr", fmt.Sprintf("sonarr_anime GET episode?seriesId=%d", s.ID), &eps); err != nil {
			return nil, err
		}
		set := map[anime.SE]bool{}
		for _, e := range eps {
			set[anime.SE{e.Season, e.Episode}] = true
		}
		out[s.TVDBID] = set
	}
	return out, nil
}

// quote is urllib.parse.quote with safe="/".
func quote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.IndexByte("_.-~/", ch) >= 0 {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

// Static list file names under <dir>/static.
const (
	FileScudLee   = "scudlee-anime-list-master.xml"
	FileFribb     = "fribb-anime-list-full.json"
	FileAnibridge = "anibridge-mappings.json"
	FileManami    = "manami-aod-minified.json"
)

// LoadLists parses the four static lists from dir (the cache's static/ directory, or a trimmed copy).
func LoadLists(dir string) (*anime.Lists, error) {
	l := &anime.Lists{}
	for _, f := range []struct {
		name  string
		parse func(*os.File) error
	}{
		{FileScudLee, func(r *os.File) error { return l.ParseScudLee(r) }},
		{FileFribb, func(r *os.File) error { return l.ParseFribb(r) }},
		{FileAnibridge, func(r *os.File) error { return l.ParseAnibridge(r) }},
		{FileManami, func(r *os.File) error { return l.ParseManami(r) }},
	} {
		r, err := os.Open(filepath.Join(dir, f.name))
		if err != nil {
			return nil, err
		}
		err = f.parse(r)
		r.Close()
		if err != nil {
			return nil, err
		}
	}
	return l, nil
}

// Sources returns every source backed by the cache at dir, with the lists from dir/static.
func Sources(dir string) (*anime.Sources, *Cache, error) {
	c, err := Open(dir)
	if err != nil {
		return nil, nil, err
	}
	l, err := LoadLists(filepath.Join(dir, "static"))
	if err != nil {
		return nil, nil, err
	}
	a := Arr{c}
	return &anime.Sources{AniList: AniList{c}, Kitsu: Kitsu{c}, TVDB: SkyHook{c}, XEM: XEM{c}, Movies: a, Library: a,
		Lists: l}, c, nil
}
