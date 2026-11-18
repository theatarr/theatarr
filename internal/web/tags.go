package web

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/catalog"
)

// Tags edited in Theatarr (docs/design/arrview.md "Tags per version"): the title page's Tags fact and the selection
// bar's Tags… apply only the labels the person added or removed, so a label one library carries and the person did
// not touch stays as each copy has it.

// TitleTagsRequest is the body of POST /titles/tags.
type TitleTagsRequest struct {
	TitleIDs []int64 `json:"title_ids"`
	// Add puts each label on every current copy of each title (a new label is created by the compatible rule).
	Add []string `json:"add"`
	// Remove takes each label off every copy of each title, removed copies included.
	Remove []string `json:"remove"`
}

// TitleTagsResult is the answer of POST /titles/tags.
type TitleTagsResult struct {
	// Changed counts the titles a label was put on or taken off.
	Changed int `json:"changed"`
	// Tags are, for one title, its labels after (as the title page shows them).
	Tags []string `json:"tags,omitempty"`
}

// TagList is the answer of GET /tags: every label, in label order.
type TagList struct {
	Tags []string `json:"tags"`
}

func (w *Web) mountTagsAPI(mux *http.ServeMux) {
	w.handle(mux, "GET "+APIBase+"/tags", SignedIn, auth.PermSeeFiles, w.apiTags)
	w.handle(mux, "POST "+APIBase+"/titles/tags", SignedIn, auth.PermManage|auth.PermSeeFiles, w.apiTitleTags)
}

func (w *Web) apiTags(rw http.ResponseWriter, r *http.Request) {
	out := TagList{Tags: []string{}}
	err := scanAll(r.Context(), w.st.Reader(), `SELECT label FROM arrview_tag ORDER BY label`, nil, func(rows *sql.Rows) error {
		var l string
		if err := rows.Scan(&l); err != nil {
			return err
		}
		out.Tags = append(out.Tags, l)
		return nil
	})
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, out)
}

// tagLabels trims and lower-cases typed labels, each once; ok is false when one breaks the rule.
func tagLabels(in []string) ([]string, bool) {
	var out []string
	for _, l := range in {
		l = strings.ToLower(strings.TrimSpace(l))
		if !catalog.ValidTagLabel(l) {
			return nil, false
		}
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out, true
}

func (w *Web) apiTitleTags(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req TitleTagsRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	add, ok1 := tagLabels(req.Add)
	remove, ok2 := tagLabels(req.Remove)
	if !ok1 || !ok2 {
		badRequest(rw, catalog.TagLabelWords)
		return
	}
	for _, l := range add {
		if slices.Contains(remove, l) {
			badRequest(rw, "A tag cannot be added and removed at once: "+l)
			return
		}
	}
	ids, ok := w.choiceTitles(rw, r, req.TitleIDs, "", "tags")
	if !ok {
		return
	}
	var out TitleTagsResult
	err := w.st.WriteTx(ctx, func(tx *sql.Tx) error {
		out.Changed = 0
		list := titleIDsJSON(ids)
		// One statement per label over the whole batch; changed counts the distinct titles a row was written for.
		changed := map[int64]bool{}
		run := func(q string, args ...any) error {
			return queryEach(ctx, tx, q, args, func(rows *sql.Rows) error {
				var id int64
				if err := rows.Scan(&id); err != nil {
					return err
				}
				changed[id] = true
				return nil
			})
		}
		// A label is created only for titles that have a current copy to put it on.
		var current bool
		if len(add) > 0 {
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM version v WHERE v.title_id IN
				(SELECT value FROM json_each(?)) AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id))`,
				list).Scan(&current); err != nil {
				return err
			}
		}
		for _, l := range add {
			if !current {
				break
			}
			tag, _, err := catalog.FindOrCreateTag(ctx, tx, l, w.now())
			if err != nil {
				return err
			}
			// Every current copy (not one removed from its library), whichever library it is in.
			if err := run(`INSERT INTO arrview_title_tag (version, title_id, tag_id)
				SELECT v.policy_key, v.title_id, ? FROM version v WHERE v.title_id IN (SELECT value FROM json_each(?)) AND NOT EXISTS
				(SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id) ON CONFLICT DO NOTHING RETURNING title_id`,
				tag, list); err != nil {
				return err
			}
		}
		for _, l := range remove {
			// Removed copies too, so the label does not come back with a restored copy; an unknown label matches
			// nothing and is not created.
			if err := run(`DELETE FROM arrview_title_tag WHERE tag_id IN (SELECT id FROM arrview_tag WHERE label = ?)
				AND title_id IN (SELECT value FROM json_each(?)) RETURNING title_id`, l, list); err != nil {
				return err
			}
		}
		out.Changed = len(changed)
		return nil
	})
	if err != nil {
		w.internal(rw, err)
		return
	}
	// A tag decides which webhooks get a title's events: who changed them is logged.
	w.log.Info("tags changed", "source", "ui", "by", actor(r), "titles", len(ids), "changed", out.Changed, "add", add,
		"remove", remove)
	if len(ids) == 1 {
		tags, err := titleTags(ctx, w.st.Reader(), []any{ids[0]})
		if err != nil {
			w.internal(rw, err)
			return
		}
		out.Tags = tags[ids[0]]
	}
	writeJSON(rw, http.StatusOK, out)
}

// titleIDsJSON is ids as a JSON array, for json_each.
func titleIDsJSON(ids []int64) string {
	b, _ := json.Marshal(ids)
	return string(b)
}
