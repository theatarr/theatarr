package journal

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestActionEntries: an operator action is its own entry (act:<id>, kind action, source web) with who did it, found
// by kind and by title, and free text is cleaned before it is stored.
func TestActionEntries(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, err := e.j.Record(ctx, Action{Action: ActUnblock, TitleID: 1, Version: "movie-web", InfoHash: strings.ToUpper(hashC),
		Release: "Some.Release", Target: hashC, Detail: "was blocked by library: sample https://x.example/?apikey=secret",
		By: "mgr"})
	if err != nil || id == 0 {
		t.Fatalf("record: %d %v", id, err)
	}
	if _, err := e.j.Record(ctx, Action{}); err == nil {
		t.Error("an action without a name was stored")
	}
	p, err := e.j.List(ctx, Filter{Kinds: []string{KindAction}})
	if err != nil || len(p.Items) != 1 {
		t.Fatalf("list: %+v %v", p, err)
	}
	en := p.Items[0]
	if en.ID != "act:1" || en.Kind != KindAction || en.Source != "web" || en.Action == nil || en.Action.By != "mgr" ||
		en.Release == nil || en.Release.InfoHash != hashC || !strings.HasPrefix(en.Summary, "release unblocked by mgr: ") ||
		strings.Contains(en.Summary, "secret") || !en.At.Equal(testNow) {
		t.Errorf("entry %+v", en)
	}
	got, err := e.j.Get(ctx, "act:1")
	if err != nil || got.Action.Action != ActUnblock {
		t.Errorf("get: %+v %v", got, err)
	}
	if _, err := e.j.Get(ctx, "act:2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	p, err = e.j.List(ctx, Filter{Title: "1", Version: "movie-web"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range p.Items {
		found = found || x.ID == "act:1"
	}
	if !found {
		t.Error("the title's journal lacks the action")
	}
}
