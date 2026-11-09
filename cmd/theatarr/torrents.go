package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/torrents"
)

func init() {
	register(command{
		name: "torrents",
		summary: "the debrid accounts' torrents: report what uses them, back up, restore, delete chosen ones " +
			"(docs/design/torrents.md)",
		run: runTorrents,
	})
}

const torrentsUsage = "usage: theatarr torrents report|backup|backups|export|import|restore|delete|journal [flags]"

func runTorrents(args []string) error {
	if helpArg(args) {
		fmt.Fprintln(os.Stderr, torrentsUsage)
		return flag.ErrHelp
	}
	if len(args) == 0 {
		return errors.New(torrentsUsage)
	}
	sub := map[string]func([]string) error{
		"report": runTorrentsReport, "backup": runTorrentsBackup, "backups": runTorrentsBackups,
		"export": runTorrentsExport, "import": runTorrentsImport, "restore": runTorrentsRestore,
		"delete": runTorrentsDelete, "journal": runTorrentsJournal,
	}
	if f := sub[args[0]]; f != nil {
		return f(args[1:])
	}
	return fmt.Errorf("unknown subcommand %q; %s", args[0], torrentsUsage)
}

// torrentBackupDir is where the backups live: next to the database.
func torrentBackupDir(db string) string { return filepath.Join(filepath.Dir(db), "torrent-backups") }

// torrentFlags are the flags every subcommand shares.
type torrentFlags struct {
	fs                      *flag.FlagSet
	db, policyDir, envFile  *string
	asJSON, verbose         *bool
	needGateway, needAdders bool
	summary, usageLine      string
}

func newTorrentFlags(name, usage, summary string, gateway, adders bool) *torrentFlags {
	f := &torrentFlags{fs: flag.NewFlagSet("torrents "+name, flag.ContinueOnError), needGateway: gateway,
		needAdders: adders, usageLine: usage, summary: summary}
	settingsSourceFlag(f.fs)
	f.fs.Usage = func() {
		fmt.Fprintf(f.fs.Output(), "usage: theatarr torrents %s\n\n%s\n\n", f.usageLine, f.summary)
		f.fs.PrintDefaults()
	}
	f.db = f.fs.String("db", defaultDBPath(), "database file (env THEATARR_DB); backups are in torrent-backups next to it")
	policyBase := policyDirDefault()
	f.policyDir = &policyBase
	f.envFile = f.fs.String("env-file", "", "read KEY=VALUE secrets from this file (variables already set win)")
	f.asJSON = f.fs.Bool("json", false, "print the result as JSON")
	f.verbose = f.fs.Bool("v", false, "log the gateway's requests to stderr")
	return f
}

// open sets up the manager: policy, database and, when needed, the gateway, read clients and adders.
func (f *torrentFlags) open(ctx context.Context) (*torrents.Manager, func(), error) {
	e, err := openProviderEnv(ctx, *f.db, *f.policyDir, *f.envFile, *f.verbose, f.needGateway,
		f.fs.Lookup("settings-source").Value.String())
	if err != nil {
		return nil, nil, err
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := &torrents.Manager{Store: e.st, Policy: e.pol, Providers: e.clients, Dir: torrentBackupDir(*f.db), Log: log}
	if f.needGateway {
		for _, k := range e.missing {
			fmt.Fprintf(os.Stderr, "%s: %s is not set; it is reported as unreadable\n", k,
				e.pol.Providers[k].APIKeyEnv)
		}
	}
	if f.needAdders {
		m.Adders, _ = router.BuildAdders(e.pol, e.gw, os.Getenv, log)
	}
	return m, e.Close, nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func runTorrentsReport(args []string) error {
	f := newTorrentFlags("report", "report [flags]",
		"Lists every configured provider's torrents (GET only) with what uses them: used (a library link, an arr\n"+
			"file, a download, a symlink under torrents.scan_roots), unused, or unknown. Groups: unused, duplicate\n"+
			"hash, duplicate title, failed, stalled, largest. Writes nothing; works with the router off.", true, false)
	group := f.fs.String("group", "", "print only this group: unused, failed, stalled, largest")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	m, done, err := f.open(ctx)
	if err != nil {
		return err
	}
	defer done()
	r, err := m.Report(ctx)
	if err != nil {
		return err
	}
	if *f.asJSON {
		return printJSON(r)
	}
	writeTorrentReport(os.Stdout, r, *group)
	return nil
}

func writeTorrentReport(w io.Writer, r *torrents.Report, group string) {
	byID := map[torrents.Member]torrents.Torrent{}
	for _, p := range r.Providers {
		for _, t := range p.Torrents {
			byID[torrents.Member{Provider: t.Provider, ID: t.ID}] = t
		}
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "provider\tok\ttorrents\tsize\tused\tunused\tunused size\tunknown\t")
	for _, p := range r.Providers {
		ok := "yes"
		if !p.OK {
			ok = "NO"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%d\t%d\t%s\t%d\t\n", p.Provider, ok, p.Count, humanBytes(p.Bytes),
			p.Usage.Used, p.Usage.Unused, humanBytes(p.Usage.UnusedBytes), p.Usage.Unknown)
	}
	tw.Flush()
	for _, p := range r.Providers {
		if p.Error != "" {
			fmt.Fprintf(w, "%s: %s\n", p.Provider, p.Error)
		}
		if p.NearLimit {
			fmt.Fprintf(w, "%s: %d of its %d torrent limit (magnet_limit); nothing is removed automatically\n",
				p.Provider, p.Count, p.Limit)
		}
	}
	for _, u := range r.Unknown {
		fmt.Fprintf(w, "unknown for every unreferenced torrent: %s\n", u)
	}
	list := func(name string, ms []torrents.Member) {
		if group != "" && group != name {
			return
		}
		fmt.Fprintf(w, "\n%s (%d)\n", name, len(ms))
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, m := range ms {
			t := byID[m]
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\t\n", t.Provider, t.ID, t.Hash, humanBytes(t.Bytes), t.Usage,
				t.Name)
		}
		tw.Flush()
	}
	list("unused", r.Unused)
	list("failed", r.Failed)
	list("stalled", r.Stalled)
	list("largest", r.Largest)
	groups := func(name string, gs []torrents.Group) {
		if group != "" && group != name {
			return
		}
		fmt.Fprintf(w, "\n%s (%d groups)\n", name, len(gs))
		for _, g := range gs {
			fmt.Fprintf(w, "  %s\n", g.Label)
			for _, m := range g.Members {
				t := byID[m]
				fmt.Fprintf(w, "    %s %s %s %s %s\n", t.Provider, t.ID, humanBytes(t.Bytes), t.Usage, t.Name)
			}
		}
	}
	groups("duplicate_hash", r.DuplicateHash)
	groups("duplicate_title", r.DuplicateTitle)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func runTorrentsBackup(args []string) error {
	f := newTorrentFlags("backup", "backup [flags]",
		"Takes a backup now: every provider's list with each torrent's hash, name, size, date and selected files\n"+
			"(read within a budget, reused from earlier backups). Stored owner-only in torrent-backups.", true, false)
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	m, done, err := f.open(ctx)
	if err != nil {
		return err
	}
	defer done()
	_, info, err := m.TakeBackup(ctx, torrents.BackupOptions{Reason: torrents.ReasonManual, By: "cli"})
	if err != nil {
		return err
	}
	if *f.asJSON {
		return printJSON(info)
	}
	writeBackups(os.Stdout, []torrents.BackupInfo{info})
	return nil
}

func writeBackups(w io.Writer, list []torrents.BackupInfo) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "id\treason\ttorrents\tsize\tproviders\t")
	for _, b := range list {
		var ps []string
		for _, p := range b.Providers {
			s := fmt.Sprintf("%s %d", p.Provider, p.Torrents)
			if !p.OK {
				s = p.Provider + " unreadable"
			}
			ps = append(ps, s)
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t\n", b.ID, b.Reason, b.Torrents, humanBytes(b.Bytes),
			strings.Join(ps, ", "))
	}
	tw.Flush()
}

func runTorrentsBackups(args []string) error {
	f := newTorrentFlags("backups", "backups [flags]", "Lists the stored backups, newest first.", false, false)
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	m, done, err := f.open(context.Background())
	if err != nil {
		return err
	}
	defer done()
	list, err := m.Backups()
	if err != nil {
		return err
	}
	if *f.asJSON {
		return printJSON(list)
	}
	writeBackups(os.Stdout, list)
	return nil
}

func runTorrentsExport(args []string) error {
	f := newTorrentFlags("export", "export -id BACKUP [-dmm] [-provider KEY] [flags]",
		"Prints a stored backup: Theatarr's format, or with -dmm Debrid Media Manager's (filename and hash).", false,
		false)
	id := f.fs.String("id", "", "backup id (theatarr torrents backups)")
	dmm := f.fs.Bool("dmm", false, "Debrid Media Manager's format")
	prov := f.fs.String("provider", "", "with -dmm, only this provider's torrents")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	m, done, err := f.open(context.Background())
	if err != nil {
		return err
	}
	defer done()
	b, err := m.ReadBackup(*id)
	if err != nil {
		return err
	}
	if *dmm {
		return printJSON(b.DMM(*prov))
	}
	return printJSON(b)
}

func runTorrentsImport(args []string) error {
	f := newTorrentFlags("import", "import -file PATH [-provider KEY] [flags]",
		"Stores a downloaded backup (Theatarr's, or Debrid Media Manager's with -provider naming the account to\n"+
			"restore it to) as a backup of reason import. Nothing is sent to a provider.", false, false)
	file := f.fs.String("file", "", "the backup file")
	prov := f.fs.String("provider", "", "the provider to restore it to (required for a DMM backup)")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	ctx := context.Background()
	m, done, err := f.open(ctx)
	if err != nil {
		return err
	}
	defer done()
	_, info, err := m.ImportBackup(ctx, data, *prov, "cli")
	if err != nil {
		return err
	}
	if *f.asJSON {
		return printJSON(info)
	}
	writeBackups(os.Stdout, []torrents.BackupInfo{info})
	return nil
}

func runTorrentsRestore(args []string) error {
	f := newTorrentFlags("restore", "restore -id BACKUP [-provider KEY] [-to KEY] [-hash H]... [-confirm TOKEN]",
		"Previews putting a backup back: per torrent, already present, will be re-added, or skipped (not a v1\n"+
			"hash, or the target cannot be listed). With -confirm and the preview's token it re-adds them,\n"+
			"paced by the providers' limits, with the backup's file selection where the provider takes one.", true,
		true)
	id := f.fs.String("id", "", "backup id")
	prov := f.fs.String("provider", "", "only this provider's list in the backup")
	to := f.fs.String("to", "", "restore to this provider (default: where each list came from)")
	var hashes stringList
	f.fs.Var(&hashes, "hash", "only this hash (repeatable)")
	confirm := f.fs.String("confirm", "", "the preview's confirmation token: re-add for real")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	m, done, err := f.open(ctx)
	if err != nil {
		return err
	}
	defer done()
	req := torrents.RestoreRequest{BackupID: *id, Provider: *prov, Target: *to, Hashes: hashes}
	if *confirm == "" {
		plan, err := m.PlanRestore(ctx, req)
		if err != nil {
			return err
		}
		if *f.asJSON {
			return printJSON(plan)
		}
		writeRestore(os.Stdout, plan)
		fmt.Printf("\nPREVIEW: nothing was added. To re-add %d torrents (and finish %d file selections): -confirm %s\n",
			plan.Readd, plan.Resume, plan.Token)
		return nil
	}
	plan, err := m.CheckRestore(ctx, req, *confirm)
	if err != nil {
		return err
	}
	res, err := m.Restore(ctx, req, plan.Confirmed(), "cli")
	if err != nil {
		return err
	}
	if *f.asJSON {
		return printJSON(res)
	}
	writeRestore(os.Stdout, res)
	for _, it := range res.Items {
		if it.Outcome == torrents.OutIncomplete || it.Outcome == torrents.OutFailed {
			return fmt.Errorf("torrents: some re-adds failed or still wait for their file selection (see above)")
		}
	}
	return nil
}

func writeRestore(w io.Writer, p *torrents.RestorePlan) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "target\thash\taction\toutcome\tname\tnote\t")
	for _, it := range p.Items {
		note := it.Reason
		if it.Error != "" {
			note = it.Error
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t\n", it.Target, it.Hash, it.Action, it.Outcome, it.Name, note)
	}
	tw.Flush()
	fmt.Fprintf(w, "counts: %s\n", kv(p.Counts))
}

func runTorrentsDelete(args []string) error {
	f := newTorrentFlags("delete",
		"delete -torrent PROVIDER:ID:HASH... [-override PROVIDER:ID:TOKEN]... [-allow-recent] [-confirm TOKEN]",
		"Previews deleting the chosen torrents (dry run by default): a fresh list (same id, same hash) and a\n"+
			"fresh usage check; used or unknown torrents are refused unless overridden with the override token the\n"+
			"preview prints for them, and torrents added in the last 24 hours or still downloading unless\n"+
			"-allow-recent. With -confirm and the preview's token, a full backup is taken (no backup, no delete) and\n"+
			"the allowed torrents are deleted one by one, each journaled. Other apps on the same account may use\n"+
			"these torrents.", true, true)
	var sel, overrides stringList
	f.fs.Var(&sel, "torrent", "a torrent to delete, provider:id:hash as the report shows it (repeatable)")
	f.fs.Var(&overrides, "override", "delete this used or unknown torrent: provider:id:override-token (repeatable)")
	recent := f.fs.Bool("allow-recent", false, "also delete chosen torrents added in the last 24 hours or downloading")
	confirm := f.fs.String("confirm", "", "the preview's confirmation token: delete for real")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	choice, err := parseDeleteSelection(sel, overrides, *recent)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	m, done, err := f.open(ctx)
	if err != nil {
		return err
	}
	defer done()
	var plan *torrents.DeletePlan
	if *confirm == "" {
		plan, err = m.PlanDelete(ctx, choice)
	} else {
		plan, err = m.Delete(ctx, choice, *confirm, "cli")
	}
	if plan != nil {
		if *f.asJSON {
			if jerr := printJSON(plan); jerr != nil {
				return jerr
			}
		} else {
			writeDelete(os.Stdout, plan, *confirm == "")
		}
	}
	return err
}

func parseDeleteSelection(sel, overrides []string, recent bool) ([]torrents.DeleteSelection, error) {
	ov := map[string]string{}
	for _, o := range overrides {
		parts := strings.Split(o, ":")
		if len(parts) != 3 {
			return nil, fmt.Errorf("-override %q: want provider:id:token", o)
		}
		ov[parts[0]+":"+parts[1]] = parts[2]
	}
	var out []torrents.DeleteSelection
	for _, s := range sel {
		parts := strings.Split(s, ":")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return nil, fmt.Errorf("-torrent %q: want provider:id:hash", s)
		}
		d := torrents.DeleteSelection{Provider: parts[0], ID: parts[1], Hash: parts[2], AllowRecent: recent}
		if tok, ok := ov[parts[0]+":"+parts[1]]; ok {
			d.Override, d.OverrideToken = true, tok
			delete(ov, parts[0]+":"+parts[1])
		}
		out = append(out, d)
	}
	for k := range ov {
		return nil, fmt.Errorf("-override %s names no -torrent", k)
	}
	if len(out) == 0 {
		return nil, errors.New("choose at least one -torrent")
	}
	return out, nil
}

func writeDelete(w io.Writer, p *torrents.DeletePlan, preview bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "provider\tid\thash\tsize\tusage\taction\toutcome\tname\tnote\t")
	for _, it := range p.Items {
		note := it.Refusal
		if it.Error != "" {
			note = it.Error
		}
		if it.NeedsOverride && it.ExpectedOverride != "" {
			note += fmt.Sprintf(" [override: -override %s:%s:%s]", it.Provider, it.ID, it.ExpectedOverride)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n", it.Provider, it.ID, it.Hash, humanBytes(it.Bytes),
			it.Usage, it.Action, it.Outcome, it.Name, note)
	}
	tw.Flush()
	fmt.Fprintln(w, p.Warning)
	if preview {
		fmt.Fprintf(w, "\nDRY RUN: nothing was deleted. %d torrents (%s) would be deleted, %d refused.\n", p.Count,
			humanBytes(p.Bytes), p.Refused)
		if p.Count > 0 {
			fmt.Fprintf(w, "To delete them after a full backup: -confirm %s\n", p.Token)
		}
		return
	}
	fmt.Fprintf(w, "\nbackup %s; deleted %d, failed %d\n", p.BackupID, p.Deleted, p.Failed)
}

func runTorrentsJournal(args []string) error {
	f := newTorrentFlags("journal", "journal [-n N] [flags]",
		"Lists the torrent journal: every delete, restore and backup, newest first.", false, false)
	n := f.fs.Int("n", 50, "entries")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	m, done, err := f.open(ctx)
	if err != nil {
		return err
	}
	defer done()
	list, err := m.Journal(ctx, *n)
	if err != nil {
		return err
	}
	if *f.asJSON {
		return printJSON(list)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "at\taction\tprovider\thash\toutcome\tby\tbackup\tname\tdetail\t")
	for _, e := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n", e.At.UTC().Format("2006-01-02 15:04:05"),
			e.Action, e.Provider, e.Hash, e.Outcome, e.By, e.BackupID, e.Name, e.Detail)
	}
	return tw.Flush()
}
