package library

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// Link maintenance: renames from zurg's log, the owner of a replaced link, the dangling
// report.

func TestParseZurgRename(t *testing.T) {
	t.Parallel()
	for line, want := range map[string][2]string{
		`2026-10-20T10:00:00Z INFO Renaming "Show.S01.1080p" to "Show.S01.1080p {abc123}" (another release shares its name)`: {"Show.S01.1080p", "Show.S01.1080p {abc123}"},
		`INFO Release "Movie 2001 [x]" is filed as "Movie.2001.2160p" now that its preferred account names it`:               {"Movie 2001 [x]", "Movie.2001.2160p"},
		`Renaming "A \"quoted\" é" to "B" (another release shares its name)`:                                                 {`A "quoted" é`, "B"},
	} {
		o, n, ok := ParseZurgRename(line)
		if !ok || o != want[0] || n != want[1] {
			t.Errorf("%s: %q %q %v", line, o, n, ok)
		}
	}
	for _, line := range []string{
		`Renaming "A" to "A" (another release shares its name)`,
		`Renaming "A" to "B"`,
		`some other line`,
		`Release "A" is filed as "B"`,
	} {
		if _, _, ok := ParseZurgRename(line); ok {
			t.Errorf("%s: parsed", line)
		}
	}
}

func TestZurgLogFollower(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "zurg.log")
	if err := os.WriteFile(log, []byte(`Renaming "Before" to "Start" (another release shares its name)`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := make(chan map[string]string, 4)
	f := &ZurgLogFollower{Path: log, Poll: 5 * time.Millisecond, Batch: 40 * time.Millisecond,
		OnRenames: func(_ context.Context, m map[string]string) { got <- m }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.Run(ctx)
	time.Sleep(30 * time.Millisecond)
	appendTo := func(p, s string) {
		fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		fh.WriteString(s)
		fh.Close()
	}
	appendTo(log, "noise\n"+`Renaming "A" to "B" (another release shares its name)`+"\n")
	appendTo(log, `Release "B" is filed as "C" now that its preferred account names it`+"\n"+`Renaming "X" to "Y" (another`)
	appendTo(log, ` release shares its name)`+"\n")
	select {
	case m := <-got:
		if len(m) != 3 || m["A"] != "C" || m["B"] != "C" || m["X"] != "Y" {
			t.Errorf("batch %v (the line before the start must not count; a->b->c chains)", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no renames")
	}
	// Rotation: a new file under the same name is read from its start.
	if err := os.Rename(log, log+".1"); err != nil {
		t.Fatal(err)
	}
	appendTo(log, `Renaming "R1" to "R2" (another release shares its name)`+"\n")
	select {
	case m := <-got:
		if len(m) != 1 || m["R1"] != "R2" {
			t.Errorf("after rotation %v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no renames after rotation")
	}
}

func TestRelinkRenamed(t *testing.T) {
	oldWait, oldPoll := RenameWait, renamePoll
	RenameWait, renamePoll = 200*time.Millisecond, 10*time.Millisecond
	defer func() { RenameWait, renamePoll = oldWait, oldPoll }()
	e := newEnv(t, true, policy.ModeActive)
	e.now = time.Now()
	e.lib.now = time.Now
	// Nothing in our database knows the release: only the log's rename finds it.
	e.release("New.Name.2001.2160p", map[string]float64{"movie.mkv": 7000})
	e.release("Other.New", map[string]float64{"else.mkv": 7000})
	link := filepath.Join(e.movies, "Movie (2001)", "Movie (2001).mkv")
	miss := filepath.Join(e.movies, "Other (2002)", "Other (2002).mkv")
	for p, tg := range map[string]string{link: "Old Name/movie.mkv", miss: "Other.Old/other.mkv"} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(e.mount, "__all__", tg), p); err != nil {
			t.Fatal(err)
		}
	}
	if scans, err := e.lib.Relink(e.ctx, "", false); err != nil || len(scans[0].Relinked) != 0 {
		t.Fatalf("the sweep alone relinked: %+v %v", scans, err)
	}
	scans, err := e.lib.RelinkRenamed(e.ctx, map[string]string{"Old Name": "New.Name.2001.2160p",
		"Other.Old": "Other.New", "Never": "Never.There"})
	if err != nil {
		t.Fatal(err)
	}
	var relinked, dangling int
	for _, s := range scans {
		relinked += len(s.Relinked)
		dangling += len(s.Dangling)
	}
	if relinked != 1 || dangling != 1 {
		t.Errorf("relinked %d dangling %d (the other folder lacks the file)", relinked, dangling)
	}
	if tg, _ := os.Readlink(link); tg != filepath.Join(e.mount, "__all__", "New.Name.2001.2160p", "movie.mkv") {
		t.Errorf("link -> %q", tg)
	}
	if tg, _ := os.Readlink(miss); !strings.Contains(tg, "Other.Old") {
		t.Errorf("a link whose file is not in the new folder moved: %q", tg)
	}
	if n := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'relink' AND mode = 'applied'`); n != "1" {
		t.Errorf("relink actions %s", n)
	}
	// Shadow: the gate makes it a dry run.
	e2 := newEnv(t, true, policy.ModeShadow)
	e2.release("New", map[string]float64{"movie.mkv": 7000})
	l2 := filepath.Join(e2.movies, "M (2001)", "M (2001).mkv")
	os.MkdirAll(filepath.Dir(l2), 0o755)
	os.Symlink(filepath.Join(e2.mount, "__all__", "Old", "movie.mkv"), l2)
	if _, err := e2.lib.RelinkRenamed(e2.ctx, map[string]string{"Old": "New"}); err != nil {
		t.Fatal(err)
	}
	if tg, _ := os.Readlink(l2); !strings.Contains(tg, "/Old/") {
		t.Errorf("a shadow version's link moved: %q", tg)
	}
}

func TestRelinkKeepsOwner(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 {
		t.Skip("needs root to chown")
	}
	e := newEnv(t, true, policy.ModeActive)
	e.release("Movie.Release {abcdef}", map[string]float64{"movie.mkv": 7000})
	e.exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES (1, ?, 'Movie.Release', 'present', 0, 0)`, hashA)
	p := filepath.Join(e.movies, "Movie (2001)", "Movie (2001).mkv")
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.Symlink(filepath.Join(e.mount, "__all__", "Movie.Release", "movie.mkv"), p); err != nil {
		t.Fatal(err)
	}
	if err := os.Lchown(p, 1234, 2345); err != nil {
		t.Fatal(err)
	}
	if _, err := e.lib.Relink(e.ctx, "", false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	tg, _ := os.Readlink(p)
	if !strings.Contains(tg, "{abcdef}") || st.Uid != 1234 || st.Gid != 2345 {
		t.Errorf("target %q owner %d:%d", tg, st.Uid, st.Gid)
	}
}

func TestDanglingReport(t *testing.T) {
	t.Parallel()
	// Library disabled: the report still runs (it only reads) and writes nothing to disk.
	e := newEnv(t, false, policy.ModeShadow)
	downloads := filepath.Join(e.dir, "downloads")
	e.pol.Library.Dangling = policy.LibraryDangling{Roots: []string{filepath.Join(e.dir, "media"), downloads}}
	e.lib = New(Options{Store: e.st, Policy: e.pol, Now: func() time.Time { return e.now }})
	e.release("Here", map[string]float64{"a.mkv": 1})
	links := map[string]string{
		filepath.Join(e.movies, "Good (2001)", "Good.mkv"): "Here/a.mkv",
		filepath.Join(e.movies, "Bad (2001)", "Bad.mkv"):   "Gone/b.mkv",
		filepath.Join(e.dir, "media", "arr-lib", "x.mkv"):  "Lost/x.mkv",
		filepath.Join(downloads, "sonarr", "rel", "y.mkv"): "Here/missing.mkv",
		filepath.Join(downloads, "other", "not-zurg.mkv"):  "/elsewhere/file.mkv",
	}
	for p, tg := range links {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if !filepath.IsAbs(tg) {
			tg = filepath.Join(e.mount, "__all__", tg)
		}
		if err := os.Symlink(tg, p); err != nil {
			t.Fatal(err)
		}
	}
	before := tree(t, e.dir)
	sum, err := e.lib.DanglingReport(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Dangling != 3 || sum.Links != 4 || len(sum.Errors) != 0 {
		t.Errorf("summary %+v", sum)
	}
	if after := tree(t, e.dir); strings.Join(before, "|") != strings.Join(after, "|") {
		t.Error("the report wrote to the filesystem")
	}
	rows, last, err := Dangling(e.ctx, e.st.Reader())
	if err != nil || len(rows) != 3 || last.Dangling != 3 {
		t.Fatalf("rows %+v last %+v err %v", rows, last, err)
	}
	for _, r := range rows {
		switch {
		case strings.HasSuffix(r.Path, "Bad.mkv"):
			if r.Version != "movie-remux" || r.Folder != "Gone" {
				t.Errorf("version root link %+v", r)
			}
		case strings.HasSuffix(r.Path, "x.mkv"), strings.HasSuffix(r.Path, "y.mkv"):
			if r.Version != "" {
				t.Errorf("extra root link has a version: %+v", r)
			}
		default:
			t.Errorf("unexpected %+v", r)
		}
	}
	// Fixed links leave the report; the others keep their first sighting.
	first := rows[0].FirstSeen
	e.release("Gone", map[string]float64{"b.mkv": 1})
	e.now = e.now.Add(6 * time.Hour)
	if sum, err = e.lib.DanglingReport(e.ctx); err != nil || sum.Dangling != 2 {
		t.Fatalf("second report %+v %v", sum, err)
	}
	rows, _, _ = Dangling(e.ctx, e.st.Reader())
	if len(rows) != 2 || !rows[0].FirstSeen.Equal(first) || !rows[0].LastSeen.Equal(e.now) {
		t.Errorf("rows after the fix %+v (first %v)", rows, first)
	}
	if n := e.one(`SELECT COUNT(*) FROM library_dangling_scan`); n != "2" {
		t.Errorf("scans %s", n)
	}
}

// A release folder whose name starts with ".." is inside the mount: its links are listed and reported like any other.
func TestDanglingReportDotDotFolder(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false, policy.ModeShadow)
	e.lib = New(Options{Store: e.st, Policy: e.pol, Now: func() time.Time { return e.now }})
	e.release("...And Justice for All 1979", map[string]float64{"a.mkv": 1})
	links := map[string]string{
		filepath.Join(e.movies, "Good (1979)", "Good.mkv"): "...And Justice for All 1979/a.mkv",
		filepath.Join(e.movies, "Bad (1979)", "Bad.mkv"):   "...Gone 1979/b.mkv",
	}
	for p, tg := range links {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.Symlink(filepath.Join(e.mount, "__all__", tg), p); err != nil {
			t.Fatal(err)
		}
	}
	sum, err := e.lib.DanglingReport(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Dangling != 1 || sum.Links != 2 {
		t.Errorf("summary %+v", sum)
	}
	if !escapes("..") || !escapes("../x") || escapes("...x") || escapes("..x/y") {
		t.Error("escapes")
	}
}
