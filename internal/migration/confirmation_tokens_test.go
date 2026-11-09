package migration

import (
	"reflect"
	"testing"
)

func TestApplyExplicitConfirmationTokens(t *testing.T) {
	t.Parallel()
	fs := newFakeStack(t)
	se := newSettingsEnv(t)
	se.mkdirs(t)
	p, _ := se.plan(t, fs)
	set := &confirming{Settings: se.coord}
	calls := 0
	const sid = "provider/tb-migrated"
	o := applyOpts()
	o.Steps = []string{sid}
	got, err := Apply(t.Context(), se.st, p.ID, set, se.checkpointer(&calls), o)
	if err != nil || got.Step(sid).Confirmation == nil {
		t.Fatalf("confirmation: %v", err)
	}
	for _, tc := range []struct {
		name    string
		tokens  map[string]string
		steps   []string
		confirm []string
	}{
		{"wrong", map[string]string{sid: "wrong"}, []string{sid}, nil},
		{"empty", map[string]string{sid: ""}, []string{sid}, nil},
		{"unknown", map[string]string{"unknown": "tok-1"}, []string{sid}, nil},
		{"unselected", map[string]string{sid: "tok-1"}, []string{"version/movie-uhd"}, nil},
		{"mixed flows", map[string]string{sid: "tok-1"}, []string{sid}, []string{sid}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := o
			bad.Steps, bad.ConfirmationTokens, bad.Confirm = tc.steps, tc.tokens, tc.confirm
			if _, err := Apply(t.Context(), se.st, p.ID, set, se.checkpointer(&calls), bad); err == nil {
				t.Fatal("invalid proof accepted")
			}
			if calls != 1 || len(set.tokens) != 1 {
				t.Fatal("invalid proof reached checkpoint or settings")
			}
		})
	}
	o.ConfirmationTokens = map[string]string{sid: "tok-1"}
	got, err = Apply(t.Context(), se.st, p.ID, set, se.checkpointer(&calls), o)
	if err != nil || got.Step(sid).Status != StatusDone || !reflect.DeepEqual(set.tokens, []string{"", "tok-1"}) {
		t.Fatalf("confirmed: %v, tokens %v", err, set.tokens)
	}
	if _, err := Apply(t.Context(), se.st, p.ID, set, se.checkpointer(&calls), o); err == nil {
		t.Fatal("used token accepted again")
	}
}
