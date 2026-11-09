package demo

import (
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animestore"
	"github.com/theatarr/theatarr/internal/policy"
)

// FranchiseSlug is the demo's anime franchise.
const FranchiseSlug = "aokaze-requiem"

// anime stores one franchise build through animestore.Save, as theatarr anime build does: the series' two seasons, two
// films, an OVA mapped by a heuristic and a short with conflicting mappings (both open in the review queue), and the
// release, watch and story orders.
func (s *seeder) anime() error {
	series := s.byName["Aokaze Requiem"]
	if series == nil {
		return nil
	}
	movie1, movie2 := s.byName["Aokaze Requiem: The Movie"], s.byName["Aokaze Requiem: Ember of Dawn"]
	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }
	tvdbRange := func(season, from, to int) []anime.SE {
		var out []anime.SE
		for e := from; e <= to; e++ {
			out = append(out, anime.SE{season, e})
		}
		return out
	}
	start := func(t time.Time) *string { v := date(t); return &v }
	s2 := series.first.AddDate(1, 0, 0)
	for _, e := range series.episodes {
		if e.season == 2 && e.number == 1 {
			s2 = e.air
		}
	}
	type spec struct {
		key, title, format, kind string
		id, eps, dur             int
		at                       time.Time
		tvdb                     []anime.SE
		method                   string
		tmdb                     []int
		mapping                  *anime.MappingOutcome
		mainline                 bool
	}
	margin := 1.4
	fit := 0.9
	specs := []spec{
		{key: "al:900300001", title: "Aokaze Requiem", format: "TV", kind: "line", id: AniListBase + 1, eps: 12, dur: 24,
			at: series.first, tvdb: tvdbRange(1, 1, 12), method: "scudlee", mainline: true},
		{key: "al:900300101", title: "Aokaze Requiem: Tidebreak", format: "OVA", kind: "side", id: AniListBase + 101, eps: 1,
			dur: 28, at: series.first.AddDate(0, 5, 0), tvdb: []anime.SE{{0, 1}}, method: "heuristic",
			mapping: &anime.MappingOutcome{Status: anime.StatusHeuristic, Winner: "heuristic",
				Reason: "matched by air date and title only"}},
		{key: "al:900300103", title: "Aokaze Requiem: The Movie", format: "MOVIE", kind: "line", id: AniListBase + 103,
			eps: 1, dur: 112, at: firstOf(movie1, s2.AddDate(1, 0, 0)), tmdb: tmdbOf(movie1), mainline: true},
		{key: "al:900300102", title: "Aokaze Requiem 2nd Season", format: "TV", kind: "line", id: AniListBase + 102, eps: 12,
			dur: 24, at: s2, tvdb: tvdbRange(2, 1, 12), method: "anibridge", mainline: true,
			mapping: &anime.MappingOutcome{Status: anime.StatusArbitrated, Winner: "anibridge", Margin: &margin,
				Reason: "the lists disagree; anibridge fits the air dates",
				Candidates: []anime.MappingCandidate{
					{Source: "anibridge", SeriesID: series.tvdb, Episodes: tvdbRange(2, 1, 12), Range: "S02E01-S02E12",
						Exists: 1, DateFit: &fit, Cover: 1, TitleSim: 0.92, Score: 4.6},
					{Source: "scudlee", SeriesID: series.tvdb, Episodes: tvdbRange(1, 13, 24), Range: "S01E13-S01E24",
						Exists: 0, Cover: 0, TitleSim: 0.92, Score: 3.2, Dropped: "no such TVDB episodes"},
				}}},
		{key: "al:900300105", title: "Aokaze Requiem: Short Breaks", format: "ONA", kind: "side", id: AniListBase + 105,
			eps: 1, dur: 6, at: s2.AddDate(0, 2, 0), tvdb: []anime.SE{{0, 2}}, method: "scudlee",
			mapping: &anime.MappingOutcome{Status: anime.StatusConflict, Winner: "scudlee",
				Reason: "scudlee and anibridge disagree with no clear margin",
				Candidates: []anime.MappingCandidate{
					{Source: "scudlee", SeriesID: series.tvdb, Episodes: []anime.SE{{0, 2}}, Range: "S00E02", Exists: 1,
						Cover: 1, TitleSim: 0.7, Score: 3.1},
					{Source: "anibridge", SeriesID: series.tvdb, Episodes: []anime.SE{{0, 3}}, Range: "S00E03", Exists: 0,
						Cover: 0, TitleSim: 0.7, Score: 2.9},
				}}},
	}
	if movie2 != nil {
		specs = append(specs, spec{key: "al:900300104", title: "Aokaze Requiem: Ember of Dawn", format: "MOVIE", kind: "line",
			id: AniListBase + 104, eps: 1, dur: 118, at: movie2.first, tmdb: tmdbOf(movie2), mainline: true})
	}
	res := &anime.Result{Franchise: "Aokaze Requiem", Slug: FranchiseSlug, SeedAniList: AniListBase + 1,
		Generated: date(s.now), Config: map[string]any{"scope": "franchise"},
		TVDBSeriesList: []anime.TVDBSeries{}, DatasetVersions: map[string]any{"demo": "1"},
		StoryOrder: &anime.StoryOrder{Basis: "watch", MovesFailed: []anime.Move{}}}
	rows := map[string]*anime.Row{}
	for i, sp := range specs {
		e := &anime.Entry{Key: sp.key, Title: str(sp.title), TitleEn: str(sp.title), Names: []string{sp.title},
			AniListID: num(sp.id), MalID: num(sp.id - AniListBase + MALBase), AniDBID: num(sp.id - AniListBase + AniDBBase),
			KitsuID: num(sp.id - AniListBase + KitsuBase), Format: str(sp.format), Status: str("FINISHED"),
			Start: start(sp.at), Episodes: num(sp.eps), DurationMin: num(sp.dur), Origin: "anilist", Kind: sp.kind,
			TMDbMovie: sp.tmdb, Flags: []string{}, Notes: []string{}, Mainline: sp.mainline, Mapping: sp.mapping,
			AniListRelations: []anime.Relation{}}
		if sp.at.After(s.now) {
			e.Status = str("NOT_YET_RELEASED")
		}
		if e.TMDbMovie == nil {
			e.TMDbMovie = []int{}
		}
		var tv *string
		if len(sp.tvdb) > 0 {
			e.TVDB = &anime.TVDBMap{SeriesID: series.tvdb, Episodes: sp.tvdb, Method: sp.method}
			e.Target = "tvdb"
			r := sp.tvdb[0].String()
			if len(sp.tvdb) > 1 {
				r += "-" + sp.tvdb[len(sp.tvdb)-1].String()
			}
			tv = &r
		} else {
			e.Target = "tmdb_movie"
		}
		res.Entries = append(res.Entries, e)
		n := i + 1
		row := &anime.Row{Key: sp.key, Title: str(sp.title), Format: str(sp.format), Date: start(sp.at), TVDB: tv,
			TMDb: sp.tmdb, Flags: []string{}, N: &n}
		if sp.kind == "side" {
			opt := true
			row.Optional = &opt
		}
		rows[sp.key] = row
	}
	// Release order by date; the watch order puts the OVA after the first season and the films after the second.
	sorted := append([]spec(nil), specs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].at.Before(sorted[j].at) })
	var release []string
	for _, sp := range sorted {
		release = append(release, sp.key)
	}
	watch := []string{"al:900300001", "al:900300101", "al:900300102", "al:900300105", "al:900300103", "al:900300104"}
	order := func(keys []string, placedBy string) []*anime.Row {
		var out []*anime.Row
		for i, k := range keys {
			r, ok := rows[k]
			if !ok {
				continue
			}
			c := *r
			n := i + 1
			c.N, c.PlacedBy = &n, placedBy
			out = append(out, &c)
		}
		return out
	}
	res.ReleaseOrder = order(release, "")
	res.WatchOrder = order(watch, "")
	if mv := rowsKey(res.WatchOrder, "al:900300103"); mv != nil {
		mv.TVDBPlacesAfter = "S02E12"
		mv.PlacedBy = "the film is set after the second season's finale"
	}
	res.StoryOrder.Order = order(watch, "")
	res.StoryOrder.Alternates = []*anime.Row{}
	st := &animestore.Store{DB: s.st, Now: func() time.Time { return s.now }}
	if _, err := st.Save(s.ctx, res, "cache", true); err != nil {
		return fmt.Errorf("demo: anime franchise: %w", err)
	}
	// A watch-order playlist, planned only (the library is off).
	_, err := s.st.Writer().ExecContext(s.ctx, `INSERT INTO plex_playlist (franchise, order_kind, route, name, items, plan,
		mode, detail, synced_at, created_at, updated_at) VALUES (?, 'watch', '', 'Aokaze Requiem (watch order)', '[]', ?,
		'dry_run', 'the library is off: planned only', ?, ?, ?)`, FranchiseSlug, jsonOf(watch), ms(s.ago(time.Hour)),
		ms(s.ago(days(10))), ms(s.ago(time.Hour)))
	return err
}

func rowsKey(rows []*anime.Row, key string) *anime.Row {
	for _, r := range rows {
		if r.Key == key {
			return r
		}
	}
	return nil
}

func firstOf(t *title, def time.Time) time.Time {
	if t == nil {
		return def
	}
	return t.first
}

func tmdbOf(t *title) []int {
	if t == nil {
		return []int{}
	}
	return []int{t.tmdb}
}

// users creates the demo accounts and requests with the known demo password.
func (s *seeder) users() error {
	movieKeys := s.versionKeys(policy.KindMovie, false)
	seriesKeys := s.versionKeys(policy.KindSeries, false)
	limitedVersions := []string{movieKeys[0], seriesKeys[0]}
	type account struct {
		username, role string
		autoApprove    bool
		versions       []string
	}
	accounts := []account{
		{username: Username, role: auth.RoleAdmin},
		{username: ManagerUsername, role: auth.RoleManager},
		{username: AutoApproveUsername, role: auth.RoleUser, autoApprove: true},
		{username: LimitedUsername, role: auth.RoleUser, versions: limitedVersions},
	}
	accountIDs := make(map[string]int64, len(accounts))
	for _, a := range accounts {
		if err := auth.ResetPassword(s.ctx, s.st, a.username, Password); err != nil {
			return fmt.Errorf("demo: account %s: %w", a.username, err)
		}
		var versions any
		if a.versions != nil {
			versions = jsonOf(a.versions)
		}
		at := ms(s.now)
		if _, err := s.st.Writer().ExecContext(s.ctx, `UPDATE web_user SET role = ?, permissions = 0, status = 'active',
			auto_approve = ?, request_versions = ?, created_at = ?, updated_at = ?, password_changed_at = ? WHERE username = ?`,
			a.role, a.autoApprove, versions, at, at, at, a.username); err != nil {
			return fmt.Errorf("demo: account %s settings: %w", a.username, err)
		}
		var id int64
		if err := s.st.Reader().QueryRowContext(s.ctx, `SELECT id FROM web_user WHERE username = ?`, a.username).Scan(&id); err != nil {
			return fmt.Errorf("demo: account %s id: %w", a.username, err)
		}
		accountIDs[a.username] = id
	}
	// The demo API keys (shadow.go) belong to the admin: GET /keys lists the caller's own keys.
	if _, err := s.st.Writer().ExecContext(s.ctx, `UPDATE api_key SET created_by = (SELECT id FROM web_user WHERE username = ?)
		WHERE created_by IS NULL`, Username); err != nil {
		return err
	}
	return s.requests(accountIDs, movieKeys[0], seriesKeys[0])
}

func (s *seeder) requests(users map[string]int64, movieVersion, seriesVersion string) error {
	manager, auto, limited := users[ManagerUsername], users[AutoApproveUsername], users[LimitedUsername]
	created := 0
	return s.st.WriteTx(s.ctx, func(tx *sql.Tx) error {
		add := func(owner int64, kind string, tmdbID int, title *title, name string, year int, anime bool,
			versions []string, status, note, reason string, decidedBy int64) (int64, error) {
			created++
			at := s.ago(time.Duration(created) * 24 * time.Hour)
			updated := at.Add(30 * time.Minute)
			var tmdb, tvdb, titleID, titleYear, decider, decidedAt any
			if tmdbID > 0 {
				tmdb = tmdbID
			}
			if year > 0 {
				titleYear = year
			}
			if title != nil {
				tmdb, tvdb, titleID = title.tmdb, title.tvdb, title.id
				name, anime = title.name, title.anime
				if title.year > 0 {
					titleYear = title.year
				}
			}
			if decidedBy > 0 {
				decider, decidedAt = decidedBy, ms(updated)
			}
			res, err := tx.ExecContext(s.ctx, `INSERT INTO request (user_id, kind, tmdb_id, tvdb_id, title_id, name, year,
				anime, poster_path, versions, seasons, note, status, decided_by, decided_at, reason, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?, '[]', ?, ?, ?, ?, ?, ?, ?)`, owner, kind, tmdb, tvdb, titleID,
				name, titleYear, anime, jsonOf(versions), note, status, decider, decidedAt, reason, ms(at), ms(updated))
			if err != nil {
				return 0, err
			}
			id, err := res.LastInsertId()
			return id, err
		}
		merge := func(requestID, userID int64, version, note string) error {
			at := ms(s.ago(time.Hour))
			_, err := tx.ExecContext(s.ctx, `INSERT INTO request_user (request_id, user_id, versions, note, created_at)
				VALUES (?, ?, ?, ?, ?)`, requestID, userID, jsonOf([]string{version}), note, at)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(s.ctx, `UPDATE request SET updated_at = ? WHERE id = ?`, at, requestID)
			return err
		}
		pending, err := add(limited, "movie", 0, s.byName["The Vellichor Tapes"], "", 0, false,
			[]string{movieVersion}, "pending", "", "", 0)
		if err != nil {
			return err
		}
		if err := merge(pending, auto, movieVersion, "I second this request"); err != nil {
			return err
		}
		if _, err := add(auto, "movie", 0, s.byName["Cinderhook"], "", 0, false, []string{movieVersion},
			"approved", "Please add this", "", auto); err != nil {
			return err
		}
		if _, err := add(limited, "movie", TMDbMovieBase+900, nil, "The Glass Orchard", 2024, false,
			[]string{movieVersion}, "declined", "A new release", "Not a fit for the library right now", manager); err != nil {
			return err
		}
		if _, err := add(auto, "series", TMDbTVBase+900, nil, "The Far Meridian", 2023, false,
			[]string{seriesVersion}, "canceled", "Changed our minds", "", auto); err != nil {
			return err
		}
		processing, err := add(limited, "series", 0, s.byName["The Harrowgate Files"], "", 0, false,
			[]string{seriesVersion}, "processing", "The whole series, please", "", manager)
		if err != nil {
			return err
		}
		if err := merge(processing, auto, seriesVersion, "Looking forward to this one"); err != nil {
			return err
		}
		if _, err := add(auto, "movie", 0, s.byName["The Brassmoor Accord"], "", 0, false,
			[]string{movieVersion}, "available", "", "", auto); err != nil {
			return err
		}
		if _, err := add(limited, "movie", 0, s.byName["The Marrowfield Heist"], "", 0, false,
			[]string{movieVersion}, "failed", "", "No provider could complete this request", manager); err != nil {
			return err
		}
		return nil
	})
}
