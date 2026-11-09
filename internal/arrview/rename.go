package arrview

import (
	"context"
	"errors"
	"net/http"

	"github.com/theatarr/theatarr/internal/library"
)

func (s *Service) renameCommand(ctx context.Context, v *View, body map[string]any) (string, error) {
	rl, ok := s.library.(renameLibrary)
	if !ok {
		return "", errors.New("theatarr does not rename existing files")
	}
	sc, err := s.commandScope(ctx, v, "", body)
	if err != nil {
		return "", err
	}
	ids := append([]int64(nil), sc.sel.TitleIDs...)
	if len(ids) == 0 {
		if files, ok := body["files"].([]any); ok {
			seen := map[int64]bool{}
			for _, raw := range files {
				m, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				for _, k := range []string{"movieFileId", "episodeFileId"} {
					if n, ok := m[k].(float64); ok && n >= LinkFileBase {
						var tid int64
						if err := s.st.Reader().QueryRowContext(ctx, `SELECT title_id FROM link WHERE id = ?`, int64(n)-LinkFileBase).Scan(&tid); err != nil {
							continue
						}
						if !seen[tid] {
							seen[tid] = true
							ids = append(ids, tid)
						}
					}
				}
			}
		}
	}
	if len(ids) == 0 {
		return "", errors.New("no titles in this view match the rename command")
	}
	var season *int
	if n, ok := body["seasonNumber"].(float64); ok && n >= 0 {
		x := int(n)
		season = &x
	}
	for _, id := range ids {
		var err error
		if scoped, ok := s.library.(scopedRenameLibrary); ok {
			_, err = scoped.ApplyRenameVersions(ctx, id, season, v.Versions)
		} else {
			_, err = rl.ApplyRename(ctx, id, season)
		}
		if err != nil {
			return "", err
		}
	}
	return "Rename completed", nil
}

type renameLibrary interface {
	PreviewRename(context.Context, int64, *int) (*library.RenamePreview, error)
	ApplyRename(context.Context, int64, *int) (*library.RenamePreview, error)
}

type scopedRenameLibrary interface {
	PreviewRenameVersions(context.Context, int64, *int, []string) (*library.RenamePreview, error)
	ApplyRenameVersions(context.Context, int64, *int, []string) (*library.RenamePreview, error)
}

type radarrRename struct {
	MovieID      int64  `json:"movieId"`
	MovieFileID  int64  `json:"movieFileId"`
	ExistingPath string `json:"existingPath"`
	NewPath      string `json:"newPath"`
}

type sonarrRename struct {
	SeriesID      int64  `json:"seriesId"`
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNums   []int  `json:"episodeNumbers"`
	EpisodeFileID int64  `json:"episodeFileId"`
	ExistingPath  string `json:"existingPath"`
	NewPath       string `json:"newPath"`
}

func (s *Service) rename(w http.ResponseWriter, r *request) {
	rl, ok := s.library.(renameLibrary)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var titleID int64
	var season *int
	if r.v.Flavour == Radarr {
		titleID = int64(r.queryInt("movieId", 0))
	} else {
		titleID = int64(r.queryInt("seriesId", 0))
		if r.URL.Query().Has("seasonNumber") {
			n := r.queryInt("seasonNumber", -1)
			if n < 0 {
				badRequest(w, "", "seasonNumber must be non-negative", nil)
				return
			}
			season = &n
		}
	}
	if titleID <= 0 {
		badRequest(w, "", "movieId or seriesId must be provided", nil)
		return
	}
	tid, err := s.resolve(r.Context(), r.v, titleID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if tid == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var p *library.RenamePreview
	if scoped, ok := s.library.(scopedRenameLibrary); ok {
		p, err = scoped.PreviewRenameVersions(r.Context(), tid, season, r.v.Versions)
	} else {
		p, err = rl.PreviewRename(r.Context(), tid, season)
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if r.v.Flavour == Radarr {
		out := make([]radarrRename, 0, len(p.Files))
		for _, f := range p.Files {
			out = append(out, radarrRename{MovieID: viewID(tid), MovieFileID: LinkFileBase + f.LinkID, ExistingPath: f.ExistingPath, NewPath: f.NewPath})
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	out := make([]sonarrRename, 0, len(p.Files))
	for _, f := range p.Files {
		out = append(out, sonarrRename{SeriesID: viewID(tid), SeasonNumber: f.SeasonNumber, EpisodeNums: f.EpisodeNums,
			EpisodeFileID: LinkFileBase + f.LinkID, ExistingPath: f.ExistingPath, NewPath: f.NewPath})
	}
	writeJSON(w, http.StatusOK, out)
}
