package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Playlists (docs/design/anime.md "Watch-order playlists"): admin-owned regular video playlists, which keep a set order and can mix
// episodes and films from several sections. Only the calls the watch-order sync needs: list and read playlists,
// create one, add, remove and move items, and list a section's files to find rating keys. Nothing here deletes a
// playlist or touches a library item. Callers gate every write (library.enabled and playlists.enabled).

// Playlist is a playlist as Plex lists it.
type Playlist struct {
	RatingKey string `json:"ratingKey"`
	Title     string `json:"title"`
	Smart     bool   `json:"smart"`
	Type      string `json:"playlistType"`
	LeafCount int    `json:"leafCount"`
}

// PlaylistItem is one entry of a playlist: the library item (RatingKey) and its place in the playlist (ItemID).
type PlaylistItem struct {
	RatingKey string
	ItemID    string
	Title     string
}

// FileItem is a library item found by its file.
type FileItem struct {
	RatingKey string
	Title     string
}

type container struct {
	MediaContainer struct {
		MachineIdentifier string            `json:"machineIdentifier"`
		Metadata          []json.RawMessage `json:"Metadata"`
	} `json:"MediaContainer"`
}

// flexString reads a JSON string or number (Plex sends ids as either).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexString(n.String())
	return nil
}

// call sends one request and decodes the container (nil out: discard).
func (c *Client) call(ctx context.Context, method, path string, q url.Values, out *container) error {
	u := *c.base
	u.Path += path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Plex-Token", c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.gw.Do(ctx, Target, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("plex: %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if out == nil || len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("plex: %s %s: %w", method, path, err)
	}
	return nil
}

// Identity returns the server's machine identifier (playlist URIs name it).
func (c *Client) Identity(ctx context.Context) (string, error) {
	var out container
	if err := c.call(ctx, http.MethodGet, "/identity", nil, &out); err != nil {
		return "", err
	}
	if out.MediaContainer.MachineIdentifier == "" {
		return "", fmt.Errorf("plex: /identity has no machineIdentifier")
	}
	return out.MediaContainer.MachineIdentifier, nil
}

// Playlists lists the token owner's video playlists.
func (c *Client) Playlists(ctx context.Context) ([]Playlist, error) {
	var out container
	if err := c.call(ctx, http.MethodGet, "/playlists", url.Values{"playlistType": {"video"}}, &out); err != nil {
		return nil, err
	}
	var ps []Playlist
	for _, m := range out.MediaContainer.Metadata {
		var p struct {
			RatingKey flexString `json:"ratingKey"`
			Title     string     `json:"title"`
			Smart     bool       `json:"smart"`
			Type      string     `json:"playlistType"`
			LeafCount int        `json:"leafCount"`
		}
		if err := json.Unmarshal(m, &p); err != nil {
			return nil, err
		}
		ps = append(ps, Playlist{RatingKey: string(p.RatingKey), Title: p.Title, Smart: p.Smart, Type: p.Type, LeafCount: p.LeafCount})
	}
	return ps, nil
}

// PlaylistItems lists a playlist's items in order.
func (c *Client) PlaylistItems(ctx context.Context, id string) ([]PlaylistItem, error) {
	var out container
	if err := c.call(ctx, http.MethodGet, "/playlists/"+url.PathEscape(id)+"/items", nil, &out); err != nil {
		return nil, err
	}
	var items []PlaylistItem
	for _, m := range out.MediaContainer.Metadata {
		var p struct {
			RatingKey flexString `json:"ratingKey"`
			ItemID    flexString `json:"playlistItemID"`
			Title     string     `json:"title"`
		}
		if err := json.Unmarshal(m, &p); err != nil {
			return nil, err
		}
		items = append(items, PlaylistItem{RatingKey: string(p.RatingKey), ItemID: string(p.ItemID), Title: p.Title})
	}
	return items, nil
}

func itemsURI(machine string, keys []string) string {
	return "server://" + machine + "/com.plexapp.plugins.library/library/metadata/" + strings.Join(keys, ",")
}

// CreatePlaylist creates a regular video playlist with items, and returns its rating key.
func (c *Client) CreatePlaylist(ctx context.Context, title, machine string, keys []string) (string, error) {
	if title == "" || len(keys) == 0 {
		return "", fmt.Errorf("plex: a playlist needs a title and items")
	}
	q := url.Values{"type": {"video"}, "title": {title}, "smart": {"0"}, "uri": {itemsURI(machine, keys)}}
	var out container
	if err := c.call(ctx, http.MethodPost, "/playlists", q, &out); err != nil {
		return "", err
	}
	for _, m := range out.MediaContainer.Metadata {
		var p struct {
			RatingKey flexString `json:"ratingKey"`
		}
		if json.Unmarshal(m, &p) == nil && p.RatingKey != "" {
			return string(p.RatingKey), nil
		}
	}
	return "", fmt.Errorf("plex: created playlist %q but got no rating key", title)
}

// AddToPlaylist appends items.
func (c *Client) AddToPlaylist(ctx context.Context, id, machine string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	return c.call(ctx, http.MethodPut, "/playlists/"+url.PathEscape(id)+"/items", url.Values{"uri": {itemsURI(machine, keys)}}, nil)
}

// RemoveFromPlaylist removes one playlist entry (the library item stays).
func (c *Client) RemoveFromPlaylist(ctx context.Context, id, itemID string) error {
	return c.call(ctx, http.MethodDelete, "/playlists/"+url.PathEscape(id)+"/items/"+url.PathEscape(itemID), nil, nil)
}

// MovePlaylistItem moves an entry after another (after "" moves it first).
func (c *Client) MovePlaylistItem(ctx context.Context, id, itemID, after string) error {
	q := url.Values{}
	if after != "" {
		q.Set("after", after)
	}
	return c.call(ctx, http.MethodPut, "/playlists/"+url.PathEscape(id)+"/items/"+url.PathEscape(itemID)+"/move", q, nil)
}

// Plex metadata types for SectionFiles.
const (
	TypeMovie   = 1
	TypeEpisode = 4
)

// SectionFiles lists a section's items of one type by file path: every part's file maps to the item's rating key.
func (c *Client) SectionFiles(ctx context.Context, section, typ int) (map[string]FileItem, error) {
	if section <= 0 {
		return nil, fmt.Errorf("plex: bad section %d", section)
	}
	var out container
	if err := c.call(ctx, http.MethodGet, "/library/sections/"+strconv.Itoa(section)+"/all",
		url.Values{"type": {strconv.Itoa(typ)}}, &out); err != nil {
		return nil, err
	}
	files := map[string]FileItem{}
	for _, m := range out.MediaContainer.Metadata {
		var it struct {
			RatingKey flexString `json:"ratingKey"`
			Title     string     `json:"title"`
			Media     []struct {
				Part []struct {
					File string `json:"file"`
				} `json:"Part"`
			} `json:"Media"`
		}
		if err := json.Unmarshal(m, &it); err != nil {
			return nil, err
		}
		for _, md := range it.Media {
			for _, p := range md.Part {
				if p.File != "" {
					files[p.File] = FileItem{RatingKey: string(it.RatingKey), Title: it.Title}
				}
			}
		}
	}
	return files, nil
}

// SyncResult counts what a playlist sync changed.
type SyncResult struct {
	RatingKey string `json:"rating_key"`
	Created   bool   `json:"created,omitempty"`
	Added     int    `json:"added,omitempty"`
	Removed   int    `json:"removed,omitempty"`
	Moved     int    `json:"moved,omitempty"`
}

// PlaylistAPI is what SyncPlaylist needs (Client, or a fake).
type PlaylistAPI interface {
	Playlists(ctx context.Context) ([]Playlist, error)
	PlaylistItems(ctx context.Context, id string) ([]PlaylistItem, error)
	CreatePlaylist(ctx context.Context, title, machine string, keys []string) (string, error)
	AddToPlaylist(ctx context.Context, id, machine string, keys []string) error
	RemoveFromPlaylist(ctx context.Context, id, itemID string) error
	MovePlaylistItem(ctx context.Context, id, itemID, after string) error
}

// SyncPlaylist makes a playlist hold exactly keys, in order, idempotently (diff, then move): it finds the playlist
// by id (known, e.g. stored from an earlier sync) or by title among regular video playlists, creates it when
// missing, removes entries that are not wanted (and duplicates), appends the missing ones, then moves entries into
// place. A playlist already in order gets no write at all.
func SyncPlaylist(ctx context.Context, api PlaylistAPI, machine, known, title string, keys []string) (SyncResult, error) {
	var res SyncResult
	lists, err := api.Playlists(ctx)
	if err != nil {
		return res, err
	}
	id := ""
	for _, p := range lists {
		if known != "" && p.RatingKey == known && !p.Smart {
			id = p.RatingKey
			break
		}
	}
	if id == "" {
		for _, p := range lists {
			if p.Title == title && !p.Smart && (p.Type == "" || p.Type == "video") {
				id = p.RatingKey
				break
			}
		}
	}
	if id == "" {
		if len(keys) == 0 {
			return res, nil
		}
		rk, err := api.CreatePlaylist(ctx, title, machine, keys)
		if err != nil {
			return res, err
		}
		return SyncResult{RatingKey: rk, Created: true, Added: len(keys)}, nil
	}
	res.RatingKey = id
	items, err := api.PlaylistItems(ctx, id)
	if err != nil {
		return res, err
	}
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	seen := map[string]bool{}
	var kept []PlaylistItem
	for _, it := range items {
		if !want[it.RatingKey] || seen[it.RatingKey] {
			if err := api.RemoveFromPlaylist(ctx, id, it.ItemID); err != nil {
				return res, err
			}
			res.Removed++
			continue
		}
		seen[it.RatingKey] = true
		kept = append(kept, it)
	}
	var missing []string
	for _, k := range keys {
		if !seen[k] {
			missing = append(missing, k)
			seen[k] = true
		}
	}
	if len(missing) > 0 {
		if err := api.AddToPlaylist(ctx, id, machine, missing); err != nil {
			return res, err
		}
		res.Added = len(missing)
		if kept, err = api.PlaylistItems(ctx, id); err != nil {
			return res, err
		}
	}
	// move into place: walk the wanted order; an entry out of place moves after the previous wanted one
	byKey := map[string]PlaylistItem{}
	for _, it := range kept {
		if _, dup := byKey[it.RatingKey]; !dup {
			byKey[it.RatingKey] = it
		}
	}
	cur := make([]string, 0, len(kept))
	for _, it := range kept {
		cur = append(cur, it.RatingKey)
	}
	for i, k := range keys {
		it, ok := byKey[k]
		if !ok {
			continue
		}
		if i < len(cur) && cur[i] == k {
			continue
		}
		after := ""
		if i > 0 {
			after = byKey[keys[i-1]].ItemID
		}
		if err := api.MovePlaylistItem(ctx, id, it.ItemID, after); err != nil {
			return res, err
		}
		res.Moved++
		cur = moveAfter(cur, k, i)
	}
	return res, nil
}

// moveAfter moves k to index i of cur.
func moveAfter(cur []string, k string, i int) []string {
	out := make([]string, 0, len(cur))
	for _, x := range cur {
		if x != k {
			out = append(out, x)
		}
	}
	if i > len(out) {
		i = len(out)
	}
	out = append(out[:i], append([]string{k}, out[i:]...)...)
	return out
}
