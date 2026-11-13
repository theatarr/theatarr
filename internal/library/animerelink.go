package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/store"
)

// Re-link after review (docs/design/anime.md): an episode whose franchise mapping was not importable imported through
// Sonarr's numbering. When review accepts the mapping, or a build turns it importable, animestore queues an
// anime_relink job for the TVDB series. The job re-runs the import's resolver (decide.ResolveFile with the current
// franchise mappings) for every active link Theatarr made for the series, and moves the links whose episodes the
// franchise mapping now changes through the change-of-episodes move (reassign.go: the version's naming, the gated
// compare-and-swap rename, episode states, the download's recorded file identity and the Plex scan).
//
// Left alone: links an arr owns or that were adopted from one (no download), links shared by several versions, files
// whose episodes a client or an admin chose, and files the resolver does not place through the franchise (an explicit
// S01E02 or special keeps Sonarr's meaning). A version the gate refuses (library off, a shadow version)
// gets a dry-run plan only. A target episode that already has another file (after every move that frees one: a
// shifted range moves from its free end), or a new name another file already has on disk, is skipped, both files
// stay, and the error inbox names the two paths.

// AnimeRelinkJobKind is the job animestore queues (animemap.RelinkJobKind).
const AnimeRelinkJobKind = animemap.RelinkJobKind

// AnimeRelinked is what a re-link of one series did.
type AnimeRelinked struct {
	Moved    []string // "path: S00E26 → S01E12"
	Planned  []string // the same, as dry runs
	Occupied []string // skipped: the target episode has another file
}

// RegisterAnimeRelinkJob registers the anime_relink handler.
func (l *Library) RegisterAnimeRelinkJob(w *jobs.Worker) {
	w.Handle(AnimeRelinkJobKind, l.HandleAnimeRelink)
}

// HandleAnimeRelink runs a re-link with the library captured when the job started.
func (l *Library) HandleAnimeRelink(ctx context.Context, j *jobs.Job) error {
	var p animemap.RelinkPayload
	if err := j.Decode(&p); err != nil || p.TVDBID <= 0 {
		return jobs.Permanent(fmt.Errorf("anime relink: bad payload %s", j.Payload))
	}
	_, err := l.RelinkAnime(ctx, p.TVDBID)
	return err
}

// RelinkAnime re-checks the links of the series with this TVDB id.
func (l *Library) RelinkAnime(ctx context.Context, tvdbID int) (*AnimeRelinked, error) {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.relinkAnime(pinned, tvdbID)
	}
	return l.relinkAnime(ctx, tvdbID)
}

// relinkCandidate is a link whose episodes the franchise mapping changes.
type relinkCandidate struct {
	linkID     int64
	titleID    int64
	path, rel  string
	version    string
	versionID  int64
	downloadID int64
	how        string
	from, to   []int64
	detail     string
}

func (l *Library) relinkAnime(ctx context.Context, tvdbID int) (*AnimeRelinked, error) {
	db := l.st.Reader()
	out := &AnimeRelinked{}
	var cands []*relinkCandidate
	if err := scanRows(ctx, db, `SELECT l.id, l.path, l.download_id, cf.path, lv.version_id, v.policy_key, l.title_id
		FROM link l JOIN title t ON t.id = l.title_id JOIN link_version lv ON lv.link_id = l.id
		JOIN version v ON v.id = lv.version_id JOIN content_file cf ON cf.id = l.content_file_id
		WHERE t.kind = 'series' AND t.tvdb_id = ? AND l.kind = 'symlink' AND l.state = 'active'
		AND l.download_id IS NOT NULL AND cf.path <> ''
		AND (SELECT COUNT(*) FROM link_version x WHERE x.link_id = l.id) = 1 ORDER BY l.id`, []any{tvdbID},
		func(r *sql.Rows) error {
			c := &relinkCandidate{}
			if err := r.Scan(&c.linkID, &c.path, &c.downloadID, &c.rel, &c.versionID, &c.version, &c.titleID); err != nil {
				return err
			}
			cands = append(cands, c)
			return nil
		}); err != nil {
		return nil, err
	}
	titles := map[[2]int64]*decide.Title{}
	downloads := map[int64]*importDownload{}
	var moves []*relinkCandidate
	for _, c := range cands {
		rel, titleID := c.rel, c.titleID
		d := downloads[c.downloadID]
		if d == nil {
			var err error
			if d, err = loadDownload(ctx, db, l.pol, c.downloadID); err != nil {
				return nil, err
			}
			downloads[c.downloadID] = d
		}
		// a client's or an admin's choice of episodes stays
		if _, ok := d.Manual[rel]; ok || d.Recorded[rel].How == HowManual {
			continue
		}
		key := [2]int64{titleID, c.versionID}
		t := titles[key]
		if t == nil {
			tt, err := decide.LoadTitle(ctx, db, titleID)
			if err != nil {
				return nil, err
			}
			if err := decide.LoadSeriesVersion(ctx, db, &tt, c.versionID); err != nil {
				return nil, err
			}
			t, titles[key] = &tt, &tt
		}
		var files int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT content_file_id) FROM link WHERE download_id = ?`,
			c.downloadID).Scan(&files); err != nil {
			return nil, err
		}
		res, err := decide.ResolveFile(t, decide.FileInput{Path: rel, Release: d.ReleaseTitle, Folder: d.Name,
			OtherVideoFiles: files > 1, Mapping: d.EpisodeMap})
		if err != nil || res.Refused != "" || len(res.EpisodeIDs) == 0 || !franchisePlaced(res.How) {
			continue // only a placement through the franchise moves a file
		}
		cur, err := linkEpisodes(ctx, db, c.linkID)
		if err != nil {
			return nil, err
		}
		for _, e := range cur {
			c.from = append(c.from, e.ID)
		}
		slices.Sort(c.from)
		c.to = slices.Compact(slices.Sorted(slices.Values(res.EpisodeIDs)))
		if slices.Equal(c.from, c.to) {
			continue // the mapping agrees with where it landed
		}
		c.how = res.How
		c.detail = "relinked after review: " + labels(t, c.from) + " → " + labels(t, c.to)
		moves = append(moves, c)
	}

	// A version the gate refuses gets the plan; a root that does not answer retries the job.
	var live []*relinkCandidate
	for _, c := range moves {
		err := l.gate.CheckVersion(c.version)
		if Unavailable(err) {
			return out, err
		}
		if err != nil {
			if rerr := l.record(ctx, nil, action{Kind: "link", Mode: "dry_run", Version: c.version, LinkID: c.linkID,
				DownloadID: c.downloadID, Path: c.path, Detail: c.detail + " (" + err.Error() + ")"}); rerr != nil {
				return out, rerr
			}
			out.Planned = append(out.Planned, c.path+": "+c.detail)
			continue
		}
		live = append(live, c)
	}

	// Moves in turn; one whose target episode another file holds waits for the next pass (a shifted range frees it),
	// and passes repeat while one moves something. What still waits after that is skipped, as is a move whose new
	// name another file already has (on disk or another link): both files stay.
	occupied := map[int64][]string{}
	var taken []*relinkCandidate
	for len(live) > 0 {
		var wait []*relinkCandidate
		progress := false
		for _, c := range live {
			holders, err := l.episodeHolders(ctx, c)
			if err != nil {
				return out, err
			}
			if len(holders) > 0 {
				occupied[c.linkID] = holders
				wait = append(wait, c)
				continue
			}
			delete(occupied, c.linkID)
			r, err := l.reassignEpisodes(ctx, c.linkID, c.to, false, &reassignNote{how: c.how, detail: c.detail})
			var refused *Refused
			switch {
			case Unavailable(err):
				return out, err
			case errors.As(err, &refused) && refused.Reason == refusedTaken:
				occupied[c.linkID] = []string{r.Path}
				taken = append(taken, c)
				continue
			case errors.Is(err, ErrRefused) || errors.Is(err, ErrLinkChanged):
				l.log.Warn("library: anime relink skipped", "link", c.linkID, "path", c.path, "err", err)
				continue
			case err != nil:
				return out, err
			}
			progress = true
			out.Moved = append(out.Moved, r.Path+": "+c.detail)
		}
		live = wait
		if !progress {
			break
		}
	}
	for _, c := range append(live, taken...) {
		holders := occupied[c.linkID]
		out.Occupied = append(out.Occupied, c.path)
		what := "the target episode already has"
		if slices.Contains(taken, c) {
			what = "its new name is already taken by"
		}
		msg := fmt.Sprintf("anime relink skipped (%s): %s stays, %s %s",
			strings.TrimPrefix(c.detail, "relinked after review: "), c.path, what, strings.Join(holders, ", "))
		l.log.Warn("library: "+msg, "link", c.linkID)
		l.reportInbox(errinbox.Event{Source: errinbox.SourceLog, Component: "anime-relink", Level: "error", Message: msg,
			Key: "link:" + strconv.FormatInt(c.linkID, 10), Context: map[string]string{"version": c.version,
				"path": c.path, "other": strings.Join(holders, ", ")}, Time: l.now()})
	}
	if len(out.Moved)+len(out.Planned)+len(out.Occupied) > 0 {
		l.log.Info("library: anime relink", "tvdb", tvdbID, "moved", len(out.Moved), "planned", len(out.Planned),
			"occupied", len(out.Occupied))
	}
	return out, nil
}

// episodeHolders are the other files of the version that hold one of the candidate's target episodes: Theatarr's
// active links and the arr's files.
func (l *Library) episodeHolders(ctx context.Context, c *relinkCandidate) ([]string, error) {
	in := store.Placeholders(len(c.to))
	args := []any{c.versionID, c.linkID}
	for _, id := range c.to {
		args = append(args, id)
	}
	args = append(args, c.versionID)
	for _, id := range c.to {
		args = append(args, id)
	}
	var out []string
	err := scanRows(ctx, l.st.Reader(), `SELECT DISTINCT l.path FROM link l JOIN link_version lv ON lv.link_id = l.id
		JOIN link_episode le ON le.link_id = l.id WHERE lv.version_id = ? AND l.state = 'active' AND l.id <> ?
		AND le.episode_id IN (`+in+`)
		UNION SELECT DISTINCT vf.path FROM version_file vf JOIN version_file_episode fe ON fe.version_file_id = vf.id
		WHERE vf.version_id = ? AND fe.episode_id IN (`+in+`) ORDER BY 1`, args, func(r *sql.Rows) error {
		var p string
		err := r.Scan(&p)
		out = append(out, p)
		return err
	})
	return out, err
}

// reportInbox puts an event in the error inbox (errinbox.Report; tests replace it).
func (l *Library) reportInbox(ev errinbox.Event) {
	if l.inbox != nil {
		l.inbox(ev)
		return
	}
	errinbox.Report(ev)
}

// labels names episodes of t as "S00E26, S00E27" ("none" for none).
func labels(t *decide.Title, ids []int64) string {
	var out []string
	for _, id := range ids {
		label := fmt.Sprintf("episode %d", id)
		for _, e := range t.Episodes {
			if e.ID == id {
				label = fmt.Sprintf("S%02dE%02d", e.Season, e.Number)
				break
			}
		}
		out = append(out, label)
	}
	return episodeList(out)
}
