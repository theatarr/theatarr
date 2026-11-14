package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"
)

// GET titles/{id}/episodes?season=: a series' seasons with counts per version, and its episodes (all, or one
// season's) with a cell per version: the episode_version_state row and the current file that holds the episode.

// EpisodeVersion is a version column of the episode grid.
type EpisodeVersion struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	Label     string `json:"label"`
	Monitored bool   `json:"monitored"`
	// Control and Cutoff are the version's controls and the cutoff its files are measured against (manage.go).
	Control *VersionControl `json:"control,omitempty"`
	Cutoff  *Cutoff         `json:"cutoff,omitempty"`
}

// SeasonCounts is one version's episodes in one season.
type SeasonCounts struct {
	Upgrade *UpgradeInfo `json:"upgrade,omitempty"`
	Have    int          `json:"have"`    // episodes with a file
	Missing int          `json:"missing"` // aired, monitored, no file
	// Total: episodes aired or held in the version, what Have is of (as EpisodeCounts.Total on the title page).
	Total int `json:"total"`
	// BelowCutoff: episodes whose file is below the cutoff; UpgradeSeen: of them, with a better release seen;
	// Monitored: episodes monitored in the version. Quality is what most files below cutoff have (manage.go).
	BelowCutoff int    `json:"below_cutoff"`
	UpgradeSeen int    `json:"upgrade_seen"`
	Monitored   int    `json:"monitored"`
	Quality     string `json:"quality,omitempty"`
	// For callers who see files: the season's file size in the version and the format score most files below
	// cutoff have.
	SizeBytes *int64 `json:"size_bytes,omitempty"`
	Score     *int64 `json:"score,omitempty"`
	// InFlight: episodes with a download in hand for the version; Stages: of them, how many are at each acquisition
	// stage (copies.go). For callers who see files.
	InFlight int            `json:"in_flight,omitempty"`
	Stages   map[string]int `json:"stages,omitempty"`
	// NextSearchAt is from when the season's next automatic search in the version may start, the earliest time of
	// its Wanted rows (nextSearches, manage.go; now or earlier: due), and NextSearchWhy why it is early. Unset when
	// Wanted gives the season no time (as for a movie copy's control). Schedules are per season: there is no time per
	// episode.
	NextSearchAt  *time.Time `json:"next_search_at,omitempty"`
	NextSearchWhy string     `json:"next_search_why,omitempty"`
}

// Season is one season in brief.
type Season struct {
	Season   int64                   `json:"season"`
	Episodes int                     `json:"episodes"`
	Aired    int                     `json:"aired"`
	Versions map[string]SeasonCounts `json:"versions"`
}

// EpisodeCell is one episode in one version.
type EpisodeCell struct {
	Upgrade   *UpgradeInfo `json:"upgrade,omitempty"`
	State     string       `json:"state"`
	Monitored bool         `json:"monitored"`
	HasFile   bool         `json:"has_file"`
	Quality   string       `json:"quality,omitempty"`
	// For callers who see files.
	SizeBytes    *int64 `json:"size_bytes,omitempty"`
	Score        *int64 `json:"score,omitempty"`
	ReleaseGroup string `json:"release_group,omitempty"`
	ReleaseTitle string `json:"release_title,omitempty"`
	Path         string `json:"path,omitempty"`
	// CachedOn is the cache codes (rd, ad, tb; cacheCodes) of the providers that hold the episode's link's content
	// now (providerHolds), for callers who see files.
	CachedOn []string `json:"cached_on,omitempty"`
	// InfoHash is the current file's torrent, read as the episode window reads it (linkHashSQL, fileHashSQL), for
	// callers who see files.
	InfoHash string `json:"info_hash,omitempty"`
	// UpgradeSeen: below cutoff, and a better release than its file has been seen.
	UpgradeSeen bool `json:"upgrade_seen,omitempty"`
	// Acquisition is the download in hand for the episode in this version (copies.go), for callers who see files.
	Acquisition *Acquisition `json:"acquisition,omitempty"`
}

// Episode is one episode with its cells by version key.
type Episode struct {
	ID       int64  `json:"id"`
	Season   int64  `json:"season"`
	Number   int64  `json:"number"`
	Absolute *int64 `json:"absolute,omitempty"`
	Name     string `json:"name"`
	AirDate  string `json:"air_date,omitempty"`
	// Aired: the air instant has passed; for an episode without an instant, its air date is today (UTC) or earlier
	// (airClock).
	Aired          bool                   `json:"aired"`
	RuntimeMinutes *int64                 `json:"runtime_minutes,omitempty"`
	Monitored      bool                   `json:"monitored"`
	Versions       map[string]EpisodeCell `json:"versions"`
}

// EpisodeList is the answer of GET titles/{id}/episodes.
type EpisodeList struct {
	TitleID  int64            `json:"title_id"`
	Versions []EpisodeVersion `json:"versions"`
	Seasons  []Season         `json:"seasons"`
	Episodes []Episode        `json:"episodes"`
}

func (w *Web) apiTitleEpisodes(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	season := -1
	if v := r.URL.Query().Get("season"); v != "" {
		n, err := intParam(r, "season", 0)
		if err != nil {
			badRequest(rw, "season must be a non-negative integer")
			return
		}
		season = n
	}
	out, err := w.titleEpisodes(r.Context(), id, season, w.permsOf(r).SeeFiles)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	if !canManage(w.permsOf(r)) {
		redactEpisodeCeilings(out)
	}
	if !w.permsOf(r).SeeFiles {
		redactEpisodes(out)
	}
	writeJSON(rw, http.StatusOK, out)
}

// titleEpisodes lists the episodes of title id; season < 0 means every season.
func (w *Web) titleEpisodes(ctx context.Context, id int64, season int, seeFiles bool) (*EpisodeList, error) {
	db := w.st.Reader()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM title WHERE id = ?`, id).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errNotFound
	}
	out := &EpisodeList{TitleID: id, Versions: []EpisodeVersion{}, Seasons: []Season{}, Episodes: []Episode{}}
	keyOf := map[int64]string{}
	// A version removed from the library (liveVersion) has no column; its cells are dropped with it.
	if err := scanAll(ctx, db, `SELECT v.id, v.policy_key, v.monitored FROM version v WHERE v.title_id = ? AND `+
		liveVersion+` ORDER BY v.policy_key`,
		[]any{id}, func(r *sql.Rows) error {
			var v EpisodeVersion
			if err := r.Scan(&v.ID, &v.Key, &v.Monitored); err != nil {
				return err
			}
			v.Label = w.versionLabel(v.Key)
			keyOf[v.ID] = v.Key
			out.Versions = append(out.Versions, v)
			return nil
		}); err != nil {
		return nil, err
	}
	var all []Episode
	idx := map[int64]int{}
	clock := newAirClock(w.now())
	if err := scanAll(ctx, db, `SELECT e.id, e.season, e.number, e.name, COALESCE(e.air_date, ''), e.runtime_minutes,
		e.monitored, (SELECT MIN(n.number) FROM episode_number n WHERE n.episode_id = e.id AND n.scheme = 'absolute'),
		e.air_date_utc_at
		FROM episode e WHERE e.title_id = ? ORDER BY e.season, e.number`, []any{id}, func(r *sql.Rows) error {
		var e Episode
		var rt, abs, airAt sql.NullInt64
		if err := r.Scan(&e.ID, &e.Season, &e.Number, &e.Name, &e.AirDate, &rt, &e.Monitored, &abs, &airAt); err != nil {
			return err
		}
		e.Aired = clock.aired(airAt, e.AirDate)
		e.RuntimeMinutes, e.Absolute = nullInt(rt), nullInt(abs)
		e.Versions = map[string]EpisodeCell{}
		idx[e.ID] = len(all)
		all = append(all, e)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanAll(ctx, db, `SELECT s.episode_id, s.version_id, s.state, s.monitored FROM episode_version_state s
		JOIN version v ON v.id = s.version_id WHERE v.title_id = ?`, []any{id}, func(r *sql.Rows) error {
		var eid, vid int64
		var c EpisodeCell
		if err := r.Scan(&eid, &vid, &c.State, &c.Monitored); err != nil {
			return err
		}
		if i, ok := idx[eid]; ok && keyOf[vid] != "" {
			all[i].Versions[keyOf[vid]] = c
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// The current files that hold each episode: arr files, then active links (which win).
	setFile := func(eid, vid int64, quality string, size, score sql.NullInt64, group, release, path, hash string) {
		i, ok := idx[eid]
		key := keyOf[vid]
		if !ok || key == "" {
			return
		}
		c, ok := all[i].Versions[key]
		if !ok {
			c = EpisodeCell{State: "filled", Monitored: true}
		}
		c.HasFile, c.Quality = true, quality
		if seeFiles {
			c.SizeBytes, c.Score, c.ReleaseGroup, c.ReleaseTitle, c.Path, c.InfoHash = nullInt(size), nullInt(score), group,
				release, path, hash
		}
		all[i].Versions[key] = c
	}
	// The torrent hash as the episode window reads it (episodeFile: linkHashSQL, fileHashSQL), over the title's
	// versions.
	vids := make([]int64, 0, len(keyOf))
	for vid := range keyOf {
		vids = append(vids, vid)
	}
	versions, err := json.Marshal(vids)
	if err != nil {
		return nil, err
	}
	if err := scanAll(ctx, db, uniqueLinkedTargetHash+`SELECT fe.episode_id, vf.version_id, vf.quality, vf.size_bytes,
		vf.custom_format_score, vf.release_group, COALESCE(NULLIF(vf.release_title, ''), vf.scene_name), vf.path,
		`+fileHashSQL+` FROM version_file vf
		JOIN version_file_episode fe ON fe.version_file_id = vf.id`+fileHashJoin+`
		LEFT JOIN version_source vs ON vs.version_id = vf.version_id
		WHERE vf.version_id IN (SELECT id FROM version WHERE title_id = ?) AND `+curFile,
		[]any{string(versions), id}, func(r *sql.Rows) error {
			var eid, vid int64
			var q, group, release, path, hash string
			var size, score sql.NullInt64
			if err := r.Scan(&eid, &vid, &q, &size, &score, &group, &release, &path, &hash); err != nil {
				return err
			}
			setFile(eid, vid, q, size, score, group, release, path, hash)
			return nil
		}); err != nil {
		return nil, err
	}
	// Links: the release group their file metadata keeps (decide.ExistingFile), and the providers holding their
	// content now.
	linkMeta := `CASE WHEN json_valid(l.file_metadata) THEN json_extract(l.file_metadata, '$.ReleaseGroup') END`
	if err := scanAll(ctx, db, uniqueLinkedTargetHash+`SELECT le.episode_id, lv.version_id, COALESCE(l.quality, ''), `+
		linkSizeSQL+`, l.score, COALESCE(`+linkMeta+`, ''), COALESCE(l.release_title, ''), l.path, `+linkHashSQL+`
		FROM link l JOIN link_episode le ON le.link_id = l.id JOIN link_version lv ON lv.link_id = l.id
		LEFT JOIN content_file cf ON cf.id = l.content_file_id`+linkHashJoin+`WHERE l.title_id = ? AND l.state = 'active'`,
		[]any{string(versions), id}, func(r *sql.Rows) error {
			var eid, vid int64
			var q, group, release, path, hash string
			var size, score sql.NullInt64
			if err := r.Scan(&eid, &vid, &q, &size, &score, &group, &release, &path, &hash); err != nil {
				return err
			}
			setFile(eid, vid, q, size, score, group, release, path, hash)
			return nil
		}); err != nil {
		return nil, err
	}
	if seeFiles {
		held := map[[2]int64][]string{}
		if err := scanAll(ctx, db, `SELECT DISTINCT le.episode_id, lv.version_id, cp.provider FROM link l
			JOIN link_episode le ON le.link_id = l.id JOIN link_version lv ON lv.link_id = l.id
			JOIN content_provider cp ON cp.content_id = `+linkContentSQL+`
			WHERE l.title_id = ? AND l.state = 'active' AND `+providerHolds+` ORDER BY 1, 2, 3`,
			[]any{id}, func(r *sql.Rows) error {
				var eid, vid int64
				var p string
				if err := r.Scan(&eid, &vid, &p); err != nil {
					return err
				}
				k := [2]int64{eid, vid}
				held[k] = append(held[k], p)
				return nil
			}); err != nil {
			return nil, err
		}
		for k, ps := range held {
			i, ok := idx[k[0]]
			key := keyOf[k[1]]
			if !ok || key == "" {
				continue
			}
			if c, ok := all[i].Versions[key]; ok {
				c.CachedOn = w.cacheCodes(ps)
				all[i].Versions[key] = c
			}
		}
	}

	seasons := map[int64]*Season{}
	for _, e := range all {
		s := seasons[e.Season]
		if s == nil {
			s = &Season{Season: e.Season, Versions: map[string]SeasonCounts{}}
			for _, v := range out.Versions {
				s.Versions[v.Key] = SeasonCounts{}
			}
			seasons[e.Season] = s
		}
		s.Episodes++
		aired := e.Aired
		if aired {
			s.Aired++
		}
		for k, c := range e.Versions {
			sc := s.Versions[k]
			if aired || c.HasFile {
				sc.Total++
			}
			if c.HasFile {
				sc.Have++
			} else if aired && c.Monitored && c.State == "wanted" {
				sc.Missing++
			}
			s.Versions[k] = sc
		}
		if season < 0 || e.Season == int64(season) {
			out.Episodes = append(out.Episodes, e)
		}
	}
	for _, s := range seasons {
		out.Seasons = append(out.Seasons, *s)
	}
	// Specials last, as Sonarr and Plex list them.
	sort.Slice(out.Seasons, func(i, j int) bool {
		a, b := out.Seasons[i].Season, out.Seasons[j].Season
		if (a == 0) != (b == 0) {
			return b == 0
		}
		return a < b
	})
	if err := w.enrichEpisodes(ctx, db, out, seeFiles); err != nil {
		return nil, err
	}
	return out, w.fillEpisodeAcquisition(ctx, db, out)
}
