package adder

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A provider's error text can echo the key it was sent; the key never reaches the adder's errors.
func TestAddErrorsRedactTheKey(t *testing.T) {
	ctx := context.Background()
	for _, echo := range []string{key, strings.ToLower(key), base64.StdEncoding.EncodeToString([]byte("u:" + key))} {
		for _, c := range []struct {
			prov   string
			status int
			body   any
		}{
			{"tb", 401, map[string]any{"success": false, "error": "BAD_TOKEN", "detail": "invalid token " + echo}},
			{"tb", 200, map[string]any{"success": false, "error": "BAD_TOKEN " + echo,
				"detail": strings.Repeat("y", 110) + echo}},
			{"ad", 200, map[string]any{"status": "error", "error": map[string]any{"code": "AUTH_BAD_APIKEY",
				"message": echo}}},
			{"ad", 200, map[string]any{"status": "error", "error": map[string]any{"code": "MAGNET_INVALID_URI",
				"message": "bad " + echo}}},
			{"rd", 401, map[string]any{"error": echo, "error_code": 8}},
		} {
			f := newFakeNet(t)
			f.serve(c.prov+".test", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_ = json.NewEncoder(w).Encode(c.body)
			})
			_, err := f.adder(c.prov).AddMagnet(ctx, magnet(t, h("d"), "x"), AddOptions{})
			if err == nil {
				t.Fatalf("%s: want an error", c.prov)
			}
			msg := fmt.Sprintf("%v %+v", err, err)
			for _, part := range []string{echo, echo[:12], echo[len(echo)-12:]} {
				if strings.Contains(msg, part) {
					t.Errorf("%s: error %q holds %q", c.prov, msg, part)
				}
			}
		}
	}
}
