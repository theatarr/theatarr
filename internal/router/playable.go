package router

import (
	"path"
	"strings"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

// The file selection on Real-Debrid (docs/design/router.md "File selection on Real-Debrid").
//
// Real-Debrid caches a torrent per selected file set: its API documented instantAvailability as returning, per
// hash, "all file IDs variants", with "you must ask all file IDs from this array on /selectFiles to get instant
// downloading". One selection of a torrent can be cached while another is not. The set most likely cached is the
// one the most common tools select:
//
//   - Debrid Media Manager (src/utils/addMagnet.ts handleSelectFilesInRd) selects every file its isVideo accepts
//     (src/utils/selectable.ts), movie or series alike, and every file only when none is a video;
//   - decypharr (pkg/debrid/providers/realdebrid/realdebrid.go CheckStatus) selects every file its
//     ValidateFileAllowed accepts (internal/config/misc.go: video and audio extensions, samples left out);
//   - zurg's force_select_playable_files selects the playable files.
//
// So both a movie and a series get every playable file, never the non-playable ones: an nfo, a text file, an image
// or an executable selected with them can make Real-Debrid pack the torrent into one RAR, which cannot be streamed.
//
// The extensions, the sample rule and the rarbg promo are the indexer package's (indexer.PlayableVideo,
// PlayableAudio, SampleOrTrailer, RARBGPromo), which judges Real-Debrid's file names by the same rule.

// Playable is the selection a Real-Debrid torrent gets: every video that is no sample or trailer, with the audio
// files beside them. Samples and trailers count only when nothing else is a video. nil when no file is a video (an
// audio-only torrent included). Files without an id are skipped.
func Playable(files []provider.File) []provider.File {
	var videos, samples, audio []provider.File
	for _, f := range files {
		switch {
		case f.ID == "" || indexer.RARBGPromo(f.Path):
		case indexer.PlayableVideo(f.Path) && indexer.SampleOrTrailer(f.Path):
			samples = append(samples, f)
		case indexer.PlayableVideo(f.Path):
			videos = append(videos, f)
		case indexer.PlayableAudio(f.Path) && !indexer.SampleOrTrailer(f.Path):
			audio = append(audio, f)
		}
	}
	if len(videos) == 0 {
		videos = samples
	}
	if len(videos) == 0 {
		return nil
	}
	// Videos then audio would reorder the torrent's files; the selection keeps their order.
	take := make(map[string]bool, len(videos)+len(audio))
	for _, f := range append(videos, audio...) {
		take[f.ID] = true
	}
	out := make([]provider.File, 0, len(take))
	for _, f := range files {
		if take[f.ID] {
			out = append(out, f)
			delete(take, f.ID)
		}
	}
	return out
}

// presentVideo are the extensions that make a torrent hold a video for noVideo: the Real-Debrid selection's videos
// plus .vob and .iso, which only Real-Debrid's selection leaves out. AllDebrid and TorBox take every file of a
// torrent, and the library imports a DVD's .vob and a movie's .iso.
var presentVideo = map[string]bool{".vob": true, ".iso": true}

// HasVideo reports whether any file is a video (samples and trailers included), by path alone. It is the presence
// check for every provider: it needs no file ids (AllDebrid and TorBox files have none) and does not apply Real-
// Debrid's selection limits; whether Real-Debrid can select a set is Playable's.
func HasVideo(files []provider.File) bool {
	for _, f := range files {
		if (indexer.PlayableVideo(f.Path) || presentVideo[strings.ToLower(path.Ext(f.Path))]) && !indexer.RARBGPromo(f.Path) {
			return true
		}
	}
	return false
}

// PlayableIDs is Playable's file ids, in the torrent's order.
func PlayableIDs(files []provider.File) []string {
	p := Playable(files)
	if len(p) == 0 {
		return nil
	}
	ids := make([]string, len(p))
	for i, f := range p {
		ids[i] = f.ID
	}
	return ids
}

// ChooseFiles picks the files to select (provider file ids): every playable file (Playable), for a movie and a
// series alike. nil when no file is a video.
func ChooseFiles(_ policy.Kind, files []provider.File) []string { return PlayableIDs(files) }

// AltFiles is the one other selection worth an add when ChooseFiles' came back uncached under router.cached_only:
// a movie's largest playable video alone, the set tools that stream one file select (and the router selected for
// movies before). nil for a series (no other set holds the episodes; every file is what risks the RAR) and when it
// is the same set as ChooseFiles'.
func AltFiles(kind policy.Kind, files []provider.File) []string {
	if kind == policy.KindSeries {
		return nil
	}
	p := Playable(files)
	if len(p) < 2 {
		return nil
	}
	var best *provider.File
	for i := range p {
		if !indexer.PlayableVideo(p[i].Path) {
			continue
		}
		if best == nil || p[i].Bytes > best.Bytes {
			best = &p[i]
		}
	}
	if best == nil {
		return nil
	}
	return []string{best.ID}
}

// SameFiles reports whether two selections name the same file ids, in any order.
func SameFiles(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]int, len(a))
	for _, id := range a {
		set[id]++
	}
	for _, id := range b {
		if set[id] == 0 {
			return false
		}
		set[id]--
	}
	return true
}
