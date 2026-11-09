package plex

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Now playing (docs/design/now-playing.md): GET /status/sessions, and the notification websocket for playing-state
// changes, the only source of buffering events. Both read only.

// Session is one playback as /status/sessions reports it, with the selected media and part.
type Session struct {
	SessionKey       string
	RatingKey        string
	Type             string // movie, episode, track...
	Title            string
	GrandparentTitle string
	ParentIndex      int
	Index            int
	ViewOffset       int64 // ms
	Duration         int64 // ms
	UserID           string
	UserName         string
	Player           string // the player's title, or its product
	Product          string
	Platform         string
	State            string // playing, paused, buffering
	Local            bool
	Relayed          bool
	Location         string // lan or wan
	Bandwidth        int64  // kbps Plex allotted, not measured
	Bitrate          int64  // kbps of the media
	Resolution       string
	Container        string
	File             string
	Size             int64
	Decision         string // directplay, copy, transcode
	// Transcode is set only while Plex transcodes.
	Transcode *Transcode
}

// Transcode is a session's TranscodeSession.
type Transcode struct {
	VideoDecision string  `json:"videoDecision"`
	AudioDecision string  `json:"audioDecision"`
	Speed         float64 `json:"speed"`
	Throttled     bool    `json:"throttled"`
}

type sessionJSON struct {
	SessionKey       flexString `json:"sessionKey"`
	RatingKey        flexString `json:"ratingKey"`
	Type             string     `json:"type"`
	Title            string     `json:"title"`
	GrandparentTitle string     `json:"grandparentTitle"`
	ParentIndex      int        `json:"parentIndex"`
	Index            int        `json:"index"`
	ViewOffset       int64      `json:"viewOffset"`
	Duration         int64      `json:"duration"`
	User             struct {
		ID    flexString `json:"id"`
		Title string     `json:"title"`
	} `json:"User"`
	Player struct {
		Title    string `json:"title"`
		Product  string `json:"product"`
		Platform string `json:"platform"`
		State    string `json:"state"`
		Local    bool   `json:"local"`
		Relayed  bool   `json:"relayed"`
	} `json:"Player"`
	Session struct {
		Bandwidth int64  `json:"bandwidth"`
		Location  string `json:"location"`
	} `json:"Session"`
	Media []struct {
		ID              flexString `json:"id"`
		Bitrate         int64      `json:"bitrate"`
		VideoResolution string     `json:"videoResolution"`
		Container       string     `json:"container"`
		Selected        bool       `json:"selected"`
		Part            []struct {
			ID       flexString `json:"id"`
			File     string     `json:"file"`
			Size     int64      `json:"size"`
			Decision string     `json:"decision"`
			Selected bool       `json:"selected"`
		} `json:"Part"`
	} `json:"Media"`
	TranscodeSession *Transcode `json:"TranscodeSession"`
}

// Sessions lists the current playbacks.
func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	var out container
	if err := c.call(ctx, http.MethodGet, "/status/sessions", nil, &out); err != nil {
		return nil, err
	}
	var ss []Session
	for _, m := range out.MediaContainer.Metadata {
		var j sessionJSON
		if err := json.Unmarshal(m, &j); err != nil {
			return nil, fmt.Errorf("plex: /status/sessions: %w", err)
		}
		s := Session{SessionKey: string(j.SessionKey), RatingKey: string(j.RatingKey), Type: j.Type, Title: j.Title,
			GrandparentTitle: j.GrandparentTitle, ParentIndex: j.ParentIndex, Index: j.Index, ViewOffset: j.ViewOffset,
			Duration: j.Duration, UserID: string(j.User.ID), UserName: j.User.Title, Player: j.Player.Title,
			Product: j.Player.Product, Platform: j.Player.Platform, State: j.Player.State, Local: j.Player.Local,
			Relayed: j.Player.Relayed, Location: j.Session.Location, Bandwidth: j.Session.Bandwidth,
			Transcode: j.TranscodeSession}
		if s.Player == "" {
			s.Player = s.Product
		}
		// The selected media and part; the first when none is marked (a single version).
		if mi := selected(len(j.Media), func(i int) bool { return j.Media[i].Selected }); mi >= 0 {
			md := j.Media[mi]
			s.Bitrate, s.Resolution, s.Container = md.Bitrate, md.VideoResolution, md.Container
			if pi := selected(len(md.Part), func(i int) bool { return md.Part[i].Selected }); pi >= 0 {
				pt := md.Part[pi]
				s.File, s.Size, s.Decision = pt.File, pt.Size, pt.Decision
				if s.File == "" && s.Type != "track" && s.RatingKey != "" {
					// Plex may leave a video session's file out: the item's metadata has it, by media and part id.
					s.File, s.Size = c.partFile(ctx, s.RatingKey, string(md.ID), string(pt.ID), s.Size)
				}
			}
		}
		ss = append(ss, s)
	}
	return ss, nil
}

// partFile looks up a part's file (and size, when size is 0) in /library/metadata/<ratingKey>, by its media and part
// ids (the first when an id is empty). "" when the lookup fails: the session is still listed, without a source.
func (c *Client) partFile(ctx context.Context, ratingKey, mediaID, partID string, size int64) (string, int64) {
	var out container
	if err := c.call(ctx, http.MethodGet, "/library/metadata/"+url.PathEscape(ratingKey), nil, &out); err != nil ||
		len(out.MediaContainer.Metadata) == 0 {
		return "", size
	}
	var j struct {
		Media []struct {
			ID   flexString `json:"id"`
			Part []struct {
				ID   flexString `json:"id"`
				File string     `json:"file"`
				Size int64      `json:"size"`
			} `json:"Part"`
		} `json:"Media"`
	}
	if json.Unmarshal(out.MediaContainer.Metadata[0], &j) != nil {
		return "", size
	}
	for _, m := range j.Media {
		if mediaID != "" && string(m.ID) != mediaID {
			continue
		}
		for _, p := range m.Part {
			if partID != "" && string(p.ID) != partID {
				continue
			}
			if size == 0 {
				size = p.Size
			}
			return p.File, size
		}
	}
	return "", size
}

// selected is the index of the first selected item, else 0; -1 when there is none.
func selected(n int, sel func(int) bool) int {
	for i := range n {
		if sel(i) {
			return i
		}
	}
	return min(0, n-1)
}

// PlayEvent is one playing-state change from the notification websocket.
type PlayEvent struct {
	SessionKey string
	RatingKey  string
	State      string // playing, paused, buffering, stopped
	ViewOffset int64
}

// wsLifetime bounds one websocket connection: the gateway gives the request this long, then it is opened again.
const wsLifetime = time.Hour

// wsMaxMessage bounds one notification message.
const wsMaxMessage = 1 << 20

// wsGUID is RFC 6455's key suffix.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Notifications opens /:/websockets/notifications through the gateway and calls on for each playing-state change
// until the connection ends (an error), ctx ends (nil, and the connection is closed at once) or wsLifetime passes.
// The only frames it sends are pongs to the server's pings.
func (c *Client) Notifications(ctx context.Context, on func(PlayEvent)) error {
	u := *c.base
	u.Path += "/:/websockets/notifications"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	key := base64.StdEncoding.EncodeToString(nonce[:])
	req.Header.Set("X-Plex-Token", c.token)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", key)
	t := Target
	t.Wait, t.MaxBytes = wsLifetime, 1<<40
	resp, err := c.gw.Do(ctx, t, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// An upgraded connection outlives its request's context: close it when ctx ends, which ends the read below.
	stop := context.AfterFunc(ctx, func() { resp.Body.Close() })
	defer stop()
	sum := sha1.Sum([]byte(key + wsGUID))
	if resp.StatusCode != http.StatusSwitchingProtocols ||
		resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		return fmt.Errorf("plex: websocket: HTTP %d, no upgrade", resp.StatusCode)
	}
	r := bufio.NewReader(resp.Body)
	var pong func([]byte)
	if w, ok := resp.Body.(io.Writer); ok {
		pong = func(p []byte) { _, _ = w.Write(pongFrame(p)) }
	}
	for {
		msg, err := readMessage(r, pong)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("plex: websocket: %w", err)
		}
		var n struct {
			NotificationContainer struct {
				Type   string `json:"type"`
				States []struct {
					SessionKey flexString `json:"sessionKey"`
					RatingKey  flexString `json:"ratingKey"`
					State      string     `json:"state"`
					ViewOffset int64      `json:"viewOffset"`
				} `json:"PlaySessionStateNotification"`
			} `json:"NotificationContainer"`
		}
		if json.Unmarshal(msg, &n) != nil || n.NotificationContainer.Type != "playing" {
			continue
		}
		for _, s := range n.NotificationContainer.States {
			on(PlayEvent{SessionKey: string(s.SessionKey), RatingKey: string(s.RatingKey), State: s.State,
				ViewOffset: s.ViewOffset})
		}
	}
}

var errWSClosed = errors.New("closed by the server")

// pongFrame is the client's answer to a ping: a masked pong frame carrying the ping's payload (at most 125 bytes,
// as a control frame's).
func pongFrame(payload []byte) []byte {
	payload = payload[:min(len(payload), 125)]
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	f := append([]byte{0x8A, 0x80 | byte(len(payload))}, mask[:]...)
	for i, b := range payload {
		f = append(f, b^mask[i%4])
	}
	return f
}

// readMessage reads one data message (text or binary, reassembled from its fragments), skipping pongs and
// answering pings with pong (when set).
func readMessage(r *bufio.Reader, pong func([]byte)) ([]byte, error) {
	var msg []byte
	for {
		var h [2]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return nil, err
		}
		fin, op := h[0]&0x80 != 0, h[0]&0x0f
		n := uint64(h[1] & 0x7f)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		var mask [4]byte
		masked := h[1]&0x80 != 0
		if masked {
			if _, err := io.ReadFull(r, mask[:]); err != nil {
				return nil, err
			}
		}
		if n > wsMaxMessage || uint64(len(msg))+n > wsMaxMessage {
			return nil, fmt.Errorf("message over %d bytes", wsMaxMessage)
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		switch {
		case op == 0x8:
			return nil, errWSClosed
		case op == 0x9:
			if pong != nil {
				pong(payload)
			}
			continue
		case op >= 0x8: // pong
			continue
		}
		msg = append(msg, payload...)
		if fin {
			return msg, nil
		}
	}
}
