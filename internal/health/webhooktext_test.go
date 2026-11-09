package health

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/store"
)

// Message is what the Health webhook sends, so it names neither indexer keys nor the provider error text (which
// can carry torrent ids); both stay in Detail for the API.
func TestHealthWebhookTextNamesNoKeysOrTorrents(t *testing.T) {
	ctx := context.Background()
	s := gateway.State{Backoffs: []gateway.BackoffState{{Subject: "indexer:secret-tracker", Active: true, Reason: "5 failures"}}}
	r := Indexers(func() gateway.State { return s }, []string{"secret-tracker", "other"})(ctx)
	if r.Type != Warning || strings.Contains(r.Message, "secret-tracker") || !strings.Contains(r.Message, "1 of 2") {
		t.Errorf("indexers message: %+v", r)
	}
	if down, _ := r.Detail.(map[string]any)["unavailable"].(map[string]string); down["secret-tracker"] == "" {
		t.Errorf("indexers detail: %+v", r.Detail)
	}
	st := openStore(t)
	exec(t, st, `INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'M', 0, 0)`)
	exec(t, st, `INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'm', 0, 0)`)
	exec(t, st, `INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		provider, error, created_at, updated_at) VALUES (1, 1, 'm', 'a', 'R', 'pub', 'adding', 'rd', ?, 0, ?)`,
		"add not settled: torrent ABCDEF123456 not in rd's list", store.Millis(t0.Add(-time.Hour)))
	r = UnsettledAdds(st, 0, func() time.Time { return t0 })(ctx)
	if r.Type != Warning || strings.Contains(r.Message, "ABCDEF123456") {
		t.Errorf("unsettled message: %+v", r)
	}
	if why, _ := r.Detail.(map[string]any)["error"].(string); !strings.Contains(why, "ABCDEF123456") {
		t.Errorf("unsettled detail: %+v", r.Detail)
	}
}
