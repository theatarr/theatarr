package library

import (
	"context"
	"database/sql"
	"path"
	"path/filepath"
	"slices"
)

// decypharr as the mount (library.mount.decypharr, Options.Decypharr), in mount mode: the mount is read as any
// other, and decypharr's API adds two things. Its name for a download's hash is tried first as the release folder,
// and a download's own file list filters the folder's files: decypharr serves same-named torrents from one folder,
// so a folder can hold another torrent's files. The list filters, it never gates: without a list the import goes by
// name as on a plain mount.

// TorrentFile is one file of a download's torrent as its source lists it: a provider's selected file, or a file of
// decypharr's own list. Name may be a path; files are matched by its last element and the exact size.
type TorrentFile struct {
	Name string
	Size int64
}

// TorrentFilesFunc reads the selected files a provider holds for a download's torrent: the provider's policy key,
// the torrent id the download recorded and its info hash. ok is false when there is no list (the provider is not
// set up, the read failed, the torrent is another hash's, or it lists no files).
type TorrentFilesFunc func(ctx context.Context, provider, torrentID, hash string) (files []TorrentFile, ok bool)

// inherit carries prev's index, its failure clock and loading answer into d, a client for the same decypharr built
// by a later settings generation (its gateway, and so its budget, are the new generation's).
func (d *Decypharr) inherit(prev *Decypharr) {
	if d == nil || prev == nil || d == prev || d.Config.URL != prev.Config.URL {
		return
	}
	prev.mu.Lock()
	ix, hintAt, failAt, failErr := prev.ix, prev.hintAt, prev.failAt, prev.failErr
	readyAt, loading := prev.readyAt, prev.loading
	prev.mu.Unlock()
	d.mu.Lock()
	d.ix, d.hintAt, d.failAt, d.failErr = ix, hintAt, failAt, failErr
	d.readyAt, d.loading = readyAt, loading
	d.mu.Unlock()
}

// Decypharr is the library's decypharr client (the torrents page and the health check share its index); nil when
// library.mount.decypharr is not set.
func (l *Library) Decypharr() *Decypharr { return l.decy }

// Zurg is the library's client of zurg's API (the torrents page and the import share its cached listing); nil when
// library.mount.zurg_api is not set.
func (l *Library) Zurg() *ZurgAPI { return l.zurg }

// DecypharrState says why decypharr cannot be read now, for the mount's health check: ErrDecypharrLoading while it
// loads, ErrDecypharrAuth when it refuses the credentials, any other error when its API does not answer. nil when
// it answers, and without decypharr. It reads the API's root, one request, not the index.
func (l *Library) DecypharrState(ctx context.Context) error {
	if l.decy == nil {
		return nil
	}
	if err := l.decy.Ready(ctx); err != nil {
		return err
	}
	_, err := l.decy.Groups(ctx)
	return err
}

// decypharrNames is the folder names decypharr lists hash under, exact. known is false when its index cannot be
// read: then nothing is known about the hash's folder.
func (l *Library) decypharrNames(ctx context.Context, hash string, fresh bool) (names []string, known bool) {
	read := l.decy.Index
	if fresh {
		read = l.decy.FreshIndex
	}
	ix, _ := read(ctx)
	return ix.Names(hash)
}

// fileList is a download's own file list: the sizes listed per file name. nil is "no list".
type fileList map[string][]int64

// has reports whether the list holds a file of this name and exact size.
func (fl fileList) has(name string, size int64) bool {
	return slices.Contains(fl[name], size)
}

// downloadFiles reads a download's own file list, once per import attempt or relinked hash: decypharr's own list
// for a download added through a client (it answers only while decypharr keeps the torrent queued), the provider's
// selected files for a direct add. nil when there is none.
func (l *Library) downloadFiles(ctx context.Context, hash, provider, torrentID, client string) fileList {
	if l.decy == nil || hash == "" {
		return nil
	}
	var files []TorrentFile
	switch {
	case client != "":
		list, found, err := l.decy.TorrentFiles(ctx, hash)
		if err != nil || !found {
			return nil
		}
		for _, f := range list {
			files = append(files, TorrentFile{Name: f.Name, Size: f.Size})
		}
	case provider != "" && l.files != nil:
		var ok bool
		if files, ok = l.files(ctx, provider, torrentID, hash); !ok {
			return nil
		}
	}
	if len(files) == 0 {
		return nil
	}
	fl := fileList{}
	for _, f := range files {
		name := path.Base(filepath.ToSlash(f.Name))
		fl[name] = append(fl[name], f.Size)
	}
	return fl
}

// unreadyStates are the states of a download the router still polls at its provider.
var unreadyStates = []string{"queued", "adding", "added", "downloading"}

// relinkLists are the file lists a relink sweep read, by provider, torrent id, client and hash.
type relinkLists map[string]fileList

// relinkHolds reports whether a relink candidate (folder n holding the link's file) is the link's own: with a file
// list for the link's download, the file there must be in it by name and exact size. Without decypharr, without a
// list or without the file's size the mount's answer stands.
func (l *Library) relinkHolds(ctx context.Context, db *sql.DB, lists relinkLists, ml mountLink, hash, n string) bool {
	if l.decy == nil {
		return true
	}
	var provider, tid, client sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT d.provider, d.provider_torrent_id, d.client FROM link lk
		JOIN download d ON d.id = lk.download_id WHERE lk.path = ?`, ml.path).Scan(&provider, &tid, &client)
	key := provider.String + "\x00" + tid.String + "\x00" + client.String + "\x00" + hash
	fl, ok := lists[key]
	if !ok {
		fl = l.downloadFiles(ctx, hash, provider.String, tid.String, client.String)
		lists[key] = fl
	}
	if fl == nil {
		return true
	}
	fi, err := l.mount.Stat(ctx, filepath.Join(l.mount.AllPath(), n, ml.inner))
	if err != nil {
		return true
	}
	return fl.has(filepath.Base(ml.inner), fi.Size())
}

// withDecypharrNames puts decypharr's names for hash before a relink's candidates, each once.
func withDecypharrNames(dnames, names []string) []string {
	out := slices.Clone(dnames)
	for _, n := range names {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// unlistedReason is why a file of the folder is skipped: it is not in the download's own list.
func unlistedReason(rel string) string {
	return "decypharr: " + filepath.Base(rel) + " is not a file of the download (its name and size are not in the download's file list)"
}
