package notify_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
)

// An installed arr webhook target's ManualInteractionRequired for a held import carries the
// release name and indexer, as the arrs' payload does.
func TestHeldImportWebhookHasReleaseAndIndexer(t *testing.T) {
	e := newParityEnv(t)
	id := e.install("sonarr", map[string]bool{"onManualInteractionRequired": true})
	e.download(1, 2, "private", false)
	e.exec(`INSERT INTO library_import (download_id, state, last_error, created_at, updated_at)
		VALUES (1, 'rejected', 'held for review', ?, ?)`, store.Millis(e.now), store.Millis(e.now))
	e.scan()
	bodies := e.bodies(fmt.Sprintf("notification:%d", id), "ManualInteractionRequired")
	if len(bodies) != 1 {
		t.Fatal(bodies)
	}
	b, err := json.Marshal(bodies[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "SECRET-RELEASE") || !strings.Contains(string(b), `"indexer":"private"`) {
		t.Fatalf("missing release or indexer: %s", b)
	}
}
