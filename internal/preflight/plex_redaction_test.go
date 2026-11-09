package preflight

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestPlexMasksTokenLikeURLPath(t *testing.T) {
	const token = "abc123TOKEN456secret789"
	for _, result := range []string{"not asked", "answers", "error"} {
		t.Run(result, func(t *testing.T) {
			o, _ := good(t)
			o.Policy.Library.Plex.URL += "/" + token + "/plex"
			if result == "not asked" {
				o.Plex = nil
			} else if result == "error" {
				o.Plex = func(context.Context) error {
					return &url.Error{Op: "Get", URL: o.Policy.Library.Plex.URL + "/identity", Err: errors.New("connection refused")}
				}
			}
			it := items(Run(context.Background(), o))["plex/plex"]
			if strings.Contains(it.Why, token) || !strings.Contains(it.Why, "plex.invalid:32400/") ||
				!strings.Contains(it.Why, "********") {
				t.Fatalf("unsafe Plex preflight: %+v", it)
			}
		})
	}
}
