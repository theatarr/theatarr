package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
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
	Missing int          `json:"missing"` // aired, monitored, no file, in a monitored version
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
	// InfoHash is the current file's torrent, read as the episode window reads it (linkHashSQL), for
	// callers who see files.
	InfoHash string `json:"info_hash,omitempty"`
	// UpgradeSeen: below cutoff, and a better release than its file has been seen.
	UpgradeSeen bool `json:"upgrade_seen,omitempty"`
	// Acquisition is the download in hand for the episode in this version (copies.go), for callers who see files.
	Acquisition *Acquisition `json:"acquisition,omitempty"`
	// Media is the file's media info (linkMedia: Theatarr's probe, else an adopted file's arr media info); Languages
	// and Formats are the languages and matched custom formats its link's file metadata keeps (linkFacts). For
	// callers who see files; with more than one file, all from the newest link.
	Media     *MediaInfo           `json:"media,omitempty"`
	Languages []string             `json:"languages,omitempty"`
	Formats   []decide.FormatScore `json:"formats,omitempty"`
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
	// A title every copy of which was removed answers as one never added (its open requests still read the list).
	in, err := w.titleInLibrary(r.Context(), id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if !in {
		notFound(rw)
		return
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
		e.monitored, COALESCE(
			(SELECT MIN(n.number) FROM episode_number n WHERE n.episode_id = e.id AND n.scheme = 'absolute_tvdb'),
			(SELECT MIN(n.number) FROM episode_number n WHERE n.episode_id = e.id AND n.scheme = 'absolute')),
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
	// The torrent hash as the episode window reads it (episodeFile: linkHashSQL); only for callers who see files.
	// So are the file's probe, adopted media and file metadata (release group, languages, formats: linkFacts).
	linkHash, fileFacts := "''", "NULL, NULL, 0, NULL"
	if seeFiles {
		linkHash, fileFacts = linkHashSQL, "cf.probe, l.adopted_media, l.download_id IS NULL, l.file_metadata"
	}
	// The current files that hold each episode, active links, oldest first: a cell with more than one file ends with
	// the newest link's facts and hash (for Cached on, heldByHash below), the file the episode window shows
	// (episodeFile). A link holding several episodes (a season pack) is parsed once, and only for the season asked for.
	type facts struct {
		media *MediaInfo
		file  CurrentFile
	}
	parsed := map[int64]facts{}
	if err := scanAll(ctx, db, `SELECT l.id, le.episode_id, lv.version_id, COALESCE(l.quality, ''), `+
		linkSizeSQL+`, `+linkScoreSQL+`, COALESCE(l.release_title, ''), l.path, `+linkHash+`,
		`+fileFacts+`
		FROM link l JOIN link_episode le ON le.link_id = l.id JOIN link_version lv ON lv.link_id = l.id
		LEFT JOIN content_file cf ON cf.id = l.content_file_id WHERE l.title_id = ? AND l.state = 'active'
		ORDER BY l.created_at ASC, l.id ASC`,
		[]any{id}, func(r *sql.Rows) error {
			var lid, eid, vid int64
			var q, release, path, hash string
			var size, score sql.NullInt64
			var pr, am, meta sql.NullString
			var adopted bool
			if err := r.Scan(&lid, &eid, &vid, &q, &size, &score, &release, &path, &hash, &pr, &am, &adopted,
				&meta); err != nil {
				return err
			}
			i, ok := idx[eid]
			key := keyOf[vid]
			if !ok || key == "" {
				return nil
			}
			c, ok := all[i].Versions[key]
			if !ok {
				c = EpisodeCell{State: "filled", Monitored: true}
			}
			c.HasFile, c.Quality = true, q
			if seeFiles {
				f, ok := parsed[lid]
				if !ok && (season < 0 || all[i].Season == int64(season)) {
					linkFacts(&f.file, meta.String)
					f.media = linkMedia(pr, am, adopted, release, true)
					if f.media != nil {
						f.media.AudioTracks = nil // the episode window lists the tracks; no list cell reads them
					}
					parsed[lid] = f
				}
				c.SizeBytes, c.Score, c.ReleaseGroup, c.ReleaseTitle, c.Path, c.InfoHash = nullInt(size), nullInt(score),
					f.file.ReleaseGroup, release, path, hash
				c.Media, c.Languages, c.Formats = f.media, f.file.Languages, f.file.Formats
			}
			all[i].Versions[key] = c
			return nil
		}); err != nil {
		return nil, err
	}
	if seeFiles {
		var hashes []string
		for _, e := range all {
			for _, c := range e.Versions {
				hashes = append(hashes, c.InfoHash)
			}
		}
		byHash, err := heldByHash(ctx, db, hashes...)
		if err != nil {
			return nil, err
		}
		for _, e := range all {
			for k, c := range e.Versions {
				if c.HasFile {
					c.CachedOn = w.cacheCodes(byHash[c.InfoHash])
					e.Versions[k] = c
				}
			}
		}
	}

	// A gap in a library that is not monitored is not missing (titleAvailabilitySQL), for those who see files; others
	// keep watchability.
	on := map[string]bool{}
	for _, v := range out.Versions {
		on[v.Key] = v.Monitored
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
			} else if aired && c.Monitored && c.State == "wanted" && (!seeFiles || on[k]) {
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
