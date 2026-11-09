package naming

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestGoldenDB names every arr file in `theatarr import-arr` databases and compares with the arr's own path. Opt-in: it
// skips unless THEATARR_LIBRARY_DB lists databases (comma-separated) and THEATARR_CORPUS points at the corpus (for each
// instance's naming config, quality definitions and custom formats). THEATARR_GOLDEN_WRITE=1 also rewrites the committed
// sample in testdata/golden from the cases that match.
func TestGoldenDB(t *testing.T) {
	dbs, corpus := os.Getenv("THEATARR_LIBRARY_DB"), os.Getenv("THEATARR_CORPUS")
	if dbs == "" || corpus == "" {
		t.Skip("THEATARR_LIBRARY_DB and THEATARR_CORPUS not set")
	}
	byInstance := map[string][]goldenCase{}
	for _, fn := range strings.Split(dbs, ",") {
		cases, err := loadDBCases(fn, corpus)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cases {
			byInstance[c.Instance] = append(byInstance[c.Instance], c)
		}
	}
	var insts []string
	for k := range byInstance {
		insts = append(insts, k)
	}
	sort.Strings(insts)
	for _, inst := range insts {
		raw, err := os.ReadFile(filepath.Join(corpus, inst, "snapshot", "config-naming.json"))
		if err != nil {
			t.Fatal(err)
		}
		c, err := ParseArrConfig(raw)
		if err != nil {
			t.Fatal(err)
		}
		var st goldenStats
		var keep []goldenCase
		for _, gc := range byInstance[inst] {
			r := runGolden(&c, &gc)
			if st.add(&gc, r) {
				keep = append(keep, gc)
			}
		}
		t.Logf("%s: %d files, %d in the naming format's shape; file names equal %d (%.1f%%); folders equal %d of %d (%.1f%%)",
			inst, st.total, st.renamed, st.fileEqual, 100*float64(st.fileEqual)/float64(max(st.renamed, 1)),
			st.folderEqual, st.total, 100*float64(st.folderEqual)/float64(max(st.total, 1)))
		var kinds []string
		for k := range st.diffs {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			ds := st.diffs[k]
			sort.Strings(ds)
			t.Logf("  %s differs: %d", k, len(ds))
			for i, d := range ds {
				if i >= 40 {
					break
				}
				t.Logf("    %s", d)
			}
		}
		if os.Getenv("THEATARR_GOLDEN_WRITE") == "1" {
			writeSample(t, inst, raw, keep)
		}
	}
}

// writeSample keeps up to 100 matching cases per instance, chosen with a fixed seed, preferring the variety of
// tokens (custom formats, editions, multi-episode files, propers).
func writeSample(t *testing.T, inst string, naming []byte, cases []goldenCase) {
	r := rand.New(rand.NewSource(1))
	var special, rest []goldenCase
	for _, c := range cases {
		interesting := c.CFSource == "name" || len(c.Eps) > 1 || (c.MFile != nil && (c.MFile.Edition != "" ||
			c.MFile.Quality.Version > 1)) || (c.EFile != nil && c.EFile.Quality.Version > 1)
		if interesting {
			special = append(special, c)
		} else {
			rest = append(rest, c)
		}
	}
	r.Shuffle(len(special), func(i, j int) { special[i], special[j] = special[j], special[i] })
	r.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
	out := append(special[:min(len(special), 60)], rest[:min(len(rest), 100-min(len(special), 60))]...)
	sort.Slice(out, func(i, j int) bool { return out[i].FileID < out[j].FileID })
	for i := range out {
		// No instance format uses subtitle languages; leave the long lists out of the fixture.
		if out[i].MFile != nil && out[i].MFile.MediaInfo != nil {
			out[i].MFile.MediaInfo.Subtitles = nil
		}
		if out[i].EFile != nil && out[i].EFile.MediaInfo != nil {
			out[i].EFile.MediaInfo.Subtitles = nil
		}
	}
	b, err := json.MarshalIndent(goldenFile{Instance: inst, Naming: naming, Cases: out}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("testdata", "golden", inst+".json"), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

type snapshotData struct {
	qualityTitle map[string]string
	cfNames      []string // includeCustomFormatWhenRenaming, by id
}

func loadSnapshot(corpus, inst string) (*snapshotData, error) {
	s := &snapshotData{qualityTitle: map[string]string{}}
	var qd []struct {
		Title   string `json:"title"`
		Quality struct {
			Name string `json:"name"`
		} `json:"quality"`
	}
	b, err := os.ReadFile(filepath.Join(corpus, inst, "snapshot", "qualitydefinition.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &qd); err != nil {
		return nil, err
	}
	for _, q := range qd {
		s.qualityTitle[q.Quality.Name] = q.Title
	}
	var cfs []struct {
		ID      int    `json:"id"`
		Name    string `json:"name"`
		Include bool   `json:"includeCustomFormatWhenRenaming"`
	}
	b, err = os.ReadFile(filepath.Join(corpus, inst, "snapshot", "customformat.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &cfs); err != nil {
		return nil, err
	}
	sort.Slice(cfs, func(i, j int) bool { return cfs[i].ID < cfs[j].ID })
	for _, c := range cfs {
		if c.Include {
			s.cfNames = append(s.cfNames, c.Name)
		}
	}
	return s, nil
}

type apiMediaInfo struct {
	AudioChannels         float64 `json:"audioChannels"`
	AudioCodec            string  `json:"audioCodec"`
	AudioLanguages        string  `json:"audioLanguages"`
	Subtitles             string  `json:"subtitles"`
	VideoBitDepth         int     `json:"videoBitDepth"`
	VideoCodec            string  `json:"videoCodec"`
	VideoDynamicRange     string  `json:"videoDynamicRange"`
	VideoDynamicRangeType string  `json:"videoDynamicRangeType"`
}

func splitLangs(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "/")
}

// mediaInfoFromAPI turns the arr's MediaInfoResource (already formatted) into MediaInfo.
func mediaInfoFromAPI(raw string) *MediaInfo {
	if raw == "" || raw == "{}" {
		return nil
	}
	var a apiMediaInfo
	if json.Unmarshal([]byte(raw), &a) != nil {
		return nil
	}
	return &MediaInfo{VideoCodec: a.VideoCodec, VideoBitDepth: a.VideoBitDepth, AudioCodec: a.AudioCodec,
		AudioChannels: a.AudioChannels, AudioLanguages: splitLangs(a.AudioLanguages), Subtitles: splitLangs(a.Subtitles),
		VideoDynamicRange: a.VideoDynamicRange, VideoDynamicRangeType: a.VideoDynamicRangeType}
}

// cfFromName reads the custom formats back from the arr's file name: the bracket right before "[<Quality Full>]".
func cfFromName(name, qualityFull string) ([]string, bool) {
	i := strings.Index(name, "["+qualityFull+"]")
	if i <= 0 || name[i-1] != ']' {
		return nil, false
	}
	j := strings.LastIndex(name[:i-1], "[")
	if j < 0 {
		return nil, false
	}
	inner := name[j+1 : i-1]
	if inner == "3D" {
		return nil, false
	}
	return []string{inner}, true
}

func loadDBCases(fn, corpus string) ([]goldenCase, error) {
	db, err := sql.Open("sqlite", "file:"+fn+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx := context.Background()
	snaps := map[string]*snapshotData{}
	snap := func(inst string) (*snapshotData, error) {
		if s, ok := snaps[inst]; ok {
			return s, nil
		}
		s, err := loadSnapshot(corpus, inst)
		snaps[inst] = s
		return s, err
	}
	seriesType := "''"
	var n int
	if db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('arr_title') WHERE name = 'series_type'`).Scan(&n) == nil && n > 0 {
		seriesType = "at.series_type"
	}
	rows, err := db.QueryContext(ctx, `
		SELECT vf.id, vf.instance, ai.flavour, vf.path, vf.relative_path, vf.quality, vf.quality_revision, vf.release_group,
		       vf.scene_name, vf.edition, vf.media_info, vf.original_file_path, at.path, at.root_folder_path, `+seriesType+`,
		       t.name, COALESCE(t.year, 0), COALESCE(t.imdb_id, ''), COALESCE(t.tmdb_id, 0), COALESCE(t.tvdb_id, 0)
		FROM version_file vf
		JOIN arr_instance ai ON ai.name = vf.instance
		JOIN arr_title at ON at.instance = vf.instance AND at.title_id = vf.title_id
		JOIN title t ON t.id = vf.title_id
		ORDER BY vf.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []goldenCase
	for rows.Next() {
		var gc goldenCase
		var flavour, p, rel, quality, group, scene, edition, mi, orig, tpath, root, stype, name, imdb string
		var rev, year, tmdb, tvdb int
		if err := rows.Scan(&gc.FileID, &gc.Instance, &flavour, &p, &rel, &quality, &rev, &group, &scene, &edition, &mi, &orig,
			&tpath, &root, &stype, &name, &year, &imdb, &tmdb, &tvdb); err != nil {
			return nil, err
		}
		s, err := snap(gc.Instance)
		if err != nil {
			return nil, err
		}
		qt := s.qualityTitle[quality]
		if qt == "" {
			qt = quality
		}
		q := Quality{Title: qt, Version: rev}
		gc.Ext = path.Ext(p)
		gc.RootDir = root
		gc.ExpectFolder = path.Base(tpath)
		base := path.Base(p)
		media := mediaInfoFromAPI(mi)
		if media != nil && strings.TrimSpace(scene) == "" {
			// The API formats the codec with the file's scene name only; naming used GetSceneOrFileName, which at
			// import time was the original file name.
			atImport := orig
			if atImport == "" {
				atImport = rel
			}
			atImport = strings.TrimSuffix(path.Base(atImport), path.Ext(atImport))
			switch media.VideoCodec {
			case "h264":
				media.VideoCodec = sceneNameMatch(atImport, "AVC", "x264", "h264")
			case "h265":
				media.VideoCodec = sceneNameMatch(atImport, "HEVC", "x265", "h265")
			}
		}
		proper := ""
		if rev > 1 {
			proper = " Proper"
			if stype == "anime" {
				proper = fmt.Sprintf(" v%d", rev)
			}
		}
		cfs, ok := cfFromName(base, qt+proper)
		gc.CFSource = "none"
		if ok {
			gc.CFSource = "name"
		}
		if flavour == "radarr" {
			gc.Movie = &Movie{Title: name, Year: year, ImdbID: imdb, TmdbID: tmdb}
			gc.MFile = &MovieFile{Quality: q, ReleaseGroup: group, Edition: edition, SceneName: scene, RelativePath: rel,
				Path: p, MediaInfo: media, CustomFormats: cfs}
			gc.ExpectFile = strings.TrimSuffix(base, gc.Ext)
		} else {
			gc.Series = &Series{Title: name, Year: year, Type: SeriesType(stype), ImdbID: imdb, TvdbID: tvdb, TmdbID: tmdb}
			gc.EFile = &EpisodeFile{ID: gc.FileID, Quality: q, ReleaseGroup: group, SceneName: scene, RelativePath: rel,
				Path: p, MediaInfo: media, CustomFormats: cfs}
			gc.ExpectFile = base
			gc.ExpectSeason = path.Base(path.Dir(p))
		}
		out = append(out, gc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Episodes per series file.
	for i := range out {
		gc := &out[i]
		if gc.Series == nil {
			continue
		}
		er, err := db.QueryContext(ctx, `
			SELECT e.season, e.number, e.name, COALESCE(e.air_date, ''),
			       (SELECT n.number FROM episode_number n WHERE n.episode_id = e.id AND n.scheme = 'absolute_tvdb'
			        ORDER BY n.id LIMIT 1)
			FROM version_file_episode vfe JOIN episode e ON e.id = vfe.episode_id
			WHERE vfe.version_file_id = ? ORDER BY e.season, e.number`, gc.FileID)
		if err != nil {
			return nil, err
		}
		for er.Next() {
			var e Episode
			var abs sql.NullInt64
			if err := er.Scan(&e.Season, &e.Number, &e.Title, &e.AirDate, &abs); err != nil {
				er.Close()
				return nil, err
			}
			if abs.Valid {
				v := int(abs.Int64)
				e.Absolute = &v
			}
			gc.Eps = append(gc.Eps, e)
		}
		er.Close()
	}
	kept := out[:0]
	for _, gc := range out {
		if gc.Series != nil && len(gc.Eps) == 0 {
			continue
		}
		kept = append(kept, gc)
	}
	return kept, nil
}
