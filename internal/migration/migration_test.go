package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/golive"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func TestInspectReadsTheStackWithGetOnlyAndKeepsNoKey(t *testing.T) {
	t.Parallel()
	fs := newFakeStack(t)
	inv := fs.inspect(t)
	for _, r := range fs.requests() {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("request %s", r)
		}
	}
	if n := len(fs.requests()); n != 38 {
		t.Errorf("%d requests, want 38 (16 per arr, 3 Prowlarr, 3 Seerr): %v", n, fs.requests())
	}
	b, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, "the inventory", b)

	radarr := inv.Instance("radarr_uhd")
	if radarr == nil || radarr.Error != "" || radarr.AppVersion != "5.14.0.9383" || radarr.Key != "RADARR_UHD_KEY" {
		t.Fatalf("radarr_uhd: %+v", radarr)
	}
	if radarr.Titles != (TitleCounts{Total: 3, Monitored: 2, WithFiles: 2}) {
		t.Errorf("radarr titles %+v", radarr.Titles)
	}
	wantRoots := []RootFolder{{Path: "/media/movies-uhd", Titles: 2}, {Path: "/media/movies-old", Titles: 1}}
	if !reflect.DeepEqual(radarr.RootFolders, wantRoots) {
		t.Errorf("radarr roots %+v", radarr.RootFolders)
	}
	if !reflect.DeepEqual(radarr.QualityProfiles, []NamedCount{{ID: 7, Name: "Remux + WEB 2160p", Titles: 3}}) {
		t.Errorf("radarr profiles %+v", radarr.QualityProfiles)
	}
	var ids []int
	for _, ix := range radarr.Indexers {
		ids = append(ids, ix.ProwlarrID)
	}
	if !reflect.DeepEqual(ids, []int{1, 10, 11}) {
		t.Errorf("radarr indexers' Prowlarr ids %v", ids)
	}
	if c := radarr.DownloadClients; len(c) != 2 || c[0].Bridge != "decypharr" || c[0].Host != "http://decypharr:8282" ||
		c[0].Category != "radarr_uhd" || c[1].Bridge != "" || c[1].Protocol != "usenet" {
		t.Errorf("radarr clients %+v", c)
	}
	if radarr.DelayProfiles != 0 || radarr.Hardlinks || len(radarr.Raw) != 6 {
		t.Errorf("radarr delay %d hardlinks %t raw %d", radarr.DelayProfiles, radarr.Hardlinks, len(radarr.Raw))
	}
	// Connect targets and import lists: names, triggers and switches, and only the host of their addresses.
	wantNotes := []ArrNotification{
		{Name: "Plex", Implementation: "PlexServer", Triggers: []string{"Download", "MovieDelete", "Rename", "Upgrade"},
			Target: "http://plex:32400"},
		{Name: "Discord grabs", Implementation: "Discord", Triggers: []string{"Grab"}, Target: "https://discord.com"},
		{Name: "usenet-bridge", Implementation: "Webhook", Triggers: []string{}, Target: "http://usenet-bridge:8080"},
		// Userinfo in a scheme-less host is dropped; Email's server is a plain hostname with its port.
		{Name: "Signal", Implementation: "Signal", Triggers: []string{"HealthIssue"}, Target: "https://signal.example:8080"},
		{Name: "Mail", Implementation: "Email", Triggers: []string{"Download"}, Target: "smtp.example.com:587"}}
	if !reflect.DeepEqual(radarr.Notifications, wantNotes) {
		t.Errorf("radarr notifications %+v", radarr.Notifications)
	}
	wantLists := []ArrImportList{
		{Name: "radarr_4k_web", Implementation: "RadarrImport", Enabled: true, AutoAdd: true, Source: "http://radarr4kweb:7878"},
		{Name: "Watchlist", Implementation: "PlexRssImport", Source: "https://rss.plex.tv"}}
	if !reflect.DeepEqual(radarr.ImportLists, wantLists) {
		t.Errorf("radarr import lists %+v", radarr.ImportLists)
	}
	sonarr := inv.Instance("sonarr_anime")
	if sonarr != nil && (len(sonarr.Notifications) != 0 || !reflect.DeepEqual(sonarr.ImportLists, []ArrImportList{
		{Name: "Trakt anime", Implementation: "TraktListImport", Enabled: true, AutoAdd: true},
		// Sonarr has no enabled switch: a list without automatic add does not sync.
		{Name: "Paused list", Implementation: "SonarrImport", Source: "http://sonarr4k:8989"}})) {
		t.Errorf("sonarr notifications %+v, lists %+v", sonarr.Notifications, sonarr.ImportLists)
	}
	if sonarr == nil || sonarr.Titles != (TitleCounts{Total: 2, Monitored: 2, WithFiles: 1, Anime: 2}) || !sonarr.Hardlinks {
		t.Fatalf("sonarr_anime: %+v", sonarr)
	}

	pr := inv.Prowlarr
	if pr == nil || pr.Error != "" || len(pr.Indexers) != 5 {
		t.Fatalf("prowlarr: %+v", pr)
	}
	for _, ix := range pr.Indexers {
		for _, f := range ix.Fields {
			if secretField(f.Name) || f.Value == "********" {
				t.Errorf("prowlarr indexer %s kept field %s", ix.Name, f.Name)
			}
		}
	}
	apps := map[string]string{}
	for _, a := range pr.Applications {
		apps[a.Name] = a.Instance
	}
	if !reflect.DeepEqual(apps, map[string]string{"Radarr UHD": "radarr_uhd", "Sonarr Anime": "sonarr_anime", "Lidarr": ""}) {
		t.Errorf("prowlarr applications %v", apps)
	}
	s := inv.Seerr
	if s == nil || s.Error != "" || len(s.Radarr) != 2 || len(s.Sonarr) != 1 {
		t.Fatalf("seerr: %+v", s)
	}
	if s.Radarr[0].Instance != "radarr_uhd" || s.Radarr[1].Instance != "" || s.Sonarr[0].Instance != "sonarr_anime" ||
		s.Sonarr[0].ActiveAnimeProfileName != "Remux-2160p Anime" {
		t.Errorf("seerr servers %+v %+v", s.Radarr, s.Sonarr)
	}
	r := inv.Recyclarr
	if r == nil || r.Error != "" || len(r.Instances) != 2 || r.Instances[0].Instance != "radarr_uhd" ||
		r.Instances[0].Profiles[0].TrashID != "fd161a61e3ab826d3a22d53f935696dd" || r.Instances[1].Instance != "" {
		t.Errorf("recyclarr %+v", r)
	}
	if strings.Contains(string(b), "radarr_uhd_api_key") || strings.Contains(string(b), "RADARR_UHD_URL") {
		t.Error("the inventory holds a recyclarr secret or env reference")
	}
}

func TestInspectNeedsEveryKeyFirst(t *testing.T) {
	t.Parallel()
	fs := newFakeStack(t)
	keys := MapKeys(map[string]string{"RADARR_UHD_KEY": radarrKey, "SONARR_ANIME_KEY": sonarrKey, "PROWLARR_KEY": prowlarrKey})
	_, err := Inspect(context.Background(), fs.sources(), keys, testDialer, testNow)
	if err == nil || !strings.Contains(err.Error(), "SEERR_KEY is not set") {
		t.Fatalf("err %v", err)
	}
	if n := len(fs.requests()); n != 0 {
		t.Fatalf("%d requests before the keys were checked", n)
	}
}

func TestInspectRecordsAServiceItCannotRead(t *testing.T) {
	t.Parallel()
	fs := newFakeStack(t)
	const wrong = "wrong-seerr-key-000000"
	keys := MapKeys(map[string]string{"RADARR_UHD_KEY": radarrKey, "SONARR_ANIME_KEY": sonarrKey,
		"PROWLARR_KEY": prowlarrKey, "SEERR_KEY": wrong})
	inv, err := Inspect(context.Background(), fs.sources(), keys, testDialer, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Seerr == nil || inv.Seerr.Error == "" || len(inv.Seerr.Radarr) != 0 {
		t.Fatalf("seerr %+v", inv.Seerr)
	}
	b, _ := json.Marshal(inv)
	if strings.Contains(string(b), wrong) {
		t.Error("the inventory holds the key that failed")
	}
	p, err := Build(BuildInput{Inventory: inv, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if !hasGap(p, GapUnread, "seerr") {
		t.Errorf("no unread gap for Seerr: %+v", p.Gaps)
	}
	for _, s := range p.Steps {
		if strings.HasPrefix(s.ID, "seerr/") {
			t.Errorf("step %s planned without Seerr", s.ID)
		}
	}
}

func TestInspectRefusesAKeyEchoedBack(t *testing.T) {
	t.Parallel()
	fs := newFakeStack(t)
	fs.override["radarr_uhd/tag"] = `[{"id": 1, "label": "` + prowlarrKey + `"}]`
	if _, err := Inspect(context.Background(), fs.sources(), testKeys(), testDialer, testNow); err == nil ||
		strings.Contains(err.Error(), prowlarrKey) {
		t.Fatalf("err %v", err)
	}
}

func TestSourcesValidate(t *testing.T) {
	t.Parallel()
	for name, src := range map[string]Sources{
		"none":            {},
		"key value":       {Arr: []ArrSource{{Name: "r", Flavour: "radarr", URL: "http://r:7878", Key: "abc+/="}}},
		"credentials":     {Arr: []ArrSource{{Name: "r", Flavour: "radarr", URL: "http://u:p@r:7878", Key: "K"}}},
		"query":           {Prowlarr: &ServiceSource{URL: "http://p:9696?apikey=x", Key: "K"}},
		"flavour":         {Arr: []ArrSource{{Name: "r", Flavour: "lidarr", URL: "http://r:8686", Key: "K"}}},
		"name":            {Arr: []ArrSource{{Name: "Radarr 4K", Flavour: "radarr", URL: "http://r:7878", Key: "K"}}},
		"twice":           {Arr: []ArrSource{{Name: "r", Flavour: "radarr", URL: "http://r:1", Key: "K"}, {Name: "r", Flavour: "radarr", URL: "http://r:2", Key: "K"}}},
		"seerr no scheme": {Seerr: &ServiceSource{URL: "seerr:5055", Key: "K"}},
	} {
		if err := src.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func hasGap(p *Plan, topic, subject string) bool {
	return slices.ContainsFunc(p.Gaps, func(g Gap) bool { return g.Topic == topic && g.Subject == subject })
}

func TestBuildMapsTheStack(t *testing.T) {
	t.Parallel()
	fs := newFakeStack(t)
	se := newSettingsEnv(t)
	p, _ := se.plan(t, fs)
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, "the plan", b)
	if !strings.Contains(string(b), `"PROWLARR_KEY"`) || !strings.Contains(string(b), `"TB_KEY"`) {
		t.Error("the plan does not name the keys its records take")
	}

	choices := map[string]string{}
	for _, ix := range p.Indexers {
		choices[ix.Name] = ix.Choice + " " + ix.Key
	}
	want := map[string]string{"The Pirate Bay": "existing prowlarr-public", "1337x": "prowlarr prowlarr-1337x",
		"Private-1": "prowlarr prowlarr-private-1", "Usenet-1": "skip ", "Nyaa.si": "prowlarr prowlarr-nyaa-si"}
	if !reflect.DeepEqual(choices, want) {
		t.Errorf("indexers %v", choices)
	}
	ix := p.Step("indexer/prowlarr-1337x")
	var cfg policy.Indexer
	if ix == nil || decodeConfig(ix.Payload.Config, &cfg) != nil || cfg.Kind != "torznab" ||
		cfg.URL != "http://prowlarr:9696/10/api" || cfg.Privacy != "public" || cfg.Budget != "prowlarr" || ix.Payload.Secret != "PROWLARR_KEY" {
		t.Errorf("1337x step %+v config %+v", ix, cfg)
	}
	if p.Step("budget/prowlarr") != nil {
		t.Error("planned the prowlarr budget the policy has")
	}
	if p.Step("provider/rd") != nil || p.Step("provider/tb-migrated") == nil {
		t.Error("providers: rd is reused, tb-migrated added")
	}

	versions := map[string]VersionPlan{}
	for _, v := range p.Versions() {
		versions[v.Instance] = v
	}
	movie, anime := versions["radarr_uhd"], versions["sonarr_anime"]
	// The private Prowlarr indexer goes into the version like the public ones: privacy is a label.
	if movie.Key != "movie-uhd" || movie.Library != se.media+"/movies-uhd" || movie.View != "radarr" ||
		!reflect.DeepEqual(movie.Indexers, []string{"prowlarr-public", "prowlarr-1337x", "prowlarr-private-1"}) || movie.Client != "decypharr" ||
		movie.Recyclarr == nil || movie.Recyclarr.Preset != "fd161a61e3ab826d3a22d53f935696dd" {
		t.Errorf("movie version %+v", movie)
	}
	// anime-tv is the example policy's: the instance gets a key of its own.
	if anime.Key != "anime-tv-2" || anime.Library != se.media+"/anime" ||
		!reflect.DeepEqual(anime.Indexers, []string{"prowlarr-nyaa-si"}) || anime.View != "sonarr" {
		t.Errorf("anime version %+v", anime)
	}
	for _, v := range p.Versions() {
		var vc policy.VersionPolicy
		if err := decodeConfig(p.Step("version/"+v.Key).Payload.Config, &vc); err != nil {
			t.Fatal(err)
		}
		if vc.Mode != policy.ModeShadow || vc.Grab || vc.Storage != policy.StorageDebrid || vc.Import != "symlink" {
			t.Errorf("version %s config %+v", v.Key, vc)
		}
	}
	for _, s := range p.Steps {
		if s.Automatic && (s.Status != StatusPending || s.Blocked != "") {
			t.Errorf("step %s: %s %s", s.ID, s.Status, s.Blocked)
		}
		if !s.Automatic && s.Status != StatusManual {
			t.Errorf("manual step %s: %s", s.ID, s.Status)
		}
		if s.What == "" || s.Why == "" || s.Undo == "" {
			t.Errorf("step %s does not say what, why and how to undo", s.ID)
		}
	}
	view := p.Step("view/radarr/movie-uhd")
	if view == nil || !reflect.DeepEqual(view.Payload.Profiles, map[string][]string{"Remux + WEB 2160p": {"movie-uhd"}}) {
		t.Errorf("view step %+v", view)
	}
	for _, id := range []string{"import/radarr_uhd", "import/sonarr_anime", "follow/movie-uhd", "seerr/radarr/0", "seerr/sonarr/0"} {
		if s := p.Step(id); s == nil || s.Automatic {
			t.Errorf("manual step %s: %+v", id, s)
		}
	}
	if s := p.Step("seerr/radarr/0"); s == nil || !strings.Contains(s.What, "URL base /radarr") {
		t.Errorf("seerr step %+v", s)
	}
	for _, g := range [][2]string{{GapUsenet, "indexer:Usenet-1"},
		{GapUsenet, "radarr_uhd/nzbget"}, {GapPaths, "version:movie-uhd"}, {GapSecret, "indexer:sonarr_anime/AnimeTosho"},
		{GapLocal, "version:anime-tv-2"}, {GapUnsupported, "prowlarr:Lidarr"}, {GapUnsupported, "seerr:Radarr old"}} {
		if !hasGap(p, g[0], g[1]) {
			t.Errorf("no %s gap for %s", g[0], g[1])
		}
	}
}

func TestBuildFlagsALibraryOutsideTheArea(t *testing.T) {
	t.Parallel()
	fs := newFakeStack(t)
	se := newSettingsEnv(t)
	inv := fs.inspect(t)
	p, err := Build(BuildInput{Inventory: inv, Policy: se.head(t).Policy, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(p.Gaps, func(g Gap) bool {
		return g.Subject == "version:movie-uhd" && strings.Contains(g.What, "outside the library area")
	}) {
		t.Errorf("no library area gap: %+v", p.Gaps)
	}
	if !hasGap(p, GapPrereq, "providers") == (len(se.head(t).Policy.Providers) == 0) {
		t.Errorf("provider gap with %d providers", len(se.head(t).Policy.Providers))
	}
}

func TestPlanRoundTrip(t *testing.T) {
	t.Parallel()
	fs := newFakeStack(t)
	se := newSettingsEnv(t)
	p, inv := se.plan(t, fs)
	if p.ID == 0 {
		t.Fatal("no id")
	}
	got, gotInv, err := Load(context.Background(), se.st.Reader(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(p)
	b, _ := json.Marshal(got)
	if string(a) != string(b) {
		t.Errorf("plan changed on the round trip:\n%s\n%s", a, b)
	}
	a, _ = json.Marshal(inv.WithoutRaw())
	b, _ = json.Marshal(gotInv)
	if string(a) != string(b) {
		t.Error("inventory changed on the round trip")
	}
	if gotInv.Arr[0].Raw != nil {
		t.Error("the saved inventory kept the raw arr bodies")
	}
	list, err := List(context.Background(), se.st.Reader())
	if err != nil || len(list) != 1 || list[0].ID != p.ID || list[0].Steps[StatusPending] != 10 || list[0].Steps[StatusManual] != 6 {
		t.Fatalf("list %+v %v", list, err)
	}
	if _, _, err := Load(context.Background(), se.st.Reader(), p.ID+1); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func applyOpts() ApplyOptions {
	return ApplyOptions{Keys: testKeys(), Now: func() time.Time { return testNow }}
}

// publicIndexerSteps are the fake stack's new public Prowlarr feeds; they apply without a confirmation.
var publicIndexerSteps = []string{"indexer/prowlarr-1337x", "indexer/prowlarr-nyaa-si"}

// confirmPublicIndexers checks that no public indexer step stopped for a confirmation, then applies the rest of the
// plan.
func confirmPublicIndexers(t *testing.T, se *settingsEnv, set Settings, id int64, calls *int, p *Plan) *Plan {
	t.Helper()
	for _, sid := range publicIndexerSteps {
		if s := p.Step(sid); s.Status == StatusConfirmation {
			t.Fatalf("step %s stopped for a confirmation: %+v", sid, s)
		}
	}
	got, err := Apply(context.Background(), se.st, id, set, se.checkpointer(calls), applyOpts())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// dumpPlans is every stored plan row.
func dumpPlans(t *testing.T, se *settingsEnv) []byte {
	t.Helper()
	rows, err := se.st.Reader().Query(`SELECT name, inventory, plan FROM migration_plan`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []byte
	for rows.Next() {
		var a, b, c string
		if err := rows.Scan(&a, &b, &c); err != nil {
			t.Fatal(err)
		}
		out = append(out, a+b+c...)
	}
	return out
}

func TestApplyLandsDisabledInShadowAndIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := newFakeStack(t)
	se := newSettingsEnv(t)
	se.mkdirs(t)
	p, _ := se.plan(t, fs)
	before := se.head(t)
	requests := len(fs.requests())
	calls := 0
	got, err := Apply(ctx, se.st, p.ID, se.coord, se.checkpointer(&calls), applyOpts())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got.Steps {
		if s.Automatic && s.Status != StatusDone {
			t.Errorf("step %s: %s %s", s.ID, s.Status, s.Error)
		}
	}
	got = confirmPublicIndexers(t, se, se.coord, p.ID, &calls, got)
	if len(fs.requests()) != requests {
		t.Error("apply contacted the old services")
	}
	for _, s := range got.Steps {
		if s.Automatic && s.Status != StatusDone {
			t.Errorf("step %s: %s %s", s.ID, s.Status, s.Error)
		}
	}
	if calls != len(got.Checkpoints) || got.Checkpoints[0].Generation != before.Generation {
		t.Fatalf("checkpoints %d %+v", calls, got.Checkpoints)
	}
	head := se.head(t)
	for _, ix := range head.Records.Indexers {
		if strings.HasPrefix(ix.Key, "prowlarr-") && ix.Key != "prowlarr-public" && ix.Enabled {
			t.Errorf("indexer %s landed enabled", ix.Key)
		}
	}
	found := false
	for _, pr := range head.Records.Providers {
		if pr.Key == "tb-migrated" {
			found = true
			if pr.Enabled {
				t.Error("provider landed enabled")
			}
		}
	}
	if !found {
		t.Error("provider tb-migrated not added")
	}
	for _, k := range []string{"movie-uhd", "anime-tv-2"} {
		v := head.Policy.Versions[k]
		if v == nil || v.Mode != policy.ModeShadow || v.Grab {
			t.Errorf("version %s: %+v", k, v)
		}
	}
	if v := head.Policy.Views["radarr"]; !slices.Contains(v.Versions, "movie-uhd") ||
		!slices.Contains(v.Profiles["Remux + WEB 2160p"], "movie-uhd") || !slices.Contains(v.Versions, "movie-remux") {
		t.Errorf("radarr view %+v", v)
	}
	if head.Policy.Router.Enabled != before.Policy.Router.Enabled {
		t.Error("the router switch moved")
	}
	assertNoSecrets(t, "the stored plans", dumpPlans(t, se))

	// Again: nothing to run, no checkpoint, no write.
	taken := calls
	again, err := Apply(ctx, se.st, p.ID, se.coord, se.checkpointer(&calls), applyOpts())
	if err != nil || calls != taken || se.head(t).Generation != head.Generation || len(again.Checkpoints) != taken {
		t.Fatalf("second apply: %v, %d checkpoints, generation %d -> %d", err, calls, head.Generation, se.head(t).Generation)
	}
	// A plan whose steps were reset finds its records in place: done, nothing written.
	for i := range again.Steps {
		if again.Steps[i].Automatic {
			again.Steps[i].Status = StatusPending
		}
	}
	if err := update(ctx, se.st, again); err != nil {
		t.Fatal(err)
	}
	third, err := Apply(ctx, se.st, p.ID, se.coord, se.checkpointer(&calls), applyOpts())
	if err != nil {
		t.Fatal(err)
	}
	if se.head(t).Generation != head.Generation {
		t.Error("re-applying wrote settings")
	}
	for _, s := range third.Steps {
		if s.Automatic && (s.Status != StatusDone || !slices.Contains(s.Notes, applyNote+"already in the settings, nothing written")) {
			t.Errorf("step %s: %s %v", s.ID, s.Status, s.Notes)
		}
	}
}

func TestApplyFailedStepKeepsTheCheckpointAndResumes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := newFakeStack(t)
	se := newSettingsEnv(t)
	p, _ := se.plan(t, fs) // the libraries do not exist yet
	before := se.head(t)
	calls := 0
	got, err := Apply(ctx, se.st, p.ID, se.coord, se.checkpointer(&calls), applyOpts())
	if err != nil {
		t.Fatal(err)
	}
	if s := got.Step("indexer/prowlarr-1337x"); s.Status != StatusDone {
		t.Errorf("indexer step %+v", s)
	}
	stored, _, err := Load(ctx, se.st.Reader(), p.ID)
	if err != nil || len(stored.Checkpoints) != 1 {
		t.Fatalf("stored checkpoints %+v %v", stored, err)
	}
	cp := stored.Checkpoints[0]
	if cp.Generation != before.Generation {
		t.Errorf("checkpoint generation %d, settings were at %d", cp.Generation, before.Generation)
	}
	// The checkpoint is the settings as they were before apply.
	b, err := os.ReadFile(cp.Settings)
	if err != nil {
		t.Fatal(err)
	}
	exported, _ := before.Export()
	if string(b) != string(exported) {
		t.Error("the checkpoint is not the settings before apply")
	}
	if strings.Contains(string(b), "movie-uhd") || strings.Contains(string(b), "prowlarr-1337x") {
		t.Error("the checkpoint holds what apply added")
	}

	// The indexers landed; the version still fails on its missing library, and every apply that ran a step
	// recorded its checkpoint.
	got = confirmPublicIndexers(t, se, se.coord, p.ID, &calls, got)
	if s := got.Step("version/movie-uhd"); s.Status != StatusFailed || !strings.Contains(s.Error, "directory must exist") {
		t.Errorf("version step %+v", s)
	}
	if s := got.Step("view/radarr/movie-uhd"); s.Status != StatusBlocked || s.Error != "waits for version/movie-uhd" {
		t.Errorf("view step %+v", s)
	}
	if s := got.Step("indexer/prowlarr-1337x"); s.Status != StatusDone {
		t.Errorf("indexer step %+v", s)
	}
	if stored, _, err = Load(ctx, se.st.Reader(), p.ID); err != nil || len(stored.Checkpoints) != calls ||
		stored.Checkpoints[0].Generation != before.Generation {
		t.Fatalf("stored checkpoints %+v %v", stored, err)
	}

	se.mkdirs(t)
	taken := calls
	got, err = Apply(ctx, se.st, p.ID, se.coord, se.checkpointer(&calls), applyOpts())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got.Steps {
		if s.Automatic && s.Status != StatusDone {
			t.Errorf("resumed step %s: %s %s", s.ID, s.Status, s.Error)
		}
	}
	if calls != taken+1 || len(got.Checkpoints) != calls {
		t.Errorf("%d checkpoints taken, %d recorded", calls, len(got.Checkpoints))
	}
}

func TestApplyWithoutACheckpointChangesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := newFakeStack(t)
	se := newSettingsEnv(t)
	se.mkdirs(t)
	p, _ := se.plan(t, fs)
	gen := se.head(t).Generation
	failing := func(context.Context) (Checkpoint, error) { return Checkpoint{}, errors.New("disk full") }
	if _, err := Apply(ctx, se.st, p.ID, se.coord, failing, applyOpts()); err == nil || !strings.Contains(err.Error(), "nothing was applied") {
		t.Fatalf("err %v", err)
	}
	if _, err := Apply(ctx, se.st, p.ID, se.coord, nil, applyOpts()); err == nil {
		t.Fatal("applied without a checkpointer")
	}
	stored, _, _ := Load(ctx, se.st.Reader(), p.ID)
	if se.head(t).Generation != gen || len(stored.Checkpoints) != 0 {
		t.Fatal("settings or plan changed")
	}
	for _, s := range stored.Steps {
		if s.Automatic && s.Status != StatusPending {
			t.Errorf("step %s: %s", s.ID, s.Status)
		}
	}
}

// confirming asks for a confirmation of the provider step, as the settings do for a write-enabling change.
type confirming struct {
	Settings
	tokens []string
	done   bool
}

func (c *confirming) SaveAndApply(ctx context.Context, req settings.SaveRequest) (*settings.Snapshot, error) {
	for _, p := range req.Records.Providers {
		if p.Key == "tb-migrated" && !c.done {
			c.tokens = append(c.tokens, req.ConfirmationToken)
			c.done = req.ConfirmationToken == "tok-1"
			if !c.done {
				return nil, &settings.ConfirmationError{Confirmation: &settings.Confirmation{Token: "tok-1",
					Effects: []settings.ConfirmationEffect{{}}}}
			}
		}
	}
	if req.ConfirmationToken == "tok-1" {
		req.ConfirmationToken = "" // the token is this fake's, not the coordinator's
	}
	return c.Settings.SaveAndApply(ctx, req)
}

func TestApplySendsAConfirmationOnlyWhenTold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := newFakeStack(t)
	se := newSettingsEnv(t)
	se.mkdirs(t)
	p, _ := se.plan(t, fs)
	set := &confirming{Settings: se.coord}
	calls := 0
	got, err := Apply(ctx, se.st, p.ID, set, se.checkpointer(&calls), applyOpts())
	if err != nil {
		t.Fatal(err)
	}
	s := got.Step("provider/tb-migrated")
	if s.Status != StatusConfirmation || s.Confirmation == nil || s.Confirmation.Token != "tok-1" {
		t.Fatalf("provider step %+v", s)
	}
	if v := got.Step("version/movie-uhd"); v.Status != StatusBlocked {
		t.Errorf("version step went ahead without its provider: %+v", v)
	}
	if _, err := Apply(ctx, se.st, p.ID, set, se.checkpointer(&calls), applyOpts()); err != nil {
		t.Fatal(err)
	}
	o := applyOpts()
	o.Confirm = []string{"provider/tb-migrated"}
	got, err = Apply(ctx, se.st, p.ID, set, se.checkpointer(&calls), o)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(set.tokens, []string{"", "", "tok-1"}) {
		t.Errorf("tokens sent %q", set.tokens)
	}
	got = confirmPublicIndexers(t, se, set, p.ID, &calls, got)
	for _, s := range got.Steps {
		if s.Automatic && s.Status != StatusDone {
			t.Errorf("step %s: %s %s", s.ID, s.Status, s.Error)
		}
	}
}

func TestApplyRefusesGrabAndManualSteps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := newFakeStack(t)
	se := newSettingsEnv(t)
	se.mkdirs(t)
	p, _ := se.plan(t, fs)
	calls := 0
	o := applyOpts()
	o.Steps = []string{"import/radarr_uhd"}
	if _, err := Apply(ctx, se.st, p.ID, se.coord, se.checkpointer(&calls), o); err == nil {
		t.Fatal("ran a manual step")
	}
	o.Steps = []string{"nope"}
	if _, err := Apply(ctx, se.st, p.ID, se.coord, se.checkpointer(&calls), o); err == nil {
		t.Fatal("ran an unknown step")
	}
	if calls != 0 {
		t.Fatal("checkpoint taken for a refused apply")
	}
	// A stored plan edited to grab, or to go active, is refused whatever the settings would say.
	for _, edit := range []func(*policy.VersionPolicy){
		func(v *policy.VersionPolicy) { v.Grab = true },
		func(v *policy.VersionPolicy) { v.Mode = policy.ModeActive },
	} {
		s := p.Step("version/movie-uhd")
		var v policy.VersionPolicy
		if err := decodeConfig(s.Payload.Config, &v); err != nil {
			t.Fatal(err)
		}
		edit(&v)
		s.Payload.Config = mustConfig(v)
		if err := update(ctx, se.st, p); err != nil {
			t.Fatal(err)
		}
		got, err := Apply(ctx, se.st, p.ID, se.coord, se.checkpointer(&calls), applyOpts())
		if err != nil {
			t.Fatal(err)
		}
		got = confirmPublicIndexers(t, se, se.coord, p.ID, &calls, got)
		if s := got.Step("version/movie-uhd"); s.Status != StatusFailed || !strings.HasPrefix(s.Error, "refused") {
			t.Errorf("edited version step %+v", s)
		}
		if se.head(t).Policy.Versions["movie-uhd"] != nil {
			t.Fatal("the edited version was written")
		}
		p = got
	}
}

// failing is a settings coordinator whose saves fail with an error carrying the credential.
type failing struct{ Settings }

func (failing) SaveAndApply(context.Context, settings.SaveRequest) (*settings.Snapshot, error) {
	return nil, errors.New("provider check: 401 for key " + torboxKey)
}

func TestApplyKeepsCredentialsOutOfErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := newFakeStack(t)
	se := newSettingsEnv(t)
	se.mkdirs(t)
	p, _ := se.plan(t, fs)
	calls := 0
	o := applyOpts()
	o.Steps = []string{"provider/tb-migrated"}
	got, err := Apply(ctx, se.st, p.ID, failing{se.coord}, se.checkpointer(&calls), o)
	if err != nil {
		t.Fatal(err)
	}
	if s := got.Step("provider/tb-migrated"); s.Status != StatusFailed || !strings.Contains(s.Error, "***") {
		t.Errorf("step %+v", s)
	}
	b, _ := json.Marshal(got)
	assertNoSecrets(t, "the plan after a failed step", b)
	assertNoSecrets(t, "the stored plans", dumpPlans(t, se))
}

func TestVerifyReportsReadinessAndCutover(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := newFakeStack(t)
	se := newSettingsEnv(t)
	se.mkdirs(t)
	p, _ := se.plan(t, fs)
	calls := 0
	got, err := Apply(ctx, se.st, p.ID, se.coord, se.checkpointer(&calls), applyOpts())
	if err != nil {
		t.Fatal(err)
	}
	confirmPublicIndexers(t, se, se.coord, p.ID, &calls, got)
	gen := se.head(t).Generation
	pol := func(ctx context.Context) (*policy.Policy, error) {
		cp, err := se.head(t).Policy.Clone()
		if err != nil {
			return nil, err
		}
		cp.Import.Arr["radarr_uhd"] = &policy.ArrSource{Flavour: "radarr", URL: "http://radarr-uhd:7878",
			APIKeyEnv: "THEATARR_ARR_RADARR_UHD_KEY", Version: "movie-uhd"}
		cp.Versions["movie-uhd-theatarr"] = &policy.VersionPolicy{Mode: policy.ModeShadow, Follows: "movie-uhd"}
		return cp, nil
	}
	var asked []string
	v := Verifier{Policy: pol, Golive: func(_ context.Context, version string) (*golive.Report, error) {
		asked = append(asked, version)
		st := golive.Pass
		if version == "anime-tv-2" {
			st = golive.Fail
		}
		return &golive.Report{Version: version, Results: []golive.Result{{Section: golive.SecHealth, Status: st}}}, nil
	}, Adopt: func(_ context.Context, version string) (*library.AdoptReport, error) {
		asked = append(asked, "adopt "+version)
		return &library.AdoptReport{Files: 3, Linked: 2, Problems: []library.AdoptProblem{{}}}, nil
	}, Refresh: func(_ context.Context, version string) (*library.RefreshReport, error) {
		asked = append(asked, "refresh "+version)
		return &library.RefreshReport{Links: 2, Current: 2}, nil
	}}
	rep, err := Verify(ctx, se.st, p.ID, v, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if se.head(t).Generation != gen || len(fs.requests()) != 38 {
		t.Error("verify wrote settings or called the old services")
	}
	if len(rep.Pending) != 0 || rep.Rollback == nil || len(rep.Versions) != 2 || !reflect.DeepEqual(asked, []string{"anime-tv-2", "movie-uhd", "adopt movie-uhd-theatarr", "refresh movie-uhd-theatarr"}) {
		t.Fatalf("report %+v asked %v", rep, asked)
	}
	byKey := map[string]VersionReadiness{}
	for _, vr := range rep.Versions {
		byKey[vr.Key] = vr
	}
	movie, anime := byKey["movie-uhd"], byKey["anime-tv-2"]
	if !movie.Ready {
		t.Errorf("movie-uhd not ready: %+v", movie.Checks)
	}
	if anime.Ready {
		t.Error("anime-tv-2 ready without an import and with a failing golive check")
	}
	status := map[string]golive.Status{}
	for _, c := range anime.Checks {
		status[c.Name] = c.Status
	}
	if status["import"] != golive.Fail || status["golive/"+golive.SecHealth] != golive.Fail || status["view"] != golive.Pass {
		t.Errorf("anime checks %+v", anime.Checks)
	}
	status = map[string]golive.Status{}
	for _, c := range movie.Checks {
		status[c.Name] = c.Status
	}
	if status["adopt/movie-uhd-theatarr"] != golive.Warn || status["refresh/movie-uhd-theatarr"] != golive.Pass {
		t.Errorf("follower checks %+v", movie.Checks)
	}
	cut := strings.Join(movie.Cutover, "\n")
	for _, want := range []string{"Drain radarr_uhd", `server "Radarr 4K"`, "grab on", "Enable grabbing for movie-uhd only",
		"global router switch"} {
		if !strings.Contains(cut, want) {
			t.Errorf("cutover lacks %q:\n%s", want, cut)
		}
	}
	back := strings.Join(movie.Rollback, "\n")
	if !strings.Contains(back, "backup restore -from "+rep.Rollback.Backup) || !strings.Contains(back, "Turn grab off for movie-uhd") {
		t.Errorf("rollback:\n%s", back)
	}
	b, _ := json.Marshal(rep)
	assertNoSecrets(t, "the verify report", b)
}

func TestVerifyBeforeApply(t *testing.T) {
	t.Parallel()
	fs := newFakeStack(t)
	se := newSettingsEnv(t)
	p, _ := se.plan(t, fs)
	rep, err := Verify(context.Background(), se.st, p.ID, Verifier{Policy: func(context.Context) (*policy.Policy, error) {
		return se.head(t).Policy, nil
	}}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Pending) != 10 || rep.Rollback != nil {
		t.Errorf("pending %v rollback %v", rep.Pending, rep.Rollback)
	}
	for _, vr := range rep.Versions {
		if vr.Ready || vr.Checks[0].Name != "plan" || vr.Checks[0].Status != golive.Fail {
			t.Errorf("%s: %+v", vr.Key, vr.Checks)
		}
	}
}

// A debrid version needs no download client: with no decypharr client the plan has no prerequisite gap, the
// version step is not blocked and applies with an empty client, and the manual steps point at the settings
// database, not the policy files.
func TestPlanWithoutADecypharrClient(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := newFakeStack(t)
	se := newSettingsEnv(t)
	se.mkdirs(t)
	pol, err := se.head(t).Policy.Clone()
	if err != nil {
		t.Fatal(err)
	}
	for k, c := range pol.Clients {
		if c.Kind == "decypharr" {
			delete(pol.Clients, k)
		}
	}
	inv := fs.inspect(t)
	p, err := Build(BuildInput{Inventory: inv, Options: testOptions(se.media), Policy: pol, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if hasGap(p, GapPrereq, "clients") {
		t.Error("a missing decypharr client is a gap")
	}
	for _, v := range p.Versions() {
		s := p.Step("version/" + v.Key)
		var vc policy.VersionPolicy
		if err := decodeConfig(s.Payload.Config, &vc); err != nil {
			t.Fatal(err)
		}
		if s.Blocked != "" || v.Client != "" || vc.Client != "" {
			t.Errorf("version %s: blocked %q client %q/%q", v.Key, s.Blocked, v.Client, vc.Client)
		}
	}
	for _, s := range p.Steps {
		if strings.Contains(s.What, ".yaml") || strings.Contains(s.What, "and restart") || strings.Contains(s.Undo, "and restart") {
			t.Errorf("step %s still sends the user to the policy files: %s / %s", s.ID, s.What, s.Undo)
		}
	}
	if s := p.Step("import/radarr_uhd"); s == nil || !strings.Contains(s.What, "Advanced › Sonarr/Radarr migration") ||
		!strings.Contains(s.What, "arr.radarr_uhd.api_key") {
		t.Errorf("import step %+v", s)
	}
	if err := Save(ctx, se.st, p, inv); err != nil {
		t.Fatal(err)
	}
	calls := 0
	got, err := Apply(ctx, se.st, p.ID, se.coord, se.checkpointer(&calls), applyOpts())
	if err != nil {
		t.Fatal(err)
	}
	got = confirmPublicIndexers(t, se, se.coord, p.ID, &calls, got)
	for _, v := range got.Versions() {
		if s := got.Step("version/" + v.Key); s.Status != StatusDone {
			t.Errorf("version %s: %s %s", v.Key, s.Status, s.Error)
		}
	}
}

// An initialized database may legitimately own no profiles yet.
func TestApplyFirstQualityProfileOnEmptyInstall(t *testing.T) {
	t.Parallel()
	for _, initialized := range []bool{false, true} {
		t.Run(fmt.Sprint(initialized), func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if _, err := st.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			base, err := policy.ReadOrEmpty(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			ss, err := settings.New(st)
			if err != nil {
				t.Fatal(err)
			}
			snap, err := ss.Bootstrap(ctx, base, settings.ImportRequest{Actor: "test"})
			if err != nil {
				t.Fatal(err)
			}
			if initialized {
				snap, err = ss.BootstrapQuality(ctx, base, "test")
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(snap.Records.Profiles) != 0 {
				t.Fatal("expected empty install")
			}
			coord, err := settings.NewRuntimeCoordinator(ss, base, snap, nil, func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) { return nil, nil })
			if err != nil {
				t.Fatal(err)
			}
			se := &settingsEnv{st: st, coord: coord, dir: t.TempDir(), media: t.TempDir()}
			plan, _ := se.plan(t, newFakeStack(t))
			var qualityID string
			for _, step := range plan.Steps {
				if step.Kind == KindQuality {
					qualityID = step.ID
					break
				}
			}
			if qualityID == "" {
				t.Fatal("no quality step")
			}
			calls := 0
			got, err := Apply(ctx, st, plan.ID, coord, se.checkpointer(&calls), ApplyOptions{Steps: []string{qualityID}})
			if err != nil {
				t.Fatal(err)
			}
			step := got.Step(qualityID)
			if initialized {
				if step.Status != StatusDone {
					t.Fatalf("first profile: %s: %s", step.Status, step.Error)
				}
				if len(se.head(t).Records.Profiles) != 1 {
					t.Fatal("first profile was not saved")
				}
			} else if step.Status != StatusFailed || !strings.Contains(step.Error, "do not manage quality") {
				t.Fatalf("uninitialized quality accepted: %+v", step)
			}
		})
	}
}
