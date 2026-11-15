package library

import (
	"context"
	"errors"
	"path"
)

// Verify scans a version's roots read-only: counts and dangling links (with the candidate a relink would use). It writes nothing, to the filesystem or the database.
func (l *Library) Verify(ctx context.Context, version string) ([]*LinkScan, error) {
	if l.runtime != nil {
		ctx, active := l.runtime(ctx)
		return active.Verify(ctx, version)
	}
	var out []*LinkScan
	var ix *mountIndex
	mountErr := ""
	var zv *zurgView
	if l.set.MountPath == "" {
		mountErr = "library.mount.path is not set"
	} else if err := l.mountHealthy(ctx); err != nil {
		mountErr = err.Error()
	} else if ix, err = l.newMountIndex(ctx); err != nil {
		mountErr = err.Error()
	} else {
		zv = l.zurgView(ctx)
		if zv != nil {
			zv.ix = ix
		}
	}
	for _, k := range l.gate.Versions() {
		if version != "" && k != version {
			continue
		}
		for _, root := range l.gate.Roots(k) {
			sc := &LinkScan{Version: k, Root: root, Causes: map[string]int{}, DryRun: true, Mount: mountErr}
			zv.note(sc)
			if err := l.gate.Reachable(ctx, root); err != nil {
				l.skipRoot(sc, err)
				out = append(out, sc)
				continue
			}
			links, err := l.scanRoot(ctx, root, sc)
			if Unavailable(err) {
				l.skipRoot(sc, err)
				out = append(out, sc)
				continue
			}
			if err != nil {
				return out, err
			}
			if ix != nil {
				for _, ml := range links {
					ok, err := ix.has(ml.folder, ml.inner)
					if errors.Is(err, errFolderUnreadable) {
						continue // no verdict on a link into a folder the mount would not list
					}
					if err != nil {
						return out, err
					}
					if ok {
						continue
					}
					dl := DanglingLink{Path: ml.path, Target: ml.target, Cause: "unknown"}
					hash, names, err := l.candidates(ctx, l.st.Reader(), ml)
					if err != nil {
						return out, err
					}
					dl.Hash = hash
					for _, n := range names {
						if found, _ := ix.has(n, ml.inner); found {
							dl.Cause, dl.NewTarget = "renamed", l.mount.ToLink(path.Join(l.mount.AllPath(), n, ml.inner))
							break
						}
					}
					if dl.Cause == "unknown" {
						dl.Cause, dl.Reason = zv.classify(dl.Cause, hash, ml.folder)
					}
					sc.Dangling = append(sc.Dangling, dl)
					sc.Causes[dl.Cause]++
				}
			}
			out = append(out, sc)
		}
	}
	return out, nil
}
