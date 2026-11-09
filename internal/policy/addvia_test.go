package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// movieWebClient is the example's movie-web version, which names client decypharr (callback view radarr, which
// serves movie-web).
const movieWebClient = "    providers: [rd, ad, tb]\n    client: decypharr\n\n  anime-movie:"

// editExample copies the example policy and applies each old → new replacement to file (old "" appends new).
func editExample(t *testing.T, edits ...[3]string) string {
	t.Helper()
	dir := copyExample(t)
	for _, ed := range edits {
		path := filepath.Join(dir, ed[0])
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b) + ed[2]
		if ed[1] != "" {
			if !strings.Contains(string(b), ed[1]) {
				t.Fatalf("test setup: %q not in %s", ed[1], ed[0])
			}
			s = strings.Replace(string(b), ed[1], ed[2], 1)
		}
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestAddViaDefaultsToProviders(t *testing.T) {
	t.Parallel()
	p, err := Load(copyExample(t))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range p.Versions {
		if v.AddVia != AddViaProviders || v.ViaClient() {
			t.Errorf("versions[%s].add_via = %q", k, v.AddVia)
		}
	}
	if c := p.Clients["decypharr"]; c.FromDownloadFolder() || c.ImportFrom != "" {
		t.Errorf("import_from = %q", c.ImportFrom)
	}
}

func TestAddViaClientLoads(t *testing.T) {
	t.Parallel()
	dir := editExample(t, [3]string{"versions.yaml", movieWebClient,
		"    add_via: client\n    client: decypharr\n\n  anime-movie:"},
		[3]string{"clients.yaml", "    callback_view: radarr\n", "    callback_view: radarr\n" +
			"    import_from: download_folder\n    remote_path: /data/symlinks\n    local_path: /mnt/decypharr\n"})
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	v := p.Versions["movie-web"]
	if !v.ViaClient() || len(v.Providers) != 0 {
		t.Fatalf("movie-web: %+v", v)
	}
	c := p.Clients["decypharr"]
	if !c.FromDownloadFolder() {
		t.Fatal("import_from not read")
	}
	for in, want := range map[string]string{
		"/data/symlinks/theatarr/Movie.2024": "/mnt/decypharr/theatarr/Movie.2024",
		"/data/symlinks":                     "/mnt/decypharr",
		"/data/symlinks2/x":                  "/data/symlinks2/x",
		"/elsewhere/x":                       "/elsewhere/x",
	} {
		if got := c.LocalContentPath(in); got != want {
			t.Errorf("LocalContentPath(%s) = %s, want %s", in, got, want)
		}
	}
	if got := (&Client{}).LocalContentPath("/data/x"); got != "/data/x" {
		t.Errorf("no mapping: %s", got)
	}
}

func TestAddViaClientRules(t *testing.T) {
	t.Parallel()
	viaClient := [3]string{"versions.yaml", movieWebClient, "    add_via: client\n    client: decypharr\n\n  anime-movie:"}
	cases := []struct {
		name  string
		edits [][3]string
		want  []string
	}{
		{name: "unknown add_via", edits: [][3]string{{"versions.yaml", movieWebClient,
			"    providers: [rd, ad, tb]\n    add_via: decypharr\n    client: decypharr\n\n  anime-movie:"}},
			want: []string{`versions[movie-web].add_via: must be providers or client, got "decypharr"`}},
		{name: "no client", edits: [][3]string{{"versions.yaml", movieWebClient, "    add_via: client\n\n  anime-movie:"}},
			want: []string{`versions[movie-web].client: required`}},
		{name: "seedbox client", edits: [][3]string{{"versions.yaml", movieWebClient,
			"    add_via: client\n    client: seedbox\n\n  anime-movie:"}},
			want: []string{`versions[movie-web].client: client "seedbox" (qbittorrent) does not serve debrid storage`}},
		{name: "seedbox storage", edits: [][3]string{{"versions.yaml", "    storage: debrid\n    indexers: [prowlarr-public, dmm, torrentio, zilean]\n    providers: [rd, ad, tb]\n    client: decypharr\n\n  anime-movie:",
			"    storage: seedbox\n    indexers: [prowlarr-public, dmm, torrentio, zilean]\n    add_via: client\n    client: seedbox\n\n  anime-movie:"}},
			want: []string{`versions[movie-web].add_via: a client adds to debrid: storage must be debrid, got "seedbox"`}},
		{name: "view does not serve the version", edits: [][3]string{viaClient,
			{"views.yaml", "versions: [movie-remux, movie-web, anime-movie]", "versions: [movie-remux, anime-movie]"},
			{"views.yaml", "      WEB only: [movie-web]\n", ""}},
			want: []string{`versions[movie-web].client: client "decypharr" calls back view "radarr", which does not serve this version`}},
		{name: "view of the other flavour", edits: [][3]string{viaClient,
			{"clients.yaml", "    callback_view: radarr", "    callback_view: sonarr"},
			{"views.yaml", "versions: [tv-2160p, anime-tv]", "versions: [tv-2160p, anime-tv, movie-web]"}},
			want: []string{`versions[movie-web].client: client "decypharr" calls back view "sonarr" (sonarr), which does not serve movie versions`}},
		{name: "import_from", edits: [][3]string{{"clients.yaml", "    callback_view: radarr\n",
			"    callback_view: radarr\n    import_from: folder\n"}},
			want: []string{`clients[decypharr].import_from: must be mount or download_folder, got "folder"`}},
		{name: "half a path mapping", edits: [][3]string{{"clients.yaml", "    callback_view: radarr\n",
			"    callback_view: radarr\n    remote_path: /data\n"}},
			want: []string{`clients[decypharr].remote_path: remote_path and local_path go together: set both or neither`}},
		{name: "relative mapping", edits: [][3]string{{"clients.yaml", "    callback_view: radarr\n",
			"    callback_view: radarr\n    remote_path: data\n    local_path: /mnt/x\n"}},
			want: []string{`clients[decypharr].remote_path: "data" must be an absolute path`}},
		{name: "no category", edits: [][3]string{viaClient,
			{"clients.yaml", "    category: theatarr\n    callback_view: radarr", "    callback_view: radarr"}},
			want: []string{`versions[movie-web].client: client "decypharr" has no category: decypharr needs one for the adds of an add_via: client version`}},
		{name: "providers still required without add_via client", edits: [][3]string{{"versions.yaml", movieWebClient,
			"    client: decypharr\n\n  anime-movie:"}},
			want: []string{`versions[movie-web].providers: required for debrid storage (or add_via: client)`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(editExample(t, tc.edits...))
			if err == nil {
				t.Fatal("loaded")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("missing %q in:\n%v", w, err)
				}
			}
		})
	}
}

// A provider-routed version's decypharr client needs no category (decypharr only calls back).
func TestClientCategoryOptionalForProviderRouted(t *testing.T) {
	t.Parallel()
	if _, err := Load(editExample(t, [3]string{"clients.yaml", "    category: theatarr\n    callback_view: radarr",
		"    callback_view: radarr"})); err != nil {
		t.Fatal(err)
	}
}
