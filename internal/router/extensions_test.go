package router

import (
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

// Sonarr v5 TorrentClientBaseFixture (2b7906724, 1c01b720a) with FailDownloads = [Executables, PotentiallyDangerous]
// and RejectedFileExtensionServiceFixture; the user-defined extension cases have no counterpart (no such list).
func TestTorrentFileRejection(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		files []string
		want  string
	}{
		{"should_blocklist_and_throw_when_torrent_contains_dangerous_file", []string{"Droned.S01E01.Pilot.1080p.WEB-DL-DRONE.exe"},
			"Caution:\nFound executables with extensions: .exe"},
		{"should_include_all_rejection_groups_in_blocklist_message (without the user list)",
			[]string{"Droned.S01E01.Pilot.1080p.WEB-DL-DRONE.mkv", "setup.exe", "payload.lnk", "info.nfo"},
			"Caution:\nFound potentially dangerous files with extensions: .lnk\nFound executables with extensions: .exe"},
		{"should_not_blocklist_when_torrent_contains_only_safe_files", []string{"Droned.S01E01.Pilot.1080p.WEB-DL-DRONE.mkv", "Sample/sample.mkv"}, ""},
		{"case and duplicates", []string{"a/x.EXE", "b/y.exe", "c/z.Scr", "d/run.sh", "e/no-extension", "f/dot."},
			"Caution:\nFound potentially dangerous files with extensions: .Scr\nFound executables with extensions: .EXE, .sh"},
		{"every dangerous extension", []string{"1.arj", "2.lnk", "3.lzh", "4.ps1", "5.scr", "6.vbs", "7.zipx", "8.zip", "9.rar"},
			"Caution:\nFound potentially dangerous files with extensions: .arj, .lnk, .lzh, .ps1, .scr, .vbs, .zipx"},
		{"every executable extension", []string{`Show\1.bat`, "2.cmd", "3.exe", "4.sh", "5.com", "6.msi"},
			"Caution:\nFound executables with extensions: .bat, .cmd, .exe, .sh"},
	} {
		if got := TorrentFileRejection(c.files); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestImportFileRejection(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name         string
		files        []string
		reason, want string
	}{
		{"should_not_return_rejection_for_media_files", []string{`C:\Test\Droned.S01E01.mkv`, `C:\Test\Droned.S01E01.srt`}, "", ""},
		{"should_not_return_rejection_for_files_without_an_extension", []string{`C:\Test\Droned.S01E01`}, "", ""},
		{"should_return_rejection_for_dangerous_file", []string{`C:\Test\Droned.S01E01.mkv`, `C:\Test\Droned.S01E01.lnk`},
			RejectDangerousFile, "Caution: Found potentially dangerous file with extension: .lnk"},
		{"should_return_rejection_for_executable_file", []string{"Droned.S01E01/Droned.S01E01.bat"},
			RejectExecutableFile, "Caution: Found executable file with extension: .bat"},
		{"dangerous before executable", []string{"a.exe", "b.vbs"}, RejectDangerousFile,
			"Caution: Found potentially dangerous file with extension: .vbs"},
	} {
		reason, msg := ImportFileRejection(c.files)
		if reason != c.reason || msg != c.want {
			t.Errorf("%s: %q %q, want %q %q", c.name, reason, msg, c.reason, c.want)
		}
	}
}

// A torrent with an executable is refused on the first read that lists its files: on Real-Debrid before any
// selection, on AllDebrid even when it is already downloaded. The download fails, its hash is blocklisted for the
// version, nothing is written to a provider, and the next route skips the hash.
func TestPollRefusesDangerousTorrents(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Outcome != OutcomeAdded || res.Provider != "rd" {
		t.Fatalf("route %+v %v", res, err)
	}
	e.pr["rd"].set("rd-aaaa", provider.Torrent{Status: "waiting_files_selection", State: provider.StateQueued,
		Name: "Movie.2024.2160p.WEB.H265-GRP", Files: []provider.File{{ID: "1", Path: "Movie/Movie.mkv", Bytes: 9000},
			{ID: "2", Path: "Movie/Codec/setup.exe", Bytes: 1}}})
	sum, err := e.r.Poll(e.ctx)
	if err != nil || sum.Selected != 0 || sum.Failed != 1 {
		t.Fatalf("poll %+v %v", sum, err)
	}
	if len(e.ad["rd"].selects) != 0 {
		t.Errorf("selected %v", e.ad["rd"].selects)
	}
	want := "Caution:\nFound executables with extensions: .exe"
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'failed' AND error = ?`, want); n != 1 {
		t.Error("download not failed with the reason")
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE version_key = 'movie' AND info_hash = ? AND
		source = ? AND reason = ? AND download_id IS NOT NULL AND title_id = 1`, h("a"), BlocklistTorrentFiles, want); n != 1 {
		t.Error("hash not blocklisted")
	}
	adds := len(e.ad["rd"].adds)
	res, err = e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Outcome == OutcomeAdded || len(e.ad["rd"].adds) != adds || !strings.Contains(steps(res), "blocklisted") {
		t.Errorf("blocklisted hash routed again: %+v %v (%s)", res, err, steps(res))
	}

	// AllDebrid, already downloaded: refused before it becomes ready, so the library never sees it.
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
		provider, provider_torrent_id, state, created_at, updated_at) VALUES (50, 1, 10, 'movie', ?, 'y', 'pub',
		'ad', 'A9', 'added', 0, 0)`, h("b"))
	e.pr["ad"].set("A9", provider.Torrent{Status: "Ready", State: provider.StateDownloaded, Progress: 100,
		Files: []provider.File{{Path: "Movie/Movie.mkv"}, {Path: "Movie/Movie.lnk"}}})
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 50 AND state = 'failed'`); n != 1 {
		t.Error("AllDebrid download with a .lnk should fail")
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE info_hash = ?`, h("b")); n != 1 {
		t.Error("AllDebrid hash not blocklisted")
	}
	if e.ad["ad"].writes() != 0 {
		t.Error("refusing wrote to AllDebrid")
	}
}

func TestPollRejectsDifferentProviderTorrentID(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO download (id,title_id,version_id,version_key,info_hash,release_title,indexer,provider,provider_torrent_id,state,created_at,updated_at) VALUES (77,1,10,'movie',?,'Movie','pub','rd','T77','added',0,0)`, h("a"))
	e.pr["rd"].set("T77", provider.Torrent{Hash: h("b"), State: provider.StateDownloading, Status: "downloading"})
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	var state, msg string
	if err := e.st.Reader().QueryRow(`SELECT state,error FROM download WHERE id=77`).Scan(&state, &msg); err != nil {
		t.Fatal(err)
	}
	if state != StateFailed || msg != "provider returned a different torrent for this ID" {
		t.Fatalf("%s %s", state, msg)
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Fatalf("blocklisted %d", n)
	}
}
