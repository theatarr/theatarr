package web

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
)

// mediaProbe is Theatarr's own probe of a 4K HDR10 file with two audio tracks and two subtitles.
const mediaProbe = `{"container":"matroska","runtime_seconds":7384,"video":{"codec":"hevc","width":3840,"height":2160,
	"bit_depth":10,"primaries":"bt2020","transfer":"smpte2084","dovi_compat":-1,"mastering":true},
	"audio":[{"codec":"eac3","channels":6,"language":"eng"},{"codec":"aac","channels":2,"language":"jpn"}],
	"subtitles":["eng","fre"]}`

// wantProbeMedia checks the media the title page shows for mediaProbe.
func wantProbeMedia(t *testing.T, m *MediaInfo) {
	t.Helper()
	if m == nil || m.VideoCodec != "h265" || m.VideoBitDepth != 10 || m.VideoDynamicRange != "HDR10" || m.AudioCodec != "EAC3" ||
		m.AudioChannels != 5.1 || m.AudioLanguages != "eng/jpn" || m.Subtitles != "eng/fre" || m.Resolution != "3840x2160" ||
		m.RunTime != "2:03:04" {
		t.Fatalf("media %+v", m)
	}
}

// TestTitleMediaFromProbe: a movie version with a file Theatarr imported shows the media of its stored probe.
func TestTitleMediaFromProbe(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`DELETE FROM link WHERE id IN (SELECT link_id FROM link_version WHERE version_id = 1)`) // the seed's file: this test's link is the file
	e.exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES (1, 'aa', 'Alpha', 'present', 0, 0)`)
	e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes, probe) VALUES (1, 1, 'Alpha.mkv', 1000, ?)`, mediaProbe)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, size_bytes,
		state, created_at, updated_at) VALUES (1, 1, 1, 'movie-remux', 'aa', 'Alpha.2020.2160p.WEB-DL-GRP', 'dmm', 1000, 'imported', 0, 0)`)
	e.exec(`INSERT INTO link (id, path, target, content_file_id, title_id, download_id, release_title, state, created_at,
		updated_at) VALUES (1, '/movies/Alpha (2020)/Alpha.mkv', '/mount/Alpha.mkv', 1, 1, 1, 'Alpha.2020.2160p.WEB-DL-GRP',
		'active', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 1)`)
	var d TitleDetail
	e.call("GET", APIBase+"/titles/1", nil, 200, &d)
	if d.Versions[0].Current == nil {
		t.Fatalf("no current file: %+v", d.Versions[0])
	}
	wantProbeMedia(t, d.Versions[0].Current.Media)
}

// TestTitleMediaFromAdoptedLink: an adopted link (no download, no probe) shows the media info its arr had recorded.
func TestTitleMediaFromAdoptedLink(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`DELETE FROM link WHERE id IN (SELECT link_id FROM link_version WHERE version_id = 1)`) // the seed's file: this test's link is the file
	e.exec(`INSERT INTO link (id, path, target, title_id, release_title, adopted_from, adopted_media, state, created_at,
		updated_at) VALUES (1, '/theatarr/Alpha (2020)/Alpha.mkv', '/mount/Alpha.mkv', 1, 'Alpha.2020.2160p.WEB-DL-GRP',
		'radarr /movies/Alpha.mkv', '{"resolution":"3840x2160","videoCodec":"x265","videoDynamicRangeType":"DV HDR10",
		"audioCodec":"TrueHD Atmos","audioChannels":7.1}', 'active', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 1)`)
	var d TitleDetail
	e.call("GET", APIBase+"/titles/1", nil, 200, &d)
	c := d.Versions[0].Current
	if c == nil || c.Media == nil || c.Media.VideoCodec != "x265" || c.Media.VideoDynamicRange != "DV HDR10" ||
		c.Media.AudioCodec != "TrueHD Atmos" || c.Media.AudioChannels != 7.1 || c.Media.Resolution != "3840x2160" {
		t.Fatalf("current %+v", c)
	}
}

// TestEpisodeMediaFromProbe: an episode's file Theatarr imported shows the media of its stored probe.
func TestEpisodeMediaFromProbe(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES (1, 'aa', 'Show', 'present', 0, 0)`)
	e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes, probe) VALUES (1, 1, 'S01E01.mkv', 1000, ?)`, mediaProbe)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, size_bytes,
		state, created_at, updated_at) VALUES (1, 10, 10, 'tv-2160p', 'aa', 'The.Show.S01E01.2160p.WEB-DL-GRP', 'dmm', 1000, 'imported', 0, 0)`)
	e.exec(`DELETE FROM link WHERE id IN (900)`) // the seed's adopted files: this test's links replaced them
	e.exec(`INSERT INTO link (id, path, target, content_file_id, title_id, download_id, release_title, state, created_at,
		updated_at) VALUES (1, '/tv/Show/S01E01.theatarr.mkv', '/mount/S01E01.mkv', 1, 10, 1,
		'The.Show.S01E01.2160p.WEB-DL-GRP', 'active', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (1, 101)`)
	var ep EpisodeDetail
	e.call("GET", APIBase+"/titles/10/episodes/101", nil, 200, &ep)
	if f := ep.Versions[0].File; f == nil || f.LinkID != 1 {
		t.Fatalf("file %+v", ep.Versions[0].File)
	}
	wantProbeMedia(t, ep.Versions[0].File.Media)
}

// TestEpisodeListFileFacts: the episode list's cells carry the file's media (an adopted file's arr media info, or
// Theatarr's probe), and the languages and formats its link's metadata keeps, all from the newest link when an
// episode has two; a file with none of them has none; a household caller gets none of it.
func TestEpisodeListFileFacts(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	cells := func() (EpisodeCell, EpisodeCell) {
		var l EpisodeList
		e.call("GET", APIBase+"/titles/10/episodes", nil, 200, &l)
		var c1, c2 EpisodeCell
		for _, ep := range l.Episodes {
			switch ep.ID {
			case 101:
				c1 = ep.Versions["tv-2160p"]
			case 102:
				c2 = ep.Versions["tv-2160p"]
			}
		}
		return c1, c2
	}
	// The seed's adopted files: S01E01 with the arr's media info, S01E02 with none and no languages or formats.
	c1, c2 := cells()
	if m := c1.Media; m == nil || m.VideoCodec != "x265" || m.AudioCodec != "EAC3" || m.Resolution != "1920x1080" {
		t.Errorf("adopted S01E01 media %+v", c1.Media)
	}
	if !c2.HasFile || c2.Media != nil || c2.Languages != nil || c2.Formats != nil {
		t.Errorf("S01E02 %+v", c2)
	}
	// A newer file for S01E01 that Theatarr imported and probed; its lower id would come first without the order.
	e.exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES (1, 'aa', 'Show', 'present', 0, 0)`)
	e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes, probe) VALUES (1, 1, 'S01E01.mkv', 1000, ?)`, mediaProbe)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, size_bytes,
		state, created_at, updated_at) VALUES (1, 10, 10, 'tv-2160p', 'aa', 'The.Show.S01E01.2160p.WEB-DL-NEW', 'dmm', 1000, 'imported', 0, 0)`)
	e.exec(`INSERT INTO link (id, path, target, content_file_id, title_id, download_id, release_title, quality,
		file_metadata, state, created_at, updated_at) VALUES (1, '/tv/Show/S01E01.theatarr.mkv', '/mount/S01E01.mkv', 1, 10, 1,
		'The.Show.S01E01.2160p.WEB-DL-NEW', 'WEBDL-2160p', '{"ReleaseGroup":"NEW","Languages":["English","Japanese"],
		"Formats":[{"name":"HDR10","score":500},{"name":"WEB Tier 01","score":1700}]}', 'active', 2000,
		2000)`)
	// The older file's torrent is held on one provider, the newer one's on another: Cached on is the newer's alone.
	e.exec(`UPDATE link SET file_metadata = json_set(file_metadata, '$.InfoHash', ?) WHERE id = 900`, strings.Repeat("bb", 20))
	e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash, status, state, progress) VALUES
		('old-acct', 't1', ?, 'downloaded', 'ready', 100), ('new-acct', 't2', ?, 'downloaded', 'ready', 100)`,
		strings.Repeat("bb", 20), "aa")
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (1, 101)`)
	c1, _ = cells()
	wantProbeMedia(t, c1.Media)
	if c1.ReleaseGroup != "NEW" || c1.Quality != "WEBDL-2160p" || !slices.Equal(c1.Languages, []string{"English", "Japanese"}) ||
		len(c1.Formats) != 2 || c1.Formats[0].Name != "HDR10" || c1.Formats[0].Score != 500 || c1.Formats[1].Name != "WEB Tier 01" ||
		c1.InfoHash != "aa" || !slices.Equal(c1.CachedOn, []string{"new-acct"}) {
		t.Errorf("newest S01E01 %+v", c1)
	}
	// A household caller sees none of it.
	ur := httptest.NewRequest("GET", APIBase+"/titles/10/episodes", nil)
	ur.Header.Set(auth.APIKeyHeader, e.key)
	urec := httptest.NewRecorder()
	e.userMux().ServeHTTP(urec, ur)
	if b := urec.Body.String(); urec.Code != 200 || strings.Contains(b, `"media"`) || strings.Contains(b, `"languages"`) ||
		strings.Contains(b, `"formats"`) || strings.Contains(b, "x265") {
		t.Errorf("household %d %s", urec.Code, b)
	}
}
