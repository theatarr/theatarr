package policy

// Quality names as the arrs spell them, so profiles read like arr profiles and TRaSH profile JSON maps onto them.
// Radarr: src/NzbDrone.Core/Qualities/Quality.cs @ a96bf7c. Sonarr: src/NzbDrone.Core/Qualities/Quality.cs @ cab419a.

var radarrQualities = setOf(
	"Unknown", "WORKPRINT", "CAM", "TELESYNC", "TELECINE", "DVDSCR", "REGIONAL", "SDTV", "DVD", "DVD-R",
	"HDTV-720p", "HDTV-1080p", "HDTV-2160p",
	"WEBDL-480p", "WEBDL-720p", "WEBDL-1080p", "WEBDL-2160p",
	"WEBRip-480p", "WEBRip-720p", "WEBRip-1080p", "WEBRip-2160p",
	"Bluray-480p", "Bluray-576p", "Bluray-720p", "Bluray-1080p", "Bluray-2160p",
	"Remux-1080p", "Remux-2160p", "BR-DISK", "Raw-HD",
)

var sonarrQualities = setOf(
	"Unknown", "SDTV", "DVD",
	"HDTV-720p", "HDTV-1080p", "HDTV-2160p", "Raw-HD",
	"WEBDL-480p", "WEBDL-720p", "WEBDL-1080p", "WEBDL-2160p",
	"WEBRip-480p", "WEBRip-720p", "WEBRip-1080p", "WEBRip-2160p",
	"Bluray-480p", "Bluray-576p", "Bluray-720p", "Bluray-1080p", "Bluray-2160p",
	"Bluray-1080p Remux", "Bluray-2160p Remux",
)

func setOf(s ...string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

// qualitiesFor returns the quality names valid for a kind of title.
func qualitiesFor(k Kind) map[string]bool {
	if k == KindSeries {
		return sonarrQualities
	}
	return radarrQualities
}
