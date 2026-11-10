// Package decypharrtest is a fake decypharr for tests: the read-only routes Theatarr's client uses, answering the
// way decypharr v2.7.1 does where the client's rules depend on it. Any other method or route fails the test.
//
// What it reproduces:
//   - a group lists a folder name once, with the hash of the first torrent that has it;
//   - same-named torrents share one folder; per file name the newer torrent's file wins;
//   - folders are flat: a file has a name, no directory;
//   - PROPFIND of a missing folder (or one without files) is 207 with an empty multistatus;
//   - the browse API pages at most 100 entries (a larger limit means 50) and sorts by lower-case name;
//   - the API wants a Bearer token and WebDAV wants Basic, each only when switched on;
//   - only WebDAV answers 503 (with Retry-After) while loading;
//   - /api/v2/torrents/files knows only queued torrents and answers 404 for the rest.
//
// Every name in Fixture is invented.
package decypharrtest

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// File is one file of a torrent. Name has no directory part.
type File struct {
	Name string
	Size int64
}

// Torrent is one torrent decypharr knows.
type Torrent struct {
	Hash string
	// Name is the folder name decypharr gives it.
	Name string
	// Provider is the debrid it is on; it must be one of the server's Providers to show in that group.
	Provider string
	// NZB marks a usenet entry: it shows in __all__ and nzbs, not in torrents.
	NZB     bool
	AddedOn time.Time
	Files   []File
	// Queued torrents answer /api/v2/torrents/files; decypharr drops the queue entry of a finished torrent whose
	// download action is none.
	Queued bool
}

// Server is the fake. Change its state with Set.
type Server struct {
	t  testing.TB
	ts *httptest.Server

	mu sync.Mutex
	// Base is decypharr's url_base without the trailing slash ("" or "/decypharr").
	Base string
	// Providers are the configured debrids, each a group of kind provider.
	Providers []string
	// Virtual are virtual folders, groups of kind virtual (always empty here).
	Virtual  []string
	Torrents []Torrent
	// Token switches API auth on: requests need "Authorization: Bearer <Token>".
	Token string
	// DAVUser switches WebDAV auth on: requests need Basic DAVUser/DAVPassword.
	DAVUser     string
	DAVPassword string
	// Loading makes WebDAV answer 503.
	Loading bool
	// Redirect, when set, answers every request with 302 to this path on the same server. A request that follows
	// it fails the test.
	Redirect string
	// Fail, when set, answers every API request with this status.
	Fail int

	requests []string
}

// New starts a fake decypharr with no torrents and no auth. It is closed with the test.
func New(t testing.TB) *Server {
	s := &Server{t: t}
	s.ts = httptest.NewServer(s)
	t.Cleanup(s.ts.Close)
	return s
}

// URL is the base URL a client is configured with, including Base.
func (s *Server) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ts.URL + s.Base
}

// Set changes the server's state under its lock.
func (s *Server) Set(f func(s *Server)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// Requests is every request received so far, as "METHOD /escaped/path?query".
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// Count is how many requests had this method and escaped path (without Base and query).
func (s *Server) Count(method, escapedPath string) int {
	n := 0
	for _, r := range s.Requests() {
		p, _, _ := strings.Cut(r, "?")
		if p == method+" "+escapedPath {
			n++
		}
	}
	return n
}

// Reset forgets the recorded requests.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := r.URL.EscapedPath()
	if !strings.HasPrefix(p, s.Base+"/") {
		s.t.Errorf("decypharrtest: %s %s: outside url_base %q", r.Method, p, s.Base)
		http.NotFound(w, r)
		return
	}
	p = strings.TrimPrefix(p, s.Base)
	rec := r.Method + " " + p
	if r.URL.RawQuery != "" {
		rec += "?" + r.URL.RawQuery
	}
	s.requests = append(s.requests, rec)
	if s.Redirect != "" && p == s.Redirect {
		s.t.Errorf("decypharrtest: %s: a redirect was followed", rec)
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case "PROPFIND", http.MethodGet, http.MethodHead:
	default:
		s.t.Errorf("decypharrtest: %s: the client may only send PROPFIND, GET and HEAD", rec)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	segs, ok := split(p)
	if !ok {
		s.t.Errorf("decypharrtest: %s: bad path", rec)
		http.NotFound(w, r)
		return
	}
	switch {
	case len(segs) >= 2 && segs[0] == "api" && segs[1] == "browse":
		s.api(w, r, rec, func() { s.browse(w, r, rec, segs[2:]) })
	case len(segs) == 4 && segs[0] == "api" && segs[1] == "v2" && segs[2] == "torrents" && segs[3] == "files":
		s.api(w, r, rec, func() { s.torrentFiles(w, r) })
	case segs[0] == "webdav":
		s.webdav(w, r, rec, segs[1:])
	default:
		s.t.Errorf("decypharrtest: %s: not a route the client may use", rec)
		http.NotFound(w, r)
	}
}

// split is the unescaped elements of an escaped path; a trailing slash adds none.
func split(p string) ([]string, bool) {
	var segs []string
	for _, e := range strings.Split(strings.Trim(p, "/"), "/") {
		u, err := url.PathUnescape(e)
		if err != nil || u == "" {
			return nil, false
		}
		segs = append(segs, u)
	}
	return segs, len(segs) > 0
}

// api applies what every API route shares: GET only, the redirect and failure switches, Bearer auth.
func (s *Server) api(w http.ResponseWriter, r *http.Request, rec string, serve func()) {
	if r.Method != http.MethodGet {
		s.t.Errorf("decypharrtest: %s: the API is read with GET only", rec)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Redirect != "" {
		http.Redirect(w, r, s.Base+s.Redirect, http.StatusFound)
		return
	}
	if s.Token != "" && r.Header.Get("Authorization") != "Bearer "+s.Token {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Authentication required."}`))
		return
	}
	if _, _, basic := r.BasicAuth(); basic {
		s.t.Errorf("decypharrtest: %s: WebDAV credentials sent to the API", rec)
	}
	if s.Fail != 0 {
		http.Error(w, "failing", s.Fail)
		return
	}
	serve()
}

// entry is one line of a browse listing.
type entry struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	Size         int64  `json:"size"`
	ModTime      string `json:"mod_time"`
	IsDir        bool   `json:"is_dir"`
	InfoHash     string `json:"info_hash,omitempty"`
	CanDelete    bool   `json:"can_delete,omitempty"`
	ActiveDebrid string `json:"active_debrid"`
	Kind         string `json:"kind,omitempty"`
	modTime      time.Time
}

func (s *Server) root() []entry {
	var es []entry
	for _, g := range []string{"__all__", "__bad__", "torrents", "nzbs"} {
		es = append(es, entry{Name: g, Path: "/" + g, IsDir: true, Kind: "system"})
	}
	for _, g := range s.Providers {
		es = append(es, entry{Name: g, Path: "/" + g, IsDir: true, Kind: "provider"})
	}
	for _, g := range s.Virtual {
		es = append(es, entry{Name: g, Path: "/" + g, IsDir: true, Kind: "virtual"})
	}
	return append(es, entry{Name: "version.txt", Path: "/version.txt", Size: 7, Kind: "system"})
}

// group lists a group's folders, a name once. kind is "" when there is no such group.
func (s *Server) group(group string) (kind string, es []entry) {
	var keep func(t Torrent) bool
	switch {
	case group == "__all__":
		kind, keep = "system", func(Torrent) bool { return true }
	case group == "torrents":
		kind, keep = "system", func(t Torrent) bool { return !t.NZB }
	case group == "nzbs":
		kind, keep = "system", func(t Torrent) bool { return t.NZB }
	case group == "__bad__":
		kind, keep = "system", func(Torrent) bool { return false }
	case contains(s.Providers, group):
		kind, keep = "provider", func(t Torrent) bool { return t.Provider == group }
	case contains(s.Virtual, group):
		kind, keep = "virtual", func(Torrent) bool { return false }
	default:
		return "", nil
	}
	seen := map[string]bool{}
	for _, t := range s.Torrents {
		if !keep(t) || seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		var size int64
		for _, f := range t.Files {
			size += f.Size
		}
		es = append(es, entry{Name: t.Name, Path: "/" + group + "/" + t.Name, Size: size, IsDir: true,
			InfoHash: t.Hash, CanDelete: true, ActiveDebrid: t.Provider, Kind: "entry", modTime: t.AddedOn})
	}
	return kind, es
}

// folder is the files of the folder name: every torrent of that name merged, the newer torrent's file winning.
// Nil when no torrent has the name, or none has a file.
func (s *Server) folder(name string) []entry {
	type owned struct {
		File
		added time.Time
	}
	files := map[string]owned{}
	for _, t := range s.Torrents {
		if t.Name != name {
			continue
		}
		for _, f := range t.Files {
			if old, ok := files[f.Name]; !ok || t.AddedOn.After(old.added) || t.AddedOn.Equal(old.added) && f.Size != old.Size {
				files[f.Name] = owned{f, t.AddedOn}
			}
		}
	}
	var es []entry
	for _, f := range files {
		es = append(es, entry{Name: f.Name, Size: f.Size, Kind: "file", modTime: f.added})
	}
	sortEntries(es)
	return es
}

func sortEntries(es []entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].IsDir != es[j].IsDir {
			return es[i].IsDir
		}
		a, b := strings.ToLower(es[i].Name), strings.ToLower(es[j].Name)
		if a != b {
			return a < b
		}
		return es[i].Path < es[j].Path
	})
}

func (s *Server) browse(w http.ResponseWriter, r *http.Request, rec string, segs []string) {
	var es []entry
	cur, parent, kind := "/", "", "system"
	switch {
	case len(segs) == 0:
		es = s.root()
	case segs[0] == "download":
		s.t.Errorf("decypharrtest: %s: the download route makes decypharr fetch a provider link", rec)
		http.NotFound(w, r)
		return
	case len(segs) == 1:
		if kind, es = s.group(segs[0]); kind == "" {
			http.Error(w, "Group not found", http.StatusNotFound)
			return
		}
		cur, parent = "/"+segs[0], "/"
	case len(segs) == 2:
		// decypharr finds the folder by name alone and does not look at the group.
		if es = s.folder(segs[1]); es == nil {
			http.Error(w, "Torrent not found", http.StatusNotFound)
			return
		}
		for i := range es {
			es[i].Path = path.Join("/", segs[0], segs[1], es[i].Name)
		}
		cur, parent, kind = "/"+segs[0]+"/"+segs[1], "/"+segs[0], "entry"
	default:
		s.t.Errorf("decypharrtest: %s: not a browse listing", rec)
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	if search := strings.ToLower(strings.TrimSpace(q.Get("search"))); search != "" && len(segs) > 0 {
		kept := es[:0]
		for _, e := range es {
			if strings.Contains(strings.ToLower(e.Name), search) {
				kept = append(kept, e)
			}
		}
		es = kept
	}
	for i := range es {
		es[i].ModTime = es[i].modTime.Format("2006-01-02 15:04:05")
	}
	sortEntries(es)
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 50
	}
	total := len(es)
	out := []entry{}
	if off := (page - 1) * limit; off < total {
		out = es[off:min(off+limit, total)]
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"entries": out, "total": total, "page": page, "limit": limit,
		"total_pages": (total + limit - 1) / limit, "current_dir": cur, "parent_dir": parent, "current_kind": kind})
}

// torrentFiles is GET /api/v2/torrents/files?hash=. decypharr numbers the files in map order; here it is the
// torrent's order.
func (s *Server) torrentFiles(w http.ResponseWriter, r *http.Request) {
	hash := strings.ToLower(r.URL.Query().Get("hash"))
	for _, t := range s.Torrents {
		if !t.Queued || strings.ToLower(t.Hash) != hash {
			continue
		}
		type file struct {
			Index int    `json:"index"`
			Name  string `json:"name,omitempty"`
			Size  int64  `json:"size,omitempty"`
		}
		files := []file{}
		for i, f := range t.Files {
			files = append(files, file{Index: i, Name: f.Name, Size: f.Size})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(files)
		return
	}
	http.Error(w, "Entry not found", http.StatusNotFound)
}

func (s *Server) webdav(w http.ResponseWriter, r *http.Request, rec string, segs []string) {
	if s.Loading {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "WebDAV service is initializing, please try again shortly", http.StatusServiceUnavailable)
		return
	}
	if s.Redirect != "" {
		http.Redirect(w, r, s.Base+s.Redirect, http.StatusFound)
		return
	}
	if s.DAVUser != "" {
		if u, p, ok := r.BasicAuth(); !ok || u != s.DAVUser || p != s.DAVPassword {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		s.t.Errorf("decypharrtest: %s: the API token sent to WebDAV", rec)
	}
	if r.Method != "PROPFIND" {
		// File reads make decypharr fetch a provider link; a folder HEAD crashes its handler. Nothing reads yet.
		s.t.Errorf("decypharrtest: %s: only PROPFIND is served on WebDAV", rec)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	// The hrefs carry the request's own path (url_base and /webdav included), cleaned.
	self := path.Clean(r.URL.Path)
	var es []entry
	hasSelf := true
	switch len(segs) {
	case 0:
		es = s.root()
	case 1:
		kind, list := s.group(segs[0])
		if kind == "" {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		es = list
	case 2:
		// A missing folder is not a 404: the multistatus is just empty.
		es = s.folder(segs[1])
		hasSelf = es != nil
	default:
		s.t.Errorf("decypharrtest: %s: PROPFIND of a file is not used", rec)
		http.NotFound(w, r)
		return
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><d:multistatus xmlns:d="DAV:">`)
	item := func(href, name string, dir bool, size int64) {
		b.WriteString(`<d:response><d:href>` + esc(escapePath(href)) + `</d:href><d:propstat><d:prop>`)
		if dir {
			b.WriteString(`<d:resourcetype><d:collection/></d:resourcetype>`)
		} else {
			fmt.Fprintf(&b, `<d:resourcetype/><d:getcontentlength>%d</d:getcontentlength>`, size)
		}
		b.WriteString(`<d:getlastmodified>2026-01-02T03:04:05Z</d:getlastmodified><d:displayname>` + esc(name) +
			`</d:displayname></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
	}
	if hasSelf {
		name := ""
		if len(segs) > 0 {
			name = segs[len(segs)-1]
		}
		item(self, name, true, 0)
	}
	for _, e := range es {
		href := path.Join("/", self, e.Name)
		if e.IsDir {
			href += "/"
		}
		item(href, e.Name, e.IsDir, e.Size)
	}
	b.WriteString(`</d:multistatus>`)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write([]byte(b.String()))
}

func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// escapePath percent-escapes everything but unreserved characters and "/".
func escapePath(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', strings.IndexByte("-_.~/", c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Hash is a made-up 40-character hash: the two-digit hex of n, repeated.
func Hash(n int) string { return strings.Repeat(fmt.Sprintf("%02x", n%256), 20) }

// Names of the fixture that tests ask for.
const (
	// SharedName is held by two torrents on two debrids: one folder, files merged.
	SharedName = "Marrow.Lantern.2014.1080p.BluRay.x264-FERNWOOD"
	// HiddenName is held by two torrents on the same debrid: only the first one's hash is listed anywhere.
	HiddenName = "Copper Tide (2009) [1080p] [WEB-DL]"
	// OddName has characters that need escaping in a URL and in XML.
	OddName = "Glass & Thistle #2 100% <Remux> (2021)"
	// StrippedName is a single-file torrent's name with its extension removed by decypharr.
	StrippedName = "Hollow.Meridian.2018.2160p.WEB-DL.DDP5.1.HEVC-OSPREY"
	// QueuedName was added through decypharr's qBittorrent API and is still queued.
	QueuedName = "Pale.Ferry.S02.1080p.AMZN.WEB-DL.DDP2.0.H.264-TILLER"
)

// Hashes of the fixture, by role.
var (
	SharedFirst  = Hash(1) // on debrid "realdebrid", older
	SharedSecond = Hash(2) // on debrid "torbox", newer
	HiddenFirst  = Hash(3)
	HiddenSecond = Hash(4) // not listed in any group
	OddHash      = Hash(5)
	StrippedHash = Hash(6)
	BareHash     = Hash(7) // its folder name is the hash itself
	QueuedHash   = Hash(8)
	NZBHash      = Hash(9)
)

// FixtureSeries is how many generated season folders Fixture adds, enough for three pages of 100.
const FixtureSeries = 230

// Fixture is a library of invented names with the shapes decypharr listings have: a name shared across two
// debrids, a name shared on one debrid, a name with odd characters, a single-file name with its extension stripped,
// a folder named by its hash, a name cut short, a usenet entry, a queued torrent, and FixtureSeries generated
// folders for paging. Providers are "realdebrid" and "torbox".
func Fixture() (providers []string, torrents []Torrent) {
	day := func(n int) time.Time { return time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).AddDate(0, 0, n) }
	torrents = []Torrent{
		{Hash: SharedFirst, Name: SharedName, Provider: "realdebrid", AddedOn: day(1), Files: []File{
			{Name: "Marrow.Lantern.2014.1080p.BluRay.x264-FERNWOOD.mkv", Size: 8_100_000_001},
			{Name: "Marrow.Lantern.2014.Extras.mkv", Size: 700_000_001}}},
		{Hash: SharedSecond, Name: SharedName, Provider: "torbox", AddedOn: day(2), Files: []File{
			{Name: "Marrow.Lantern.2014.1080p.BluRay.x264-FERNWOOD.mkv", Size: 8_100_000_777}}},
		{Hash: HiddenFirst, Name: HiddenName, Provider: "realdebrid", AddedOn: day(3), Files: []File{
			{Name: "Copper Tide (2009).mkv", Size: 4_000_000_003}}},
		{Hash: HiddenSecond, Name: HiddenName, Provider: "realdebrid", AddedOn: day(4), Files: []File{
			{Name: "Copper Tide (2009).mkv", Size: 4_000_000_004}}},
		{Hash: OddHash, Name: OddName, Provider: "torbox", AddedOn: day(5), Files: []File{
			{Name: "Glass & Thistle #2 100% <Remux>.mkv", Size: 31_000_000_005}}},
		{Hash: StrippedHash, Name: StrippedName, Provider: "realdebrid", AddedOn: day(6), Files: []File{
			{Name: StrippedName + ".mkv", Size: 17_000_000_006}}},
		{Hash: BareHash, Name: BareHash, Provider: "torbox", AddedOn: day(7), Files: []File{
			{Name: "video.mkv", Size: 900_000_007}}},
		{Hash: QueuedHash, Name: QueuedName, Provider: "realdebrid", AddedOn: day(8), Queued: true, Files: []File{
			{Name: "Pale.Ferry.S02E01.1080p.AMZN.WEB-DL.DDP2.0.H.264-TILLER.mkv", Size: 2_100_000_008},
			{Name: "Pale.Ferry.S02E02.1080p.AMZN.WEB-DL.DDP2.0.H.264-TILLER.mkv", Size: 2_200_000_008}}},
		{Hash: NZBHash, Name: "Tin.Sparrow.2020.720p.WEB.h264-ALDER", NZB: true, AddedOn: day(9), Files: []File{
			{Name: "Tin.Sparrow.2020.720p.WEB.h264-ALDER.mkv", Size: 1_500_000_009}}},
		// A name cut short, as one debrid does with long names.
		{Hash: Hash(10), Name: "The.Long.Quiet.Hours.Of.A.Lighthouse.Keeper.S01.COMPLETE.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.2",
			Provider: "torbox", AddedOn: day(10), Files: []File{{Name: "The.Long.Quiet.Hours.S01E01.mkv", Size: 6_000_000_010}}},
	}
	for i := 0; i < FixtureSeries; i++ {
		p := "realdebrid"
		if i%3 == 0 {
			p = "torbox"
		}
		name := fmt.Sprintf("Quiet.Orchard.S%03d.1080p.WEB-DL.x265-BRAMBLE", i+1)
		torrents = append(torrents, Torrent{Hash: fmt.Sprintf("%040x", 0xabc000+i), Name: name, Provider: p,
			AddedOn: day(20 + i), Files: []File{{Name: name + ".E01.mkv", Size: int64(1_000_000_000 + i)}}})
	}
	return []string{"realdebrid", "torbox"}, torrents
}
