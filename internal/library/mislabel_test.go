package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
)

// probed is a real-shaped probe: codec, frame size, and the encoder SEI MediaInfo would show.
func probed(codec string, w, h int, encoder, rc string) *probe.Result {
	return &probe.Result{Container: "matroska", RunTimeSeconds: 7200,
		Video: &probe.Video{Codec: codec, Width: w, Height: h, BitDepth: 10, DoviCompat: -1, Encoder: encoder, EncoderRC: rc},
		Audio: []probe.Audio{{Codec: "truehd", Channels: 8, Language: "eng"}}}
}

// mbps is the file size of a 7200 s file at that overall bitrate.
func mbps(m float64) int64 { return int64(m * 1e6 * 7200 / 8) }

func TestMislabelCheck(t *testing.T) {
	t.Parallel()
	uhdRemux := claimOf("Frankenstein.2025.2160p.UHD.BluRay.REMUX.DV.HDR.HEVC.TrueHD.Atmos-GRP", "movie")
	fhdRemux := claimOf("Frankenstein.2025.1080p.BluRay.REMUX.AVC.DTS-HD.MA.5.1-GRP", "movie")
	uhdWeb := claimOf("Frankenstein.2025.2160p.NF.WEB-DL.DDP5.1.HDR.H.265-GRP", "movie")
	if !uhdRemux.Remux || uhdRemux.Resolution != 2160 || !fhdRemux.Remux || fhdRemux.Resolution != 1080 || uhdWeb.Remux {
		t.Fatalf("claims %+v %+v %+v", uhdRemux, fhdRemux, uhdWeb)
	}
	if c := claimOf("Show.S01E01.2160p.UHD.BluRay.Remux.HEVC-GRP", "series"); !c.Remux || c.Resolution != 2160 {
		t.Fatalf("series claim %+v", c)
	}
	for _, tc := range []struct {
		name         string
		claim        labelClaim
		probe        *probe.Result
		size         int64
		refuse, note string
	}{
		// Refused.
		{"x265 fan cut named BDRemux", uhdRemux, probed("hevc", 3840, 1600, "x265", "crf=18.0"), mbps(25),
			"mislabelled remux: x265 encode with crf=18.0 (disc video is never CRF)", ""},
		{"x264 CRF named remux", fhdRemux, probed("h264", 1920, 1080, "x264", "crf=20.0"), mbps(20),
			"mislabelled remux: x264 encode with crf=20.0 (disc video is never CRF)", ""},
		{"upscale label", uhdRemux, probed("hevc", 1920, 1080, "", ""), mbps(40),
			"mislabelled 2160p: the video is 1920x1080", ""},
		{"upscale label on a WEB-DL", uhdWeb, probed("hevc", 1920, 800, "", ""), mbps(12),
			"mislabelled 2160p: the video is 1920x800", ""},
		// Noted only.
		{"x264 bluray-compat SEI", fhdRemux, probed("h264", 1920, 1080, "x264", "2pass"), mbps(28),
			"", "remux: x264 encoder SEI, rate control 2pass"},
		{"x265 SEI naming no rate control", uhdRemux, probed("hevc", 3840, 2160, "x265", ""), mbps(55),
			"", "remux: x265 encoder SEI, rate control not named"},
		{"1080p remux in HEVC", fhdRemux, probed("hevc", 1920, 1080, "", ""), mbps(30), "", "remux: 1080p remux in HEVC"},
		{"2160p remux at 14.9 Mb/s", uhdRemux, probed("hevc", 3840, 2160, "", ""), mbps(14.9),
			"", "remux: 14.9 Mb/s overall, under the 15 Mb/s floor of a 2160p disc"},
		{"2160p remux one byte under the floor", uhdRemux, probed("hevc", 3840, 2160, "", ""), mbps(15) - 1,
			"", "remux: 15.0 Mb/s overall, under the 15 Mb/s floor of a 2160p disc"},
		{"lean 1080p disc at 5.5 Mb/s", fhdRemux, probed("h264", 1920, 1080, "", ""), mbps(5.5),
			"", "remux: 5.5 Mb/s overall, under the 6 Mb/s floor of a 1080p disc"},
		{"x264 2pass at 4 Mb/s", fhdRemux, probed("h264", 1920, 1080, "x264", "2pass"), mbps(4),
			"", "remux: x264 encoder SEI, rate control 2pass; 4.0 Mb/s overall, under the 6 Mb/s floor of a 1080p disc"},
		// Honest.
		{"UHD HEVC remux", uhdRemux, probed("hevc", 3840, 2160, "", ""), mbps(62), "", ""},
		{"2160p remux exactly at the floor", uhdRemux, probed("hevc", 3840, 2160, "", ""), mbps(15), "", ""},
		{"scope UHD remux", uhdRemux, probed("hevc", 3840, 1600, "", ""), mbps(48), "", ""},
		{"AVC 1080p remux", fhdRemux, probed("h264", 1920, 1080, "", ""), mbps(29), "", ""},
		{"VC-1 1080p remux", fhdRemux, probed("vc1", 1920, 1080, "", ""), mbps(22), "", ""},
		{"1080p remux exactly at the floor", fhdRemux, probed("h264", 1920, 1080, "", ""), mbps(6), "", ""},
		{"x265 CRF WEB-DL is not a remux claim", uhdWeb, probed("hevc", 3840, 2160, "x265", "crf=18.0"), mbps(12), "", ""},
		{"no probe", uhdRemux, nil, 5, "", ""},
	} {
		f := &PlannedFile{Path: "/m/movie.mkv", Rel: "movie.mkv", Size: tc.size, Probe: tc.probe}
		if refuse, note := mislabelCheck(tc.claim, f); refuse != tc.refuse || note != tc.note {
			t.Errorf("%s: %q / %q\nwant %q / %q", tc.name, refuse, note, tc.refuse, tc.note)
		}
	}
}

// A pack's file is judged by its own name's label when it has one.
func TestMislabelFileClaim(t *testing.T) {
	t.Parallel()
	pack := claimOf("Show.S01.2160p.BluRay.REMUX-PACK", "series")
	web := &PlannedFile{Rel: "Show.S01E01.1080p.WEB-DL-FILE.mkv", Size: mbps(5), Probe: probed("h264", 1920, 1080, "", "")}
	if c := fileClaim(pack, web, "series"); c.Remux || c.Resolution != 1080 {
		t.Errorf("file claim %+v", c)
	}
	if refuse, _ := mislabelCheck(fileClaim(pack, web, "series"), web); refuse != "" {
		t.Errorf("a WEB-DL file in a remux pack refused: %s", refuse)
	}
	bare := &PlannedFile{Rel: "Show.S01E02.mkv", Size: mbps(5), Probe: probed("h264", 1920, 1080, "", "")}
	if c := fileClaim(pack, bare, "series"); c != pack {
		t.Errorf("a bare name takes the release's claim: %+v", c)
	}
	// A name with only a resolution keeps the release's Remux claim: the CRF encode is refused.
	remux := claimOf("Amelie.2001.2160p.BDRemux.HEVC.TrueHD.7.1-FAN", "movie")
	resOnly := &PlannedFile{Rel: "Amelie.2001.2160p.mkv", Size: mbps(25), Probe: probed("hevc", 3840, 1600, "x265", "crf=18.0")}
	if c := fileClaim(remux, resOnly, "movie"); !c.Remux || c.Resolution != 2160 {
		t.Errorf("resolution-only movie file claim %+v", c)
	}
	if refuse, _ := mislabelCheck(fileClaim(remux, resOnly, "movie"), resOnly); refuse != "mislabelled remux: x265 encode with crf=18.0 (disc video is never CRF)" {
		t.Errorf("a resolution-only file in a remux release: %q", refuse)
	}
	ep := &PlannedFile{Rel: "Show.S01E03.1080p.mkv"}
	if c := fileClaim(pack, ep, "series"); !c.Remux || c.Resolution != 1080 {
		t.Errorf("resolution-only episode claim %+v", c)
	}
	// A name with a source but no resolution keeps the release's 2160p: the parser's inferred 1080p is no claim.
	noRes := &PlannedFile{Rel: "Amelie.2001.BluRay.REMUX.mkv", Size: mbps(40), Probe: probed("h264", 1920, 1080, "", "")}
	if c := fileClaim(remux, noRes, "movie"); !c.Remux || c.Resolution != 2160 {
		t.Errorf("source-only movie file claim %+v", c)
	}
	if refuse, _ := mislabelCheck(fileClaim(remux, noRes, "movie"), noRes); refuse != "mislabelled 2160p: the video is 1920x1080" {
		t.Errorf("a 1080-line source-only file in a 2160p release: %q", refuse)
	}
	if c := fileClaim(pack, &PlannedFile{Rel: "Show.S01E04.BluRay.REMUX.mkv"}, "series"); !c.Remux || c.Resolution != 2160 {
		t.Errorf("source-only episode claim %+v", c)
	}
	// Another source without a resolution claims no resolution: a 1080-line HDTV file in a 2160p remux pack is no lie.
	hdtvEp := &PlannedFile{Rel: "Show.S01E05.HDTV.mkv", Size: mbps(8), Probe: probed("h264", 1920, 1080, "", "")}
	if c := fileClaim(pack, hdtvEp, "series"); c.Remux || c.Resolution != 0 {
		t.Errorf("source-only HDTV episode claim %+v", c)
	}
	if refuse, _ := mislabelCheck(fileClaim(pack, hdtvEp, "series"), hdtvEp); refuse != "" {
		t.Errorf("a 1080-line HDTV file in a 2160p remux pack refused: %q", refuse)
	}
	hdtv := &PlannedFile{Rel: "Amelie.2001.2160p.HDTV.mkv"}
	if c := fileClaim(remux, hdtv, "movie"); c.Remux {
		t.Errorf("a file naming its source is judged by it: %+v", c)
	}
}

// size makes every file of a release folder size bytes (sparse).
func (e *env) size(folder string, size int64) {
	e.t.Helper()
	dir := filepath.Join(e.mount, "__all__", folder)
	entries, err := os.ReadDir(dir)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, de := range entries {
		if err := os.Truncate(filepath.Join(dir, de.Name()), size); err != nil {
			e.t.Fatal(err)
		}
	}
}

// The x265 fan cut named "BDRemux" offered as an upgrade over a real Bluray: refused like a sample, blocklisted for
// the version with the signal as the reason (the poll then queues the re-search, router TestBlocklistedFailuresResearch),
// and the Bluray stays.
func TestImportRefusesMislabelledRemux(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	folder := filepath.Join(e.movies, "Amelie The Movie (2001)")
	held := filepath.Join(folder, "held.mkv")
	target := filepath.Join(e.mount, "__all__", "Held", "held.mkv")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, held); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, kind, title_id, release_title, quality, state, created_at, updated_at)
		VALUES (1, ?, ?, 'symlink', 1, 'Amelie.2001.2160p.UHD.BluRay.TrueHD-OLD', 'Bluray-2160p', 'active', 0, 0)`, held, target)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
	rel := "Amelie.2001.2160p.BDRemux.HEVC.TrueHD.7.1-FAN"
	e.release(rel, map[string]float64{"movie.mkv": 7000})
	e.size(rel, 30<<30)
	p := filepath.Join(e.mount, "__all__", rel, "movie.mkv")
	e.probes[p].Video.Encoder, e.probes[p].Video.EncoderRC = "x265", "crf=18.0"
	e.download(1, "movie-remux", 1, 10, rel, rel, "[]")
	before := tree(t, e.movies)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "mislabelled remux: x265 encode with crf=18.0 (disc video is never CRF)"
	if len(plan.Links) != 0 || len(plan.Rejected) != 1 || plan.Rejected[0].Reason != want {
		t.Fatalf("plan %+v", plan)
	}
	if got := e.one(`SELECT state || '|' || error FROM download WHERE id = 1`); got != "failed|library: "+want {
		t.Errorf("download %s", got)
	}
	if got := e.one(`SELECT state || '|' || last_error FROM library_import WHERE download_id = 1`); got != "rejected|"+want {
		t.Errorf("import %s", got)
	}
	if got := e.one(`SELECT version_key || '|' || info_hash || '|' || source || '|' || reason || '|' ||
		(researched_at IS NULL) FROM router_blocklist`); got != "movie-remux|"+hashA+"|library|"+want+"|1" {
		t.Errorf("blocklist %s", got)
	}
	if after := tree(t, e.movies); strings.Join(before, "|") != strings.Join(after, "|") {
		t.Errorf("the refused import changed the library:\n%v\n%v", before, after)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "active" {
		t.Errorf("the Bluray's link %s", got)
	}
}

// Honest remuxes are linked; a signal that does not refuse is noted on the file in the plan (the journal shows it).
func TestImportMislabelGuardPassesHonestRemux(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, release string
		w, h          int
		codec, enc    string
		rc, note      string
		size          int64
	}{
		{"UHD HEVC remux", movieRelease, 3840, 2160, "hevc", "", "", "", 55 << 30},
		{"AVC 1080p remux", "Amelie.2001.1080p.BluRay.REMUX.AVC.DTS-HD.MA.5.1-GRP", 1920, 1080, "h264", "", "", "", 30 << 30},
		{"x264 bluray-compat", "Amelie.2001.1080p.BluRay.REMUX.AVC.DTS-HD.MA.5.1-GRP", 1920, 1080, "h264", "x264", "2pass",
			"remux: x264 encoder SEI, rate control 2pass", 25 << 30},
		{"1080p HEVC remux", "Amelie.2001.1080p.UHD.BluRay.REMUX.HEVC.DTS-HD.MA.5.1-GRP", 1920, 1080, "hevc", "", "",
			"remux: 1080p remux in HEVC", 25 << 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.release(tc.release, map[string]float64{"movie.mkv": 7000})
			e.size(tc.release, tc.size)
			e.videoSize(tc.release, tc.w, tc.h)
			p := e.probes[filepath.Join(e.mount, "__all__", tc.release, "movie.mkv")]
			p.Video.Codec, p.Video.Encoder, p.Video.EncoderRC = tc.codec, tc.enc, tc.rc
			e.download(1, "movie-remux", 1, 10, tc.release, tc.release, "[]")
			plan, err := e.lib.Import(e.ctx, 1, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Links) != 1 || len(plan.Files) != 1 || plan.Files[0].Note != tc.note {
				t.Fatalf("plan links %+v files %+v", plan.Links, plan.Files)
			}
			if got := e.one(`SELECT count(*) FROM router_blocklist`); got != "0" {
				t.Errorf("blocklisted %s", got)
			}
			if tc.note != "" && !strings.Contains(e.one(`SELECT plan FROM library_import WHERE download_id = 1`), `"note":"`+tc.note) {
				t.Error("the note is not in the stored plan")
			}
		})
	}
}

// A 2160p pack whose episodes are 1080p upscale labels is refused, by the series path too; a client's explicit files
// (manual import) are only noted.
func TestImportMislabelSeriesAndManual(t *testing.T) {
	t.Parallel()
	pack := "Show.S01.2160p.UHD.BluRay.Remux.HEVC-PACK"
	setup := func(t *testing.T) *env {
		e := newEnv(t, true, policy.ModeActive)
		e.release(pack, map[string]float64{"Show.S01E01.mkv": 2700, "Show.S01E02.mkv": 2700})
		e.size(pack, 20<<30)
		e.videoSize(pack, 1920, 1080)
		e.download(1, "tv", 2, 20, pack, pack, "[201, 202]")
		return e
	}
	e := setup(t)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 0 || len(plan.Rejected) != 2 || !isMislabel(plan.Rejected[0].Reason) {
		t.Fatalf("plan %+v", plan)
	}
	if got := e.one(`SELECT reason FROM router_blocklist`); got != "mislabelled 2160p: the video is 1920x1080" {
		t.Errorf("blocklist %s", got)
	}

	e = setup(t)
	e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, how, updated_at)
		VALUES (1, 'Show.S01E01.mkv', '[201]', 'manual', 'manual', 0)`)
	plan, err = e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 1 || !strings.HasPrefix(plan.Files[0].Note, "not refused (manual import): mislabelled 2160p") {
		t.Fatalf("manual plan links %+v files %+v", plan.Links, plan.Files)
	}
}

// A pack whose E01 is an honest UHD file and whose E02 is a 1080-line file named 2160p. E01 links; E02 is
// refused, recorded with its episode for the journal, and stays wanted. The pack is not blocklisted (the blocklist
// is per release, and E01 came from it), so no re-search is queued: the wanted loop may pick the pack again.
func TestImportMislabelPartPack(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	pack := "Show.S01.2160p.UHD.BluRay.Remux.HEVC-PACK"
	e.release(pack, map[string]float64{"Show.S01E01.mkv": 2700, "Show.S01E02.mkv": 2700})
	e.size(pack, 20<<30)
	p := e.probes[filepath.Join(e.mount, "__all__", pack, "Show.S01E02.mkv")]
	p.Video.Width, p.Video.Height = 1920, 1080
	e.download(1, "tv", 2, 20, pack, pack, "[201, 202]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 1 || len(plan.Rejected) != 1 || plan.Rejected[0].Reason != "mislabelled 2160p: the video is 1920x1080" ||
		strings.Join(plan.Rejected[0].Episodes, ",") != "S01E02" {
		t.Fatalf("plan links %+v rejected %+v", plan.Links, plan.Rejected)
	}
	if got := e.one(`SELECT count(*) FROM router_blocklist`); got != "0" {
		t.Errorf("blocklisted %s", got)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "imported" {
		t.Errorf("download %s", got)
	}
	if got := e.one(`SELECT plan FROM library_import WHERE download_id = 1`); !strings.Contains(got, `"episodes":["S01E02"]`) {
		t.Errorf("stored plan %s", got)
	}
	if got := e.one(`SELECT count(*) FROM link_episode WHERE episode_id = 202`) + " " +
		e.one(`SELECT COALESCE((SELECT state FROM episode_version_state WHERE version_id = 20 AND episode_id = 202), '')`); got != "0 " {
		t.Errorf("E02 links and state %q", got)
	}
}
