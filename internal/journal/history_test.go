package journal

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// row is one History row in brief: event, release, quality, score, episodes and what replaced it.
func histRow(e *Entry) string {
	h := e.Hist
	s := h.Event
	if r := h.Release; r != nil {
		s += " " + r.Title + " " + r.Quality
		if r.Score != nil {
			s += fmt.Sprintf(" %d", *r.Score)
		}
	} else {
		s += " ?"
	}
	if len(h.Episodes) > 0 {
		s += " [" + strings.Join(h.Episodes, ",") + "]"
	}
	if r := h.ReplacedBy; r != nil {
		s += " -> " + r.Title
	}
	if h.Provider != "" {
		s += " @" + h.Provider
	}
	if h.Reason != "" {
		s += " (" + h.Reason + ")"
	}
	return s
}

func (e *env) history(f Filter) []string {
	e.t.Helper()
	f.History = true
	var out []string
	for _, x := range e.list(f).Items {
		out = append(out, histRow(x))
	}
	return out
}

// History's rows: a season pack's grab and import are one row each; an upgrade over an adopted file is Grabbed,
// Imported and Deleted (upgrade) naming the old release and what replaced it; a hand unlink is Removed; copies are
// their own rows; rows from before the facts were recorded show what the link rows still tell, or nothing.
func TestHistoryRows(t *testing.T) {
	e := newEnv(t)
	at := testNow.Add(-48 * time.Hour)
	ms := func(d time.Duration) int64 { return msOf(at.Add(d)) }

	// Gamma S01E01-E03 as one pack: grabbed, imported, three links.
	e.dlEvent(10, "imported", at, 3, 3, "tv-2160p", hashA, "Gamma.S01.2160p.WEB-DL-GRP", "WEBDL-2160p", 1200, 9<<30,
		[]int64{101, 102, 103}, "")
	e.exec(`UPDATE download SET provider = 'rd' WHERE id = 10`)
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, plan, created_at, updated_at)
		VALUES (10, 'linked', '/m/Gamma', '{}', ?, ?)`, ms(time.Hour), ms(time.Hour))
	for i := int64(1); i <= 3; i++ {
		e.exec(`INSERT INTO link (id, path, target, title_id, download_id, release_title, quality, score, state, created_at,
			updated_at) VALUES (?, ?, '/t', 3, 10, 'Gamma.S01.2160p.WEB-DL-GRP', 'WEBDL-2160p', 1200, 'active', ?, ?)`,
			i, fmt.Sprintf("/tv/Gamma/S01E0%d.mkv", i), ms(time.Hour), ms(time.Hour))
		e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (?, ?)`, i, 100+i)
		e.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id, link_id, path, release_title,
			quality, score) VALUES (?, 'link', 'applied', 'tv-2160p', 10, ?, ?, 'Gamma.S01.2160p.WEB-DL-GRP',
			'WEBDL-2160p', 1200)`, ms(time.Hour), i, fmt.Sprintf("/tv/Gamma/S01E0%d.mkv", i))
	}
	got := e.history(Filter{Title: "3"})
	want := []string{
		"imported Gamma.S01.2160p.WEB-DL-GRP WEBDL-2160p 1200 [S01E01,S01E02,S01E03] @rd",
		"grabbed Gamma.S01.2160p.WEB-DL-GRP WEBDL-2160p 1200 [S01E01,S01E02,S01E03] @rd",
	}
	if !slices.Equal(got, want) {
		t.Errorf("pack:\n got %q\nwant %q", got, want)
	}

	// Alpha movie-web: adopted (facts recorded), then upgraded by download 20 at another path.
	e.exec(`INSERT INTO link (id, path, target, title_id, release_title, quality, score, adopted_from, state, created_at,
		updated_at) VALUES (50, '/movies/Alpha/Alpha.WEB.mkv', '/t/a', 1, 'Alpha.2020.2160p.WEB-OLD', 'WEBDL-2160p', 4000,
		'radarr_4k /arr/a.mkv', 'unlinked', ?, ?)`, ms(0), ms(3*time.Hour))
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, link_id, path, detail, release_title, quality, score)
		VALUES (?, 'link', 'applied', 'movie-web', 50, '/movies/Alpha/Alpha.WEB.mkv', 'adopted from radarr_4k /arr/a.mkv',
		'Alpha.2020.2160p.WEB-OLD', 'WEBDL-2160p', 4000)`, ms(0))
	e.dlEvent(20, "imported", at.Add(2*time.Hour), 1, 2, "movie-web", hashB, "Alpha.2020.2160p.WEB-DL-FLUX",
		"WEBDL-2160p", 5000, 20<<30, nil, "")
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, plan, created_at, updated_at)
		VALUES (20, 'linked', '/m/Alpha', '{}', ?, ?)`, ms(3*time.Hour), ms(3*time.Hour))
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, release_title, quality, score, state, created_at,
		updated_at) VALUES (51, '/movies/Alpha/Alpha.FLUX.mkv', '/t/b', 1, 20, 'Alpha.2020.2160p.WEB-DL-FLUX', 'WEBDL-2160p',
		5000, 'active', ?, ?)`, ms(3*time.Hour), ms(3*time.Hour))
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id, link_id, path, release_title, quality,
		score) VALUES (?, 'link', 'applied', 'movie-web', 20, 51, '/movies/Alpha/Alpha.FLUX.mkv',
		'Alpha.2020.2160p.WEB-DL-FLUX', 'WEBDL-2160p', 5000)`, ms(3*time.Hour))
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id, path, detail, release_title, quality,
		score) VALUES (?, 'replace', 'applied', 'movie-web', 20, '/movies/Alpha/Alpha.WEB.mkv', 'moved aside to /trash/a',
		'Alpha.2020.2160p.WEB-OLD', 'WEBDL-2160p', 4000)`, ms(3*time.Hour)+1)
	got = e.history(Filter{Title: "1", Version: "movie-web"})
	want = []string{
		"deleted Alpha.2020.2160p.WEB-OLD WEBDL-2160p 4000 -> Alpha.2020.2160p.WEB-DL-FLUX",
		"imported Alpha.2020.2160p.WEB-DL-FLUX WEBDL-2160p 5000",
		"grabbed Alpha.2020.2160p.WEB-DL-FLUX WEBDL-2160p 5000",
		"adopted Alpha.2020.2160p.WEB-OLD WEBDL-2160p 4000",
	}
	if !slices.Equal(got, want) {
		t.Errorf("upgrade:\n got %q\nwant %q", got, want)
	}

	// A hand unlink of the new file: Removed, with its reason and release.
	e.exec(`UPDATE link SET state = 'unlinked' WHERE id = 51`)
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, link_id, path, detail, release_title, quality, score)
		VALUES (?, 'replace', 'applied', 'movie-web', 51, '/movies/Alpha/Alpha.FLUX.mkv',
		'unlinked (deleted in Radarr), moved aside to /trash/b', 'Alpha.2020.2160p.WEB-DL-FLUX', 'WEBDL-2160p', 5000)`,
		ms(5*time.Hour))
	// Copies of the FLUX torrent: added to TorBox, not cached on AllDebrid.
	e.exec(`INSERT INTO provider_copy_event (at, info_hash, name, title_id, provider, state, message, quality, score)
		VALUES (?, ?, 'Alpha.2020.2160p.WEB-DL-FLUX', 1, 'tb', 'added', 'Added to TB', 'WEBDL-2160p', 5000),
		(?, ?, 'Alpha.2020.2160p.WEB-DL-FLUX', 1, 'ad', 'not_cached', 'Not cached on AD; our add was deleted', NULL, NULL)`,
		ms(4*time.Hour), hashB, ms(4*time.Hour)+1, hashB)
	got = e.history(Filter{Title: "1", Since: at.Add(4 * time.Hour)})
	want = []string{
		"removed Alpha.2020.2160p.WEB-DL-FLUX WEBDL-2160p 5000 (deleted in Radarr)",
		"copy_not_cached Alpha.2020.2160p.WEB-DL-FLUX  @ad (Not cached on AD; our add was deleted)",
		"copy_added Alpha.2020.2160p.WEB-DL-FLUX WEBDL-2160p 5000 @tb (Added to TB)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("unlink and copies:\n got %q\nwant %q", got, want)
	}
	if got := e.history(Filter{Title: "1", Events: []string{EvCopyAdded}}); len(got) != 1 ||
		!strings.HasPrefix(got[0], "copy_added") {
		t.Errorf("events filter: %q", got)
	}

	// Rows from before 00129: an upgrade that overwrote the old link at its path (nothing left: unknown), and an
	// adoption whose link still holds the adopted file (its facts shown).
	e.exec(`INSERT INTO link (id, path, target, title_id, release_title, quality, score, adopted_from, state, created_at,
		updated_at) VALUES (60, '/movies/Beta/Beta.mkv', '/t/c', 2, 'Beta.2021.Remux-OLD', 'Remux-2160p', 8000,
		'radarr_4k /arr/b.mkv', 'active', ?, ?)`, ms(0), ms(0))
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, link_id, path, detail) VALUES
		(?, 'link', 'applied', 'movie-remux', 60, '/movies/Beta/Beta.mkv', 'adopted from radarr_4k /arr/b.mkv')`, ms(0))
	got = e.history(Filter{Title: "2"})
	if want := []string{"adopted Beta.2021.Remux-OLD Remux-2160p 8000"}; !slices.Equal(got, want) {
		t.Errorf("old adoption:\n got %q\nwant %q", got, want)
	}
	e.dlEvent(30, "imported", at.Add(6*time.Hour), 2, 4, "movie-remux", hashC, "Beta.2021.Remux-NEW", "Remux-2160p",
		9000, 60<<30, nil, "")
	e.exec(`UPDATE link SET download_id = 30, release_title = 'Beta.2021.Remux-NEW', score = 9000, adopted_from = NULL
		WHERE id = 60`)
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id, path, detail) VALUES
		(?, 'replace', 'applied', 'movie-remux', 30, '/movies/Beta/Beta.mkv', 'moved aside to /trash/c')`,
		ms(7*time.Hour))
	got = e.history(Filter{Title: "2", Events: []string{EvDeleted, EvAdopted}})
	want = []string{"deleted ? -> Beta.2021.Remux-NEW", "adopted ?"}
	if !slices.Equal(got, want) {
		t.Errorf("old rows:\n got %q\nwant %q", got, want)
	}
	// The journal itself still lists every link action, and no copy.
	for _, x := range e.list(Filter{Title: "1"}).Items {
		if strings.HasPrefix(x.ID, "copy:") || x.Hist != nil {
			t.Errorf("journal entry %s: %+v", x.ID, x.Hist)
		}
	}
}
