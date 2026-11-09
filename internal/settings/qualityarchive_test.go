package settings

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func qualityArchive(t *testing.T, headers []*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write(bytes.Repeat([]byte(" "), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestQualityArchiveBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		header *tar.Header
	}{
		{"traversal", &tar.Header{Name: "repo/../metadata.json", Typeflag: tar.TypeReg}},
		{"absolute", &tar.Header{Name: "/metadata.json", Typeflag: tar.TypeReg}},
		{"link", &tar.Header{Name: "repo/metadata.json", Typeflag: tar.TypeSymlink, Linkname: "/tmp/elsewhere"}},
		{"large", &tar.Header{Name: "repo/metadata.json", Typeflag: tar.TypeReg, Size: (8 << 20) + 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if root, err := unpackQualityArchive(qualityArchive(t, []*tar.Header{tc.header})); err == nil {
				os.RemoveAll(root)
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	h := &tar.Header{Name: "repo/metadata.json", Typeflag: tar.TypeReg, Size: 2}
	if root, err := unpackQualityArchive(qualityArchive(t, []*tar.Header{h, h})); err == nil {
		os.RemoveAll(root)
		t.Fatal("duplicate accepted")
	}
	if root, err := unpackQualityArchive(qualityArchive(t, []*tar.Header{{Name: "repo/file.txt", Typeflag: tar.TypeReg}})); err == nil {
		os.RemoveAll(root)
		t.Fatal("missing metadata accepted")
	}
}

func TestQualitySyncPinnedArchivePreview(t *testing.T) {
	t.Parallel()
	svc, _, initial := syncService(t)
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	// The HTTP fixture is a git archive of the complete local tree, including one harmless non-JSON file.
	root := svc.TrashRoot
	paths := []string{"metadata.json"}
	entries, err := os.ReadDir(root + "/radarr")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		paths = append(paths, "radarr/"+e.Name())
	}
	for _, path := range paths {
		raw, err := os.ReadFile(root + "/" + path)
		if err != nil {
			t.Fatal(err)
		}
		if err := tw.WriteHeader(&tar.Header{Name: "guides-commit/" + path, Mode: 0600, Size: int64(len(raw))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(b.Bytes()) }))
	defer server.Close()
	svc.FetchArchive = func(ctx context.Context, c QualitySyncConfig) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", c.ArchiveURL, nil)
		if err != nil {
			return nil, err
		}
		res, err := server.Client().Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		return io.ReadAll(res.Body)
	}
	commit := strings.Repeat("a", 40)
	p, err := svc.Preview(context.Background(), QualitySyncSource{Record: Record{Key: "archive"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr", ArchiveURL: server.URL + "/" + commit + ".tar.gz", Commit: commit}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Commit != commit || p.Generation != initial.Generation || p.HasChanges() {
		t.Fatalf("pinned preview differs: %+v", p)
	}
}

// git archive and GitHub's codeload tarballs begin with a pax global header holding the commit id.
func TestQualityArchiveGitGlobalHeader(t *testing.T) {
	t.Parallel()
	data := qualityArchive(t, []*tar.Header{
		{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": strings.Repeat("a", 40)}},
		{Name: "Guides-abc/", Typeflag: tar.TypeDir, Mode: 0755},
		{Name: "Guides-abc/metadata.json", Typeflag: tar.TypeReg, Size: 2, Mode: 0644},
	})
	root, err := unpackQualityArchive(data)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if _, err := os.Stat(root + "/metadata.json"); err != nil {
		t.Fatal(err)
	}
	// The real thing, when a git archive of a TRaSH checkout is at hand (THEATARR_TEST_TRASH_ARCHIVE; never fetched).
	if path := os.Getenv("THEATARR_TEST_TRASH_ARCHIVE"); path != "" {
		real, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		root, err := unpackQualityArchive(real)
		if err != nil {
			t.Fatal(err)
		}
		os.RemoveAll(root)
	}
}
