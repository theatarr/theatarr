package torrents

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

// Usage is what a torrent is to Theatarr's libraries.
type Usage string

const (
	// Used: a file of it is referenced (RefKind).
	Used Usage = "used"
	// Unused: every file is unreferenced, and Theatarr has all the data to say so.
	Unused Usage = "unused"
	// Unknown: Theatarr lacks the data to say (see Torrent.Reasons). Never offered as unused.
	Unknown Usage = "unknown"
)

// Reference kinds.
const (
	RefLink     = "link"     // a library link of Theatarr (active or dead)
	RefArrFile  = "arr_file" // a current arr file (arr import)
	RefDownload = "download" // a download queued or in flight
	RefScan     = "scan"     // a symlink into the mount under torrents.scan_roots
)

// Ref is one reference to a torrent.
type Ref struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"` // a path, or the download
}

// activeDownloadStates are the download states that still need their torrent.
var activeDownloadStates = []string{"queued", "adding", "added", "downloading", "ready", "importing"}

// refIndex is every reference Theatarr knows, by hash and by mount folder, and what it lacks.
type refIndex struct {
	byHash   map[string][]Ref
	byFolder map[string][]Ref
	// unknown are the reasons every torrent without references is unknown (a scan root unreadable).
	unknown []string
	// registry holds the hashes the content registry knows; synced the providers listed completely at least once.
	registry map[string]bool
	synced   map[string]bool
	// added holds "provider\x00torrent id" of the torrents Theatarr's router added (not reused ones).
	added map[string]bool
	// files counts the registry's files per hash, for providers whose list has none.
	files map[string]int
	// decyOn: the mount is decypharr's (library.mount.decypharr). decy is then its index of hash and folder name,
	// nil when it could not be read: a torrent without references is unknown then, never unused.
	decyOn bool
	decy   *library.DecypharrIndex
	// folderOnly: some reference is known only by its mount folder (a link without a hash, a scan root's link), so a
	// torrent whose list name is not its folder's may be used under its original name (secondName).
	folderOnly bool
	// zurg is zurg's listing (Manager.Zurg), read only when folderOnly; nil without it or when it failed. It only adds
	// folder names (torrentFolders).
	zurg *library.ZurgTorrents
}

// zurgListing is zurg's listing for a usage with folder-name references; nil without zurg_api or when it fails.
func (m *Manager) zurgListing(ctx context.Context) *library.ZurgTorrents {
	if m.Zurg == nil {
		return nil
	}
	l, err := m.Zurg.Torrents(ctx)
	if err != nil {
		return nil
	}
	return l
}

// decypharrIndex reads decypharr's index when the mount is decypharr's (on). ix is nil when it cannot be read, and
// without a client.
func (m *Manager) decypharrIndex(ctx context.Context) (on bool, ix *library.DecypharrIndex) {
	if m.Policy == nil || m.Policy.Library == nil || m.Policy.Library.Mount.Decypharr == nil {
		return false, nil
	}
	if m.Decypharr != nil {
		ix, _ = m.Decypharr.Index(ctx)
	}
	return true, ix
}

// folderNames are the mount folders a torrent may be under: the names any mount gives it and, on decypharr's mount,
// the names decypharr lists its hash under. A link into one of them is a reference: decypharr serves same-named
// torrents from one folder, so every hash listed under a linked folder's name counts as used.
func folderNames(decy *library.DecypharrIndex, hash, name string) []string {
	names := library.ReleaseFolderNames(hash, name)
	dnames, _ := decy.Names(hash)
	for _, n := range dnames {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return names
}

// secondName is the torrent's original name when the provider gave one: from a detail read, or from the files a list
// gives (TorBox's), named as zurg names the folder. "" when neither is known (Real-Debrid's and AllDebrid's lists).
func secondName(t provider.Torrent) string {
	if t.OriginalName != "" {
		return t.OriginalName
	}
	return provider.OriginalName(t.Files, t.Name)
}

// torrentFolders are folderNames of the provider's name and of its original name (secondName), which a mount may
// use for its folder instead, and the folder names of zurg's name for the hash (as the import names its folder).
func torrentFolders(decy *library.DecypharrIndex, zurg *library.ZurgTorrents, hash string, t provider.Torrent) []string {
	names := folderNames(decy, hash, t.Name)
	more := func(ns []string) {
		for _, n := range ns {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	if o := secondName(t); o != "" && o != t.Name {
		more(folderNames(decy, hash, o))
	}
	if hash != "" {
		if z, ok := zurg.Name(hash); ok {
			more(library.ReleaseFolderNames(hash, z))
		}
	}
	return names
}

// Reasons a delete's detail read leaves a torrent unknown (fixed strings: they are part of its override token).
const (
	reasonNoOriginal = "its original name could not be read from the provider, so the folder it is under is not known"
	reasonNoName     = "the provider gives no original name for it yet, so the folder it is under is not known"
)

// deleteUsage is ix.usage for a delete (its plan and its last check). A torrent that would be unused while some
// reference is known only by its folder, and whose original name the list did not give, has its detail read: the
// mount may name its folder by that name. A failed read, or one without a name, leaves it unknown. reload gives the
// references to judge t with its original name (a scoped load must read that folder's links too). Torrents matched
// by hash or by their list name, and installs whose references all carry hashes, read nothing.
func (m *Manager) deleteUsage(ctx context.Context, key string, t provider.Torrent, ix *refIndex,
	reload func(provider.Torrent) (*refIndex, error)) (Usage, map[string]int, []Ref, []string, error) {
	u, refs, list, reasons := ix.usage(key, t)
	if u != Unused || !ix.folderOnly || secondName(t) != "" {
		return u, refs, list, reasons, nil
	}
	p := m.Providers[key]
	if p == nil {
		return Unknown, nil, nil, []string{reasonNoOriginal}, nil
	}
	d, err := p.Torrent(ctx, t.ID)
	if err != nil {
		return Unknown, nil, nil, []string{reasonNoOriginal}, nil
	}
	// The provider's own answer only (each fills it from its detail): no guess from the files here, a detail
	// without the name is unknown.
	if t.OriginalName = d.OriginalName; t.OriginalName == "" {
		return Unknown, nil, nil, []string{reasonNoName}, nil
	}
	if ix, err = reload(t); err != nil {
		return "", nil, nil, nil, err
	}
	u, refs, list, reasons = ix.usage(key, t)
	return u, refs, list, reasons, nil
}

// mountView maps a link target to the mount folder it names.
type mountView struct{ path, link, all string }

func newMountView(pol *policy.Policy) *mountView {
	if pol == nil || pol.Library == nil || pol.Library.Mount.Path == "" {
		return nil
	}
	mt := pol.Library.Mount
	all := mt.AllDir
	if all == "" {
		all = policy.DefaultLibraryAllDir
	}
	return &mountView{path: filepath.Clean(mt.Path), link: filepath.Clean(mt.LinkPath), all: all}
}

// folder is the release folder a target in the mount names: <mount>/<dir>/<folder>[/...]. A target outside the
// mount is ok false.
func (v *mountView) folder(target string) (string, bool) {
	if v == nil || target == "" {
		return "", false
	}
	target = filepath.Clean(target)
	for _, base := range []string{v.link, v.path} {
		if base == "" || base == "." {
			continue
		}
		rel, err := filepath.Rel(base, target)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 2 {
			return "", true // the mount root or one of its directories: in the mount, no release
		}
		return parts[1], true
	}
	return "", false
}

// loadRefs reads every reference: the database's (loadDBRefs) and the scan roots' (scanRefs).
func (m *Manager) loadRefs(ctx context.Context) (*refIndex, error) {
	ix, err := m.loadDBRefs(ctx)
	if err != nil {
		return nil, err
	}
	m.scanRefs(ctx, ix)
	if ix.folderOnly {
		ix.zurg = m.zurgListing(ctx)
	}
	return ix, nil
}

// loadDBRefs reads the references Theatarr writes (library links, arr files, downloads) and the registry.
func (m *Manager) loadDBRefs(ctx context.Context) (*refIndex, error) {
	return m.loadDBRefsFor(ctx, nil)
}

// refScope narrows loadDBRefsFor to what one torrent's usage reads: its hash (normalized) and release folder names.
type refScope struct {
	hash    string
	folders []string
	// decyOn and decy are refIndex's, as read for folders; so is zurg.
	decyOn bool
	decy   *library.DecypharrIndex
	zurg   *library.ZurgTorrents
}

// loadDBRefsFor is loadDBRefs, or with sc only the rows one torrent's usage can see (a delete's last check of each
// item under the fence): the links tied to its hash or naming one of its folders, plus every link with
// no hash (they decide the "could not be tied" reason), the arr files and active downloads of its hash, and its
// registry row. Its usage and override token are the same as from the full index (rows are read in id order in both),
// at a fraction of the reads; added and files are filled only by the full load.
func (m *Manager) loadDBRefsFor(ctx context.Context, sc *refScope) (*refIndex, error) {
	db := m.Store.Reader()
	ix := &refIndex{byHash: map[string][]Ref{}, byFolder: map[string][]Ref{}, registry: map[string]bool{},
		synced: map[string]bool{}, added: map[string]bool{}, files: map[string]int{}}
	mv := newMountView(m.Policy)
	if sc != nil {
		ix.decyOn, ix.decy, ix.zurg = sc.decyOn, sc.decy, sc.zurg
	} else {
		ix.decyOn, ix.decy = m.decypharrIndex(ctx)
	}

	// Library links: by the content's hash, the download's, the recorded file metadata's, and the mount folder of the
	// target. A link with none of them in a configured mount cannot be tied to a torrent.
	q := `SELECT path, target, state, h FROM (SELECT l.id, l.path, l.target, l.state,
		lower(COALESCE(NULLIF(c.info_hash, ''), NULLIF(d.info_hash, ''), CASE WHEN json_valid(l.file_metadata)
		THEN NULLIF(json_extract(l.file_metadata, '$.InfoHash'), '') END, '')) AS h
		FROM link l LEFT JOIN content_file cf ON cf.id = l.content_file_id LEFT JOIN content c ON c.id = cf.content_id
		LEFT JOIN download d ON d.id = l.download_id WHERE l.state != 'unlinked')`
	var args []any
	if sc != nil {
		q += ` WHERE h = ? OR h = ''`
		args = append(args, sc.hash)
		for _, f := range sc.folders {
			q += ` OR instr(target, ?) > 0`
			args = append(args, "/"+f)
		}
	}
	rows, err := db.QueryContext(ctx, q+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("torrents: reading links: %w", err)
	}
	untied := 0
	for rows.Next() {
		var path, target, state, hash string
		if err := rows.Scan(&path, &target, &state, &hash); err != nil {
			rows.Close()
			return nil, err
		}
		r := Ref{Kind: RefLink, Detail: path}
		if state != "active" {
			r.Detail += " (" + state + ")"
		}
		folder, inMount := mv.folder(target)
		if hash != "" {
			ix.byHash[hash] = append(ix.byHash[hash], r)
		}
		if folder != "" {
			ix.byFolder[folder] = append(ix.byFolder[folder], r)
			ix.folderOnly = ix.folderOnly || hash == ""
		}
		if hash == "" && folder == "" && (mv == nil || inMount) {
			untied++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if untied > 0 {
		ix.unknown = append(ix.unknown, fmt.Sprintf("%d library link(s) could not be tied to a torrent "+
			"(no hash recorded and no library.mount to read the folder from)", untied))
	}

	// Arr files: the content the import tied to them, else the arr's download id (the hash for torrents).
	// Scoped, a value that is not a plain hex hash (base32, padded) is normalized here like the rest.
	q = `SELECT instance, path, h FROM (SELECT vf.id, vf.instance, vf.path,
		lower(COALESCE(c.info_hash, vf.download_id, '')) AS h FROM version_file vf LEFT JOIN content c ON c.id = vf.content_id)`
	args = nil
	if sc != nil {
		q += ` WHERE h = ? OR length(h) <> 40`
		args = append(args, sc.hash)
	}
	rows, err = db.QueryContext(ctx, q+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("torrents: reading arr files: %w", err)
	}
	for rows.Next() {
		var inst, path, hash string
		if err := rows.Scan(&inst, &path, &hash); err != nil {
			rows.Close()
			return nil, err
		}
		if h := indexer.NormalizeHash(hash); h != "" {
			ix.byHash[h] = append(ix.byHash[h], Ref{Kind: RefArrFile, Detail: inst + ": " + path})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Downloads still needing their torrent; and which torrents the router added.
	q = `SELECT id, lower(info_hash), state, COALESCE(provider, ''), COALESCE(provider_torrent_id, ''), reused
		FROM download`
	args = nil
	if sc != nil {
		q += ` WHERE lower(info_hash) = ?`
		args = append(args, sc.hash)
	}
	rows, err = db.QueryContext(ctx, q+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("torrents: reading downloads: %w", err)
	}
	active := map[string]bool{}
	for _, s := range activeDownloadStates {
		active[s] = true
	}
	for rows.Next() {
		var id int64
		var hash, state, prov, tid string
		var reused int
		if err := rows.Scan(&id, &hash, &state, &prov, &tid, &reused); err != nil {
			rows.Close()
			return nil, err
		}
		if active[state] {
			ix.byHash[hash] = append(ix.byHash[hash], Ref{Kind: RefDownload,
				Detail: fmt.Sprintf("download %d (%s)", id, state)})
		}
		if prov != "" && tid != "" && reused == 0 {
			ix.added[prov+"\x00"+tid] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The registry: which hashes it knows, its files, and which providers were listed completely.
	q = `SELECT lower(c.info_hash), (SELECT count(*) FROM content_file f WHERE f.content_id = c.id) FROM content c`
	args = nil
	if sc != nil {
		q += ` WHERE lower(c.info_hash) = ?`
		args = append(args, sc.hash)
	}
	rows, err = db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("torrents: reading the registry: %w", err)
	}
	for rows.Next() {
		var h string
		var n int
		if err := rows.Scan(&h, &n); err != nil {
			rows.Close()
			return nil, err
		}
		ix.registry[h] = true
		if n > 0 {
			ix.files[h] = n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT DISTINCT provider FROM provider_sync_run WHERE ok = 1`)
	if err != nil {
		return nil, fmt.Errorf("torrents: reading sync runs: %w", err)
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		ix.synced[p] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return ix, nil
}

// scanRefs adds the links of the libraries other tools manage (torrents.scan_roots) to ix, or why they are unknown.
func (m *Manager) scanRefs(ctx context.Context, ix *refIndex) {
	mv := newMountView(m.Policy)
	if roots := m.Policy.TorrentScanRoots(); len(roots) > 0 {
		if mv == nil {
			ix.unknown = append(ix.unknown, "torrents.scan_roots is set but library.mount is not, so their links "+
				"cannot be tied to torrents")
		} else {
			mounts := m.MountPaths
			if mounts == nil {
				mounts = m.Policy.MountPaths()
			}
			for _, root := range roots {
				if err := scanRoot(ctx, root, mounts, mv, ix); err != nil {
					ix.unknown = append(ix.unknown, fmt.Sprintf("scan root %s: %v", root, err))
				}
			}
		}
	}
}

// newScanIndex is an empty index for scanRefs alone (a delete scans once and reuses it per item: withScan).
func newScanIndex() *refIndex {
	return &refIndex{byHash: map[string][]Ref{}, byFolder: map[string][]Ref{}}
}

// withScan adds what scanRefs put in sc (its folder references and unknown reasons) to ix.
func (ix *refIndex) withScan(sc *refIndex) *refIndex {
	for f, refs := range sc.byFolder {
		ix.byFolder[f] = append(ix.byFolder[f], refs...)
	}
	ix.unknown = append(ix.unknown, sc.unknown...)
	ix.folderOnly = ix.folderOnly || sc.folderOnly
	return ix
}

// scanRoot walks root (a local library, never the mount) for symlinks and files their targets under the mount
// folder they name. Only the links are read (Readlink), never their targets. Every read is bounded and charged to
// its filesystem: the root's resolution through the root prober (policy.RootResolver), the walk
// through library.WalkLinks, so a scan root on a share that stops answering ends the scan (an error: the usage is
// unknown, nothing is offered for deletion) instead of holding the report, a delete preview or a delete (which holds
// the router's add lock) on it.
func scanRoot(ctx context.Context, root string, mounts []string, mv *mountView, ix *refIndex) error {
	resolved, err := policy.NewRootResolver(ctx).ResolvePath(root, mounts)
	if err != nil {
		if errors.Is(err, policy.ErrInMount) {
			return errors.New("it is inside the debrid mount; scan roots are libraries of links into it")
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return library.WalkLinks(ctx, resolved, func(p, t string) {
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(p), t)
		}
		if folder, _ := mv.folder(t); folder != "" {
			ix.byFolder[folder] = append(ix.byFolder[folder], Ref{Kind: RefScan, Detail: p})
			ix.folderOnly = true
		}
	})
}

// maxRefList bounds the references listed per torrent; Refs still counts them all.
const maxRefList = 20

// usage decides one torrent of provider key.
func (ix *refIndex) usage(key string, t provider.Torrent) (Usage, map[string]int, []Ref, []string) {
	h := indexer.NormalizeHash(t.Hash)
	var refs []Ref
	seen := map[Ref]bool{}
	add := func(rs []Ref) {
		for _, r := range rs {
			if !seen[r] {
				seen[r] = true
				refs = append(refs, r)
			}
		}
	}
	if h != "" {
		add(ix.byHash[h])
	}
	for _, f := range torrentFolders(ix.decy, ix.zurg, h, t) {
		add(ix.byFolder[f])
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].Kind < refs[j].Kind })
	counts := map[string]int{}
	for _, r := range refs {
		counts[r.Kind]++
	}
	if len(refs) > maxRefList {
		refs = refs[:maxRefList]
	}
	if len(counts) > 0 {
		return Used, counts, refs, nil
	}
	var reasons []string
	if h == "" {
		reasons = append(reasons, "no v1 info hash")
	}
	if !ix.synced[key] {
		reasons = append(reasons, "the registry has no complete sync of "+key+" yet")
	} else if h != "" && !ix.registry[h] {
		reasons = append(reasons, "not in the registry yet (added since the last provider sync)")
	}
	if ix.decyOn && ix.decy == nil {
		reasons = append(reasons, "decypharr's list of torrents could not be read, so the folder it is under is not known")
	} else if ix.decyOn {
		// decypharr lists one hash of same-named torrents on a debrid: a hash it does not list may sit in a linked
		// folder under another name than the provider's.
		if names, _ := ix.decy.Names(h); len(names) == 0 {
			reasons = append(reasons, "decypharr does not list it, so the folder it is under is not known")
		}
	}
	reasons = append(reasons, ix.unknown...)
	if len(reasons) > 0 {
		return Unknown, nil, nil, reasons
	}
	return Unused, nil, nil, nil
}
