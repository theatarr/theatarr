package main

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

// Backup sets (docs/restore.md). The nightly job and theatarr backup now write three files side by side:
//
//	theatarr-<UTC stamp>.db             a consistent database snapshot (VACUUM INTO)
//	theatarr-<UTC stamp>.policy.tar     the policy directory: every regular file, overrides included (none when the
//	                              install has no policy directory: everything lives in the database)
//	theatarr-<UTC stamp>.manifest.json  what the set holds: theatarr version, schema, file hashes, the policy revision, and the
//	                              names of the env secrets the instance needs (never their values)
//
// The manifest is written last, so a set without one is incomplete. Restoring needs the set plus the secrets.

func init() {
	register(command{
		name:    "backup",
		summary: "backup sets: now (database + policy + manifest), list, restore, fingerprint (titles, versions, links)",
		run:     func(args []string) error { return runBackup(args, os.Stdout) },
	})
}

const backupUsage = `usage:
  theatarr backup now [-db FILE] [-policy DIR] [-dir DIR]     write a backup set now, as the nightly job does
  theatarr backup list [-dir DIR]                             the sets in DIR, newest last, with what each holds
  theatarr backup restore -from SET -db FILE [-policy-out DIR] [-force] [-env-file FILE]
                                                             restore a set (SET: any of its three paths, or the path
                                                             without a suffix): check hashes, validate the settings,
                                                             copy the database, unpack the policy (-policy-out; not
                                                             needed for a set without a policy directory), report
                                                             which env secrets are set (names only)
  theatarr backup fingerprint [-db FILE]                      counts and hashes of titles, versions, links and users, to
                                                             compare a restored database with its source
`

// manifestFormat is the manifest's version. 2 added policy.state, so a database-only set (no policy archive) is
// refused by a Theatarr that would look for one; format 1 manifests are read as state "files".
const manifestFormat = 2

// The policy directory of a backup set (backupPolicy.State): YAML files, a directory with no YAML (the settings
// live in the database), or no directory at all (the policy archive is left out).
const (
	policyFiles  = "files"
	policyEmpty  = "empty"
	policyAbsent = "absent"
)

type backupManifest struct {
	Format          int            `json:"format"`
	CreatedAt       time.Time      `json:"created_at"`
	TheatarrVersion string         `json:"theatarr_version"`
	Schema          int64          `json:"schema_version"`
	Database        backupFile     `json:"database"`
	Policy          backupPolicy   `json:"policy"`
	Secrets         []backupSecret `json:"secrets"`
	Restore         string         `json:"restore"`
}

type backupFile struct {
	File   string `json:"file"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type backupPolicy struct {
	// backupFile is the archive; empty when State is absent.
	backupFile
	// State says what the policy directory held: files, empty or absent.
	State string `json:"state"`
	// Dir is where the policy was read from.
	Dir string `json:"dir"`
	// Revision is the archived files' policy revision; LoadedRevision the one the service ran with (they differ when
	// the files were edited after the last start).
	Revision       string   `json:"revision"`
	LoadedRevision string   `json:"loaded_revision"`
	Files          []string `json:"files"`
}

// backupSecret names one env var the instance reads a secret from. Set says whether it was set when the backup was
// written; Optional ones only add a feature (the TVDB subscriber PIN).
type backupSecret struct {
	Env      string   `json:"env"`
	UsedBy   []string `json:"used_by"`
	Set      bool     `json:"set"`
	Optional bool     `json:"optional,omitempty"`
}

// secretsOf lists the secret env vars the policy names, and the optional ones the service reads beside them.
// Credentials the settings database holds are left out: they are in the backup's database file.
func secretsOf(pol *policy.Policy, getenv func(string) string) []backupSecret {
	var out []backupSecret
	for env, users := range pol.SecretEnvs() {
		if settingsHeldSecret(env) {
			continue
		}
		out = append(out, backupSecret{Env: env, UsedBy: users, Set: getenv(env) != ""})
	}
	if pol.Metadata != nil && pol.Metadata.Sources["tvdb"] != nil {
		out = append(out, backupSecret{Env: tvdbPINEnv, UsedBy: []string{"metadata.sources[tvdb] (subscriber PIN)"},
			Set: getenv(tvdbPINEnv) != "", Optional: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Env < out[j].Env })
	return out
}

// writeBackupSet writes one backup set into dir and returns its path without a suffix. A policy directory that does
// not exist (a database-only install) is recorded as absent and no archive is written. On an error nothing of the
// set is left behind, so an incomplete set never counts towards the kept ones.
func writeBackupSet(ctx context.Context, st *store.Store, pol *policy.Policy, dir string, now time.Time) (_ string, _ *backupManifest, err error) {
	base := filepath.Join(dir, "theatarr-"+now.UTC().Format("20060102-150405"))
	// Two sets in the same second (a checkpoint beside the nightly run) must not clean up each other's files.
	for _, sfx := range []string{".db", ".policy.tar", ".manifest.json"} {
		if _, err := os.Lstat(base + sfx); err == nil {
			return "", nil, fmt.Errorf("%s already exists", base+sfx)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", nil, err
		}
	}
	defer func() {
		if err != nil {
			for _, sfx := range []string{".db", ".policy.tar", ".manifest.json"} {
				os.Remove(base + sfx)
			}
		}
	}()
	if err := st.Backup(ctx, base+".db"); err != nil {
		return "", nil, err
	}
	m := &backupManifest{Format: manifestFormat, CreatedAt: now.UTC(), TheatarrVersion: buildinfo.String(),
		Secrets: secretsOf(pol, os.Getenv), Restore: "docs/restore.md"}
	if m.Schema, _, err = st.SchemaVersion(ctx); err != nil {
		return "", nil, err
	}
	if m.Database, err = describeFile(base + ".db"); err != nil {
		return "", nil, err
	}
	m.Policy = backupPolicy{Dir: pol.Dir, LoadedRevision: pol.Revision, State: policyAbsent, Revision: emptyRevision()}
	if _, err := os.Stat(pol.Dir); err == nil {
		files, err := tarDir(pol.Dir, base+".policy.tar")
		if err != nil {
			return "", nil, fmt.Errorf("backup: policy: %w", err)
		}
		m.Policy.Files, m.Policy.State = files, policyEmpty
		if hasPolicyFile(files) {
			m.Policy.State = policyFiles
		}
		if m.Policy.backupFile, err = describeFile(base + ".policy.tar"); err != nil {
			return "", nil, err
		}
		if m.Policy.Revision, err = policy.RevisionOf(pol.Dir); err != nil {
			return "", nil, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", nil, fmt.Errorf("backup: policy: %w", err)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", nil, err
	}
	if err := writeAtomic(base+".manifest.json", append(b, '\n')); err != nil {
		return "", nil, err
	}
	return base, m, nil
}

// emptyRevision is the policy revision of a directory with no policy files (policy.ReadOrEmpty's).
func emptyRevision() string {
	h := sha256.Sum256(nil)
	return hex.EncodeToString(h[:])[:12]
}

// hasPolicyFile reports whether an archive's files include a policy file: a .yaml or .yml directly in the
// directory, dotfiles left out (as policy.Read picks them).
func hasPolicyFile(files []string) bool {
	for _, f := range files {
		ext := strings.ToLower(path.Ext(f))
		if !strings.Contains(f, "/") && !strings.HasPrefix(f, ".") && (ext == ".yaml" || ext == ".yml") {
			return true
		}
	}
	return false
}

func describeFile(p string) (backupFile, error) {
	f, err := os.Open(p)
	if err != nil {
		return backupFile{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return backupFile{}, err
	}
	return backupFile{File: filepath.Base(p), Bytes: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func writeAtomic(p string, b []byte) error {
	tmp := p + ".partial"
	err := os.WriteFile(tmp, b, 0o600)
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// tarDir archives every regular file and directory under dir (symlinks and special files are left out) into dest,
// and returns the files' paths relative to dir, sorted.
func tarDir(dir, dest string) ([]string, error) {
	tmp := dest + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	tw := tar.NewWriter(f)
	var files []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Every directory, empty ones too: the settings may name one (metadata.overrides_dir) that has no files yet.
			if rel == "." {
				return nil
			}
			return tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel) + "/", Mode: 0o755, ModTime: info.ModTime(),
				Typeflag: tar.TypeDir, Format: tar.FormatPAX})
		}
		if !d.Type().IsRegular() {
			return nil
		}
		hdr := &tar.Header{Name: filepath.ToSlash(rel), Mode: 0o644, Size: info.Size(), ModTime: info.ModTime(),
			Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		if _, err := io.Copy(tw, src); err != nil {
			return err
		}
		files = append(files, hdr.Name)
		return nil
	})
	if err == nil {
		err = tw.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, os.Rename(tmp, dest)
}

// untar unpacks src into dir. Only regular files and directories are written, and no name may leave dir.
func untar(src, dir string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(hdr.Name)
		if name == "." || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("policy archive: unsafe name %q", hdr.Name)
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("policy archive: %s is not a regular file", hdr.Name)
		}
	}
}

// pruneBackups keeps the newest keep sets in dir (by their theatarr-*.db files, whose names sort by time) and removes the
// older ones with their policy archives, manifests and migration settings exports.
func pruneBackups(dir string, keep int) error {
	files, err := filepath.Glob(filepath.Join(dir, "theatarr-*.db"))
	if err != nil || keep <= 0 || len(files) <= keep {
		return err
	}
	sort.Strings(files)
	var errs []error
	for _, f := range files[:len(files)-keep] {
		base := strings.TrimSuffix(f, ".db")
		for _, p := range []string{f, base + ".policy.tar", base + ".manifest.json", base + ".settings.yaml"} {
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func runBackup(args []string, out io.Writer) error {
	if len(args) == 0 || helpArg(args) {
		fmt.Fprint(os.Stderr, backupUsage)
		if helpArg(args) {
			return flag.ErrHelp
		}
		return errors.New("give now, list, restore or fingerprint")
	}
	fs := flag.NewFlagSet("backup "+args[0], flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), backupUsage+"\nflags:\n"); fs.PrintDefaults() }
	dbPath := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	policyDir := fs.String("policy", policyDirDefault(), "policy directory (env THEATARR_POLICY)")
	dir := fs.String("dir", "", "backup directory (default: env THEATARR_BACKUP_DIR, else backups/ next to the database)")
	from := fs.String("from", "", "restore: the backup set")
	policyOut := fs.String("policy-out", "", "restore: directory to unpack the policy into (empty or missing)")
	force := fs.Bool("force", false, "restore: replace an existing database (kept as <db>.pre-restore-<stamp>) and "+
		"unpack into a non-empty policy directory")
	envFile := fs.String("env-file", "", "restore: an env file to check the secrets against (names only are printed)")
	settingsSourceFlag(fs)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *dir == "" {
		*dir = defaultBackupDir(*dbPath)
	}
	ctx := context.Background()
	switch args[0] {
	case "now":
		// Never create a database just to back it up.
		if _, err := os.Stat(*dbPath); err != nil {
			return fmt.Errorf("backup: database: %w", err)
		}
		pol, err := backupSourcePolicy(fs, *policyDir)
		if err != nil {
			return err
		}
		st, err := store.Open(ctx, *dbPath, store.Options{})
		if err != nil {
			return err
		}
		defer st.Close()
		base, m, err := writeBackupSet(ctx, st, pol, *dir, time.Now())
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s\n  database %s (%d bytes, schema %d)\n  policy   %s\n",
			base, m.Database.File, m.Database.Bytes, m.Schema, describePolicy(m.Policy))
		printSecrets(out, m.Secrets, nil)
		return nil
	case "list":
		return listBackups(out, *dir)
	case "restore":
		if *from == "" {
			return errors.New("restore needs -from and -db (and -policy-out unless the set has no policy directory)")
		}
		getenv := os.Getenv
		if *envFile != "" {
			vals, err := readEnvNames(*envFile)
			if err != nil {
				return err
			}
			getenv = func(k string) string {
				if vals[k] {
					return "set"
				}
				return os.Getenv(k)
			}
		}
		return restoreBackup(ctx, out, *from, *dbPath, *policyOut, *force, getenv, time.Now())
	case "fingerprint":
		fp, err := fingerprint(ctx, *dbPath)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(fp)
		fmt.Fprintln(out, string(b))
		return nil
	}
	return fmt.Errorf("unknown subcommand %q: give now, list, restore or fingerprint", args[0])
}

// backupSourcePolicy is the policy serve runs with, as far as a backup needs it (its directory and the secrets it
// names), for the -settings-source and -db flags of fset (headBackupPolicy).
func backupSourcePolicy(fset *flag.FlagSet, dir string) (*policy.Policy, error) {
	source := settingsSourceDefault()
	if f := fset.Lookup("settings-source"); f != nil {
		source = f.Value.String()
	}
	dbPath := defaultDBPath()
	if f := fset.Lookup("db"); f != nil {
		dbPath = f.Value.String()
	}
	return headBackupPolicy(context.Background(), source, dir, dbPath)
}

// headBackupPolicy is the settings database's head over the YAML base, or the YAML alone before the first settings
// generation. A database-only install with no policy directory, or one with no YAML yet, is an empty policy, as
// serve treats it.
func headBackupPolicy(ctx context.Context, source, dir, dbPath string) (*policy.Policy, error) {
	p, snap, err := loadSettingsSnapshot(ctx, source, dir, dbPath)
	switch {
	case err == nil && snap != nil:
		return snap.Policy, nil
	case err == nil:
		return p, nil
	case errors.Is(err, errNoSettings):
		return databasePolicyBase(dir)
	}
	return nil, err
}

// backupPolicyNow gives the policy a backup set records (its directory and secret names) at the time of the backup:
// the current settings head through backup now's loader, so a secret named in the settings editor after the start is
// listed; fallback (the policy serve started with) when the head cannot be loaded.
func backupPolicyNow(source, dir, dbPath string, fallback *policy.Policy, log *slog.Logger) func(context.Context) *policy.Policy {
	return func(ctx context.Context) *policy.Policy {
		p, err := headBackupPolicy(ctx, source, dir, dbPath)
		if err != nil {
			log.Warn("backup: settings head did not load; the set names the startup policy's secrets", "err", err)
			return fallback
		}
		return p
	}
}

// describePolicy is the policy part of a set in one line.
func describePolicy(p backupPolicy) string {
	switch p.State {
	case policyAbsent:
		return fmt.Sprintf("none: %s did not exist (database-only install)", p.Dir)
	case policyEmpty:
		return fmt.Sprintf("%s (no policy files, %d other file(s); the settings are in the database)", p.File, len(p.Files))
	}
	return fmt.Sprintf("%s (revision %s, %d files)", p.File, p.Revision, len(p.Files))
}

// defaultBackupDir is THEATARR_BACKUP_DIR, else backups/ next to the database.
func defaultBackupDir(dbPath string) string {
	if d := os.Getenv("THEATARR_BACKUP_DIR"); d != "" {
		return d
	}
	return filepath.Join(filepath.Dir(dbPath), "backups")
}

// readEnvNames reads the names an env file sets (NAME=value lines, # comments); values are only checked for
// emptiness and never kept.
func readEnvNames(p string) (map[string]bool, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.Trim(strings.TrimSpace(v), `"'`) != "" {
			out[strings.TrimSpace(k)] = true
		}
	}
	return out, nil
}

// backupHoldsCredentials is the plain truth about what a backup set holds.
const backupHoldsCredentials = "credentials entered in the web UI are stored in the database, so this backup " +
	"holds them: keep it private"

func printSecrets(out io.Writer, secrets []backupSecret, getenv func(string) string) {
	var env []backupSecret
	for _, s := range secrets {
		// Sets written before these names were left out list them too.
		if !settingsHeldSecret(s.Env) {
			env = append(env, s)
		}
	}
	if len(env) == 0 {
		fmt.Fprintln(out, "  secrets  no environment variables named by the settings; "+backupHoldsCredentials)
		return
	}
	fmt.Fprintln(out, "  secrets  "+backupHoldsCredentials+"; the environment variables below are not in it (names only):")
	for _, s := range env {
		set := s.Set
		if getenv != nil {
			set = getenv(s.Env) != ""
		}
		state := "set"
		switch {
		case !set && s.Optional:
			state = "not set (optional)"
		case !set:
			state = "MISSING"
		}
		fmt.Fprintf(out, "    %-28s %-18s %s\n", s.Env, state, strings.Join(s.UsedBy, ", "))
	}
}

func readManifest(base string) (*backupManifest, error) {
	b, err := os.ReadFile(base + ".manifest.json")
	if err != nil {
		return nil, fmt.Errorf("backup set %s has no manifest (incomplete, or written before manifests): %w", base, err)
	}
	var m backupManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", base+".manifest.json", err)
	}
	if m.Format < 1 || m.Format > manifestFormat {
		return nil, fmt.Errorf("manifest %s: format %d, this Theatarr reads 1 to %d", base+".manifest.json", m.Format, manifestFormat)
	}
	if m.Format == 1 {
		m.Policy.State = policyFiles
	}
	switch {
	case m.Policy.State == policyAbsent && (m.Policy.File != "" || len(m.Policy.Files) > 0):
		return nil, fmt.Errorf("manifest %s: an absent policy directory with an archive", base+".manifest.json")
	case m.Policy.State != policyAbsent && m.Policy.State != policyEmpty && m.Policy.State != policyFiles:
		return nil, fmt.Errorf("manifest %s: unknown policy state %q", base+".manifest.json", m.Policy.State)
	case m.Policy.State != policyAbsent && m.Policy.File == "":
		return nil, fmt.Errorf("manifest %s: no policy archive", base+".manifest.json")
	}
	return &m, nil
}

func listBackups(out io.Writer, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "theatarr-*.db"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	if len(files) == 0 {
		fmt.Fprintf(out, "no backups in %s\n", dir)
		return nil
	}
	for _, f := range files {
		base := strings.TrimSuffix(f, ".db")
		m, err := readManifest(base)
		if err != nil {
			fi, _ := os.Stat(f)
			var size int64
			if fi != nil {
				size = fi.Size()
			}
			fmt.Fprintf(out, "%s  database only (%d bytes): no manifest\n", base, size)
			continue
		}
		named, missing := 0, 0
		for _, s := range m.Secrets {
			if settingsHeldSecret(s.Env) {
				continue
			}
			named++
			if !s.Set && !s.Optional {
				missing++
			}
		}
		pol := fmt.Sprintf("policy %s (%d files)", m.Policy.Revision, len(m.Policy.Files))
		switch m.Policy.State {
		case policyAbsent:
			pol = "no policy directory (database only)"
		case policyEmpty:
			pol = "no policy files (database only)"
		}
		fmt.Fprintf(out, "%s  %s  schema %d  %s  %d secret(s) named, %d unset at backup\n", base,
			m.CreatedAt.Format(time.RFC3339), m.Schema, pol, named, missing)
	}
	return nil
}

// setBase turns any of a set's paths into its path without a suffix.
func setBase(p string) string {
	for _, s := range []string{".manifest.json", ".policy.tar", ".db"} {
		if b, ok := strings.CutSuffix(p, s); ok {
			return b
		}
	}
	return p
}

// restoreBackup restores a set: hashes checked, the policy staged (it must have the manifest's revision), the
// database copied and integrity-checked, and the configuration serve would run with (the staged database's settings
// over the staged policy, or the policy alone before the first settings generation) validated, all before anything
// live is touched; then the old database is moved aside, the new one takes its name and the policy is unpacked, with
// the old database put back if a later step fails. A database-only set (no policy directory) unpacks no
// policy, and policyOut may be empty. The secrets are reported by name, set or not as getenv says; a legacy set's
// recovery preview reads the process environment (previewRecoveryImport). It never starts the service.
func restoreBackup(ctx context.Context, out io.Writer, from, dbPath, policyOut string, force bool,
	getenv func(string) string, now time.Time) error {
	return restoreBackupFor(ctx, out, from, dbPath, policyOut, "", force, getenv, now)
}

// restoreBackupFor is restoreBackup for a service that runs with the policy directory configured (its -policy) rather
// than the restored one: the restored settings are checked against configured, the unpacked policy is for reference
// only, and a legacy set, which needs the command-line import of its files, is refused (servebackups.go). An empty
// configured is the command-line restore.
func restoreBackupFor(ctx context.Context, out io.Writer, from, dbPath, policyOut, configured string, force bool,
	getenv func(string) string, now time.Time) error {
	base := setBase(from)
	m, err := readManifest(base)
	if err != nil {
		return err
	}
	if err := store.CheckSchemaVersion(m.Schema); err != nil {
		return fmt.Errorf("backup restore: manifest: %w", err)
	}
	unpack := m.Policy.State != policyAbsent
	want := []backupFile{m.Database}
	if unpack {
		want = append(want, m.Policy.backupFile)
	}
	for _, want := range want {
		got, err := describeFile(filepath.Join(filepath.Dir(base), want.File))
		if err != nil {
			return err
		}
		if got.SHA256 != want.SHA256 || got.Bytes != want.Bytes {
			return fmt.Errorf("%s does not match its manifest (sha256 %s, want %s)", want.File, got.SHA256, want.SHA256)
		}
	}
	switch {
	case unpack && policyOut == "":
		return errors.New("restore needs -policy-out: the set holds a policy directory")
	case unpack:
		if entries, err := os.ReadDir(policyOut); err == nil && len(entries) > 0 && !force {
			return fmt.Errorf("%s is not empty: give -force to unpack into it", policyOut)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	case policyOut != "":
		// Nothing is unpacked, so policy files already there would sit under the restored settings.
		if rev, err := policy.RevisionOf(policyOut); err == nil && rev != m.Policy.Revision {
			return fmt.Errorf("%s holds policy files and the set has none (a database-only install): give an empty or "+
				"missing directory", policyOut)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	_, statErr := os.Stat(dbPath)
	replacing := statErr == nil
	if replacing && !force {
		return fmt.Errorf("%s exists: give -force to replace it (the old one is kept beside it)", dbPath)
	}

	// Stage the policy beside its target (an empty directory for a database-only set).
	stageParent := filepath.Dir(dbPath)
	if policyOut != "" {
		stageParent = filepath.Dir(policyOut)
	}
	if err := os.MkdirAll(stageParent, 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(stageParent, ".policy-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if unpack {
		if err := unpackPolicy(base+".policy.tar", stage, m.Policy.Revision); err != nil {
			return err
		}
	}

	// Stage and check the database beside its target.
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return err
	}
	tmp := dbPath + ".partial"
	defer os.Remove(tmp)
	if err := copyFile(filepath.Join(filepath.Dir(base), m.Database.File), tmp); err != nil {
		return err
	}
	if err := integrityCheck(ctx, tmp); err != nil {
		return err
	}
	yamlAuthority, note, made, atSource, err := checkRestoredConfig(ctx, tmp, stage, configured, m.Policy, dbPath+".validate")
	if err != nil {
		return err
	}

	// Swap the database in, keeping the old one beside it; put it back if anything after this fails.
	keep := dbPath + ".pre-restore-" + now.UTC().Format("20060102-150405")
	var moved []string
	restoreOld := func() {
		if len(moved) > 0 || !replacing {
			os.Remove(dbPath)
		}
		for _, sfx := range moved {
			os.Rename(keep+sfx, dbPath+sfx)
		}
	}
	if replacing {
		for _, sfx := range []string{"", "-wal", "-shm"} {
			if err := os.Rename(dbPath+sfx, keep+sfx); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				restoreOld()
				return err
			}
			moved = append(moved, sfx)
		}
	}
	if err := os.Rename(tmp, dbPath); err != nil {
		restoreOld()
		return err
	}
	if unpack {
		if err := os.MkdirAll(policyOut, 0o755); err != nil {
			restoreOld()
			return err
		}
		if err := unpackPolicy(base+".policy.tar", policyOut, m.Policy.Revision); err != nil {
			restoreOld()
			return fmt.Errorf("%w (the database was put back; check %s)", err, policyOut)
		}
		if made != "" {
			if err := os.MkdirAll(filepath.Join(policyOut, made), 0o755); err != nil {
				restoreOld()
				return fmt.Errorf("%w (the database was put back; check %s)", err, policyOut)
			}
		}
	}
	if replacing {
		fmt.Fprintf(out, "kept the old database as %s\n", keep)
	}

	polLine := fmt.Sprintf("%s (revision %s, %d files)", policyOut, m.Policy.Revision, len(m.Policy.Files))
	polFlag := policyOut
	switch m.Policy.State {
	case policyAbsent:
		polLine = "none: the set is of a database-only install (no policy directory); nothing was unpacked"
		polFlag = "<an empty or missing directory>"
		if policyOut != "" {
			polFlag = policyOut
		}
	case policyEmpty:
		polLine = fmt.Sprintf("%s (no policy files, %d other file(s); the settings are in the database)", policyOut,
			len(m.Policy.Files))
	}
	if configured != "" {
		polFlag = configured
	}
	fmt.Fprintf(out, "restored %s\n  database %s (schema %d, from Theatarr %s)\n  policy   %s\n",
		base, dbPath, m.Schema, m.TheatarrVersion, polLine)
	if note != "" {
		fmt.Fprintf(out, "  note     %s\n", note)
	}
	printSecrets(out, m.Secrets, getenv)
	if yamlAuthority {
		// Serve reads no settings from the policy files: without an import it would start with none or an incomplete set.
		fmt.Fprintf(out, "next: the set has no complete settings generation: import its policy before starting serve:\n"+
			"  theatarr settings import -policy %s -db %s   (review the preview, then repeat with -confirm)\n"+
			"  then start theatarr serve with -db %s -policy %s and the environment variables above, wait for /readyz, then\n"+
			"  theatarr library dangling -scan; theatarr library relink -dry-run   (docs/restore.md)\n", polFlag, dbPath, dbPath, polFlag)
		return nil
	}
	if atSource != "" && filepath.Clean(policyOut) != filepath.Clean(m.Policy.Dir) {
		// The stored absolute path names the source policy directory: serve finds it only with the policy back there.
		fmt.Fprintf(out, "next: install the restored policy %s at %s, the directory its stored metadata.overrides_dir (%s) "+
			"is in (docs/restore.md step 4), then\n", policyOut, m.Policy.Dir, atSource)
		fmt.Fprintf(out, "  start theatarr serve with -db %s -policy %s and the environment variables above, wait for /readyz, then\n"+
			"  theatarr library dangling -scan; theatarr library relink -dry-run   (docs/restore.md)\n", dbPath, m.Policy.Dir)
		return nil
	}
	fmt.Fprintf(out, "next: start theatarr serve with -db %s -policy %s and the environment variables above, wait for /readyz, then\n"+
		"  theatarr library dangling -scan; theatarr library relink -dry-run   (docs/restore.md)\n", dbPath, polFlag)
	return nil
}

// unpackPolicy unpacks archive into dir and checks the policy files have the manifest's revision.
func unpackPolicy(archive, dir, revision string) error {
	if err := untar(archive, dir); err != nil {
		return err
	}
	rev, err := policy.RevisionOf(dir)
	if err != nil {
		return err
	}
	if rev != revision {
		return fmt.Errorf("unpacked policy has revision %s, the manifest says %s", rev, revision)
	}
	return nil
}

// checkRestoredConfig validates the configuration serve would start with from the staged database, on a scratch copy
// migrated as serve would migrate it (the staged copy stays as backed up). The settings head must load from the
// database alone over the policy directory serve will use (only configured, the browser's, when set; otherwise the
// staged one dir), whose root YAML is not read; root YAML that does not load on its own then only gets a note. A database with
// no generation is a fresh install, which serve starts with nothing configured, or with policy files a set the
// settings import seeds. A set whose settings do not load from the database alone is legacy: its settings load only
// over its policy files. For both, the command-line restore names the settings import (yamlAuthority) only when
// that import's preview succeeds on the staged copy. A configured restore (the browser's) never offers that recovery.
// The command-line restore checks an absolute overrides_dir inside the backed-up policy directory (src.Dir) under
// the staged copy, and creates the overrides directory the settings name there when an archive written before
// archives kept empty directories leaves it out; made is that directory, relative, for the restored policy too.
// atSource is the stored overrides_dir of a normal set when it is absolute inside src.Dir: serve finds it only with
// the restored policy installed there.
func checkRestoredConfig(ctx context.Context, db, dir, configured string, src backupPolicy, scratch string) (yamlAuthority bool,
	note, made, atSource string, err error) {
	state := src.State
	for _, sfx := range []string{"", "-wal", "-shm"} {
		os.Remove(scratch + sfx)
		defer os.Remove(scratch + sfx)
	}
	if err := copyFile(db, scratch); err != nil {
		return false, "", "", "", err
	}
	st, err := store.Open(ctx, scratch, store.Options{})
	if err != nil {
		return false, "", "", "", err
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		return false, "", "", "", fmt.Errorf("the database does not migrate with this Theatarr, nothing was restored: %w", err)
	}
	var gen int64
	if err := st.Reader().QueryRowContext(ctx, `SELECT COALESCE(MAX(generation), 0) FROM settings_generation`).Scan(&gen); err != nil {
		return false, "", "", "", err
	}
	if gen == 0 {
		// Before the first settings generation: a fresh install, or a set of the policy files alone, which the
		// settings import seeds.
		if state != policyFiles {
			return false, "", "", "", nil
		}
		if configured != "" {
			return false, "", "", "", fmt.Errorf("%s", policyOnlyRestoreNote)
		}
		if _, err := policy.Load(dir); err != nil {
			return false, "", "", "", fmt.Errorf("the policy does not validate with this Theatarr, nothing was restored: %w", err)
		}
		// The restore names the settings import that seeds the database: only if it would succeed.
		if err := previewRecoveryImport(ctx, st, dir); err != nil {
			return false, "", "", "", fmt.Errorf("the restored policy files (no settings generation) do not import with this Theatarr, "+
				"nothing was restored: %w", err)
		}
		return true, "", made, "", nil
	}
	settingsErr := func(err error) error {
		return fmt.Errorf("the restored settings (generation %d) do not load with this Theatarr and the restored policy, "+
			"nothing was restored: %w", gen, err)
	}
	if configured != "" {
		// As the service will load them: over its own policy directory only. The archived copy is for reference; a set
		// that needs it (or its files' import) is restored from the command line.
		if err := loadRestoredSettings(ctx, st, configured, ""); err != nil {
			return false, "", "", "", fmt.Errorf("the restored settings (generation %d) do not load with the policy directory this "+
				"service uses (%s): %w; restore it from the command line (docs/restore.md) to use the archived policy "+
				"files; nothing was restored", gen, configured, err)
		}
		return false, "", "", "", nil
	}
	// As serve loads them: over the policy directory, without reading its root YAML.
	if state != policyAbsent {
		if made, atSource, err = addOverridesDir(ctx, st, dir, src); err != nil {
			return false, "", "", "", err
		}
	}
	loadErr := loadRestoredSettings(ctx, st, dir, src.Dir)
	if loadErr == nil {
		if state == policyFiles {
			if _, err := policy.Load(dir); err != nil {
				note = fmt.Sprintf("the policy files alone do not validate (%v); serve does not read them as settings", err)
			}
		}
		return false, note, made, atSource, nil
	}
	if state != policyFiles {
		return false, "", "", "", settingsErr(loadErr)
	}
	// The recovery import, previewed on the scratch copy as theatarr settings import would run it.
	if err := previewRecoveryImport(ctx, st, dir); err != nil {
		return false, "", "", "", settingsErr(fmt.Errorf("%w; the settings import that would complete them fails: %w", loadErr, err))
	}
	return true, "", made, "", nil
}

// previewRecoveryImport previews, on the scratch copy st, the settings import of dir that a legacy restore names, as
// theatarr settings import runs it: with the process environment and its THEATARR_TRASH. The restore's -env-file only
// reports which secrets are set; it holds no values.
func previewRecoveryImport(ctx context.Context, st *store.Store, dir string) error {
	runtime, req, err := settingsImport(ctx, st, dir, os.LookupEnv, envOr("THEATARR_TRASH", ""))
	if err != nil {
		return err
	}
	_, err = runtime.PreviewImport(ctx, req)
	return err
}

// loadRestoredSettings loads the stored settings head over the policy directory dir as serve would; copyOf, when set,
// is the policy directory dir is a copy of (policy.CopyOf).
func loadRestoredSettings(ctx context.Context, st *store.Store, dir, copyOf string) error {
	base, err := databasePolicyBase(dir)
	if err != nil {
		return err
	}
	if copyOf != "" {
		base.CopyOf(copyOf)
	}
	sst, err := settings.New(st)
	if err != nil {
		return err
	}
	snap, err := sst.Load(ctx, base)
	if err != nil {
		return err
	}
	_, _, err = runtimeServicePolicy(snap)
	return err
}

// addOverridesDir creates, empty, the overrides directory the stored settings name inside the staged policy directory
// dir (relative, or absolute inside src.Dir) when it is missing there and the archive lists no files under it, and
// returns it relative to dir. An archive written before archives kept empty directories leaves an empty one out.
// atSource is the stored overrides_dir when it is absolute inside src.Dir (resolved under dir), otherwise empty.
func addOverridesDir(ctx context.Context, st *store.Store, dir string, src backupPolicy) (made, atSource string, err error) {
	base, err := databasePolicyBase(dir)
	if err != nil {
		return "", "", err
	}
	base.CopyOf(src.Dir)
	sst, err := settings.New(st)
	if err != nil {
		return "", "", err
	}
	// The settings are not valid yet when the directory is missing; the policy they make is all this needs.
	snap, _ := sst.LoadDiagnostic(ctx, base)
	if snap == nil || snap.Policy == nil || snap.Policy.OverridesPath() == "" {
		return "", "", nil
	}
	rel, err := filepath.Rel(base.Dir, snap.Policy.OverridesPath())
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", nil
	}
	if stored := snap.Policy.Metadata.OverridesDir; filepath.IsAbs(stored) {
		atSource = stored
	}
	prefix := filepath.ToSlash(rel) + "/"
	for _, f := range src.Files {
		if strings.HasPrefix(f, prefix) {
			return "", atSource, nil
		}
	}
	if _, err := os.Stat(filepath.Join(base.Dir, rel)); !errors.Is(err, fs.ErrNotExist) {
		return "", atSource, nil
	}
	return rel, atSource, os.MkdirAll(filepath.Join(base.Dir, rel), 0o755)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// openRO opens a database read-only, outside the store (no migrations, no writer).
func openRO(p string) (*sql.DB, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: url.Values{"mode": {"ro"},
		"_pragma": {"busy_timeout(5000)"}}.Encode()}
	return sql.Open("sqlite", u.String())
}

func integrityCheck(ctx context.Context, p string) error {
	db, err := openRO(p)
	if err != nil {
		return err
	}
	defer db.Close()
	var res string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	if res != "ok" {
		return fmt.Errorf("integrity check: %s", res)
	}
	return nil
}

// fingerprint is what a restore must keep: counts and hashes of the titles (kind, name, year, ids), versions
// (title, policy key, monitored), links (path, target, state) and users (names, roles), and the schema.
func fingerprint(ctx context.Context, dbPath string) (map[string]any, error) {
	db, err := openRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := map[string]any{}
	var schema int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied`).
		Scan(&schema); err != nil {
		return nil, err
	}
	out["schema"] = schema
	for _, q := range []struct{ name, sql string }{
		{"titles", `SELECT id, kind, name, COALESCE(year, 0), COALESCE(tmdb_id, 0), COALESCE(tvdb_id, 0),
			COALESCE(imdb_id, '') FROM title ORDER BY id`},
		{"versions", `SELECT id, title_id, policy_key, monitored FROM version ORDER BY id`},
		{"links", `SELECT path, target, state FROM link ORDER BY path`},
		{"users", `SELECT username, role, status FROM web_user ORDER BY username`},
	} {
		n, sum, err := hashRows(ctx, db, q.sql)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", q.name, err)
		}
		out[q.name] = n
		out[q.name+"_sha256"] = sum
	}
	return out, nil
}

func hashRows(ctx context.Context, db *sql.DB, q string) (int, string, error) {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	n := 0
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return 0, "", err
		}
		for _, v := range vals {
			fmt.Fprintf(h, "%v\x1f", v)
		}
		h.Write([]byte{'\n'})
		n++
	}
	return n, hex.EncodeToString(h.Sum(nil))[:16], rows.Err()
}
