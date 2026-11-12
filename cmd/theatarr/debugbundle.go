package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/logring"
	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{name: "debug", summary: "write a redacted diagnostics bundle (debug bundle -out FILE)",
		run: func(args []string) error { return runDebug(args, os.Stdout) }})
}

const debugUsage = `usage: theatarr debug bundle [flags]

Writes a tar.gz of redacted diagnostics to attach to a bug report: build info and schema version, the settings
review export (credentials omitted), the latest log lines, the open error inbox rows with their stacks, and the
last health snapshot theatarr serve recorded. Every credential the settings and the environment name is masked, as
are credential-named log attributes, URL credentials and long tokens. Reads only; safe while the service runs.
`

// debugBundleSecretSuffixes mark the environment variables (THEATARR_*) whose values a bundle masks beside the
// ones the policy names.
var debugBundleSecretSuffixes = []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "PASS", "PIN"}

func runDebug(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "bundle" {
		fmt.Fprint(os.Stderr, debugUsage)
		if helpArg(args) {
			return flag.ErrHelp
		}
		return errors.New("want a subcommand: bundle")
	}
	fs := flag.NewFlagSet("debug bundle", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), debugUsage+"\nflags:\n"); fs.PrintDefaults() }
	dbPath := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	policyDir := policyDirDefault()
	settingsSourceFlag(fs)
	logDir := fs.String("log-dir", envOr("THEATARR_LOG_DIR", ""), "log directory (env THEATARR_LOG_DIR; default logs/ next to the database)")
	lines := fs.Int("lines", 2000, "latest log lines to include")
	outPath := fs.String("out", "", "the bundle to write (.tar.gz)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *outPath == "" || fs.NArg() != 0 {
		return errors.New("debug bundle: give -out FILE and nothing else")
	}
	if *logDir == "" {
		*logDir = filepath.Join(filepath.Dir(*dbPath), "logs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	b := &debugBundle{now: time.Now()}
	files, err := b.collect(ctx, fs, *dbPath, policyDir, *logDir, *lines)
	if err != nil {
		return err
	}
	if err := writeBundle(*outPath, files, b.now); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "wrote %s (%d files)\n", *outPath, len(files))
	return err
}

type debugBundle struct {
	now   time.Time
	known []string // known secret values, longest first
}

type bundleFile struct {
	name string
	data []byte
}

// collect gathers the bundle's files, redacted. A part that cannot be read becomes a note in its file rather than
// failing the bundle: a bundle is most wanted when something is broken.
func (b *debugBundle) collect(ctx context.Context, fs *flag.FlagSet, dbPath, policyDir, logDir string, lines int) ([]bundleFile, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	st, err := store.Open(ctx, dbPath, store.Options{})
	if err != nil {
		return nil, err
	}
	defer st.Close()

	var files []bundleFile
	add := func(name string, data []byte) { files = append(files, bundleFile{name, data}) }
	note := func(err error) []byte { return []byte("unavailable: " + err.Error() + "\n") }

	// Settings first: they name the secrets every other part is scrubbed of.
	pol, snap, err := loadCommandSettings(fs, policyDir)
	switch {
	case err != nil:
		add("settings.yaml", note(err))
	case snap == nil:
		add("settings.yaml", []byte("# settings come from the YAML policy (no settings generation in the database): not exported\n"))
	default:
		if y, err := snap.Export(); err != nil {
			add("settings.yaml", note(err))
		} else {
			add("settings.yaml", y)
		}
	}
	var known []string
	if pol != nil {
		for env := range pol.SecretEnvs() {
			known = append(known, pol.Getenv(env))
		}
	}
	if snap != nil {
		// A record still on its environment fallback has no stored value: resolve it as the runtime does.
		for _, r := range snap.Records.Indexers {
			_ = snap.WithSecret("indexers", r.Key, os.LookupEnv, func(v string) error { known = append(known, v); return nil })
		}
		for _, r := range snap.Records.Providers {
			_ = snap.WithSecret("providers", r.Key, os.LookupEnv, func(v string) error { known = append(known, v); return nil })
		}
	}
	stored, err := storedSecretValues(ctx, st)
	if err != nil {
		return nil, fmt.Errorf("stored credentials: %w", err)
	}
	known = append(known, stored...)
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(k, "THEATARR_") {
			continue
		}
		for _, sfx := range debugBundleSecretSuffixes {
			if strings.HasSuffix(k, "_"+sfx) {
				known = append(known, v)
			}
		}
	}
	b.setKnown(known)

	cur, target, schemaErr := st.SchemaVersion(ctx)
	build := map[string]any{"version": buildinfo.String(), "go": runtime.Version(), "os": runtime.GOOS,
		"arch": runtime.GOARCH, "written_at": b.now.UTC(), "database": dbPath, "log_dir": logDir,
		"schema_version": cur, "schema_target": target}
	if schemaErr != nil {
		build["schema_error"] = schemaErr.Error()
	}
	add("build.json", mustJSON(build))

	if l, err := recentLogLines(logDir, "theatarr.log", lines); err != nil {
		add("logs.jsonl", note(err))
	} else {
		var buf bytes.Buffer
		for _, line := range l {
			buf.WriteString(redactLogLine(line))
			buf.WriteByte('\n')
		}
		add("logs.jsonl", buf.Bytes())
	}

	if rows, drops, err := openErrors(ctx, st); err != nil {
		add("errors.json", note(err))
	} else {
		add("errors.json", mustJSON(map[string]any{"open": rows, "dropped": drops}))
	}

	if v, at, err := errinbox.State(ctx, st, "health"); err != nil {
		add("health.json", note(err))
	} else if v == "" {
		add("health.json", []byte(`{"note":"no health snapshot: theatarr serve has not recorded one yet"}`+"\n"))
	} else {
		add("health.json", mustJSON(map[string]any{"recorded_at": at.UTC(), "snapshot": json.RawMessage(v)}))
	}

	for i := range files {
		files[i].data = []byte(b.scrub(string(files[i].data)))
	}
	return files, nil
}

// storedSecretValues is every non-empty credential stored in the settings database (setting_secret), whatever owns
// it, and every Radarr or Sonarr list's API key (import_list.api_key). A section's credential is a JSON object of
// values by path; each value is returned.
func storedSecretValues(ctx context.Context, st *store.Store) ([]string, error) {
	rows, err := st.Reader().QueryContext(ctx, "SELECT value FROM setting_secret WHERE value <> ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
		var byPath map[string]string
		if json.Unmarshal([]byte(v), &byPath) == nil {
			for _, value := range byPath {
				out = append(out, value)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// A Radarr or Sonarr list keeps its instance's API key on its own row.
	lists, err := st.Reader().QueryContext(ctx, "SELECT api_key FROM import_list WHERE api_key <> ''")
	if err != nil {
		return nil, err
	}
	defer lists.Close()
	for lists.Next() {
		var v string
		if err := lists.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, lists.Err()
}

// setKnown keeps the credible secret values (6 characters or more, as the gateway's redactor), with their JSON
// escaped forms, longest first so a secret containing another is masked whole.
func (b *debugBundle) setKnown(values []string) {
	b.known = nil
	for _, v := range values {
		if len(v) < 6 {
			continue
		}
		forms := []string{v}
		if j, err := json.Marshal(v); err == nil {
			forms = append(forms, string(j[1:len(j)-1]))
		}
		for _, f := range forms {
			if !slices.Contains(b.known, f) {
				b.known = append(b.known, f)
			}
		}
	}
	sort.Slice(b.known, func(i, j int) bool { return len(b.known[i]) > len(b.known[j]) })
}

func (b *debugBundle) scrub(s string) string {
	for _, v := range b.known {
		s = strings.ReplaceAll(s, v, "********")
	}
	return s
}

// redactLogLine redacts one JSON log line: credential-named attributes (at any depth) become "[redacted]" and
// every string goes through the inbox's text redaction. A line that is not JSON is redacted as text.
func redactLogLine(line string) string {
	var m map[string]any
	if json.Unmarshal([]byte(line), &m) != nil {
		return errinbox.RedactText(line)
	}
	b, err := json.Marshal(redactValue(m))
	if err != nil {
		return errinbox.RedactText(line)
	}
	return string(b)
}

func redactValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if logring.SecretKey(k) {
				x[k] = "[redacted]"
				continue
			}
			x[k] = redactValue(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = redactValue(e)
		}
		return x
	case string:
		return errinbox.RedactText(x)
	}
	return v
}

// recentLogLines returns the last n lines of the log, reading the rotated files (base.1, base.2, ...) when the
// current one holds fewer.
func recentLogLines(dir, base string, n int) ([]string, error) {
	var out []string
	for i := 0; len(out) < n; i++ {
		path := filepath.Join(dir, base)
		if i > 0 {
			path = fmt.Sprintf("%s.%d", path, i)
		}
		l, err := tailLines(path, n-len(out))
		if errors.Is(err, os.ErrNotExist) {
			if i == 0 {
				return nil, fmt.Errorf("no log at %s", path)
			}
			break
		}
		if err != nil {
			return nil, err
		}
		out = append(l, out...)
	}
	return out, nil
}

func tailLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	var ring []string
	for sc.Scan() {
		ring = append(ring, sc.Text())
		if len(ring) > n {
			ring = ring[1:]
		}
	}
	return ring, sc.Err()
}

// openErrors returns the new and seen inbox rows with their stacks, newest first (at most 500), and the drop count.
func openErrors(ctx context.Context, st *store.Store) ([]errinbox.Row, int64, error) {
	p, err := errinbox.List(ctx, st, errinbox.Filter{Status: "open", Limit: 500})
	if err != nil {
		return nil, 0, err
	}
	rows := make([]errinbox.Row, 0, len(p.Items))
	for _, r := range p.Items {
		full, err := errinbox.Get(ctx, st, r.ID)
		if err != nil {
			return nil, 0, err
		}
		full.Sample = errinbox.RedactText(full.Sample)
		full.Stack = errinbox.RedactText(full.Stack)
		var c map[string]any
		if json.Unmarshal(full.Context, &c) == nil {
			full.Context = mustJSON(redactValue(c))
		}
		rows = append(rows, full)
	}
	return rows, p.Drops, nil
}

func mustJSON(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return []byte(`{"error":"cannot encode"}`)
	}
	return append(b, '\n')
}

// writeBundle writes the files as a gzipped tar under a theatarr-debug-<time>/ directory, private to the owner, and
// replaces out only once the archive is complete.
func writeBundle(out string, files []bundleFile, now time.Time) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(out), ".theatarr-debug-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)
	dir := "theatarr-debug-" + now.UTC().Format("20060102-150405") + "/"
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: dir + f.name, Mode: 0o600, Size: int64(len(f.data)),
			ModTime: now, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := tw.Write(f.data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), out)
}
