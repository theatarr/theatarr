package playlists

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animestore"
	"github.com/theatarr/theatarr/internal/metadata/anime/offlinecache"
	"github.com/theatarr/theatarr/internal/plex"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// fakePlex counts every call; it holds playlists in memory and knows the files of section 30.
type fakePlex struct {
	calls  []string
	files  map[string]plex.FileItem
	lists  map[string][]plex.PlaylistItem
	titles map[string]string
	next   int
}

func (f *fakePlex) Identity(context.Context) (string, error) {
	f.calls = append(f.calls, "identity")
	return "machine", nil
}
func (f *fakePlex) SectionFiles(_ context.Context, section, typ int) (map[string]plex.FileItem, error) {
	f.calls = append(f.calls, fmt.Sprintf("section %d type %d", section, typ))
	if section != 30 || typ != plex.TypeEpisode {
		return nil, nil
	}
	return f.files, nil
}
func (f *fakePlex) Playlists(context.Context) ([]plex.Playlist, error) {
	f.calls = append(f.calls, "playlists")
	var out []plex.Playlist
	for id, t := range f.titles {
		out = append(out, plex.Playlist{RatingKey: id, Title: t, Type: "video"})
	}
	return out, nil
}
func (f *fakePlex) PlaylistItems(_ context.Context, id string) ([]plex.PlaylistItem, error) {
	f.calls = append(f.calls, "items")
	return f.lists[id], nil
}
func (f *fakePlex) CreatePlaylist(_ context.Context, title, machine string, keys []string) (string, error) {
	f.calls = append(f.calls, "create "+title)
	f.next++
	id := fmt.Sprint(1000 + f.next)
	f.titles[id] = title
	for i, k := range keys {
		f.lists[id] = append(f.lists[id], plex.PlaylistItem{RatingKey: k, ItemID: fmt.Sprint(i + 1)})
	}
	return id, nil
}
func (f *fakePlex) AddToPlaylist(context.Context, string, string, []string) error {
	f.calls = append(f.calls, "add")
	return nil
}
func (f *fakePlex) RemoveFromPlaylist(context.Context, string, string) error {
	f.calls = append(f.calls, "remove")
	return nil
}
func (f *fakePlex) MovePlaylistItem(context.Context, string, string, string) error {
	f.calls = append(f.calls, "move")
	return nil
}

func (f *fakePlex) writes() int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "create") || c == "add" || c == "remove" || c == "move" {
			n++
		}
	}
	return n
}

const fixture = "../testdata/offline"

// setup saves the Re:Zero build and gives TVDB 305089 an arr file for every mapped episode, and one active link.
func setup(t *testing.T) (*store.Store, *fakePlex) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	src, _, err := offlinecache.Sources(filepath.Join(fixture, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	var fs []anime.Franchise
	b, _ := os.ReadFile(filepath.Join(fixture, "franchises.json"))
	if err := json.Unmarshal(b, &fs); err != nil {
		t.Fatal(err)
	}
	var res *anime.Result
	for _, f := range fs {
		if f.Slug == "re-zero" {
			ov, _ := anime.LoadOverrides(filepath.Join(fixture, "overrides"), f.Slug)
			if res, err = anime.Build(ctx, src, f, anime.Options{Today: "2026-10-25", Overrides: ov}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := (&animestore.Store{DB: st}).Save(ctx, res, "cache", false); err != nil {
		t.Fatal(err)
	}
	w := st.Writer()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := w.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO title (id, kind, name, tvdb_id, series_type, created_at, updated_at) VALUES (1, 'series', 'Re:Zero', 305089, 'anime', 0, 0)`)
	exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'anime-tv', 0, 0)`)
	rows, err := st.Reader().Query(`SELECT episodes FROM anime_mapping WHERE tvdb_series = 305089`)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[anime.SE]bool{}
	for rows.Next() {
		var s string
		var eps []anime.SE
		rows.Scan(&s)
		json.Unmarshal([]byte(s), &eps)
		for _, se := range eps {
			seen[se] = true
		}
	}
	rows.Close()
	fp := &fakePlex{files: map[string]plex.FileItem{}, lists: map[string][]plex.PlaylistItem{}, titles: map[string]string{}}
	id := 0
	for se := range seen {
		id++
		p := fmt.Sprintf("/media/anime/Re ZERO/Season %02d/Re ZERO - %s.mkv", se[0], se)
		exec(`INSERT INTO episode (id, title_id, season, number, created_at, updated_at) VALUES (?, 1, ?, ?, 0, 0)`, id, se[0], se[1])
		exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path) VALUES (?, 1, 1, 'sonarr_anime_debrid', ?, ?)`, id, id, p)
		exec(`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (?, ?)`, id, id)
		fp.files[p] = plex.FileItem{RatingKey: fmt.Sprint(5000 + id)}
		if se == (anime.SE{1, 1}) {
			exec(`INSERT INTO link (id, path, target, title_id, created_at, updated_at) VALUES (1, '/media/anime/Re ZERO/S01E01.mkv', '/mnt/x', 1, 0, 0)`)
			exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (1, ?)`, id)
			fp.files["/media/anime/Re ZERO/S01E01.mkv"] = plex.FileItem{RatingKey: "7777"}
		}
	}
	if len(seen) < 50 {
		t.Fatalf("only %d mapped episodes", len(seen))
	}
	return st, fp
}

func pol(library, playlists bool) *policy.Policy {
	return &policy.Policy{
		Library: &policy.Library{Enabled: library, Plex: &policy.Plex{URL: "http://plex:32400", TokenEnv: "THEATARR_PLEX_TOKEN"},
			Versions: map[string]*policy.LibraryVersion{"anime-tv": {Roots: []string{"/media/anime"}, PlexSection: 30}}},
		Playlists: &policy.Playlists{Enabled: playlists},
	}
}

// TestGated is the proof that playlists are gated: unless library.enabled and playlists.enabled are both true (and
// there is a token), a sync makes no Plex call at all, not even a read, and records a dry run.
func TestGated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, c := range []struct {
		library, playlists bool
		client             bool
		why                string
	}{
		{false, false, true, "library.enabled is false"},
		{false, true, true, "library.enabled is false"},
		{true, false, true, "playlists.enabled is false"},
		{true, true, false, "no Plex token"},
	} {
		st, fp := setup(t)
		s := &Syncer{Store: st, Policy: pol(c.library, c.playlists)}
		if c.client {
			s.Plex = fp
		}
		rep, err := s.Sync(ctx, "", false)
		if err != nil {
			t.Fatal(err)
		}
		if !rep.DryRun || !strings.Contains(rep.Refused, c.why) || len(fp.calls) != 0 {
			t.Errorf("%+v: dry run %v (%s), Plex calls %v", c, rep.DryRun, rep.Refused, fp.calls)
		}
		var n int
		st.Reader().QueryRow(`SELECT count(*) FROM plex_playlist WHERE mode = 'dry_run' AND rating_key = ''`).Scan(&n)
		if n != len(rep.Playlists) || n != 2 {
			t.Errorf("%+v: %d dry-run rows for %d playlists", c, n, len(rep.Playlists))
		}
	}
	// enabled, but a dry run asked for: still no call
	st, fp := setup(t)
	rep, err := (&Syncer{Store: st, Policy: pol(true, true), Plex: fp}).Sync(ctx, "re-zero", true)
	if err != nil || !rep.DryRun || len(fp.calls) != 0 {
		t.Errorf("dry run: %v %v %v", rep.DryRun, err, fp.calls)
	}
}

func TestPlanAndSync(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, fp := setup(t)
	s := &Syncer{Store: st, Policy: pol(true, true), Plex: fp, Now: func() time.Time { return time.Unix(1e9, 0) }}
	pls, err := s.Plan(ctx, "re-zero")
	if err != nil {
		t.Fatal(err)
	}
	if len(pls) != 2 || !strings.HasSuffix(pls[0].Name, " (watch order)") || !strings.HasSuffix(pls[1].Name, " (story order)") ||
		!strings.HasPrefix(pls[0].Name, "Re:Zero") {
		t.Fatalf("playlists %s, %s", pls[0].Name, pls[len(pls)-1].Name)
	}
	story := pls[1]
	// Memory Snow (S00E26) comes after S01E11 in the story order (the override), inside season 1
	pos := map[string]int{}
	for i, it := range story.Items {
		if _, ok := pos[it.Episode]; !ok {
			pos[it.Episode] = i
		}
	}
	if !(pos["S01E11"] < pos["S00E26"] && pos["S00E26"] < pos["S01E12"]) {
		t.Errorf("story order: S01E11 at %d, Memory Snow at %d, S01E12 at %d", pos["S01E11"], pos["S00E26"], pos["S01E12"])
	}
	if it := story.Items[pos["S01E01"]]; it.From != "link" || it.Path != "/media/anime/Re ZERO/S01E01.mkv" {
		t.Errorf("S01E01 should be our link: %+v", it)
	}
	if story.Ready < 50 {
		t.Errorf("ready %d, missing %d", story.Ready, story.Missing)
	}

	rep, err := s.Sync(ctx, "re-zero", false)
	if err != nil || rep.DryRun {
		t.Fatalf("%v %+v", err, rep)
	}
	if fp.writes() != 2 || fp.calls[0] != "identity" {
		t.Errorf("calls %v", fp.calls)
	}
	for _, pl := range rep.Playlists {
		if pl.Sync == nil || !pl.Sync.Created || pl.Error != "" {
			t.Errorf("%s: %+v %s", pl.Name, pl.Sync, pl.Error)
		}
	}
	var rk, mode string
	st.Reader().QueryRow(`SELECT rating_key, mode FROM plex_playlist WHERE franchise = 're-zero' AND order_kind = 'story'`).Scan(&rk, &mode)
	if rk == "" || mode != "applied" {
		t.Errorf("recorded %q %q", rk, mode)
	}
	// a second sync changes nothing in Plex
	fp.calls = nil
	if _, err := s.Sync(ctx, "re-zero", false); err != nil || fp.writes() != 0 {
		t.Errorf("second sync wrote: %v %v", err, fp.calls)
	}
}
