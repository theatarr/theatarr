// Package nativeapi holds the request and result types of the native decision and import APIs (POST
// /api/theatarr/v1/decide and /import), shared by internal/arrview, which serves them, and internal/web, which routes them.
package nativeapi

import (
	"errors"

	"github.com/theatarr/theatarr/internal/library"
)

// ErrBadRequest marks an error that is the caller's.
var ErrBadRequest = errors.New("bad request")

// DecideRequest asks for the decision on one release name (or file name) for a version of a title.
type DecideRequest struct {
	TitleID int64  `json:"title_id"`
	Version string `json:"version"`
	// Release is the release or file name.
	Release string `json:"release"`
	// Indexer is a policy indexer key; empty means unknown.
	Indexer  string `json:"indexer,omitempty"`
	InfoHash string `json:"info_hash,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Seeders  *int   `json:"seeders,omitempty"`
	// Languages and Flags are the source's (torznab languages, indexer flags), when known.
	Languages []string `json:"languages,omitempty"`
	Flags     []string `json:"flags,omitempty"`
}

// ImportRequest hands a finished download to the library for a version of a title: the release is found
// on the mount by its info hash and names (or at Path), probed, guarded and linked with the version's naming,
// exactly as a download the router placed.
type ImportRequest struct {
	TitleID int64  `json:"title_id"`
	Version string `json:"version"`
	// InfoHash is the torrent's v1 hash (the mount folder is found by it); Path is the release folder, a folder or
	// file in it, on the mount (as Theatarr reads it or as links carry it, or a symlink into it): the import takes that
	// folder, and only the files under Path. One of them is required.
	InfoHash string `json:"info_hash,omitempty"`
	Path     string `json:"path,omitempty"`
	// Release is the release title (default: the base name of Path). Quality, formats and score come from the
	// decision on it.
	Release string `json:"release,omitempty"`
	// Provider is the debrid provider holding it (rd, ad, tb), when known.
	Provider string `json:"provider,omitempty"`
	// Episodes limits a series import to these episode ids; empty: every episode the files name. With Path naming
	// one file, they are that file's episodes, whatever its name says.
	Episodes []int64 `json:"episodes,omitempty"`
	// Files, when given, are the only files imported, each with its episodes (a series) as the client chose them:
	// the paths must be in one release folder on the mount.
	Files []ImportFile `json:"files,omitempty"`
	// DryRun plans without writing anything.
	DryRun bool `json:"dry_run,omitempty"`
}

// ImportFile is one file a client chose, with its episode ids (a series; none for a movie).
type ImportFile struct {
	Path     string  `json:"path"`
	Episodes []int64 `json:"episodes,omitempty"`
}

// ImportResult is the library's plan, with what happened.
type ImportResult struct {
	Plan *library.Plan `json:"plan"`
	// Applied is true when the library linked the files; false for a dry run (asked, or the gate refused).
	Applied    bool   `json:"applied"`
	DownloadID int64  `json:"download_id,omitempty"`
	Refused    string `json:"refused,omitempty"`
}
