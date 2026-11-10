package library

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
)

// decypharr's read-only surface (library.mount.decypharr), confirmed against decypharr v2.7.1. The client sends
// PROPFIND, GET and HEAD and nothing else, and only to these routes:
//   - GET /api/browse/: the groups (__all__, torrents, one folder per debrid, ...), each with a kind;
//   - GET /api/browse/<group>: the release folders of a group, paged, each with its torrent hash;
//   - GET /api/browse/<group>/<folder>: the files of a release folder, paged (no hash per file);
//   - GET /api/v2/torrents/files?hash=: decypharr's own file list of a torrent it still has queued (qBittorrent API);
//   - PROPFIND /webdav/[<group>[/<folder>]]: the same listing over WebDAV;
//   - GET or HEAD /webdav/<group>/<folder>/<file>: allowed by shape for direct mode; nothing here sends one.
//
// Everything else is refused before a request is made (see decypharrAllowed). decypharr has routes that change
// state on a plain GET (GET /api/v2/app/shutdown, /api/v2/torrents/pause) and a download route that redirects to a
// provider link (GET /api/browse/download/...), so the list is by route, not by method alone.
//
// Read-only on the wire is not "no side effects": a file read makes decypharr fetch a provider link, and for
// Real-Debrid that can replace the torrent. This client never reads a file.
//
// Two auth schemes: the API takes "Authorization: Bearer <api token>" when decypharr's use_auth is on (also on
// /api/v2), WebDAV takes Basic only when enable_webdav_auth is on too.

// DecypharrTarget is the gateway target for decypharr. Redirects are never followed: the client refuses any 3xx.
var DecypharrTarget = gateway.Target{Class: gateway.ClassDecypharr, Key: gateway.DecypharrKey, NoFollow: true}

// DecypharrConfig is what the client needs to know about one decypharr.
type DecypharrConfig struct {
	// URL is decypharr's base URL including its url_base, e.g. http://decypharr:8282.
	URL string
	// Token is the API token, sent as Bearer on /api/browse and /api/v2 when set.
	Token string
	// Username and Password are sent as Basic on /webdav when Username is set.
	Username string
	Password string
	// Interval is the index's maximum age. Default 1m.
	Interval time.Duration
	// Budget is the gateway budget the target is registered with. The client does not read it.
	Budget string
}

// Errors of the decypharr client.
var (
	// ErrDecypharrRefused: the method or route is not on the client's list; no request was made.
	ErrDecypharrRefused = errors.New("library: decypharr request refused: not a read-only route")
	// ErrDecypharrRedirect: decypharr answered 3xx (its login or setup page, or a provider link). Never followed.
	ErrDecypharrRedirect = errors.New("library: decypharr answered with a redirect")
	// ErrDecypharrAuth: decypharr answered 401.
	ErrDecypharrAuth = errors.New("library: decypharr refuses the credentials")
	// ErrDecypharrLoading: decypharr's WebDAV answers 503, it is still loading.
	ErrDecypharrLoading = errors.New("decypharr is still loading (WebDAV 503)")
	// ErrDecypharrIndexUnknown: the index could not be built, so what decypharr lists is not known. It wraps the
	// cause. It is not "not listed".
	ErrDecypharrIndexUnknown = errors.New("library: decypharr index not available")
)

// DecypharrEntry is one entry of a browse listing: a group at the root, a release folder in a group.
type DecypharrEntry struct {
	// Name is decypharr's exact name.
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"is_dir"`
	// Hash is the torrent hash of a release folder, lower case. A folder that merges same-named torrents shows one
	// of their hashes. Empty for groups and files.
	Hash string `json:"info_hash"`
	// Provider is the debrid a release folder is on.
	Provider string `json:"active_debrid"`
	// Kind is system, provider or virtual for a group, entry for a release folder, file for a file.
	Kind string `json:"kind"`
}

// DecypharrFile is one file of a release folder. Folders are flat: Name has no directory part.
type DecypharrFile struct {
	Name string
	Size int64
}

// DecypharrTorrentFile is one file of GET /api/v2/torrents/files. decypharr numbers the files in map order, so
// Index is not stable between two calls: match by Name and Size.
type DecypharrTorrentFile struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
}

// Decypharr reads decypharr through the gateway. Safe for concurrent use.
type Decypharr struct {
	Config DecypharrConfig
	GW     ZurgLogDoer
	// Target is the gateway target. Default DecypharrTarget. NoFollow is always set.
	Target gateway.Target
	Logger *slog.Logger
	Now    func() time.Time

	// build serialises index builds and change checks: a build is dozens of requests.
	build sync.Mutex

	mu sync.Mutex
	ix *DecypharrIndex
	// hintAt is when the group totals were last compared with the index's.
	hintAt time.Time
	// failAt and failErr are the last failed build; it is not retried for decypharrRetryFloor.
	failAt  time.Time
	failErr error
	// readyAt is when WebDAV was last asked; loading is set while it answers 503.
	readyAt time.Time
	loading bool
}

func (d *Decypharr) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Decypharr) interval() time.Duration {
	if d.Config.Interval > 0 {
		return d.Config.Interval
	}
	return time.Minute
}

func (d *Decypharr) log() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

// decypharrRoute is one of the three route families the client knows.
type decypharrRoute int

const (
	// decypharrBrowse is /api/browse/[<group>[/<folder>]].
	decypharrBrowse decypharrRoute = iota
	// decypharrTorrentFiles is /api/v2/torrents/files.
	decypharrTorrentFiles
	// decypharrDAV is /webdav/[<group>[/<folder>[/<file>]]].
	decypharrDAV
)

const methodPropfind = "PROPFIND"

// decypharrAllowed is the client's method and route list: nil when the request may be sent.
//   - Methods: PROPFIND, GET, HEAD.
//   - Browse API: GET of the root, a group or a release folder. The groups download (the download route, a redirect
//     to a provider link), __bad__ and nzbs are not read.
//   - GET /api/v2/torrents/files, the only route of the qBittorrent API.
//   - WebDAV: PROPFIND of the root, a group or a release folder; GET and HEAD of a file only. A folder is never
//     sent GET or HEAD (decypharr's HEAD of a missing folder dereferences nil).
//
// A segment is one path element: empty, ".", ".." or one holding "/" is refused, so a name cannot reach another
// route.
func decypharrAllowed(method string, route decypharrRoute, segs []string) error {
	refuse := func(why string) error {
		return fmt.Errorf("%w: %s %s (%s)", ErrDecypharrRefused, method, decypharrRoutePath(route, segs), why)
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." || strings.Contains(s, "/") {
			return refuse("bad path element")
		}
	}
	switch route {
	case decypharrBrowse:
		if method != http.MethodGet {
			return refuse("the browse API is read with GET only")
		}
		if len(segs) > 2 {
			return refuse("no such listing")
		}
		if len(segs) > 0 {
			switch segs[0] {
			case "download", "__bad__", "nzbs":
				return refuse("group not read")
			}
		}
	case decypharrTorrentFiles:
		if method != http.MethodGet || len(segs) != 0 {
			return refuse("torrents/files is read with GET only")
		}
	case decypharrDAV:
		switch method {
		case methodPropfind:
			if len(segs) > 2 {
				return refuse("PROPFIND is for folders")
			}
		case http.MethodGet, http.MethodHead:
			if len(segs) != 3 || segs[0] == "stream" {
				return refuse("GET and HEAD are for a file in a release folder")
			}
		default:
			return refuse("method not allowed")
		}
	default:
		return refuse("unknown route")
	}
	return nil
}

// decypharrRoutePath is the route's path below the base URL, each segment escaped.
func decypharrRoutePath(route decypharrRoute, segs []string) string {
	var p string
	switch route {
	case decypharrBrowse:
		p = "/api/browse"
		if len(segs) == 0 {
			p += "/"
		}
	case decypharrTorrentFiles:
		p = "/api/v2/torrents/files"
	case decypharrDAV:
		p = "/webdav"
		if len(segs) == 0 {
			p += "/"
		}
	default:
		p = "/unknown"
	}
	for _, s := range segs {
		p += "/" + url.PathEscape(s)
	}
	return p
}

// do is the client's only way to send a request: it refuses what decypharrAllowed refuses, adds the route's
// credentials, and turns a 3xx or a 401 into an error. The caller closes the body of a returned response.
func (d *Decypharr) do(ctx context.Context, method string, route decypharrRoute, segs []string, q url.Values) (*http.Response, error) {
	if err := decypharrAllowed(method, route, segs); err != nil {
		return nil, err
	}
	u, err := url.Parse(d.Config.URL)
	if err != nil || u.Host == "" {
		return nil, errors.New("library: bad decypharr URL")
	}
	escaped := strings.TrimSuffix(u.EscapedPath(), "/") + decypharrRoutePath(route, segs)
	if u.Path, err = url.PathUnescape(escaped); err != nil {
		return nil, errors.New("library: bad decypharr URL")
	}
	u.RawPath = escaped
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if route == decypharrDAV {
		if d.Config.Username != "" {
			req.SetBasicAuth(d.Config.Username, d.Config.Password)
		}
		if method == methodPropfind {
			req.Header.Set("Depth", "1")
		}
	} else {
		if d.Config.Token != "" {
			req.Header.Set("Authorization", "Bearer "+d.Config.Token)
		}
		req.Header.Set("Accept", "application/json")
	}
	t := d.Target
	if t.Class == "" {
		t = DecypharrTarget
	}
	t.NoFollow = true
	resp, err := d.GW.Do(ctx, t, req)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		drainClose(resp)
		return nil, fmt.Errorf("%w: %s %s: HTTP %d", ErrDecypharrRedirect, method, decypharrRoutePath(route, segs),
			resp.StatusCode)
	case resp.StatusCode == http.StatusUnauthorized:
		drainClose(resp)
		return nil, fmt.Errorf("%w: %s %s", ErrDecypharrAuth, method, decypharrRoutePath(route, segs))
	}
	return resp, nil
}

func drainClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

// getJSON reads one JSON answer. found is false for a 404.
func (d *Decypharr) getJSON(ctx context.Context, route decypharrRoute, segs []string, q url.Values, into any) (found bool, err error) {
	resp, err := d.do(ctx, http.MethodGet, route, segs, q)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	what := decypharrRoutePath(route, segs)
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		drainClose(resp)
		return false, nil
	default:
		drainClose(resp)
		return false, fmt.Errorf("library: decypharr %s: HTTP %d", what, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return false, fmt.Errorf("library: decypharr %s: %w", what, err)
	}
	return true, nil
}

// decypharrPageLimit is the browse API's largest page: a larger limit falls back to 50.
const decypharrPageLimit = 100

// decypharrMaxPages stops a listing that never ends (10 million entries).
const decypharrMaxPages = 100000

// decypharrPage is one page of a browse listing.
type decypharrPage struct {
	Entries    []DecypharrEntry `json:"entries"`
	Total      int              `json:"total"`
	TotalPages int              `json:"total_pages"`
}

// browsePage reads one page of a browse listing. found is false when decypharr has no such group or folder.
func (d *Decypharr) browsePage(ctx context.Context, segs []string, page, limit int) (*decypharrPage, bool, error) {
	var p decypharrPage
	q := url.Values{"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(limit)}}
	found, err := d.getJSON(ctx, decypharrBrowse, segs, q, &p)
	if err != nil || !found {
		return nil, false, err
	}
	for i := range p.Entries {
		p.Entries[i].Hash = strings.ToLower(p.Entries[i].Hash)
	}
	return &p, true, nil
}

// browseAll reads every page of a browse listing; total is the listing's own count.
func (d *Decypharr) browseAll(ctx context.Context, segs []string) (all []DecypharrEntry, total int, found bool, err error) {
	for page := 1; page <= decypharrMaxPages; page++ {
		p, found, err := d.browsePage(ctx, segs, page, decypharrPageLimit)
		if err != nil || !found {
			return nil, 0, false, err
		}
		all = append(all, p.Entries...)
		if page == 1 {
			total = p.Total
		}
		if page >= p.TotalPages || len(p.Entries) == 0 {
			return all, total, true, nil
		}
	}
	return nil, 0, false, errors.New("library: decypharr listing has too many pages")
}

// Groups lists decypharr's root: the system groups, one group per debrid, virtual folders and version.txt.
func (d *Decypharr) Groups(ctx context.Context) ([]DecypharrEntry, error) {
	all, _, found, err := d.browseAll(ctx, nil)
	if err == nil && !found {
		err = errors.New("library: decypharr has no browse API")
	}
	return all, err
}

// GroupEntries lists the release folders of a group (__all__, torrents, a debrid's folder). decypharr lists a name
// once per group, so same-named torrents show as one entry with one of their hashes. found is false when decypharr
// has no such group.
func (d *Decypharr) GroupEntries(ctx context.Context, group string) (entries []DecypharrEntry, found bool, err error) {
	entries, _, found, err = d.browseAll(ctx, []string{group})
	return entries, found, err
}

// FolderFiles lists the files of a release folder through the browse API. found is false when decypharr has no
// such folder, or it has no files. decypharr finds the folder by name alone; group only has to be a group that may
// be read.
func (d *Decypharr) FolderFiles(ctx context.Context, group, folder string) (files []DecypharrFile, found bool, err error) {
	entries, _, found, err := d.browseAll(ctx, []string{group, folder})
	if err != nil || !found {
		return nil, false, err
	}
	for _, e := range entries {
		if !e.IsDir {
			files = append(files, DecypharrFile{Name: e.Name, Size: e.Size})
		}
	}
	return files, true, nil
}

// davMultistatus is the part of a PROPFIND answer that is read.
type davMultistatus struct {
	Responses []struct {
		Prop struct {
			Collection *struct{} `xml:"resourcetype>collection"`
			Length     int64     `xml:"getcontentlength"`
			Name       string    `xml:"displayname"`
		} `xml:"propstat>prop"`
	} `xml:"response"`
}

// propfind sends one PROPFIND and decodes its 207.
func (d *Decypharr) propfind(ctx context.Context, segs []string) (*davMultistatus, error) {
	resp, err := d.do(ctx, methodPropfind, decypharrDAV, segs, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	what := decypharrRoutePath(decypharrDAV, segs)
	switch resp.StatusCode {
	case http.StatusMultiStatus:
	case http.StatusServiceUnavailable:
		drainClose(resp)
		d.setLoading(true)
		return nil, fmt.Errorf("library: decypharr %s: %w", what, ErrDecypharrLoading)
	default:
		drainClose(resp)
		return nil, fmt.Errorf("library: decypharr %s: HTTP %d", what, resp.StatusCode)
	}
	var ms davMultistatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, fmt.Errorf("library: decypharr %s: %w", what, err)
	}
	return &ms, nil
}

// PropfindFolder lists the files of a release folder over WebDAV. found is false when the folder is not there:
// decypharr answers a missing folder (and one without files) with 207 and an empty multistatus, not 404.
func (d *Decypharr) PropfindFolder(ctx context.Context, group, folder string) (files []DecypharrFile, found bool, err error) {
	ms, err := d.propfind(ctx, []string{group, folder})
	if err != nil || len(ms.Responses) == 0 {
		return nil, false, err
	}
	// The folder itself is the one collection; its files follow.
	for _, r := range ms.Responses {
		if r.Prop.Collection == nil {
			files = append(files, DecypharrFile{Name: r.Prop.Name, Size: r.Prop.Length})
		}
	}
	return files, true, nil
}

// TorrentFiles is decypharr's own file list for a torrent added through its qBittorrent API. found is false when
// decypharr has no queue entry for the hash (404): it was never added there, or decypharr removed it when it
// completed. An error is anything else.
func (d *Decypharr) TorrentFiles(ctx context.Context, hash string) (files []DecypharrTorrentFile, found bool, err error) {
	found, err = d.getJSON(ctx, decypharrTorrentFiles, nil, url.Values{"hash": {strings.ToLower(hash)}}, &files)
	if err != nil || !found {
		return nil, false, err
	}
	return files, true, nil
}

// Ready asks decypharr's WebDAV root whether it has loaded: ErrDecypharrLoading on 503, nil otherwise. Only WebDAV
// answers 503 while decypharr loads. Any other answer (refused credentials, a refused connection, another status)
// is no answer: nil, and the mount checks alone decide. A loading answer is reused for readyLoadingTTL, the others
// for the interval.
func (d *Decypharr) Ready(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	age := d.now().Sub(d.readyAt)
	fresh := !d.readyAt.IsZero() && (age < readyLoadingTTL || !d.loading && age < d.interval())
	loading := d.loading
	d.mu.Unlock()
	if !fresh {
		_, err := d.propfind(ctx, nil)
		if ctx.Err() != nil {
			return nil
		}
		// The 503 carries Retry-After, so the gateway backs decypharr off: a request it refuses for that says
		// nothing new, and a loading decypharr stays loading.
		var refused *gateway.Refused
		backoff := errors.As(err, &refused) && refused.Reason == gateway.ReasonBackoff
		loading = errors.Is(err, ErrDecypharrLoading) || backoff && loading
		d.setLoading(loading)
	}
	if loading {
		return ErrDecypharrLoading
	}
	return nil
}

// setLoading records what WebDAV last said about loading (any PROPFIND's 503 counts, not only Ready's).
func (d *Decypharr) setLoading(loading bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if loading != d.loading {
		if loading {
			d.log().Warn("library: decypharr is loading (WebDAV 503); mount sweeps wait")
		} else {
			d.log().Info("library: decypharr has loaded")
		}
	}
	d.readyAt, d.loading = d.now(), loading
}

// decypharrAllGroup is the group that lists every entry.
const decypharrAllGroup = "__all__"

// DecypharrIndex is one reading of which torrent hash decypharr lists under which folder name. It finds candidate
// folders and feeds the delete fence; it is never proof that a folder's files belong to a hash: same-named torrents
// share one folder, and of two on the same debrid only one hash is listed at all.
type DecypharrIndex struct {
	// At is when the reading was complete.
	At     time.Time
	names  map[string][]string // hash -> names
	hashes map[string][]string // name -> hashes
	// totals is each read group's count at the reading.
	totals map[string]int
}

// Names is the folder names decypharr lists hash under, exact. known is false on a nil index (decypharr could not
// be read): then "no names" is not an answer.
func (ix *DecypharrIndex) Names(hash string) (names []string, known bool) {
	if ix == nil {
		return nil, false
	}
	return ix.names[strings.ToLower(hash)], true
}

// Hashes is every hash decypharr lists under the folder name, lower case and sorted. More than one means the folder
// merges several torrents. known is false on a nil index.
func (ix *DecypharrIndex) Hashes(name string) (hashes []string, known bool) {
	if ix == nil {
		return nil, false
	}
	return ix.hashes[name], true
}

// Len is the number of hashes listed.
func (ix *DecypharrIndex) Len() int {
	if ix == nil {
		return 0
	}
	return len(ix.names)
}

// decypharrHintFloor is the least time between two change checks of FreshIndex.
const decypharrHintFloor = 5 * time.Second

// decypharrRetryFloor is how long a failed build is not retried.
const decypharrRetryFloor = 5 * time.Second

// Index is the hash and name index, read at most once per interval. The interval is its maximum age without
// condition: an older reading is never returned, whatever the group totals say. When it cannot be read the error
// wraps ErrDecypharrIndexUnknown (and the cause, e.g. ErrDecypharrAuth) and the index is nil.
func (d *Decypharr) Index(ctx context.Context) (*DecypharrIndex, error) {
	return d.index(ctx, false)
}

// FreshIndex is Index for a caller that waits for a torrent to show up: within the interval it first compares the
// group totals with the reading's (one small request per group, at most every decypharrHintFloor) and reads the
// index again when they differ. Equal totals are only a hint that nothing changed (a group lists a name once), so
// they never keep a reading past the interval.
func (d *Decypharr) FreshIndex(ctx context.Context) (*DecypharrIndex, error) {
	return d.index(ctx, true)
}

func (d *Decypharr) index(ctx context.Context, fresh bool) (*DecypharrIndex, error) {
	d.build.Lock()
	defer d.build.Unlock()
	d.mu.Lock()
	ix, hintAt, failAt, failErr := d.ix, d.hintAt, d.failAt, d.failErr
	d.mu.Unlock()
	now := d.now()
	if ix != nil && now.Sub(ix.At) < d.interval() {
		if !fresh || now.Sub(hintAt) < decypharrHintFloor {
			return ix, nil
		}
		changed, err := d.totalsChanged(ctx, ix)
		d.mu.Lock()
		d.hintAt = now
		d.mu.Unlock()
		// The check took time: the reading may have passed its age meanwhile, whatever the check said.
		if now = d.now(); (err != nil || !changed) && now.Sub(ix.At) < d.interval() {
			// A failed check says nothing; the reading is still within its age.
			return ix, nil
		}
	}
	if failErr != nil && now.Sub(failAt) < decypharrRetryFloor {
		return nil, failErr
	}
	ix, err := d.readIndex(ctx)
	if err != nil {
		err = fmt.Errorf("%w: %w", ErrDecypharrIndexUnknown, err)
		// The old reading goes whatever the cause (it was too old, or known to be out of date); a cancelled
		// reading is not kept as the failure, so the next caller reads again at once.
		d.mu.Lock()
		d.ix = nil
		if ctx.Err() == nil {
			if failErr == nil {
				d.log().Warn("library: decypharr index could not be read; what it lists is unknown for now", "err", err)
			}
			d.failAt, d.failErr = d.now(), err
		}
		d.mu.Unlock()
		return nil, err
	}
	d.mu.Lock()
	d.ix, d.hintAt, d.failErr = ix, ix.At, nil
	d.mu.Unlock()
	return ix, nil
}

// indexGroups is the groups the index reads: __all__ and every debrid's folder. __all__ lists a name once, so a
// name two debrids hold shows its second hash only in that debrid's folder.
func (d *Decypharr) indexGroups(ctx context.Context) ([]string, error) {
	root, err := d.Groups(ctx)
	if err != nil {
		return nil, err
	}
	var groups []string
	all := false
	for _, g := range root {
		switch {
		case !g.IsDir:
		case g.Name == decypharrAllGroup && g.Kind == "system":
			all = true
		case g.Kind == "provider":
			groups = append(groups, g.Name)
		}
	}
	if !all {
		return nil, errors.New("library: unsupported decypharr version: no " + decypharrAllGroup + " group")
	}
	sort.Strings(groups)
	return append([]string{decypharrAllGroup}, groups...), nil
}

// readIndex pages every index group.
func (d *Decypharr) readIndex(ctx context.Context) (*DecypharrIndex, error) {
	groups, err := d.indexGroups(ctx)
	if err != nil {
		return nil, err
	}
	ix := &DecypharrIndex{names: map[string][]string{}, hashes: map[string][]string{}, totals: map[string]int{}}
	seen := map[[2]string]bool{}
	for _, g := range groups {
		entries, total, found, err := d.browseAll(ctx, []string{g})
		if err == nil && !found {
			err = fmt.Errorf("library: decypharr group %s is gone", g)
		}
		if err != nil {
			return nil, err
		}
		ix.totals[g] = total
		for _, e := range entries {
			k := [2]string{e.Hash, e.Name}
			if !e.IsDir || e.Hash == "" || e.Name == "" || seen[k] {
				continue
			}
			seen[k] = true
			ix.names[e.Hash] = append(ix.names[e.Hash], e.Name)
			ix.hashes[e.Name] = append(ix.hashes[e.Name], e.Hash)
		}
	}
	for _, hs := range ix.hashes {
		sort.Strings(hs)
	}
	ix.At = d.now()
	return ix, nil
}

// totalsChanged compares the groups and their counts now with the reading's.
func (d *Decypharr) totalsChanged(ctx context.Context, ix *DecypharrIndex) (bool, error) {
	groups, err := d.indexGroups(ctx)
	if err != nil {
		return false, err
	}
	if len(groups) != len(ix.totals) {
		return true, nil
	}
	for _, g := range groups {
		was, ok := ix.totals[g]
		if !ok {
			return true, nil
		}
		p, found, err := d.browsePage(ctx, []string{g}, 1, 1)
		if err != nil {
			return false, err
		}
		if !found || p.Total != was {
			return true, nil
		}
	}
	return false, nil
}
