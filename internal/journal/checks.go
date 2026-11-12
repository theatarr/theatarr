package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
)

// Check codes. docs/journal.md says what each means and what to do about it.
const (
	CheckSize        = "size_outlier"
	CheckLanguage    = "language"
	CheckFileAudio   = "file_audio_language"
	CheckEpisodes    = "episode_mapping"
	CheckTitle       = "title_mismatch"
	CheckYear        = "year_mismatch"
	CheckSample      = "sample_or_extras"
	CheckFileRuntime = "file_runtime"
	CheckFileRes     = "file_resolution"
	CheckLowerScore  = "upgrade_lower_score"
	CheckLowerQual   = "upgrade_lower_quality"
	CheckFailures    = "repeated_failures"
	CheckStuck       = "download_stuck"
	CheckDeadLink    = "dead_link"
	CheckGuard       = "library_guard"
	CheckUnparsed    = "unparseable"
)

// Checks lists every check code.
var Checks = []string{CheckSize, CheckLanguage, CheckFileAudio, CheckEpisodes, CheckTitle, CheckYear, CheckSample,
	CheckFileRuntime, CheckFileRes, CheckLowerScore, CheckLowerQual, CheckFailures, CheckStuck,
	CheckDeadLink, CheckGuard, CheckUnparsed}

func (e *Entry) flag(code, sev, format string, args ...any) {
	e.Flags = append(e.Flags, Flag{Code: code, Severity: sev, Reason: fmt.Sprintf(format, args...)})
}

// releaseKinds are the kinds whose release is a decision to check.
var releaseKinds = []string{KindGrab, KindUpgrade, KindPlan}

// check runs the suspect checks that apply to the entry.
func (b *builder) check(e *Entry) error {
	ti, err := b.title(e.Title.ID)
	if err != nil {
		return err
	}
	if ti != nil && e.Release != nil && e.Release.Title != "" && slices.Contains(releaseKinds, e.Kind) {
		b.checkName(e, ti)
		if err := b.checkSize(e, ti); err != nil {
			return err
		}
		b.checkLanguage(e, ti)
		b.checkUpgrade(e)
	}
	if ti != nil && (e.Kind == KindImport || e.Kind == KindUpgrade || e.Kind == KindGrab) {
		b.checkFile(e, ti)
	}
	switch e.Kind {
	case KindFailure, KindStall, KindBlocklist:
		switch n := e.f.failureCount; {
		case e.f.failuresAfter > 0:
			// Flag the latest failure of a run only: the agent acts once per title and version.
		case n >= 5:
			e.flag(CheckFailures, SevError, "%d failures for this title and version in 30 days", n)
		case n >= 3:
			e.flag(CheckFailures, SevWarn, "%d failures for this title and version in 30 days", n)
		}
	case KindImport:
		b.checkGuardRejects(e)
		if e.f.importNotes != "" {
			e.flag(CheckGuard, SevInfo, "the library's guard noted a file: %s (not refused)", e.f.importNotes)
		}
		if im := e.Import; im != nil && im.State == "waiting_mount" && b.j.now().Sub(e.At) > 6*time.Hour {
			e.flag(CheckStuck, SevWarn, "still waiting for the release on the mount after %s",
				b.j.now().Sub(e.At).Round(time.Hour))
		}
	case KindLink:
		if strings.HasPrefix(e.Summary, "dead (applied)") || strings.HasPrefix(e.Summary, "dead (dry_run)") {
			e.flag(CheckDeadLink, SevWarn, "the link's target is gone and no provider folder holds the file")
		}
	}
	if d := e.Download; d != nil && (e.Kind == KindGrab || e.Kind == KindUpgrade) &&
		slices.Contains([]string{"queued", "adding", "added", "downloading"}, d.State) {
		if age := b.j.now().Sub(d.CreatedAt); age > 24*time.Hour {
			e.flag(CheckStuck, SevWarn, "still %s %s after the grab", d.State, age.Round(time.Hour))
		}
	}
	return nil
}

var (
	sampleRE = regexp.MustCompile(`(?i)(?:^|[^a-z])(sample|trailer|teaser|featurettes?|extras|bonus|behind[ ._-]the[ ._-]scenes|deleted[ ._-]scenes)(?:[^a-z]|$)`)
	nonWord  = regexp.MustCompile(`[^a-z0-9]+`)
)

// checkName compares the release's name with the title: the parsed title (fuzzy), the year, and for a series the
// season and episode numbers against the episodes the series has and the episodes the release was filed under.
func (b *builder) checkName(e *Entry, ti *titleInfo) {
	name := e.Release.Title
	if m := sampleRE.FindStringSubmatch(name); m != nil {
		e.flag(CheckSample, SevWarn, "the release name says %q", strings.ToLower(m[1]))
	}
	if ti.t.Kind == "series" {
		b.checkSeriesName(e, ti, name)
		return
	}
	p := radarr.ParseMovieTitle(name, false)
	if p == nil || len(p.MovieTitles) == 0 {
		e.flag(CheckUnparsed, SevInfo, "the movie parser cannot read the release name")
		return
	}
	best, bestName := 0.0, ""
	for _, rt := range p.MovieTitles {
		if s, n := bestMatch(radarr.CleanMovieTitle(rt), ti.names, radarr.CleanMovieTitle); s > best {
			best, bestName = s, n
		}
	}
	if best < 0.6 {
		how := ""
		if strings.EqualFold(e.f.matchType, "Id") {
			how = "; the arr matched it by an id the indexer gave, not by name" // how mis-tagged releases get in
		}
		e.flag(CheckTitle, SevWarn, "the release names %q; closest title name %q (similarity %.2f)%s",
			p.MovieTitles[0], bestName, best, how)
	}
	if p.Year > 0 && ti.t.Year > 0 && abs(p.Year-ti.t.Year) > 1 && p.Year != ti.t.SecondaryYear {
		e.flag(CheckYear, SevWarn, "the release says %d; the title is from %d", p.Year, ti.t.Year)
	}
}

func (b *builder) checkSeriesName(e *Entry, ti *titleInfo, name string) {
	p := sonarr.ParseTitle(name)
	if p == nil {
		e.flag(CheckUnparsed, SevInfo, "the series parser cannot read the release name")
		return
	}
	if p.SeriesTitle != "" {
		if s, n := bestMatch(sonarr.CleanSeriesTitle(p.SeriesTitle), ti.names, sonarr.CleanSeriesTitle); s < 0.6 {
			e.flag(CheckTitle, SevWarn, "the release names %q; closest series name %q (similarity %.2f)",
				p.SeriesTitle, n, s)
		}
	}
	if len(ti.eps) == 0 || p.IsDaily() {
		return
	}
	sev := func(base string) string {
		if ti.scene || ti.t.SeriesType == "anime" {
			return SevInfo // scene or absolute numbering: release numbers may legitimately differ from TVDB's
		}
		return base
	}
	filed := map[int]bool{}
	var filedLabels []string
	for _, id := range e.f.episodeIDs {
		if ep, ok := ti.eps[id]; ok {
			filed[ep.season] = true
			filedLabels = append(filedLabels, fmt.Sprintf("S%02dE%02d", ep.season, ep.number))
		}
	}
	if p.IsAbsoluteNumbering() && len(p.EpisodeNumbers) == 0 {
		mx := slices.Max(p.AbsoluteEpisodeNumbers)
		if ti.total > 0 && mx > ti.total+2 {
			e.flag(CheckEpisodes, SevWarn, "absolute episode %d, but the series has %d episodes", mx, ti.total)
		}
		return
	}
	s := p.SeasonNumber
	if s > 0 && ti.counts[s] == 0 && !p.IsMultiSeason {
		e.flag(CheckEpisodes, sev(SevError), "the release is season %d; the series has seasons up to %d", s, ti.maxS)
		return
	}
	if p.FullSeason {
		if len(filed) > 0 && !filed[s] {
			e.flag(CheckEpisodes, sev(SevError), "a season %d pack filed under %s", s, strings.Join(seasonList(filed), ", "))
		}
		return
	}
	for _, n := range p.EpisodeNumbers {
		if c := ti.counts[s]; c > 0 && n > c {
			base := SevWarn
			if s == ti.maxS {
				base = SevInfo // the latest season: the episode list may lag the airing
			}
			e.flag(CheckEpisodes, sev(base), "episode S%02dE%02d, but season %d has %d episodes", s, n, s, c)
			return
		}
	}
	if len(filedLabels) > 0 && len(p.EpisodeNumbers) > 0 {
		want := fmt.Sprintf("S%02dE%02d", s, p.EpisodeNumbers[0])
		if !slices.Contains(filedLabels, want) {
			sort.Strings(filedLabels)
			e.flag(CheckEpisodes, sev(SevWarn), "the release names %s but was filed as %s", want,
				strings.Join(filedLabels, " "))
		}
	}
}

func seasonList(m map[int]bool) []string {
	var out []string
	for s := range m {
		out = append(out, "season "+strconv.Itoa(s))
	}
	sort.Strings(out)
	return out
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// bestMatch is the highest similarity of a cleaned name against the title's names, cleaned the same way.
func bestMatch(cleaned string, names []string, cleanFn func(string) string) (float64, string) {
	best, bestName := 0.0, ""
	for _, n := range names {
		if n == "" {
			continue
		}
		c := cleanFn(n)
		var s float64
		switch {
		case c == cleaned:
			return 1, n
		case c != "" && cleaned != "" && (strings.HasPrefix(cleaned, c) || strings.HasPrefix(c, cleaned)):
			// "starwarsepisodeiv" vs "starwars": a subtitle or an edition word left in.
			s = 0.8
		default:
			s = similarity(cleaned, c)
		}
		if s > best {
			best, bestName = s, n
		}
	}
	return best, bestName
}

// similarity is 1 - Levenshtein distance / the longer length, on letters and digits only.
func similarity(a, b string) float64 {
	a, b = nonWord.ReplaceAllString(strings.ToLower(a), ""), nonWord.ReplaceAllString(strings.ToLower(b), "")
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 || len(rb) == 0 {
		return 0
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return 1 - float64(prev[len(rb)])/float64(max(len(ra), len(rb)))
}

// checkSize compares the release's size per minute of runtime with the median of every grab of the same quality
// (the arrs' history and ours).
func (b *builder) checkSize(e *Entry, ti *titleInfo) error {
	r := e.Release
	if r.Size <= 0 || r.Quality == "" || strings.EqualFold(r.Quality, "unknown") {
		return nil
	}
	eps := e.f.episodeIDs
	minutes := ti.runtimeMinutes(eps)
	if ti.t.Kind == "series" && len(eps) == 0 {
		minutes = 0
		if p := sonarr.ParseTitle(r.Title); p != nil && ti.t.Runtime > 0 {
			switch {
			case p.FullSeason && ti.counts[p.SeasonNumber] > 0:
				minutes = ti.counts[p.SeasonNumber] * ti.t.Runtime
			case len(p.EpisodeNumbers) > 0:
				minutes = len(p.EpisodeNumbers) * ti.t.Runtime
			}
		}
	}
	if minutes <= 0 {
		return nil
	}
	stats, err := b.j.sizeStats(b.ctx)
	if err != nil {
		return err
	}
	perMin := float64(r.Size) / (1 << 20) / float64(minutes)
	med, n := stats.median(r.Quality)
	if n < minSamples {
		return nil
	}
	ratio := perMin / med
	what := fmt.Sprintf("%.1f MB/min over %d min; the median %s grab is %.1f MB/min (%d grabs)", perMin, minutes,
		r.Quality, med, n)
	switch {
	case ratio < 0.2:
		e.flag(CheckSize, SevError, "far too small: %s", what)
	case ratio < 0.35:
		e.flag(CheckSize, SevWarn, "small: %s", what)
	case ratio > 4:
		e.flag(CheckSize, SevWarn, "large: %s", what)
	}
	return nil
}

// expectedLanguages is what the version wants: the profile's language (original: the title's), else English and
// the title's original language. strict is true for a profile language.
func (b *builder) expectedLanguages(e *Entry, ti *titleInfo) (langs []string, strict bool) {
	if b.pol != nil {
		if vp := b.pol.Versions[e.Version]; vp != nil {
			if pr := b.pol.Profiles[vp.Profile]; pr != nil {
				switch l := strings.TrimSpace(pr.Language); {
				case strings.EqualFold(l, "original") && ti.t.OriginalLanguage != "":
					return []string{languageName(ti.t.OriginalLanguage)}, true
				case l != "" && !strings.EqualFold(l, "any") && !strings.EqualFold(l, "original"):
					return []string{l}, true
				}
			}
		}
	}
	langs = []string{"English"}
	if o := languageName(ti.t.OriginalLanguage); o != "" && !strings.EqualFold(o, "English") {
		langs = append(langs, o)
	}
	return langs, false
}

// languageName turns an ISO 639-1 code into the arr's language name; names pass through.
func languageName(s string) string {
	if len(s) == 2 {
		if iso := radarr.FindIsoLanguage(strings.ToLower(s)); iso != nil {
			return iso.Language.Name
		}
	}
	return s
}

func threeLetter(name string) string {
	if iso := radarr.FindIsoLanguageByName(name); iso != nil {
		return iso.ThreeLetterCode
	}
	if l, ok := radarr.LanguageByName(name); ok {
		if iso := radarr.GetIsoLanguage(l); iso != nil {
			return iso.ThreeLetterCode
		}
	}
	return ""
}

func (b *builder) checkLanguage(e *Entry, ti *titleInfo) {
	var langs []string
	for _, l := range e.Release.Languages {
		if l != "" && !strings.EqualFold(l, "Unknown") && !strings.EqualFold(l, "Original") {
			langs = append(langs, l)
		}
	}
	if len(langs) == 0 {
		return
	}
	want, strict := b.expectedLanguages(e, ti)
	for _, l := range langs {
		for _, w := range want {
			if strings.EqualFold(l, w) {
				return
			}
		}
	}
	why := "English or the original language"
	if strict {
		why = "the profile's language"
	}
	e.flag(CheckLanguage, SevWarn, "the release is %s; wanted %s (%s)", strings.Join(langs, "/"), strings.Join(want, " or "), why)
}

// qualityRank orders quality names: resolution first, then source.
func qualityRank(q string) int {
	l := strings.ToLower(q)
	res := 0
	for _, r := range []int{2160, 1080, 720, 576, 480} {
		if strings.Contains(l, strconv.Itoa(r)) {
			res = r
			break
		}
	}
	src := 0
	for i, s := range []string{"sdtv", "dvd", "hdtv", "webrip", "webdl", "bluray", "remux"} {
		if strings.Contains(l, s) {
			src = i + 1
		}
	}
	return res*10 + src
}

func (b *builder) checkUpgrade(e *Entry) {
	if e.Kind != KindUpgrade {
		return
	}
	if e.f.prevScore == nil && e.f.prevQuality == "" {
		return
	}
	cur, quality := e.Release.Score, e.Release.Quality
	if r := e.Reasoning; r != nil {
		if r.Chosen != nil {
			s := r.Chosen.Score
			cur = &s
		}
	}
	sev, hand := SevWarn, ""
	if e.Release.How == "InteractiveSearch" {
		sev, hand = SevInfo, " (a hand grab)"
	}
	pq, cq := qualityRank(e.f.prevQuality), qualityRank(quality)
	if pq > 0 && cq > 0 && cq < pq {
		e.flag(CheckLowerQual, sev, "the upgrade lowers the quality: %s → %s%s", e.f.prevQuality, quality, hand)
		return
	}
	if cur != nil && e.f.prevScore != nil && *cur < *e.f.prevScore && (pq == 0 || cq <= pq) {
		e.flag(CheckLowerScore, sev, "the upgrade lowers the format score: %d → %d (%s → %s)%s", *e.f.prevScore,
			*cur, orDash(e.f.prevQuality), orDash(quality), hand)
	}
}

// checkFile checks what a probe or the arr's media info said about the imported file.
func (b *builder) checkFile(e *Entry, ti *titleInfo) {
	if e.f.fileRuntime > 0 {
		want := ti.runtimeMinutes(e.f.episodeIDs)
		if ti.t.Kind == "series" {
			want = ti.t.Runtime
			if len(e.f.episodeIDs) == 1 {
				want = ti.runtimeMinutes(e.f.episodeIDs)
			}
		}
		if want > 0 {
			got := float64(e.f.fileRuntime) / 60
			if got < 0.5*float64(want) {
				sev := SevWarn
				if got < 0.2*float64(want) {
					sev = SevError
				}
				e.flag(CheckFileRuntime, sev, "the file runs %.0f min; the %s runs %d (sample or wrong file?)", got,
					ti.t.Kind, want)
			}
		}
	}
	// Width or height: scope films are narrower, 4:3 films are less wide.
	if w, h := e.f.fileWidth, e.f.fileHeight; w > 0 && e.Release != nil {
		q := strings.ToLower(e.Release.Quality)
		switch {
		case strings.Contains(q, "2160") && w < 3000 && h < 1900:
			e.flag(CheckFileRes, SevWarn, "a %s release holds a %dx%d file", e.Release.Quality, w, h)
		case strings.Contains(q, "1080") && w < 1200 && h < 800:
			e.flag(CheckFileRes, SevWarn, "a %s release holds a %dx%d file", e.Release.Quality, w, h)
		}
	}
	if len(e.f.fileAudio) > 0 {
		want, strict := b.expectedLanguages(e, ti)
		var codes []string
		for _, w := range want {
			if c := threeLetter(w); c != "" {
				codes = append(codes, c)
			}
		}
		known := false
		for _, a := range e.f.fileAudio {
			a = audioCode(a)
			if a == "" || a == "und" || a == "zxx" || a == "mul" {
				continue
			}
			known = true
			if slices.Contains(codes, a) {
				return
			}
		}
		if known && len(codes) > 0 {
			sev := SevInfo // English or the original language: TMDb's original language is sometimes not the spoken one
			if strict {
				sev = SevWarn
			}
			e.flag(CheckFileAudio, sev, "the file's audio is %s; wanted %s", strings.Join(e.f.fileAudio, "/"),
				strings.Join(codes, " or "))
		}
	}
}

// audioCode normalises a stream's language tag to ISO 639-2/B as the arrs' languages map: Norwegian Bokmål and
// Nynorsk count as Norwegian, two-letter tags become three.
func audioCode(a string) string {
	a = strings.ToLower(strings.TrimSpace(a))
	switch a {
	case "nob", "nno":
		return "nor"
	}
	if len(a) == 2 {
		if iso := radarr.FindIsoLanguage(a); iso != nil {
			return iso.ThreeLetterCode
		}
	}
	return a
}

// Size statistics: the median MB per minute per quality, over the arrs' grabs and ours, recomputed every 6 hours.

const minSamples = 15

type sizeStats struct {
	byQuality map[string][]float64
}

func (s *sizeStats) median(q string) (float64, int) {
	v := s.byQuality[strings.ToLower(q)]
	if len(v) == 0 {
		return 0, 0
	}
	return v[len(v)/2], len(v)
}

func (j *Journal) sizeStats(ctx context.Context) (*sizeStats, error) {
	j.mu.Lock()
	if j.sizes != nil && j.now().Sub(j.sizeAt) < 6*time.Hour {
		s := j.sizes
		j.mu.Unlock()
		return s, nil
	}
	j.mu.Unlock()
	s, err := j.computeSizes(ctx)
	if err != nil {
		return nil, err
	}
	j.mu.Lock()
	j.sizes, j.sizeAt = s, j.now()
	j.mu.Unlock()
	return s, nil
}

func (j *Journal) computeSizes(ctx context.Context) (*sizeStats, error) {
	db := j.st.Reader()
	movies, err := decide.LoadMovies(ctx, db)
	if err != nil {
		return nil, err
	}
	series := map[int64]int{}
	rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(runtime_minutes, 0) FROM title WHERE kind = 'series'`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var rt int
		if err := rows.Scan(&id, &rt); err != nil {
			rows.Close()
			return nil, err
		}
		series[id] = rt
	}
	rows.Close()
	s := &sizeStats{byQuality: map[string][]float64{}}
	add := func(title int64, quality string, size int64, eps int) {
		minutes := 0
		if m, ok := movies[title]; ok {
			minutes = m.Runtime
		} else if rt := series[title]; rt > 0 && eps > 0 {
			minutes = rt * eps
		}
		if size <= 0 || minutes <= 0 || quality == "" {
			return
		}
		k := strings.ToLower(quality)
		s.byQuality[k] = append(s.byQuality[k], float64(size)/(1<<20)/float64(minutes))
	}
	rows, err = db.QueryContext(ctx, `SELECT title_id, quality, MAX(CAST(json_extract(data, '$.size') AS INTEGER)),
		COUNT(DISTINCT COALESCE(arr_episode_id, 0)) FROM arr_history WHERE event_type = 'grabbed' AND title_id IS NOT NULL
		GROUP BY instance, COALESCE(download_id, '#' || arr_id)`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var title int64
		var q string
		var size sql.NullInt64
		var eps int
		if err := rows.Scan(&title, &q, &size, &eps); err != nil {
			rows.Close()
			return nil, err
		}
		add(title, q, size.Int64, eps)
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, `SELECT title_id, quality, COALESCE(size_bytes, 0), episodes FROM download`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var title, size int64
		var q, eps string
		if err := rows.Scan(&title, &q, &size, &eps); err != nil {
			return nil, err
		}
		var ids []int64
		_ = json.Unmarshal([]byte(eps), &ids)
		add(title, q, size, max(len(ids), 1))
	}
	for k := range s.byQuality {
		sort.Float64s(s.byQuality[k])
	}
	return s, rows.Err()
}

// checkGuardRejects explains the guard's refusals by what the import did (library import.go): a dry run did nothing;
// a pack whose other files linked keeps the refused episodes wanted (a movie's other files simply linked); only a
// failed import with the library's blocklist row was blocklisted (a hold for review, e.g. a naming collision, was not).
// The re-search is not claimed: it runs later and only while grabbing is on.
func (b *builder) checkGuardRejects(e *Entry) {
	if len(e.f.importRejects) == 0 || e.Import == nil {
		return
	}
	rejects := strings.Join(e.f.importRejects, "; ")
	switch im := e.Import; {
	case im.State == "dry_run":
		e.flag(CheckGuard, SevInfo, "the library's guard would reject a file: %s (dry run: nothing was linked, "+
			"blocklisted or searched)", rejects)
	case e.f.importLinked && e.Title.Kind == "movie":
		e.flag(CheckGuard, SevInfo, "the library's guard rejected a file: %s; the rest of the release linked", rejects)
	case e.f.importLinked:
		e.flag(CheckGuard, SevInfo, "the library's guard rejected a file: %s; the rest of the release linked, so "+
			"its episodes stay wanted without a blocklist or re-search, and the wanted loop can pick this release "+
			"again (refused again each time)", rejects)
	case e.f.importBlocklisted:
		e.flag(CheckGuard, SevInfo, "the library's guard rejected a file: %s (the release was blocklisted)", rejects)
	default:
		e.flag(CheckGuard, SevInfo, "the library's guard rejected a file: %s", rejects)
	}
}
