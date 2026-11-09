// Package nowplaying watches what Plex plays and says where each file comes from and why a playback struggles
// (docs/design/now-playing.md). It reads Plex's sessions and its notification websocket, zurg's /stats and log, the
// mount's rclone rc and Theatarr's database; it keeps its state in memory only. The one thing it does besides reading
// is the next-episode pre-warm: a head read of the next episode's file through the mount, then, past half of the
// episode, a rate-capped read of the whole file so the mount's cache keeps it.
package nowplaying

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/plex"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Polling: every activeEvery while someone looked at the page (or the API) within viewedWithin, else idleEvery.
const (
	activeEvery  = 10 * time.Second
	idleEvery    = 30 * time.Second
	viewedWithin = time.Minute
	cacheEvery   = time.Minute // vfs/stats
	// resolveEvery: a playback whose file resolved to no Theatarr link is looked up again (database and maybe a
	// zurg read) at most this often, not on every poll.
	resolveEvery = 2 * time.Minute
	// A playback is slow when, between two samples at least minSample apart while playing, its position moved less
	// than slowRatio of the wall time. A jump of more than seekRatio is a seek, not a measure.
	minSample = 5 * time.Second
	slowRatio = 0.8
	seekRatio = 3.0
	// recentBuffering: buffering this recent still counts as the playback struggling.
	recentBuffering = 2 * time.Minute
	bufferingWindow = 10 * time.Minute // the count shown
	// The pre-warm starts this far into an episode and reads at most prewarmBytes.
	prewarmAfter = 60 * time.Second
	prewarmBytes = 4 << 20
	prewarmLimit = 2 * time.Minute
)

// PlexAPI is what the tracker reads from Plex.
type PlexAPI interface {
	Sessions(ctx context.Context) ([]plex.Session, error)
	Notifications(ctx context.Context, on func(plex.PlayEvent)) error
}

// ZurgAPI is what it reads from zurg's API.
type ZurgAPI interface {
	Entry(ctx context.Context, folder, rel string) (*library.ZurgEntry, error)
	Stats(ctx context.Context) (*library.ZurgStats, error)
}

// Mount is the library's mount: its health, the pre-warm's head read and its whole-file caching read.
type Mount interface {
	MountHealthy(ctx context.Context) error
	Prewarm(ctx context.Context, target string, max int64) error
	MountFileSize(ctx context.Context, target string) (int64, error)
	CacheFile(ctx context.Context, target string, rate int64, progress func(done, size int64)) (int64, error)
}

// Options configure a Tracker. Plex and Store are required; the rest is optional.
type Options struct {
	Plex  PlexAPI
	Store *store.Store
	Zurg  ZurgAPI
	RC    *RC
	// Mount returns the current library (nil without one): mount health and the pre-warm.
	Mount func() Mount
	// LinkPath is the mount as symlinks name it (and Plex sees it), MountPath as this process sees it, AllDir its
	// release root.
	LinkPath, MountPath, AllDir string
	// Roots returns a version's library roots (by policy key): a target several versions link resolves to the
	// version whose root, or whose followed version's root, holds Plex's path. nil: not decided by roots.
	Roots func(version string) []string
	// Prewarm turns the next-episode pre-warm on.
	Prewarm bool
	// PrewarmRate caps the caching read in bytes per second; a next episode over PrewarmMaxSize bytes is only
	// head-checked. 0: the defaults (policy.DefaultPrewarmRate, DefaultPrewarmMaxSize).
	PrewarmRate, PrewarmMaxSize int64
	Logger                      *slog.Logger
	Now                         func() time.Time
	Readlink                    func(string) (string, error)
	// Report raises an error-inbox item (errinbox.Report by default).
	Report func(errinbox.Event)
}

// Tracker holds the current playbacks.
type Tracker struct {
	o    Options
	wake chan struct{}

	mu        sync.Mutex
	sess      map[string]*session
	updatedAt time.Time
	err       string
	viewedAt  time.Time
	events    []zurgEvent
	stats     *library.ZurgStats
	cache     *Cache
	cacheAt   time.Time
	warmed    map[string]bool // session key + link id
	warming   bool
	cached    map[string]bool // session key + link id: its caching read started
	caching   *cacheJob       // the one caching read running, server-wide
}

// cacheJob is the running caching read, shared by viewers of the same title and version.
type cacheJob struct {
	cancel context.CancelFunc
	nf     *nextFile
	state  *PrewarmState
}

// nextFile is a next episode whose head read passed: what its caching read needs.
type nextFile struct {
	linkID    int64 // the playing link
	target    string
	ep        string
	episodeID int64
	titleID   int64
	title     string
	versionID int64
	version   string
}

type session struct {
	s        plex.Session
	src      *Source
	servedBy string
	// Progress samples while playing.
	sampleOff int64
	sampleAt  time.Time
	slow      bool
	buffering []time.Time
	health    Health
	fetch     *Fetch
	prewarm   *PrewarmState
	next      *nextFile
	// resolvedAt is when src was last looked up (resolve).
	resolvedAt time.Time
}

// Health is a playback's verdict: ok, paused, buffering or slow, and for the last two the likeliest cause
// (transcode, provider, mount, network or unknown) with what points at it.
type Health struct {
	Verdict string `json:"verdict"`
	Cause   string `json:"cause,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Fetch is the mount's fetch of the playing file (rclone rc core/stats): only while the cache downloads it.
type Fetch struct {
	Speed float64 `json:"speed"` // bytes/s
	// Ratio is the fetch speed over the media bitrate; under 1 the cache falls behind playback.
	Ratio float64 `json:"ratio,omitempty"`
	Bytes int64   `json:"bytes"`
	Size  int64   `json:"size"`
}

// PrewarmState is the next-episode pre-warm of a playback.
type PrewarmState struct {
	Episode string `json:"episode"` // S01E04
	State   string `json:"state"`   // running, ok, caching, cached, failed, skipped
	Detail  string `json:"detail,omitempty"`
	// Progress is the caching read's share done, 0 to 1.
	Progress float64   `json:"progress,omitempty"`
	At       time.Time `json:"at"`
}

// SessionView is one playback as the API shows it.
type SessionView struct {
	Key        string          `json:"key"`
	UserID     string          `json:"user_id"`
	User       string          `json:"user"`
	Player     string          `json:"player"`
	Product    string          `json:"product,omitempty"`
	Platform   string          `json:"platform,omitempty"`
	State      string          `json:"state"`
	Type       string          `json:"type"`
	Title      string          `json:"title"`
	Show       string          `json:"show,omitempty"`
	Season     int             `json:"season,omitempty"`
	Episode    int             `json:"episode,omitempty"`
	ViewOffset int64           `json:"view_offset_ms"`
	Duration   int64           `json:"duration_ms"`
	Local      bool            `json:"local"`
	Relayed    bool            `json:"relayed"`
	Location   string          `json:"location,omitempty"`
	Bitrate    int64           `json:"bitrate_kbps,omitempty"`
	Resolution string          `json:"resolution,omitempty"`
	Decision   string          `json:"decision,omitempty"`
	Transcode  *plex.Transcode `json:"transcode,omitempty"`
	Buffering  int             `json:"buffering_events"`
	Health     Health          `json:"health"`
	Source     *Source         `json:"source,omitempty"`
	Fetch      *Fetch          `json:"fetch,omitempty"`
	Prewarm    *PrewarmState   `json:"prewarm,omitempty"`
}

// Snapshot is the API's answer.
type Snapshot struct {
	Enabled   bool          `json:"enabled"`
	UpdatedAt *time.Time    `json:"updated_at,omitempty"`
	Error     string        `json:"error,omitempty"`
	Sessions  []SessionView `json:"sessions"`
	// Cache is the mount's disk cache (rclone rc vfs/stats), when rclone_rc is set.
	Cache *Cache `json:"cache,omitempty"`
	// ZurgStreams is zurg's open streams (/stats), when zurg_api is set.
	ZurgStreams *int `json:"zurg_streams,omitempty"`
}

// New returns a tracker; Run starts it.
func New(o Options) *Tracker {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Readlink == nil {
		o.Readlink = os.Readlink
	}
	if o.Report == nil {
		o.Report = errinbox.Report
	}
	if o.AllDir == "" {
		o.AllDir = "__all__"
	}
	if o.LinkPath == "" {
		o.LinkPath = o.MountPath
	}
	if o.PrewarmRate <= 0 {
		o.PrewarmRate = policy.DefaultPrewarmRate
	}
	if o.PrewarmMaxSize <= 0 {
		o.PrewarmMaxSize = policy.DefaultPrewarmMaxSize
	}
	return &Tracker{o: o, wake: make(chan struct{}, 1), sess: map[string]*session{}, warmed: map[string]bool{},
		cached: map[string]bool{}}
}

// Run polls until ctx ends, and follows the notification websocket meanwhile.
func (t *Tracker) Run(ctx context.Context) {
	go t.notifications(ctx)
	for {
		t.poll(ctx)
		timer := time.NewTimer(t.interval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-t.wake:
			timer.Stop()
		}
	}
}

func (t *Tracker) interval() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.viewedAt.IsZero() && t.o.Now().Sub(t.viewedAt) < viewedWithin {
		return activeEvery
	}
	return idleEvery
}

func (t *Tracker) poke() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// notifications follows Plex's websocket, opening it again after 5 s, doubling to 5 min while it keeps failing.
func (t *Tracker) notifications(ctx context.Context) {
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		start := t.o.Now()
		err := t.o.Plex.Notifications(ctx, t.event)
		if ctx.Err() != nil {
			return
		}
		if t.o.Now().Sub(start) > time.Minute {
			backoff = 5 * time.Second
		}
		if err != nil {
			t.o.Logger.Debug("nowplaying: plex websocket ended", "err", err, "retry_in", backoff)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, 5*time.Minute)
	}
}

// event applies a playing-state change: the state (buffering only comes this way) and the position.
func (t *Tracker) event(e plex.PlayEvent) {
	t.mu.Lock()
	se := t.sess[e.SessionKey]
	if se != nil && (e.RatingKey == "" || e.RatingKey == se.s.RatingKey) && e.State != "stopped" {
		now := t.o.Now()
		changed := e.State != se.s.State
		t.observe(se, e.State, now)
		se.s.State = e.State
		if e.ViewOffset > 0 {
			se.s.ViewOffset = e.ViewOffset
		}
		// The verdict follows the state at once; a poll now finds its cause.
		if v := verdictOf(se, now); v != se.health.Verdict {
			se.health = Health{Verdict: v}
		}
		t.mu.Unlock()
		if changed {
			t.poke()
		}
		return
	}
	t.mu.Unlock()
	t.poke() // a new playback, another item or a stop: read the sessions now
}

// observe records a state seen at at: a change to buffering counts, and anything but playing ends the progress
// sample (a pause is not slowness).
func (t *Tracker) observe(se *session, state string, at time.Time) {
	if state == "buffering" && se.s.State != "buffering" {
		se.buffering = append(se.buffering, at)
	}
	if state != "playing" {
		se.sampleAt, se.slow = time.Time{}, false
	}
}

// ZurgLine takes one zurg log line (the library's log follower forwards them).
func (t *Tracker) ZurgLine(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.sess) == 0 && !strings.Contains(line, "cooling down") {
		return
	}
	var files []string
	for _, se := range t.sess {
		files = append(files, path.Base(se.s.File))
		if se.src != nil && se.src.Target != se.s.File {
			files = append(files, path.Base(se.src.Target))
		}
	}
	now := t.o.Now()
	ev, ok := parseZurgLine(line, files, now)
	if !ok {
		return
	}
	t.events = append(pruneEvents(t.events, now), ev)
	if ev.Kind == kindTransfer {
		for _, se := range t.sess {
			if path.Base(se.s.File) == ev.File || (se.src != nil && path.Base(se.src.Target) == ev.File) {
				se.servedBy = ev.Account
			}
		}
	}
}

func pruneEvents(evs []zurgEvent, now time.Time) []zurgEvent {
	i := 0
	for i < len(evs) && now.Sub(evs[i].At) > eventTTL {
		i++
	}
	return evs[i:]
}

// poll reads the sessions and everything the verdicts need.
func (t *Tracker) poll(ctx context.Context) {
	ss, err := t.o.Plex.Sessions(ctx)
	if ctx.Err() != nil {
		return
	}
	now := t.o.Now()
	if err != nil {
		t.mu.Lock()
		if t.err != err.Error() {
			t.o.Logger.Warn("nowplaying: plex sessions", "err", err)
		}
		t.err, t.updatedAt = err.Error(), now
		t.mu.Unlock()
		return
	}
	// Sources of new playbacks (database and maybe zurg reads), outside the lock.
	// A source without a link is looked up again only every resolveEvery.
	t.mu.Lock()
	type knownSrc struct {
		src *Source
		at  time.Time
	}
	known := map[string]knownSrc{}
	for k, se := range t.sess {
		known[k+"\x00"+se.s.File] = knownSrc{se.src, se.resolvedAt}
	}
	t.mu.Unlock()
	srcs := map[string]*Source{}
	resolved := map[string]bool{}
	for _, s := range ss {
		k := s.SessionKey + "\x00" + s.File
		if kn, ok := known[k]; ok && kn.src != nil && (kn.src.LinkID != 0 || now.Sub(kn.at) < resolveEvery) {
			srcs[k] = kn.src
		} else if s.File != "" {
			srcs[k], resolved[k] = t.resolve(ctx, s.File), true
		}
	}
	var stats *library.ZurgStats
	var transfers []Transfer
	if len(ss) > 0 && t.o.Zurg != nil {
		if stats, err = t.o.Zurg.Stats(ctx); err != nil {
			t.o.Logger.Debug("nowplaying: zurg /stats", "err", err)
		}
	}
	if len(ss) > 0 && t.o.RC != nil {
		if transfers, err = t.o.RC.Transfers(ctx); err != nil {
			t.o.Logger.Debug("nowplaying: rclone rc core/stats", "err", err)
		}
	}
	t.mu.Lock()
	cacheDue := t.o.RC != nil && now.Sub(t.cacheAt) >= cacheEvery
	t.mu.Unlock()
	var cache *Cache
	if cacheDue {
		if cache, err = t.o.RC.Cache(ctx); err != nil {
			t.o.Logger.Debug("nowplaying: rclone rc vfs/stats", "err", err)
		}
	}

	t.mu.Lock()
	if cacheDue {
		t.cache, t.cacheAt = cache, now
	}
	t.stats, t.err, t.updatedAt = stats, "", now
	t.events = pruneEvents(t.events, now)
	seen := map[string]bool{}
	for _, s := range ss {
		seen[s.SessionKey] = true
		se := t.sess[s.SessionKey]
		if se == nil || se.s.RatingKey != s.RatingKey || se.s.File != s.File {
			se = &session{s: s}
			t.sess[s.SessionKey] = se
			if s.State == "buffering" {
				se.buffering = []time.Time{now}
			}
		} else {
			t.observe(se, s.State, now)
			t.sample(se, s, now)
		}
		se.s = s
		se.src = srcs[s.SessionKey+"\x00"+s.File]
		if resolved[s.SessionKey+"\x00"+s.File] {
			se.resolvedAt = now
		}
		se.fetch = matchFetch(transfers, se)
		cut := 0
		for cut < len(se.buffering) && now.Sub(se.buffering[cut]) > bufferingWindow {
			cut++
		}
		se.buffering = se.buffering[cut:]
	}
	for k := range t.sess {
		if !seen[k] {
			delete(t.sess, k)
		}
	}
	for k := range t.warmed {
		if key, _, _ := strings.Cut(k, "\x00"); !seen[key] {
			delete(t.warmed, k)
		}
	}
	for k := range t.cached {
		if key, _, _ := strings.Cut(k, "\x00"); !seen[key] {
			delete(t.cached, k)
		}
	}
	var struggling []*session
	for _, se := range t.sess {
		se.health = Health{Verdict: verdictOf(se, now)}
		if se.health.Verdict == "buffering" || se.health.Verdict == "slow" {
			struggling = append(struggling, se)
		}
	}
	t.mu.Unlock()

	if len(struggling) > 0 {
		var mountErr error
		if t.o.Mount != nil {
			if m := t.o.Mount(); m != nil {
				mountErr = m.MountHealthy(ctx)
			}
		}
		t.mu.Lock()
		for _, se := range struggling {
			se.health.Cause, se.health.Detail = t.cause(se, mountErr)
		}
		t.mu.Unlock()
	}
	// Diagnose foreground playback before admitting any new background read.
	t.mu.Lock()
	warm := t.nextWarm()
	full := t.nextCache(ctx)
	t.mu.Unlock()
	if full != nil {
		go t.cacheNext(full.ctx, full.job)
	}
	if warm != nil {
		go t.prewarm(ctx, warm)
	}
}

// sample measures progress between two polls while playing.
func (t *Tracker) sample(se *session, s plex.Session, now time.Time) {
	if s.State != "playing" || se.s.State != "playing" {
		se.sampleAt = time.Time{}
	}
	if s.State != "playing" {
		return
	}
	if se.sampleAt.IsZero() {
		se.sampleOff, se.sampleAt = s.ViewOffset, now
		return
	}
	dt := now.Sub(se.sampleAt)
	if dt < minSample {
		return
	}
	moved := time.Duration(s.ViewOffset-se.sampleOff) * time.Millisecond
	if moved >= 0 && float64(moved) <= seekRatio*float64(dt) {
		se.slow = float64(moved) < slowRatio*float64(dt)
	}
	se.sampleOff, se.sampleAt = s.ViewOffset, now
}

func verdictOf(se *session, now time.Time) string {
	switch {
	case se.s.State == "paused":
		return "paused"
	case se.s.State == "buffering":
		return "buffering"
	case len(se.buffering) > 0 && now.Sub(se.buffering[len(se.buffering)-1]) < recentBuffering:
		return "buffering"
	case se.slow:
		return "slow"
	}
	return "ok"
}

// cause is the likeliest reason a playback struggles, in order: Plex's transcoder, the provider (zurg names the
// file), the mount (unhealthy, zurg not ready or the cache erroring), the provider again (the mount fetches
// the file slower than its bitrate), or a relayed network connection. Without evidence the cause is unknown.
func (t *Tracker) cause(se *session, mountErr error) (string, string) {
	if tc := se.s.Transcode; tc != nil && tc.Speed > 0 && tc.Speed < 1 && !tc.Throttled {
		return "transcode", fmt.Sprintf("Plex transcodes at %.1fx, slower than playback", tc.Speed)
	}
	base, target := path.Base(se.s.File), base0(se.src)
	if t.stats != nil {
		for _, b := range t.stats.BlockedReads {
			f := path.Base(b.Filename)
			if b.Filename == "" {
				f = path.Base(b.Path)
			}
			if f == base || f == target || (se.src != nil && se.src.Hash != "" && b.Hash == se.src.Hash) {
				why := b.Reason
				if why == "" {
					why = b.Status
				}
				return "provider", "zurg refuses reads of the file: " + why
			}
		}
	}
	for _, f := range []string{base, target} {
		if s := issue(t.events, f, se.servedBy); s != "" {
			return "provider", s
		}
	}
	// A definite mount problem (the mount or zurg not answering, the cache erroring) before a slow fetch,
	// which such a problem also causes.
	if mountErr != nil {
		return "mount", mountErr.Error()
	}
	// A full cache alone is normal: rclone evicts old files as needed.
	if c := t.cache; c != nil && c.ErroredFiles > 0 {
		return "mount", fmt.Sprintf("the rclone cache has %d errored files", c.ErroredFiles)
	}
	if se.fetch != nil && se.fetch.Ratio > 0 && se.fetch.Ratio < 1 {
		return "provider", fmt.Sprintf("the mount fetches the file at %.1fx its bitrate", se.fetch.Ratio)
	}
	switch {
	case se.s.Relayed:
		return "network", "remote client relayed through Plex's servers (bandwidth capped)"
	case se.s.Location == "wan" || !se.s.Local:
		return "unknown", "no diagnostic evidence identifies the cause; check the remote connection or the upload"
	}
	return "unknown", "no diagnostic evidence identifies the cause; check the local player or home network"
}

func base0(src *Source) string {
	if src == nil {
		return ""
	}
	return path.Base(src.Target)
}

// matchFetch finds the playing file among the mount's transfers (rclone names them by their path in the remote).
func matchFetch(trs []Transfer, se *session) *Fetch {
	for _, tr := range trs {
		ok := path.Base(tr.Name) == path.Base(se.s.File)
		if se.src != nil && se.src.rel != "" {
			ok = ok || strings.HasSuffix(tr.Name, "/"+se.src.rel) || tr.Name == se.src.rel
		}
		if !ok {
			continue
		}
		f := &Fetch{Speed: tr.Speed, Bytes: tr.Bytes, Size: tr.Size}
		if f.Speed == 0 {
			f.Speed = tr.SpeedAvg
		}
		if se.s.Bitrate > 0 {
			f.Ratio = f.Speed * 8 / 1000 / float64(se.s.Bitrate)
		}
		return f
	}
	return nil
}

// Snapshot is the current state. Calling it marks the page as watched (polling speeds up); a stale state is read
// again at once.
func (t *Tracker) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.o.Now()
	if t.viewedAt.IsZero() || now.Sub(t.viewedAt) >= viewedWithin {
		t.poke()
	}
	t.viewedAt = now
	out := Snapshot{Enabled: true, Error: t.err, Sessions: []SessionView{}, Cache: t.cache}
	if !t.updatedAt.IsZero() {
		at := t.updatedAt
		out.UpdatedAt = &at
	}
	if t.stats != nil {
		n := t.stats.ActiveStreams
		out.ZurgStreams = &n
	}
	for _, se := range t.sess {
		s := se.s
		v := SessionView{Key: s.SessionKey, UserID: s.UserID, User: s.UserName, Player: s.Player, Product: s.Product,
			Platform: s.Platform, State: s.State, Type: s.Type, Title: s.Title, ViewOffset: s.ViewOffset,
			Duration: s.Duration, Local: s.Local, Relayed: s.Relayed, Location: s.Location, Bitrate: s.Bitrate,
			Resolution: s.Resolution, Decision: s.Decision, Transcode: s.Transcode, Buffering: len(se.buffering),
			Health: se.health, Fetch: se.fetch}
		if s.Type == "episode" {
			v.Show, v.Season, v.Episode = s.GrandparentTitle, s.ParentIndex, s.Index
		}
		if se.src != nil {
			src := *se.src
			src.ServedBy = se.servedBy
			v.Source = &src
		}
		if se.prewarm != nil {
			p := *se.prewarm
			v.Prewarm = &p
		}
		if j := t.caching; j != nil && j.state != nil && waitsOn(se, j.nf) {
			p := *j.state
			v.Prewarm = &p
		}
		out.Sessions = append(out.Sessions, v)
	}
	sort.Slice(out.Sessions, func(i, j int) bool { return out.Sessions[i].Key < out.Sessions[j].Key })
	return out
}

// warmJob is one pre-warm to run.
type warmJob struct {
	key       string // session key
	linkID    int64
	titleID   int64
	title     string
	versionID int64
	version   string
	episodes  []int64
}

// nextWarm picks a playback due for its pre-warm (t.mu held): an episode linked by Theatarr, past prewarmAfter,
// not warmed yet for this session and link. One runs at a time.
func (t *Tracker) nextWarm() *warmJob {
	if !t.o.Prewarm || t.warming || t.o.Mount == nil || t.prewarmBlocked() {
		return nil
	}
	keys := make([]string, 0, len(t.sess))
	for k := range t.sess {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		se := t.sess[k]
		src := se.src
		if se.s.Type != "episode" || src == nil || src.LinkID == 0 || len(src.versionIDs) == 0 ||
			len(src.episodes) == 0 || time.Duration(se.s.ViewOffset)*time.Millisecond < prewarmAfter {
			continue
		}
		wk := fmt.Sprintf("%s\x00%d", k, src.LinkID)
		if t.warmed[wk] {
			continue
		}
		t.warmed[wk], t.warming = true, true
		return &warmJob{key: k, linkID: src.LinkID, titleID: src.TitleID, title: src.Title,
			versionID: src.versionIDs[0], version: src.Versions[0], episodes: src.episodes}
	}
	return nil
}

func (t *Tracker) setPrewarm(key string, linkID int64, p PrewarmState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if se := t.sess[key]; se != nil && se.src != nil && se.src.LinkID == linkID {
		se.prewarm = &p
	}
}

// prewarm warms the next episode of the job's playback: finds it in the version's order, checks its link, the
// registry and zurg's entry, then reads the head of the file through the mount. A missing next episode (aired and
// monitored but not linked, gone from the providers, without a zurg entry, off the mount) or an unreadable file
// raises an error-inbox item (one per version and episode). It never calls a provider.
func (t *Tracker) prewarm(ctx context.Context, j *warmJob) {
	defer func() {
		t.mu.Lock()
		t.warming = false
		t.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, prewarmLimit)
	defer cancel()
	set := func(ep, state, detail string) {
		t.setPrewarm(j.key, j.linkID, PrewarmState{Episode: ep, State: state, Detail: detail, At: t.o.Now()})
	}
	next, err := t.nextEpisode(ctx, j.titleID, j.versionID, j.episodes, store.Millis(t.o.Now()))
	if err != nil {
		if !errors.Is(err, errNoNext) {
			t.o.Logger.Debug("nowplaying: next episode", "err", err)
		}
		set("", "skipped", "no next episode (last one, not aired or not monitored)")
		return
	}
	ep := fmt.Sprintf("S%02dE%02d", next.Season, next.Number)
	set(ep, "running", "")
	var linkID int64
	var target, kind string
	var contentFile sql.NullInt64
	fail := func(msg string) {
		set(ep, "failed", msg)
		ctx := map[string]string{"title": j.title, "version": j.version, "episode": ep}
		if linkID != 0 {
			ctx["link_id"] = fmt.Sprint(linkID)
		}
		t.o.Report(errinbox.Event{Source: errinbox.SourceJob, Component: "nowplaying.prewarm",
			Message: "next episode " + msg, Key: fmt.Sprintf("%d:%d", j.versionID, next.ID), Time: t.o.Now(),
			Context: ctx})
	}
	err = t.o.Store.Reader().QueryRowContext(ctx, `SELECT l.id, l.target, l.kind, l.content_file_id
		FROM link l JOIN link_episode le ON le.link_id = l.id JOIN link_version lv ON lv.link_id = l.id
		WHERE le.episode_id = ? AND lv.version_id = ? AND l.state = 'active' ORDER BY l.id DESC LIMIT 1`,
		next.ID, j.versionID).Scan(&linkID, &target, &kind, &contentFile)
	if errors.Is(err, sql.ErrNoRows) {
		// Aired and monitored (nextEpisode), yet not linked: the playback will stop at the end of this one.
		fail("is missing: aired and monitored, but not in the library")
		return
	}
	if err != nil {
		t.o.Logger.Debug("nowplaying: next episode link", "err", err)
		set(ep, "skipped", "could not read its link")
		return
	}
	if kind != "symlink" {
		set(ep, "skipped", "a local file, nothing to warm")
		return
	}
	// The registry's content, found as source mapping finds it: the link's content file, else the release folder,
	// else the hash of zurg's entry.
	rel := mountRel(target, t.o.AllDir)
	folder, file, _ := strings.Cut(rel, "/")
	var entry *library.ZurgEntry
	if rel != "" && t.o.Zurg != nil {
		e, err := t.o.Zurg.Entry(ctx, folder, file)
		if err == nil && e == nil {
			fail("is missing: zurg has no entry for its file")
			return
		}
		entry = e
	}
	db := t.o.Store.Reader()
	var contentID int64
	if contentFile.Valid {
		_ = db.QueryRowContext(ctx, `SELECT content_id FROM content_file WHERE id = ?`, contentFile.Int64).Scan(&contentID)
	}
	if contentID == 0 && folder != "" {
		_ = db.QueryRowContext(ctx, `SELECT id FROM content WHERE mount_folder = ? ORDER BY id LIMIT 1`, folder).Scan(&contentID)
	}
	if contentID == 0 && entry != nil && entry.Hash != "" {
		_ = db.QueryRowContext(ctx, `SELECT id FROM content WHERE info_hash = ?`, strings.ToLower(entry.Hash)).Scan(&contentID)
	}
	var state string
	if contentID != 0 {
		_ = db.QueryRowContext(ctx, `SELECT state FROM content WHERE id = ?`, contentID).Scan(&state)
	}
	if state == "gone" || state == "absent" {
		fail("is gone from every provider per the registry")
		return
	}
	m := t.o.Mount()
	if m == nil {
		set(ep, "skipped", "no library")
		return
	}
	if err := m.Prewarm(ctx, target, prewarmBytes); err != nil {
		if errors.Is(err, library.ErrNotOnMount) {
			fail("is not on the mount: its link points elsewhere")
			return
		}
		if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return
		}
		fail("is unreadable: " + err.Error())
		return
	}
	set(ep, "ok", "")
	t.mu.Lock()
	if se := t.sess[j.key]; se != nil && se.src != nil && se.src.LinkID == j.linkID {
		se.next = &nextFile{linkID: j.linkID, target: target, ep: ep, episodeID: next.ID, titleID: j.titleID,
			title: j.title, versionID: j.versionID, version: j.version}
	}
	t.mu.Unlock()
}

// report raises the pre-warm's error-inbox item for a next episode (one per version and episode).
func (t *Tracker) report(nf *nextFile, msg string, linkID int64) {
	c := map[string]string{"title": nf.title, "version": nf.version, "episode": nf.ep}
	if linkID != 0 {
		c["link_id"] = fmt.Sprint(linkID)
	}
	t.o.Report(errinbox.Event{Source: errinbox.SourceJob, Component: "nowplaying.prewarm",
		Message: "next episode " + msg, Key: fmt.Sprintf("%d:%d", nf.versionID, nf.episodeID), Time: t.o.Now(),
		Context: c})
}

type cacheStart struct {
	ctx context.Context
	job *cacheJob
}

// prewarmBlocked defers new reads only for buffering attributed to the debrid side (t.mu held).
func (t *Tracker) prewarmBlocked() bool {
	for _, se := range t.sess {
		if se.health.Verdict == "buffering" && (se.health.Cause == "provider" || se.health.Cause == "mount") {
			return true
		}
	}
	return false
}

func cacheApplies(se *session, nf *nextFile) bool {
	return se.s.Type == "episode" && se.src != nil && se.src.TitleID == nf.titleID &&
		slices.Contains(se.src.versionIDs, nf.versionID)
}

// waitsOn reports whether the session's next episode is the file a caching read fetches: only those sessions show
// its progress (another viewer of the version, on another episode, does not).
func waitsOn(se *session, nf *nextFile) bool {
	return cacheApplies(se, nf) && se.next != nil && se.next.target == nf.target
}

// nextCache picks a playback due for its caching read (t.mu held): its next episode passed the head read, it is
// past half of its episode, and no caching read runs (one at a time, server-wide).
func (t *Tracker) nextCache(ctx context.Context) *cacheStart {
	if !t.o.Prewarm || t.caching != nil || t.o.Mount == nil || t.prewarmBlocked() {
		return nil
	}
	keys := make([]string, 0, len(t.sess))
	for k := range t.sess {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		se := t.sess[k]
		nf := se.next
		if nf == nil || se.src == nil || se.src.LinkID != nf.linkID || se.s.Duration <= 0 ||
			se.s.ViewOffset*2 <= se.s.Duration {
			continue
		}
		wk := fmt.Sprintf("%s\x00%d", k, nf.linkID)
		if t.cached[wk] {
			continue
		}
		t.cached[wk] = true
		cctx, cancel := context.WithCancel(ctx)
		t.caching = &cacheJob{cancel: cancel, nf: nf}
		return &cacheStart{ctx: cctx, job: t.caching}
	}
	return nil
}

// cacheNext reads the next episode's whole file through the mount at the rate cap, so rclone's VFS cache keeps it
// and it plays from local disk. Once started it runs to the end, whatever the sessions do (only Theatarr stopping
// ends it early). A file over the size cap is left alone; a read error raises the inbox item and stops. It never
// calls a provider and deletes nothing from the cache (rclone evicts on its own).
func (t *Tracker) cacheNext(ctx context.Context, j *cacheJob) {
	nf := j.nf
	defer func() {
		j.cancel()
		t.mu.Lock()
		if t.caching == j {
			t.caching = nil
		}
		t.mu.Unlock()
	}()
	set := func(state, detail string, progress float64) {
		t.mu.Lock()
		defer t.mu.Unlock()
		j.state = &PrewarmState{Episode: nf.ep, State: state, Detail: detail, Progress: progress, At: t.o.Now()}
		for key, se := range t.sess {
			if waitsOn(se, nf) {
				se.prewarm = j.state
				// This read serves all viewers waiting on this file, including a viewer who joined later.
				t.cached[fmt.Sprintf("%s\x00%d", key, se.next.linkID)] = true
			}
		}
	}
	m := t.o.Mount()
	if m == nil {
		return
	}
	size, err := m.MountFileSize(ctx, nf.target)
	if err != nil {
		if ctx.Err() == nil {
			set("failed", "is unreadable: "+err.Error(), 0)
			t.report(nf, "is unreadable: "+err.Error(), 0)
		}
		return
	}
	if size > t.o.PrewarmMaxSize {
		set("ok", fmt.Sprintf("head read; not cached: %s is over prewarm_max_size", gb(size)), 0)
		return
	}
	log := t.o.Logger.With("title", nf.title, "episode", nf.ep, "target", nf.target)
	log.Info("nowplaying: caching the next episode", "size", size, "rate", t.o.PrewarmRate)
	start := time.Now()
	set("caching", "caching 0%", 0)
	n, err := m.CacheFile(ctx, nf.target, t.o.PrewarmRate, func(done, total int64) {
		if total > 0 {
			f := float64(done) / float64(total)
			set("caching", fmt.Sprintf("caching %d%%", int(f*100)), f)
		}
	})
	took := time.Since(start).Round(time.Second)
	switch {
	case err != nil && ctx.Err() != nil:
		log.Info("nowplaying: caching stopped", "bytes", n, "duration", took.String())
		set("ok", "head read; caching stopped: Theatarr is stopping", 0)
	case err != nil:
		pct := 0
		if size > 0 {
			pct = int(float64(n) / float64(size) * 100)
		}
		log.Warn("nowplaying: caching failed", "bytes", n, "duration", took.String(), "err", err)
		msg := fmt.Sprintf("is unreadable at %d%%: %v", pct, err)
		set("failed", msg, 0)
		t.report(nf, msg, 0)
	default:
		log.Info("nowplaying: cached the next episode", "bytes", n, "duration", took.String())
		set("cached", fmt.Sprintf("cached %s in %s", gb(n), took), 1)
	}
}

func gb(n int64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }
