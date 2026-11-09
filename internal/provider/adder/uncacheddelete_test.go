package adder

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/theatarr/theatarr/internal/gateway"
)

type uncachedDeleteDoer func(context.Context, gateway.Target, *http.Request) (*http.Response, error)

func (f uncachedDeleteDoer) Do(ctx context.Context, target gateway.Target, req *http.Request) (*http.Response, error) {
	return f(ctx, target, req)
}

// The own-add contract also runs against an in-memory gateway, without opening a provider HTTP listener.
func TestAllDebridOwnUncachedDelete(t *testing.T) {
	ctx := context.Background()
	deletes := 0
	deleteAnswer := "ad/delete_ok.json"
	doer := uncachedDeleteDoer(func(_ context.Context, target gateway.Target, req *http.Request) (*http.Response, error) {
		answer := "ad/upload_not_ready.json"
		switch req.URL.Path {
		case "/v4.1/magnet/upload":
		case "/v4/magnet/delete":
			deletes++
			answer = deleteAnswer
			if err := req.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if req.Method != http.MethodPost || req.PostForm.Get("id") != "412345679" || len(req.PostForm) != 1 || req.URL.RawQuery != "" {
				t.Fatalf("delete request: %s %s %+v", req.Method, req.URL, req.PostForm)
			}
			if target.Class != gateway.ClassProvider || target.Key != "ad" {
				t.Fatalf("delete lost provider/privacy target: %+v", target)
			}
		default:
			t.Fatalf("unexpected request: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(fixture(t, answer)))}, nil
	})
	ad, err := New("ad", testPolicy().Providers["ad"], key, doer)
	if err != nil {
		t.Fatal(err)
	}
	del := ad.(Deleter)
	a, err := ad.AddMagnet(ctx, magnet(t, h("b"), "x"), AddOptions{})
	if err != nil {
		t.Fatal(err)
	}
	proof := OwnAdd{NotOnAccountBefore: true, NewEntry: true}
	for _, tc := range []struct {
		a     *Added
		proof OwnAdd
	}{
		{a, OwnAdd{}}, {a, OwnAdd{NotOnAccountBefore: true}}, {a, OwnAdd{NewEntry: true}},
		{&Added{ID: a.ID, Hash: a.Hash}, proof}, {nil, proof},
	} {
		if err := del.DeleteOwnUncachedAdd(ctx, tc.a, tc.proof); !errors.Is(err, ErrNotOwnAdd) {
			t.Fatalf("incomplete own-add proof accepted: %v", err)
		}
	}
	if deletes != 0 {
		t.Fatal("unproven delete sent")
	}
	if err := del.DeleteOwnUncachedAdd(ctx, a, proof); err != nil {
		t.Fatal(err)
	}
	if err := del.DeleteOwnUncachedAdd(ctx, a, proof); !errors.Is(err, ErrNotOwnAdd) || deletes != 1 {
		t.Fatalf("second delete: %v, requests %d", err, deletes)
	}
	a, err = ad.AddMagnet(ctx, magnet(t, h("b"), "x"), AddOptions{})
	if err != nil {
		t.Fatal(err)
	}
	deleteAnswer = "ad/delete_invalid_id.json"
	if err := del.DeleteOwnUncachedAdd(ctx, a, proof); err == nil || deletes != 2 {
		t.Fatalf("failed delete accepted: %v, requests %d", err, deletes)
	}
}
