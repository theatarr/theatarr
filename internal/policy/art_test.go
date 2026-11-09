package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArtExample(t *testing.T) {
	t.Parallel()
	p, err := Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	a := p.ArtSettings()
	if a.PerRun != 200 || a.Interval != 6*time.Hour || a.Prewarm != "w342" {
		t.Errorf("art %+v", a)
	}
	im := p.ImageSettings("/var/lib/theatarr")
	if im.Dir != "/data/images" || im.MaxBytes != 2<<30 || im.Original {
		t.Errorf("images %+v", im)
	}
	if !im.ValidImageSize("w342") || im.ValidImageSize("original") || im.ValidImageSize("w343") {
		t.Error("sizes")
	}
	if b := p.Budgets[TMDbImagesSource]; b == nil || b.Daily != 20000 {
		t.Errorf("budget %+v", b)
	}
	var found bool
	for _, h := range p.OutboundHosts() {
		found = found || h.Host == "image.tmdb.org"
	}
	if !found {
		t.Error("image.tmdb.org is not an outbound host")
	}
	// Defaults without the sections.
	empty := &Policy{}
	if a := empty.ArtSettings(); a.PerRun != DefaultArtPerRun || a.Interval != DefaultArtInterval || a.Prewarm != "w342" {
		t.Errorf("default art %+v", a)
	}
	if im := empty.ImageSettings("/data"); im.Dir != "/data/images" || im.MaxBytes != DefaultImageMaxBytes {
		t.Errorf("default images %+v", im)
	}
}

func TestArtValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, file, old, new, want string
	}{
		{"per_run", "metadata.yaml", "per_run: 200", "per_run: 9000", "metadata.art.per_run"},
		{"interval", "metadata.yaml", "interval: 6h", "interval: 1m", "metadata.art.interval"},
		{"prewarm", "metadata.yaml", "prewarm: w342", "prewarm: w343", "metadata.art.prewarm"},
		{"prewarm off", "metadata.yaml", "prewarm: w342", "prewarm: \"off\"", ""},
		{"image key", "metadata.yaml", "budget: tmdb-images", "budget: tmdb-images\n      api_key_env: THEATARR_TMDB_KEY", "metadata.sources[tmdb-images].api_key_env"},
		{"no image source", "metadata.yaml", "    tmdb-images:\n      url: https://image.tmdb.org/t/p\n      budget: tmdb-images\n", "", "ui.images"},
		{"relative dir", "ui.yaml", "dir: /data/images", "dir: images", "ui.images.dir"},
		{"small cache", "ui.yaml", "max_bytes: 2GiB", "max_bytes: 1MiB", "ui.images.max_bytes"},
		{"bad size", "ui.yaml", "max_bytes: 2GiB", "max_bytes: lots", "lots"},
		{"bytes", "ui.yaml", "max_bytes: 2GiB", "max_bytes: 536870912", ""},
		{"unknown field", "ui.yaml", "original: false", "originals: false", "originals"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := copyExample(t)
			path := filepath.Join(dir, tc.file)
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), tc.old) {
				t.Fatalf("%s lacks %q", tc.file, tc.old)
			}
			b = []byte(strings.Replace(string(b), tc.old, tc.new, 1))
			if err := os.WriteFile(path, b, 0o644); err != nil {
				t.Fatal(err)
			}
			_, err = Load(dir)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("want valid, got %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("want an error about %s, got %v", tc.want, err)
			}
		})
	}
}

func TestParseByteSize(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int64{"2GiB": 2 << 30, "512MiB": 512 << 20, "500MB": 500e6, "1048576": 1 << 20,
		"1.5GiB": 3 << 29, "64KiB": 64 << 10, "10B": 10} {
		if got, err := ParseByteSize(in); err != nil || int64(got) != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "GiB", "-1MiB", "2 GB extra", "1e30TiB"} {
		if _, err := ParseByteSize(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}
