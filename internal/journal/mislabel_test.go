package journal

import (
	"strings"
	"testing"
)

// The library's mislabel guard: a note on a file it kept shows on the file and as an info check; a refusal as the
// rejected file and the guard check.
func TestImportMislabelSignals(t *testing.T) {
	e := newEnv(t)
	e.seedRoute()
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, last_error, plan, created_at, updated_at)
		VALUES (1, 'linked', '/m/Alpha', NULL, ?, ?, ?)`, `{"files":[{"path":"/m/Alpha/a.mkv","size":30000000000,
		"probe":{"container":"matroska","runtime_seconds":7200,"video":{"codec":"h264","width":1920,"height":1080,
		"encoder":"x264","encoder_rc":"2pass"}},"note":"remux: x264 encoder SEI, rate control 2pass"}]}`,
		msOf(testNow), msOf(testNow))
	g := e.get("dl:1:import")
	if g.Import == nil || len(g.Import.Files) != 1 || g.Import.Files[0].Note != "remux: x264 encoder SEI, rate control 2pass" {
		t.Fatalf("import %+v", g.Import)
	}
	found := false
	for _, f := range g.Flags {
		found = found || (f.Code == CheckGuard && strings.Contains(f.Reason, "noted a file: remux: x264 encoder SEI"))
	}
	if !found {
		t.Errorf("flags %+v", g.Flags)
	}
}

// guardFlags are the entry's library_guard reasons.
func guardFlags(g *Entry) []string {
	var out []string
	for _, f := range g.Flags {
		if f.Code == CheckGuard {
			out = append(out, f.Reason)
		}
	}
	return out
}

// A pack whose other files linked: every refused file's episodes are named, and the check says they stay wanted with
// no blocklist or re-search, and that the release can be picked again (a movie's says only that the rest linked).
func TestImportPartPackRefusal(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE title SET kind = 'series' WHERE id = 1`) // seedRoute's title, as a series pack
	seedPartPack(e)
	g := e.get("dl:1:import")
	flags := guardFlags(g)
	if len(flags) != 1 || !strings.Contains(flags[0], "mislabelled 2160p: the video is 1920x1080 (S01E02); "+
		"sample: 60s of 2700s (S01E03); the rest of the release linked") ||
		!strings.Contains(flags[0], "can pick this release again") {
		t.Errorf("flags %q", flags)
	}
	if g.Import == nil || len(g.Import.Files) != 5 || g.Import.Files[3].Source != "rejected: mislabelled 2160p: the video is 1920x1080 (S01E02)" ||
		g.Import.Files[4].Source != "rejected: sample: 60s of 2700s (S01E03)" {
		t.Errorf("import %+v", g.Import)
	}
}

// seedPartPack is an import of seedRoute's release that linked e01 and refused e02 and e03.
func seedPartPack(e *env) {
	e.seedRoute()
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, last_error, plan, created_at, updated_at)
		VALUES (1, 'linked', '/m/Alpha', NULL, ?, ?, ?)`, `{"files":[{"path":"/m/Alpha/e01.mkv","size":1},
		{"path":"/m/Alpha/e02.mkv","size":1,"reason":"mislabelled 2160p: the video is 1920x1080","episodes":["S01E02"]},
		{"path":"/m/Alpha/e03.mkv","size":1,"reason":"sample: 60s of 2700s","episodes":["S01E03"]}],
		"links":[{"path":"/l/e01.mkv","target":"/m/Alpha/e01.mkv"}],
		"rejected":[{"path":"/m/Alpha/e02.mkv","reason":"mislabelled 2160p: the video is 1920x1080","episodes":["S01E02"]},
		{"path":"/m/Alpha/e03.mkv","reason":"sample: 60s of 2700s","episodes":["S01E03"]}]}`,
		msOf(testNow), msOf(testNow))
	e.exec(`INSERT INTO link (path, target, title_id, download_id, state, created_at, updated_at)
		VALUES ('/l/e01.mkv', '/m/Alpha/e01.mkv', 1, 1, 'active', 0, 0)`)
}

// A movie's other files simply linked: no episode wording.
func TestImportPartMovieRefusal(t *testing.T) {
	e := newEnv(t)
	seedPartPack(e)
	if flags := guardFlags(e.get("dl:1:import")); len(flags) != 1 || !strings.HasSuffix(flags[0], "; the rest of the release linked") {
		t.Errorf("movie flags %q", flags)
	}
}

// A shadow dry run that would link one file and refuse another: nothing linked, blocklisted or searched.
func TestImportDryRunRefusal(t *testing.T) {
	e := newEnv(t)
	e.seedRoute()
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, last_error, plan, created_at, updated_at)
		VALUES (1, 'dry_run', '/m/Alpha', NULL, ?, ?, ?)`, `{"files":[{"path":"/m/Alpha/e01.mkv","size":1}],
		"links":[{"path":"/l/e01.mkv","target":"/m/Alpha/e01.mkv"}],
		"rejected":[{"path":"/m/Alpha/e02.mkv","reason":"mislabelled 2160p: the video is 1920x1080","episodes":["S01E02"]}]}`,
		msOf(testNow), msOf(testNow))
	flags := guardFlags(e.get("dl:1:import"))
	if len(flags) != 1 || flags[0] != "the library's guard would reject a file: mislabelled 2160p: the video is "+
		"1920x1080 (S01E02) (dry run: nothing was linked, blocklisted or searched)" {
		t.Errorf("flags %q", flags)
	}
}

// A failed import says blocklisted only when the library's blocklist row is there: a hold for
// review (here a naming collision next to the refusal) has none.
func TestImportRejectedBlocklist(t *testing.T) {
	plan := `{"files":[{"path":"/m/Alpha/a.mkv","size":1,"reason":"sample: 60s of 7200s"}],
		"rejected":[{"path":"/m/Alpha/a.mkv","reason":"sample: 60s of 7200s"}]}`
	for _, blocklisted := range []bool{true, false} {
		e := newEnv(t)
		e.seedRoute()
		e.exec(`INSERT INTO library_import (download_id, state, mount_folder, last_error, plan, created_at, updated_at)
			VALUES (1, 'rejected', '/m/Alpha', 'no file passed the sample guard', ?, ?, ?)`, plan, msOf(testNow), msOf(testNow))
		want := "the library's guard rejected a file: sample: 60s of 7200s"
		if blocklisted {
			e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source,
				download_id, created_at) VALUES ('movie-remux', 'title:x', 1, 'Alpha', 'no file passed the sample guard',
				'library', 1, 0)`)
			want += " (the release was blocklisted)"
		}
		if flags := guardFlags(e.get("dl:1:import")); len(flags) != 1 || flags[0] != want {
			t.Errorf("blocklisted %v: flags %q", blocklisted, flags)
		}
	}
}

// A file skipped as not an upgrade (no episodes recorded) next to a linked one is no partly accepted pack, and no
// guard refusal: nothing says it was blocklisted.
func TestImportNotUpgradeIsNotPartPack(t *testing.T) {
	e := newEnv(t)
	e.seedRoute()
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, last_error, plan, created_at, updated_at)
		VALUES (1, 'linked', '/m/Alpha', NULL, ?, ?, ?)`, `{"files":[{"path":"/m/Alpha/e01.mkv","size":1}],
		"links":[{"path":"/l/e02.mkv","target":"/m/Alpha/e02.mkv"}],
		"rejected":[{"path":"/m/Alpha/e01.mkv","reason":"not an upgrade","not_upgrade":true}]}`,
		msOf(testNow), msOf(testNow))
	e.exec(`INSERT INTO link (path, target, title_id, download_id, state, created_at, updated_at)
		VALUES ('/l/e02.mkv', '/m/Alpha/e02.mkv', 1, 1, 'active', 0, 0)`)
	for _, f := range e.get("dl:1:import").Flags {
		if strings.Contains(f.Reason, "rest of the release linked") || strings.Contains(f.Reason, "blocklisted") {
			t.Errorf("flag %+v", f)
		}
	}
}
