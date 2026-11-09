package provider

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestAllDebridNoServerIsAccountRefusal(t *testing.T) {
	f := newFakeNet(t)
	f.serve("ad.test", keyAD, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","error":{"code":"NO_SERVER","message":"server address refused"}}`))
	})
	_, err := f.client("ad", keyAD).Account(context.Background())
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("NO_SERVER should back off the account: %v", err)
	}
}
