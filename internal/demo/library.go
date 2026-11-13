package demo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
)

var jsonMarshal = json.Marshal

// Scenarios: what state a title's versions are in.
const (
	scCeilingKnown   = "ceiling_known"
	scCeilingLikely  = "ceiling_likely"
	scCeilingHigher  = "ceiling_higher"
	scCeilingUnknown = "ceiling_unknown"
	scComplete       = "complete"    // every version has its file at the cutoff
	scUpgrade        = "upgrade"     // the first version has a file below its cutoff
	scPartial        = "partial"     // the first version wanted (movies) or seasons part-way (series)
	scMissing        = "missing"     // released, every version wanted, no files
	scUpcoming       = "upcoming"    // not released yet, every version wanted
	scUnmonitored    = "unmonitored" // the title is not monitored; one version off
	scAiring         = "airing"      // series: aired episodes filled, the rest air in the coming weeks
)

type title struct {
	id                int64
	kind              string
	name              string
	year              int
	anime             bool
	genres            []string
	network           string
	scenario          string
	monitored         bool
	tmdb, tvdb        int
	imdb              string
	anilist           int
	first             time.Time // theatrical release or first air date
	digital, physical time.Time // movies
	runtime           int
	cert              string
	rating            float64
	votes             int
	overview, tagline string
	status            string
	poster, backdrop  string
	added             time.Time
	versions          []*version
	seasons           []int
	episodes          []*episode
	arrIDs            map[string]int64 // instance -> arr title id
}

type version struct {
	id        int64
	key       string
	vp        *policy.VersionPolicy
	state     string
	monitored bool
	instance  string
	flavour   string
	native    bool   // filled through Theatarr's own router and library (links), not an arr
	copyOf    string // Theatarr's own copy (key <k>-theatarr): the arr-fed version k it copies
	// files are the arr's episode files of an arr-fed series version by episode id, for its copy to adopt.
	files map[int64]arrFile
}

// arrFile is one file an arr holds: the release it came from and its path in the arr's root.
type arrFile struct {
	rel  *release
	f    relFile
	path string
}

// theatarrCopy returns t's Theatarr copy (version <k>-theatarr) and the arr-fed version k it copies; nil, nil when
// the policy has no such copy for t.
func (t *title) theatarrCopy() (cp, followed *version) {
	for _, v := range t.versions {
		if v.copyOf == "" {
			continue
		}
		for _, f := range t.versions {
			if f.key == v.copyOf {
				return v, f
			}
		}
	}
	return nil, nil
}

type episode struct {
	id             int64
	season, number int
	abs            int
	name           string
	air            time.Time
}

func (e *episode) aired(now time.Time) bool { return !e.air.After(now) }

func (e *episode) se() string { return fmt.Sprintf("S%02dE%02d", e.season, e.number) }

// plan builds the titles in memory: ids, dates, metadata and version states. Nothing is written.
func (s *seeder) plan() error {
	movieKeys := s.versionKeys(policy.KindMovie, false)
	animeMovieKeys := s.versionKeys(policy.KindMovie, true)
	seriesKeys := s.versionKeys(policy.KindSeries, false)
	animeSeriesKeys := s.versionKeys(policy.KindSeries, true)
	if len(movieKeys) == 0 || len(seriesKeys) == 0 {
		return fmt.Errorf("demo: the policy needs at least one movie and one series version (has movies %v, series %v)",
			movieKeys, seriesKeys)
	}
	s.byName = map[string]*title{}
	var id int64
	add := func(t *title, keys []string) {
		id++
		t.id = id
		t.monitored = t.scenario != scUnmonitored
		t.arrIDs = map[string]int64{}
		for _, k := range keys {
			v := &version{key: k, vp: s.pol.Versions[k], monitored: true}
			if base, ok := strings.CutSuffix(k, "-theatarr"); ok && s.pol.Versions[base] != nil {
				v.copyOf = base
			}
			v.instance, v.flavour = s.instanceFor(k)
			v.native = v.instance == ""
			t.versions = append(t.versions, v)
		}
		s.titles = append(s.titles, t)
		s.byName[t.name] = t
	}
	for i, m := range movieSpecs {
		t := &title{kind: "movie", name: m.name, year: m.year, genres: m.genres, scenario: m.scenario,
			tmdb: TMDbMovieBase + i + 1, imdb: fmt.Sprintf("tt%08d", IMDbBase+i+1)}
		s.movieDates(t, i)
		add(t, movieKeys)
	}
	for i, m := range animeMovieSpecs {
		t := &title{kind: "movie", name: m.name, year: m.year, genres: m.genres, scenario: m.scenario, anime: true,
			tmdb: TMDbMovieBase + 500 + i + 1, imdb: fmt.Sprintf("tt%08d", IMDbBase+500+i+1),
			anilist: AniListBase + 500 + i + 1}
		s.movieDates(t, i+len(movieSpecs))
		add(t, animeMovieKeys)
	}
	for i, sp := range seriesSpecs {
		t := &title{kind: "series", name: sp.name, year: sp.year, genres: sp.genres, network: sp.network,
			scenario: sp.scenario, seasons: sp.seasons, tmdb: TMDbTVBase + i + 1, tvdb: TVDBBase + i + 1,
			imdb: fmt.Sprintf("tt%08d", IMDbBase+1000+i+1)}
		s.seriesDates(t, i)
		add(t, seriesKeys)
	}
	for i, sp := range animeSeriesSpecs {
		t := &title{kind: "series", name: sp.name, year: sp.year, genres: sp.genres, network: sp.network,
			scenario: sp.scenario, seasons: sp.seasons, anime: true, tmdb: TMDbTVBase + 500 + i + 1,
			tvdb: TVDBBase + 500 + i + 1, imdb: fmt.Sprintf("tt%08d", IMDbBase+1500+i+1), anilist: AniListBase + i + 1}
		s.seriesDates(t, i+len(seriesSpecs))
		add(t, animeSeriesKeys)
	}
	var epID int64
	for _, t := range s.titles {
		s.meta(t)
		for _, e := range t.episodes {
			epID++
			e.id = epID
		}
		for i, v := range t.versions {
			v.state = s.versionState(t, i)
		}
		if t.scenario == scUnmonitored && len(t.versions) > 1 {
			v := t.versions[len(t.versions)-1]
			v.monitored, v.state = false, "wanted"
		}
		if cp, followed := t.theatarrCopy(); cp != nil {
			cp.monitored = followed.monitored
			cp.state = s.copyState(t, cp, followed)
		}
	}
	return nil
}

// movieDates sets the release dates: past titles in their year, upcoming ones around now (some digital releases
// inside the next 30 days, so Home's Upcoming row and the calendar fill).
func (s *seeder) movieDates(t *title, i int) {
	if t.scenario == scUpcoming {
		offsets := []int{-30, -12, 4, 16, 33, 58, 75}
		th := s.now.AddDate(0, 0, offsets[i%len(offsets)])
		t.first = th
		t.digital = th.AddDate(0, 0, 31+i%5*4)
		t.physical = t.digital.AddDate(0, 0, 21+i%3*7)
		t.year = th.Year()
		t.added = s.between(days(60), days(5))
		return
	}
	t.first = time.Date(t.year, time.Month(1+s.r.IntN(12)), 1+s.r.IntN(27), 0, 0, 0, 0, time.UTC)
	t.digital = t.first.AddDate(0, 0, 40+s.r.IntN(70))
	t.physical = t.digital.AddDate(0, 0, 20+s.r.IntN(50))
	if t.physical.After(s.now) {
		t.physical = s.now.AddDate(0, 0, -3)
	}
	if t.digital.After(s.now) {
		t.digital = s.now.AddDate(0, 0, -10)
	}
	t.added = s.between(days(720), days(30))
}

// seriesDates sets the first air date and every episode's air date: a season a year, an episode a week; airing
// series have their last season part-way, with the rest in the coming weeks.
func (s *seeder) seriesDates(t *title, i int) {
	n := len(t.seasons)
	var premieres []time.Time
	if t.scenario == scAiring {
		last := t.seasons[n-1]
		// How many episodes of the last season have aired: about half, or none for a series not started yet.
		aired := []int{last / 2, last/2 + 1, 2, 0, 5, 1}[i%6]
		lastPremiere := s.now.AddDate(0, 0, -7*aired+1)
		if aired == 0 {
			lastPremiere = s.now.AddDate(0, 0, 4+i%5)
		}
		for k := range n {
			premieres = append(premieres, lastPremiere.AddDate(k-(n-1), 0, 0))
		}
	} else {
		for k := range n {
			premieres = append(premieres, time.Date(t.year+k, time.Month(1+(i*5+k*3)%12), 3+(i*7)%24, 0, 0, 0, 0,
				time.UTC))
		}
	}
	t.first = premieres[0]
	t.year = t.first.Year()
	t.added = s.between(days(700), days(40))
	abs := 0
	for k, count := range t.seasons {
		for e := 1; e <= count; e++ {
			abs++
			air := time.Date(premieres[k].Year(), premieres[k].Month(), premieres[k].Day(), 21, 0, 0, 0, time.UTC).
				AddDate(0, 0, 7*(e-1))
			t.episodes = append(t.episodes, &episode{season: k + 1, number: e, abs: abs, air: air,
				name: s.pick(episodeAdj) + " " + s.pick(episodeNoun)})
		}
	}
	// A special for the long-runners, so season 0 shows.
	if n >= 3 {
		t.episodes = append(t.episodes, &episode{season: 0, number: 1, name: "Behind the " + s.pick(episodeNoun),
			air: premieres[1].AddDate(0, 0, -10)})
	}
}

// versionState is the state version i of t is in.
func (s *seeder) versionState(t *title, i int) string {
	switch t.scenario {
	case scComplete, scUnmonitored:
		return "cutoff_met"
	case scCeilingKnown, scCeilingLikely, scCeilingHigher, scCeilingUnknown:
		if i == 0 {
			return "filled"
		}
		return "cutoff_met"
	case scUpgrade:
		if i == 0 {
			return "filled"
		}
		return "cutoff_met"
	case scPartial:
		if t.kind == "movie" && i > 0 {
			return "cutoff_met"
		}
		return "wanted"
	}
	return "wanted"
}

// episodeState is the state of e in version v of series t.
func (s *seeder) episodeState(t *title, v *version, e *episode) string {
	if v.copyOf != "" {
		return s.copyEpisodeState(t, e)
	}
	return s.arrEpisodeState(t, e)
}

// arrEpisodeState is the state of e in an arr-fed version of t: what the scenario says.
func (s *seeder) arrEpisodeState(t *title, e *episode) string {
	if e.season == 0 {
		if t.scenario == scComplete {
			return "cutoff_met"
		}
		return "wanted"
	}
	if !e.aired(s.now) {
		return "wanted"
	}
	switch t.scenario {
	case scComplete, scAiring:
		return "cutoff_met"
	case scCeilingKnown, scCeilingLikely, scCeilingHigher, scCeilingUnknown:
		return "filled"
	case scUpgrade:
		if e.season == 1 {
			return "filled"
		}
		return "cutoff_met"
	case scPartial:
		last := t.seasons[len(t.seasons)-1]
		if e.season < len(t.seasons) || e.number <= last/2 {
			return "cutoff_met"
		}
		return "wanted"
	}
	return "wanted"
}

// meta fills the TMDb-like metadata: overview, tagline, rating, certification, runtime, status, art paths.
func (s *seeder) meta(t *title) {
	who := s.pick(firstNames) + " " + s.pick(lastNames)
	t.overview = fmt.Sprintf("%s, %s in %s, sets out to %s. %s", who, s.pick(roles), s.pick(places), s.pick(goals),
		s.pick(twists))
	if t.kind == "series" {
		t.overview = fmt.Sprintf("In %s, %s and %s %s try to %s, one season at a time. %s", s.pick(places), who,
			s.pick(firstNames), s.pick(lastNames), s.pick(goals), s.pick(twists))
	}
	t.tagline = s.pick(taglines)
	t.rating = float64(52+s.r.IntN(38)) / 10
	t.votes = 40 + s.r.IntN(9000)
	switch {
	case t.kind == "movie" && t.anime:
		t.runtime = 85 + s.r.IntN(45)
		t.cert = []string{"PG", "PG-13"}[s.r.IntN(2)]
	case t.kind == "movie":
		t.runtime = 88 + s.r.IntN(70)
		t.cert = []string{"G", "PG", "PG-13", "R"}[s.r.IntN(4)]
	case t.anime:
		t.runtime = 24
		t.cert = []string{"TV-PG", "TV-14"}[s.r.IntN(2)]
	default:
		t.runtime = 42 + s.r.IntN(20)
		t.cert = []string{"TV-PG", "TV-14", "TV-MA"}[s.r.IntN(3)]
	}
	switch {
	case t.kind == "movie" && t.first.After(s.now):
		t.status, t.rating, t.votes = "Post Production", 0, 0
	case t.kind == "movie":
		t.status = "Released"
	case t.scenario == scAiring:
		t.status = "Returning Series"
	case t.scenario == scPartial:
		t.status = "Returning Series"
	default:
		t.status = "Ended"
	}
	if s.o.ImagesDir != "" {
		h := sha256.Sum256(fmt.Appendf(nil, "%d/%s", t.id, t.name))
		base := "mmdemo" + hex.EncodeToString(h[:6])
		t.poster, t.backdrop = "/"+base+"p.png", "/"+base+"b.png"
	}
}

// library writes the titles, their metadata, versions, episodes and files.
func (s *seeder) library() {
	s.content = map[string]int64{}
	s.arrTitles, s.arrFileIDs = map[string]int64{}, map[string]int64{}
	s.arrInstances()
	for _, t := range s.titles {
		var tmdb, tvdb any = t.tmdb, nil
		var seriesType any
		if t.kind == "series" {
			tvdb = t.tvdb
			seriesType = "standard"
			if t.anime {
				seriesType = "anime"
			}
		}
		lang := "English"
		if t.anime {
			lang = "Japanese"
		}
		s.exec(`INSERT INTO title (id, kind, name, sort_name, year, tmdb_id, tvdb_id, imdb_id, anime, monitored,
			series_type, runtime_minutes, original_language, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, t.id, t.kind, t.name, sortName(t.name), t.year, tmdb,
			tvdb, t.imdb, b01(t.anime), b01(t.monitored), seriesType, t.runtime, lang, ms(t.added), ms(t.added))
		if t.anilist != 0 {
			s.exec(`INSERT INTO title_external_id (title_id, source, external_id) VALUES (?, 'anilist', ?), (?, 'mal', ?)`,
				t.id, fmt.Sprint(t.anilist), t.id, fmt.Sprint(t.anilist-AniListBase+MALBase))
		}
		s.titleMeta(t)
		s.releaseDates(t)
		for _, v := range t.versions {
			v.id = s.exec(`INSERT INTO version (title_id, policy_key, monitored, state, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?)`, t.id, v.key, b01(v.monitored), v.state, ms(t.added), ms(t.added))
		}
		for _, e := range t.episodes {
			var air, airAt any
			if !e.air.IsZero() {
				air, airAt = date(e.air), ms(e.air)
			}
			s.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date, air_date_utc_at, runtime_minutes,
				monitored, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, e.id, t.id, e.season,
				e.number, e.name, air, airAt, t.runtime, b01(e.season > 0), ms(t.added), ms(t.added))
			if t.anime && e.abs > 0 {
				s.exec(`INSERT INTO episode_number (episode_id, title_id, scheme, season, number, source)
					VALUES (?, ?, 'absolute', NULL, ?, 'tvdb'), (?, ?, 'tvdb', ?, ?, 'tvdb')`, e.id, t.id, e.abs,
					e.id, t.id, e.season, e.number)
			}
			for _, v := range t.versions {
				st := s.episodeState(t, v, e)
				if !v.monitored {
					st = "wanted"
				}
				monitored := e.season > 0 && v.monitored && !(v.copyOf != "" && copyPlans[t.name].unmonitors(e.season))
				s.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, cutoff_met, updated_at)
					VALUES (?, ?, ?, ?, ?, ?)`, v.id, e.id, b01(monitored), st, b01(st == "cutoff_met"), ms(t.added))
			}
		}
		s.files(t)
		s.adopt(t)
	}
}

func (s *seeder) titleMeta(t *title) {
	var hash string
	if t.poster != "" {
		h := sha256.Sum256([]byte(t.poster + t.backdrop))
		hash = hex.EncodeToString(h[:5])
	}
	var rating, votes any
	if t.votes > 0 {
		rating, votes = t.rating, t.votes
	}
	lang := "en"
	if t.anime {
		lang = "ja"
	}
	var last, next string
	if t.kind == "series" {
		for _, e := range t.episodes {
			if e.season == 0 {
				continue
			}
			if e.aired(s.now) {
				last = date(e.air)
			} else if next == "" {
				next = date(e.air)
			}
		}
	}
	refreshed := s.between(days(6), days(1))
	s.exec(`INSERT INTO title_meta (title_id, tmdb_id, overview, tagline, poster_path, backdrop_path, genres,
		runtime_minutes, rating, rating_votes, certification, status, network, original_language, release_date,
		last_air_date, next_air_date, art_hash, refreshed_at, attempted_at, refresh_after)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, t.id, t.tmdb, t.overview, t.tagline,
		t.poster, t.backdrop, jsonOf(t.genres), t.runtime, rating, votes, t.cert, t.status, t.network, lang,
		date(t.first), last, next, hash, ms(refreshed), ms(refreshed), ms(s.now.AddDate(10, 0, 0)))
}

func (s *seeder) releaseDates(t *title) {
	if t.kind != "movie" {
		return
	}
	for _, d := range []struct {
		typ string
		at  time.Time
	}{{"theatrical", t.first}, {"digital", t.digital}, {"physical", t.physical}} {
		s.exec(`INSERT INTO title_release_date (title_id, source, country, type, date, certification)
			VALUES (?, 'tmdb', 'US', ?, ?, ?)`, t.id, d.typ, date(d.at), t.cert)
		// The canonical schedule a TMDb refresh derives (tmdb.ReleaseSchedule: the US date of each type).
		s.exec(`INSERT INTO title_release_date (title_id, source, country, type, date, fetched_at)
			VALUES (?, 'tmdb-art', '', ?, ?, ?)`, t.id, d.typ, date(d.at), ms(s.now))
	}
	s.exec(`UPDATE title_meta SET movie_dates_at = ? WHERE title_id = ?`, ms(s.now), t.id)
	if t.scenario != scUpcoming {
		s.exec(`INSERT INTO title_release_date (title_id, source, country, type, date) VALUES (?, 'tmdb', 'GB', 'theatrical', ?)`,
			t.id, date(t.first.AddDate(0, 0, 7)))
	}
}

// release is one release a file came from.
type release struct {
	name, quality, group string
	hash                 string
	size                 int64
	score                int
	at                   time.Time // grabbed
	files                []relFile
	indexer              string
	langs                []string
}

type relFile struct {
	name string // file name inside the release
	size int64
	ep   *episode
}

// quality is the quality a version's file has, at its cutoff or below it.
func quality(v *version, below bool) string {
	anime := v.vp != nil && v.vp.Anime != nil && *v.vp.Anime
	k := v.key
	switch {
	case v.vp != nil && v.vp.Kind == policy.KindMovie && anime:
		return pickQ(below, "Bluray-1080p", "Remux-1080p")
	case strings.Contains(k, "remux"):
		return pickQ(below, "Bluray-2160p", "Remux-2160p")
	case v.vp != nil && v.vp.Kind == policy.KindMovie:
		return pickQ(below, "WEBDL-1080p", "WEBDL-2160p")
	case anime:
		return pickQ(below, "WEBDL-1080p", "Bluray-1080p")
	}
	return pickQ(below, "WEBDL-1080p", "WEBDL-2160p")
}

func pickQ(below bool, low, high string) string {
	if below {
		return low
	}
	return high
}

func dots(name string) string {
	r := strings.NewReplacer(":", "", "'", "", ",", "", "&", "and", "?", "", "!", "")
	return strings.Join(strings.Fields(r.Replace(name)), ".")
}

func safePath(name string) string {
	return strings.NewReplacer(":", " -", "/", "-", "?", "").Replace(name)
}

// movieRelease names a movie release of quality q.
func (s *seeder) movieRelease(t *title, q string) *release {
	src := s.pick(webSources)
	g := s.pick(movieGroups)
	d := dots(t.name)
	rel := &release{quality: q, group: g, hash: s.hash(), indexer: s.pick([]string{"dmm", "torrentio", "prowlarr-public", "zilean"}),
		langs: []string{"English"}}
	switch q {
	case "Remux-2160p":
		rel.name = fmt.Sprintf("%s.%d.2160p.UHD.BluRay.REMUX.DV.HDR10.HEVC.TrueHD.Atmos.7.1-%s", d, t.year, g)
		rel.size, rel.score = gib(48+s.r.IntN(38)), 2600+s.r.IntN(900)
	case "Bluray-2160p":
		rel.name = fmt.Sprintf("%s.%d.2160p.UHD.BluRay.x265.HDR10.DTS-HD.MA.5.1-%s", d, t.year, g)
		rel.size, rel.score = gib(22+s.r.IntN(14)), 400+s.r.IntN(500)
	case "WEBDL-2160p":
		rel.name = fmt.Sprintf("%s.%d.2160p.%s.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-%s", d, t.year, src, g)
		rel.size, rel.score = gib(12+s.r.IntN(16)), 1500+s.r.IntN(900)
	case "WEBDL-1080p":
		rel.name = fmt.Sprintf("%s.%d.1080p.%s.WEB-DL.DDP5.1.H.264-%s", d, t.year, src, g)
		rel.size, rel.score = gib(5+s.r.IntN(6)), 300+s.r.IntN(400)
	case "Remux-1080p":
		g = s.pick(animeGroups)
		rel.group = g
		rel.name = fmt.Sprintf("%s.%d.1080p.BluRay.REMUX.AVC.FLAC.2.0-%s", d, t.year, strings.ReplaceAll(g, "-", ""))
		rel.size, rel.score = gib(22+s.r.IntN(12)), 1800+s.r.IntN(600)
		rel.langs = []string{"Japanese", "English"}
	default:
		rel.name = fmt.Sprintf("%s.%d.1080p.BluRay.x264.FLAC.2.0-%s", d, t.year, strings.ReplaceAll(s.pick(animeGroups), "-", ""))
		rel.size, rel.score = gib(8+s.r.IntN(6)), 600+s.r.IntN(400)
		rel.langs = []string{"Japanese"}
	}
	rel.size += s.r.Int64N(1 << 30)
	rel.files = []relFile{{name: rel.name + ".mkv", size: rel.size}}
	return rel
}

// seriesRelease names a release of the given episodes: a season pack when eps is a whole season, else one episode.
func (s *seeder) seriesRelease(t *title, q string, eps []*episode, pack bool) *release {
	d := dots(t.name)
	src := s.pick(webSources)
	rel := &release{quality: q, hash: s.hash(), indexer: s.pick([]string{"dmm", "torrentio", "prowlarr-public"}),
		langs: []string{"English"}}
	res := "2160p"
	if strings.Contains(q, "1080p") {
		res = "1080p"
	}
	per := gib(3 + s.r.IntN(5))
	if res == "1080p" {
		per = gib(1+s.r.IntN(2)) + s.r.Int64N(1<<29)
	}
	if t.anime {
		g := s.pick(animeGroups)
		rel.group = g
		rel.langs = []string{"Japanese", "English"}
		if pack {
			kind := "BD 1080p HEVC FLAC"
			if strings.HasPrefix(q, "WEB") {
				kind = "WEB 1080p AVC AAC"
			}
			rel.name = fmt.Sprintf("[%s] %s - S%02d (%s) [Dual Audio]", g, t.name, eps[0].season, kind)
		} else {
			rel.name = fmt.Sprintf("[%s] %s - %02d (1080p) [%08X]", g, t.name, eps[0].abs, s.r.Uint32())
		}
		for _, e := range eps {
			fn := fmt.Sprintf("[%s] %s - %02d (1080p) [%08X].mkv", g, t.name, e.abs, s.r.Uint32())
			if !pack {
				fn = rel.name + ".mkv"
			}
			rel.files = append(rel.files, relFile{name: fn, size: per + s.r.Int64N(1<<28), ep: e})
		}
	} else {
		g := s.pick(seriesGroups)
		rel.group = g
		if pack {
			rel.name = fmt.Sprintf("%s.S%02d.%s.%s.WEB-DL.DDP5.1.DV.HDR.H.265-%s", d, eps[0].season, res, src, g)
		} else {
			rel.name = fmt.Sprintf("%s.%s.%s.%s.WEB-DL.DDP5.1.H.265-%s", d, eps[0].se(), res, src, g)
		}
		for _, e := range eps {
			fn := fmt.Sprintf("%s.%s.%s.%s.WEB-DL.DDP5.1.H.265-%s.mkv", d, e.se(), res, src, g)
			rel.files = append(rel.files, relFile{name: fn, size: per + s.r.Int64N(1<<28), ep: e})
		}
	}
	for _, f := range rel.files {
		rel.size += f.size
	}
	rel.score = 1200 + s.r.IntN(900)
	if strings.Contains(q, "1080p") && !t.anime {
		rel.score = 200 + s.r.IntN(400)
	}
	return rel
}

func gib(n int) int64 { return int64(n) << 30 }

// mediaInfo is the arr's mediaInfo for a file of quality q.
func mediaInfo(q string, runtime int, anime bool) map[string]any {
	m := map[string]any{"audioBitrate": 0, "audioChannels": 5.1, "audioCodec": "EAC3 Atmos", "audioLanguages": "eng",
		"audioStreamCount": 1, "videoBitDepth": 10, "videoBitrate": 0, "videoCodec": "HEVC",
		"videoDynamicRangeType": "DV HDR10", "videoFps": 23.976, "resolution": "3840x2160",
		"runTime": fmt.Sprintf("%d:%02d:%02d", runtime/60, runtime%60, 17), "scanType": "Progressive",
		"subtitles": "eng/spa/fre"}
	switch {
	case strings.HasPrefix(q, "Remux-2160"):
		m["audioCodec"], m["audioChannels"], m["audioStreamCount"] = "TrueHD Atmos", 7.1, 3
	case strings.HasPrefix(q, "Bluray-2160"):
		m["audioCodec"], m["videoDynamicRangeType"] = "DTS-HD MA", "HDR10"
	case strings.Contains(q, "1080p"):
		m["resolution"], m["videoCodec"], m["videoBitDepth"], m["videoDynamicRangeType"] = "1920x1080", "x264", 8, ""
	}
	if anime {
		m["audioLanguages"], m["audioCodec"], m["audioChannels"], m["audioStreamCount"] = "jpn/eng", "FLAC", 2.0, 2
		m["subtitles"] = "eng"
	}
	return m
}

// probeOf is the library probe of a file of quality q.
func probeOf(q string, runtime int, anime bool) probe.Result {
	v := &probe.Video{Codec: "hevc", Width: 3840, Height: 2160, BitDepth: 10, Primaries: "bt2020", Transfer: "smpte2084"}
	if strings.Contains(q, "1080p") {
		v = &probe.Video{Codec: "h264", Width: 1920, Height: 1080, BitDepth: 8, Primaries: "bt709", Transfer: "bt709"}
	}
	a := []probe.Audio{{Codec: "eac3", Channels: 6, Language: "eng", Tag: "ec+3"}}
	if strings.HasPrefix(q, "Remux-2160") {
		a = []probe.Audio{{Codec: "truehd", Channels: 8, Language: "eng", Tag: "thd+"}, {Codec: "ac3", Channels: 6, Language: "eng"}}
	}
	if anime {
		a = []probe.Audio{{Codec: "flac", Channels: 2, Language: "jpn"}, {Codec: "flac", Channels: 2, Language: "eng"}}
	}
	return probe.Result{Container: "matroska", RunTimeSeconds: float64(runtime*60 + 17), Video: v, Audio: a,
		Subtitles: []string{"eng"}, BytesRead: 4 << 20}
}

// files writes the files a title's versions hold: arr files (with their content and arr records) for versions an
// arr instance owns. Versions Theatarr fills itself get their links from imported downloads (downloads.go).
func (s *seeder) files(t *title) {
	for i, v := range t.versions {
		if v.native {
			continue
		}
		if v.state == "wanted" && t.kind == "movie" {
			s.arrTitle(t, v)
			continue
		}
		s.arrTitle(t, v)
		s.exec(`INSERT OR IGNORE INTO version_source (version_id, instance, arr_id) VALUES (?, ?, ?)`, v.id, v.instance,
			t.arrIDs[v.instance])
		if t.kind == "movie" {
			below := v.state == "filled"
			q := quality(v, below)
			if strings.HasPrefix(t.scenario, "ceiling_") {
				q = "WEBDL-2160p"
			}
			rel := s.movieRelease(t, q)
			rel.at = s.fileTime(t, i)
			s.arrFiles(t, v, rel, below)
			continue
		}
		// Series: a season pack per complete aired season, single episodes for a season part-way.
		for season := 1; season <= len(t.seasons); season++ {
			var have []*episode
			full := true
			for _, e := range t.episodes {
				if e.season != season {
					continue
				}
				if st := s.episodeState(t, v, e); st != "wanted" {
					have = append(have, e)
				} else {
					full = false
				}
			}
			if len(have) == 0 {
				continue
			}
			below := t.scenario == scUpgrade && season == 1 || strings.HasPrefix(t.scenario, "ceiling_")
			q := quality(v, below)
			if full {
				rel := s.seriesRelease(t, q, have, true)
				// Grabbed a few days after the season ended, or (an older season) when the arr was set up.
				rel.at = have[len(have)-1].air.Add(days(2 + float64(s.r.IntN(30))))
				if early := s.between(days(420), days(20)); rel.at.Before(early) {
					rel.at = early
				}
				if rel.at.After(s.now) {
					rel.at = s.ago(days(1))
				}
				s.arrFiles(t, v, rel, below)
				continue
			}
			for _, e := range have {
				// The newest aired episode of an airing series comes from Theatarr's own router (downloads), into
				// Theatarr's copy when there is one, else into this version.
				if cp, _ := t.theatarrCopy(); cp == nil && t.scenario == scAiring && e == s.latestAired(t) {
					continue
				}
				rel := s.seriesRelease(t, q, []*episode{e}, false)
				rel.at = e.air.Add(time.Duration(3+s.r.IntN(20)) * time.Hour)
				if rel.at.After(s.now) {
					rel.at = s.ago(time.Hour)
				}
				s.arrFiles(t, v, rel, below)
			}
		}
	}
}

// fileTime is when a movie version's file was grabbed: spread over the last year, some in the last two weeks (the
// shadow report's window and Home's Recently added).
func (s *seeder) fileTime(t *title, i int) time.Time {
	if (t.id+int64(i))%4 == 0 {
		return s.between(days(13), days(0.2))
	}
	at := s.between(days(400), days(15))
	if at.Before(t.digital) && t.digital.Before(s.now) {
		at = t.digital.Add(days(1 + float64(s.r.IntN(20))))
	}
	if at.After(s.now) {
		at = s.ago(days(16))
	}
	return at
}

// latestAired is the newest aired regular episode of a series (nil when none aired).
func (s *seeder) latestAired(t *title) *episode {
	var last *episode
	for _, e := range t.episodes {
		if e.season > 0 && e.aired(s.now) {
			last = e
		}
	}
	return last
}

// arrTitle writes the arr instance's record of a title once.
func (s *seeder) arrTitle(t *title, v *version) {
	if _, ok := t.arrIDs[v.instance]; ok {
		return
	}
	s.arrTitles[v.instance]++
	arrID := s.arrTitles[v.instance]
	t.arrIDs[v.instance] = arrID
	folder := path.Join(v.vp.Library, s.folder(t))
	status := "released"
	if t.kind == "series" {
		status = map[bool]string{true: "continuing", false: "ended"}[t.status == "Returning Series"]
	} else if t.first.After(s.now) {
		status = "announced"
	}
	stype := ""
	if t.kind == "series" {
		stype = map[bool]string{true: "anime", false: "standard"}[t.anime]
	}
	s.exec(`INSERT INTO arr_title (instance, arr_id, kind, title_id, version_id, monitored, quality_profile_id,
		minimum_availability, status, path, root_folder_path, tags, added_at, series_type)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, '[]', ?, ?)`, v.instance, arrID, t.kind, t.id, v.id, b01(v.monitored),
		map[bool]string{true: "released", false: ""}[t.kind == "movie"], status, folder, v.vp.Library, ms(t.added), stype)
	s.exec(`INSERT INTO arr_alias (instance, kind, arr_id, target_id, created_at) VALUES (?, ?, ?, ?, ?)`, v.instance,
		t.kind, arrID, t.id, ms(t.added))
}

func (s *seeder) folder(t *title) string {
	if t.kind == "movie" {
		return fmt.Sprintf("%s (%d) {imdb-%s}", safePath(t.name), t.year, t.imdb)
	}
	return fmt.Sprintf("%s (%d)", safePath(t.name), t.year)
}

// libraryPath is where a file of rel sits in the version's library root.
func (s *seeder) libraryPath(t *title, v *version, rel *release, f relFile) string {
	if t.kind == "movie" {
		return path.Join(v.vp.Library, s.folder(t), fmt.Sprintf("%s (%d) {imdb-%s} - [%s]-%s.mkv", safePath(t.name),
			t.year, t.imdb, rel.quality, strings.ReplaceAll(rel.group, " ", "")))
	}
	e := f.ep
	season := fmt.Sprintf("Season %02d", e.season)
	if e.season == 0 {
		season = "Specials"
	}
	return path.Join(v.vp.Library, s.folder(t), season, fmt.Sprintf("%s - %s - %s [%s].mkv", safePath(t.name), e.se(),
		e.name, rel.quality))
}

// arrFiles writes the files of rel as the arr holds them, with the content registry rows behind them.
func (s *seeder) arrFiles(t *title, v *version, rel *release, below bool) {
	cid := s.contentFor(rel, s.pick([]string{"rd", "rd", "rd", "ad", "tb"}))
	imported := rel.at.Add(time.Duration(4+s.r.IntN(50)) * time.Minute)
	if imported.After(s.now) {
		imported = s.now.Add(-time.Minute)
	}
	langs := jsonOf(rel.langs)
	for _, f := range rel.files {
		s.arrFileIDs[v.instance]++
		p := s.libraryPath(t, v, rel, f)
		rt := ""
		if t.kind == "series" {
			rt = map[bool]string{true: "seasonPack", false: "singleEpisode"}[len(rel.files) > 1]
		}
		if f.ep != nil {
			if v.files == nil {
				v.files = map[int64]arrFile{}
			}
			v.files[f.ep.id] = arrFile{rel: rel, f: f, path: p}
		}
		vfID := s.exec(`INSERT INTO version_file (version_id, title_id, instance, arr_file_id, path, relative_path,
			size_bytes, quality, release_group, scene_name, languages, media_info, custom_format_score,
			original_file_path, cutoff_not_met, added_at, download_id, release_title, content_id, release_type)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, v.id, t.id, v.instance, s.arrFileIDs[v.instance], p,
			strings.TrimPrefix(p, path.Join(v.vp.Library, s.folder(t))+"/"), f.size, rel.quality, rel.group,
			strings.TrimSuffix(f.name, ".mkv"), langs, jsonOf(mediaInfo(rel.quality, t.runtime, t.anime)), rel.score,
			path.Join(rel.name, f.name), b01(below), ms(imported), strings.ToUpper(rel.hash), rel.name, cid, rt)
		if f.ep != nil {
			s.exec(`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (?, ?)`, vfID, f.ep.id)
			if st := s.episodeState(t, v, f.ep); st == "filled" {
				s.exec(`UPDATE episode_version_state SET current_score = ? WHERE version_id = ? AND episode_id = ?`,
					rel.score, v.id, f.ep.id)
			}
		}
	}
	s.exec(`INSERT OR IGNORE INTO content_version (content_id, version_id, release_title, source, grabbed_at, imported_at)
		VALUES (?, ?, ?, 'arr_history', ?, ?)`, cid, v.id, rel.name, ms(rel.at), ms(imported))
}

// contentFor writes (once) the registry content of a release: the content, its files with their probe, and the
// provider holding it.
func (s *seeder) contentFor(rel *release, provider string) int64 {
	if id, ok := s.content[rel.hash]; ok {
		return id
	}
	first := rel.at.Add(-time.Minute)
	id := s.exec(`INSERT INTO content (info_hash, name, mount_folder, size_bytes, state, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, 'present', ?, ?)`, rel.hash, rel.name, rel.name, rel.size, ms(first), ms(s.ago(days(0.25))))
	s.content[rel.hash] = id
	for _, f := range rel.files {
		runtime := 0
		anime := strings.HasPrefix(rel.name, "[") || strings.Contains(rel.quality, "1080p") && len(rel.langs) > 1
		if f.ep != nil {
			runtime = 45
			if anime {
				runtime = 24
			}
		} else {
			runtime = 100 + s.r.IntN(40)
		}
		s.exec(`INSERT INTO content_file (content_id, path, size_bytes, probe, probed_at) VALUES (?, ?, ?, ?, ?)`, id,
			f.name, f.size, jsonOf(probeOf(rel.quality, runtime, anime)), ms(rel.at.Add(10*time.Minute)))
	}
	s.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, folder_name, seen_at, state, status,
		progress, size_bytes, links, added_at) VALUES (?, ?, ?, ?, ?, 'present', 'downloaded', 100, ?, ?, ?)`, id,
		provider, s.torrentID(provider), rel.name, ms(first), rel.size, len(rel.files), ms(first))
	return id
}

func (s *seeder) torrentID(provider string) string {
	switch provider {
	case "rd":
		const abc = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
		b := make([]byte, 13)
		for i := range b {
			b[i] = abc[s.r.IntN(len(abc))]
		}
		return string(b)
	case "ad":
		return fmt.Sprint(300000000 + s.r.IntN(90000000))
	}
	return fmt.Sprint(9000000 + s.r.IntN(900000))
}

func sortName(n string) string {
	l := strings.ToLower(n)
	for _, p := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(l, p) {
			return strings.TrimPrefix(l, p)
		}
	}
	return l
}

func b01(b bool) int {
	if b {
		return 1
	}
	return 0
}
