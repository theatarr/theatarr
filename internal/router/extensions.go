package router

// Port of Sonarr v5 (develop @ a6171f0ca) src/NzbDrone.Core/MediaFiles/FileExtensions.cs (DangerousExtensions,
// ExecutableExtensions), src/NzbDrone.Core/Download/TorrentClientBase.cs (ValidateFileNames: grab time, PR #8775
// 2b7906724) and src/NzbDrone.Core/MediaFiles/EpisodeImport/RejectedFileExtensionService.cs (import time), with the
// indexer default FailDownloads = [Executables, PotentiallyDangerous] of PR #9093 (1c01b720a).
//
// Deviations:
//   - The check is always on, for every indexer: v5's default for torznab, newznab, nyaa and torrent RSS indexers.
//     There is no per-indexer FailDownloads setting and no user-defined rejected extensions list.
//   - v5 checks a .torrent file's list before the add and never checks magnets. Theatarr adds magnets only, so the router
//     checks the provider's file list on the first read after the add (Real-Debrid: before any file is selected).
//     The release is refused and blocklisted as v5 does; the torrent stays on the provider (nothing is deleted).

import (
	"path"
	"strings"
)

// dangerousExtensions is FileExtensions.DangerousExtensions.
var dangerousExtensions = map[string]bool{".arj": true, ".lnk": true, ".lzh": true, ".ps1": true, ".scr": true,
	".vbs": true, ".zipx": true}

// executableExtensions is FileExtensions.ExecutableExtensions.
var executableExtensions = map[string]bool{".bat": true, ".cmd": true, ".exe": true, ".sh": true}

// Import rejection reasons (ImportRejectionReason).
const (
	RejectDangerousFile  = "DangerousFile"
	RejectExecutableFile = "ExecutableFile"
)

// Blocklist source of TorrentFileRejection: v5's "TorrentFileValidation".
const BlocklistTorrentFiles = "torrent_files"

// extension is Path.GetExtension: the last element's suffix from its last dot, "" when there is none or the name
// ends with the dot.
func extension(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	e := path.Ext(p)
	if e == "." {
		return ""
	}
	return e
}

// distinctExt collects the extensions of paths that are in set, once each ignoring case, in order of first sight
// (a .NET HashSet with OrdinalIgnoreCase keeps the first spelling).
func distinctExt(paths []string, set map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		e := extension(p)
		k := strings.ToLower(e)
		if e == "" || !set[k] || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

// TorrentFileRejection is TorrentClientBase.ValidateFileNames with FailDownloads = [Executables,
// PotentiallyDangerous]: the blocklist message when a torrent holds any potentially dangerous or executable file,
// "" otherwise. Any such file refuses the whole release, whatever else it holds.
func TorrentFileRejection(paths []string) string {
	var rejections []string
	if d := distinctExt(paths, dangerousExtensions); len(d) > 0 {
		rejections = append(rejections, "Found potentially dangerous files with extensions: "+strings.Join(d, ", "))
	}
	if x := distinctExt(paths, executableExtensions); len(x) > 0 {
		rejections = append(rejections, "Found executables with extensions: "+strings.Join(x, ", "))
	}
	if len(rejections) == 0 {
		return ""
	}
	return "Caution:\n" + strings.Join(rejections, "\n")
}

// ImportFileRejection is RejectedFileExtensionService.GetRejection: the reason and message when the files hold a
// potentially dangerous file (checked first) or an executable, "" otherwise. v5 asks it when a download has no
// video file to import.
func ImportFileRejection(paths []string) (reason, message string) {
	if d := distinctExt(paths, dangerousExtensions); len(d) > 0 {
		return RejectDangerousFile, "Caution: Found potentially dangerous file with extension: " + strings.Join(d, ", ")
	}
	if x := distinctExt(paths, executableExtensions); len(x) > 0 {
		return RejectExecutableFile, "Caution: Found executable file with extension: " + strings.Join(x, ", ")
	}
	return "", ""
}
