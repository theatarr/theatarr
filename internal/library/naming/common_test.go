package naming

import "testing"

func TestSimplifyToken(t *testing.T) {
	for _, tc := range []struct{ token, want string }{
		{"{Season}", "season"},
		{"season", "season"},
		{"{Series Clean_Title}", "seriescleantitle"},
		{"MediaInfo.Audio-Codec", "mediainfoaudiocodec"},
		{" \t\n\u0085\u00a0_{}-.", ""},
		{"Ée\u0301١\u203f\u200c\u200d", "ée\u0301١\u203f\u200c\u200d"},
		{"a\u0903\u20dd²😀b", "ab"},
	} {
		if got := simplifyToken(tc.token); got != tc.want {
			t.Errorf("simplifyToken(%q) = %q, want %q", tc.token, got, tc.want)
		}
	}
}

func TestSeasonFolderTokenNormalization(t *testing.T) {
	c := DefaultSeriesConfig()
	s := &Series{Title: "Show"}
	for _, format := range []string{"Volume {season:00}", "Volume {Season:00}", "Volume {SEASON:00}"} {
		c.SeasonFolderFormat = format
		if got, _ := SeasonFolder(&c, s, 1); got != "Volume 01" {
			t.Errorf("SeasonFolder(%q) = %q, want Volume 01", format, got)
		}
	}
}
