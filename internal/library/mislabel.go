package library

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
)

// The mislabel guard is the second half of the sample and fake guard (sampleCheck): a file whose
// probe plainly contradicts its name's Remux or 2160p label is refused like a sample. Remux ranks over Bluray, so a
// re-encode named "BDRemux" would otherwise replace a real Bluray. Only clear signals refuse; a false positive throws
// a good file away. The rules (docs/design/library.md, "Mislabel guard"):
//   - a 2160p label on video at most 1080 lines tall (and under 3200 wide): an upscale label;
//   - a Remux label on an x264/x265 encode whose SEI shows CRF rate control: disc video is bitrate (VBV) targeted,
//     never CRF. x264/x265 without CRF is only noted (disc authoring uses x264 --bluray-compat and x265);
//   - a Remux label under the overall bitrate floor of its resolution (remuxFloor) is only noted: lean discs go lower;
//   - a 1080p Remux in HEVC or AV1 is only noted (UHD discs may carry 1080p HEVC).

// labelClaim is what a file's name (else the release title) claims.
type labelClaim struct {
	Resolution int
	Remux      bool
	// Source: the name names a source (WEB-DL, BluRay, HDTV, ...), not one the parser infers from a resolution alone.
	Source bool
	// Named: the name states its resolution, not one the parser infers (a bare BluRay REMUX reads as 1080p).
	Named bool
}

// Overall bitrate floors (file size over probed runtime, audio included) under which a Remux label is noted. Most
// discs sit far above: a 1080p Blu-ray remux rarely averages under ~15 Mb/s, a UHD remux under ~30; x264/x265 encodes
// live under the floors (1080p 2-10 Mb/s, 2160p fan encodes 8-15). Disc authoring allows 5 Mb/s video, so a lean
// honest disc can sit under a floor too: a note, never a refusal.
const (
	remuxFloor1080 = 6_000_000
	remuxFloor2160 = 15_000_000
)

func remuxFloor(res int) int64 {
	switch {
	case res >= 2160:
		return remuxFloor2160
	case res >= 1080:
		return remuxFloor1080
	}
	return 0
}

// claimOf parses a name's quality label for the guard.
func claimOf(name, kind string) labelClaim {
	if kind == "movie" {
		m := radarr.ParseQuality(name)
		return labelClaim{Resolution: m.Quality.Resolution, Remux: m.Quality.Modifier == radarr.ModifierREMUX,
			Source: m.SourceDetectionSource == radarr.DetectionName, Named: m.ResolutionDetectionSource == radarr.DetectionName}
	}
	m := sonarr.ParseQuality(name)
	return labelClaim{Resolution: m.Quality.Resolution, Remux: m.Quality.Source == sonarr.SourceBlurayRaw,
		Source: m.SourceDetectionSource == sonarr.DetectionName, Named: m.ResolutionDetectionSource == sonarr.DetectionName}
}

// fileClaim is the file name's label, else the release's: a pack's file named for another quality is judged as what
// its name says (a 1080p WEB-DL file in a 2160p remux pack is no lie). A name with a resolution but no source keeps
// the release's Remux claim (Amelie.2001.2160p.mkv in a 2160p BDRemux release is still a remux). A name without a
// resolution keeps the release's when it names no other source (Amelie.2001.BluRay.REMUX.mkv in a 2160p release is
// not the parser's inferred 1080p); a name with another source and no resolution claims none (Show.S01E05.HDTV.mkv in
// a 2160p remux pack makes no 2160p claim, so its 1080 lines are no lie).
func fileClaim(release labelClaim, f *PlannedFile, kind string) labelClaim {
	name := filepath.Base(f.Rel)
	name = strings.TrimSuffix(name, filepath.Ext(name)) // the parsers read .mkv alone as HDTV-720p
	c := claimOf(name, kind)
	if !c.Named {
		c.Resolution = release.Resolution
		if c.Source && !c.Remux {
			c.Resolution = 0
		}
	}
	switch {
	case c.Source || c.Remux:
		return c
	case c.Named:
		release.Resolution = c.Resolution
	}
	return release
}

// mislabelCheck returns why the file is refused, or a note of signals that do not refuse ("" for none).
func mislabelCheck(c labelClaim, f *PlannedFile) (refuse, note string) {
	if f.Probe == nil || f.Probe.Video == nil {
		return "", ""
	}
	v := f.Probe.Video
	if c.Resolution >= 2160 && v.Height > 0 && v.Height <= 1080 && v.Width < 3200 {
		return fmt.Sprintf("mislabelled 2160p: the video is %dx%d", v.Width, v.Height), ""
	}
	if !c.Remux {
		return "", ""
	}
	if strings.HasPrefix(v.EncoderRC, "crf") {
		return fmt.Sprintf("mislabelled remux: %s encode with %s (disc video is never CRF)", v.Encoder, v.EncoderRC), ""
	}
	var notes []string
	if v.Encoder != "" {
		rc := v.EncoderRC
		if rc == "" {
			rc = "not named"
		}
		notes = append(notes, fmt.Sprintf("%s encoder SEI, rate control %s", v.Encoder, rc))
	}
	if c.Resolution == 1080 && (v.Codec == "hevc" || v.Codec == "av1") {
		notes = append(notes, "1080p remux in "+strings.ToUpper(v.Codec))
	}
	if floor, rt := remuxFloor(c.Resolution), f.Probe.RunTimeSeconds; floor > 0 && f.Size > 0 && rt > 0 {
		if bps := float64(f.Size) * 8 / rt; bps < float64(floor) {
			notes = append(notes, fmt.Sprintf("%.1f Mb/s overall, under the %d Mb/s floor of a %dp disc",
				bps/1e6, floor/1e6, c.Resolution))
		}
	}
	if len(notes) == 0 {
		return "", ""
	}
	return "", "remux: " + strings.Join(notes, "; ")
}

// isMislabel reports whether a guard reason is the mislabel guard's.
func isMislabel(reason string) bool {
	return strings.HasPrefix(reason, "mislabelled ")
}
