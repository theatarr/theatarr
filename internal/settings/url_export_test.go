package settings

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

func TestExportMasksTokenLikeURLPath(t *testing.T) {
	const token = "abc123TOKEN456secret789"
	s, _, p := setup(t)
	bootstrap(t, s, p)
	base, err := p.Clone()
	if err != nil {
		t.Fatal(err)
	}
	base.Library = &policy.Library{Plex: &policy.Plex{URL: "http://plex.invalid/" + token + "/plex", TokenEnv: "PLEX_TOKEN"}}
	snap, err := s.Load(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := snap.Export()
	if err != nil {
		t.Fatal(err)
	}
	if text := string(b); strings.Contains(text, token) || !strings.Contains(text, "plex.invalid/") ||
		!strings.Contains(text, "********") {
		t.Fatal("token-like path exposed or masked URL lost in export")
	}
}
