package indexer

import (
	"path"
	"regexp"
	"strings"
)

// The file selection on Real-Debrid takes the playable files (the router's Playable; docs/design/router.md "File
// selection on Real-Debrid"). Its rule lives here so the indexer can judge file names the same way (DMM's
// availability answer, CheckAvailability): the router imports this package, not the reverse.
//
// playableVideo follows DMM's isVideo, the list the cached sets on Real-Debrid were most often selected by: the video
// extensions it ships enabled plus its three audio ones (playableAudio). .vob and .iso are left out (DMM marks .vob
// "not yet confirmed working" on Real-Debrid and has no .iso; decypharr has no .iso either, and the library cannot
// stream an image). Subtitles are left out as both DMM and decypharr leave them: selecting them gives a set the cache
// was not filled with, and the library imports no subtitle files from a release.
var playableVideo = map[string]bool{".avi": true, ".flv": true, ".m2ts": true, ".m4v": true, ".mkv": true,
	".mov": true, ".mp4": true, ".mpg": true, ".mpeg": true, ".ts": true, ".webm": true, ".wmv": true}

// playableAudio is selected with the videos (DMM's isVideo takes them), never on its own: a torrent with audio and
// no video holds nothing to import.
var playableAudio = map[string]bool{".mp3": true, ".flac": true, ".m4a": true}

// sampleRE is DMM's sample and trailer rule (the words sample and trailer anywhere in the path).
var sampleRE = regexp.MustCompile(`(?i)\b(sample|trailer)\b`)

// PlayableVideo reports whether a path has one of the selection's video extensions.
func PlayableVideo(p string) bool { return playableVideo[strings.ToLower(path.Ext(p))] }

// PlayableAudio reports whether a path has one of the audio extensions selected with the videos.
func PlayableAudio(p string) bool { return playableAudio[strings.ToLower(path.Ext(p))] }

// SampleOrTrailer reports whether a path names a sample or trailer (DMM's rule).
func SampleOrTrailer(p string) bool { return sampleRE.MatchString(p) }

// RARBGPromo reports a file at the torrent's root named rarbg* (the RARBG.com.mp4 promo, RARBG.txt): DMM leaves it out.
func RARBGPromo(p string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimPrefix(p, "/")), "rarbg")
}

// SelectedVideos is the videos of paths that Real-Debrid's file selection takes: every video that is no sample or
// trailer, or the samples and trailers when nothing else is a video; a rarbg* promo never.
func SelectedVideos(paths []string) []string {
	var videos, samples []string
	for _, p := range paths {
		switch {
		case RARBGPromo(p) || !PlayableVideo(p):
		case SampleOrTrailer(p):
			samples = append(samples, p)
		default:
			videos = append(videos, p)
		}
	}
	if len(videos) == 0 {
		return samples
	}
	return videos
}

// RealDebridRefusedFile is the base name of the first video the selection takes (SelectedVideos) that Real-Debrid's
// name rule refuses (RealDebridRefuses, strictly: these are Real-Debrid's own names), or "" when there is none.
// Real-Debrid applies the rule to each selected file's own name when the file is read.
func RealDebridRefusedFile(paths []string) string {
	for _, p := range SelectedVideos(paths) {
		if name := path.Base(p); RealDebridRefuses(name, false) {
			return name
		}
	}
	return ""
}
