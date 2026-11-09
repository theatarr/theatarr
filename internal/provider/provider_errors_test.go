package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// AllDebrid's MAINTENANCE and GENERIC and TorBox's server errors come in an error envelope on HTTP 200;
// they are ErrUnavailable (the provider down), not an answer about the torrent. Torrent codes keep their kinds.
func TestEnvelopeUnavailable(t *testing.T) {
	for _, c := range []struct {
		pk, key, body string
		want          error
	}{
		{"ad", keyAD, `{"status":"error","error":{"code":"MAINTENANCE","message":"under maintenance"}}`, ErrUnavailable},
		{"ad", keyAD, `{"status":"error","error":{"code":"GENERIC","message":"An error occurred"}}`, ErrUnavailable},
		{"ad", keyAD, `{"status":"error","error":{"code":"MAGNET_INVALID_ID","message":"no"}}`, ErrNotFound},
		{"tb", keyTB, `{"success":false,"error":"DATABASE_ERROR","detail":"db","data":null}`, ErrUnavailable},
		{"tb", keyTB, `{"success":false,"error":"SERVER_ERROR","detail":"x","data":null}`, ErrUnavailable},
		{"tb", keyTB, `{"success":false,"error":"ITEM_NOT_FOUND","detail":"x","data":null}`, ErrNotFound},
	} {
		t.Run(fmt.Sprintf("%s/%s", c.pk, c.body[:30]), func(t *testing.T) {
			f := newFakeNet(t)
			f.serve(c.pk+".test", c.key, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(c.body))
			})
			_, err := f.client(c.pk, c.key).Torrent(context.Background(), "101")
			var ae *APIError
			if !errors.Is(err, c.want) || !errors.As(err, &ae) || ae.Status != 200 {
				t.Fatalf("err %v, want %v on HTTP 200", err, c.want)
			}
			if c.want == ErrUnavailable && (errors.Is(err, ErrNotFound) || errors.Is(err, ErrAuth)) {
				t.Errorf("err %v also matches a torrent or auth kind", err)
			}
		})
	}
}

// AllDebrid's file tree is read only for a ready magnet. A queued, downloading or failed magnet is its
// status alone, so a failing files call cannot fail its read and a failed magnet shows as StateError.
func TestAllDebridFilesOnlyWhenReady(t *testing.T) {
	for _, c := range []struct {
		code      int
		state     State
		wantFiles bool
	}{{0, StateQueued, false}, {1, StateDownloading, false}, {7, StateError, false}, {4, StateDownloaded, true}} {
		t.Run(fmt.Sprint(c.code), func(t *testing.T) {
			f := newFakeNet(t)
			filesCalls := 0
			f.serve("ad.test", keyAD, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v4.1/magnet/status":
					fmt.Fprintf(w, `{"status":"success","data":{"magnets":{"id":101,"filename":"M","size":10,"hash":"%s",`+
						`"status":"x","statusCode":%d,"downloaded":5}}}`, h("b"), c.code)
				case "/v4.1/magnet/files":
					filesCalls++
					if c.code != 4 {
						_, _ = w.Write([]byte(`{"status":"error","error":{"code":"MAGNET_NOT_READY"}}`))
						return
					}
					_, _ = w.Write([]byte(`{"status":"success","data":{"magnets":[{"id":"101","files":[{"n":"M.mkv","s":10}]}]}}`))
				default:
					w.WriteHeader(404)
				}
			})
			got, err := f.client("ad", keyAD).Torrent(context.Background(), "101")
			if err != nil {
				t.Fatal(err)
			}
			if got.State != c.state || (len(got.Files) > 0) != c.wantFiles || (filesCalls > 0) != c.wantFiles {
				t.Errorf("state %s files %v files calls %d", got.State, got.Files, filesCalls)
			}
		})
	}
}

// TorBox answers the single read of a gone id with HTTP 500. The whole list decides: absent from it is
// not found, listed is the torrent; a failed list keeps the 5xx (TorBox may be down).
func TestTorBoxGoneIdOn5xx(t *testing.T) {
	for _, c := range []struct {
		name, list string
		listFails  bool
		notFound   bool
		found      bool
	}{
		{name: "gone", list: `[]`, notFound: true},
		{name: "listed", list: `[{"id":101,"hash":"` + h("b") + `","name":"M","size":10,"download_state":"downloading","progress":0.5}]`, found: true},
		{name: "list fails", listFails: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeNet(t)
			f.serve("tb.test", keyTB, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Query().Get("id") != "" || c.listFails {
					w.WriteHeader(500)
					_, _ = w.Write([]byte(`{"success":false,"error":"DATABASE_ERROR","detail":"db","data":null}`))
					return
				}
				fmt.Fprintf(w, `{"success":true,"error":null,"detail":"","data":%s}`, c.list)
			})
			got, err := f.client("tb", keyTB).Torrent(context.Background(), "101")
			switch {
			case c.notFound:
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("err %v, want ErrNotFound", err)
				}
			case c.found:
				if err != nil || got.ID != "101" || got.State != StateDownloading {
					t.Fatalf("got %+v %v", got, err)
				}
			default:
				var ae *APIError
				if errors.Is(err, ErrNotFound) || !errors.As(err, &ae) || ae.Status != 500 {
					t.Fatalf("err %v, want the 5xx", err)
				}
			}
		})
	}
}
