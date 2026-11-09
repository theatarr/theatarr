package policy

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// Library configures the library (docs/design/library.md): imports of ready downloads into the versions'
// library roots, relinks, verification and Plex scans.
//
// Writes are gated three ways, and all three must allow one:
//   - Enabled must be true. It defaults to false, which blocks every filesystem write and every Plex call;
//   - the version must be mode: active (a shadow version only records what it would do);
//   - the path must be under one of the version's Roots here (or under Trash.Dir for a link moved aside).
type Library struct {
	// Enabled turns on filesystem writes and Plex calls. Default false.
	Enabled bool `yaml:"enabled"`
	// Mount is where zurg is read.
	Mount LibraryMount `yaml:"mount"`
	// Probe bounds the header probe.
	Probe LibraryProbe `yaml:"probe"`
	// Sample tunes the sample and fake guard.
	Sample LibrarySample `yaml:"sample"`
	// Trash is where replaced links are moved aside (upgrades, relinks). Required once Write library links is on.
	Trash LibraryTrash `yaml:"trash"`
	// Plex is the Plex server scanned after a change. Optional.
	Plex *Plex `yaml:"plex"`
	// Versions lists, per version key, where the library may write and which Plex section shows it. A version not
	// listed here never gets a write.
	Versions map[string]*LibraryVersion `yaml:"versions"`
	// RelinkInterval is how often theatarr serve looks for dangling links and repoints them. Default 1h, minimum 10m.
	RelinkInterval Duration `yaml:"relink_interval"`
	// Dangling configures the read-only report of dangling links into the mount.
	Dangling LibraryDangling `yaml:"dangling"`
}

// LibraryDangling is the dangling-link report: a read-only walk of the version roots, plus Roots, recorded in the
// database (theatarr library dangling prints it). It never writes to the filesystem, whatever the gate says.
type LibraryDangling struct {
	// Interval is the time between reports in theatarr serve (one at start). Default 6h, minimum 10m.
	Interval Duration `yaml:"interval"`
	// Roots lists folders walked besides the version roots, read-only: the libraries the arrs still own and decypharr's
	// downloads (for example /media and /downloads).
	Roots []string `yaml:"roots"`
}

// LibraryMount is the zurg mount.
type LibraryMount struct {
	// Path is where this service sees the mount, e.g. /mnt/debrid. Required once Write library links is on.
	Path string `yaml:"path"`
	// LinkPath is the mount path written into symlinks: where Plex sees it. Default Path.
	LinkPath string `yaml:"link_path"`
	// AllDir is zurg's directory holding every release. Default __all__.
	AllDir string `yaml:"all_dir"`
	// WaitTimeout is how long an import waits for the release to appear before it alerts (it keeps waiting; no
	// re-grab). Default 6m.
	WaitTimeout Duration `yaml:"wait_timeout"`
	// PollInterval is how often a waiting import looks again. Default 20s, minimum 5s.
	PollInterval Duration `yaml:"poll_interval"`
	// StatTimeout bounds the health sentinel's stat; a hang or ENOTCONN pauses imports and sweeps. Default 5s.
	StatTimeout Duration `yaml:"stat_timeout"`
	// ZurgLog is zurg's log (read-only): an absolute file path, or the http(s) URL of zurg's /logs endpoint when
	// the file is on another host. When set, theatarr serve follows it and relinks as soon as zurg logs a folder rename
	// the hourly sweep still catches what it misses.
	ZurgLog string `yaml:"zurg_log"`
	// ZurgLogInterval is how often a URL zurg_log is fetched (each fetch returns the whole current log). Default
	// 15s, 5s to 10m. A file is followed every 2s whatever this says.
	ZurgLogInterval Duration `yaml:"zurg_log_interval"`
	// ZurgLogBudget names a budget for the fetches of a URL zurg_log. Optional.
	ZurgLogBudget string `yaml:"zurg_log_budget"`
	// ZurgAPI is zurg's base URL (e.g. http://zurg:9999), read-only: GET /debug/torrents, /api/library/entry
	// and /torrents/repair/status only. When set, the dangling report, verify and the relink sweep name the links whose
	// torrent has no video upstream, imports and relinks confirm the hash of the file they link, and dangling links are
	// not marked dead while zurg repairs (internal/library/zurgapi.go). Off when unset.
	ZurgAPI string `yaml:"zurg_api"`
	// ZurgAPIInterval is the longest an answer of zurg_api is reused: the torrent listing and the repair state are
	// fetched at most once per interval, when a pass needs them. Default 1m, 10s to 1h.
	ZurgAPIInterval Duration `yaml:"zurg_api_interval"`
	// ZurgAPIBudget names a budget for zurg_api requests. Optional.
	ZurgAPIBudget string `yaml:"zurg_api_budget"`
	// ZurgAPIRepairGrace bounds how long a running repair keeps dangling links from being marked dead: past it the
	// sweep marks them as it would without the API. Default 2h, 10m to 24h.
	ZurgAPIRepairGrace Duration `yaml:"zurg_api_repair_grace"`
	// RcloneRC is the mount's rclone remote control (rclone rc): the now playing page reads core/stats and vfs/stats
	// (docs/design/now-playing.md); forget_release_root lets an import clear one cached listing. Optional.
	RcloneRC *RcloneRC `yaml:"rclone_rc"`
}

// RcloneRC is an rclone remote control endpoint.
type RcloneRC struct {
	// URL is the rc base URL, e.g. http://rclone:5572.
	URL string `yaml:"url"`
	// UsernameEnv and PasswordEnv name the basic auth credentials (--rc-user, --rc-pass). Optional.
	UsernameEnv string `yaml:"username_env"`
	// PasswordEnv names the environment variable holding the rclone remote control password.
	PasswordEnv string `yaml:"password_env"`
	// Budget names a budget for rc calls. Optional.
	Budget string `yaml:"budget"`
	// ForgetReleaseRoot clears rclone's cache of the release root (vfs/forget; its subfolders and parent too) when
	// zurg_api lists a torrent the mount does not show yet, at most every 3s. Default false.
	ForgetReleaseRoot bool `yaml:"forget_release_root"`
}

// ZurgLogIsURL reports whether a zurg_log value is an http(s) URL rather than a file path.
func ZurgLogIsURL(v string) bool {
	return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")
}

// LibraryProbe bounds the header-only probe.
type LibraryProbe struct {
	// MaxBytes limits how many bytes are read per file. Default 16 MiB, 1 to 256 MiB.
	MaxBytes int64 `yaml:"max_bytes"`
}

// LibrarySample tunes the sample and fake guard (docs/design/library.md). The arrs' runtime rule always applies.
type LibrarySample struct {
	// MinRuntimeRatio rejects a file shorter than this share of the title's (or episode's) known runtime. Default
	// 0.5; 0 < x <= 1.
	MinRuntimeRatio float64 `yaml:"min_runtime_ratio"`
	// Resolution rejects a file whose probed resolution is two or more steps under the release's (a 2160p release
	// holding a 720p file). Default true.
	Resolution *bool `yaml:"resolution"`
	// Mislabel refuses a file whose probe plainly contradicts its Remux or 2160p label (an x264/x265 CRF encode named
	// Remux, 1080-line video named 2160p). Default true.
	Mislabel *bool `yaml:"mislabel"`
}

// LibraryTrash is where replaced links go.
type LibraryTrash struct {
	// Dir is absolute, outside every library root (Plex must not scan it) and outside the mount.
	Dir string `yaml:"dir"`
	// KeepDays is the number of days before a moved-aside link is removed for good (the link only; debrid content is never touched).
	// Default 30.
	KeepDays int `yaml:"keep_days"`
}

// Plex is the Plex Media Server the library scans.
type Plex struct {
	// URL is the Plex server endpoint.
	URL string `yaml:"url"`
	// TokenEnv names the environment variable holding the Plex token.
	TokenEnv string `yaml:"token_env"`
	// Budget names a budget for Plex calls. Optional.
	Budget string `yaml:"budget"`
	// Debounce coalesces scans of one folder. Default 30s.
	Debounce Duration `yaml:"debounce"`
	// NowPlaying follows Plex's sessions for the now playing page (docs/design/now-playing.md). It only reads, so it
	// does not need library.enabled. Default true.
	NowPlaying *bool `yaml:"now_playing"`
	// Prewarm reads the head of the next episode of a show being watched through the mount, about a minute into an
	// episode, and raises an error inbox item when that episode is missing or unreadable; past half of the episode
	// it reads the next one whole so the mount's cache keeps it. Default true.
	Prewarm *bool `yaml:"prewarm"`
	// PrewarmRate caps the pre-warm's whole-file read of the next episode, in bytes per second (25MB: 25 MB/s), so
	// it never starves a live stream. Default 25MB.
	PrewarmRate ByteSize `yaml:"prewarm_rate"`
	// PrewarmMaxSize limits whole-file preloading; larger episodes get only a header check. Default 20GB.
	PrewarmMaxSize ByteSize `yaml:"prewarm_max_size"`
}

// LibraryVersion is one version's library settings.
type LibraryVersion struct {
	// Roots lists the only directories this version may write under. versions[key].library must be one of them or
	// under one.
	Roots []string `yaml:"roots"`
	// PlexSection is the Plex library section id that shows this version's root. 0: no scans.
	PlexSection int `yaml:"plex_section"`
}

// Library defaults and limits.
const (
	// DefaultPrewarmRate and DefaultPrewarmMaxSize: library.plex.prewarm_rate (bytes/s) and prewarm_max_size.
	DefaultPrewarmRate    = 25e6
	DefaultPrewarmMaxSize = 20e9
	// MinPrewarmRate: a lower cap would make the read outlast the episode.
	MinPrewarmRate               = 1e6
	DefaultLibraryAllDir         = "__all__"
	DefaultLibraryWaitTimeout    = 6 * time.Minute
	DefaultLibraryPollInterval   = 20 * time.Second
	MinLibraryPollInterval       = 5 * time.Second
	DefaultLibraryStatTimeout    = 5 * time.Second
	DefaultLibraryProbeBytes     = 16 << 20
	MaxLibraryProbeBytes         = 256 << 20
	DefaultLibraryRuntimeRatio   = 0.5
	DefaultLibraryTrashKeepDays  = 30
	DefaultPlexDebounce          = 30 * time.Second
	DefaultLibraryRelinkInterval = time.Hour
	MinLibraryRelinkInterval     = 10 * time.Minute
	DefaultDanglingInterval      = 6 * time.Hour
	DefaultZurgLogInterval       = 15 * time.Second
	MinZurgLogInterval           = 5 * time.Second
	MaxZurgLogInterval           = 10 * time.Minute
	DefaultZurgAPIInterval       = time.Minute
	MinZurgAPIInterval           = 10 * time.Second
	MaxZurgAPIInterval           = time.Hour
	DefaultZurgRepairGrace       = 2 * time.Hour
	MinZurgRepairGrace           = 10 * time.Minute
	MaxZurgRepairGrace           = 24 * time.Hour
)

// LibrarySettings is the library section with every default applied.
type LibrarySettings struct {
	Enabled         bool
	MountPath       string
	LinkPath        string
	AllDir          string
	WaitTimeout     time.Duration
	PollInterval    time.Duration
	StatTimeout     time.Duration
	ProbeMaxBytes   int64
	MinRuntimeRatio float64
	Resolution      bool
	Mislabel        bool
	TrashDir        string
	TrashKeepDays   int
	Plex            *Plex
	PlexDebounce    time.Duration
	RelinkInterval  time.Duration
	Versions        map[string]*LibraryVersion
	// ZurgLog is a cleaned file path, or a URL as given (ZurgLogURL says which).
	ZurgLog         string
	ZurgLogURL      bool
	ZurgLogInterval time.Duration
	// ZurgAPI is zurg's base URL as given ("" when off).
	ZurgAPI            string
	ZurgAPIInterval    time.Duration
	ZurgAPIRepairGrace time.Duration
	// RcloneRC is the mount's rc, nil when off.
	RcloneRC *RcloneRC
	// NowPlaying and Prewarm: library.plex is set and its switch is not off.
	NowPlaying bool
	Prewarm    bool
	// PrewarmRate (bytes/s) and PrewarmMaxSize (bytes) bound the pre-warm's whole-file read.
	PrewarmRate    int64
	PrewarmMaxSize int64
	DanglingEvery  time.Duration
	DanglingRoots  []string
}

// LibrarySettings returns the library section with defaults; a policy without one gets a disabled library.
func (p *Policy) LibrarySettings() LibrarySettings {
	s := LibrarySettings{AllDir: DefaultLibraryAllDir, WaitTimeout: DefaultLibraryWaitTimeout,
		PollInterval: DefaultLibraryPollInterval, StatTimeout: DefaultLibraryStatTimeout,
		ProbeMaxBytes: DefaultLibraryProbeBytes, MinRuntimeRatio: DefaultLibraryRuntimeRatio, Resolution: true,
		Mislabel:      true,
		TrashKeepDays: DefaultLibraryTrashKeepDays, PlexDebounce: DefaultPlexDebounce,
		RelinkInterval: DefaultLibraryRelinkInterval, Versions: map[string]*LibraryVersion{},
		DanglingEvery: DefaultDanglingInterval, ZurgLogInterval: DefaultZurgLogInterval,
		ZurgAPIInterval: DefaultZurgAPIInterval, ZurgAPIRepairGrace: DefaultZurgRepairGrace,
		PrewarmRate: DefaultPrewarmRate, PrewarmMaxSize: DefaultPrewarmMaxSize}
	l := p.Library
	if l == nil {
		return s
	}
	s.Enabled = l.Enabled
	s.MountPath = cleanAbs(l.Mount.Path)
	s.LinkPath = s.MountPath
	if l.Mount.LinkPath != "" {
		s.LinkPath = cleanAbs(l.Mount.LinkPath)
	}
	if l.Mount.AllDir != "" {
		s.AllDir = l.Mount.AllDir
	}
	if l.Mount.WaitTimeout != 0 {
		s.WaitTimeout = l.Mount.WaitTimeout.D()
	}
	if l.Mount.PollInterval != 0 {
		s.PollInterval = l.Mount.PollInterval.D()
	}
	if l.Mount.StatTimeout != 0 {
		s.StatTimeout = l.Mount.StatTimeout.D()
	}
	if l.Probe.MaxBytes != 0 {
		s.ProbeMaxBytes = l.Probe.MaxBytes
	}
	if l.Sample.MinRuntimeRatio != 0 {
		s.MinRuntimeRatio = l.Sample.MinRuntimeRatio
	}
	if l.Sample.Resolution != nil {
		s.Resolution = *l.Sample.Resolution
	}
	if l.Sample.Mislabel != nil {
		s.Mislabel = *l.Sample.Mislabel
	}
	s.TrashDir = cleanAbs(l.Trash.Dir)
	if l.Trash.KeepDays != 0 {
		s.TrashKeepDays = l.Trash.KeepDays
	}
	s.Plex = l.Plex
	if l.Plex != nil && l.Plex.Debounce != 0 {
		s.PlexDebounce = l.Plex.Debounce.D()
	}
	if l.Plex != nil {
		s.NowPlaying = l.Plex.NowPlaying == nil || *l.Plex.NowPlaying
		s.Prewarm = s.NowPlaying && (l.Plex.Prewarm == nil || *l.Plex.Prewarm)
		if l.Plex.PrewarmRate != 0 {
			s.PrewarmRate = int64(l.Plex.PrewarmRate)
		}
		if l.Plex.PrewarmMaxSize != 0 {
			s.PrewarmMaxSize = int64(l.Plex.PrewarmMaxSize)
		}
	}
	s.RcloneRC = l.Mount.RcloneRC
	if l.RelinkInterval != 0 {
		s.RelinkInterval = l.RelinkInterval.D()
	}
	if ZurgLogIsURL(l.Mount.ZurgLog) {
		s.ZurgLog, s.ZurgLogURL = l.Mount.ZurgLog, true
	} else {
		s.ZurgLog = cleanAbs(l.Mount.ZurgLog)
	}
	if l.Mount.ZurgLogInterval != 0 {
		s.ZurgLogInterval = l.Mount.ZurgLogInterval.D()
	}
	s.ZurgAPI = l.Mount.ZurgAPI
	if l.Mount.ZurgAPIInterval != 0 {
		s.ZurgAPIInterval = l.Mount.ZurgAPIInterval.D()
	}
	if l.Mount.ZurgAPIRepairGrace != 0 {
		s.ZurgAPIRepairGrace = l.Mount.ZurgAPIRepairGrace.D()
	}
	if l.Dangling.Interval != 0 {
		s.DanglingEvery = l.Dangling.Interval.D()
	}
	for _, r := range l.Dangling.Roots {
		s.DanglingRoots = append(s.DanglingRoots, cleanAbs(r))
	}
	for k, v := range l.Versions {
		if v == nil {
			continue
		}
		c := &LibraryVersion{PlexSection: v.PlexSection}
		for _, r := range v.Roots {
			c.Roots = append(c.Roots, cleanAbs(r))
		}
		s.Versions[k] = c
	}
	return s
}

func cleanAbs(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

// Within reports whether path is dir or under it (both cleaned, absolute).
func Within(path, dir string) bool {
	if dir == "" || path == "" {
		return false
	}
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	if path == dir {
		return true
	}
	if dir == "/" {
		return strings.HasPrefix(path, "/")
	}
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

var colonFormats = setOf("delete", "dash", "space_dash", "space_dash_space", "smart")
var multiEpisodeStyles = setOf("extend", "duplicate", "repeat", "scene", "range", "prefixed_range")

func (p *Policy) validateNaming(e *Errors, k string, v *VersionPolicy) {
	path := fmt.Sprintf("versions[%s].naming", k)
	n := v.Naming
	if n.FromVersion != "" {
		p.validateNamingFromVersion(e, path, k, v)
		return
	}
	if n.FromArr != "" {
		var src *ArrSource
		if p.Import != nil {
			src = p.Import.Arr[n.FromArr]
		}
		if src == nil {
			e.add(path+".from_arr", "unknown import.arr instance %q", n.FromArr)
		} else if kind, ok := flavours[src.Flavour]; ok && kind != v.Kind {
			e.add(path+".from_arr", "%s instance %q cannot name a %s version", src.Flavour, n.FromArr, v.Kind)
		}
		if n.Folder != "" || n.File != "" || n.Season != "" || n.Specials != "" || n.Daily != "" || n.Anime != "" ||
			n.Colon != "" || n.MultiEpisode != "" {
			e.add(path, "from_arr takes the whole naming from the instance; set nothing else")
		}
		return
	}
	if n.Colon != "" && !colonFormats[n.Colon] {
		e.add(path+".colon", "must be one of %s, got %q", list(colonFormats), n.Colon)
	}
	if n.MultiEpisode != "" {
		if v.Kind != KindSeries {
			e.add(path+".multi_episode", "only for series versions")
		} else if !multiEpisodeStyles[n.MultiEpisode] {
			e.add(path+".multi_episode", "must be one of %s, got %q", list(multiEpisodeStyles), n.MultiEpisode)
		}
	}
	if v.Kind == KindMovie && (n.Season != "" || n.Specials != "" || n.Daily != "" || n.Anime != "") {
		e.add(path, "season, specials, daily and anime are for series versions")
	}
}

func (p *Policy) validateLibrary(e *Errors) {
	l := p.Library
	if l == nil {
		return
	}
	abs := func(path, v string, required bool) bool {
		if v == "" {
			if required {
				e.add(path, "required")
			}
			return false
		}
		if !filepath.IsAbs(v) {
			e.add(path, "%q must be an absolute path", v)
			return false
		}
		return true
	}
	mountOK := abs("library.mount.path", l.Mount.Path, l.Enabled)
	abs("library.mount.link_path", l.Mount.LinkPath, false)
	if strings.ContainsAny(l.Mount.AllDir, "/\\") {
		e.add("library.mount.all_dir", "a directory name, not a path")
	}
	dur := func(path string, d Duration, lo time.Duration) {
		if d != 0 && d.D() < lo {
			e.add(path, "%v is under the %v minimum", d.D(), lo)
		}
	}
	dur("library.mount.wait_timeout", l.Mount.WaitTimeout, time.Minute)
	dur("library.mount.poll_interval", l.Mount.PollInterval, MinLibraryPollInterval)
	dur("library.mount.stat_timeout", l.Mount.StatTimeout, time.Second)
	dur("library.relink_interval", l.RelinkInterval, MinLibraryRelinkInterval)
	dur("library.dangling.interval", l.Dangling.Interval, MinLibraryRelinkInterval)
	if ZurgLogIsURL(l.Mount.ZurgLog) {
		checkURL(e, "library.mount.zurg_log", l.Mount.ZurgLog)
		checkRef(e, "library.mount.zurg_log_budget", "budget", l.Mount.ZurgLogBudget, p.Budgets, false)
	} else {
		abs("library.mount.zurg_log", l.Mount.ZurgLog, false)
		if l.Mount.ZurgLogBudget != "" {
			e.add("library.mount.zurg_log_budget", "only for a URL zurg_log")
		}
	}
	if d := l.Mount.ZurgLogInterval; d != 0 && (d.D() < MinZurgLogInterval || d.D() > MaxZurgLogInterval) {
		e.add("library.mount.zurg_log_interval", "must be %v to %v, got %v", MinZurgLogInterval, MaxZurgLogInterval, d.D())
	}
	if l.Mount.ZurgAPI != "" {
		checkURL(e, "library.mount.zurg_api", l.Mount.ZurgAPI)
		if u, err := url.Parse(l.Mount.ZurgAPI); err == nil && (u.RawQuery != "" || u.Fragment != "") {
			e.add("library.mount.zurg_api", "the base URL only, without a query")
		}
		checkRef(e, "library.mount.zurg_api_budget", "budget", l.Mount.ZurgAPIBudget, p.Budgets, false)
	} else if l.Mount.ZurgAPIBudget != "" || l.Mount.ZurgAPIInterval != 0 || l.Mount.ZurgAPIRepairGrace != 0 {
		e.add("library.mount.zurg_api", "required by zurg_api_budget, zurg_api_interval and zurg_api_repair_grace")
	}
	if d := l.Mount.ZurgAPIInterval; d != 0 && (d.D() < MinZurgAPIInterval || d.D() > MaxZurgAPIInterval) {
		e.add("library.mount.zurg_api_interval", "must be %v to %v, got %v", MinZurgAPIInterval, MaxZurgAPIInterval, d.D())
	}
	if d := l.Mount.ZurgAPIRepairGrace; d != 0 && (d.D() < MinZurgRepairGrace || d.D() > MaxZurgRepairGrace) {
		e.add("library.mount.zurg_api_repair_grace", "must be %v to %v, got %v", MinZurgRepairGrace, MaxZurgRepairGrace, d.D())
	}
	if rc := l.Mount.RcloneRC; rc != nil {
		checkURL(e, "library.mount.rclone_rc.url", rc.URL)
		checkEnv(e, "library.mount.rclone_rc.username_env", rc.UsernameEnv, false)
		checkEnv(e, "library.mount.rclone_rc.password_env", rc.PasswordEnv, false)
		checkRef(e, "library.mount.rclone_rc.budget", "budget", rc.Budget, p.Budgets, false)
	}
	for i, r := range l.Dangling.Roots {
		rp := fmt.Sprintf("library.dangling.roots[%d]", i)
		if abs(rp, r, true) && cleanAbs(r) == "/" {
			e.add(rp, "the filesystem root is not a library root")
		}
	}
	if l.Probe.MaxBytes < 0 || (l.Probe.MaxBytes != 0 && (l.Probe.MaxBytes < 1<<20 || l.Probe.MaxBytes > MaxLibraryProbeBytes)) {
		e.add("library.probe.max_bytes", "must be 1 MiB to 256 MiB, got %d", l.Probe.MaxBytes)
	}
	if r := l.Sample.MinRuntimeRatio; r < 0 || r > 1 {
		e.add("library.sample.min_runtime_ratio", "must be over 0 and at most 1, got %v", r)
	}
	if l.Trash.KeepDays < 0 {
		e.add("library.trash.keep_days", "must not be negative")
	}
	trashOK := abs("library.trash.dir", l.Trash.Dir, l.Enabled)
	mounts := []string{cleanAbs(l.Mount.Path), cleanAbs(l.Mount.LinkPath)}
	underMount := func(path, v string) {
		for _, m := range mounts {
			if m != "" && (Within(v, m) || Within(m, v)) {
				e.add(path, "%q overlaps the zurg mount %q: the library never writes into the mount", v, m)
			}
		}
	}
	if trashOK {
		underMount("library.trash.dir", cleanAbs(l.Trash.Dir))
	}
	for i, r := range l.Dangling.Roots {
		if filepath.IsAbs(r) {
			underMount(fmt.Sprintf("library.dangling.roots[%d]", i), cleanAbs(r))
		}
	}
	_ = mountOK

	if l.Plex != nil {
		checkURL(e, "library.plex.url", l.Plex.URL)
		checkEnv(e, "library.plex.token_env", l.Plex.TokenEnv, true)
		checkRef(e, "library.plex.budget", "budget", l.Plex.Budget, p.Budgets, false)
		dur("library.plex.debounce", l.Plex.Debounce, time.Second)
		if l.Plex.PrewarmRate != 0 && l.Plex.PrewarmRate < MinPrewarmRate {
			e.add("library.plex.prewarm_rate", "%d bytes/s is under the 1MB minimum", int64(l.Plex.PrewarmRate))
		}
	}
	for _, k := range sortedKeys(l.Versions) {
		v := l.Versions[k]
		path := fmt.Sprintf("library.versions[%s]", k)
		vp, ok := p.Versions[k]
		if !ok {
			e.add(path, "unknown version %q", k)
			continue
		}
		if v == nil || len(v.Roots) == 0 {
			e.add(path+".roots", "required: the directories this version may write under")
			continue
		}
		// A relative root or library folder is reported as that alone: whether the folder is under a root is
		// only asked of absolute paths.
		covered, relative := false, !filepath.IsAbs(vp.Library)
		for i, r := range v.Roots {
			rp := fmt.Sprintf("%s.roots[%d]", path, i)
			if !abs(rp, r, true) {
				relative = true
				continue
			}
			r = cleanAbs(r)
			if r == "/" {
				e.add(rp, "the filesystem root is not a library root")
				continue
			}
			underMount(rp, r)
			if trashOK && (Within(cleanAbs(l.Trash.Dir), r) || Within(r, cleanAbs(l.Trash.Dir))) {
				e.add(rp, "%q overlaps library.trash.dir: Plex would scan the trash", r)
			}
			if vp.Library != "" && Within(cleanAbs(vp.Library), r) {
				covered = true
			}
		}
		if vp.Library != "" && !covered && !relative {
			e.add(path+".roots", "versions[%s].library %q is under none of the roots", k, vp.Library)
		}
		if v.PlexSection < 0 {
			e.add(path+".plex_section", "must be a Plex section id")
		}
		if v.PlexSection > 0 && l.Plex == nil {
			e.add(path+".plex_section", "set, but library.plex is not configured")
		}
	}
}
