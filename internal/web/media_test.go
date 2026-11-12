package web

import (
	"testing"
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
	e.exec(`DELETE FROM version_file WHERE version_id = 1`) // a Theatarr library: links only
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
	e.exec(`DELETE FROM version_file WHERE version_id = 1`) // a Theatarr library: links only
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
