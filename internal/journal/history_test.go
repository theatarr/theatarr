package journal

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// row is one History row in brief: event, release, quality, score, episodes and the releases it replaced.
func histRow(h *HistoryRow) string {
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
	if len(h.Replaced) > 0 {
		s += " replaced"
		for _, f := range h.Replaced {
			if f.Release != nil {
				s += " " + f.Release.Title
			} else {
				s += " ?"
			}
		}
	}
	if h.Provider != "" {
		s += " @" + h.Provider
	}
	if h.Reason != "" {
		s += " (" + h.Reason + ")"
	}
	return s
}

// copyRows stores copy History rows of title 1 directly (at, ref, event, release, hash, quality, score, provider,
// reason): their source table is gone.
func (e *env) copyRows(values string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().Exec(`INSERT INTO history (at, ref, event, release, info_hash, quality, score, provider,
		reason, kind, title_id) SELECT column1, column2, column3, column4, column5, column6, column7, column8, column9,
		'copy', 1 FROM (VALUES `+values+`)`, args...); err != nil {
		e.t.Fatal(err)
	}
}

// history is History's rows in brief, stored from the sources the test wrote (as the migration would have).
func (e *env) history(f Filter) []string {
	e.t.Helper()
	ctx := context.Background()
	e.flag()
	p, err := e.j.History(ctx, f)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, x := range p.Items {
		out = append(out, histRow(x))
	}
	return out
}

// History's rows: a season pack's grab and import are one row each; an upgrade over an adopted file is Grabbed and
// Upgraded (deleted) with the new release and the old one in Replaced, its import folded in; a hand unlink is Removed; copies are
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
		"deleted Alpha.2020.2160p.WEB-DL-FLUX WEBDL-2160p 5000 replaced Alpha.2020.2160p.WEB-OLD",
		"grabbed Alpha.2020.2160p.WEB-DL-FLUX WEBDL-2160p 5000",
		"adopted Alpha.2020.2160p.WEB-OLD WEBDL-2160p 4000",
	}
	if !slices.Equal(got, want) {
		t.Errorf("upgrade:\n got %q\nwant %q", got, want)
	}
	// The upgrade's row stands for its import: the import's facts and journal entry. Paging one row at a time never
	// shows the import; filtered to imports, its row is there (the upgrade is not listed).
	var ids []string
	for cur := ""; ; {
		p, err := e.j.History(context.Background(), Filter{Title: "1", Version: "movie-web", Limit: 1, Cursor: cur})
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range p.Items {
			ids = append(ids, x.ID)
			if x.Event == EvDeleted && (x.Entry != "dl:20:import" || x.Release.InfoHash != hashB ||
				x.Release.Indexer != "dmm" || x.Release.Size != 20<<30) {
				t.Errorf("upgrade row facts: %+v %+v", x, x.Release)
			}
		}
		if cur = p.NextCursor; cur == "" {
			break
		}
	}
	if len(ids) != 3 || slices.Contains(ids, "dl:20:import") {
		t.Errorf("pages: %q", ids)
	}
	got = e.history(Filter{Title: "1", Events: []string{EvImported}})
	if want := []string{"imported Alpha.2020.2160p.WEB-DL-FLUX WEBDL-2160p 5000"}; !slices.Equal(got, want) {
		t.Errorf("imports only:\n got %q\nwant %q", got, want)
	}

	// A hand unlink of the new file: Removed, with its reason and release.
	e.exec(`UPDATE link SET state = 'unlinked' WHERE id = 51`)
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, link_id, path, detail, release_title, quality, score)
		VALUES (?, 'replace', 'applied', 'movie-web', 51, '/movies/Alpha/Alpha.FLUX.mkv',
		'unlinked (deleted in Radarr), moved aside to /trash/b', 'Alpha.2020.2160p.WEB-DL-FLUX', 'WEBDL-2160p', 5000)`,
		ms(5*time.Hour))
	// Copies of the FLUX torrent: added to TorBox, not cached on AllDebrid (stored rows only: their source table is
	// gone).
	e.flag()
	e.copyRows(`(?, 'copy:1', 'copy_added', 'Alpha.2020.2160p.WEB-DL-FLUX', ?, 'WEBDL-2160p', 5000, 'tb', 'Added to TB'),
		(?, 'copy:2', 'copy_not_cached', 'Alpha.2020.2160p.WEB-DL-FLUX', ?, '', NULL, 'ad',
		'Not cached on AD; our add was deleted')`, ms(4*time.Hour), hashB, ms(4*time.Hour)+1, hashB)
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
	want = []string{"deleted Beta.2021.Remux-NEW Remux-2160p 9000 replaced ?", "adopted ?"}
	if !slices.Equal(got, want) {
		t.Errorf("old rows:\n got %q\nwant %q", got, want)
	}
	// The journal itself still lists every link action, and no copy.
	for _, x := range e.list(Filter{Title: "1"}).Items {
		if strings.HasPrefix(x.ID, "copy:") {
			t.Errorf("journal entry %s", x.ID)
		}
	}
	checkStoredHistory(t, context.Background(), e.j, e.st)
}

// An import lists the episodes its links hold, not the ones asked for; a download that never reached a provider is
// no grab (only its failure shows).
func TestHistoryImportEpisodesAndNeverStarted(t *testing.T) {
	e := newEnv(t)
	at := testNow.Add(-time.Hour)
	e.dlEvent(40, "imported", at, 3, 3, "tv-2160p", hashA, "Gamma.S01E01-E04.WEB", "WEBDL-2160p", 1000, 4<<30,
		[]int64{101, 102, 103, 104}, "")
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, plan, created_at, updated_at)
		VALUES (40, 'linked', '/m/G', '{}', ?, ?)`, msOf(at.Add(time.Minute)), msOf(at.Add(time.Minute)))
	for i, ep := range []int64{102, 103} {
		id := int64(70 + i)
		e.exec(`INSERT INTO link (id, path, target, title_id, download_id, state, created_at, updated_at)
			VALUES (?, ?, '/t', 3, 40, 'active', 0, 0)`, id, fmt.Sprintf("/tv/G/%d.mkv", ep))
		e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (?, ?)`, id, ep)
		e.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id, link_id, path) VALUES
			(?, 'link', 'applied', 'tv-2160p', 40, ?, '/p')`, msOf(at.Add(time.Minute)), id)
	}
	e.dlEvent(41, "failed", at.Add(2*time.Minute), 1, 2, "movie-web", hashB, "Alpha.Never.Started", "WEBDL-2160p", 0,
		1<<30, nil, "route ended: router: router.enabled is false")
	e.exec(`UPDATE download SET added_at = NULL WHERE id = 41`)
	got := e.history(Filter{})
	want := []string{
		"failed Alpha.Never.Started WEBDL-2160p (route ended: router: router.enabled is false)",
		"imported Gamma.S01E01-E04.WEB WEBDL-2160p 1000 [S01E02,S01E03]",
		"grabbed Gamma.S01E01-E04.WEB WEBDL-2160p 1000 [S01E01,S01E02,S01E03,S01E04]",
	}
	if !slices.Equal(got, want) {
		t.Errorf("rows:\n got %q\nwant %q", got, want)
	}
	// Paging: one row a page, each page after the last.
	p, err := e.j.History(context.Background(), Filter{Limit: 1})
	if err != nil || len(p.Items) != 1 || p.NextCursor == "" {
		t.Fatalf("page 1: %+v %v", p, err)
	}
	p2, err := e.j.History(context.Background(), Filter{Limit: 2, Cursor: p.NextCursor})
	if err != nil || len(p2.Items) != 2 || p2.NextCursor != "" || p2.Items[0].Event != EvImported {
		t.Fatalf("page 2: %+v %v", p2, err)
	}
	checkStoredHistory(t, context.Background(), e.j, e.st)
}

// Facts recorded with the action outlive later changes: an import's episodes as its links held them (not the ones it
// asked for, even when its links are gone), a removed link's title after its row was deleted. A grab is dated when it
// reached the provider, and an add that found the torrent there already is its own copy event.
func TestHistoryRecordedFacts(t *testing.T) {
	e := newEnv(t)
	at := testNow.Add(-48 * time.Hour)
	// Download 50 asked for E01-E03, linked E01 only (recorded), its link since moved to E05.
	e.dlEvent(50, "imported", at, 3, 3, "tv-2160p", hashA, "Gamma.S01.Pack", "WEBDL-2160p", 800, 1<<30,
		[]int64{101, 102, 103}, "")
	e.exec(`UPDATE download SET added_at = ? WHERE id = 50`, msOf(testNow.Add(-2*time.Hour)))
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, plan, created_at, updated_at)
		VALUES (50, 'linked', '/m/G', '{}', ?, ?)`, msOf(testNow.Add(-time.Hour)), msOf(testNow.Add(-time.Hour)))
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, state, created_at, updated_at)
		VALUES (90, '/tv/G/a.mkv', '/t', 3, 50, 'active', 0, 0)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (90, 105)`)
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id, link_id, path, title_id, episodes)
		VALUES (?, 'link', 'applied', 'tv-2160p', 50, 90, '/tv/G/a.mkv', 3, '[101]')`, msOf(testNow.Add(-time.Hour)))
	// Download 51 (older rows): its links are gone, nothing recorded: no episodes, not the requested ones.
	e.dlEvent(51, "imported", at, 3, 3, "tv-2160p", hashB, "Gamma.S02.Pack", "WEBDL-2160p", 800, 1<<30,
		[]int64{201, 202}, "")
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, plan, created_at, updated_at)
		VALUES (51, 'linked', '/m/G2', '{}', ?, ?)`, msOf(at.Add(time.Hour)), msOf(at.Add(time.Hour)))
	// A hand unlink whose link row is gone since: the recorded title and episodes keep it on Gamma's History.
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, link_id, path, detail, title_id, episodes,
		release_title) VALUES (?, 'replace', 'applied', 'tv-2160p', 999, '/tv/G/b.mkv',
		'unlinked (replaced: link 7 now holds S01E04), moved aside to /trash/b', 3, '[104]', 'Gamma.S01E04.WEB')`,
		msOf(testNow.Add(-30*time.Minute)))
	got := e.history(Filter{Title: "3", Events: []string{EvImported, EvRemoved}})
	want := []string{
		"removed Gamma.S01E04.WEB  [S01E04] (replaced: link 7 now holds S01E04)",
		"imported Gamma.S01.Pack WEBDL-2160p 800 [S01E01]",
		"imported Gamma.S02.Pack WEBDL-2160p 800",
	}
	if !slices.Equal(got, want) {
		t.Errorf("rows:\n got %q\nwant %q", got, want)
	}
	// Grabbed two days after it was queued: dated when the provider took it, so "since yesterday" keeps it.
	got = e.history(Filter{Since: testNow.Add(-24 * time.Hour), Events: []string{EvGrabbed}})
	if len(got) != 1 || !strings.Contains(got[0], "Gamma.S01.Pack") {
		t.Errorf("grabs since yesterday: %q", got)
	}
	e.flag()
	if _, err := e.st.Writer().Exec(`INSERT INTO history (ref, at, event, kind, title_id, release, info_hash, provider,
		reason) VALUES ('copy:3', ?, 'copy_already', 'copy', 3, 'Gamma.S01.Pack', ?, 'ad', 'Already on AD')`,
		msOf(testNow), hashA); err != nil {
		t.Fatal(err)
	}
	if got := e.history(Filter{Events: []string{EvCopyAlready}}); len(got) != 1 || !strings.HasPrefix(got[0], "copy_already") {
		t.Errorf("already: %q", got)
	}
	checkStoredHistory(t, context.Background(), e.j, e.st)
}

// An adoption that replaced the previous file (Radarr upgraded it, Theatarr took the new file over) is one Adopted row
// with the old file in Replaced; a removal with no adoption beside it stays Removed; filtered to removals, or paged one
// row at a time, no row shows half of the pair.
func TestHistoryAdoptionReplaced(t *testing.T) {
	e := newEnv(t)
	at := testNow.Add(-24 * time.Hour)
	ms := func(d time.Duration) int64 { return msOf(at.Add(d)) }
	adopt := func(link int64, d time.Duration, path, release, quality string, score int) {
		e.exec(`INSERT INTO link (id, path, target, title_id, release_title, quality, score, adopted_from, state,
			created_at, updated_at) VALUES (?, ?, '/t', 2, ?, ?, ?, 'radarr_4k /arr/b.mkv', 'active', ?, ?)`, link, path,
			release, quality, score, ms(d), ms(d))
		e.exec(`INSERT INTO library_action (at, kind, mode, version_key, link_id, path, detail, release_title, quality,
			score) VALUES (?, 'link', 'applied', 'movie-remux', ?, ?, 'adopted from radarr_4k /arr/b.mkv', ?, ?, ?)`,
			ms(d), link, path, release, quality, score)
	}
	unlink := func(link int64, d time.Duration, path, release, quality string, score int) {
		e.exec(`UPDATE link SET state = 'unlinked' WHERE id = ?`, link)
		e.exec(`INSERT INTO library_action (at, kind, mode, version_key, link_id, path, detail, release_title, quality,
			score, title_id) VALUES (?, 'replace', 'applied', 'movie-remux', ?, ?,
			'unlinked (deleted in Radarr), moved aside to /trash/x', ?, ?, ?, 2)`, ms(d), link, path, release, quality,
			score)
	}
	adopt(1, 0, "/movies/Beta/Beta.A.mkv", "Beta.2021.Bluray-A", "Bluray-2160p", 6650)
	// Radarr replaced A with B: B adopted, A unlinked two seconds later.
	adopt(2, time.Hour, "/movies/Beta/Beta.B.mkv", "Beta.2021.Remux-B", "Remux-2160p", 6500)
	unlink(1, time.Hour+2*time.Second, "/movies/Beta/Beta.A.mkv", "Beta.2021.Bluray-A", "Bluray-2160p", 6650)
	// B removed by hand hours later, nothing adopted beside it.
	unlink(2, 5*time.Hour, "/movies/Beta/Beta.B.mkv", "Beta.2021.Remux-B", "Remux-2160p", 6500)
	// C adopted, and unlinked again 20 s later: the adopted file going, not one it replaced.
	adopt(3, 10*time.Hour, "/movies/Beta/Beta.C.mkv", "Beta.2021.Remux-C", "Remux-2160p", 7000)
	unlink(3, 10*time.Hour+20*time.Second, "/movies/Beta/Beta.C.mkv", "Beta.2021.Remux-C", "Remux-2160p", 7000)
	got := e.history(Filter{Title: "2"})
	want := []string{
		"removed Beta.2021.Remux-C Remux-2160p 7000 (deleted in Radarr)",
		"adopted Beta.2021.Remux-C Remux-2160p 7000",
		"removed Beta.2021.Remux-B Remux-2160p 6500 (deleted in Radarr)",
		"adopted Beta.2021.Remux-B Remux-2160p 6500 replaced Beta.2021.Bluray-A",
		"adopted Beta.2021.Bluray-A Bluray-2160p 6650",
	}
	if !slices.Equal(got, want) {
		t.Errorf("rows:\n got %q\nwant %q", got, want)
	}
	p, err := e.j.History(context.Background(), Filter{Title: "2", Events: []string{EvAdopted}})
	if err != nil {
		t.Fatal(err)
	}
	if r := p.Items[1].Replaced; len(r) != 1 || r[0].Path != "/movies/Beta/Beta.A.mkv" || r[0].Release.Quality !=
		"Bluray-2160p" || *r[0].Release.Score != 6650 {
		t.Errorf("replaced: %+v", r)
	}
	got = e.history(Filter{Title: "2", Events: []string{EvRemoved}})
	want = []string{
		"removed Beta.2021.Remux-C Remux-2160p 7000 (deleted in Radarr)",
		"removed Beta.2021.Remux-B Remux-2160p 6500 (deleted in Radarr)",
		"removed Beta.2021.Bluray-A Bluray-2160p 6650 (deleted in Radarr)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("removals only:\n got %q\nwant %q", got, want)
	}
	var n int
	for cur := ""; ; n++ {
		p, err := e.j.History(context.Background(), Filter{Title: "2", Limit: 1, Cursor: cur})
		if err != nil {
			t.Fatal(err)
		}
		if cur = p.NextCursor; cur == "" {
			break
		}
	}
	if n != 4 {
		t.Errorf("pages: %d, want 5", n+1)
	}
	// Radarr replaced Alpha's file at the same path: the adoption took over the link row (its creation time kept),
	// and the old file's unlink is folded into it.
	e.exec(`INSERT INTO link (id, path, target, title_id, release_title, quality, score, adopted_from, state, created_at,
		updated_at) VALUES (9, '/movies/Alpha/Alpha.mkv', '/t', 1, 'Alpha.2020.NEW', 'Remux-2160p', 8000,
		'radarr_4k /arr/a.mkv', 'active', ?, ?)`, ms(0), ms(8*time.Hour))
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, link_id, path, detail, release_title, quality,
		score) VALUES (?, 'link', 'applied', 'movie-web', 9, '/movies/Alpha/Alpha.mkv', 'adopted from radarr_4k /arr/a.mkv',
		'Alpha.2020.NEW', 'Remux-2160p', 8000)`, ms(8*time.Hour))
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, link_id, path, detail, release_title, quality,
		score, title_id) VALUES (?, 'replace', 'applied', 'movie-web', 9, '/movies/Alpha/Alpha.mkv',
		'unlinked (deleted in Radarr), moved aside to /trash/a', 'Alpha.2020.OLD', 'WEBDL-2160p', 4000, 1)`,
		ms(8*time.Hour+2*time.Second))
	got = e.history(Filter{Title: "1"})
	if want := []string{"adopted Alpha.2020.NEW Remux-2160p 8000 replaced Alpha.2020.OLD"}; !slices.Equal(got, want) {
		t.Errorf("same path:\n got %q\nwant %q", got, want)
	}
}

// A pack upgrade that replaced E01 and brought E02-E03 as well, partly imported: its one Upgraded row lists the
// episodes the import brought (the old file's in Replaced) and keeps the import's partial state.
func TestHistoryPackUpgrade(t *testing.T) {
	e := newEnv(t)
	at := testNow.Add(-time.Hour)
	e.dlEvent(60, "imported", at, 3, 3, "tv-2160p", hashC, "Gamma.S01.2160p.NEW", "WEBDL-2160p", 2000, 9<<30,
		[]int64{101, 102, 103, 104}, "")
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, plan, created_at, updated_at)
		VALUES (60, 'partial', '/m/G', '{}', ?, ?)`, msOf(at.Add(time.Minute)), msOf(at.Add(time.Minute)))
	for i, ep := range []int64{101, 102, 103} {
		e.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id, link_id, path, title_id, episodes)
			VALUES (?, 'link', 'applied', 'tv-2160p', 60, ?, ?, 3, ?)`, msOf(at.Add(time.Minute)), 70+i,
			fmt.Sprintf("/tv/G/new%d.mkv", ep), fmt.Sprintf("[%d]", ep))
	}
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id, path, detail, release_title, quality,
		score, title_id, episodes) VALUES (?, 'replace', 'applied', 'tv-2160p', 60, '/tv/G/S01E01.old.mkv',
		'moved aside to /trash/g', 'Gamma.S01E01.OLD', 'WEBDL-1080p', 500, 3, '[101]')`, msOf(at.Add(time.Minute))+1)
	got := e.history(Filter{Title: "3"})
	want := []string{
		"deleted Gamma.S01.2160p.NEW WEBDL-2160p 2000 [S01E01,S01E02,S01E03] replaced Gamma.S01E01.OLD",
		"grabbed Gamma.S01.2160p.NEW WEBDL-2160p 2000 [S01E01,S01E02,S01E03,S01E04]",
	}
	if !slices.Equal(got, want) {
		t.Errorf("rows:\n got %q\nwant %q", got, want)
	}
	p, err := e.j.History(context.Background(), Filter{Title: "3", Events: []string{EvDeleted}})
	if err != nil {
		t.Fatal(err)
	}
	if x := p.Items[0]; x.Import != "partial" || strings.Join(x.Replaced[0].Episodes, ",") != "S01E01" {
		t.Errorf("upgrade row: import %q, replaced %+v", x.Import, x.Replaced)
	}
}
