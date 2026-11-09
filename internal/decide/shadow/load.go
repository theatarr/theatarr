// Package shadow replays the arrs' grab history through the decision engine (docs/design/shadow.md): for each
// release an arr grabbed, would our engine, with the arr's own profile (a from_arr policy profile), accept it, with
// the quality and score the arr recorded? It reads our database (arr_history from theatarr import-arr) or a collected corpus
// (history.jsonl and parse.jsonl), never a live service.
package shadow

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// Grab is one grabbed release from arr history, with what the decision needs and what the arr recorded.
type Grab struct {
	Instance  string
	HistoryID int
	Date      time.Time
	Release   decide.Release
	Title     decide.Title
	// Existing is the movie's file just before the grab, rebuilt from the import and delete events (database only).
	Existing *decide.ExistingFile
	// Known is whether Existing was rebuilt (false in the corpus, which holds grabs only).
	Known bool

	// What the arr recorded: the grab's quality, the score at grab time (data.customFormatScore, database only),
	// and the score the arr computes now from the record with the current profile (the history API's).
	Quality   string
	GrabScore *int
	NowScore  *int
	// NowFormats are the formats the arr computes now from the record (corpus only).
	NowFormats []string
	// Languages are the languages the arr recorded for the grab (RemoteMovie.Languages after aggregation, which
	// includes what the indexer said).
	Languages []string
	// ArrProfileID is the arr quality profile the movie is on now; 0 when unknown.
	ArrProfileID  int
	MatchType     string
	ReleaseSource string
	Indexer       string // the arr's indexer name
	// InfoHash is the torrent's hash as history recorded it (any case), empty for usenet.
	InfoHash string

	// Kind is movie or series ("" is movie).
	Kind policy.Kind
	// Series: every history row of the grab (one per episode), the episodes the arr grabbed (Title.Episodes ids;
	// arr episode ids in ArrEpisodes, database only), the arr's release type, and the search criteria the decision
	// uses (nil: the RSS path).
	HistoryIDs  []int
	Episodes    []int64
	ArrEpisodes []int
	ReleaseType string
	Search      *decide.SeriesSearch
}

// LoadDB reads one instance's grabs from our database, oldest first. The titles carry the alternative titles,
// translations and secondary year the database holds (title_alias, from theatarr import-arr and --enrich); aliases maps a
// TMDb id to more alternative titles (a database imported before they were stored); it may be nil.
func LoadDB(ctx context.Context, db *sql.DB, instance string, aliases map[int][]string) ([]Grab, error) {
	titles, err := decide.LoadMovies(ctx, db)
	if err != nil {
		return nil, err
	}
	for id, t := range titles {
		for _, a := range aliases[t.TMDb] {
			if !slices.Contains(t.Aliases, a) {
				t.Aliases = append(t.Aliases, a)
			}
		}
		titles[id] = t
	}
	profiles := map[int]int{}
	prows, err := db.QueryContext(ctx, `SELECT arr_id, quality_profile_id FROM arr_title WHERE instance = ?`, instance)
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		var a, p int
		if err := prows.Scan(&a, &p); err != nil {
			prows.Close()
			return nil, err
		}
		profiles[a] = p
	}
	prows.Close()
	rows, err := db.QueryContext(ctx, historySelect+` WHERE instance = ? ORDER BY date_at, arr_id`, instance)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHistory(rows, instance, titles, profiles, nil)
}

const historySelect = `SELECT arr_id, event_type, date_at, arr_title_id, COALESCE(title_id, 0), protocol,
	source_title, quality, custom_format_score, languages, data FROM arr_history`

// LoadGrab reads one grab (by its history id) from our database, with the movie's file just before it rebuilt from
// the movie's import and delete events. titles are LoadMovies' result, aliases filled or not.
func LoadGrab(ctx context.Context, db *sql.DB, instance string, historyID int, titles map[int64]decide.Title) (*Grab, error) {
	var arrTitle, profileID int
	err := db.QueryRowContext(ctx, `SELECT h.arr_title_id, COALESCE(t.quality_profile_id, 0) FROM arr_history h
		LEFT JOIN arr_title t ON t.instance = h.instance AND t.arr_id = h.arr_title_id
		WHERE h.instance = ? AND h.arr_id = ? AND h.event_type = 'grabbed'`, instance, historyID).Scan(&arrTitle, &profileID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%s: no grab with history id %d", instance, historyID)
	}
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, historySelect+` WHERE instance = ? AND arr_title_id = ? ORDER BY date_at, arr_id`,
		instance, arrTitle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grabs, err := scanHistory(rows, instance, titles, map[int]int{arrTitle: profileID}, func(id int) bool {
		return id == historyID
	})
	if err != nil {
		return nil, err
	}
	if len(grabs) == 0 {
		return nil, fmt.Errorf("%s: grab %d is of a movie we do not hold", instance, historyID)
	}
	return &grabs[0], nil
}

// scanHistory replays history rows in order, keeping each movie's file, and returns the grabs keep accepts (all
// when keep is nil).
func scanHistory(rows *sql.Rows, instance string, titles map[int64]decide.Title, profiles map[int]int,
	keep func(id int) bool) ([]Grab, error) {
	files := map[int]*decide.ExistingFile{} // by arr title id
	var out []Grab
	for rows.Next() {
		var (
			id, arrTitle         int
			titleID              int64
			event, proto, source string
			quality, langs, data string
			date                 int64
			score                sql.NullInt64
		)
		if err := rows.Scan(&id, &event, &date, &arrTitle, &titleID, &proto, &source, &quality, &score, &langs,
			&data); err != nil {
			return nil, err
		}
		d := map[string]string{}
		if err := json.Unmarshal([]byte(data), &d); err != nil {
			return nil, fmt.Errorf("arr_history %s/%d: data: %w", instance, id, err)
		}
		var languages []string
		_ = json.Unmarshal([]byte(langs), &languages)
		switch event {
		case "downloadFolderImported":
			f := &decide.ExistingFile{Quality: quality, Revision: 1, SceneName: source, ReleaseGroup: d["releaseGroup"],
				Languages: languages, Size: atoi64(d["size"]), IndexerFlags: flagsValue(d["indexerFlags"])}
			if p := d["importedPath"]; p != "" {
				f.RelativePath = path.Base(p)
			}
			files[arrTitle] = f
		case "movieFileDeleted":
			delete(files, arrTitle)
		case "grabbed":
			t, ok := titles[titleID]
			if !ok || (keep != nil && !keep(id)) {
				continue
			}
			g := Grab{Instance: instance, HistoryID: id, Date: time.UnixMilli(date).UTC(), Title: t, Quality: quality,
				MatchType: d["movieMatchType"], ReleaseSource: d["releaseSource"], Indexer: d["indexer"], Known: true,
				Languages: languages, ArrProfileID: profiles[arrTitle], InfoHash: d["torrentInfoHash"]}
			if score.Valid {
				v := int(score.Int64)
				g.NowScore = &v
			}
			if s, err := strconv.Atoi(d["customFormatScore"]); err == nil {
				g.GrabScore = &s
			}
			if f := files[arrTitle]; f != nil {
				c := *f
				g.Existing = &c
			}
			g.Release = release(source, d, proto == "usenet")
			out = append(out, g)
		}
	}
	return out, rows.Err()
}

// release builds the release as the arr's ReleaseInfo had it, from the grab's history data.
func release(title string, d map[string]string, usenet bool) decide.Release {
	r := decide.Release{Release: indexer.Release{Title: title, Size: atoi64(d["size"]), Seeders: -1, Peers: -1},
		Usenet: usenet}
	if v, err := strconv.Atoi(d["imdbId"]); err == nil && v > 0 {
		r.IMDb = fmt.Sprintf("tt%07d", v)
	}
	r.TMDb, _ = strconv.Atoi(d["tmdbId"])
	if t, err := time.Parse(time.RFC3339, d["publishedDate"]); err == nil {
		r.PublishDate = t
	}
	fl := flagsValue(d["indexerFlags"])
	r.IndexerFlags = &fl
	return r
}

// flagsValue is Enum.TryParse(ignoreCase) of IndexerFlags as history stores them ("G_Freeleech, G_Scene" or "0").
func flagsValue(s string) int {
	v, _ := format.Radarr.IndexerFlagsValue(s)
	return v
}

func atoi64(s string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v
}

// LoadLibrary reads every movie of the database, for indexer ids that name another title.
func LoadLibrary(ctx context.Context, db *sql.DB) (decide.Library, error) {
	titles, err := decide.LoadMovies(ctx, db)
	if err != nil {
		return nil, err
	}
	return decide.NewLibrary(titles), nil
}

// corpus record shapes.
type corpusHistory struct {
	ID                int               `json:"id"`
	Date              time.Time         `json:"date"`
	EventType         string            `json:"eventType"`
	MovieID           int               `json:"movieId"`
	SourceTitle       string            `json:"sourceTitle"`
	Data              map[string]string `json:"data"`
	CustomFormatScore *int              `json:"customFormatScore"`
	Languages         []struct {
		Name string `json:"name"`
	} `json:"languages"`
	CustomFormats []struct {
		Name string `json:"name"`
	} `json:"customFormats"`
	Quality struct {
		Quality struct {
			Name string `json:"name"`
		} `json:"quality"`
	} `json:"quality"`
}

type corpusMovie struct {
	ID               int    `json:"id"`
	Title            string `json:"title"`
	OriginalTitle    string `json:"originalTitle"`
	Year             int    `json:"year"`
	SecondaryYear    int    `json:"secondaryYear"`
	ImdbID           string `json:"imdbId"`
	TmdbID           int    `json:"tmdbId"`
	Runtime          int    `json:"runtime"`
	QualityProfileID int    `json:"qualityProfileId"`
	OriginalLanguage struct {
		Name string `json:"name"`
	} `json:"originalLanguage"`
	AlternateTitles []struct {
		Title string `json:"title"`
	} `json:"alternateTitles"`
}

// corpusMovies reads the movies the arr's parse answers carry (parse.jsonl), by arr movie id.
func corpusMovies(dir, instance string) (map[int]*corpusMovie, error) {
	movies := map[int]*corpusMovie{}
	err := eachLine(filepath.Join(dir, instance, "parse.jsonl"), func(b []byte) error {
		var l struct {
			Body struct {
				Movie *corpusMovie `json:"movie"`
			} `json:"body"`
		}
		if err := json.Unmarshal(b, &l); err != nil {
			return err
		}
		if m := l.Body.Movie; m != nil && movies[m.ID] == nil {
			movies[m.ID] = m
		}
		return nil
	})
	return movies, err
}

// CorpusAliases are the alternative titles of the corpus movies, by TMDb id, for LoadDB.
func CorpusAliases(dir, instance string) (map[int][]string, error) {
	movies, err := corpusMovies(dir, instance)
	if err != nil {
		return nil, err
	}
	out := map[int][]string{}
	for _, m := range movies {
		out[m.TmdbID] = m.aliases()
	}
	return out, nil
}

func (m *corpusMovie) aliases() []string {
	var a []string
	for _, t := range m.AlternateTitles {
		a = append(a, t.Title)
	}
	return a
}

// LoadCorpus reads one instance's grabs from the corpus. The movie of each grab comes from the parse answers; grabs
// of a movie no parse answer names are left out. The corpus has grabs only, so the existing file is unknown.
func LoadCorpus(dir, instance string) ([]Grab, error) {
	movies, err := corpusMovies(dir, instance)
	if err != nil {
		return nil, err
	}
	var out []Grab
	err = eachLine(filepath.Join(dir, instance, "history.jsonl"), func(b []byte) error {
		var h corpusHistory
		if err := json.Unmarshal(b, &h); err != nil {
			return err
		}
		m := movies[h.MovieID]
		if h.EventType != "grabbed" || m == nil {
			return nil
		}
		t := decide.Title{ID: int64(m.ID), Kind: policy.KindMovie, Name: m.Title, Year: m.Year,
			SecondaryYear: m.SecondaryYear, OriginalTitle: m.OriginalTitle, Aliases: m.aliases(), IMDb: m.ImdbID,
			TMDb: m.TmdbID, Runtime: m.Runtime, OriginalLanguage: m.OriginalLanguage.Name}
		g := Grab{Instance: instance, HistoryID: h.ID, Date: h.Date, Title: t, Quality: h.Quality.Quality.Name,
			NowScore: h.CustomFormatScore, Indexer: h.Data["indexer"], ArrProfileID: m.QualityProfileID,
			Release: release(h.SourceTitle, h.Data, h.Data["protocol"] == "1")}
		for _, l := range h.Languages {
			g.Languages = append(g.Languages, l.Name)
		}
		for _, f := range h.CustomFormats {
			g.NowFormats = append(g.NowFormats, f.Name)
		}
		sort.Strings(g.NowFormats)
		out = append(out, g)
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, err
}

func eachLine(p string, fn func([]byte) error) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		if err := fn(sc.Bytes()); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return sc.Err()
}
