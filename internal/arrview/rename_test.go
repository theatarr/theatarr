package arrview

import (
	"context"
	"errors"
	"testing"

	"github.com/theatarr/theatarr/internal/library"
)

type renameTestLibrary struct {
	*fakeLibrary
	preview library.RenamePreview
	applied []int64
	err     error
}

func (l *renameTestLibrary) PreviewRename(context.Context, int64, *int) (*library.RenamePreview, error) {
	return &l.preview, nil
}

func (l *renameTestLibrary) ApplyRename(_ context.Context, titleID int64, _ *int) (*library.RenamePreview, error) {
	if l.err != nil {
		return nil, l.err
	}
	l.applied = append(l.applied, titleID)
	return &l.preview, nil
}

func TestRenameCommandGateClosed(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	l := &renameTestLibrary{fakeLibrary: e.lib, err: errors.New("library: write refused (disabled): library.enabled is false")}
	e.svc.library = l
	var cmd map[string]any
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "RenameMovie", "movieId": 1000001}, 201, &cmd)
	if cmd["status"] != "failed" || cmd["result"] != "unsuccessful" || len(l.applied) != 0 {
		t.Fatalf("closed gate command: %+v", cmd)
	}
}

func TestRenamePreviewAndCommands(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	l := &renameTestLibrary{fakeLibrary: e.lib, preview: library.RenamePreview{Files: []library.RenameFile{{
		LinkID: 7, Version: "movie-web", ExistingPath: "/old.mkv", NewPath: "/new.mkv",
	}}}}
	e.svc.library = l
	var files []map[string]any
	e.call("GET", "/radarr/api/v3/rename?movieId=1000001", nil, 200, &files)
	if len(files) != 1 || files[0]["movieFileId"] != float64(LinkFileBase+7) || files[0]["newPath"] != "/new.mkv" {
		t.Fatalf("preview: %+v", files)
	}
	var cmd map[string]any
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "RenameMovie", "movieId": 1000001}, 201, &cmd)
	if cmd["status"] != "completed" || len(l.applied) != 1 || l.applied[0] != 1 {
		t.Fatalf("command: %+v applied=%v", cmd, l.applied)
	}
	var series []map[string]any
	e.call("GET", "/sonarr/api/v3/rename?seriesId=1000010&seasonNumber=1", nil, 200, &series)
	if len(series) != 1 || series[0]["episodeFileId"] != float64(LinkFileBase+7) {
		t.Fatalf("series preview: %+v", series)
	}
}
