package settings

import (
	"context"
	"encoding/json"
	"github.com/theatarr/theatarr/internal/policy"
	"testing"
)

func TestFreshSettingsFirstSave(t *testing.T) {
	st, _, _ := setup(t)
	ctx := context.Background()
	base := &policy.Policy{}
	initial, err := st.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Generation != 0 || initial.Records.Switches == nil || *initial.Records.Switches != (GlobalSwitches{}) {
		t.Fatalf("fresh snapshot: %+v", initial)
	}
	manager := versionCoordinator(t, st, base, initial)
	records := initial.Records
	records.Sections = []Section{{Record: Record{Key: "provider_sync"}, Config: json.RawMessage(`{"interval":"8h"}`)}}
	saved, err := manager.SaveAndApply(ctx, SaveRequest{Base: base, Actor: "setup", Records: records})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Generation != 1 || saved.Records.Switches == nil || *saved.Records.Switches != (GlobalSwitches{}) {
		t.Fatalf("first save: %+v", saved)
	}
}
