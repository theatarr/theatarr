package library

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/library/probe"
)

// tracks are probed audio tracks with these language tags, each tagged in the file.
func tracks(langs ...string) []probe.Audio {
	out := make([]probe.Audio, len(langs))
	for i, l := range langs {
		out[i] = probe.Audio{Codec: "ac3", Channels: 6, Language: l, Tagged: true}
	}
	return out
}

// audio sets the audio tracks of every file of a release folder.
func (e *env) audio(folder string, a []probe.Audio) {
	prefix := filepath.Join(e.mount, "__all__", folder) + string(filepath.Separator)
	for p, r := range e.probes {
		if strings.HasPrefix(p, prefix) {
			r.Audio = slices.Clone(a)
		}
	}
}

// audioFilm makes the film's library want its original language or English (profile original) and gives the
// title the stored original language.
func (e *env) audioFilm(original string) {
	e.pol.Profiles["movie-state"].Language = "original"
	e.exec(`UPDATE title SET original_language = ? WHERE id = 1`, original)
}

// heldFilm is the film's current file in movie-remux (link 1) at path (default: held.mkv in the film's folder), its
// stored probe holding audio tracks with these tags.
func (e *env) heldFilm(path string, langs ...string) string {
	e.t.Helper()
	return e.heldFilmAudio(path, tracks(langs...))
}

// heldFilmAudio is heldFilm with the stored probe's audio tracks as given.
func (e *env) heldFilmAudio(path string, a []probe.Audio) string {
	e.t.Helper()
	if path == "" {
		path = filepath.Join(e.movies, "Amelie The Movie (2001)", "held.mkv")
	}
	target := filepath.Join(e.mount, "__all__", "Held", "held.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		e.t.Fatal(err)
	}
	pr, _ := json.Marshal(probe.Result{Container: "matroska", RunTimeSeconds: 7000, Audio: a})
	e.exec(`INSERT INTO content (id, info_hash, first_seen_at, last_seen_at) VALUES (1, 'held', 0, 0)`)
	e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes, probe) VALUES (1, 1, 'held.mkv', 1, ?)`, string(pr))
	e.exec(`INSERT INTO link (id, path, target, kind, title_id, release_title, quality, content_file_id, state,
		created_at, updated_at) VALUES (1, ?, ?, 'symlink', 1, 'Amelie.2001.2160p.UHD.BluRay.TrueHD-OLD', 'Bluray-2160p', 1,
		'active', 0, 0)`, path, target)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
	return path
}

// filmImport offers release (hash) as download id to movie-remux with one file whose audio is a, and imports it.
func (e *env) filmImport(id int64, hash, release string, a []probe.Audio) *Plan {
	e.t.Helper()
	e.release(release, map[string]float64{"movie.mkv": 7000})
	e.audio(release, a)
	e.download(id, "movie-remux", 1, 10, release, release, "[]")
	e.exec(`UPDATE download SET info_hash = ? WHERE id = ?`, hash, id)
	plan, err := e.lib.Import(e.ctx, id, false)
	if err != nil {
		e.t.Fatal(err)
	}
	return plan
}

func (e *env) state(id int64) string {
	return e.one(`SELECT state FROM download WHERE id = ?`, id)
}

// The rule's verdict for the shapes of held files (read from live's audio tracks, 2026-11-21): a file in a slot
// being upgraded by a like file, and the same file into an empty film slot (a first refusal).
func TestAudioRuleVerdicts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, original string
		tags           []string
		emptyRefused   bool
	}{
		{"anime, Japanese", "Japanese", []string{"jpn"}, false},
		{"anime, two Japanese tracks", "Japanese", []string{"jpn", "jpn"}, false},
		{"a code, ko, and a Korean file", "ko", []string{"kor"}, false},
		{"French (B code)", "French", []string{"fre"}, false},
		{"French (T code)", "French", []string{"fra"}, false},
		{"Norwegian Bokmål reads Norwegian", "Norwegian", []string{"nob"}, false},
		{"Portuguese and Norwegian Bokmål", "Norwegian", []string{"por", "nob"}, false},
		{"Mayan is not in the table", "English", []string{"myn"}, false},
		{"und", "Japanese", []string{"und"}, false},
		{"mul", "Japanese", []string{"mul"}, false},
		{"zxx", "Japanese", []string{"zxx"}, false},
		{"region subtag", "Japanese", []string{"ja-JP"}, false},
		{"Emilia Pérez: listed French, spoken Spanish", "French", []string{"spa", "spa"}, true},
		{"a French dub of an English series", "English", []string{"fra"}, true},
		{"an original outside the table: English alone", "Flemish", []string{"jpn"}, true},
		{"an original outside the table, English file", "Flemish", []string{"eng"}, false},
	} {
		want := originalWanted(tc.original)
		langs, judged := fileAudio(&probe.Result{Audio: tracks(tc.tags...)})
		if refused := judged && !sharesLanguage(langs, want); refused != tc.emptyRefused {
			t.Errorf("%s: empty slot refused %v, want %v (want %v, file %v)", tc.name, refused, tc.emptyRefused, want, langs)
		}
		// Upgraded by a like file: the new file shares the held file's languages, so it is never refused.
		if judged && !sharesLanguage(langs, want) && !sharesLanguage(langs, langs) {
			t.Errorf("%s: a like upgrade refused", tc.name)
		}
	}
	if _, judged := fileAudio(&probe.Result{Audio: []probe.Audio{{Language: "eng"}}}); judged {
		t.Errorf("an untagged Matroska track (eng by default) is judged")
	}
	if _, judged := fileAudio(&probe.Result{Audio: append(tracks("fra"), probe.Audio{Language: "eng"})}); judged {
		t.Errorf("a file with one untagged track is judged")
	}
	if _, judged := fileAudio(&probe.Result{}); judged {
		t.Errorf("a file with no audio track is judged")
	}
	if w := originalWanted(""); w != nil {
		t.Errorf("a missing original language wants %v", w)
	}
	if w, decided := profileWanted("movie", "any"); !decided || w != nil {
		t.Errorf("profile any: %v %v", w, decided)
	}
	if w, decided := profileWanted("movie", "French"); !decided || !slices.Equal(w, []string{"fra"}) {
		t.Errorf("profile French: %v %v", w, decided)
	}
	if _, decided := profileWanted("series", ""); decided {
		t.Errorf("a series library decides without the original language")
	}
}

// The opening is the marker: the names ordered the same whatever the track order, and the stored reason starts with
// exactly what the lookup builds.
func TestAudioOpening(t *testing.T) {
	t.Parallel()
	a, _ := fileAudio(&probe.Result{Audio: tracks("spa", "fre", "spa")})
	b, _ := fileAudio(&probe.Result{Audio: tracks("fra", "spa")})
	if audioOpening(a) != audioOpening(b) || audioOpening(a) != "Import refused: French and Spanish audio only; this library wants " {
		t.Errorf("openings %q %q", audioOpening(a), audioOpening(b))
	}
	if got := languageWords([]string{"deu", "fra", "spa"}, "and"); got != "German, French and Spanish" {
		t.Errorf("three languages %q", got)
	}
}

// Untagged audio, MP4 und and a known wanted language import; an English-only original outside the table refuses
// a Japanese file.
func TestAudioRuleUnknownImports(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, original string
		a              []probe.Audio
		imported       bool
	}{
		{"untagged Matroska track, original Japanese", "Japanese", []probe.Audio{{Codec: "ac3", Language: "eng"}}, true},
		{"MP4 und", "Japanese", []probe.Audio{{Codec: "aac", Language: "und"}}, true},
		{"ko title, Korean file", "ko", tracks("kor"), true},
		{"original outside the table, English file", "Flemish", tracks("eng"), true},
		{"original outside the table, Japanese file", "Flemish", tracks("jpn"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, true)
			e.audioFilm(tc.original)
			e.filmImport(1, hashA, movieRelease, tc.a)
			if got := e.state(1) == "imported"; got != tc.imported {
				t.Errorf("imported %v, want %v (%s)", got, tc.imported, e.one(`SELECT COALESCE(error, '') FROM download WHERE id = 1`))
			}
		})
	}
}

// Hercules: a French-only file offered as an upgrade over an English one is refused; the old link survives, the
// release is blocklisted for the library and its re-search is left to the poll (researched_at unset).
func TestAudioRuleRefusesDowngradeOfAudio(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.audioFilm("English")
	held := e.heldFilm("", "eng")
	before := tree(t, e.movies)
	plan := e.filmImport(1, hashA, movieRelease, tracks("fre"))
	want := "Import refused: French audio only; the file in the library has English."
	if len(plan.Links) != 0 || len(plan.Rejected) == 0 || plan.Rejected[0].Reason != want {
		t.Fatalf("plan %+v", plan)
	}
	if got := e.one(`SELECT state || '|' || error FROM download WHERE id = 1`); got != "failed|library: "+want {
		t.Errorf("download %s", got)
	}
	if got := e.one(`SELECT source || '|' || reason || '|' || (researched_at IS NULL) FROM router_blocklist
		WHERE version_key = 'movie-remux' AND info_hash = ?`, hashA); got != "library|"+want+"|1" {
		t.Errorf("blocklist %s", got)
	}
	if after := tree(t, e.movies); strings.Join(before, "|") != strings.Join(after, "|") {
		t.Errorf("the refused import changed the library:\n%v\n%v", before, after)
	}
	if got := e.one(`SELECT state || '|' || path FROM link WHERE id = 1`); got != "active|"+held {
		t.Errorf("the English file's link %s", got)
	}
}

// Replacing a file: a new file that shares a language with the held one imports (a Spanish Emilia Pérez listed
// French, upgraded by a Spanish one); Russian over Spanish is refused; a held file whose audio is unknown shares
// nothing.
func TestAudioRuleReplacementSharesLanguage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		held     []string
		file     []string
		imported bool
		reason   string
	}{
		{"Spanish over Spanish", []string{"spa"}, []string{"spa", "spa"}, true, ""},
		{"Russian over Spanish", []string{"spa"}, []string{"rus"}, false,
			"Import refused: Russian audio only; the file in the library has Spanish."},
		{"French over a file of unknown audio", []string{"und"}, []string{"fre"}, false,
			"Import refused: French audio only; the file in the library has no known audio language."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, true)
			e.audioFilm("French")
			e.heldFilm("", tc.held...)
			if tc.file[0] == "fre" {
				e.audioFilm("Japanese") // French is not wanted here
			}
			e.filmImport(1, hashA, movieRelease, tracks(tc.file...))
			if got := e.state(1) == "imported"; got != tc.imported {
				t.Fatalf("imported %v, want %v (%s)", got, tc.imported, e.one(`SELECT COALESCE(error, '') FROM download WHERE id = 1`))
			}
			if !tc.imported {
				if got := e.one(`SELECT reason FROM router_blocklist WHERE info_hash = ?`, hashA); got != tc.reason {
					t.Errorf("reason %q, want %q", got, tc.reason)
				}
			}
		})
	}
}

// A replacement at the same path (not in the plan's Replaces) is a replacement too.
func TestAudioRuleSamePathReplacement(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.audioFilm("English")
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	dry, err := e.lib.Import(e.ctx, 1, true) // where the file would go
	if err != nil || len(dry.Links) != 1 {
		t.Fatalf("dry run %+v %v", dry, err)
	}
	// Only the language changes: the file's name (its audio codec and channels) stays the one the dry run planned,
	// so the held file is at the link's own path and in no Replaces.
	a := &e.probes[filepath.Join(e.mount, "__all__", movieRelease, "movie.mkv")].Audio[0]
	a.Language, a.Tagged = "fre", true
	e.heldFilm(dry.Links[0].Path, "eng")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "Import refused: French audio only; the file in the library has English."
	if len(plan.Links) != 0 || e.state(1) != "failed" || plan.Why != want {
		t.Fatalf("plan %+v state %s", plan, e.state(1))
	}
	if tg, _ := os.Readlink(dry.Links[0].Path); tg != filepath.Join(e.mount, "__all__", "Held", "held.mkv") {
		t.Errorf("the held link now points at %s", tg)
	}
}

// An empty film slot: the first French-only release is refused; the same torrent held on another
// provider and a release with other audio do not count; a second different release with the same audio is taken,
// noted for the journal.
func TestAudioRuleEmptyFilmSlotRefusedOnce(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.audioFilm("Japanese")
	plan := e.filmImport(1, hashA, movieRelease, tracks("fre", "fra"))
	first := "Import refused: French audio only; this library wants English or Japanese. Another release with the " +
		"same audio would be taken."
	if e.state(1) != "failed" || plan.Why != first {
		t.Fatalf("first: %s %q", e.state(1), plan.Why)
	}
	if got := e.one(`SELECT reason || '|' || (researched_at IS NULL) FROM router_blocklist WHERE info_hash = ?`, hashA); got != first+"|1" {
		t.Errorf("first blocklist %s", got)
	}
	// The same torrent again (another provider's copy): its own row, so not a second release.
	e.filmImport(2, hashA, movieRelease, tracks("fre"))
	if e.state(2) != "failed" {
		t.Fatalf("the same torrent counted as a second release: %s", e.state(2))
	}
	// Other audio (French and German): another set, refused.
	other := "Amelie.2001.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-OTHER"
	e.filmImport(3, hashC, other, tracks("fre", "ger"))
	if e.state(3) != "failed" {
		t.Fatalf("a different audio set counted: %s", e.state(3))
	}
	second := "Amelie.2001.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-SECOND"
	p := e.filmImport(4, hashB, second, tracks("fre"))
	if e.state(4) != "imported" || len(p.Links) != 1 {
		t.Fatalf("the second release: %s %+v", e.state(4), p)
	}
	if f := plannedFile(p, "movie.mkv"); f == nil || !strings.Contains(f.Note, "an earlier release with the same audio was refused") {
		t.Errorf("note %+v", f)
	}
}

// An empty series slot is refused every time, the reason naming Manual import; a pack is one verdict: one
// wrong-audio file among those it links refuses the whole import, and nothing is linked.
func TestAudioRuleSeries(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.exec(`UPDATE title SET original_language = 'English' WHERE id = 2`)
	const pack1 = "Show.S01.2160p.WEB-DL.DDP5.1.H.265-ONE"
	files := map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-ONE.mkv": 2700,
		"Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-ONE.mkv": 2700}
	e.release(pack1, files)
	e.audio(pack1, tracks("eng"))
	e.probes[filepath.Join(e.mount, "__all__", pack1, "Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-ONE.mkv")].Audio = tracks("fra")
	e.seriesDownload(1, 2, 20, hashA, pack1, "[201, 202]", nil)
	p, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "Import refused: French audio only; this library wants English. Use Manual import to take it anyway."
	if e.state(1) != "failed" || p.Why != want || len(p.Links) != 0 {
		t.Fatalf("pack: %s %q %+v", e.state(1), p.Why, p.Links)
	}
	if got := e.one(`SELECT COUNT(*) FROM link`); got != "0" {
		t.Errorf("links %s", got)
	}
	// A second French-only pack: refused again (no second-release exception for a series).
	const pack2 = "Show.S01.1080p.WEB-DL.DDP5.1.H.264-TWO"
	e.release(pack2, map[string]float64{"Show.S01E01.1080p.WEB-DL.DDP5.1.H.264-TWO.mkv": 2700,
		"Show.S01E02.1080p.WEB-DL.DDP5.1.H.264-TWO.mkv": 2700})
	e.audio(pack2, tracks("fra"))
	e.seriesDownload(2, 2, 20, hashB, pack2, "[201, 202]", nil)
	if _, err := e.lib.Import(e.ctx, 2, false); err != nil {
		t.Fatal(err)
	}
	if e.state(2) != "failed" {
		t.Errorf("the second French pack: %s", e.state(2))
	}
}

// A wrong-audio file the plan would not link cannot veto: the download holds E01 only, so E02's French file is not
// linked and E01 imports.
func TestAudioRuleUnlinkedFileNoVeto(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.exec(`UPDATE title SET original_language = 'English' WHERE id = 2`)
	const pack = "Show.S01.2160p.WEB-DL.DDP5.1.H.265-ONE"
	e.release(pack, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-ONE.mkv": 2700,
		"Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-ONE.mkv": 2700})
	e.audio(pack, tracks("eng"))
	e.probes[filepath.Join(e.mount, "__all__", pack, "Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-ONE.mkv")].Audio = tracks("fra")
	e.seriesDownload(1, 2, 20, hashA, pack, "[201]", nil)
	p, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if e.state(1) != "imported" || len(p.Links) != 1 {
		t.Fatalf("%s %q %+v", e.state(1), p.Why, p.Links)
	}
}

// Manual import is never refused: the file links and the plan notes what the rule saw.
func TestAudioRuleManualImportLinks(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.audioFilm("Japanese")
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.audio(movieRelease, tracks("fre"))
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, how, updated_at) VALUES (1, 'movie.mkv', '[]',
		'manual', 'manual', 0)`)
	p, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if e.state(1) != "imported" || len(p.Links) != 1 {
		t.Fatalf("%s %q", e.state(1), p.Why)
	}
	if f := plannedFile(p, "movie.mkv"); f == nil || !strings.Contains(f.Note, "Manual import: French audio only; this library wants English or Japanese.") ||
		strings.Contains(f.Note, "Import refused") || strings.Contains(f.Note, "Another release") {
		t.Errorf("note %+v", f)
	}
}

// Profile any: no rule.
func TestAudioRuleProfileAny(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.exec(`UPDATE title SET original_language = 'English' WHERE id = 1`)
	e.heldFilm("", "eng")
	e.filmImport(1, hashA, movieRelease, tracks("fre"))
	if e.state(1) != "imported" {
		t.Errorf("profile any refused: %s", e.one(`SELECT COALESCE(error, '') FROM download WHERE id = 1`))
	}
}

// Two libraries decide differently for one download: movie-remux (any) imports the French file; movie-web wants
// English or Japanese (profile original) and refuses it. Its blocklist row stays (without the deleted download)
// and starts no search there.
func TestAudioRulePartnerLibrary(t *testing.T) {
	t.Parallel()
	e, _ := otherEnv(t, "movie-en")
	en := *e.pol.Profiles["movie-state"]
	en.Language = "original"
	e.exec(`UPDATE title SET original_language = 'Japanese' WHERE id = 1`)
	e.pol.Profiles["movie-en"] = &en
	engine, err := decide.New(e.pol, decide.Options{Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	e.lib.engine = engine
	e.audio(otherRelease, tracks("fre"))
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if e.state(1) != "imported" {
		t.Fatalf("movie-remux: %s", e.state(1))
	}
	if got := e.webLinks(); got != "0" {
		t.Errorf("movie-web links %s", got)
	}
	want := "Import refused: French audio only; this library wants English or Japanese. Another release with the same audio " +
		"would be taken."
	if got := e.one(`SELECT COUNT(*) || '|' || group_concat(source || '|' || reason || '|' || (download_id IS NULL) || '|' ||
		(researched_at IS NOT NULL)) FROM router_blocklist WHERE version_key = 'movie-web'`); got != "1|library|"+want+"|1|1" {
		t.Errorf("movie-web blocklist %s", got)
	}
	if got := e.one(`SELECT group_concat(id) FROM download`); got != "1" {
		t.Errorf("downloads %s", got)
	}
}

// A title whose original language nothing names has no rule (LoadTitle's English fallback is not a named one): a
// French-only pack of such a series imports.
func TestAudioRuleNoOriginalLanguage(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	const pack = "Show.S01.2160p.WEB-DL.DDP5.1.H.265-ONE"
	e.release(pack, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-ONE.mkv": 2700})
	e.audio(pack, tracks("fra"))
	e.seriesDownload(1, 2, 20, hashA, pack, "[201]", nil)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if e.state(1) != "imported" {
		t.Errorf("%s: %s", e.state(1), e.one(`SELECT COALESCE(error, '') FROM download WHERE id = 1`))
	}
}

// Each displaced file is judged on its own (episode overlap): a French-only E01-E02 file over an English E01 and a
// French E02 is refused (it would take English away from E01); two Spanish files over one Spanish E01-E02 file
// import (each shares Spanish with the file it displaces, though only the last carries its path).
func TestAudioRuleEachDisplacedFile(t *testing.T) {
	t.Parallel()
	held := func(e *env, id int64, name string, langs []string, eps ...int64) {
		e.t.Helper()
		pr, _ := json.Marshal(probe.Result{Container: "matroska", RunTimeSeconds: 2700, Audio: tracks(langs...)})
		e.exec(`INSERT INTO content (id, info_hash, first_seen_at, last_seen_at) VALUES (?, ?, 0, 0)`, id, fmt.Sprint("held", id))
		e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes, probe) VALUES (?, ?, 'f.mkv', 1, ?)`, id, id, string(pr))
		e.heldLink(id, "WEBDL-1080p", name, eps...)
		e.exec(`UPDATE link SET content_file_id = ? WHERE id = ?`, id, id)
	}
	t.Run("French double episode over English and French", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, true)
		e.exec(`UPDATE title SET original_language = 'Japanese' WHERE id = 2`)
		held(e, 1, "Show.S01E01.1080p.WEB-DL-OLD", []string{"eng"}, 201)
		held(e, 2, "Show.S01E02.1080p.WEB-DL-OLD", []string{"fra"}, 202)
		const rel = "Show.S01E01E02.2160p.BluRay.REMUX-NEW"
		e.release(rel, map[string]float64{"Show.S01E01E02.2160p.BluRay.REMUX-NEW.mkv": 5400})
		e.audio(rel, tracks("fra"))
		e.seriesDownload(1, 2, 20, hashA, rel, "[201, 202]", nil)
		p, err := e.lib.Import(e.ctx, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		if want := "Import refused: French audio only; the file in the library has English."; e.state(1) != "failed" || p.Why != want {
			t.Fatalf("%s %q", e.state(1), p.Why)
		}
	})
	t.Run("two Spanish files over one Spanish double episode", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, true)
		e.exec(`UPDATE title SET original_language = 'Japanese' WHERE id = 2`)
		held(e, 1, "Show.S01E01E02.1080p.WEB-DL-OLD", []string{"spa"}, 201, 202)
		const rel = "Show.S01.2160p.BluRay.REMUX-NEW"
		e.release(rel, map[string]float64{"Show.S01E01.2160p.BluRay.REMUX-NEW.mkv": 2700,
			"Show.S01E02.2160p.BluRay.REMUX-NEW.mkv": 2700})
		e.audio(rel, tracks("spa"))
		e.seriesDownload(1, 2, 20, hashA, rel, "[201, 202]", nil)
		p, err := e.lib.Import(e.ctx, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		if e.state(1) != "imported" || len(p.Links) != 2 {
			t.Fatalf("%s %q %d links", e.state(1), p.Why, len(p.Links))
		}
	})
}

// A series whose stored original language is Unknown takes the metadata's: a Japanese episode imports.
func TestAudioRuleSeriesUnknownOriginal(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.exec(`UPDATE title SET original_language = 'Unknown' WHERE id = 2`)
	e.exec(`INSERT INTO series_meta (title_id, original_language) VALUES (2, 'ja')`)
	const pack = "Show.S01.2160p.WEB-DL.DDP5.1.H.265-ONE"
	e.release(pack, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-ONE.mkv": 2700})
	e.audio(pack, tracks("jpn"))
	e.seriesDownload(1, 2, 20, hashA, pack, "[201]", nil)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if e.state(1) != "imported" {
		t.Errorf("%s: %s", e.state(1), e.one(`SELECT COALESCE(error, '') FROM download WHERE id = 1`))
	}
}

// An import by path (the native import naming a folder on the mount, no files chosen) is the caller's choice: it
// links, with a note, and nothing is blocklisted.
func TestAudioRulePathImportNotRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.audioFilm("Japanese")
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.audio(movieRelease, tracks("fre"))
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	e.exec(`UPDATE download SET source_path = ? WHERE id = 1`, filepath.Join(e.mount, "__all__", movieRelease))
	p, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if e.state(1) != "imported" || len(p.Links) != 1 {
		t.Fatalf("%s %q", e.state(1), p.Why)
	}
	if got := e.one(`SELECT COUNT(*) FROM router_blocklist`); got != "0" {
		t.Errorf("blocklist rows %s", got)
	}
	if f := plannedFile(p, "movie.mkv"); f == nil || !strings.Contains(f.Note, "Manual import: French audio only") {
		t.Errorf("note %+v", f)
	}
}

// Manual import's list (a dry run) keeps every assignment of a pack with one wrong-audio file: E01 stays matched,
// and E02's audio is a note.
func TestAudioRulePreviewKeepsAssignments(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.exec(`UPDATE title SET original_language = 'English' WHERE id = 2`)
	const pack = "Show.S01.2160p.WEB-DL.DDP5.1.H.265-ONE"
	e2 := "Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-ONE.mkv"
	e.release(pack, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-ONE.mkv": 2700, e2: 2700})
	e.audio(pack, tracks("eng"))
	e.probes[filepath.Join(e.mount, "__all__", pack, e2)].Audio = tracks("fra")
	e.seriesDownload(1, 2, 20, hashA, pack, "[201, 202]", nil)
	p, err := e.lib.Import(e.ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Links) != 2 || p.Why != "" {
		t.Fatalf("preview links %d, why %q", len(p.Links), p.Why)
	}
	if f := plannedFile(p, e2); f == nil || !strings.Contains(f.Note, "Manual import: French audio only; this library wants English.") {
		t.Errorf("note %+v", f)
	}
}

// The held file's stored probe says whether a track's language was in the file. An untagged Matroska track (eng by
// Matroska's default, and any eng in a Matroska probe stored before the fact was kept) is unknown: an English-only
// file does not replace it under a French profile, and a refusal does not say it has English. Another language in
// an old probe stays known.
func TestAudioRuleStoredTagFact(t *testing.T) {
	t.Parallel()
	untagged := []probe.Audio{{Codec: "ac3", Channels: 6, Language: "eng"}}
	for _, tc := range []struct {
		name, profile, original string
		held                    []probe.Audio
		file                    []string
		imported                bool
		reason                  string
	}{
		{"English over an untagged track, French profile", "French", "", untagged, []string{"eng"}, false,
			"Import refused: English audio only; the file in the library has no known audio language."},
		{"French over an untagged track", "original", "Japanese", untagged, []string{"fre"}, false,
			"Import refused: French audio only; the file in the library has no known audio language."},
		{"old probe, eng", "original", "Japanese", untagged, []string{"spa"}, false,
			"Import refused: Spanish audio only; the file in the library has no known audio language."},
		{"old probe, jpn stays known", "English", "", []probe.Audio{{Codec: "ac3", Language: "jpn"}}, []string{"jpn"},
			true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, true)
			e.pol.Profiles["movie-state"].Language = tc.profile
			e.exec(`UPDATE title SET original_language = ? WHERE id = 1`, tc.original)
			e.heldFilmAudio("", tc.held)
			e.filmImport(1, hashA, movieRelease, tracks(tc.file...))
			if got := e.state(1) == "imported"; got != tc.imported {
				t.Fatalf("imported %v, want %v (%s)", got, tc.imported, e.one(`SELECT COALESCE(error, '') FROM download WHERE id = 1`))
			}
			if !tc.imported {
				if got := e.one(`SELECT reason FROM router_blocklist WHERE info_hash = ?`, hashA); got != tc.reason {
					t.Errorf("reason %q, want %q", got, tc.reason)
				}
			}
		})
	}
}

// The fact is stored with the probe at import: the content file's probe says which tracks were tagged.
func TestAudioRuleTagFactStored(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.audio(movieRelease, append(tracks("jpn"), probe.Audio{Codec: "ac3", Language: "eng"}))
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT COALESCE(json_extract(probe, '$.audio[0].tagged'), 0) || '|' ||
		COALESCE(json_extract(probe, '$.audio[1].tagged'), 0) FROM content_file`); got != "1|0" {
		t.Errorf("stored tagged %s", got)
	}
}

// A film whose title row names Japanese while its cached details carry no original language: the rule wants
// Japanese or English (the first source that names one), so a Japanese file imports.
func TestAudioRuleFilmEmptyDetailsLanguage(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.audioFilm("Japanese")
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, _ = zw.Write([]byte(`{"original_title":"Amelie","original_language":"","runtime":120}`))
	_ = zw.Close()
	e.exec(`INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at) VALUES ('tmdb',
		'/movie/194?append_to_response=external_ids%2Crelease_dates', 200, ?, 0, 0)`, b.Bytes())
	e.filmImport(1, hashA, movieRelease, tracks("jpn"))
	if e.state(1) != "imported" {
		t.Errorf("%s: %s", e.state(1), e.one(`SELECT COALESCE(error, '') FROM download WHERE id = 1`))
	}
}
