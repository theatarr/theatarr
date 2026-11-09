package main

import (
	"context"
	"errors"

	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/web"
)

// serveOps gives the web UI's recovery actions (web.Ops) the arr views' service: its decision engine, router and
// library, the same parts POST release, DELETE queue and DELETE moviefile use. Errors pass through unchanged, so
// web sees arrview.Refused as a refusal.
type serveOps struct{ av *arrview.Service }

func (o serveOps) GrabRelease(ctx context.Context, titleID int64, version string, rel indexer.Release, override bool,
	source string) (*web.GrabbedRelease, error) {
	g, err := o.av.GrabRelease(ctx, titleID, version, rel, override, source)
	if g == nil {
		return nil, err
	}
	return &web.GrabbedRelease{Decision: g.Decision, Overridden: g.Overridden, Job: g.Job}, err
}

func (o serveOps) RetryDownload(ctx context.Context, id int64, source string) (*web.RetriedDownload, error) {
	r, err := o.av.RetryDownload(ctx, id, source)
	if r == nil {
		return nil, err
	}
	return &web.RetriedDownload{TitleID: r.TitleID, Version: r.Version, Hash: r.Hash, Release: r.Release, What: r.What,
		Outcome: r.Outcome, Job: r.Job}, err
}

func (o serveOps) UnlinkLink(ctx context.Context, linkID int64, reason string) (*web.UnlinkedLink, error) {
	u, err := o.av.UnlinkLink(ctx, linkID, reason)
	if u == nil {
		return nil, err
	}
	return &web.UnlinkedLink{TitleID: u.TitleID, Path: u.Path, Versions: u.Versions, Trash: u.Trash}, err
}

// PreviewRename and ApplyRename give the title page's rename (internal/web/rename.go) the library's rename through
// the arr views' service. Apply writes only through the library gate (library.enabled, mode: active, the version's
// roots), moves each link as a compare-and-swap, and queues Plex scans of the folders it moved links in and out of.
// A gate refusal or a link that changed under the rename answers 409 with the reason.
func (o serveOps) PreviewRename(ctx context.Context, titleID int64, season *int) (*library.RenamePreview, error) {
	p, err := o.av.PreviewRename(ctx, titleID, season)
	return p, renameRefusal(err)
}

func (o serveOps) ApplyRename(ctx context.Context, titleID int64, season *int) (*library.RenamePreview, error) {
	p, err := o.av.ApplyRename(ctx, titleID, season)
	return p, renameRefusal(err)
}

// ReassignEpisodes gives the episode overview's "Change episodes" (internal/web/reassign.go) the library's episode
// correction through the arr views' service; a gate refusal, a conflict or a changed link answers 409.
func (o serveOps) ReassignEpisodes(ctx context.Context, linkID int64, episodeIDs []int64, replace bool) (*library.Reassigned, error) {
	r, err := o.av.ReassignEpisodes(ctx, linkID, episodeIDs, replace)
	return r, renameRefusal(err)
}

func renameRefusal(err error) error {
	if errors.Is(err, library.ErrRefused) {
		return &arrview.Refused{Reason: err.Error()}
	}
	return err
}
