package web

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/theatarr/theatarr/internal/catalog"
)

// syncSourceWords says where Bring in sync's new copies take their choices from.
const syncSourceWords = "Each new copy is monitored like the title's first monitored copy in the group, or its first copy."

// syncPreviewNames is how many titles' names Bring in sync names per library.
const syncPreviewNames = 5

// SyncLibraryGain is what Bring in sync gives one library of a group: Count titles, the first of them named (Names)
// and More the rest.
type SyncLibraryGain struct {
	Key   string   `json:"key"`
	Label string   `json:"label"`
	Count int      `json:"count"`
	Names []string `json:"names"`
	More  int      `json:"more"`
}

// BringInSync is the answer of GET and POST /settings/versions/{key}/bring-in-sync: for the saved sync group of the
// library, how many titles gain (or gained) a copy (Titles) and per library of the group; Source says where the new
// copies' choices come from. Searched is the number of titles whose new copies were put on the search schedule.
type BringInSync struct {
	Group     []string          `json:"group"`
	Titles    int               `json:"titles"`
	Libraries []SyncLibraryGain `json:"libraries"`
	Source    string            `json:"source"`
	Searched  int               `json:"searched"`
}

// searchLater is the part of acquire.Acquirer that puts added titles on the paced search schedule.
type searchLater interface {
	RequestSearchLater(ctx context.Context, titleID int64, versions []string) error
}

func (w *Web) bringInSyncAnswer(group []string, gains []catalog.SyncGain) BringInSync {
	out := BringInSync{Group: group, Titles: len(gains), Libraries: []SyncLibraryGain{}, Source: syncSourceWords}
	names := w.keyLabels(group)
	for _, k := range group {
		lib := SyncLibraryGain{Key: k, Label: names[k], Names: []string{}}
		for _, g := range gains {
			for _, gk := range g.Keys {
				if gk != k {
					continue
				}
				lib.Count++
				if len(lib.Names) < syncPreviewNames {
					lib.Names = append(lib.Names, g.Name)
				}
			}
		}
		lib.More = lib.Count - len(lib.Names)
		out.Libraries = append(out.Libraries, lib)
	}
	return out
}

// syncGroupOf is the saved sync group of the path's library; nil (after answering) when there is none.
func (w *Web) syncGroupOf(rw http.ResponseWriter, r *http.Request) []string {
	if w.pol == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "no policy loaded"})
		return nil
	}
	g := w.pol.SyncGroup(r.PathValue("key"))
	if g == nil {
		notFound(rw)
	}
	return g
}

// GET /settings/versions/{key}/bring-in-sync: what Bring in sync would add for the library's saved group.
func (w *Web) apiBringInSyncPreview(rw http.ResponseWriter, r *http.Request) {
	group := w.syncGroupOf(rw, r)
	if group == nil {
		return
	}
	gains, err := catalog.SyncGains(r.Context(), w.st.Reader(), w.pol, group)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, w.bringInSyncAnswer(group, gains))
}

// POST /settings/versions/{key}/bring-in-sync {search}: gives the titles of the preview their missing copies in the
// library's saved group, in one transaction (catalog.BringInSync). With search, the new copies are put on the paced
// search schedule (RequestSearchLater: the hourly budget); never an immediate search.
func (w *Web) apiBringInSync(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Search bool `json:"search"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	group := w.syncGroupOf(rw, r)
	if group == nil {
		return
	}
	var sl searchLater
	if req.Search {
		var ok bool
		if sl, ok = w.add.Acquirer.(searchLater); !ok {
			writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "acquirer is not running"})
			return
		}
	}
	var gains []catalog.SyncGain
	err := w.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		gains, err = catalog.BringInSync(ctx, tx, w.pol, group, w.now())
		return err
	})
	if err != nil {
		w.internal(rw, err)
		return
	}
	out := w.bringInSyncAnswer(group, gains)
	for _, g := range gains {
		for _, k := range g.Keys {
			w.log.Info("version turned on", "source", "bring_in_sync", "title", g.TitleID, "version", k,
				"in_sync_with", g.Source)
		}
	}
	if sl != nil {
		for _, g := range gains {
			if !g.Monitored {
				continue
			}
			if err := sl.RequestSearchLater(context.WithoutCancel(ctx), g.TitleID, g.Keys); err != nil {
				w.log.Warn("bring in sync: search later", "title", g.TitleID, "err", err)
				continue
			}
			out.Searched++
		}
	}
	w.log.Info("bring in sync", "group", group, "titles", out.Titles, "search", req.Search, "searched", out.Searched)
	writeJSON(rw, http.StatusOK, out)
}
