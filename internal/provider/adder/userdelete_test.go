package adder

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// DeleteUserChosen sends one delete per provider, for the id of a choice ChooseForDelete made; a zero or incomplete
// choice sends nothing.
func TestDeleteUserChosen(t *testing.T) {
	ctx := context.Background()
	f := newFakeNet(t)
	f.serve("rd.test", func(w http.ResponseWriter, r *http.Request) { reply(t, w, http.StatusNoContent, "") })
	f.serve("ad.test", func(w http.ResponseWriter, r *http.Request) { reply(t, w, 200, "ad/delete_ok.json") })
	f.serve("tb.test", func(w http.ResponseWriter, r *http.Request) { reply(t, w, 200, "tb/control_ok.json") })
	for _, bad := range [][4]string{{"", h("a"), "b1", "c1"}, {"1", "nothex", "b1", "c1"}, {"1", h("a"), "", "c1"},
		{"1", h("a"), "b1", ""}} {
		if _, err := ChooseForDelete(bad[0], bad[1], bad[2], bad[3]); !errors.Is(err, ErrNotChosen) {
			t.Errorf("%v: %v", bad, err)
		}
	}
	for _, k := range []string{"rd", "ad", "tb"} {
		d, ok := f.adder(k).(UserDeleter)
		if !ok {
			t.Fatalf("%s: no user-chosen delete", k)
		}
		if err := d.DeleteUserChosen(ctx, UserChoice{}); !errors.Is(err, ErrNotChosen) {
			t.Errorf("%s zero choice: %v", k, err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("%d requests for refused deletes", n)
	}
	for k, id := range map[string]string{"rd": "RDX", "ad": "12345", "tb": "678"} {
		c, err := ChooseForDelete(id, strings.ToUpper(h("a")), "20261029T120000.000Z-pre-delete", "v1:1:10:abcd")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.adder(k).(UserDeleter).DeleteUserChosen(ctx, c); err != nil {
			t.Errorf("%s: %v", k, err)
		}
	}
	got := map[string]request{}
	for _, r := range f.requests() {
		got[r.host] = r
	}
	if r := got["rd.test"]; r.method != http.MethodDelete || r.path != "/rest/1.0/torrents/delete/RDX" {
		t.Errorf("rd %+v", r)
	}
	if r := got["ad.test"]; r.method != http.MethodPost || r.path != "/v4/magnet/delete" || r.form.Get("id") != "12345" {
		t.Errorf("ad %+v", r)
	}
	if r := got["tb.test"]; r.method != http.MethodPost || r.path != "/v1/api/torrents/controltorrent" {
		t.Errorf("tb %+v", r)
	}
	// TorBox gets JSON with the id as a number (the fake network parses bodies as forms, so the JSON is one key).
	if r := got["tb.test"]; len(r.form) != 1 || !r.form.Has(`{"torrent_id":678,"operation":"delete"}`) {
		t.Errorf("tb body %v", r.form)
	}
	cNum, _ := ChooseForDelete("abc", h("a"), "b", "c")
	if err := f.adder("tb").(UserDeleter).DeleteUserChosen(ctx, cNum); code(err) != CodeInvalid {
		t.Errorf("non-number TorBox id: %v", err)
	}
}

// A TorBox refusal and an AllDebrid error answer are errors, not deletes.
func TestDeleteUserChosenRefused(t *testing.T) {
	ctx := context.Background()
	f := newFakeNet(t)
	f.serve("ad.test", func(w http.ResponseWriter, r *http.Request) { reply(t, w, 200, "ad/delete_invalid_id.json") })
	f.serve("tb.test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"success": false, "error": "BAD_TOKEN", "detail": "no"}`))
	})
	c, _ := ChooseForDelete("1", h("a"), "b", "c")
	if err := f.adder("ad").(UserDeleter).DeleteUserChosen(ctx, c); err == nil {
		t.Error("AllDebrid error taken as deleted")
	}
	if err := f.adder("tb").(UserDeleter).DeleteUserChosen(ctx, c); code(err) != CodeAuth {
		t.Errorf("TorBox 403: %v", err)
	}
}

// Restore makes a magnet from a v1 hash (any case) and refuses anything else; the zero Magnet is still refused.
func TestRestore(t *testing.T) {
	for name, tc := range map[string]struct {
		hash string
		err  error
	}{
		"bad hash": {"abc", ErrNotRestorable},
		"empty":    {"", ErrNotRestorable},
		"ok":       {strings.ToUpper(h("a")), nil},
	} {
		m, err := Restore(tc.hash, nil)
		if !errors.Is(err, tc.err) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if err == nil && (m.Hash() != h("a") || !m.valid() || m.Indexer() != "") {
			t.Errorf("%s: %+v", name, m)
		}
	}
	if _, err := Restore(h("a"), []string{"http://t.example.invalid/announce?passkey=0123456789abcdef0123"}); err == nil {
		t.Error("a keyed tracker is refused")
	}
	if (Magnet{}).valid() {
		t.Error("the zero Magnet is valid")
	}
	// The restore magnet is added like any other.
	f := newFakeNet(t)
	f.serve("rd.test", func(w http.ResponseWriter, r *http.Request) { reply(t, w, http.StatusCreated, "rd/add_magnet.json") })
	m, _ := Restore(h("a"), nil)
	if a, err := f.adder("rd").AddMagnet(context.Background(), m, AddOptions{}); err != nil || a.ID == "" {
		t.Errorf("add: %+v %v", a, err)
	}
}

// A copy of ours on TorBox (router copy.go) is deleted with the same request as a person's choice, with the full proof
// only; BareMagnet makes the bare magnet of a library hash and applies Real-Debrid's name rule loosely.
func TestCopyDeleteAndBareMagnet(t *testing.T) {
	ctx := context.Background()
	f := newFakeNet(t)
	f.serve("tb.test", func(w http.ResponseWriter, r *http.Request) { reply(t, w, 200, "tb/control_ok.json") })
	sd := f.adder("tb").(StalledDeleter)
	if err := sd.DeleteOwnStalledAdd(ctx, "678", OwnAdd{NewEntry: true}); !errors.Is(err, ErrNotOwnAdd) {
		t.Errorf("no proof: %v", err)
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("%d requests for a refused delete", n)
	}
	if err := sd.DeleteOwnStalledAdd(ctx, "678", OwnAdd{NotOnAccountBefore: true, NewEntry: true}); err != nil {
		t.Fatal(err)
	}
	if r := f.requests(); len(r) != 1 || r[0].path != "/v1/api/torrents/controltorrent" ||
		!r[0].form.Has(`{"torrent_id":678,"operation":"delete"}`) {
		t.Errorf("tb %+v", r)
	}
	if _, err := BareMagnet("nothex", "x", nil); !errors.Is(err, ErrNoHash) {
		t.Errorf("bad hash: %v", err)
	}
	m, err := BareMagnet(strings.ToUpper(h("a")), "Movie 2024 2160p", []string{"udp://tracker.example:1337/announce"})
	if err != nil || !m.valid() || m.Hash() != h("a") || m.URI() != "magnet:?xt=urn:btih:"+h("a")+
		"&tr=udp%3A%2F%2Ftracker.example%3A1337%2Fannounce" {
		t.Errorf("magnet %v %v %q", m, err, m.URI())
	}
}
