package web

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/store"
)

// GET discover: the Home rows in one call (docs/design/ui.md). Each row has at most discoverLimit items, the total
// and a "more" link (an SPA path). The "Continue" row (downloads in progress) comes from the activity reader and
// requests from the requests API; they are not here.
//
// GET discover/{row}?limit=&offset=: one whole row, paged. A row mixes movies and series, so its "more" link is the
// SPA's /discover/{row}, which reads this route: the same picks in the same order as the Home row, every kind.
//
//   - recent: titles by their newest current file.
//   - upcoming: digital, physical and theatrical release dates (US or the arr's), and episode air dates, in the
//     next upcomingDays days; the earliest per title.
//   - missing: monitored titles with a monitored version that is wanted and out: a movie counts as out from its
//     first digital or physical date, or 90 days after the TMDb release date when there is none, or after its year;
//     a series has aired episodes (specials left out) that are monitored and wanted. Newest first.
//   - upgrades: monitored titles with a version that has a file below its cutoff, newest file first.
//
// Missing and upgrades are copy shortfalls: a caller without "see files" (a family account) gets them empty.

const (
	discoverLimit = 20
	upcomingDays  = 30
	// discoverPageMax is the most items one GET discover/{row} answers.
	discoverPageMax = 200
)

// discoverRows are the rows GET discover/{row} serves, in Home order.
var discoverRows = []string{"recent", "upcoming", "missing", "upgrades"}

// shortfallRows are the rows about the household's copies (a version missing or below its cutoff): empty for a
// caller without "see files", who sees whether a title can be watched.
var shortfallRows = map[string]bool{"missing": true, "upgrades": true}

// discoverMore is a row's "See all" path in the SPA.
func discoverMore(row string) string { return "/discover/" + row }

// DiscoverItem is a title in a Home row, with what the row says about it.
type DiscoverItem struct {
	TitleRow
	// Note is the row's line about the title: "Digital release", "S02E03 · Pilot", "Missing 4K Remux".
	Note string `json:"note,omitempty"`
	// Date is the day the note is about (YYYY-MM-DD), if any.
	Date string `json:"date,omitempty"`
}

// DiscoverRow is one Home row.
type DiscoverRow struct {
	Items []DiscoverItem `json:"items"`
	Total int            `json:"total"`
	More  string         `json:"more"`
}

// Discover is the Home rows.
type Discover struct {
	Recent   DiscoverRow `json:"recent"`
	Upcoming DiscoverRow `json:"upcoming"`
	Missing  DiscoverRow `json:"missing"`
	Upgrades DiscoverRow `json:"upgrades"`
}

type pick struct {
	id         int64
	note, date string
}

func (w *Web) apiDiscover(rw http.ResponseWriter, r *http.Request) {
	d, err := w.discover(r.Context())
	if err != nil {
		w.internal(rw, err)
		return
	}
	perms := w.permsOf(r)
	for _, row := range []*DiscoverRow{&d.Recent, &d.Upcoming, &d.Missing, &d.Upgrades} {
		redactDiscover(perms, row)
	}
	if !perms.SeeFiles {
		// Missing and below cutoff are the household's copy shortfalls, not whether something can be watched
		// (perms.go plainAvailability): the rows are empty for users.
		d.Missing = DiscoverRow{Items: []DiscoverItem{}, More: d.Missing.More}
		d.Upgrades = DiscoverRow{Items: []DiscoverItem{}, More: d.Upgrades.More}
	}
	writeJSON(rw, http.StatusOK, d)
}

func redactDiscover(p Perms, row *DiscoverRow) {
	for i := range row.Items {
		redactRowFor(p, &row.Items[i].TitleRow)
	}
}

// GET discover/{row}?limit=&offset=: one Home row in full, paged (limit default 60, at most discoverPageMax).
func (w *Web) apiDiscoverRow(rw http.ResponseWriter, r *http.Request) {
	name := r.PathValue("row")
	if !slices.Contains(discoverRows, name) {
		notFound(rw)
		return
	}
	limit, err := intParam(r, "limit", 60)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	offset, err := intParam(r, "offset", 0)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	if shortfallRows[name] && !w.permsOf(r).SeeFiles {
		writeJSON(rw, http.StatusOK, DiscoverRow{Items: []DiscoverItem{}, More: discoverMore(name)})
		return
	}
	ctx := r.Context()
	db := w.st.Reader()
	picks, err := w.discoverPicks(ctx, db, name)
	if err != nil {
		w.internal(rw, err)
		return
	}
	row, err := w.discoverRow(ctx, db, picks, offset, min(max(limit, 1), discoverPageMax), discoverMore(name))
	if err != nil {
		w.internal(rw, err)
		return
	}
	redactDiscover(w.permsOf(r), &row)
	writeJSON(rw, http.StatusOK, row)
}

func (w *Web) discover(ctx context.Context) (*Discover, error) {
	db := w.st.Reader()
	d := &Discover{}
	for _, name := range discoverRows {
		picks, err := w.discoverPicks(ctx, db, name)
		if err != nil {
			return nil, err
		}
		row, err := w.discoverRow(ctx, db, picks, 0, discoverLimit, discoverMore(name))
		if err != nil {
			return nil, err
		}
		switch name {
		case "recent":
			d.Recent = row
		case "upcoming":
			d.Upcoming = row
		case "missing":
			d.Missing = row
		case "upgrades":
			d.Upgrades = row
		}
	}
	return d, nil
}

// discoverPicks is one row's titles, in the row's order, with the row's note and date for each.
func (w *Web) discoverPicks(ctx context.Context, db *sql.DB, row string) ([]pick, error) {
	switch row {
	case "recent":
		return w.recentPicks(ctx, db)
	case "upcoming":
		return w.upcomingPicks(ctx, db)
	case "missing":
		return w.missingPicks(ctx, db)
	case "upgrades":
		return w.upgradePicks(ctx, db)
	}
	return nil, fmt.Errorf("discover: no row %q", row)
}

// recentPicks: titles by their newest current file, newest first.
func (w *Web) recentPicks(ctx context.Context, db *sql.DB) ([]pick, error) {
	var recent []pick
	err := scanAll(ctx, db, `SELECT t.id, `+addedExpr+` AS added FROM title t WHERE added IS NOT NULL
		ORDER BY added DESC, t.id DESC`, nil, func(r *sql.Rows) error {
		var p pick
		var added int64
		if err := r.Scan(&p.id, &added); err != nil {
			return err
		}
		p.date = ts(added).Local().Format(time.DateOnly)
		recent = append(recent, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return recent, nil
}

// upcomingPicks: the earliest release or air date per title in the next upcomingDays days. A movie counts only when
// its first release of a type falls in the window: TMDb also lists later dates of the same type (a streaming service
// picking up a 2015 film is a "digital" date in 2026), which are not upcoming releases.
func (w *Web) upcomingPicks(ctx context.Context, db *sql.DB) ([]pick, error) {
	now := w.now().UTC()
	today := now.Format(time.DateOnly)
	end := now.AddDate(0, 0, upcomingDays).Format(time.DateOnly)
	first := map[int64]pick{}
	keep := func(p pick) {
		if q, ok := first[p.id]; !ok || p.date < q.date {
			first[p.id] = p
		}
	}
	err := scanAll(ctx, db, `SELECT title_id, type, MIN(date) FROM title_release_date
		WHERE type IN ('digital', 'physical', 'theatrical') AND country IN ('', 'US')
		GROUP BY title_id, type HAVING MIN(date) >= ? AND MIN(date) <= ?`, []any{today, end}, func(r *sql.Rows) error {
		var p pick
		var typ string
		if err := r.Scan(&p.id, &typ, &p.date); err != nil {
			return err
		}
		p.note = map[string]string{"digital": "Digital release", "physical": "Disc release",
			"theatrical": "In cinemas"}[typ]
		keep(p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = scanAll(ctx, db, `SELECT title_id, season, number, name, air_date FROM (
		SELECT e.title_id, e.season, e.number, e.name, e.air_date, ROW_NUMBER() OVER (PARTITION BY e.title_id
			ORDER BY e.air_date, e.season, e.number) AS rn
		FROM episode e WHERE e.season > 0 AND e.air_date >= ? AND e.air_date <= ?) WHERE rn = 1`,
		[]any{today, end}, func(r *sql.Rows) error {
			var p pick
			var season, number int64
			var name string
			if err := r.Scan(&p.id, &season, &number, &name, &p.date); err != nil {
				return err
			}
			p.note = episodeCode(season, number)
			if l := strings.ToLower(name); l != "" && l != "tba" && l != "tbd" && !strings.HasPrefix(l, "episode ") {
				p.note += " · " + name
			}
			keep(p)
			return nil
		})
	if err != nil {
		return nil, err
	}
	var upcoming []pick
	for _, p := range first {
		upcoming = append(upcoming, p)
	}
	sort.Slice(upcoming, func(i, j int) bool {
		if upcoming[i].date != upcoming[j].date {
			return upcoming[i].date < upcoming[j].date
		}
		return upcoming[i].id < upcoming[j].id
	})
	return upcoming, nil
}

// missingPicks: released movies and aired episodes that are monitored and wanted, newest first. A movie is released
// on its home release (availability.HomeRelease over the canonical dates, as list rows' home_release); without one,
// on its earliest US or worldwide digital or disc date, else TMDb's release date plus 90 days, else at the end of its
// year.
func (w *Web) missingPicks(ctx context.Context, db *sql.DB) ([]pick, error) {
	now := w.now().UTC()
	today := now.Format(time.DateOnly)
	var missing, movies []pick
	var ids []any
	err := scanAll(ctx, db, `SELECT t.id, group_concat(v.policy_key), COALESCE(
			(SELECT MIN(d.date) FROM title_release_date d WHERE d.title_id = t.id AND d.type IN ('digital', 'physical')
				AND d.country IN ('', 'US')),
			CASE WHEN m.release_date <> '' THEN date(m.release_date, '+90 days') END,
			CASE WHEN t.year IS NOT NULL THEN printf('%04d-12-31', t.year) END, '')`+titleFrom+`
		JOIN version v ON v.title_id = t.id AND v.state = 'wanted' AND v.monitored = 1
		WHERE t.kind = 'movie' AND t.monitored = 1 GROUP BY t.id`, nil, func(r *sql.Rows) error {
		var p pick
		var keys string
		if err := r.Scan(&p.id, &keys, &p.date); err != nil {
			return err
		}
		var labels []string
		ks := strings.Split(keys, ",")
		names := w.keyLabels(ks)
		for _, k := range ks {
			labels = append(labels, names[k])
		}
		sort.Strings(labels)
		p.note = "Missing " + strings.Join(labels, " and ")
		movies = append(movies, p)
		ids = append(ids, p.id)
		return nil
	})
	if err != nil {
		return nil, err
	}
	dates, err := availability.DatesFor(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	for _, p := range movies {
		if d := availability.HomeRelease(dates[p.id]); !d.IsZero() {
			p.date = d.Format(time.DateOnly)
		}
		if p.date != "" && p.date <= today {
			missing = append(missing, p)
		}
	}
	err = scanAll(ctx, db, `SELECT e.title_id, COUNT(DISTINCT e.id), MAX(e.air_date) FROM episode_version_state s
		JOIN episode e ON e.id = s.episode_id JOIN version v ON v.id = s.version_id JOIN title t ON t.id = e.title_id
		WHERE s.state = 'wanted' AND s.monitored = 1 AND v.monitored = 1 AND t.monitored = 1 AND e.monitored = 1
			AND e.season > 0 AND e.air_date IS NOT NULL AND e.air_date <= ?
		GROUP BY e.title_id`, []any{today}, func(r *sql.Rows) error {
		var p pick
		var n int
		if err := r.Scan(&p.id, &n, &p.date); err != nil {
			return err
		}
		p.note = strconv.Itoa(n) + " episodes missing"
		if n == 1 {
			p.note = "1 episode missing"
		}
		missing = append(missing, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(missing, func(i, j int) bool {
		if missing[i].date != missing[j].date {
			return missing[i].date > missing[j].date
		}
		return missing[i].id > missing[j].id
	})
	return missing, nil
}

// upgradesNote ends an upgrades pick's caption (docs/design/manage.md, Words).
const upgradesNote = ": preferred quality not met"

// upgradePicks: monitored titles with a monitored version below its cutoff, newest file first.
func (w *Web) upgradePicks(ctx context.Context, db *sql.DB) ([]pick, error) {
	var upgrades []pick
	err := scanAll(ctx, db, `SELECT t.id, group_concat(v.policy_key), `+addedExpr+` AS added FROM title t
		JOIN version v ON v.title_id = t.id AND v.state = 'filled' AND v.monitored = 1
		WHERE t.monitored = 1 GROUP BY t.id ORDER BY added IS NULL, added DESC, t.id DESC`, nil, func(r *sql.Rows) error {
		var p pick
		var keys string
		var added sql.NullInt64
		if err := r.Scan(&p.id, &keys, &added); err != nil {
			return err
		}
		var labels []string
		ks := strings.Split(keys, ",")
		names := w.keyLabels(ks)
		for _, k := range ks {
			labels = append(labels, names[k])
		}
		sort.Strings(labels)
		p.note = strings.Join(labels, " and ") + upgradesNote
		upgrades = append(upgrades, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return upgrades, nil
}

// discoverRow loads limit picks from offset as list rows, in order; Total counts every pick.
func (w *Web) discoverRow(ctx context.Context, db *sql.DB, picks []pick, offset, limit int, more string) (DiscoverRow,
	error) {
	row := DiscoverRow{Items: []DiscoverItem{}, Total: len(picks), More: more}
	picks = picks[min(offset, len(picks)):]
	if len(picks) > limit {
		picks = picks[:limit]
	}
	if len(picks) == 0 {
		return row, nil
	}
	ids := make([]any, len(picks))
	for i, p := range picks {
		ids[i] = p.id
	}
	rows, err := w.titleRows(ctx, db, `SELECT `+titleCols+titleFrom+` WHERE t.id IN (`+store.Placeholders(len(ids))+`)`, ids)
	if err != nil {
		return row, err
	}
	byID := map[int64]TitleRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	for _, p := range picks {
		if r, ok := byID[p.id]; ok {
			note := p.note
			// An upgrades pick says "upgrade found" once a better release was actually seen (ui-manage).
			if r.UpgradeSeen && strings.HasSuffix(note, upgradesNote) {
				note = strings.TrimSuffix(note, upgradesNote) + ": upgrade found"
			}
			row.Items = append(row.Items, DiscoverItem{TitleRow: r, Note: note, Date: p.date})
		}
	}
	return row, nil
}
