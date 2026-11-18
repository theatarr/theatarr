package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestManualImportOverridesAndModes(t *testing.T) {
	t.Parallel()
	for _, series := range []bool{false, true} {
		for _, mode := range []string{"move", "copy", "auto"} {
			t.Run(map[bool]string{false: "movie", true: "series"}[series]+"/"+mode, func(t *testing.T) {
				e := newEnv(t, true)
				version, folder, rel, tid, vid, episodes, fl := "movie-remux", "Amelie.2001.2160p.REMUX-OLD", "Amelie.2001.2160p.REMUX-OLD.mkv", int64(1), int64(10), "[]", format.Radarr
				if series {
					version, folder, rel, tid, vid, episodes, fl = "tv", "Show.S01.2160p.BluRay.REMUX-OLD", "Show.S01E01.2160p.BluRay.REMUX-OLD.mkv", 2, 20, "[202]", format.Sonarr
				}
				e.release(folder, map[string]float64{rel: 7200})
				source := filepath.Join(e.mount, "__all__", folder, rel)
				before, err := os.Stat(source)
				if err != nil {
					t.Fatal(err)
				}
				e.download(1, version, tid, vid, folder, folder, episodes)
				group, flags := "CHOSEN", 4
				o := &decide.ManualOverrides{Quality: &decide.ManualQuality{Name: "WEBDL-1080p", Revision: profile.Revision{Version: 2, Real: 1}}, Languages: []string{"French"}, ReleaseGroup: &group, IndexerFlags: &flags, ImportMode: mode}
				b, _ := json.Marshal(o)
				e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, how, updated_at, overrides) VALUES (1, ?, ?, 'manual', 'manual', 0, ?)`, rel, episodes, string(b))
				formats, err := format.ParseArrFormats(fl, []byte(`[{"id":900,"name":"Chosen group","includeCustomFormatWhenRenaming":true,"specifications":[{"name":"group","implementation":"ReleaseGroupSpecification","fields":{"value":"^CHOSEN$"}}]}]`))
				if err != nil {
					t.Fatal(err)
				}
				p := e.lib.engine.Profile(e.pol.Versions[version].Profile, fl)
				p.Formats = formats
				p.Profile.FormatItems = []format.FormatItem{{Format: 900, Score: 123}}
				var ids []int64
				if series {
					ids = []int64{202}
				}
				preview, err := e.lib.PlanNew(e.ctx, NewImport{TitleID: tid, VersionID: vid, Version: version,
					ReleaseTitle: folder, SourcePath: source, Files: map[string][]int64{rel: ids},
					Overrides: map[string]*decide.ManualOverrides{rel: o}})
				if err != nil || len(preview.Links) != 1 || !strings.Contains(preview.Links[0].Path, "CHOSEN") || !strings.Contains(preview.Links[0].Path, "WEBDL-1080p") {
					t.Fatalf("new import preview %+v: %v", preview, err)
				}
				if got := e.one(`SELECT COUNT(*) FROM link`); got != "0" {
					t.Fatalf("preview wrote %s links", got)
				}
				plan, err := e.lib.Import(e.ctx, 1, false)
				if err != nil || len(plan.Links) != 1 || !plan.Links[0].Done {
					t.Fatalf("plan %+v: %v", plan, err)
				}
				link := plan.Links[0]
				if got, err := os.Readlink(link.Path); err != nil || got != source {
					t.Fatalf("link %q: %v", got, err)
				}
				after, err := os.Stat(source)
				if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
					t.Fatalf("source changed: %v", err)
				}
				if data, err := os.ReadFile(source); err != nil || string(data) != "video" {
					t.Fatalf("source data %q: %v", data, err)
				}
				var saved decide.ExistingFile
				if err := json.Unmarshal([]byte(e.one(`SELECT file_metadata FROM link`)), &saved); err != nil {
					t.Fatal(err)
				}
				if saved.Quality != "WEBDL-1080p" || saved.Revision != 2 || saved.Real != 1 || saved.ReleaseGroup != group || strings.Join(saved.Languages, ",") != "French" || saved.IndexerFlags != flags || saved.Score == nil || *saved.Score != 123 {
					t.Fatalf("metadata %+v", saved)
				}
				if !strings.Contains(link.Path, "WEBDL-1080p") || !strings.Contains(link.Path, "CHOSEN") {
					t.Fatalf("name %s", link.Path)
				}
				if series && (len(link.Episodes) != 1 || link.Episodes[0] != 202 || !strings.Contains(link.Path, "S01E02")) {
					t.Fatalf("episodes %+v", link)
				}
				if got := e.one(`SELECT detail FROM library_action WHERE kind = 'link'`); !strings.Contains(got, "client importMode "+mode) || !strings.Contains(got, "linked instead") {
					t.Fatalf("journal %q", got)
				}
				if got := e.one(`SELECT quality || ':' || score FROM link`); got != "WEBDL-1080p:123" {
					t.Fatalf("link %s", got)
				}
			})
		}
	}
}

func TestManualFactsChangeUpgradeDecision(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	d, err := (&NewImport{TitleID: 1, VersionID: 10, Version: "movie-remux", ReleaseTitle: "Amelie.2001.1080p.WEB-DL-OLD"}).download(e.ctx, e.st.Reader(), e.pol)
	if err != nil {
		t.Fatal(err)
	}
	plan := &Plan{Folder: "Amelie.2001.1080p.WEB-DL-OLD"}
	f := &PlannedFile{Path: "Amelie.2001.1080p.WEB-DL-OLD.mkv", Rel: "file.mkv"}
	facts, err := e.lib.fileFacts(d, plan, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	title := &decide.Title{Kind: policy.KindMovie, Name: "Amelie", Year: 2001}
	v := vToDecideVersion(&d.Version)
	current := &decide.ExistingFile{Quality: "Remux-2160p", Revision: 1}
	check := func(facts decide.FileFacts) bool {
		incoming := withFacts(decide.ExistingFile{}, facts, f.Rel, f.Path, 1)
		ok, _, err := e.lib.engine.ImportUpgrade(title, v, &incoming, current)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if check(facts) {
		t.Fatal("parsed lower quality accepted")
	}
	d.Manual = map[string]fileIdentity{f.Rel: {Overrides: &decide.ManualOverrides{Quality: &decide.ManualQuality{Name: "Remux-2160p", Revision: profile.Revision{Version: 2}}}}}
	facts, err = e.lib.fileFacts(d, plan, f, nil)
	if err != nil || !check(facts) {
		t.Fatalf("override not used: %+v %v", facts, err)
	}
}

// TestManualOverridesScoredWithFileStanding: in a prefer-SeaDex library, a pack's files imported with manual overrides
// are scored and named with their own SeaDex standing (the entry listing the pack covers E01 only, so E02 has none),
// not with the pack's, and the rename preview right after proposes nothing.
func TestManualOverridesScoredWithFileStanding(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, "")
	e.pol.Versions["tv"].PreferSeaDex = true
	e.pol.Versions["tv"].Naming.File = "{Series CleanTitleWithoutYear} - S{season:00}E{episode:00} {[Custom Formats]}"
	p := e.lib.engine.Profile(e.pol.Versions["tv"].Profile, format.Sonarr)
	fs, err := format.ParseArrFormats(format.Sonarr, []byte(`[{"id":900,"name":"SeaDex best","includeCustomFormatWhenRenaming":true,
		"specifications":[{"name":"best","implementation":"SeaDexSpecification","negate":false,"required":true,"fields":{"value":"best"}}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	p.Formats = fs
	p.Profile.FormatItems = []format.FormatItem{{Format: 900, Score: 100}}
	scopeEntry(e, 11, `[[1,1]]`, false, false)
	e1, e2 := "Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP", "Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP"
	scopePack(e, "[201, 202]", "", e1, e2)
	group := "GRP"
	b, _ := json.Marshal(&decide.ManualOverrides{ReleaseGroup: &group})
	e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, how, updated_at, overrides)
		VALUES (1, ?, '[201]', 'manual', 'manual', 0, ?), (1, ?, '[202]', 'manual', 'manual', 0, ?)`,
		e1+".mkv", string(b), e2+".mkv", string(b))
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	got := e.one(`SELECT GROUP_CONCAT(x, ', ') FROM (SELECT le.episode_id || '=' || l.score || ' ' ||
		json_extract(l.file_metadata, '$.SeaDex') || ' ' || replace(l.path, rtrim(l.path, replace(l.path, '/', '')), '') AS x
		FROM link l JOIN link_episode le ON le.link_id = l.id WHERE l.state = 'active' ORDER BY le.episode_id)`)
	if got != "201=100 2 Show - S01E01 [SeaDex best].mkv, 202=0 0 Show - S01E02.mkv" {
		t.Errorf("links %s", got)
	}
	preview, err := e.lib.PreviewRename(e.ctx, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Files) != 0 {
		t.Errorf("rename right after the import: %+v", preview.Files)
	}
}
