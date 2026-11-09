package plex

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/gateway"
)

// memPlex is an in-memory playlist server.
type memPlex struct {
	lists  map[string][]PlaylistItem
	titles map[string]string
	next   int
	writes []string
}

func newMem() *memPlex {
	return &memPlex{lists: map[string][]PlaylistItem{}, titles: map[string]string{}, next: 100}
}

func (m *memPlex) Playlists(context.Context) ([]Playlist, error) {
	var out []Playlist
	for id, t := range m.titles {
		out = append(out, Playlist{RatingKey: id, Title: t, Type: "video"})
	}
	return out, nil
}
func (m *memPlex) PlaylistItems(_ context.Context, id string) ([]PlaylistItem, error) {
	return append([]PlaylistItem(nil), m.lists[id]...), nil
}
func (m *memPlex) item(k string) PlaylistItem {
	m.next++
	return PlaylistItem{RatingKey: k, ItemID: fmt.Sprint(m.next)}
}
func (m *memPlex) CreatePlaylist(_ context.Context, title, _ string, keys []string) (string, error) {
	m.writes = append(m.writes, "create")
	m.next++
	id := fmt.Sprint(m.next)
	m.titles[id] = title
	for _, k := range keys {
		m.lists[id] = append(m.lists[id], m.item(k))
	}
	return id, nil
}
func (m *memPlex) AddToPlaylist(_ context.Context, id, _ string, keys []string) error {
	m.writes = append(m.writes, "add")
	for _, k := range keys {
		m.lists[id] = append(m.lists[id], m.item(k))
	}
	return nil
}
func (m *memPlex) RemoveFromPlaylist(_ context.Context, id, itemID string) error {
	m.writes = append(m.writes, "remove")
	var keep []PlaylistItem
	for _, it := range m.lists[id] {
		if it.ItemID != itemID {
			keep = append(keep, it)
		}
	}
	m.lists[id] = keep
	return nil
}
func (m *memPlex) MovePlaylistItem(_ context.Context, id, itemID, after string) error {
	m.writes = append(m.writes, "move")
	var it PlaylistItem
	var rest []PlaylistItem
	for _, x := range m.lists[id] {
		if x.ItemID == itemID {
			it = x
		} else {
			rest = append(rest, x)
		}
	}
	at := 0
	for i, x := range rest {
		if x.ItemID == after {
			at = i + 1
		}
	}
	m.lists[id] = append(rest[:at:at], append([]PlaylistItem{it}, rest[at:]...)...)
	return nil
}

func (m *memPlex) keys(id string) string {
	var ks []string
	for _, it := range m.lists[id] {
		ks = append(ks, it.RatingKey)
	}
	return strings.Join(ks, ",")
}

func TestSyncPlaylist(t *testing.T) {
	ctx := context.Background()
	m := newMem()
	res, err := SyncPlaylist(ctx, m, "mach", "", "Re:Zero (watch order)", []string{"1", "2", "3"})
	if err != nil || !res.Created || m.keys(res.RatingKey) != "1,2,3" {
		t.Fatalf("create: %+v %v %s", res, err, m.keys(res.RatingKey))
	}
	id := res.RatingKey
	// the same items again: no write
	m.writes = nil
	if res, err = SyncPlaylist(ctx, m, "mach", id, "Re:Zero (watch order)", []string{"1", "2", "3"}); err != nil || len(m.writes) != 0 {
		t.Errorf("idempotent: %+v %v %v", res, err, m.writes)
	}
	// reorder, drop one, add one, and a duplicate someone added by hand
	m.lists[id] = append(m.lists[id], m.item("2"))
	res, err = SyncPlaylist(ctx, m, "mach", id, "Re:Zero (watch order)", []string{"4", "3", "1"})
	if err != nil || m.keys(id) != "4,3,1" || res.Removed != 2 || res.Added != 1 {
		t.Errorf("diff: %+v %v -> %s", res, err, m.keys(id))
	}
	// found by title when the id is unknown
	m.writes = nil
	if res, err = SyncPlaylist(ctx, m, "mach", "", "Re:Zero (watch order)", []string{"4", "3", "1"}); err != nil || res.RatingKey != id || len(m.writes) != 0 {
		t.Errorf("by title: %+v %v", res, err)
	}
	for _, want := range [][]string{{"1", "3", "4"}, {"3", "4", "1"}, {"1", "4", "3"}} {
		if _, err := SyncPlaylist(ctx, m, "mach", id, "x", want); err != nil || m.keys(id) != strings.Join(want, ",") {
			t.Errorf("order %v: got %s", want, m.keys(id))
		}
	}
}

type recDoer struct {
	reqs []*http.Request
	body string
}

func (d *recDoer) Do(_ context.Context, _ gateway.Target, r *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, r)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(d.body))}, nil
}

func TestPlaylistRequests(t *testing.T) {
	ctx := context.Background()
	d := &recDoer{}
	c, _ := New(d, "http://plex:32400", "tok")
	d.body = `{"MediaContainer": {"Metadata": [{"ratingKey": 7, "title": "A - S01E01", "Media": [{"Part": [{"file": "/media/anime/A/Season 01/a.mkv"}]}]}]}}`
	files, err := c.SectionFiles(ctx, 5, TypeEpisode)
	if err != nil || files["/media/anime/A/Season 01/a.mkv"].RatingKey != "7" {
		t.Fatalf("section files %v %v", files, err)
	}
	if r := d.reqs[0]; r.URL.Path != "/library/sections/5/all" || r.URL.Query().Get("type") != "4" || r.Header.Get("X-Plex-Token") != "tok" {
		t.Errorf("request %s", r.URL)
	}
	d.body = `{"MediaContainer": {"Metadata": [{"ratingKey": "900"}]}}`
	rk, err := c.CreatePlaylist(ctx, "Re:Zero (watch order)", "mach", []string{"7", "8"})
	if err != nil || rk != "900" {
		t.Fatal(rk, err)
	}
	r := d.reqs[1]
	if r.Method != http.MethodPost || r.URL.Path != "/playlists" || r.URL.Query().Get("uri") != "server://mach/com.plexapp.plugins.library/library/metadata/7,8" ||
		r.URL.Query().Get("smart") != "0" || strings.Contains(r.URL.String(), "tok") {
		t.Errorf("create %s %s", r.Method, r.URL)
	}
	d.body = ""
	if err := c.MovePlaylistItem(ctx, "900", "11", "10"); err != nil {
		t.Fatal(err)
	}
	if r := d.reqs[2]; r.Method != http.MethodPut || r.URL.Path != "/playlists/900/items/11/move" || r.URL.Query().Get("after") != "10" {
		t.Errorf("move %s %s", r.Method, r.URL)
	}
	if err := c.RemoveFromPlaylist(ctx, "900", "11"); err != nil || d.reqs[3].Method != http.MethodDelete {
		t.Errorf("remove %v", err)
	}
	d.body = `{"MediaContainer": {"machineIdentifier": "abc"}}`
	if m, err := c.Identity(ctx); err != nil || m != "abc" {
		t.Errorf("identity %q %v", m, err)
	}
}
