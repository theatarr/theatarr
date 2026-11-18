package journal

import (
	"context"
	"errors"
	"fmt"
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
		en.Release == nil || en.Release.InfoHash != hashC || en.Label != "Unblocked" ||
		!strings.HasPrefix(en.Summary, "Unblocked Some.Release · by mgr · was blocked by library: sample") ||
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

// The typed title filter finds, for a number, the title with that id and every title whose name holds it ("2001" finds
// "2001: A Space Odyssey"; the owner, 2026-11-18, overruling the whole-name rule of UI sweep 5's J1); a title's own
// History and journal (TitleID) keep that title's entries only, even when another title is named like its id.
func TestTitleFilterTypedNumberAndExactID(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := msOf(testNow)
	e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES (4, 'movie', '1', 2022, 104, ?, ?),
		(5, 'movie', 'Hollywood in 1', 2023, 105, ?, ?), (6, 'movie', '1917', 2019, 106, ?, ?),
		(7, 'movie', '2001: A Space Odyssey', 1968, 107, ?, ?), (2001, 'movie', 'Zeta', 2020, 108, ?, ?)`, now, now, now, now, now, now, now, now, now, now)
	for _, a := range []Action{{Action: ActUnblock, TitleID: 1, Version: "movie-web", Target: hashA, By: "mgr"},
		{Action: ActUnblock, TitleID: 4, Version: "movie-web", Target: hashB, By: "mgr"},
		{Action: ActUnblock, TitleID: 5, Version: "movie-web", Target: hashC, By: "mgr"},
		{Action: ActUnblock, TitleID: 6, Version: "movie-web", Target: "dddddddddddddddddddddddddddddddddddddddd", By: "mgr"},
		{Action: ActUnblock, TitleID: 7, Version: "movie-web", Target: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", By: "mgr"},
		{Action: ActUnblock, TitleID: 2001, Version: "movie-web", Target: "ffffffffffffffffffffffffffffffffffffffff", By: "mgr"}} {
		if _, err := e.j.Record(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	titles := func(items []*Entry) map[int64]bool {
		out := map[int64]bool{}
		for _, x := range items {
			out[x.Title.ID] = true
		}
		return out
	}
	p, err := e.j.List(ctx, Filter{Title: "1"})
	if err != nil {
		t.Fatal(err)
	}
	// A typed number is the id and the name search: every title whose name holds it.
	if got := titles(p.Items); len(got) != 5 || !got[1] || !got[4] || !got[5] || !got[6] || !got[7] {
		t.Errorf("typed 1: titles %v, want the title with id 1 and those named with a 1", got)
	}
	p, err = e.j.List(ctx, Filter{Title: "2001"})
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(p.Items); len(got) != 2 || !got[7] || !got[2001] {
		t.Errorf("typed 2001: titles %v, want 2001: A Space Odyssey and the title with id 2001", got)
	}
	// The CLI passes -title as typed: spaces around it count for nothing, as in the web box.
	p, err = e.j.List(ctx, Filter{Title: " 1 "})
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(p.Items); len(got) != 5 || !got[1] || !got[4] {
		t.Errorf("typed \" 1 \": titles %v", got)
	}
	p, err = e.j.List(ctx, Filter{TitleID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(p.Items); !got[1] || got[4] {
		t.Errorf("title id 1: titles %v", got)
	}
	for _, id := range []int64{1, 4} {
		e.exec(`INSERT INTO history (at, ref, event, release, info_hash, quality, score, provider, reason, kind, title_id)
			VALUES (?, ?, 'copy_added', 'X.2020.2160p.WEB-DL-FLUX', ?, 'WEBDL-2160p', 5000, 'tb', 'Added to TB', 'copy', ?)`,
			now+id, fmt.Sprintf("copy:%d", id), hashC, id)
	}
	h, err := e.j.History(ctx, Filter{TitleID: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range h.Items {
		if x.Title.ID != 1 {
			t.Errorf("title 1's History holds a row of title %d", x.Title.ID)
		}
	}
	if len(h.Items) == 0 {
		t.Error("title 1's History is empty")
	}
}
