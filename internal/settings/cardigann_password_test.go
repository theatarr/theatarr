package settings

import (
	"errors"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// A Cardigann setting the definition declares as a password is refused in plain settings whatever its name, so a
// credential under an unlisted name (pass, uid) never lands in exports or the settings history. An
// unchanged indexer stays usable; an unknown definition cannot verify new plain settings.
func TestCardigannPasswordTypeSettingRefused(t *testing.T) {
	t.Parallel()
	s := &Store{}
	s.SetCardigannSecrets(func(id string) (map[string]bool, bool) {
		if id != "site" {
			return nil, false
		}
		return map[string]bool{"pass": true, "username": false}, true
	})
	ix := func(def string, settings map[string]string) *policy.Policy {
		return &policy.Policy{Indexers: map[string]*policy.Indexer{"site": {Kind: "cardigann", Definition: def, Settings: settings}}}
	}
	next := ix("site", map[string]string{"username": "me", "pass": "hunter2"})
	err := s.refuseCardigannSecretSettings(ix("site", map[string]string{"username": "me"}), next)
	var fe *policy.Errors
	if !errors.As(err, &fe) || len(fe.List) != 1 || fe.List[0].Path != "indexers.site.settings.pass" {
		t.Fatalf("password-type setting accepted: %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("the refusal echoes the value: %v", err)
	}
	if err := s.refuseCardigannSecretSettings(nil, ix("site", map[string]string{"username": "me"})); err != nil {
		t.Fatalf("plain settings refused: %v", err)
	}
	if err := s.refuseCardigannSecretSettings(next, next); err != nil {
		t.Fatalf("an unchanged indexer refused: %v", err)
	}
	if err := s.refuseCardigannSecretSettings(nil, ix("other", map[string]string{"pass": "x"})); err == nil {
		t.Fatal("unknown definition accepted unverified plain settings")
	}
}
