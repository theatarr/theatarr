package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/golive"
	"github.com/theatarr/theatarr/internal/migration"
	"github.com/theatarr/theatarr/internal/uipages"
	"github.com/theatarr/theatarr/internal/web"
	"go.yaml.in/yaml/v3"
)

// The guided migration from an arr stack (docs/migration.md). inspect runs here and reads the old services with
// GET only; plan, list, show, apply and verify talk to the running theatarr serve, whose settings coordinator does
// every write.
const migrationUsage = `usage:
  theatarr migrate inspect [source flags] [-json [-raw]] [-o FILE]
      read the old services (GET only) and print what they hold
  theatarr migrate plan [source flags | -inventory FILE] [plan flags] [service flags] [-json]
      inspect, map to Theatarr settings and save the plan in Theatarr; prints the plan with its id
  theatarr migrate list [service flags] [-json]
  theatarr migrate show -plan ID [service flags] [-json]
  theatarr migrate apply -plan ID [-step ID]... [-env-file FILE] [service flags] [-json]
      run the plan's automatic steps after a rollback checkpoint; everything lands disabled or in shadow. A step
      that needs confirmation stays open: a signed-in user applies it in the UI (Advanced › Sonarr/Radarr migration)
  theatarr migrate verify -plan ID [service flags] [-json]
      per version: ready to hand over or not, the manual cutover steps and the rollback
  theatarr migrate seerr-ids -seerr URL,KEYNAME [-radarr-view PATH] [-sonarr-view PATH] [-env-file FILE] [service flags] [-json]
      read Seerr's media (GET only) and resolve each stored Radarr/Sonarr id through the view it reaches once
      Seerr points at Theatarr: ok, wrong title or unresolved (docs/design/arrview.md); exits 1 on a wrong title

  API keys are named, never given on the command line: a source's KEYNAME is looked up in the environment (or
  -env-file). Keys are never printed, saved or sent to Theatarr, except the ones apply's records take.

source flags:
  -arr name=flavour,URL,KEYNAME   a Sonarr or Radarr instance (repeat)     -prowlarr URL,KEYNAME
  -seerr URL,KEYNAME              Seerr, Overseerr or Jellyseerr          -recyclarr FILE   a recyclarr.yml
  -sources FILE                   the same as YAML or JSON (docs/migration.md)
  -env-file FILE                  KEY=VALUE lines; variables already set win

plan flags:
  -name NAME  -native NAME,...|all  -skip NAME,...  -provider kind[:key]=KEYNAME  -path-map FROM=TO  -prowlarr-url URL

service flags:
  -service URL (default http://127.0.0.1:7700)  -api-key-env NAME (default THEATARR_API_KEY: an admin's API key)
`

// migrationDialer opens inspect's GET-only clients; tests swap in one without the 400ms pacing.
var migrationDialer = migration.DefaultDialer

var migrationCommands = []string{"inspect", "plan", "list", "show", "apply", "verify", "seerr-ids"}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, " ") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

type migrationFlags struct {
	fs                               *flag.FlagSet
	arrs, providers, paths, steps    listFlag
	prowlarr, seerr, recyclarr, srcs *string
	envFile, inventory, out          *string
	name, native, skip, prowlarrURL  *string
	service, apiKeyEnv               *string
	radarrView, sonarrView           *string
	plan                             *int64
	asJSON, raw                      *bool
}

func newMigrationFlags(name string) *migrationFlags {
	f := &migrationFlags{fs: flag.NewFlagSet("migrate "+name, flag.ContinueOnError)}
	fs := f.fs
	fs.Usage = func() { fmt.Fprint(fs.Output(), migrationUsage) }
	fs.Var(&f.arrs, "arr", "name=flavour,URL,KEYNAME (repeat)")
	f.prowlarr = fs.String("prowlarr", "", "URL,KEYNAME")
	f.seerr = fs.String("seerr", "", "URL,KEYNAME")
	f.recyclarr = fs.String("recyclarr", "", "recyclarr.yml to read")
	f.srcs = fs.String("sources", "", "sources file (YAML or JSON)")
	f.envFile = fs.String("env-file", "", "KEY=VALUE secrets; variables already set win")
	f.inventory = fs.String("inventory", "", "plan: an inventory from inspect -json -raw instead of the source flags")
	f.out = fs.String("o", "", "inspect: write the output here instead of stdout")
	f.name = fs.String("name", "", "plan: a name for the plan")
	f.native = fs.String("native", "", "plan: Prowlarr indexers to run natively (comma-separated, or all)")
	f.skip = fs.String("skip", "", "plan: Prowlarr indexers to leave out (comma-separated)")
	fs.Var(&f.providers, "provider", "plan: kind[:key]=KEYNAME, a debrid account to add disabled (repeat)")
	fs.Var(&f.paths, "path-map", "plan: FROM=TO, an arr path as Theatarr sees it (repeat)")
	f.prowlarrURL = fs.String("prowlarr-url", "", "plan: Prowlarr's address as Theatarr reaches it")
	f.plan = fs.Int64("plan", 0, "the plan id")
	fs.Var(&f.steps, "step", "apply: only this step (repeat)")
	f.service = fs.String("service", "http://127.0.0.1:7700", "the running theatarr serve")
	f.apiKeyEnv = fs.String("api-key-env", "THEATARR_API_KEY", "environment variable holding an admin API key")
	f.radarrView = fs.String("radarr-view", "/radarr", "seerr-ids: the path of the view Seerr's Radarr servers will use")
	f.sonarrView = fs.String("sonarr-view", "/sonarr", "seerr-ids: the path of the view Seerr's Sonarr servers will use")
	f.asJSON = fs.Bool("json", false, "print JSON")
	f.raw = fs.Bool("raw", false, "inspect -json: keep the arrs' raw profile, format and naming bodies (plan -inventory needs them)")
	return f
}

func isMigrationCommand(args []string) bool {
	return len(args) > 0 && slices.Contains(migrationCommands, args[0])
}

func runMigration(args []string, out io.Writer) error {
	f := newMigrationFlags(args[0])
	if err := f.fs.Parse(args[1:]); err != nil {
		return err
	}
	if f.fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", f.fs.Args())
	}
	if *f.envFile != "" {
		if err := loadEnvFile(*f.envFile); err != nil {
			return err
		}
	}
	ctx := context.Background()
	switch args[0] {
	case "inspect":
		return f.inspect(ctx, out)
	case "plan":
		return f.makePlan(ctx, out)
	case "list":
		return f.list(ctx, out)
	case "show":
		return f.show(ctx, out)
	case "apply":
		return f.apply(ctx, out)
	case "seerr-ids":
		return f.seerrIDs(ctx, out)
	default:
		return f.verify(ctx, out)
	}
}

// sources reads the source flags (and -sources).
func (f *migrationFlags) sources() (migration.Sources, error) {
	var src migration.Sources
	if *f.srcs != "" {
		b, err := os.ReadFile(*f.srcs)
		if err != nil {
			return src, err
		}
		dec := yaml.NewDecoder(bytes.NewReader(b))
		dec.KnownFields(true)
		if err := dec.Decode(&src); err != nil {
			return src, fmt.Errorf("%s: %w", *f.srcs, err)
		}
	}
	for _, a := range f.arrs {
		name, rest, ok := strings.Cut(a, "=")
		parts := strings.Split(rest, ",")
		if !ok || len(parts) != 3 {
			return src, fmt.Errorf("-arr %q: want name=flavour,URL,KEYNAME", a)
		}
		src.Arr = append(src.Arr, migration.ArrSource{Name: name, Flavour: parts[0], URL: parts[1], Key: parts[2]})
	}
	service := func(flagName, v string) (*migration.ServiceSource, error) {
		u, k, ok := strings.Cut(v, ",")
		if !ok {
			return nil, fmt.Errorf("-%s %q: want URL,KEYNAME", flagName, v)
		}
		return &migration.ServiceSource{URL: u, Key: k}, nil
	}
	var err error
	if *f.prowlarr != "" {
		if src.Prowlarr, err = service("prowlarr", *f.prowlarr); err != nil {
			return src, err
		}
	}
	if *f.seerr != "" {
		if src.Seerr, err = service("seerr", *f.seerr); err != nil {
			return src, err
		}
	}
	if *f.recyclarr != "" {
		src.Recyclarr = *f.recyclarr
	}
	return src, src.Validate()
}

// envKeys resolves key names in the environment.
func envKeys(name string) (string, bool) {
	v, ok := os.LookupEnv(name)
	return v, ok && v != ""
}

func (f *migrationFlags) inspectNow(ctx context.Context) (*migration.Inventory, error) {
	src, err := f.sources()
	if err != nil {
		return nil, err
	}
	return migration.Inspect(ctx, src, envKeys, migrationDialer, time.Now())
}

func (f *migrationFlags) inspect(ctx context.Context, out io.Writer) error {
	inv, err := f.inspectNow(ctx)
	if err != nil {
		return err
	}
	if *f.out != "" {
		file, err := os.OpenFile(*f.out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		defer file.Close()
		out = file
	}
	if *f.asJSON {
		if !*f.raw {
			inv = inv.WithoutRaw()
		}
		return writeIndented(out, inv)
	}
	printInventory(out, inv)
	return nil
}

func writeIndented(out io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(b))
	return err
}

func printInventory(out io.Writer, inv *migration.Inventory) {
	fmt.Fprintf(out, "inspected %s (schema %d)\n", inv.InspectedAt.Format(time.RFC3339), inv.SchemaVersion)
	for _, a := range inv.Arr {
		if a.Error != "" {
			fmt.Fprintf(out, "\n%s (%s) %s: NOT READ: %s\n", a.Name, a.Flavour, a.URL, a.Error)
			continue
		}
		fmt.Fprintf(out, "\n%s (%s %s) %s\n", a.Name, a.Flavour, a.AppVersion, a.URL)
		fmt.Fprintf(out, "  titles: %d, %d monitored, %d with files", a.Titles.Total, a.Titles.Monitored, a.Titles.WithFiles)
		if a.Titles.Anime > 0 {
			fmt.Fprintf(out, ", %d anime", a.Titles.Anime)
		}
		fmt.Fprintln(out)
		for _, r := range a.RootFolders {
			fmt.Fprintf(out, "  root %s: %d titles\n", r.Path, r.Titles)
		}
		for _, p := range a.QualityProfiles {
			fmt.Fprintf(out, "  profile %q: %d titles\n", p.Name, p.Titles)
		}
		fmt.Fprintf(out, "  %d custom formats, tags %s\n", len(a.CustomFormats), strings.Join(a.Tags, ", "))
		for _, ix := range a.Indexers {
			via := "set in the arr"
			if ix.ProwlarrID > 0 {
				via = fmt.Sprintf("Prowlarr #%d", ix.ProwlarrID)
			}
			fmt.Fprintf(out, "  indexer %s (%s, %s)\n", ix.Name, ix.Protocol, via)
		}
		for _, c := range a.DownloadClients {
			state := "enabled"
			if !c.Enabled {
				state = "disabled"
			}
			bridge := ""
			if c.Bridge != "" {
				bridge = ", bridge " + c.Bridge
			}
			fmt.Fprintf(out, "  client %s (%s, %s, %s%s)\n", c.Name, c.Implementation, c.Protocol, state, bridge)
		}
		for _, n := range a.Notifications {
			triggers := "no triggers"
			if len(n.Triggers) > 0 {
				triggers = "on " + strings.Join(n.Triggers, ", ")
			}
			fmt.Fprintf(out, "  connect %s (%s, %s) %s\n", n.Name, n.Implementation, triggers, orDash(n.Target))
		}
		for _, l := range a.ImportLists {
			state := "enabled"
			if !l.Enabled {
				state = "disabled"
			}
			fmt.Fprintf(out, "  list %s (%s, %s, auto add %t) %s\n", l.Name, l.Implementation, state, l.AutoAdd,
				orDash(l.Source))
		}
		if a.DelayProfiles+a.ReleaseProfiles > 0 || a.Hardlinks {
			fmt.Fprintf(out, "  %d delay and %d release profiles, hardlinks %t\n", a.DelayProfiles, a.ReleaseProfiles, a.Hardlinks)
		}
	}
	if p := inv.Prowlarr; p != nil {
		if p.Error != "" {
			fmt.Fprintf(out, "\nProwlarr %s: NOT READ: %s\n", p.URL, p.Error)
		} else {
			fmt.Fprintf(out, "\nProwlarr %s %s: %d indexers\n", p.AppVersion, p.URL, len(p.Indexers))
			for _, ix := range p.Indexers {
				fmt.Fprintf(out, "  #%d %s (%s, %s, %s)\n", ix.ID, ix.Name, ix.DefinitionName, ix.Protocol, ix.Privacy)
			}
			for _, a := range p.Applications {
				fmt.Fprintf(out, "  syncs to %s (%s, %s) %s\n", a.Name, a.Implementation, a.SyncLevel, orNoInstance(a.Instance))
			}
		}
	}
	if s := inv.Seerr; s != nil {
		if s.Error != "" {
			fmt.Fprintf(out, "\nSeerr %s: NOT READ: %s\n", s.URL, s.Error)
		} else {
			fmt.Fprintf(out, "\nSeerr %s %s\n", s.AppVersion, s.URL)
			for _, srv := range append(append([]migration.SeerrServer(nil), s.Radarr...), s.Sonarr...) {
				fmt.Fprintf(out, "  %s -> %s:%d%s, profile %q, 4K %t, default %t: %s\n", srv.Name, srv.Hostname, srv.Port,
					srv.BaseURL, srv.ActiveProfileName, srv.Is4K, srv.IsDefault, orNoInstance(srv.Instance))
			}
		}
	}
	if r := inv.Recyclarr; r != nil {
		if r.Error != "" {
			fmt.Fprintf(out, "\nRecyclarr %s: NOT READ: %s\n", r.Path, r.Error)
		} else {
			fmt.Fprintf(out, "\nRecyclarr %s\n", r.Path)
			for _, ri := range r.Instances {
				var names []string
				for _, p := range ri.Profiles {
					names = append(names, p.Name)
				}
				fmt.Fprintf(out, "  %s (%s): %s: profiles %s\n", ri.Name, ri.Flavour, orNoInstance(ri.Instance), strings.Join(names, ", "))
			}
		}
	}
}

func orNoInstance(s string) string {
	if s == "" {
		return "no inspected instance"
	}
	return s
}

// planOptions reads the plan flags.
func (f *migrationFlags) planOptions() (migration.PlanOptions, error) {
	o := migration.PlanOptions{Name: *f.name, ProwlarrURL: *f.prowlarrURL, Native: splitList(*f.native),
		Skip: splitList(*f.skip)}
	for _, p := range f.providers {
		kind, secret, ok := strings.Cut(p, "=")
		if !ok {
			return o, fmt.Errorf("-provider %q: want kind[:key]=KEYNAME", p)
		}
		kind, key, _ := strings.Cut(kind, ":")
		o.Providers = append(o.Providers, migration.ProviderChoice{Kind: kind, Key: key, Secret: secret})
	}
	for _, p := range f.paths {
		from, to, ok := strings.Cut(p, "=")
		if !ok || !strings.HasPrefix(from, "/") || !strings.HasPrefix(to, "/") {
			return o, fmt.Errorf("-path-map %q: want /FROM=/TO", p)
		}
		o.PathMap = append(o.PathMap, migration.PathMapping{From: from, To: to})
	}
	return o, nil
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (f *migrationFlags) makePlan(ctx context.Context, out io.Writer) error {
	opts, err := f.planOptions()
	if err != nil {
		return err
	}
	var inv *migration.Inventory
	if *f.inventory != "" {
		b, err := os.ReadFile(*f.inventory)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &inv); err != nil {
			return fmt.Errorf("%s: %w", *f.inventory, err)
		}
	} else if inv, err = f.inspectNow(ctx); err != nil {
		return err
	}
	var view web.MigratePlan
	if err := f.call(ctx, http.MethodPost, "/plans", web.MigratePlanRequest{Inventory: inv, Options: opts}, &view); err != nil {
		return err
	}
	if *f.asJSON {
		return writeIndented(out, view)
	}
	printMigrationPlan(out, view.Plan)
	return nil
}

func (f *migrationFlags) list(ctx context.Context, out io.Writer) error {
	var list []migration.Summary
	if err := f.call(ctx, http.MethodGet, "/plans", nil, &list); err != nil {
		return err
	}
	if *f.asJSON {
		return writeIndented(out, list)
	}
	for _, s := range list {
		var counts []string
		for _, k := range sortedKeys(s.Steps) {
			counts = append(counts, fmt.Sprintf("%d %s", s.Steps[k], k))
		}
		fmt.Fprintf(out, "%d\t%s\t%s\t%s\n", s.ID, s.UpdatedAt.Local().Format("2006-01-02 15:04"), s.Name, strings.Join(counts, ", "))
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (f *migrationFlags) needPlan() (string, error) {
	if *f.plan <= 0 {
		return "", errors.New("give -plan ID (theatarr migrate list)")
	}
	return "/plans/" + strconv.FormatInt(*f.plan, 10), nil
}

func (f *migrationFlags) show(ctx context.Context, out io.Writer) error {
	path, err := f.needPlan()
	if err != nil {
		return err
	}
	var view web.MigratePlan
	if err := f.call(ctx, http.MethodGet, path, nil, &view); err != nil {
		return err
	}
	if *f.asJSON {
		return writeIndented(out, view)
	}
	printMigrationPlan(out, view.Plan)
	return nil
}

func (f *migrationFlags) apply(ctx context.Context, out io.Writer) error {
	path, err := f.needPlan()
	if err != nil {
		return err
	}
	var view web.MigratePlan
	if err := f.call(ctx, http.MethodGet, path, nil, &view); err != nil {
		return err
	}
	// Only the keys the plan's records take are sent, and only those that are set.
	keys := map[string]string{}
	for _, s := range view.Plan.Steps {
		if s.Payload != nil && s.Payload.Secret != "" {
			if v, ok := envKeys(s.Payload.Secret); ok {
				keys[s.Payload.Secret] = v
			}
		}
	}
	var p migration.Plan
	req := web.MigrateApplyRequest{Steps: f.steps, Keys: keys}
	if err := f.call(ctx, http.MethodPost, path+"/apply", req, &p); err != nil {
		return err
	}
	if *f.asJSON {
		if err := writeIndented(out, p); err != nil {
			return err
		}
	} else {
		printMigrationPlan(out, &p)
	}
	var open []string
	for _, s := range p.Steps {
		if s.Automatic && s.Status != migration.StatusDone {
			open = append(open, s.ID+" ("+s.Status+")")
		}
	}
	if len(open) > 0 {
		return fmt.Errorf("%d steps not done: %s", len(open), strings.Join(open, ", "))
	}
	return nil
}

func (f *migrationFlags) verify(ctx context.Context, out io.Writer) error {
	path, err := f.needPlan()
	if err != nil {
		return err
	}
	var rep migration.VerifyReport
	if err := f.call(ctx, http.MethodPost, path+"/verify", nil, &rep); err != nil {
		return err
	}
	if *f.asJSON {
		if err := writeIndented(out, rep); err != nil {
			return err
		}
	} else {
		printVerify(out, &rep)
	}
	var notReady []string
	for _, v := range rep.Versions {
		if !v.Ready {
			notReady = append(notReady, v.Key)
		}
	}
	if len(notReady) > 0 {
		return fmt.Errorf("not ready: %s", strings.Join(notReady, ", "))
	}
	return nil
}

// call sends one request to the service's /migrate API with the admin API key from -api-key-env.
func (f *migrationFlags) call(ctx context.Context, method, path string, body, into any) error {
	c, err := f.serviceClient(ctx)
	if err != nil {
		return err
	}
	defer c.close()
	status, b, err := c.do(ctx, method, web.APIBase+"/migrate"+path, body)
	if err != nil {
		return err
	}
	if status/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return fmt.Errorf("theatarr: %s (%d)", e.Error, status)
		}
		return fmt.Errorf("theatarr: %d %s", status, http.StatusText(status))
	}
	return json.Unmarshal(b, into)
}

// theatarrClient sends requests to the running theatarr serve with the admin API key.
type theatarrClient struct {
	base      string
	key       string
	client    *http.Client
	transport *http.Transport
}

// serviceClient checks -service and -api-key-env and opens a client for them.
func (f *migrationFlags) serviceClient(ctx context.Context) (*theatarrClient, error) {
	key, ok := envKeys(*f.apiKeyEnv)
	if !ok {
		return nil, fmt.Errorf("%s is not set: create an API key as an admin ("+uipages.Users+") and export it", *f.apiKeyEnv)
	}
	base := strings.TrimRight(*f.service, "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("-service %q is not an http(s) URL", *f.service)
	}
	// The admin API key goes to Theatarr on this machine or the LAN only.
	if err := arr.CheckHost(ctx, u.Hostname()); err != nil {
		return nil, fmt.Errorf("-service %s: %w", *f.service, err)
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: mcpLANControl}
	transport := &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second, ForceAttemptHTTP2: true}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &theatarrClient{base: base, key: key, client: client, transport: transport}, nil
}

func (c *theatarrClient) close() { c.transport.CloseIdleConnections() }

// do sends one request to path (from the service root) and returns the status and body.
func (c *theatarrClient) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set(auth.APIKeyHeader, c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("theatarr at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, b, nil
}

func printMigrationPlan(out io.Writer, p *migration.Plan) {
	fmt.Fprintf(out, "plan %d %q (schema %d), updated %s\n", p.ID, p.Name, p.SchemaVersion, p.UpdatedAt.Local().Format(time.RFC3339))
	for _, l := range p.Libraries {
		fmt.Fprintf(out, "\nlibrary %s (%s)\n", l.Name, l.Kind)
		for _, v := range l.Versions {
			existing := ""
			if v.Existing {
				existing = " (exists)"
			}
			fmt.Fprintf(out, "  %s%s <- %s: library %s, profile %s (arr %q), indexers %s, providers %s, client %s",
				v.Key, existing, v.Instance, orDash(v.Library), orDash(v.Profile), v.ArrProfile, strings.Join(v.Indexers, ","),
				strings.Join(v.Providers, ","), orDash(v.Client))
			if v.View != "" {
				fmt.Fprintf(out, ", view %s", v.View)
			}
			fmt.Fprintln(out)
		}
	}
	if len(p.Indexers) > 0 {
		fmt.Fprintln(out, "\nindexers")
		for _, ix := range p.Indexers {
			fmt.Fprintf(out, "  Prowlarr #%d %s: %s %s (%s)\n", ix.ProwlarrID, ix.Name, ix.Choice, ix.Key, ix.Privacy)
		}
	}
	fmt.Fprintln(out, "\nsteps")
	for _, s := range p.Steps {
		rev := "reversible"
		if !s.Reversible {
			rev = "NOT reversible"
		}
		fmt.Fprintf(out, "  [%s] %s: %s (%s)\n", s.Status, s.ID, s.What, rev)
		fmt.Fprintf(out, "      why: %s\n", s.Why)
		if s.Error != "" {
			fmt.Fprintf(out, "      %s\n", s.Error)
		}
		if s.Confirmation != nil {
			for _, e := range s.Confirmation.Effects {
				fmt.Fprintf(out, "      effect: %+v\n", e)
			}
		}
		for _, n := range s.Notes {
			fmt.Fprintf(out, "      note: %s\n", n)
		}
	}
	if len(p.Gaps) > 0 {
		fmt.Fprintln(out, "\nnot migrated automatically")
		for _, g := range p.Gaps {
			fmt.Fprintf(out, "  %s %s: %s; %s\n", g.Topic, g.Subject, g.What, g.Why)
		}
	}
	for _, c := range p.Checkpoints {
		fmt.Fprintf(out, "\ncheckpoint %s: %s (settings generation %d)\n", c.At.Local().Format(time.RFC3339), c.Backup, c.Generation)
	}
}

func printVerify(out io.Writer, rep *migration.VerifyReport) {
	fmt.Fprintf(out, "plan %d, verified %s\n", rep.PlanID, rep.At.Local().Format(time.RFC3339))
	if len(rep.Pending) > 0 {
		fmt.Fprintf(out, "steps not done: %s\n", strings.Join(rep.Pending, ", "))
	}
	for _, v := range rep.Versions {
		verdict := "NOT READY"
		if v.Ready {
			verdict = "READY"
		}
		fmt.Fprintf(out, "\n%s (%s): %s\n", v.Key, v.Instance, verdict)
		for _, c := range v.Checks {
			fmt.Fprintf(out, "  %-4s %s: %s\n", c.Status, c.Name, c.Message)
			if c.Fix != "" && c.Status != golive.Pass {
				fmt.Fprintf(out, "       fix: %s\n", c.Fix)
			}
		}
		fmt.Fprintln(out, "  cutover:")
		for i, s := range v.Cutover {
			fmt.Fprintf(out, "    %d. %s\n", i+1, s)
		}
		fmt.Fprintln(out, "  rollback:")
		for i, s := range v.Rollback {
			fmt.Fprintf(out, "    %d. %s\n", i+1, s)
		}
	}
}
