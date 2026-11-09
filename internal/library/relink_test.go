package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

const relinkHashB = "1111111111111111111111111111111111111111"

func TestRelinkRenameCollision(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, file string
		collision  bool
		wantMove   bool
	}{
		{"collision", "episode.mkv", true, true},
		{"plain rename", "episode.mkv", false, true},
		{"missing destination file", "other.mkv", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			if tt.collision {
				e.release("Name", map[string]float64{"episode.mkv": 7000})
			}
			e.release("Name {abcdef}", map[string]float64{tt.file: 7000})
			if !tt.wantMove {
				// A different candidate must not replace a hint whose destination lacks the file.
				e.release("Other {abcdef}", map[string]float64{"episode.mkv": 7000})
				e.exec(`INSERT INTO content (info_hash, name, mount_folder, first_seen_at, last_seen_at)
					VALUES (?, 'Name', 'Other {abcdef}', 0, 0)`, hashA)
			}
			p := filepath.Join(e.movies, "Movie (2001)", "Movie.mkv")
			old := filepath.Join(e.mount, "__all__", "Name", "episode.mkv")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(old, p); err != nil {
				t.Fatal(err)
			}
			if tt.collision {
				if scans, err := e.lib.Relink(e.ctx, "movie-remux", false); err != nil || len(scans[0].Relinked) != 0 {
					t.Fatalf("sweep guessed the identity of an untracked healthy link: %+v %v", scans, err)
				}
			}
			scans, err := e.lib.RelinkRenamed(e.ctx, map[string]string{"Name": "Name {abcdef}"})
			if err != nil {
				t.Fatal(err)
			}
			var sc *LinkScan
			for _, s := range scans {
				if s.Version == "movie-remux" {
					sc = s
				}
			}
			want := old
			if tt.wantMove {
				want = filepath.Join(e.mount, "__all__", "Name {abcdef}", "episode.mkv")
				if len(sc.Relinked) != 1 || len(sc.Dangling) != 0 {
					t.Fatalf("scan %+v", sc)
				}
			} else if len(sc.Relinked) != 0 || len(sc.Dangling) != 0 {
				t.Fatalf("healthy target changed: %+v", sc)
			}
			if got, err := os.Readlink(p); err != nil || got != want {
				t.Fatalf("target %q, want %q: %v", got, want, err)
			}
		})
	}
}

func TestRelinkKeepsNewerTorrent(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("Name", map[string]float64{"episode.mkv": 7000})
	e.release("Name {abcdef}", map[string]float64{"episode.mkv": 7000})
	old := filepath.Join(e.mount, "__all__", "Name", "episode.mkv")
	for i, hash := range []string{hashA, relinkHashB} {
		folder := "Name"
		e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, first_seen_at, last_seen_at)
			VALUES (?, ?, 'Name', ?, 0, 0)`, i+1, hash, folder)
		e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (?, ?, 'episode.mkv', 1)`, i+1, i+1)
		p := filepath.Join(e.movies, hash+".mkv")
		if err := os.Symlink(old, p); err != nil {
			t.Fatal(err)
		}
		e.exec(`INSERT INTO link (path, target, content_file_id, title_id, state, created_at, updated_at)
			VALUES (?, ?, ?, 1, 'active', 0, 0)`, p, old, i+1)
	}
	scans, err := e.lib.RelinkRenamed(e.ctx, map[string]string{"Name": "Name {abcdef}"})
	if err != nil {
		t.Fatal(err)
	}
	var moved int
	for _, sc := range scans {
		moved += len(sc.Relinked)
		if len(sc.Dangling) != 0 {
			t.Fatalf("newer torrent's link reported dead: %+v", sc)
		}
	}
	if moved != 1 {
		t.Fatalf("moved %d links, want only the older torrent's link", moved)
	}
	if got := e.one(`SELECT mount_folder FROM content WHERE info_hash = ?`, hashA); got != "Name {abcdef}" {
		t.Errorf("older torrent mount folder %q", got)
	}
	if got := e.one(`SELECT mount_folder FROM content WHERE info_hash = ?`, relinkHashB); got != "Name" {
		t.Errorf("newer torrent mount folder %q", got)
	}
	if got := e.one(`SELECT target || '|' || state FROM link WHERE content_file_id = 2`); got != old+"|active" {
		t.Errorf("newer torrent link %q", got)
	}
	if scans, err := e.lib.Relink(e.ctx, "movie-remux", false); err != nil || len(scans[0].Relinked) != 0 || len(scans[0].Dangling) != 0 {
		t.Fatalf("sweep changed links after the rename: %+v %v", scans, err)
	}
}

func TestRelinkLeavesAmbiguousHealthyLinks(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("Name", map[string]float64{"episode.mkv": 7000})
	old := filepath.Join(e.mount, "__all__", "Name", "episode.mkv")
	for i, hash := range []string{hashA, relinkHashB} {
		e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, first_seen_at, last_seen_at)
			VALUES (?, ?, 'Name', 'Name', 0, 0)`, i+1, hash)
		e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (?, ?, 'episode.mkv', 1)`, i+1, i+1)
		p := filepath.Join(e.movies, hash+".mkv")
		if err := os.Symlink(old, p); err != nil {
			t.Fatal(err)
		}
		e.exec(`INSERT INTO link (id, path, target, content_file_id, title_id, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, 1, 'active', 0, 0)`, i+1, p, old, i+1)
		e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (?, 10)`, i+1)
	}
	e.exec(`UPDATE version SET state = 'filled' WHERE id = 10`)
	for pass := 0; pass < 2; pass++ {
		scans, err := e.lib.Relink(e.ctx, "movie-remux", false)
		if err != nil {
			t.Fatal(err)
		}
		if len(scans[0].Relinked) != 0 || len(scans[0].Dangling) != 0 {
			t.Fatalf("pass %d changed healthy links: %+v", pass+1, scans[0])
		}
		if got := e.one(`SELECT COUNT(*) FROM library_action`); got != "0" {
			t.Fatalf("pass %d recorded actions: %s", pass+1, got)
		}
	}
	if got := e.one(`SELECT COUNT(*) FROM link WHERE state = 'dead'`); got != "0" {
		t.Errorf("dead healthy links: %s", got)
	}
}

func TestRelinkKeepsHealthyCurrentTorrent(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("Name", map[string]float64{"episode.mkv": 7000})
	old := filepath.Join(e.mount, "__all__", "Name", "episode.mkv")
	e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, state, first_seen_at, last_seen_at)
		VALUES (1, ?, 'Name', 'Name', 'gone', 0, 0)`, hashA)
	e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, first_seen_at, last_seen_at)
		VALUES (2, ?, 'Name', 'Name', 0, 0)`, relinkHashB)
	e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (2, 2, 'episode.mkv', 1)`)
	p := filepath.Join(e.movies, "Movie.mkv")
	if err := os.Symlink(old, p); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, content_file_id, title_id, state, created_at, updated_at)
		VALUES (1, ?, ?, 2, 1, 'active', 0, 0)`, p, old)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
	e.exec(`UPDATE version SET state = 'filled' WHERE id = 10`)
	scans, err := e.lib.Relink(e.ctx, "movie-remux", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(scans[0].Relinked) != 0 || len(scans[0].Dangling) != 0 {
		t.Fatalf("healthy current torrent changed: %+v", scans[0])
	}
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "active" {
		t.Errorf("link state %q", got)
	}
}

func TestRelinkKeepsAdoptedLinkWithAmbiguousRegistry(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("Name", map[string]float64{"episode.mkv": 7000})
	old := filepath.Join(e.mount, "__all__", "Name", "episode.mkv")
	e.exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at)
		VALUES (1, ?, 'Name', 'unknown', 0, 0)`, hashA)
	e.exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at)
		VALUES (2, ?, 'Name', 'unknown', 0, 0)`, relinkHashB)
	p := filepath.Join(e.movies, "Movie.mkv")
	if err := os.Symlink(old, p); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, file_metadata, state, created_at, updated_at)
		VALUES (1, ?, ?, 1, ?, 'active', 0, 0)`, p, old, `{"InfoHash":"`+relinkHashB+`"}`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
	e.exec(`UPDATE version SET state = 'filled' WHERE id = 10`)
	scans, err := e.lib.Relink(e.ctx, "movie-remux", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(scans[0].Relinked) != 0 || len(scans[0].Dangling) != 0 {
		t.Fatalf("healthy adopted link changed: %+v", scans[0])
	}
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "active" {
		t.Errorf("link state %q", got)
	}
}

func TestRelinkCollisionGate(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		mode    policy.Mode
		enabled bool
		dryRun  bool
		hint    bool
	}{
		{"dry run", policy.ModeActive, true, true, false},
		{"disabled", policy.ModeActive, false, false, false},
		{"shadow sweep", policy.ModeShadow, true, false, false},
		{"shadow hint", policy.ModeShadow, true, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, tt.enabled, tt.mode)
			e.release("Name", map[string]float64{"episode.mkv": 7000})
			e.release("Name {abcdef}", map[string]float64{"episode.mkv": 7000})
			e.exec(`INSERT INTO content (id, info_hash, name, first_seen_at, last_seen_at) VALUES (1, ?, 'Name', 0, 0)`, hashA)
			e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (1, 1, 'episode.mkv', 1)`)
			p := filepath.Join(e.movies, "Movie.mkv")
			old := filepath.Join(e.mount, "__all__", "Name", "episode.mkv")
			if err := os.Symlink(old, p); err != nil {
				t.Fatal(err)
			}
			e.exec(`INSERT INTO link (path, target, content_file_id, title_id, created_at, updated_at)
				VALUES (?, ?, 1, 1, 0, 0)`, p, old)
			hints := map[string]string(nil)
			if tt.hint {
				hints = map[string]string{"Name": "Name {abcdef}"}
			}
			scans, err := e.lib.relink(e.ctx, "movie-remux", tt.dryRun, hints)
			if err != nil {
				t.Fatal(err)
			}
			if !scans[0].DryRun || len(scans[0].Relinked) != 1 {
				t.Fatalf("scan %+v", scans[0])
			}
			if got, err := os.Readlink(p); err != nil || got != old {
				t.Errorf("target %q: %v", got, err)
			}
			if got := e.one(`SELECT target || '|' || state FROM link`); got != old+"|active" {
				t.Errorf("gated link row %q", got)
			}
			if got := e.one(`SELECT COUNT(*) FROM library_action WHERE mode = 'applied'`); got != "0" {
				t.Errorf("applied actions %s", got)
			}
			if got := e.one(`SELECT COUNT(*) FROM job WHERE kind = 'plex.scan'`); got != "0" {
				t.Errorf("Plex scan jobs %s", got)
			}
		})
	}
}

func TestRelinkRecordedIdentity(t *testing.T) {
	t.Parallel()
	const hashB = "1111111111111111111111111111111111111111"
	for _, tt := range []struct {
		name, source, old, current, file string
		other                            bool
		targetExists                     bool
		wantMove, wantMissing            bool
	}{
		{"content file", "content", "Name", "Name {abcdef}", "episode.mkv", true, true, true, false},
		{"download hash", "download", "Name", "Name {abcdef}", "episode.mkv", true, true, true, false},
		{"link metadata hash", "metadata", "Name", "Name {abcdef}", "episode.mkv", true, true, true, false},
		{"provider torrent id", "torrent", "Name", "Name {abcdef}", "episode.mkv", true, true, true, false},
		{"missing file", "content", "Name", "Name {abcdef}", "other.mkv", true, false, false, true},
		{"another torrent owns bare name", "content", "Old {abcdef}", "Name", "episode.mkv", true, false, false, true},
		{"old suffix without current mount evidence", "content without mount folder", "Name {abcdef}", "Name", "episode.mkv", false, false, false, true},
		{"wrong suffix", "content", "Old", "Name {111111}", "episode.mkv", false, false, false, true},
		{"current mount folder", "content", "Old", "New", "episode.mkv", false, false, true, false},
		{"healthy target with matching suffix", "content", "Name", "Name {abcdef}", "episode.mkv", false, true, true, false},
		{"missing file at current mount folder", "content", "Name", "New", "other.mkv", false, false, false, true},
		{"healthy bare name", "content", "Name", "Name", "episode.mkv", false, true, false, false},
		{"healthy target without registry", "unregistered download", "Name", "Name", "episode.mkv", false, true, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.release(tt.current, map[string]float64{tt.file: 7000})
			if tt.targetExists && !hash6Suffix.MatchString(tt.old) {
				e.release(tt.old, map[string]float64{"episode.mkv": 7000})
			}
			if tt.source != "unregistered download" {
				e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, first_seen_at, last_seen_at)
					VALUES (1, ?, 'Name', ?, 0, 0)`, hashA, tt.current)
			}
			if tt.other {
				e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, first_seen_at, last_seen_at)
					VALUES (2, ?, 'Name', 'Name', 0, 0)`, hashB)
			}
			p := filepath.Join(e.movies, "Movie (2001)", "Movie.mkv")
			old := filepath.Join(e.mount, "__all__", tt.old, "episode.mkv")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(old, p); err != nil {
				t.Fatal(err)
			}
			e.exec(`INSERT INTO link (id, path, target, title_id, created_at, updated_at) VALUES (1, ?, ?, 1, 0, 0)`, p, old)
			switch tt.source {
			case "metadata":
				e.exec(`UPDATE link SET file_metadata = ? WHERE id = 1`, `{"InfoHash":"`+hashA+`"}`)
			case "content", "content without mount folder":
				e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (1, 1, 'episode.mkv', 1)`)
				e.exec(`UPDATE link SET content_file_id = 1 WHERE id = 1`)
				if tt.source == "content without mount folder" {
					e.exec(`UPDATE content SET mount_folder = NULL WHERE id = 1`)
				}
			case "download", "torrent", "unregistered download":
				e.download(1, "movie-remux", 1, 10, "Name", "Name", "[]")
				e.exec(`UPDATE link SET download_id = 1 WHERE id = 1`)
				if tt.source == "torrent" {
					e.exec(`UPDATE download SET info_hash = '', provider_torrent_id = 'torrent-a' WHERE id = 1`)
					e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, folder_name, seen_at)
						VALUES (1, 'rd', 'torrent-a', 'Name', 0)`)
				}
			}
			if tt.wantMissing {
				// The first sweep only notes the miss; the one deadConfirm later marks it dead.
				if _, err := e.lib.Relink(e.ctx, "movie-remux", false); err != nil {
					t.Fatal(err)
				}
				e.now = e.now.Add(deadConfirm)
			}
			scans, err := e.lib.Relink(e.ctx, "movie-remux", false)
			if err != nil {
				t.Fatal(err)
			}
			sc := scans[0]
			want, state := old, "active"
			if tt.wantMove {
				want = filepath.Join(e.mount, "__all__", tt.current, "episode.mkv")
				if len(sc.Relinked) != 1 || sc.Relinked[0].Hash != hashA {
					t.Fatalf("identity relink: %+v", sc)
				}
				if got := e.one(`SELECT mount_folder FROM content WHERE info_hash = ?`, hashA); got != tt.current {
					t.Errorf("content mount folder %q, want %q", got, tt.current)
				}
				if got := e.one(`SELECT detail FROM library_action WHERE kind = 'relink' AND mode = 'applied'`); !strings.Contains(got, hashA) {
					t.Errorf("journal detail %q", got)
				}
				if got := e.one(`SELECT COUNT(*) FROM job WHERE kind = 'plex.scan'`); got != "1" {
					t.Errorf("Plex scan jobs %s", got)
				}
			} else if len(sc.Relinked) != 0 {
				t.Fatalf("unexpected relink: %+v", sc)
			}
			if tt.wantMissing {
				state = "dead"
				if len(sc.Dangling) != 1 || sc.Dangling[0].Cause != "missing" {
					t.Fatalf("unresolved identity not reported: %+v", sc)
				}
			} else if len(sc.Dangling) != 0 {
				t.Fatalf("unexpected dangling: %+v", sc)
			}
			if got, err := os.Readlink(p); err != nil || got != want {
				t.Errorf("target %q, want %q: %v", got, want, err)
			}
			if got := e.one(`SELECT target || '|' || state FROM link WHERE id = 1`); got != want+"|"+state {
				t.Errorf("link row %q", got)
			}
			if got := e.one(`SELECT COUNT(*) FROM link_trash`); got != "0" {
				t.Errorf("relink created trash rows: %s", got)
			}
			if tt.wantMove {
				if scans, err := e.lib.Relink(e.ctx, "movie-remux", false); err != nil || len(scans[0].Relinked) != 0 {
					t.Fatalf("second sweep: %+v %v", scans, err)
				}
			}
		})
	}
}
