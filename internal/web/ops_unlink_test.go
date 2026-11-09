package web

import (
	"strings"
	"syscall"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
)

// TestRemoveUnlinkFailureIsRetryable: a removal with unlink hid the copy and ended its downloads,
// then a link's move to the trash failed (a full disk): the answer was a 500, a retry found nothing to remove (409)
// and the link stayed active, while the journal already said "files unlinked". Now the failure is reported with the
// rest (200, failed), the journal claims no unlink before it happens, and removing again with unlink moves the
// links the removed copy still has.
func TestRemoveUnlinkFailureIsRetryable(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	now := store.Millis(testNow)
	o.exec(`INSERT INTO link (id, path, target, kind, title_id, state, created_at, updated_at) VALUES
		(5, '/lib/Beta/a.mkv', '/mnt/zurg/a.mkv', 'symlink', 2, 'active', ?, ?)`, now, now)
	o.exec(`INSERT INTO link_version (link_id, version_id) VALUES (5, 3)`)
	o.ops.unlinkErr = syscall.ENOSPC
	var got RemoveResult
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{Unlink: true}, o.mc, o.mcsrf,
		&got); code != 200 || !got.Title || len(got.Failed) != 1 || len(got.Unlinked) != 0 ||
		!strings.Contains(got.Message, "retry") {
		t.Fatalf("remove: %d %+v", code, got)
	}
	for _, a := range o.actions() {
		if a.Action == "unlink_file" || strings.Contains(a.Detail, "unlinked") {
			t.Errorf("journal claims an unlink that did not happen: %+v", a)
		}
	}
	var scope RemovalScope
	if code := o.jsonCall("GET", APIBase+"/titles/2/removal", nil, o.mc, "", &scope); code != 200 ||
		len(scope.Copies) != 0 || len(scope.Leftover) != 1 || scope.Leftover[0].Links != 1 {
		t.Fatalf("scope after the failure: %d %+v", code, scope)
	}
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{}, o.mc, o.mcsrf, nil); code != 409 {
		t.Errorf("remove again without unlink: %d", code)
	}
	got = RemoveResult{}
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{Unlink: true}, o.mc, o.mcsrf,
		&got); code != 200 || len(got.Removed) != 0 || len(got.Unlinked) != 1 || len(got.Failed) != 0 {
		t.Fatalf("retry: %d %+v", code, got)
	}
	if len(o.ops.unlinks) != 1 || o.ops.unlinks[0] != 5 {
		t.Errorf("unlinked %v", o.ops.unlinks)
	}
	got = RemoveResult{}
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{Versions: []string{"movie-web"},
		Unlink: true}, o.mc, o.mcsrf, &got); code != 200 {
		t.Errorf("retry naming the removed copy: %d %+v", code, got)
	}
	acts := o.actions()
	if n := len(acts); n == 0 || acts[n-1].Action != "unlink_file" {
		t.Errorf("actions %+v", acts)
	}
}
