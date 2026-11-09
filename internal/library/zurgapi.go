package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
)

// zurg's read-only API (library.mount.zurg_api): a cheaper second source beside the mount listing. Four GETs (and
// /stats for the now playing page), never
// anything that changes zurg's state:
//   - /debug/torrents: every torrent with its name, hash and total video size; a size of 0 means the torrent lost its
//     video upstream (only .nfo, .txt or an .iso are left), which the mount shows only later as dangling links;
//   - /api/library/entry?path=/dav/<all_dir>/<folder>[/<file>]: the torrent hash (and, for a file, the size) of a
//     path on the mount, 404 when zurg has no such path;
//   - /torrents/repair/status: {"running": bool}. It names no torrents, so it is one global "repair in progress"
//     signal;
//   - /ready: 503 while zurg is still loading its library, 200 once loaded (outside basic auth). While it is 503
//     the mount counts as down (Library.mountHealthy): https://notes.debridmediamanager.com/zurg/setup/docker/
//     ("If /ready fails with error 503 the library is still loading").
//
// Every caller falls back to the mount-only behaviour when the API is off, unreachable or has no answer for a path.
// The listing backs off on its own clock: a failed or missing /debug/torrents does not stop the per-folder entries
// (an import's confirmation of its folder) from being asked.

// ZurgAPITarget is the gateway target for zurg's API (library.mount.zurg_api).
var ZurgAPITarget = gateway.Target{Class: gateway.ClassZurgAPI, Key: gateway.ZurgAPIKey}

// ZurgTorrent is one torrent of GET /debug/torrents.
type ZurgTorrent struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
	// Size is the total size of the torrent's video files: 0 when it has none left upstream.
	Size int64 `json:"size"`
}

// ZurgEntry is GET /api/library/entry for a release folder or a file in one. Only these fields are read: the answer
// also carries a signed streaming path, which is never decoded, stored or logged.
type ZurgEntry struct {
	Hash   string `json:"hash"`
	Name   string `json:"name"`
	IMDbID string `json:"imdb_id"`
	// Size is the file's size; 0 for a folder.
	Size int64 `json:"size"`
	// MediaInfo is zurg's probe of the file, null until it probed it.
	MediaInfo json.RawMessage `json:"media_info"`
}

// ZurgTorrents is one listing, indexed by hash and by the folder names zurg may give each torrent.
type ZurgTorrents struct {
	All      []ZurgTorrent
	byHash   map[string]ZurgTorrent
	byFolder map[string][]ZurgTorrent
}

func newZurgTorrents(all []ZurgTorrent) *ZurgTorrents {
	t := &ZurgTorrents{All: all, byHash: map[string]ZurgTorrent{}, byFolder: map[string][]ZurgTorrent{}}
	for _, z := range all {
		z.Hash = strings.ToLower(z.Hash)
		if z.Hash != "" {
			t.byHash[z.Hash] = z
		}
		for _, f := range ReleaseFolderNames(z.Hash, z.Name) {
			t.byFolder[f] = append(t.byFolder[f], z)
		}
	}
	return t
}

// NoVideo reports whether the torrent of a link has no video upstream: by its recorded hash when it has one, else by
// its release folder when every torrent zurg may show under that name has none. A hash zurg does not list, or an
// unknown folder, is not this case (the torrent is gone, or renamed: other causes).
func (t *ZurgTorrents) NoVideo(hash, folder string) bool {
	if t == nil {
		return false
	}
	if hash != "" {
		z, ok := t.byHash[strings.ToLower(hash)]
		return ok && z.Size == 0
	}
	zs := t.byFolder[folder]
	for _, z := range zs {
		if z.Size != 0 {
			return false
		}
	}
	return len(zs) > 0
}

// Name is zurg's name for the torrent of hash (its release folder's name by zurg's rule); false when the hash is
// not listed.
func (t *ZurgTorrents) Name(hash string) (string, bool) {
	if t == nil {
		return "", false
	}
	z, ok := t.byHash[strings.ToLower(hash)]
	return z.Name, ok && z.Name != ""
}

// NoVideoCount is how many listed torrents have no video upstream.
func (t *ZurgTorrents) NoVideoCount() int {
	if t == nil {
		return 0
	}
	n := 0
	for _, z := range t.All {
		if z.Size == 0 {
			n++
		}
	}
	return n
}

// ZurgAPI reads zurg's API through the gateway. Answers are reused for Interval; after a failed request the API is
// left alone for Interval (callers fall back to the mount), except that the torrent listing keeps its own retry
// clock. Safe for concurrent use.
type ZurgAPI struct {
	// URL is zurg's base URL, e.g. http://zurg:9999.
	URL string
	GW  ZurgLogDoer
	// AllDir is the release root's name on the mount (library.mount.all_dir); entry paths are /dav/<AllDir>/....
	AllDir string
	// Interval is how long an answer is reused. Default 1m.
	Interval time.Duration
	// RepairGrace bounds how long one running repair counts (Repairing). Default 2h.
	RepairGrace time.Duration
	Logger      *slog.Logger
	Now         func() time.Time

	mu        sync.Mutex
	list      *ZurgTorrents
	listAt    time.Time
	downUntil time.Time
	// listRetryAt is when the listing may be asked again after it failed with listErr (its own back-off).
	listRetryAt time.Time
	listErr     error
	running     bool
	repairAt    time.Time
	repairSince time.Time
	graceWarned bool
	// readyAt is when /ready was last asked; loading is set while it answers 503.
	readyAt time.Time
	loading bool
}

// inherit carries prev's cached answer, outage backoff and repair clock into z, a client for the same zurg built
// by a later settings generation (its gateway, and so its budget, are the new generation's).
func (z *ZurgAPI) inherit(prev *ZurgAPI) {
	if z == nil || prev == nil || z == prev || z.URL != prev.URL || z.AllDir != prev.AllDir {
		return
	}
	prev.mu.Lock()
	list, listAt, downUntil, running := prev.list, prev.listAt, prev.downUntil, prev.running
	listRetryAt, listErr := prev.listRetryAt, prev.listErr
	repairAt, repairSince, graceWarned := prev.repairAt, prev.repairSince, prev.graceWarned
	readyAt, loading := prev.readyAt, prev.loading
	prev.mu.Unlock()
	z.mu.Lock()
	z.list, z.listAt, z.downUntil, z.running = list, listAt, downUntil, running
	z.listRetryAt, z.listErr = listRetryAt, listErr
	z.repairAt, z.repairSince, z.graceWarned = repairAt, repairSince, graceWarned
	z.readyAt, z.loading = readyAt, loading
	z.mu.Unlock()
}

// ErrZurgAPIDown: a recent request failed, so the API is not asked again until the interval is over.
var ErrZurgAPIDown = errors.New("library: zurg API not answering")

func (z *ZurgAPI) now() time.Time {
	if z.Now != nil {
		return z.Now()
	}
	return time.Now()
}

func (z *ZurgAPI) interval() time.Duration {
	if z.Interval > 0 {
		return z.Interval
	}
	return time.Minute
}

func (z *ZurgAPI) log() *slog.Logger {
	if z.Logger != nil {
		return z.Logger
	}
	return slog.Default()
}

// get fetches one endpoint. ok is false for a 404 (no such entry). A failure keeps the API down for the interval.
func (z *ZurgAPI) get(ctx context.Context, endpoint string, q url.Values, into any) (ok bool, err error) {
	z.mu.Lock()
	down := z.now().Before(z.downUntil)
	z.mu.Unlock()
	if down {
		return false, ErrZurgAPIDown
	}
	ok, err = z.fetch(ctx, endpoint, q, into)
	if err != nil && ctx.Err() == nil {
		z.mu.Lock()
		if !z.now().Before(z.downUntil) {
			z.log().Warn("library: zurg API request failed; using the mount alone for now", "endpoint", endpoint,
				"err", err, "retry_in", z.interval())
		}
		z.downUntil = z.now().Add(z.interval())
		z.mu.Unlock()
	}
	return ok, err
}

func (z *ZurgAPI) fetch(ctx context.Context, endpoint string, q url.Values, into any) (bool, error) {
	u, err := url.Parse(z.URL)
	if err != nil || u.Host == "" {
		return false, errors.New("library: bad zurg_api URL")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + endpoint
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := z.GW.Do(ctx, ZurgAPITarget, req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		_, _ = io.Copy(io.Discard, resp.Body)
		return false, nil
	default:
		return false, fmt.Errorf("library: zurg API %s: HTTP %d", endpoint, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return false, fmt.Errorf("library: zurg API %s: %w", endpoint, err)
	}
	return true, nil
}

// ErrZurgLoading: zurg's /ready answers 503, its library is still loading. The mount counts as down.
var ErrZurgLoading = errors.New("zurg is still loading its library (/ready 503)")

// readyLoadingTTL is how long a 503 from /ready is reused: short, so the mount counts as up soon after zurg has
// loaded. Ready and unknown answers are reused for the interval.
const readyLoadingTTL = 15 * time.Second

// Ready asks zurg's /ready whether its library has loaded: ErrZurgLoading on 503, nil on 200. Any other answer (404
// from a zurg without the endpoint, a refused connection, a timeout, another status) is no answer: nil, and the
// mount checks alone decide, as without the API. It does not count as an API failure (no backoff of the other
// endpoints, no warning).
func (z *ZurgAPI) Ready(ctx context.Context) error {
	if z == nil {
		return nil
	}
	z.mu.Lock()
	age := z.now().Sub(z.readyAt)
	fresh := !z.readyAt.IsZero() && (age < readyLoadingTTL || !z.loading && age < z.interval())
	loading := z.loading
	z.mu.Unlock()
	if !fresh {
		status, err := z.readyStatus(ctx)
		if ctx.Err() != nil {
			return nil
		}
		loading = err == nil && status == http.StatusServiceUnavailable
		z.mu.Lock()
		if loading != z.loading {
			if loading {
				z.log().Warn("library: zurg is loading its library (/ready 503); mount sweeps wait")
			} else if !z.readyAt.IsZero() {
				z.log().Info("library: zurg has loaded its library (/ready)", "status", status)
			}
		}
		z.readyAt, z.loading = z.now(), loading
		z.mu.Unlock()
	}
	if loading {
		return ErrZurgLoading
	}
	return nil
}

// readyStatus is the status of GET /ready (body discarded).
func (z *ZurgAPI) readyStatus(ctx context.Context) (int, error) {
	u, err := url.Parse(z.URL)
	if err != nil || u.Host == "" {
		return 0, errors.New("library: bad zurg_api URL")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/ready"
	u.RawQuery = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, err
	}
	resp, err := z.GW.Do(ctx, ZurgAPITarget, req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

// mountHealthy is Mount.Healthy, plus zurg's /ready when the API is on: while zurg is loading its library the mount
// is down (ErrMountDown), so relink, the dangling-link sweep, verify, refresh, adopt, imports and the trash purge
// wait instead of reading a half-loaded __all__.
func (l *Library) mountHealthy(ctx context.Context) error {
	if err := l.zurg.Ready(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrMountDown, err)
	}
	return l.mount.Healthy(ctx)
}

// Torrents returns the torrent listing, fetched at most once per interval (FreshTorrents may read it sooner). A
// failed or missing listing is not asked again for the interval, on its own clock: it does not mark the whole API
// down, so the per-folder entries an import confirms its folder with are still asked.
func (z *ZurgAPI) Torrents(ctx context.Context) (*ZurgTorrents, error) {
	return z.torrents(ctx, z.interval())
}

// freshListFloor is the least time between two reads of the listing when FreshTorrents asks for a newer one.
const freshListFloor = 2 * time.Second

// FreshTorrents is the listing no older than freshListFloor, for a download that turned ready moments ago and is not
// in the cached listing: zurg lists a new torrent within seconds, while the cached listing can be an interval old.
// zurg serves the listing from memory, so a read every few seconds is cheap, and freshListFloor bounds it across all
// callers. A down API and the listing's own retry clock hold it back as they hold Torrents.
func (z *ZurgAPI) FreshTorrents(ctx context.Context) (*ZurgTorrents, error) {
	return z.torrents(ctx, freshListFloor)
}

// torrents returns the cached listing while it is younger than maxAge, else reads it again.
func (z *ZurgAPI) torrents(ctx context.Context, maxAge time.Duration) (*ZurgTorrents, error) {
	z.mu.Lock()
	if z.list != nil && z.now().Sub(z.listAt) < maxAge {
		l := z.list
		z.mu.Unlock()
		return l, nil
	}
	if z.now().Before(z.downUntil) {
		z.mu.Unlock()
		return nil, ErrZurgAPIDown
	}
	if z.listErr != nil && z.now().Before(z.listRetryAt) {
		err := z.listErr
		z.mu.Unlock()
		return nil, err
	}
	z.mu.Unlock()
	var all []ZurgTorrent
	ok, err := z.fetch(ctx, "/debug/torrents", nil, &all)
	if err == nil && !ok {
		err = errors.New("library: zurg API has no /debug/torrents")
	}
	if err != nil {
		if ctx.Err() == nil {
			z.mu.Lock()
			if !z.now().Before(z.listRetryAt) {
				z.log().Warn("library: zurg API listing failed; using the mount and entries alone for now",
					"endpoint", "/debug/torrents", "err", err, "retry_in", z.interval())
			}
			z.listRetryAt, z.listErr = z.now().Add(z.interval()), err
			z.mu.Unlock()
		}
		return nil, err
	}
	l := newZurgTorrents(all)
	z.mu.Lock()
	z.list, z.listAt, z.listErr = l, z.now(), nil
	z.mu.Unlock()
	return l, nil
}

// ZurgStats is the part of GET /stats the now playing page reads: the streams open now and the reads zurg refuses.
type ZurgStats struct {
	ActiveStreams int               `json:"active_streams"`
	BlockedReads  []ZurgBlockedRead `json:"blocked_reads"`
}

// ZurgBlockedRead is one file zurg refuses reads of right now.
type ZurgBlockedRead struct {
	Path     string `json:"path"`
	Filename string `json:"filename"`
	Hash     string `json:"torrent_hash"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
	Attempts int    `json:"attempts"`
}

// UnmarshalJSON accepts the textual and numeric status forms returned by zurg.
func (b *ZurgBlockedRead) UnmarshalJSON(data []byte) error {
	type plain ZurgBlockedRead
	var value struct {
		*plain
		Status json.RawMessage `json:"status"`
	}
	value.plain = (*plain)(b)
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if len(value.Status) == 0 {
		return nil
	}
	if err := json.Unmarshal(value.Status, &b.Status); err == nil {
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(value.Status, &number); err != nil {
		return err
	}
	b.Status = number.String()
	return nil
}

// Stats reads GET /stats (not cached: the now playing page asks while someone watches).
func (z *ZurgAPI) Stats(ctx context.Context) (*ZurgStats, error) {
	var out ZurgStats
	ok, err := z.get(ctx, "/stats", nil, &out)
	if err != nil || !ok {
		return nil, err
	}
	return &out, nil
}

// Entry returns zurg's entry for a release folder (rel "") or a file in it (rel is the path inside the folder);
// nil, nil when zurg has no such path.
func (z *ZurgAPI) Entry(ctx context.Context, folder, rel string) (*ZurgEntry, error) {
	p := "/dav/" + path.Join(z.allDir(), folder, rel)
	var e ZurgEntry
	ok, err := z.get(ctx, "/api/library/entry", url.Values{"path": {p}}, &e)
	if err != nil || !ok {
		return nil, err
	}
	e.Hash = strings.ToLower(e.Hash)
	return &e, nil
}

func (z *ZurgAPI) allDir() string {
	if z.AllDir != "" {
		return z.AllDir
	}
	return "__all__"
}

// RepairState is what Repairing read.
type RepairState struct {
	// Running is zurg's own answer.
	Running bool `json:"running"`
	// Since is when this process first saw the running repair.
	Since time.Time `json:"since,omitempty"`
	// Grace is true while dangling links are given grace: running, and within RepairGrace of Since.
	Grace bool `json:"grace"`
	// Error is set when the state could not be read (then there is no grace).
	Error string `json:"error,omitempty"`
}

// Repairing reads zurg's repair state (at most once per interval). While a repair runs, dangling links may be
// zurg's work in progress; the grace ends RepairGrace after the repair was first seen running, so a repair that
// never finishes cannot keep links alive for ever. An unreadable state gives no grace.
func (z *ZurgAPI) Repairing(ctx context.Context) RepairState {
	z.mu.Lock()
	fresh := !z.repairAt.IsZero() && z.now().Sub(z.repairAt) < z.interval()
	z.mu.Unlock()
	if !fresh {
		var st struct {
			Running *bool `json:"running"`
		}
		ok, err := z.get(ctx, "/torrents/repair/status", nil, &st)
		if err == nil && (!ok || st.Running == nil) {
			err = errors.New("library: zurg API: no repair state")
		}
		if err != nil {
			return RepairState{Error: err.Error()}
		}
		z.mu.Lock()
		z.repairAt = z.now()
		if *st.Running && !z.running {
			z.repairSince, z.graceWarned = z.repairAt, false
		}
		z.running = *st.Running
		z.mu.Unlock()
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if !z.running {
		return RepairState{}
	}
	grace := z.RepairGrace
	if grace <= 0 {
		grace = 2 * time.Hour
	}
	rs := RepairState{Running: true, Since: z.repairSince, Grace: z.now().Sub(z.repairSince) < grace}
	if !rs.Grace && !z.graceWarned {
		z.graceWarned = true
		z.log().Warn("library: zurg has been repairing longer than the grace; dangling links are marked again",
			"since", z.repairSince, "grace", grace)
	}
	return rs
}

// Dangling-link causes and reasons the API adds.
const (
	// CauseNoVideo: the link's torrent is listed by zurg with no video left upstream (size 0), or its release folder
	// is listed on the mount without a video file.
	CauseNoVideo = "no_video"
	// CauseRepairing: zurg is repairing, so the link is given grace instead of being marked dead.
	CauseRepairing = "repairing"

	ReasonNoVideo = "torrent has no video upstream"
)

// zurgView is what one pass reads from zurg at its start: the listing and the repair state. A nil view (the API
// off) classifies nothing and gives no grace.
type zurgView struct {
	list   *ZurgTorrents
	repair RepairState
	// err is why the listing could not be read ("" when it was).
	err string
	// ix is the pass's mount index, when it has one: a listed folder without a video file has no video upstream
	// too (zurg's size is the torrent's, not what it can still serve).
	ix *mountIndex
}

// zurgView reads the listing and the repair state for a pass; nil when the API is off.
func (l *Library) zurgView(ctx context.Context) *zurgView {
	if l.zurg == nil {
		return nil
	}
	v := &zurgView{repair: l.zurg.Repairing(ctx)}
	list, err := l.zurg.Torrents(ctx)
	if err != nil {
		v.err = err.Error()
	}
	v.list = list
	return v
}

func (v *zurgView) noVideo(hash, folder string) bool {
	return v != nil && (v.list.NoVideo(hash, folder) || v.ix.noVideo(folder))
}

// grace reports whether dangling links are left alone now: zurg is repairing, within the grace.
func (v *zurgView) grace() bool {
	return v != nil && v.repair.Grace
}

// zurgConfirm asks zurg which torrent holds a path on the mount (rel "" for the release folder). ok is false when
// the path is confirmed to belong to another torrent than hash; unknown when the API is off, does not answer, has no
// entry, or hash is empty: then the mount's answer stands.
func (l *Library) zurgConfirm(ctx context.Context, hash, folder, rel string) (e *ZurgEntry, ok bool) {
	if l.zurg == nil || hash == "" {
		return nil, true
	}
	e, err := l.zurg.Entry(ctx, folder, rel)
	if err != nil || e == nil || e.Hash == "" {
		return nil, true
	}
	return e, strings.EqualFold(e.Hash, hash)
}

// classify names the cause of a dangling link no relink fixes: the mount-only cause, unless zurg says more. Within a
// repair's grace the cause is CauseRepairing (the sweep does not mark it dead); the reason keeps what the listing
// said.
func (v *zurgView) classify(cause, hash, folder string) (string, string) {
	if v == nil {
		return cause, ""
	}
	reason := ""
	if v.noVideo(hash, folder) {
		cause, reason = CauseNoVideo, ReasonNoVideo
	}
	if v.grace() {
		r := "zurg is repairing; not marked dead (since " + v.repair.Since.UTC().Format(time.RFC3339) + ")"
		if reason != "" {
			r = reason + "; " + r
		}
		return CauseRepairing, r
	}
	return cause, reason
}

// note records the view on a scan.
func (v *zurgView) note(sc *LinkScan) {
	if v == nil {
		return
	}
	rs := v.repair
	sc.ZurgRepair, sc.ZurgError = &rs, v.err
}
