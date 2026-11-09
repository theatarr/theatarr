package cardigann

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeRepo is the definitions repository as Fetch sees it.
type fakeRepo struct {
	commit   string
	files    map[string][]byte // paths below the archive root
	commits  int
	archives int
	fail     error
}

func (f *fakeRepo) Commit(context.Context) (string, error) {
	f.commits++
	if f.fail != nil {
		return "", f.fail
	}
	return f.commit, nil
}

func (f *fakeRepo) Archive(_ context.Context, commit string) ([]byte, error) {
	f.archives++
	if commit != f.commit {
		return nil, errors.New("unknown commit")
	}
	return tarGzFiles(f.files), nil
}

var tarGzFiles func(map[string][]byte) []byte

func newTestStore(t *testing.T, repo *fakeRepo, inUse ...string) *Store {
	t.Helper()
	tarGzFiles = func(files map[string][]byte) []byte { return tarGz(t, files) }
	return &Store{Dir: t.TempDir(), Fetch: repo, InUse: func() []string { return inUse }}
}

func v11(defs map[string][]byte) map[string][]byte {
	out := map[string][]byte{}
	for id, data := range defs {
		out["definitions/v11/"+id+".yml"] = data
	}
	return out
}

func TestStoreRefreshAppliesAndKeepsTheLastGoodSnapshot(t *testing.T) {
	repo := &fakeRepo{commit: "c1", files: v11(map[string][]byte{"fixturepublic": fixtureDef(t, "fixturepublic"), "fixtureprivate": fixtureDef(t, "fixtureprivate")})}
	repo.files["definitions/v10/old.yml"] = []byte("id: old\n")
	s := newTestStore(t, repo)
	if _, _, err := s.Load(context.Background(), "fixturepublic"); !errors.Is(err, ErrNoDefinition) || !strings.Contains(err.Error(), "not been fetched") {
		t.Fatalf("before the first fetch: %v", err)
	}
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	cat, err := s.Catalog()
	if err != nil || len(cat) != 2 {
		t.Fatalf("catalog = %d, %v", len(cat), err)
	}
	if st := s.Status(); st.Commit != "c1" || st.Schema != 11 || st.Definitions != 2 || st.LastError != "" {
		t.Fatalf("status %+v", st)
	}
	def, hash, err := s.Load(context.Background(), "fixtureprivate")
	if err != nil || def.ID != "fixtureprivate" || hash == "" {
		t.Fatalf("load: %v", err)
	}
	// an unchanged commit costs only the commit request
	if err := s.Refresh(context.Background()); err != nil || repo.archives != 1 || repo.commits != 2 {
		t.Fatalf("unchanged commit: archives=%d commits=%d err=%v", repo.archives, repo.commits, err)
	}
	// a failure keeps the catalog, and says so
	repo.fail = errors.New("gateway: refused (vpn down)")
	if err := s.Refresh(context.Background()); err == nil {
		t.Fatal("expected the failure")
	}
	if st := s.Status(); st.Definitions != 2 || !strings.Contains(st.LastError, "vpn down") {
		t.Fatalf("after a failure: %+v", st)
	}
	// a new process reads the snapshot from disk
	again := &Store{Dir: s.Dir}
	if _, _, err := again.Load(context.Background(), "fixturepublic"); err != nil {
		t.Fatalf("snapshot not kept: %v", err)
	}
	if again.Status().Commit != "c1" {
		t.Fatalf("state not kept: %+v", again.Status())
	}
}

func TestStoreRefusesAnArchiveWithoutASupportedSchema(t *testing.T) {
	repo := &fakeRepo{commit: "c1", files: map[string][]byte{"definitions/v12/a.yml": []byte("id: a\n")}}
	s := newTestStore(t, repo)
	if err := s.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "v11 or older") {
		t.Fatalf("err = %v", err)
	}
	if st := s.Status(); st.Definitions != 0 || st.Commit != "" {
		t.Fatalf("status %+v", st)
	}
}

// Updates of definitions that no indexer uses apply by themselves; updates of in-use ones that change privacy, ask
// for new settings, remove the definition or stop it running wait for review, and the indexer keeps the old text.
func TestStoreHoldsRiskyUpdatesOfInUseDefinitions(t *testing.T) {
	pub := string(fixtureDef(t, "fixturepublic"))
	repo := &fakeRepo{commit: "c1", files: v11(map[string][]byte{
		"fixturepublic":  []byte(pub),
		"fixtureprivate": fixtureDef(t, "fixtureprivate"),
		"fixturejson":    fixtureDef(t, "fixturejson"),
		"fixturecookie":  fixtureDef(t, "fixturecookie"),
		"unused":         []byte(strings.Replace(pub, "id: fixturepublic", "id: unused", 1)),
	})}
	s := newTestStore(t, repo, "fixturepublic", "fixtureprivate", "fixturejson", "fixturecookie")
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pub, "type: public") {
		t.Fatal("fixture changed: fixturepublic should be public")
	}
	repo.commit = "c2"
	repo.files = v11(map[string][]byte{
		// a privacy type change is a label change: it applies by itself
		"fixturepublic": []byte(strings.Replace(pub, "type: public", "type: semi-private", 1)),
		// a harmless change applies
		"fixtureprivate": []byte(string(fixtureDef(t, "fixtureprivate")) + "\n# a comment\n"),
		// newly unsupported (a CAPTCHA login)
		"fixturecookie": []byte(string(fixtureDef(t, "fixturecookie")) + "\n"),
		// fixturejson removed
		"unused": []byte(strings.Replace(strings.Replace(pub, "id: fixturepublic", "id: unused", 1), "type: public", "type: private", 1)),
	})
	cookie := string(fixtureDef(t, "fixturecookie"))
	if !strings.Contains(cookie, "login:") {
		t.Fatal("fixture changed: fixturecookie should log in")
	}
	repo.files["definitions/v11/fixturecookie.yml"] = []byte(strings.Replace(cookie, "login:", "login:\n  captcha:\n    type: image\n    selector: img#captcha\n    input: captcha", 1))
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	holds := s.Holds()
	got := map[string][]string{}
	for _, h := range holds {
		got[h.ID] = h.Reasons
	}
	want := map[string][]string{
		"fixturejson":   {HoldRemovedInUse},
		"fixturecookie": {HoldNewlyUnsupported},
	}
	for id, reasons := range want {
		if !slices.Equal(got[id], reasons) {
			t.Errorf("%s: holds %v, want %v (all: %+v)", id, got[id], reasons, holds)
		}
	}
	if len(holds) != 2 {
		t.Errorf("holds = %+v", holds)
	}
	// the held ones still run their old text, the rest moved on
	if sum, _ := s.Summary("fixturepublic"); sum.Privacy != "semi-private" {
		t.Errorf("a privacy type change applies by itself: %+v", sum.Privacy)
	}
	if _, _, err := s.Load(context.Background(), "fixturejson"); err != nil {
		t.Errorf("a removed in-use definition stays until approved: %v", err)
	}
	if sum, _ := s.Summary("unused"); sum.Privacy != "private" {
		t.Errorf("an unused definition updates by itself: %s", sum.Privacy)
	}
	// approving applies the held text, and a removal drops the definition
	if err := s.Approve("fixturejson"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Load(context.Background(), "fixturejson"); !errors.Is(err, ErrNoDefinition) {
		t.Errorf("after approving the removal: %v", err)
	}
	if hs := s.Holds(); len(hs) != 1 || hs[0].ID != "fixturecookie" {
		t.Errorf("remaining holds %+v", hs)
	}
	if err := s.Approve("fixturejson"); err == nil {
		t.Error("approving twice should fail")
	}
	// the remaining hold survives a restart and the same update is not counted again
	since := s.Holds()[0].Since
	again := &Store{Dir: s.Dir, Fetch: repo, InUse: s.InUse, now: func() time.Time { return time.Now().Add(time.Hour) }}
	repo.commit = "c3"
	if err := again.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hs := again.Holds(); len(hs) != 1 || !hs[0].Since.Equal(since) {
		t.Errorf("hold after restart %+v", hs)
	}
}

func TestStoreHoldsNewRequiredSettings(t *testing.T) {
	pub := string(fixtureDef(t, "fixturepublic"))
	repo := &fakeRepo{commit: "c1", files: v11(map[string][]byte{"fixturepublic": []byte(pub)})}
	s := newTestStore(t, repo, "fixturepublic")
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pub, "\nsettings:\n") {
		t.Fatal("fixture changed: fixturepublic should have settings")
	}
	repo.commit = "c2"
	repo.files = v11(map[string][]byte{"fixturepublic": []byte(strings.Replace(pub, "\nsettings:\n", "\nsettings:\n  - name: apikey\n    type: text\n    label: API key\n", 1))})
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	hs := s.Holds()
	if len(hs) != 1 || !slices.Equal(hs[0].Reasons, []string{HoldNewRequiredSettings}) || !strings.Contains(strings.Join(hs[0].Detail, " "), "apikey") {
		t.Fatalf("holds %+v", hs)
	}
}

// The operator's definitions in custom/ run without a sync, win over a synced one with the same id, are marked
// custom, and survive every refresh and approval (the sync replaces only current/ and held/).
func TestStoreCustomDefinitions(t *testing.T) {
	pub := string(fixtureDef(t, "fixturepublic"))
	own := strings.Replace(pub, "name: ", "name: Own ", 1)
	mine := strings.Replace(pub, "id: fixturepublic", "id: mysite", 1)
	repo := &fakeRepo{commit: "c1", files: v11(map[string][]byte{"fixturepublic": []byte(pub), "fixturejson": fixtureDef(t, "fixturejson")})}
	s := newTestStore(t, repo, "fixturejson")
	custom := filepath.Join(s.Dir, customDir)
	if err := os.MkdirAll(custom, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(custom, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("own-copy.yml", own) // named by its id, not the file
	write("broken.yml", "id: [nope\n")
	write("notes.txt", mine)

	// before any sync, the custom one runs
	def, _, err := s.Load(context.Background(), "fixturepublic")
	if err != nil || !strings.HasPrefix(def.Name, "Own ") {
		t.Fatalf("custom before a sync: %v %+v", err, def)
	}
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if def, _, err := s.Load(context.Background(), "fixturepublic"); err != nil || !strings.HasPrefix(def.Name, "Own ") {
		t.Fatalf("a synced definition replaced the custom one: %v", err)
	}
	cat, err := s.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	marks := map[string]bool{}
	for _, c := range cat {
		marks[c.ID] = c.Custom
	}
	if len(cat) != 2 || !marks["fixturepublic"] || marks["fixturejson"] || s.Status().Definitions != 2 {
		t.Fatalf("catalog %v, status %+v", marks, s.Status())
	}

	// a file dropped later is read at the next refresh, even an unchanged commit
	write("mysite.yml", mine)
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sum, ok := s.Summary("mysite"); !ok || !sum.Custom {
		t.Fatalf("a dropped file was not read: %+v", sum)
	}

	// a new commit and an approval rewrite current/ and held/, never custom/
	repo.commit = "c2"
	repo.files = v11(map[string][]byte{"fixturepublic": []byte(pub)})
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hs := s.Holds(); len(hs) != 1 || hs[0].ID != "fixturejson" {
		t.Fatalf("holds %+v", hs)
	}
	if err := s.Approve("fixturejson"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"own-copy.yml", "mysite.yml", "broken.yml", "notes.txt"} {
		if _, err := os.Stat(filepath.Join(custom, name)); err != nil {
			t.Errorf("the sync removed custom/%s: %v", name, err)
		}
	}
	again := &Store{Dir: s.Dir}
	if def, _, err := again.Load(context.Background(), "fixturepublic"); err != nil || !strings.HasPrefix(def.Name, "Own ") {
		t.Fatalf("after a restart: %v", err)
	}
	if _, ok := again.Summary("mysite"); !ok {
		t.Fatal("custom definition lost after a restart")
	}
}
