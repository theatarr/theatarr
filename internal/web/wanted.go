package web

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/store"
)

// WantedPage names the page's titles in one read; the acquirer owns selection and waiting reasons.
type WantedPage struct {
	*acquire.WantedList
	Libraries map[string]string `json:"libraries"`
	Titles    map[int64]string  `json:"titles"`
}

func (w *Web) apiWanted(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("only") != acquire.OnlyMissing && q.Get("only") != acquire.OnlyCutoff {
		badRequest(rw, "only must be missing or cutoff")
		return
	}
	switch q.Get("waiting") {
	case "", "due", "searched_recently", "in_flight", "grab_off", "loop_off", "downloads_off":
	default:
		badRequest(rw, "invalid waiting filter")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if w.add.Acquirer == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "acquirer is not running"})
		return
	}
	list, err := w.add.Acquirer.WantedList(r.Context(), acquire.WantedQuery{Only: q.Get("only"), Version: q.Get("version"), Waiting: q.Get("waiting"), Limit: limit, Offset: offset})
	if err != nil {
		w.internal(rw, err)
		return
	}
	out := WantedPage{WantedList: list, Titles: map[int64]string{}, Libraries: w.keyLabels(w.pol.ActiveVersions())}
	var ids []any
	for _, row := range list.Items {
		if _, ok := out.Titles[row.TitleID]; !ok {
			ids = append(ids, row.TitleID)
			out.Titles[row.TitleID] = ""
		}
	}
	if len(ids) > 0 {
		err = scanAll(r.Context(), w.st.Reader(), `SELECT id,name FROM title WHERE id IN (`+store.Placeholders(len(ids))+`)`, ids, func(rows *sql.Rows) error {
			var id int64
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				return err
			}
			out.Titles[id] = name
			return nil
		})
		if err != nil {
			w.internal(rw, err)
			return
		}
	}
	writeJSON(rw, http.StatusOK, out)
}
