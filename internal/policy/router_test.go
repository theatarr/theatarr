package policy

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRouterSettings(t *testing.T) {
	t.Parallel()
	p, err := Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	def := (&Policy{}).RouterSettings()
	if def.Enabled || !def.CachedOnly || def.MissTTL != 6*time.Hour || def.PollInterval != time.Minute ||
		def.MaxCandidates != 5 || len(def.Trackers) != 0 || def.CachedGrace != 2*time.Second ||
		def.StalledAfter != 10*time.Minute || def.ReadyTimeout != 6*time.Hour || def.AllDebridDeleteUncached {
		t.Errorf("defaults %+v", def)
	}
	if got := p.RouterSettings(); got.Enabled || !got.CachedOnly || got.MissTTL != def.MissTTL ||
		got.PollInterval != def.PollInterval || got.MaxCandidates != def.MaxCandidates || len(got.Trackers) != 0 ||
		!got.AllDebridDeleteUncached {
		t.Errorf("example %+v, defaults %+v", got, def)
	}
	if got := p.ActiveVersions(); len(got) != 0 {
		t.Errorf("example active versions %v, want none", got)
	}
	for _, tc := range []struct {
		yaml, err string
	}{
		{"router:\n  enabled: true\n  cached_only: false\n  miss_ttl: 1h\n  poll_interval: 30s\n  max_candidates: 3\n" +
			"  cached_grace: 5s\n  stalled_after: 0s\n  ready_timeout: 2h\n  alldebrid_delete_uncached: true\n" +
			"  trackers: [udp://tracker.opentrackr.org:1337/announce, https://tracker.example.org/announce]\n", ""},
		{"router:\n  miss_ttl: 1m\n", "router.miss_ttl"},
		{"router:\n  poll_interval: 2h\n", "router.poll_interval"},
		{"router:\n  max_candidates: 51\n", "router.max_candidates"},
		{"router:\n  cached_grace: 500ms\n", "router.cached_grace"},
		{"router:\n  cached_grace: 1m\n", "router.cached_grace"},
		{"router:\n  stalled_after: 30s\n", "router.stalled_after"},
		{"router:\n  stalled_after: 25h\n", "router.stalled_after"},
		{"router:\n  ready_timeout: 30s\n", "router.ready_timeout"},
		{"router:\n  ready_timeout: -1h\n", "router.ready_timeout"},
		{"router:\n  ready_timeout: 169h\n", "router.ready_timeout"},
		{"router:\n  trackers: [ftp://x.org/announce]\n", "router.trackers[0]"},
		{"router:\n  trackers: ['http://t.example.org/announce?passkey=abc']\n", "router.trackers[0]"},
		{"router:\n  trackers: [http://t.example.org/abcdef0123456789abcdef/announce]\n", "router.trackers[0]"},
		{"router:\n  trackers: ['http://user:pw@t.example.org/announce']\n", "router.trackers[0]"},
		{"router:\n  bogus: 1\n", "bogus"},
	} {
		dir := copyExample(t)
		if err := os.WriteFile(filepath.Join(dir, "router.yaml"), []byte(tc.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := Load(dir)
		switch {
		case tc.err == "" && err != nil:
			t.Errorf("%q: %v", tc.yaml, err)
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%q: want an error about %s, got %v", tc.yaml, tc.err, err)
		case tc.err == "":
			s := p.RouterSettings()
			if !s.Enabled || s.CachedOnly || s.MissTTL != time.Hour || s.PollInterval != 30*time.Second ||
				s.MaxCandidates != 3 || len(s.Trackers) != 2 || s.CachedGrace != 5*time.Second ||
				s.StalledAfter != 0 || s.ReadyTimeout != 2*time.Hour || !s.AllDebridDeleteUncached {
				t.Errorf("settings %+v", s)
			}
		}
	}
}

func TestActiveVersions(t *testing.T) {
	t.Parallel()
	p := &Policy{Versions: map[string]*VersionPolicy{"b": {Mode: ModeActive}, "a": {Mode: ModeActive},
		"c": {Mode: ModeShadow}, "d": {}}}
	if got := p.ActiveVersions(); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("got %v", got)
	}
}

func TestWantedSettings(t *testing.T) {
	t.Parallel()
	p, err := Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.WantedSettings(); got.Enabled || got.Interval != 15*time.Minute || got.PerHour != 10 ||
		got.AgainAfter != 24*time.Hour || !got.Missing || got.CutoffUnmet != CutoffQuality {
		t.Errorf("example (no active version): %+v", got)
	}
	// retry_new is on unless the section turns it off.
	for _, q := range []*Policy{p, {}, {Router: &Router{}}, {Router: &Router{Wanted: &Wanted{}}}} {
		if !q.WantedSettings().RetryNew {
			t.Errorf("retry_new off by default: %+v", q.Router)
		}
	}
	if (&Policy{Router: &Router{Wanted: &Wanted{RetryNew: new(false)}}}).WantedSettings().RetryNew {
		t.Error("retry_new: false is not applied")
	}
	active := &Policy{Versions: map[string]*VersionPolicy{"a": {Mode: ModeActive}}}
	if !active.WantedSettings().Enabled {
		t.Error("the loop should default on once a version is active")
	}
	for _, tc := range []struct {
		yaml, err string
	}{
		{"router:\n  wanted:\n    enabled: true\n    interval: 30m\n    per_hour: 4\n    again_after: 6h\n" +
			"    missing: true\n    cutoff_unmet: false\n", ""},
		{"router:\n  wanted:\n    interval: 1m\n", "router.wanted.interval"},
		{"router:\n  wanted:\n    per_hour: 500\n", "router.wanted.per_hour"},
		{"router:\n  wanted:\n    again_after: 10m\n", "router.wanted.again_after"},
		{"router:\n  wanted:\n    bogus: 1\n", "bogus"},
		{"router:\n  wanted:\n    cutoff_unmet: always\n", "router.wanted.cutoff_unmet"},
		{"router:\n  wanted:\n    cutoff_unmet: [quality]\n", "cutoff_unmet"},
	} {
		dir := copyExample(t)
		if err := os.WriteFile(filepath.Join(dir, "router.yaml"), []byte(tc.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := Load(dir)
		switch {
		case tc.err == "" && err != nil:
			t.Errorf("%q: %v", tc.yaml, err)
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%q: want an error about %s, got %v", tc.yaml, tc.err, err)
		case tc.err == "":
			s := p.WantedSettings()
			if !s.Enabled || s.Interval != 30*time.Minute || s.PerHour != 4 || s.AgainAfter != 6*time.Hour ||
				!s.Missing || s.CutoffUnmet != CutoffOff {
				t.Errorf("settings %+v", s)
			}
		}
	}
}

// router.wanted.cutoff_unmet takes quality, score or off; the booleans of the earlier schema still load (true is
// quality, the arrs' cutoff-unmet search; false is off).
func TestWantedCutoffMode(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]CutoffMode{"": CutoffQuality, "quality": CutoffQuality, "score": CutoffScore,
		"off": CutoffOff, "Score": CutoffScore, "true": CutoffQuality, "false": CutoffOff, `"off"`: CutoffOff} {
		dir := copyExample(t)
		y := "router:\n  wanted:\n    per_hour: 4\n"
		if in != "" {
			y += "    cutoff_unmet: " + in + "\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "router.yaml"), []byte(y), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := Load(dir)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got := p.WantedSettings().CutoffUnmet; got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestCacheEvidenceSettings(t *testing.T) {
	t.Parallel()
	p, err := Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	def := (&Policy{}).CacheEvidenceSettings()
	if def.TTL != 24*time.Hour || def.UncachedTTL != 6*time.Hour || !def.CheckEnabled || def.CheckTop != 10 ||
		def.CheckPerHour != 60 {
		t.Errorf("defaults %+v", def)
	}
	if got := p.CacheEvidenceSettings(); got != def {
		t.Errorf("example %+v, defaults %+v", got, def)
	}
	for _, tc := range []struct {
		yaml, err string
	}{
		{"router:\n  miss_ttl: 2h\n  cache_evidence:\n    ttl: 12h\n    torbox_check: {enabled: false, top: 5, per_hour: 20}\n", ""},
		{"router:\n  cache_evidence:\n    ttl: 30m\n", "router.cache_evidence.ttl"},
		{"router:\n  cache_evidence:\n    torbox_check: {top: 101}\n", "router.cache_evidence.torbox_check.top"},
		{"router:\n  cache_evidence:\n    torbox_check: {per_hour: -1}\n", "router.cache_evidence.torbox_check.per_hour"},
	} {
		dir := copyExample(t)
		if err := os.WriteFile(filepath.Join(dir, "router.yaml"), []byte(tc.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := Load(dir)
		switch {
		case tc.err == "" && err != nil:
			t.Errorf("%q: %v", tc.yaml, err)
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%q: want an error about %s, got %v", tc.yaml, tc.err, err)
		case tc.err == "":
			if s := p.CacheEvidenceSettings(); s.TTL != 12*time.Hour || s.UncachedTTL != 2*time.Hour || s.CheckEnabled ||
				s.CheckTop != 5 || s.CheckPerHour != 20 {
				t.Errorf("settings %+v", s)
			}
		}
	}
}

// router.wanted.retry_new is on when the router section has no wanted block or the block leaves the field out, and
// off only when the file says false. The stored-section side (an explicit false is kept) is in internal/settings.
func TestRetryNewDefaultOn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		yaml string
		want bool
	}{
		{"router:\n  enabled: false\n", true},
		{"router:\n  wanted:\n    per_hour: 20\n", true},
		{"router:\n  wanted:\n    retry_new: false\n", false},
	} {
		dir := copyExample(t)
		if err := os.WriteFile(filepath.Join(dir, "router.yaml"), []byte(tc.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := Load(dir)
		if err != nil {
			t.Fatalf("%q: %v", tc.yaml, err)
		}
		if got := p.WantedSettings().RetryNew; got != tc.want {
			t.Errorf("%q: retry_new %v, want %v", tc.yaml, got, tc.want)
		}
	}
}
