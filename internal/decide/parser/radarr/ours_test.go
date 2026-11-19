package radarr

import "testing"

// TestWebTokens: ATVP before WEB and EAC3 or a separated DDP 5.1 after WEB read as WEB-DL (ours, Sonarr v5's source
// rule); DD5.1, DDP5.1 and an explicit WEBRip keep their reading.
func TestWebTokens(t *testing.T) {
	t.Parallel()
	for title, want := range map[string]string{
		"Movie.2024.2160p.HDR.WEB.EAC3-GRP":            "WEBDL-2160p",
		"Movie.2024.2160p.ATVP.WEB.DDP.5.1.DV.HDR-GRP": "WEBDL-2160p",
		"Movie.2024.2160p.ATVP.WEB.DV.HDR-GRP":         "WEBDL-2160p",
		"Movie.2024.2160p.HDR.WEB.DDP.5.1-GRP":         "WEBDL-2160p",
		"Movie 2024 2160p HDR WEB DDP 5.1 GRP":         "WEBDL-2160p",
		"Movie.2024.2160p.HDR.WEB.DD5.1-GRP":           "WEBDL-2160p",
		"Movie.2024.2160p.HDR.WEB.DDP5.1-GRP":          "WEBDL-2160p",
		"Movie.2024.2160p.ATVP.WEBRip.DDP.5.1-GRP":     "WEBRip-2160p",
		"Movie.2024.2160p.HDR.WEBRip.EAC3-GRP":         "WEBRip-2160p",
	} {
		if q := ParseQuality(title).Quality.Name; q != want {
			t.Errorf("%s: %s, want %s", title, q, want)
		}
	}
}
