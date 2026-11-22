package plex

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/gateway"
)

const sessionsJSON = `{"MediaContainer":{"size":1,"Metadata":[{"sessionKey":"12","ratingKey":345,"type":"episode",
"title":"The Third","grandparentTitle":"Show","parentIndex":1,"index":3,"viewOffset":61000,"duration":3000000,
"User":{"id":"7","title":"alice"},"Player":{"title":"Living Room","product":"Plex for Android","platform":"Android",
"state":"buffering","local":false,"relayed":true},"Session":{"id":"s","bandwidth":207722,"location":"wan"},
"Media":[{"bitrate":8000,"videoResolution":"1080","container":"mkv","Part":[{"file":"/other.mkv","size":1}]},
{"bitrate":91555,"videoResolution":"4k","container":"mkv","selected":true,"Part":[{"file":"/media/tv/Show/S01E03.mkv","size":123,"decision":"transcode","selected":true}]}],
"TranscodeSession":{"videoDecision":"transcode","audioDecision":"copy","speed":0.7,"throttled":false}}]}}`

type httpDoer struct{ last *http.Request }

func (d *httpDoer) Do(ctx context.Context, _ gateway.Target, req *http.Request) (*http.Response, error) {
	d.last = req
	return http.DefaultClient.Do(req)
}

func TestSessions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status/sessions" || r.Header.Get("X-Plex-Token") != "tok-1" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, sessionsJSON)
	}))
	defer srv.Close()
	c, _ := New(&httpDoer{}, srv.URL, "tok-1")
	ss, err := c.Sessions(context.Background())
	if err != nil || len(ss) != 1 {
		t.Fatalf("%v %+v", err, ss)
	}
	s := ss[0]
	if s.SessionKey != "12" || s.ID != "s" || s.RatingKey != "345" || s.UserID != "7" || s.UserName != "alice" || s.Player != "Living Room" ||
		s.State != "buffering" || s.Local || !s.Relayed || s.Location != "wan" || s.Bitrate != 91555 ||
		s.File != "/media/tv/Show/S01E03.mkv" || s.Decision != "transcode" || s.Transcode == nil ||
		s.Transcode.Speed != 0.7 || s.ParentIndex != 1 || s.Index != 3 || s.ViewOffset != 61000 {
		t.Errorf("session %+v", s)
	}
}

// wsFrame is a server frame (unmasked).
func wsFrame(op byte, payload string) []byte {
	b := []byte{0x80 | op}
	switch n := len(payload); {
	case n < 126:
		b = append(b, byte(n))
	default:
		b = append(b, 126, byte(n>>8), byte(n))
	}
	return append(b, payload...)
}

func TestNotifications(t *testing.T) {
	playing := `{"NotificationContainer":{"type":"playing","size":1,"PlaySessionStateNotification":[{"sessionKey":"12",` +
		`"ratingKey":"345","state":"buffering","viewOffset":62000}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/:/websockets/notifications" || r.Header.Get("X-Plex-Token") != "tok-1" ||
			r.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "no", http.StatusBadRequest)
			return
		}
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + wsGUID))
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
		_, _ = buf.Write(wsFrame(0x1, `{"NotificationContainer":{"type":"timeline"}}`))
		_, _ = buf.Write(wsFrame(0x9, "ping"))
		// A fragmented message: text without FIN, then the continuation with it.
		first := wsFrame(0x1, playing[:20])
		first[0] &^= 0x80
		_, _ = buf.Write(first)
		_, _ = buf.Write(wsFrame(0x0, playing[20:]))
		_, _ = buf.Write(wsFrame(0x8, ""))
		_ = buf.Flush()
	}))
	defer srv.Close()
	c, _ := New(&httpDoer{}, srv.URL, "tok-1")
	var got []PlayEvent
	err := c.Notifications(context.Background(), func(e PlayEvent) { got = append(got, e) })
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("err %v", err)
	}
	if len(got) != 1 || got[0] != (PlayEvent{SessionKey: "12", RatingKey: "345", State: "buffering", ViewOffset: 62000}) {
		t.Errorf("events %+v", got)
	}
	// A server that does not upgrade.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer plain.Close()
	c, _ = New(&httpDoer{}, plain.URL, "tok-1")
	if err := c.Notifications(context.Background(), func(PlayEvent) {}); err == nil {
		t.Error("no upgrade accepted")
	}
}

func TestReadMessageLimit(t *testing.T) {
	b := []byte{0x81, 127, 0, 0, 0, 0, 0x10, 0, 0, 0}
	if _, err := readMessage(bufio.NewReader(strings.NewReader(string(b))), nil); err == nil {
		t.Error("oversized frame accepted")
	}
}

// A video session without its part's file: the item's metadata has it, matched by the selected media and part ids.
func TestSessionFileFromMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status/sessions":
			io.WriteString(w, `{"MediaContainer":{"Metadata":[{"sessionKey":"42","ratingKey":"99","type":"episode","title":"Episode","User":{"id":"7","title":"Alice"},"Player":{"state":"playing"},"Media":[{"id":21,"selected":true,"Part":[{"id":102,"selected":true}]}]}]}}`)
		case "/library/metadata/99":
			io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"99","Media":[{"id":20,"Part":[{"id":101,"file":"/wrong.mkv","size":1}]},{"id":21,"Part":[{"id":102,"file":"/right.mkv","size":2}]}]}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, _ := New(&httpDoer{}, srv.URL, "test-token")
	ss, err := c.Sessions(context.Background())
	if err != nil || len(ss) != 1 {
		t.Fatalf("%v %#v", err, ss)
	}
	if ss[0].File != "/right.mkv" || ss[0].Size != 2 {
		t.Errorf("file %q size %d, want /right.mkv 2", ss[0].File, ss[0].Size)
	}
}

// Terminate asks GET /status/sessions/terminate with the session id and the reason, the token in the header only.
func TestTerminate(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		if r.URL.Query().Get("sessionId") == "refuse" {
			http.Error(w, "no", http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	c, _ := New(&httpDoer{}, srv.URL, "tok-1")
	msg := "This file stopped working. Try again later."
	if err := c.Terminate(context.Background(), "abc123", msg); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodGet || got.URL.Path != "/status/sessions/terminate" ||
		got.URL.Query().Get("sessionId") != "abc123" || got.URL.Query().Get("reason") != msg ||
		got.Header.Get("X-Plex-Token") != "tok-1" || strings.Contains(got.URL.RawQuery, "tok-1") {
		t.Errorf("request %s %s token %q", got.Method, got.URL, got.Header.Get("X-Plex-Token"))
	}
	if err := c.Terminate(context.Background(), "refuse", msg); err == nil {
		t.Error("a refusal is not an error")
	}
	if err := c.Terminate(context.Background(), "", msg); err == nil {
		t.Error("no session id: no error")
	}
}

// A session that lists several media, or several parts of its media, without saying which one plays is ambiguous: its
// file is only a guess (the first). One entry, or a selected one, is not.
func TestSessionAmbiguousMedia(t *testing.T) {
	for _, c := range []struct {
		media string
		want  bool
	}{
		{`[{"Part":[{"file":"/a.mkv"}]}]`, false},
		{`[{"Part":[{"file":"/a.mkv"}]},{"Part":[{"file":"/b.mkv"}]}]`, true},
		{`[{"Part":[{"file":"/a.mkv"}]},{"selected":true,"Part":[{"file":"/b.mkv"}]}]`, false},
		{`[{"Part":[{"file":"/a.mkv"},{"file":"/b.mkv"}]}]`, true},
		{`[{"Part":[{"file":"/a.mkv"},{"file":"/b.mkv","selected":true}]}]`, false},
		{`[{"selected":true,"Part":[{"file":"/a.mkv"},{"file":"/b.mkv"}]}]`, true},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"MediaContainer":{"Metadata":[{"sessionKey":"1","type":"movie","Player":{"state":"playing"},"Media":`+
				c.media+`}]}}`)
		}))
		cl, _ := New(&httpDoer{}, srv.URL, "test-token")
		ss, err := cl.Sessions(context.Background())
		srv.Close()
		if err != nil || len(ss) != 1 {
			t.Fatalf("%v %#v", err, ss)
		}
		if ss[0].Ambiguous != c.want {
			t.Errorf("%s: ambiguous %v, want %v", c.media, ss[0].Ambiguous, c.want)
		}
	}
}
