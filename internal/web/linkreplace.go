package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

// Replace file from the title page (docs/design/ui.md "Recovery actions"): the file rows of a movie's library row and
// of the episode window offer managers the Problems page's Replace file… for one of Theatarr's links, with the same
// refusals and the same confirmation scope (linkFiles, replaceFile in reports.go). It runs without a problem report.

// ReplaceScope is whether Replace file may run for a link now (Why says why not) and what it touches: the libraries
// that lose the file, every episode it holds, and whether its release is a pack (blocklisted as a whole). Managers
// who see files only.
type ReplaceScope struct {
	Libraries []string `json:"libraries"`
	Episodes  []string `json:"episodes,omitempty"`
	Pack      bool     `json:"pack"`
	Replace   bool     `json:"replace"`
	Why       string   `json:"why,omitempty"`
}

func replaceScopeOf(f ReportFile) *ReplaceScope {
	return &ReplaceScope{Libraries: f.Libraries, Episodes: f.Episodes, Pack: f.Pack, Replace: f.Replace, Why: f.Why}
}

// linkScopes is the Replace file scope of the title's active links (those holding episodeID when it is not 0), by
// link id.
func (w *Web) linkScopes(ctx context.Context, titleID, episodeID int64) (map[int64]*ReplaceScope, error) {
	files, err := w.linkFiles(ctx, titleID, episodeID, 0)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*ReplaceScope, len(files))
	for _, f := range files {
		out[f.LinkID] = replaceScopeOf(f)
	}
	return out, nil
}

// movieReplaceScopes adds the Replace file scope to the links of a movie's library rows (the title page, managers).
func (w *Web) movieReplaceScopes(ctx context.Context, d *TitleDetail) error {
	if d.Kind != "movie" {
		return nil
	}
	scopes, err := w.linkScopes(ctx, d.ID, 0)
	if err != nil {
		return err
	}
	for i := range d.Versions {
		for j := range d.Versions[i].Links {
			l := &d.Versions[i].Links[j]
			l.ReplaceScope = scopes[l.ID]
		}
	}
	return nil
}

// POST /links/{id}/replace: Replace file for one of the title's files from the title page, as the Problems page does
// it (replaceFile): 409 with the reason before anything changes when the search after it could not run.
func (w *Web) apiReplaceLink(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var titleID int64
	err := w.st.Reader().QueryRowContext(ctx, `SELECT title_id FROM link WHERE id = ?`, id).Scan(&titleID)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	files, err := w.linkFiles(ctx, titleID, 0, id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if len(files) == 0 {
		writeJSON(rw, http.StatusConflict, apiError{Error: "That file is no longer in the library"})
		return
	}
	by := actor(r)
	w.replaceFile(rw, r, titleID, files[0], "removed; replacement search requested by "+orSomeone(by),
		"replacement search requested", "file replaced from the title page")
}
