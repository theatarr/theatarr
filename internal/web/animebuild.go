package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/metadata/anime/autobuild"
)

// Anime franchise builds (docs/design/anime.md "Automatic builds"): queue a rebuild of a franchise, a build for an
// anime series or from an AniList seed, and list the anime series no franchise maps into. Builds run as anime_build
// jobs; the endpoints only queue them and return the job id.

// AnimeBuilder is the request's settings generation's builder (autobuild.Service).
type AnimeBuilder interface {
	EnqueueRebuild(ctx context.Context, slug string) (int64, error)
	EnqueueDiscover(ctx context.Context, tvdbID int) (int64, error)
	EnqueueSeed(ctx context.Context, anilistID int, slug, name string) (int64, string, error)
	Status() autobuild.Status
	RefreshInterval() time.Duration
}

func (w *Web) mountAnimeBuildAPI(mux *http.ServeMux) {
	b := APIBase + "/anime"
	w.handle(mux, "POST "+b+"/franchises/{slug}/build", SignedIn, auth.PermAdmin, w.apiAnimeRebuild)
	w.handle(mux, "POST "+b+"/build", SignedIn, auth.PermAdmin, w.apiAnimeBuild)
	w.handle(mux, "GET "+b+"/uncovered", SignedIn, auth.PermAdmin, w.apiAnimeUncovered)
}

// animeBuilder is the request's builder, or nil.
func (w *Web) animeBuilder(ctx context.Context) AnimeBuilder {
	if w.animeBuilds == nil {
		return nil
	}
	return w.animeBuilds(ctx)
}

// AnimeBuildQueued answers a build request: the anime_build job and the franchise it builds (when known).
type AnimeBuildQueued struct {
	JobID int64  `json:"job_id"`
	Slug  string `json:"slug,omitempty"`
}

// writeBuildErr maps the builder's errors to HTTP codes.
func (w *Web) writeBuildErr(rw http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, autobuild.ErrNotConfigured):
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: err.Error()})
	case errors.Is(err, autobuild.ErrNoFranchise), errors.Is(err, autobuild.ErrNoSeries):
		writeJSON(rw, http.StatusNotFound, apiError{Error: err.Error()})
	case errors.Is(err, autobuild.ErrSlugTaken):
		writeJSON(rw, http.StatusConflict, apiError{Error: err.Error()})
	case errors.Is(err, autobuild.ErrInvalid):
		badRequest(rw, err.Error())
	default:
		w.internal(rw, err)
	}
}

func (w *Web) noBuilder(rw http.ResponseWriter) {
	writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "anime builds are not running"})
}

func (w *Web) apiAnimeRebuild(rw http.ResponseWriter, r *http.Request) {
	b := w.animeBuilder(r.Context())
	if b == nil {
		w.noBuilder(rw)
		return
	}
	slug := r.PathValue("slug")
	id, err := b.EnqueueRebuild(r.Context(), slug)
	if err != nil {
		w.writeBuildErr(rw, err)
		return
	}
	w.log.Info("anime: rebuild queued", "franchise", slug, "job", id)
	writeJSON(rw, http.StatusAccepted, AnimeBuildQueued{JobID: id, Slug: slug})
}

// animeBuildInput is a build for an anime series (tvdb_id) or from an AniList seed (anilist_id, optional slug and
// name).
type animeBuildInput struct {
	TVDBID    int    `json:"tvdb_id"`
	AniListID int    `json:"anilist_id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
}

func (w *Web) apiAnimeBuild(rw http.ResponseWriter, r *http.Request) {
	var in animeBuildInput
	if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 64<<10)).Decode(&in); err != nil {
		badRequest(rw, `want JSON: {"tvdb_id": <id>} or {"anilist_id": <id>, "slug": "...", "name": "..."}`)
		return
	}
	switch {
	case in.TVDBID != 0 && in.AniListID != 0:
		badRequest(rw, "give tvdb_id or anilist_id, not both")
		return
	case in.TVDBID < 0 || in.AniListID < 0 || in.TVDBID == 0 && in.AniListID == 0:
		badRequest(rw, "tvdb_id or anilist_id must be a positive id")
		return
	case in.TVDBID != 0 && (in.Slug != "" || in.Name != ""):
		badRequest(rw, "slug and name are only for anilist_id")
		return
	}
	b := w.animeBuilder(r.Context())
	if b == nil {
		w.noBuilder(rw)
		return
	}
	out := AnimeBuildQueued{}
	var err error
	if in.TVDBID != 0 {
		out.JobID, err = b.EnqueueDiscover(r.Context(), in.TVDBID)
	} else {
		out.JobID, out.Slug, err = b.EnqueueSeed(r.Context(), in.AniListID, in.Slug, in.Name)
	}
	if err != nil {
		w.writeBuildErr(rw, err)
		return
	}
	w.log.Info("anime: build queued", "tvdb", in.TVDBID, "anilist", in.AniListID, "slug", out.Slug, "job", out.JobID)
	writeJSON(rw, http.StatusAccepted, out)
}

// AnimeUncovered is the builder's state and the anime series no franchise maps into.
type AnimeUncovered struct {
	Builds autobuild.Status      `json:"builds"`
	Items  []autobuild.Uncovered `json:"items"`
}

func (w *Web) apiAnimeUncovered(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := AnimeUncovered{Builds: autobuild.Status{Off: "anime builds are not running"}}
	if b := w.animeBuilder(ctx); b != nil {
		out.Builds = b.Status()
	}
	jobs, err := autobuild.LatestJobs(ctx, w.st)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if out.Items, err = autobuild.ListUncovered(ctx, w.st, jobs); err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, out)
}

// annotateBuilds adds each franchise's latest build job and, while automatic builds are on, its next refresh:
// built_at + refresh_interval, none while a refresh waits for review.
func (w *Web) annotateBuilds(ctx context.Context, fs []AnimeFranchise) error {
	if len(fs) == 0 {
		return nil
	}
	jobs, err := autobuild.LatestJobs(ctx, w.st)
	if err != nil {
		return err
	}
	var every time.Duration
	if b := w.animeBuilder(ctx); b != nil {
		if st := b.Status(); st.Ready && st.AutoBuild {
			every = b.RefreshInterval()
		}
	}
	for i := range fs {
		f := &fs[i]
		f.Job = jobs.BySlug[f.Slug]
		if every > 0 && f.BuiltAt != nil && !f.Pending {
			next := f.BuiltAt.Add(every)
			f.NextRefresh = &next
		}
	}
	return nil
}
