package acquire

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/importarr"
)

type settingsFollowRecorder struct {
	targets []importarr.FollowSearchTarget
}

func (s *settingsFollowRecorder) EnqueueFollowSearch(_ context.Context, targets []importarr.FollowSearchTarget) error {
	s.targets = append(s.targets, targets...)
	return nil
}

func TestPreparingAcquirerKeepsFollowDispatcher(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false, nil)
	dispatcher := &settingsFollowRecorder{}
	if _, err := New(Config{Store: e.st, Policy: e.pol, Queue: e.q, Router: e.rt.Router, Search: e.a.search, FollowSearcher: dispatcher}); err != nil {
		t.Fatal(err)
	}
	snapshot := &importarr.Snapshot{Instance: "settings-fixture", Flavour: "radarr", Titles: []importarr.Title{{ArrID: 91, Kind: "movie", Name: "Settings fixture", Year: 2026, TmdbID: 987654, Monitored: true}}}
	_, err := (&importarr.Importer{Store: e.st}).Write(e.ctx, importarr.Source{Instance: "settings-fixture", Flavour: "radarr", Version: "arr-fixture", Followers: []string{"fixture-theatarr"}}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.targets) != 1 || dispatcher.targets[0].VersionKey != "fixture-theatarr" {
		t.Fatalf("import used unpublished acquirer: %+v", dispatcher.targets)
	}
}
