package adder

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTorBoxCacheEvidence(t *testing.T) {
	for _, tc := range []struct {
		detail string
		ready  bool
	}{
		{"Found Cached Torrent. Using Cached Torrent.", true},
		{"Torrent added; not cached", false}, {"Torrent added; uncached", false},
		{"Torrent added", false},
	} {
		t.Run(tc.detail, func(t *testing.T) {
			f := newFakeNet(t)
			f.serve("tb.test", func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"success":true,"detail":%q,"data":{"torrent_id":1,"hash":%q}}`, tc.detail, h("c"))
			})
			got, err := f.adder("tb").AddMagnet(context.Background(), magnet(t, h("c"), "x"), AddOptions{})
			if err != nil || got.Ready != tc.ready {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

func TestTorBoxBadRequestIsNotReleaseInvalid(t *testing.T) {
	f := newFakeNet(t)
	f.serve("tb.test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"success":false,"error":"BAD_REQUEST","detail":"bad option"}`)
	})
	_, err := f.adder("tb").AddMagnet(context.Background(), magnet(t, h("c"), "x"), AddOptions{})
	if code(err) == CodeInvalid {
		t.Fatalf("request failure would blocklist a valid release: %v", err)
	}
}

func TestAllDebridRejectsMismatchedHash(t *testing.T) {
	f := newFakeNet(t)
	f.serve("ad.test", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"status":"success","data":{"magnets":[{"id":1,"hash":%q,"ready":true}]}}`, h("a"))
	})
	got, err := f.adder("ad").AddMagnet(context.Background(), magnet(t, h("b"), "x"), AddOptions{})
	if got != nil || err == nil {
		t.Fatalf("accepted wrong hash: %+v, %v", got, err)
	}
}

func TestRetryAfterFormats(t *testing.T) {
	p := &poster{}
	for _, tc := range []struct {
		header string
		status int
	}{
		{"120", 429}, {time.Now().Add(120 * time.Second).UTC().Format(http.TimeFormat), 429},
		{time.Now().Add(120 * time.Second).UTC().Format(http.TimeFormat), 503},
	} {
		_, _, delay, err := p.readResponse(&http.Response{StatusCode: tc.status, Header: http.Header{"Retry-After": {tc.header}}, Body: io.NopCloser(strings.NewReader("{}"))})
		if err != nil || delay < 118*time.Second || delay > 121*time.Second {
			t.Errorf("%s: delay %v, %v", tc.header, delay, err)
		}
	}
}
