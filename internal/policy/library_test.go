package policy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLibrarySettings(t *testing.T) {
	t.Parallel()
	p, err := Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	s := p.LibrarySettings()
	if s.Enabled {
		t.Error("the example library must be disabled")
	}
	if s.AllDir != "__all__" || s.WaitTimeout != 6*time.Minute || s.ProbeMaxBytes != 16<<20 || s.TrashKeepDays != 30 ||
		s.PlexDebounce != 30*time.Second || s.LinkPath != "/mnt/debrid" || s.Versions["movie-remux"].PlexSection != 1 {
		t.Errorf("settings %+v", s)
	}
	if def := (&Policy{}).LibrarySettings(); def.Enabled || len(def.Versions) != 0 {
		t.Errorf("no section: %+v", def)
	}
	found := false
	for _, h := range p.OutboundHosts() {
		if h.Host == "plex:32400" {
			found = true
		}
	}
	if !found {
		t.Error("plex host not in OutboundHosts")
	}
	// The example sets 60s; the default (no section) is 15s.
	if s.ZurgLogInterval != time.Minute || (&Policy{}).LibrarySettings().ZurgLogInterval != 15*time.Second {
		t.Errorf("zurg_log_interval %v", s.ZurgLogInterval)
	}
	p.Library.Mount.ZurgLog = "http://zurg:9999/logs"
	if s := p.LibrarySettings(); !s.ZurgLogURL || s.ZurgLog != "http://zurg:9999/logs" {
		t.Errorf("URL zurg_log: %+v", s)
	}
	found = false
	for _, h := range p.OutboundHosts() {
		found = found || (h.Host == "zurg:9999" && h.Users[0] == "library.mount.zurg_log")
	}
	if !found {
		t.Error("zurg_log host not in OutboundHosts")
	}
	p.Library.Mount.ZurgLog = "/logs/./zurg.log"
	if s := p.LibrarySettings(); s.ZurgLogURL || s.ZurgLog != "/logs/zurg.log" {
		t.Errorf("file zurg_log: %+v", s)
	}
	// zurg_api: the example sets it; off (and defaults) without a section.
	if s.ZurgAPI != "http://zurg:9999" || s.ZurgAPIInterval != time.Minute || s.ZurgAPIRepairGrace != 2*time.Hour {
		t.Errorf("zurg_api %+v", s)
	}
	if def := (&Policy{}).LibrarySettings(); def.ZurgAPI != "" || def.ZurgAPIInterval != time.Minute ||
		def.ZurgAPIRepairGrace != 2*time.Hour {
		t.Errorf("zurg_api defaults %+v", def)
	}
	found = false
	for _, h := range p.OutboundHosts() {
		for _, u := range h.Users {
			found = found || (h.Host == "zurg:9999" && u == "library.mount.zurg_api")
		}
	}
	if !found {
		t.Error("zurg_api host not in OutboundHosts")
	}
	if _, ok := p.SecretEnvs()["THEATARR_PLEX_TOKEN"]; !ok {
		t.Error("plex token env not in SecretEnvs")
	}
}

// The pre-warm's whole-file read: 25 MB/s and 20 GB by default, sizes with units.
// A stored library.sample without a mislabel key (live's, written before the guard) keeps the guard on.
func TestMislabelGuardDefaultsOn(t *testing.T) {
	t.Parallel()
	dir := copyExample(t)
	os.Remove(filepath.Join(dir, "library.yaml"))
	if err := os.WriteFile(filepath.Join(dir, "lib.yaml"), []byte("library:\n  sample: {min_runtime_ratio: 0.5, resolution: true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s := p.LibrarySettings(); !s.Mislabel || s.MinRuntimeRatio != 0.5 {
		t.Errorf("settings %+v", s)
	}
}

func TestPrewarmSettings(t *testing.T) {
	t.Parallel()
	s := (&Policy{Library: &Library{Plex: &Plex{URL: "http://plex:32400"}}}).LibrarySettings()
	if !s.Prewarm || s.PrewarmRate != 25_000_000 || s.PrewarmMaxSize != 20_000_000_000 {
		t.Errorf("defaults %v %d %d", s.Prewarm, s.PrewarmRate, s.PrewarmMaxSize)
	}
	s = (&Policy{Library: &Library{Plex: &Plex{URL: "http://plex:32400", PrewarmRate: 10 << 20,
		PrewarmMaxSize: 8 << 30}}}).LibrarySettings()
	if s.PrewarmRate != 10<<20 || s.PrewarmMaxSize != 8<<30 {
		t.Errorf("set %d %d", s.PrewarmRate, s.PrewarmMaxSize)
	}
}

func TestLibraryValidation(t *testing.T) {
	t.Parallel()
	base := `library:
  enabled: true
  mount: {path: /mnt/debrid}
  trash: {dir: /media/.theatarr-trash}
  versions:
    movie-remux: {roots: [/media/movies]}
`
	for _, tc := range []struct {
		yaml, err string
	}{
		{base, ""},
		{"library:\n  enabled: true\n  trash: {dir: /t}\n", "library.mount.path"},
		{"library:\n  enabled: true\n  mount: {path: /m}\n", "library.trash.dir"},
		{"library:\n  mount: {path: relative}\n", "library.mount.path"},
		{"library:\n  mount: {path: /mnt/debrid}\n  trash: {dir: /mnt/debrid/trash}\n", "overlaps the zurg mount"},
		{"library:\n  mount: {path: /mnt/debrid}\n  versions:\n    movie-remux: {roots: [/mnt]}\n", "overlaps the zurg mount"},
		{"library:\n  versions:\n    movie-remux: {roots: [/srv/other]}\n", "is under none of the roots"},
		{"library:\n  versions:\n    movie-remux: {roots: [/]}\n", "filesystem root"},
		{"library:\n  versions:\n    nope: {roots: [/x]}\n", "unknown version"},
		{"library:\n  versions:\n    movie-remux: {}\n", "library.versions[movie-remux].roots"},
		{"library:\n  trash: {dir: /media/movies/.trash}\n  versions:\n    movie-remux: {roots: [/media/movies]}\n", "Plex would scan the trash"},
		{"library:\n  versions:\n    movie-remux: {roots: [/media/movies], plex_section: 3}\n", "library.plex is not configured"},
		{"library:\n  plex: {url: http://plex:32400}\n", "library.plex.token_env"},
		{"library:\n  plex: {url: http://plex:32400, token_env: THEATARR_PLEX_TOKEN, prewarm_rate: 100KB}\n", "library.plex.prewarm_rate"},
		{"library:\n  plex: {url: http://plex:32400, token_env: THEATARR_PLEX_TOKEN, prewarm_rate: 10MB, prewarm_max_size: 8GiB}\n", ""},
		{"library:\n  probe: {max_bytes: 10}\n", "library.probe.max_bytes"},
		{"library:\n  sample: {min_runtime_ratio: 2}\n", "min_runtime_ratio"},
		{"library:\n  mount: {poll_interval: 1s}\n", "library.mount.poll_interval"},
		{"library:\n  mount: {zurg_log: /logs/zurg.log}\n", ""},
		{"library:\n  mount: {zurg_log: logs/zurg.log}\n", "library.mount.zurg_log"},
		{"library:\n  mount: {zurg_log: \"http://zurg:9999/logs\", zurg_log_interval: 30s}\n", ""},
		{"library:\n  mount: {zurg_log: \"http://user:pw@zurg:9999/logs\"}\n", "credentials in the URL"},
		{"library:\n  mount: {zurg_log: \"http://zurg:9999/logs\", zurg_log_interval: 1s}\n", "library.mount.zurg_log_interval"},
		{"library:\n  mount: {zurg_log: \"http://zurg:9999/logs\", zurg_log_interval: 1h}\n", "library.mount.zurg_log_interval"},
		{"library:\n  mount: {zurg_log: \"http://zurg:9999/logs\", zurg_log_budget: nope}\n", "library.mount.zurg_log_budget"},
		{"library:\n  mount: {zurg_log: /logs/zurg.log, zurg_log_budget: plex}\n", "only for a URL"},
		{"library:\n  mount: {zurg_api: \"http://zurg:9999\", zurg_api_interval: 30s, zurg_api_repair_grace: 6h}\n", ""},
		{"library:\n  mount: {zurg_api: /logs}\n", "library.mount.zurg_api"},
		{"library:\n  mount: {zurg_api: \"http://user:pw@zurg:9999\"}\n", "credentials in the URL"},
		{"library:\n  mount: {zurg_api: \"http://zurg:9999/?x=1\"}\n", "the base URL only"},
		{"library:\n  mount: {zurg_api: \"http://zurg:9999\", zurg_api_budget: nope}\n", "library.mount.zurg_api_budget"},
		{"library:\n  mount: {zurg_api: \"http://zurg:9999\", zurg_api_interval: 1s}\n", "library.mount.zurg_api_interval"},
		{"library:\n  mount: {zurg_api: \"http://zurg:9999\", zurg_api_interval: 2h}\n", "library.mount.zurg_api_interval"},
		{"library:\n  mount: {zurg_api: \"http://zurg:9999\", zurg_api_repair_grace: 1m}\n", "library.mount.zurg_api_repair_grace"},
		{"library:\n  mount: {zurg_api: \"http://zurg:9999\", zurg_api_repair_grace: 48h}\n", "library.mount.zurg_api_repair_grace"},
		{"library:\n  mount: {zurg_api_interval: 30s}\n", "required by zurg_api_budget"},
	} {
		dir := copyExample(t)
		os.Remove(filepath.Join(dir, "library.yaml"))
		if err := os.WriteFile(filepath.Join(dir, "lib.yaml"), []byte(tc.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(dir)
		switch {
		case tc.err == "" && err != nil:
			t.Errorf("%q: %v", tc.yaml, err)
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%q: want an error about %s, got %v", tc.yaml, tc.err, err)
		}
	}
}

// A relative root is reported as "must be an absolute path" at that root, not as a library folder under none of the
// roots (the folder's own check says it must be absolute too).
func TestLibraryRelativeRoot(t *testing.T) {
	t.Parallel()
	dir := copyExample(t)
	os.Remove(filepath.Join(dir, "library.yaml"))
	if err := os.WriteFile(filepath.Join(dir, "lib.yaml"), []byte("library:\n  versions:\n    movie-remux: {roots: [relative-folder]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	var verr *Errors
	if !errors.As(err, &verr) {
		t.Fatalf("want validation errors, got %v", err)
	}
	if len(verr.List) != 1 || verr.List[0].Path != "library.versions[movie-remux].roots[0]" || !strings.Contains(verr.List[0].Msg, "must be an absolute path") {
		t.Errorf("errors %v", verr.List)
	}
}

func TestNamingValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		yaml, err string
	}{
		{"    naming: {from_arr: radarr_4k_debrid}\n", ""},
		{"    naming: {from_arr: sonarr_4k_debrid}\n", "cannot name a movie version"},
		{"    naming: {from_arr: nope}\n", "unknown import.arr instance"},
		{"    naming: {from_arr: radarr_4k_debrid, colon: dash}\n", "set nothing else"},
		{"    naming: {colon: sideways}\n", "versions[x-movie].naming.colon"},
		{"    naming: {multi_episode: range}\n", "only for series"},
		{"    naming: {season: \"Season {season}\"}\n", "for series versions"},
	} {
		dir := copyExample(t)
		y := "versions:\n  x-movie:\n    kind: movie\n    profile: remux-2160p\n    library: /media/x\n    storage: debrid\n" +
			"    indexers: [prowlarr-public]\n    providers: [rd]\n    client: decypharr\n" + tc.yaml
		if err := os.WriteFile(filepath.Join(dir, "x.yaml"), []byte(y), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(dir)
		switch {
		case tc.err == "" && err != nil:
			t.Errorf("%q: %v", tc.yaml, err)
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%q: want an error about %s, got %v", tc.yaml, tc.err, err)
		}
	}
}
