package arrview

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/store"
)

// commandResource is CommandResource (src/Radarr.Api.V3/Commands/CommandResource.cs @ a96bf7c). Read by Seerr (it
// only posts), decypharr (pkg/arr/media.go L18-33: id, name, commandName, status, queued, body) and the scripts
// (status, message).
type commandResource struct {
	ID                  int64          `json:"id,omitempty"`
	Name                string         `json:"name"`
	CommandName         string         `json:"commandName"`
	Message             string         `json:"message,omitempty"`
	Body                map[string]any `json:"body"`
	Priority            string         `json:"priority"`
	Status              string         `json:"status"`
	Result              string         `json:"result"`
	Queued              arrTime        `json:"queued"`
	Started             *arrTime       `json:"started,omitempty"`
	Ended               *arrTime       `json:"ended,omitempty"`
	Duration            string         `json:"duration,omitempty"`
	Trigger             string         `json:"trigger"`
	ClientUserAgent     string         `json:"clientUserAgent,omitempty"`
	StateChangeTime     *arrTime       `json:"stateChangeTime,omitempty"`
	SendUpdatesToClient bool           `json:"sendUpdatesToClient"`
	UpdateScheduledTask bool           `json:"updateScheduledTask"`
}

// Commands the views accept, per flavour (the arrs' Command implementations clients use; arr-api-surface notes).
// search marks the ones that would acquire.
var commandNames = map[string]map[string]bool{
	Radarr: {"MoviesSearch": true, "MissingMoviesSearch": true, "CutOffUnmetMoviesSearch": true,
		"RefreshMovie": false, "RescanMovie": false, "RefreshMonitoredDownloads": false, "DownloadedMoviesScan": false,
		"ManualImport": false, "RssSync": true, "RenameMovie": false, "RenameFiles": false},
	Sonarr: {"SeriesSearch": true, "SeasonSearch": true, "EpisodeSearch": true, "MissingEpisodeSearch": true,
		"CutoffUnmetEpisodeSearch": true, "RefreshSeries": false, "RescanSeries": false,
		"RefreshMonitoredDownloads": false, "DownloadedEpisodesScan": false, "ManualImport": false, "RssSync": true,
		"RenameSeries": false, "RenameFiles": false},
}

func canonicalCommand(flavour, name string) (string, bool, bool) {
	for k, search := range commandNames[flavour] {
		if strings.EqualFold(k, name) {
			return k, search, true
		}
	}
	return "", false, false
}

// splitCamel is StringExtensions.SplitCamelCase: "MoviesSearch" -> "Movies Search".
func splitCamel(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		if i > 0 && unicode.IsUpper(r) && (unicode.IsLower(rs[i-1]) || i+1 < len(rs) && unicode.IsLower(rs[i+1])) {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// POST command (CommandController.StartCommand). A search over versions that are all in shadow mode is stored
// completed at once, with a message saying why nothing happened. A search touching an active version is stored
// queued and handed to the Searcher for its active versions only; its status then follows their searches and routes
// (acquire.LoadCommandStatus). Maintenance commands follow their jobs; unsupported commands fail. 201 with the command.
func (s *Service) postCommand(w http.ResponseWriter, r *request) {
	var body map[string]any
	if err := decodeBody(r, &body); err != nil {
		badRequest(w, "", err.Error(), nil)
		return
	}
	name, _ := body["name"].(string)
	canon, search, ok := canonicalCommand(r.v.Flavour, name)
	if !ok {
		badRequest(w, "Name", "Unknown command "+strconv.Quote(name), name)
		return
	}
	id, err := s.runCommand(r.Context(), r.v, canon, body, search, r.Header.Get("User-Agent"))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	c, err := s.loadCommand(r.Context(), id)
	if err != nil || c == nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// runCommand stores a command and starts what it asks for: the searches of its active versions.
func (s *Service) runCommand(ctx context.Context, v *View, canon string, body map[string]any, search bool,
	ua string) (int64, error) {
	body["name"] = canon
	for k := range body {
		if strings.EqualFold(k, "_mmJobs") {
			delete(body, k)
		}
	}
	now := store.Millis(s.now())
	status, result, msg := "completed", "successful", ""
	var runErr error
	var started, ended any = now, now
	var sc *commandScope
	if search {
		var err error
		if sc, err = s.commandScope(ctx, v, canon, body); err != nil {
			return 0, err
		}
		status, result, msg, started, ended = s.searchOutcome(sc.versions, now)
		if canon == "RssSync" {
			if s.rssSync == nil {
				status, result, msg, started, ended = "completed", "successful",
					"Nothing to do: RSS sync is not configured", now, now
			} else if err := s.rssSync(ctx); err != nil {
				status, result, msg, started, ended = "failed", "unsuccessful", err.Error(), now, now
			} else {
				status, result, msg, started, ended = "completed", "successful", "RSS sync queued", now, now
			}
		}
	} else if canon == "ManualImport" {
		var failed bool
		if msg, failed = s.manualImportCommand(ctx, v, body); failed {
			result = "unsuccessful"
			status = "failed"
		}
	} else if strings.HasPrefix(canon, "Rename") {
		msg, runErr = s.renameCommand(ctx, v, body)
		if runErr != nil {
			status, result, msg = "failed", "unsuccessful", runErr.Error()
		}
	} else {
		ids, err := s.queueMaintenance(ctx, v, canon, body)
		if err != nil {
			status, result, msg = "failed", "unsuccessful", err.Error()
		} else {
			body["_mmJobs"] = ids
			status, result, msg, started, ended = "queued", "unknown", "Queued for "+splitCamel(canon), nil, nil
		}
	}
	b, _ := json.Marshal(body)
	var existing int64
	err := s.st.Reader().QueryRowContext(ctx, `SELECT id FROM arrview_command
		WHERE view = ? AND name = ? AND body = ? AND status IN ('queued', 'started') ORDER BY id DESC LIMIT 1`,
		v.Key, canon, string(b)).Scan(&existing)
	if err == nil {
		// The stored status of a search command is only brought up to date when it is read: read it
		// now, so a finished command does not swallow its repeat.
		c, err := s.loadCommand(ctx, existing)
		if err != nil {
			return 0, err
		}
		if c != nil && (c.Status == "queued" || c.Status == "started") {
			return existing, nil
		}
	} else if err != sql.ErrNoRows {
		return 0, err
	}
	if len(ua) > 200 {
		ua = ua[:200]
	}
	res, err := s.st.Writer().ExecContext(ctx, `INSERT INTO arrview_command (view, name, body, status, result,
		message, user_agent, queued_at, started_at, ended_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, v.Key, canon,
		string(b), status, result, msg, ua, now, started, ended)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if status == "queued" && sc != nil && s.searcher != nil {
		if err := s.startSearch(ctx, id, sc); err != nil {
			// A row left queued with no searches would stay queued forever and take every repeat of the same
			// request (the dedupe above): it failed.
			end := store.Millis(s.now())
			if _, uerr := s.st.Writer().ExecContext(context.WithoutCancel(ctx), `UPDATE arrview_command
				SET status = 'failed', result = 'unsuccessful', message = ?, started_at = ?, ended_at = ? WHERE id = ?`,
				"The search could not be started", end, end, id); uerr != nil {
				s.log.Warn("command: marking failed", "id", id, "err", uerr)
			}
			return id, err
		}
	}
	s.log.Info("command recorded", "view", v.Key, "id", id, "name", canon, "status", status)
	return id, nil
}

// TrimCommands removes terminal commands older than one day, matching the arr command repository housekeeping.
func (s *Service) TrimCommands(ctx context.Context, now time.Time) error {
	// Bring the old commands still stored as running up to date first, so the finished ones are trimmed too.
	if _, err := s.loadCommands(ctx, `status IN ('queued', 'started') AND queued_at < ?`,
		store.Millis(now.Add(-24*time.Hour))); err != nil {
		return err
	}
	_, err := s.st.Writer().ExecContext(ctx, `DELETE FROM arrview_command
		WHERE status NOT IN ('queued', 'started') AND COALESCE(ended_at, queued_at) < ?`, store.Millis(now.Add(-24*time.Hour)))
	return err
}

// startSearch hands a queued search to the Searcher, for the active versions only, and notes what it started.
func (s *Service) startSearch(ctx context.Context, id int64, sc *commandScope) error {
	shadow := s.shadowVersions(sc.versions)
	var active []string
	for _, k := range sc.versions {
		if !slices.Contains(shadow, k) {
			active = append(active, k)
		}
	}
	sc.sel.Versions = active
	queued, found, err := s.searcher.Submit(ctx, id, sc.sel)
	if err != nil {
		return err
	}
	now := store.Millis(s.now())
	var msg string
	switch {
	case queued == 0:
		msg = "Nothing to search: no monitored item of the active versions (" + strings.Join(active, ", ") +
			") matches"
	case queued < found:
		msg = fmt.Sprintf("Searching %d of %d items (searched longest ago first) for %s", queued, found,
			strings.Join(active, ", "))
	default:
		msg = fmt.Sprintf("Searching %d items for %s", queued, strings.Join(active, ", "))
	}
	if len(shadow) > 0 {
		msg += "; Watch only, nothing searched for " + strings.Join(shadow, ", ")
	}
	if queued == 0 {
		_, err = s.st.Writer().ExecContext(ctx, `UPDATE arrview_command SET status = 'completed', result = 'successful',
			message = ?, started_at = ?, ended_at = ? WHERE id = ?`, msg, now, now, id)
		return err
	}
	_, err = s.st.Writer().ExecContext(ctx, `UPDATE arrview_command SET message = ? WHERE id = ?`, msg, id)
	return err
}

// commandScope is what a search command reaches: the view's versions it touches, and the selection to search.
type commandScope struct {
	versions []string
	sel      acquire.Selection
}

// commandScope resolves a search command: the versions of the titles or episodes it names, or all the view's
// versions when it names none (MissingMoviesSearch, RssSync); the selection holds the titles, season and episodes.
// A command that names ids none of which resolve gets no versions and no titles (nothing to search).
func (s *Service) commandScope(ctx context.Context, v *View, canon string, body map[string]any) (*commandScope, error) {
	sc := &commandScope{}
	switch canon {
	case "MissingMoviesSearch", "MissingEpisodeSearch":
		sc.sel.Only = acquire.OnlyMissing
	case "CutOffUnmetMoviesSearch", "CutoffUnmetEpisodeSearch":
		// The arrs search their Cutoff Unmet list: files below the quality cutoff only (Radarr
		// MoviesSearchService.Execute(CutoffUnmetMoviesSearchCommand), Sonarr EpisodeSearchService; see wantedCutoff).
		// router.wanted.cutoff_unmet governs Theatarr's own loop only.
		sc.sel.Only, sc.sel.QualityCutoff = acquire.OnlyCutoff, true
	}
	var ids []int64
	// named: the body names titles or episodes (movieIds, seriesId, episodeIds, …). Such a command reaches only
	// what its ids resolve to, never the whole view, even when none do (0, -1, [], stale ids).
	named := false
	addID := func(x any) {
		if f, ok := x.(float64); ok && f > 0 {
			ids = append(ids, int64(f))
		}
	}
	for k, x := range body {
		switch strings.ToLower(k) {
		case "movieids", "seriesids":
			named = true
			if arr, ok := x.([]any); ok {
				for _, y := range arr {
					addID(y)
				}
			}
		case "movieid", "seriesid":
			named = true
			addID(x)
		case "seasonnumber":
			if f, ok := x.(float64); ok && f >= 0 && canon == "SeasonSearch" {
				n := int(f)
				sc.sel.Season = &n
			}
		case "episodeids":
			named = true
			if arr, ok := x.([]any); ok {
				for _, y := range arr {
					if f, ok := y.(float64); ok && f > 0 {
						sc.sel.EpisodeIDs = append(sc.sel.EpisodeIDs, int64(f))
					}
				}
			}
		}
	}
	if len(sc.sel.EpisodeIDs) > 0 {
		es, err := s.entriesForEpisodes(ctx, v, sc.sel.EpisodeIDs)
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			sc.sel.TitleIDs = append(sc.sel.TitleIDs, e.id)
		}
		sc.versions = entryVersions(es, canon == "EpisodeSearch")
		return sc, nil
	}
	if len(ids) == 0 {
		if !named {
			sc.versions = append([]string(nil), v.Versions...)
		}
		return sc, nil
	}
	for _, id := range ids {
		tid := resolveID(id)
		if tid > 0 {
			sc.sel.TitleIDs = append(sc.sel.TitleIDs, tid)
		}
	}
	if len(sc.sel.TitleIDs) == 0 {
		return sc, nil
	}
	es, err := s.load(ctx, v, sc.sel.TitleIDs)
	if err != nil {
		return nil, err
	}
	sc.versions = entryVersions(es, canon == "MoviesSearch")
	return sc, nil
}

// entryVersions are the entries' monitored versions, or all of them for a command that names its movies or episodes
// (MoviesSearch, EpisodeSearch): the arrs search those unmonitored too when a user asks.
func entryVersions(es []*entry, unmonitored bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range es {
		for _, x := range e.versions {
			if (x.monitored || unmonitored) && !seen[x.key] {
				seen[x.key] = true
				out = append(out, x.key)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s *Service) loadCommands(ctx context.Context, where string, args ...any) ([]commandResource, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT id, name, body, status, result, message, user_agent, queued_at,
		started_at, ended_at FROM arrview_command WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	type row struct {
		c              commandResource
		started, ended sql.NullInt64
		jobs           []int64
	}
	var rs []row
	for rows.Next() {
		var x row
		c := &x.c
		var body string
		var queued int64
		if err := rows.Scan(&c.ID, &c.Name, &body, &c.Status, &c.Result, &c.Message, &c.ClientUserAgent, &queued,
			&x.started, &x.ended); err != nil {
			rows.Close()
			return nil, err
		}
		c.CommandName = splitCamel(c.Name)
		c.Priority, c.Trigger = "normal", "manual"
		if c.Name == "ManualImport" {
			c.Priority = "high"
		}
		c.SendUpdatesToClient, c.UpdateScheduledTask = true, true
		c.Queued = at(millis(queued))
		c.Body = map[string]any{}
		_ = json.Unmarshal([]byte(body), &c.Body)
		var refs struct {
			Jobs []int64 `json:"_mmJobs"`
		}
		_ = json.Unmarshal([]byte(body), &refs)
		x.jobs = refs.Jobs
		delete(c.Body, "_mmJobs")
		c.Body["sendUpdatesToClient"] = true
		c.Body["updateScheduledTask"] = true
		c.Body["requiresDiskAccess"] = false
		c.Body["isExclusive"] = false
		c.Body["isTypeExclusive"] = false
		c.Body["isLongRunning"] = false
		c.Body["trigger"] = "manual"
		c.Body["suppressMessages"] = false
		rs = append(rs, x)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	out := []commandResource{}
	for _, x := range rs {
		c, started, ended := x.c, x.started, x.ended
		if c.Status == "queued" || c.Status == "started" {
			var err error
			if len(x.jobs) > 0 {
				err = s.maintenanceStatus(ctx, &c, x.jobs, &started, &ended)
			} else {
				err = s.liveStatus(ctx, &c, &started, &ended)
			}
			if err != nil {
				return nil, err
			}
		}
		if started.Valid {
			c.Started = atPtr(millis(started.Int64))
			c.StateChangeTime = c.Started
		}
		if ended.Valid {
			c.Ended = atPtr(millis(ended.Int64))
			if c.StateChangeTime == nil {
				c.StateChangeTime = c.Ended
			}
			if started.Valid {
				c.Duration = timeSpan(time.Duration(ended.Int64-started.Int64) * time.Millisecond)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// liveStatus brings a queued command up to date from its searches and their routes (acquire_run, the router's job,
// download and plan rows).
func (s *Service) liveStatus(ctx context.Context, c *commandResource, started, ended *sql.NullInt64) error {
	st, err := acquire.LoadCommandStatus(ctx, s.st.Reader(), c.ID)
	if err != nil || st == nil {
		return err
	}
	c.Status = st.Status
	if st.Status != "queued" && st.Message != "" {
		c.Message = st.Message
	}
	switch st.Status {
	case "completed":
		c.Result = "successful"
	case "failed":
		c.Result = "unsuccessful"
	}
	if st.Started != nil {
		*started = sql.NullInt64{Int64: store.Millis(*st.Started), Valid: true}
	}
	if st.Ended != nil {
		*ended = sql.NullInt64{Int64: store.Millis(*st.Ended), Valid: true}
	}
	if c.Status == "queued" || c.Status == "started" {
		return nil
	}
	// Store the final status, as maintenanceStatus does: the dedupe and TrimCommands read the stored one.
	_, err = s.st.Writer().ExecContext(ctx, `UPDATE arrview_command SET status = ?, result = ?, message = ?,
		started_at = ?, ended_at = ? WHERE id = ?`, c.Status, c.Result, c.Message, *started, *ended, c.ID)
	return err
}

// timeSpan writes a duration as .NET's TimeSpan "c" format (STJTimeSpanConverter): hh:mm:ss[.fffffff].
func timeSpan(d time.Duration) string {
	h := int(d / time.Hour)
	m := int(d % time.Hour / time.Minute)
	sec := d % time.Minute
	out := strconv.Itoa(100 + h)[1:] + ":" + strconv.Itoa(100 + m)[1:] + ":" + strconv.Itoa(100 + int(sec/time.Second))[1:]
	if frac := sec % time.Second; frac > 0 {
		out += "." + strconv.Itoa(10000000 + int(frac/100))[1:]
	}
	return out
}

func (s *Service) loadCommand(ctx context.Context, id int64) (*commandResource, error) {
	cs, err := s.loadCommands(ctx, `id = ?`, id)
	if err != nil || len(cs) == 0 {
		return nil, err
	}
	return &cs[0], nil
}

// GET command: the commands of the last day, newest first (the arrs keep theirs in memory for a while).
func (s *Service) commands(w http.ResponseWriter, r *request) {
	cs, err := s.loadCommands(r.Context(), `queued_at >= ? ORDER BY id DESC LIMIT 500`,
		store.Millis(s.now().Add(-24*time.Hour)))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

// GET command/{id}. Ids are global, so any view answers any command.
func (s *Service) command(w http.ResponseWriter, r *request) {
	id, _ := r.pathID(1)
	c, err := s.loadCommand(r.Context(), id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if c == nil {
		notFound(w, "Command with ID "+strconv.FormatInt(id, 10)+" does not exist")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// recordSearch stores the search an add asked for (addOptions.searchForMovie / searchForMissingEpisodes), as if the
// client had posted it.
func (s *Service) recordSearch(ctx context.Context, v *View, name string, body map[string]any, ua string) error {
	_, err := s.runCommand(ctx, v, name, body, true, ua)
	return err
}

// searchOutcome is what a search over these versions does: nothing while they are all in shadow mode (completed,
// with the reason), else it is queued for the searches of the active versions.
func (s *Service) searchOutcome(keys []string, now int64) (status, result, msg string, started, ended any) {
	shadow := s.shadowVersions(keys)
	switch {
	case len(keys) == 0:
		return "completed", "successful", "Nothing to search: no monitored version of this view for these titles", now, now
	case len(shadow) == len(keys):
		return "completed", "successful", "Watch only: nothing searched or grabbed (libraries " +
			strings.Join(shadow, ", ") + ")", now, now
	}
	return "queued", "unknown", "Queued for the searches of the active versions", nil, nil
}
