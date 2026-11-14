package web

import (
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/library"
)

// GET /health/dangling: the links of the latest dangling-link report under the libraries' roots, for the list behind
// the Status check (which counts those only; theatarr library dangling prints every root the report walked). It reads
// what the report recorded (library_dangling); it never walks the mount, starts a report or changes a link.

// danglingListMax is how many links one answer carries; the oldest come first.
const danglingListMax = 500

// DanglingItem is one dangling link of the latest report.
type DanglingItem struct {
	Path   string `json:"path"`
	Target string `json:"target"`
	// Folder is the release folder on the mount the link points into.
	Folder string `json:"folder,omitempty"`
	// Library is the library key the link's root belongs to, Label its name for people.
	Library   string    `json:"library,omitempty"`
	Label     string    `json:"label,omitempty"`
	Cause     string    `json:"cause,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	// TitleID, Title and LinkState are set when the path is a link Theatarr recorded.
	TitleID   int64  `json:"title_id,omitempty"`
	Title     string `json:"title,omitempty"`
	LinkState string `json:"link_state,omitempty"`
}

// DanglingList is the latest dangling-link report with at most danglingListMax of its links.
type DanglingList struct {
	At        *time.Time     `json:"at"`       // null: no report yet
	Links     int            `json:"links"`    // links into the mount the report saw, every root
	Dangling  int            `json:"dangling"` // dangling links under the libraries' roots
	Truncated bool           `json:"truncated"`
	Items     []DanglingItem `json:"items"`
}

func (w *Web) apiHealthDangling(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, sum, err := library.Dangling(ctx, w.st.Reader())
	if err != nil {
		w.internal(rw, err)
		return
	}
	out := DanglingList{Items: []DanglingItem{}}
	if sum == nil {
		writeJSON(rw, http.StatusOK, out)
		return
	}
	// Links under an extra root (library.dangling.roots, other apps' folders) are not Theatarr's and not counted.
	rows = slices.DeleteFunc(rows, func(d library.DanglingRow) bool { return d.Version == "" })
	at := sum.At
	out.At, out.Links, out.Dangling = &at, sum.Links, len(rows)
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].FirstSeen.Equal(rows[j].FirstSeen) {
			return rows[i].FirstSeen.Before(rows[j].FirstSeen)
		}
		return rows[i].Path < rows[j].Path
	})
	if len(rows) > danglingListMax {
		rows, out.Truncated = rows[:danglingListMax], true
	}
	if len(rows) == 0 {
		writeJSON(rw, http.StatusOK, out)
		return
	}
	type known struct {
		id          int64
		name, state string
	}
	links := map[string]known{}
	args := make([]any, len(rows))
	for i, d := range rows {
		args[i] = d.Path
	}
	q, err := w.st.Reader().QueryContext(ctx, `SELECT l.path, l.title_id, t.name, l.state FROM link l
		JOIN title t ON t.id = l.title_id WHERE l.path IN (?`+strings.Repeat(", ?", len(args)-1)+`)`, args...)
	if err != nil {
		w.internal(rw, err)
		return
	}
	defer q.Close()
	for q.Next() {
		var p string
		var k known
		if err := q.Scan(&p, &k.id, &k.name, &k.state); err != nil {
			w.internal(rw, err)
			return
		}
		links[p] = k
	}
	if err := q.Err(); err != nil {
		w.internal(rw, err)
		return
	}
	for _, d := range rows {
		it := DanglingItem{Path: d.Path, Target: d.Target, Folder: d.Folder, Library: d.Version, Cause: d.Cause,
			FirstSeen: d.FirstSeen}
		if d.Version != "" {
			it.Label = w.versionLabel(d.Version)
		}
		if k, ok := links[d.Path]; ok {
			it.TitleID, it.Title, it.LinkState = k.id, k.name, k.state
		}
		out.Items = append(out.Items, it)
	}
	writeJSON(rw, http.StatusOK, out)
}
