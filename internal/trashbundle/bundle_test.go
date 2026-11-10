package trashbundle_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
	"github.com/theatarr/theatarr/internal/trashbundle"
)

var flavours = []format.Flavour{format.Radarr, format.Sonarr}

func bundleFS(t *testing.T) fs.FS {
	t.Helper()
	fsys, err := trashbundle.FS()
	if err != nil {
		t.Fatal(err)
	}
	return fsys
}

func bundleTree(t *testing.T, fl format.Flavour) *format.Trash {
	t.Helper()
	tr, err := format.LoadTrashFS(bundleFS(t), fl)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// checkout writes the snapshot to a directory, as a TRaSH-Guides checkout of the same commit holds these files.
func checkout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, bundleFS(t)); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The archive is a git archive of the commit COMMIT names, and the licence beside it is the one inside it.
func TestBundleIsThePinnedCommit(t *testing.T) {
	t.Parallel()
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(trashbundle.Commit()) || !regexp.MustCompile(`^\d{4}-\d\d-\d\d$`).MatchString(trashbundle.Date()) {
		t.Fatalf("COMMIT: %q %q", trashbundle.Commit(), trashbundle.Date())
	}
	raw, err := os.ReadFile("guides.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	// git archive writes the commit as the comment of a pax global header, the archive's first entry (git
	// get-tar-commit-id reads the same record).
	h, err := tar.NewReader(bytes.NewReader(mustGunzip(t, raw))).Next()
	if err != nil {
		t.Fatal(err)
	}
	if h.Typeflag != tar.TypeXGlobalHeader || h.PAXRecords["comment"] != trashbundle.Commit() {
		t.Fatalf("archive commit %q, COMMIT says %s", h.PAXRecords["comment"], trashbundle.Commit())
	}
	// NOTICE names the bundled commit; the refresh script's caller updates it with the bundle.
	if notice, err := os.ReadFile("../../NOTICE"); err != nil || !strings.Contains(string(notice), "TRaSH-Guides/Guides @ "+trashbundle.Commit()+" ("+trashbundle.Date()+")") {
		t.Fatalf("NOTICE does not name the bundled commit %s (%s): %v", trashbundle.Commit(), trashbundle.Date(), err)
	}
	license, err := fs.ReadFile(bundleFS(t), "LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	if string(license) != trashbundle.License() || !strings.HasPrefix(trashbundle.License(), "MIT License\n\nCopyright (c) ") {
		t.Fatal("LICENSE-TRaSH-Guides is not the LICENSE of the archived commit")
	}
}

func mustGunzip(t *testing.T, raw []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The snapshot is a file system fs.WalkDir, fs.Glob and os.CopyFS can read, and its files cannot be changed through it.
func TestBundleFS(t *testing.T) {
	t.Parallel()
	fsys := bundleFS(t)
	files := 0
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files++
		}
		return err
	})
	if err != nil || files < 500 {
		t.Fatalf("%d files: %v", files, err)
	}
	for _, dir := range []string{"docs/json/radarr/cf", "docs/json/sonarr/quality-profiles", "docs/json/radarr/cf-groups"} {
		if m, err := fs.Glob(fsys, dir+"/*.json"); err != nil || len(m) == 0 {
			t.Fatalf("%s: %d files: %v", dir, len(m), err)
		}
	}
	if _, err := fs.ReadFile(fsys, "docs/json/radarr/missing.json"); !os.IsNotExist(err) {
		t.Fatalf("missing file: %v", err)
	}
	a, _ := fs.ReadFile(fsys, "metadata.json")
	a[0] = 'X'
	if b, _ := fs.ReadFile(fsys, "metadata.json"); len(b) == 0 || b[0] == 'X' {
		t.Fatal("a read returned the shared bytes")
	}
}

// Both flavours load from the snapshot, and loading the same files from disk gives the same tree: every format's
// file, every profile and every group.
func TestBundleLoadsLikeACheckout(t *testing.T) {
	t.Parallel()
	dir := checkout(t)
	// At least the counts of the first bundled commit, so a refresh does not break the test.
	min := map[format.Flavour][3]int{format.Radarr: {200, 30, 30}, format.Sonarr: {200, 20, 30}}
	for _, fl := range flavours {
		bundled := bundleTree(t, fl)
		disk, err := format.LoadTrash(dir, fl)
		if err != nil {
			t.Fatal(err)
		}
		if n := min[fl]; len(bundled.Formats) < n[0] || len(bundled.Profiles) < n[1] || len(bundled.Groups) < n[2] {
			t.Fatalf("%s: %d formats, %d profiles, %d groups", fl, len(bundled.Formats), len(bundled.Profiles), len(bundled.Groups))
		}
		if len(disk.Formats) != len(bundled.Formats) || !reflect.DeepEqual(disk.Profiles, bundled.Profiles) || !reflect.DeepEqual(disk.Groups, bundled.Groups) {
			t.Fatalf("%s: the tree on disk differs from the bundled one", fl)
		}
		for i, f := range bundled.Formats {
			d := disk.Formats[i]
			if d.TrashID != f.TrashID || d.ID != f.ID || d.Name != f.Name || !bytes.Equal(disk.Raw(d.TrashID), bundled.Raw(f.TrashID)) || len(bundled.Raw(f.TrashID)) == 0 {
				t.Fatalf("%s: format %s differs on disk", fl, f.TrashID)
			}
		}
	}
}

// With THEATARR_TRASH_CHECKOUT naming a TRaSH-Guides checkout that holds the bundled commit, the snapshot's files
// are byte for byte the checkout's at that commit, and nothing LoadTrash reads there is missing from the snapshot.
// Skipped without it: the tests need no checkout.
func TestBundleMatchesCheckout(t *testing.T) {
	root := os.Getenv("THEATARR_TRASH_CHECKOUT")
	if root == "" {
		t.Skip("THEATARR_TRASH_CHECKOUT is not set")
	}
	head, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	if commit := strings.TrimSpace(string(head)); commit != trashbundle.Commit() {
		if ref, ok := strings.CutPrefix(commit, "ref: "); ok {
			b, _ := os.ReadFile(filepath.Join(root, ".git", ref))
			commit = strings.TrimSpace(string(b))
		}
		if commit != trashbundle.Commit() {
			t.Skipf("the checkout is at %s, the bundle at %s", commit, trashbundle.Commit())
		}
	}
	fsys := bundleFS(t)
	files := 0
	err = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		files++
		want, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return err
		}
		got, _ := fs.ReadFile(fsys, path)
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from the checkout", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, fl := range flavours {
		disk, err := format.LoadTrash(root, fl)
		if err != nil {
			t.Fatal(err)
		}
		bundled := bundleTree(t, fl)
		if len(disk.Formats) != len(bundled.Formats) || !reflect.DeepEqual(disk.Profiles, bundled.Profiles) || !reflect.DeepEqual(disk.Groups, bundled.Groups) {
			t.Errorf("%s: the checkout loads %d formats, %d profiles, %d groups; the bundle %d, %d, %d", fl,
				len(disk.Formats), len(disk.Profiles), len(disk.Groups), len(bundled.Formats), len(bundled.Profiles), len(bundled.Groups))
		}
	}
	t.Logf("%d bundled files equal the checkout at %s", files, trashbundle.Commit())
}

// newStore is a settings database seeded from the example policy, left without a TRaSH root. (The example policy
// names formats by trash_id, so its own import reads the small test tree; the root is cleared afterwards.)
func newStore(t *testing.T) (*settings.Store, *policy.Policy, *settings.Snapshot) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	const dir = "../../examples/policy"
	p, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Bootstrap(ctx, p, settings.ImportRequest{Dir: dir, Actor: "admin", Lookup: func(name string) (string, bool) { return "invented-" + name, true }}); err != nil {
		t.Fatal(err)
	}
	st.SetTrashRoot("../settings/testdata/trash")
	snap, err := st.BootstrapQuality(ctx, p, "upgrade")
	if err != nil {
		t.Fatal(err)
	}
	st.SetTrashRoot("")
	return st, p, snap
}

func cloneRecords(t *testing.T, r settings.Records) settings.Records {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var out settings.Records
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Release names that match many TRaSH formats, per flavour.
var releases = map[format.Flavour][]string{
	format.Radarr: {
		"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.DV.HDR.HEVC.TrueHD.7.1.Atmos-FraMeSToR",
		"Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX",
		"Dune.Part.Two.2024.1080p.BluRay.x264.DTS-HD.MA.7.1-DON",
		"Dune.Part.Two.2024.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb",
		"Dune.Part.Two.2024.2160p.WEB-DL.DV.HEVC-EVO",
		"Dune.Part.Two.2024.REPACK.1080p.BluRay.x265.10bit.AAC-YTS",
		"Dune.Part.Two.2024.IMAX.2160p.UHD.BluRay.x265.HDR10Plus.DTS-HD.MA.5.1-W4NK3R",
		"Dune.Part.Two.2024.GERMAN.DL.1080p.BluRay.x264-EXQUiSiTE",
		"Dune.Part.Two.2024.HDCAM.x264-NoGRP",
		"[SubsPlease] Dune Part Two (2024) (1080p) [BD x265 10bit FLAC]",
	},
	format.Sonarr: {
		"Severance.S02E01.2160p.ATVP.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX",
		"Severance.S02E01.1080p.ATVP.WEB-DL.DDP5.1.H.264-NTb",
		"Severance.S02E01.1080p.BluRay.x264-ROVERS",
		"Severance.S02E01.REPACK.720p.HDTV.x264-SYNCOPY",
		"Severance.S02.2160p.UHD.BluRay.REMUX.DV.HDR.HEVC.TrueHD.7.1.Atmos-FraMeSToR",
		"Severance.S02E01.1080p.WEB.h264-ETHEL",
		"Severance.S02E01.German.DL.1080p.WEB.x264-WvF",
		"[SubsPlease] Severance - 11 (1080p) [ABCD1234]",
		"[Erai-raws] Severance - 11 [1080p][HEVC][Multiple Subtitle]",
		"Severance.S02E01.480p.x264-mSD",
	},
}

// The blocking requirement: a profile created from a bundled preset, with no TRaSH root anywhere, is stored complete
// (every format carries its specification), builds into the decision engine without a tree, and scores releases as
// the same preset created from a checkout of the same commit does. Every bundled preset is built, saved through the
// settings store (which validates the whole policy) and compared.
func TestBundledPresetsScoreLikeACheckout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := checkout(t)
	st, base, snap := newStore(t)
	records := cloneRecords(t, snap.Records)
	type pair struct {
		fl                 format.Flavour
		name, bundle, disk string
	}
	var pairs []pair
	for _, fl := range flavours {
		bundled := bundleTree(t, fl)
		disk, err := format.LoadTrash(dir, fl)
		if err != nil {
			t.Fatal(err)
		}
		presets, err := settings.Presets(bundled)
		if err != nil {
			t.Fatalf("%s presets: %v", fl, err)
		}
		if len(presets) != len(bundled.Profiles) || len(presets) == 0 {
			t.Fatalf("%s: %d presets of %d profiles", fl, len(presets), len(bundled.Profiles))
		}
		for _, p := range presets {
			fromBundle, err := settings.BuildPreset(bundled, p.TrashID, nil, nil)
			if err != nil {
				t.Fatalf("%s %s (%s): %v", fl, p.Name, p.TrashID, err)
			}
			fromDisk, err := settings.BuildPreset(disk, p.TrashID, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fromBundle, fromDisk) {
				t.Fatalf("%s %s: the bundled build differs from the checkout's", fl, p.Name)
			}
			if len(fromBundle.Profile.Qualities) == 0 || len(fromBundle.Profile.Formats) == 0 {
				t.Fatalf("%s %s: empty profile", fl, p.Name)
			}
			// Stored complete: no format is a bare trash_id that a tree would have to resolve.
			for name, ref := range fromBundle.FormatSet.Config.Formats {
				if ref.Ref != "" || len(fromBundle.FormatSet.Specifications[name]) == 0 {
					t.Fatalf("%s %s: format %q is stored without its specification", fl, p.Name, name)
				}
			}
			pr := pair{fl: fl, name: p.Name, bundle: "b-" + string(fl) + "-" + p.TrashID, disk: "d-" + string(fl) + "-" + p.TrashID}
			for key, build := range map[string]*settings.PresetBuild{pr.bundle: fromBundle, pr.disk: fromDisk} {
				// No TRaSH sync source exists, as on a fresh install: the profile is created unbound.
				if source, err := settings.ApplyPreset(&records, key, build, nil); err != nil || source != "" {
					t.Fatalf("%s %s: bound to %q: %v", fl, p.Name, source, err)
				}
			}
			pairs = append(pairs, pr)
		}
	}
	// The store has no TRaSH root: the save validates every profile and format set from what is stored.
	if _, err := st.Save(ctx, settings.SaveRequest{ExpectedGeneration: snap.Generation, Actor: "admin", Base: base, Records: records}); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := st.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	pol := *loaded.Policy
	pol.Versions = map[string]*policy.VersionPolicy{}
	for _, pr := range pairs {
		kind := policy.KindMovie
		if pr.fl == format.Sonarr {
			kind = policy.KindSeries
		}
		for _, key := range []string{pr.bundle, pr.disk} {
			if pol.Profiles[key] == nil || pol.FormatSets[key] == nil {
				t.Fatalf("%s is not in the loaded policy", key)
			}
			pol.Versions[key] = &policy.VersionPolicy{Kind: kind, Profile: key}
		}
	}
	// No TrashRoot: a profile that needed the tree would fail here.
	eng, err := decide.New(&pol, decide.Options{})
	if err != nil {
		t.Fatalf("policy build without a TRaSH tree: %v", err)
	}
	scored, matched := 0, 0
	for _, pr := range pairs {
		title := decide.Title{ID: 1, Kind: policy.KindMovie, Name: "Dune: Part Two", Year: 2024}
		if pr.fl == format.Sonarr {
			title = decide.Title{ID: 2, Kind: policy.KindSeries, Name: "Severance", Year: 2022}
		}
		if eng.Profile(pr.bundle, pr.fl) == nil || eng.Profile(pr.disk, pr.fl) == nil {
			t.Fatalf("%s %s: no built profile", pr.fl, pr.name)
		}
		for _, name := range releases[pr.fl] {
			rel := decide.Release{Release: indexer.Release{Title: name, Size: 8 << 30, Seeders: 10, Peers: 10}}
			a, err := eng.Decide(ctx, rel, title, decide.Version{ID: 1, Key: pr.bundle, Policy: pol.Versions[pr.bundle]}, nil)
			if err != nil {
				t.Fatalf("%s %s %q: %v", pr.fl, pr.name, name, err)
			}
			b, err := eng.Decide(ctx, rel, title, decide.Version{ID: 1, Key: pr.disk, Policy: pol.Versions[pr.disk]}, nil)
			if err != nil {
				t.Fatal(err)
			}
			// The two decisions differ only in the names of their version and profile.
			b.Version, b.Profile = a.Version, a.Profile
			ja, _ := json.Marshal(a)
			jb, _ := json.Marshal(b)
			if !bytes.Equal(ja, jb) {
				t.Fatalf("%s %s %q: bundled\n%s\ncheckout\n%s", pr.fl, pr.name, name, ja, jb)
			}
			scored++
			if len(a.Formats) > 0 && a.Score != 0 {
				matched++
			}
		}
	}
	if matched < scored/2 {
		t.Fatalf("only %d of %d decisions matched a scored format: the comparison proves little", matched, scored)
	}
	t.Logf("%d presets, %d decisions equal, %d with a format score", len(pairs), scored, matched)
}

// The boundary: only profiles made from a preset carry their formats. A format named by trash_id alone still needs
// the checkout at policy-build time; the bundle is not a fallback there.
func TestTrashIDWithoutSpecificationStillNeedsACheckout(t *testing.T) {
	t.Parallel()
	tree := bundleTree(t, format.Radarr)
	id := tree.Formats[0].TrashID
	pol := &policy.Policy{
		Profiles: map[string]*policy.Profile{"p": {Qualities: []policy.QualityItem{{Name: "WEBDL-1080p"}}, Cutoff: "WEBDL-1080p", Formats: map[string]int{"f": 10}}},
		Formats:  map[string]*policy.FormatRef{"f": {TrashID: id, Flavour: "radarr"}},
		Versions: map[string]*policy.VersionPolicy{"v": {Kind: policy.KindMovie, Profile: "p"}},
	}
	if _, err := decide.New(pol, decide.Options{}); err == nil || !strings.Contains(err.Error(), "no TRaSH directory is set") {
		t.Fatalf("a trash_id format built without a TRaSH directory: %v", err)
	}
	if _, err := decide.New(pol, decide.Options{TrashRoot: checkout(t)}); err != nil {
		t.Fatalf("with a checkout: %v", err)
	}
}
