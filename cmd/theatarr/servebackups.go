package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/web"
)

type restoreRequest struct {
	Set         string    `json:"set"`
	RequestedBy string    `json:"requested_by"`
	RequestedAt time.Time `json:"requested_at"`
	SafetySet   string    `json:"safety_set"`
}

type serveBackups struct {
	mu                  sync.Mutex
	st                  *store.Store
	dir, dbPath, source string
	keep, hour          int
	policyNow           func(context.Context) *policy.Policy
	now                 func() time.Time
	pending             bool
}

func backupRefusal(status int, message string) error {
	return &web.BackupError{Status: status, Message: message}
}
func (b *serveBackups) writable() error {
	if b.dir == "" {
		return backupRefusal(http.StatusConflict, "Backups are off; set --backup-dir to enable them.")
	}
	if b.pending {
		return backupRefusal(http.StatusConflict, "A restore is already pending; Theatarr is restarting.")
	}
	return nil
}
func (b *serveBackups) makeSet(ctx context.Context) (string, error) {
	base, _, err := writeBackupSet(ctx, b.st, b.policyNow(ctx), b.dir, b.now())
	if err != nil {
		return "", err
	}
	return filepath.Base(base), nil
}
func (b *serveBackups) Now(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.writable(); err != nil {
		return "", err
	}
	return b.makeSet(ctx)
}
func validBackupName(name string) bool {
	if !strings.HasPrefix(name, "theatarr-") {
		return false
	}
	_, err := time.Parse("20060102-150405", strings.TrimPrefix(name, "theatarr-"))
	return err == nil
}
func (b *serveBackups) RequestRestore(ctx context.Context, name, actor string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.writable(); err != nil {
		return "", err
	}
	if !validBackupName(name) {
		return "", backupRefusal(http.StatusNotFound, "Unknown backup set")
	}
	base := filepath.Join(b.dir, name)
	if _, err := os.Stat(base + ".db"); errors.Is(err, os.ErrNotExist) {
		if _, manifestErr := os.Stat(base + ".manifest.json"); errors.Is(manifestErr, os.ErrNotExist) {
			return "", backupRefusal(http.StatusNotFound, "Unknown backup set")
		} else if manifestErr != nil {
			return "", manifestErr
		}
	} else if err != nil {
		return "", err
	}
	m, err := readManifest(base)
	if errors.Is(err, os.ErrNotExist) {
		err = backupRefusal(http.StatusUnprocessableEntity, "This backup is incomplete")
	}
	if err == nil {
		err = completeBrowserBackup(base, m)
	}
	if err == nil {
		err = browserRestoreCompatible(ctx, base, m)
	}
	if err != nil {
		return "", backupRefusal(http.StatusUnprocessableEntity, err.Error())
	}
	safety, err := b.makeSet(ctx)
	if err != nil {
		return "", err
	}
	request := restoreRequest{Set: name, RequestedBy: actor, RequestedAt: b.now().UTC(), SafetySet: safety}
	if err := writeRestoreJSON(filepath.Join(filepath.Dir(b.dbPath), "restore-request.json"), request); err != nil {
		return "", err
	}
	b.pending = true
	return safety, nil
}
func (b *serveBackups) List(ctx context.Context) (*web.BackupList, error) {
	list := &web.BackupList{Dir: b.dir, Keep: b.keep, Hour: b.hour, Enabled: b.dir != "", SettingsSource: b.source, Sets: []web.BackupSet{}}
	if b.dir != "" {
		files, err := filepath.Glob(filepath.Join(b.dir, "theatarr-*.db"))
		if err != nil {
			return nil, err
		}
		manifests, err := filepath.Glob(filepath.Join(b.dir, "theatarr-*.manifest.json"))
		if err != nil {
			return nil, err
		}
		bases := make(map[string]bool)
		for _, file := range files {
			bases[strings.TrimSuffix(file, ".db")] = true
		}
		for _, file := range manifests {
			bases[strings.TrimSuffix(file, ".manifest.json")] = true
		}
		files = files[:0]
		for base := range bases {
			files = append(files, base)
		}
		sort.Strings(files)
		for i := len(files) - 1; i >= 0; i-- {
			base := files[i]
			item := web.BackupSet{Name: filepath.Base(base)}
			item.CreatedAt, _ = time.Parse("20060102-150405", strings.TrimPrefix(item.Name, "theatarr-"))
			m, err := readManifest(base)
			if err == nil {
				item.CreatedAt, item.Schema, item.Bytes, item.PolicyState = m.CreatedAt, m.Schema, m.Database.Bytes+m.Policy.Bytes, m.Policy.State
				err = completeBrowserBackup(base, m)
				item.Complete = err == nil
				if err == nil {
					err = browserRestoreCompatible(ctx, base, m)
				}
			} else if fi, statErr := os.Stat(base + ".db"); statErr == nil {
				item.Bytes = fi.Size()
			}
			if errors.Is(err, os.ErrNotExist) {
				err = backupRefusal(http.StatusUnprocessableEntity, "This backup is incomplete")
			}
			if err != nil {
				item.WhyNot = err.Error()

			} else {
				item.Restorable = true
			}
			list.Sets = append(list.Sets, item)
		}
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(b.dbPath), "restore-result.json"))
	if err == nil {
		err = json.Unmarshal(data, &list.LastRestore)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return list, nil
}

const policyOnlyRestoreNote = "This backup is from before settings were stored in Theatarr's database. Restore it from the command line (docs/restore.md)."

func completeBrowserBackup(base string, m *backupManifest) error {
	paths := []string{base + ".db"}
	if m.Policy.State != policyAbsent {
		paths = append(paths, base+".policy.tar")
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			return backupRefusal(http.StatusUnprocessableEntity, "This backup is incomplete")
		}
	}
	return nil
}

func browserRestoreCompatible(ctx context.Context, base string, m *backupManifest) error {
	if err := store.CheckSchemaVersion(m.Schema); err != nil {
		return err
	}
	if m.Policy.State != policyFiles {
		return nil
	}
	db, err := openRO(base + ".db")
	if err != nil {
		return err
	}
	defer db.Close()
	// Older sets may predate the settings table itself.
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='settings_generation'`).Scan(&exists); err != nil {
		return err
	}
	var gen int64
	if exists != 0 {
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation), 0) FROM settings_generation`).Scan(&gen); err != nil {
			return err
		}
	}
	if gen == 0 {
		return fmt.Errorf("%s", policyOnlyRestoreNote)
	}
	return nil
}

func writeRestoreJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// A claimed request is never replayed, even if the process crashes during restore.
// Failures are reported without preventing serve from opening the original database.
func applyRestoreRequest(ctx context.Context, log *slog.Logger, out io.Writer, dir, dbPath, policyDir string, now time.Time) (*web.RestoreResult, error) {
	dataDir := filepath.Dir(dbPath)
	requestPath := filepath.Join(dataDir, "restore-request.json")
	taken := filepath.Join(dataDir, "restore-request.taken.json")
	if _, err := os.Stat(taken); err == nil {
		result := &web.RestoreResult{StartedAt: now.UTC(), FinishedAt: now.UTC(), Error: "the previous restore was interrupted; Theatarr started on the database it had before"}
		if data, err := os.ReadFile(taken); err == nil {
			var req restoreRequest
			if json.Unmarshal(data, &req) == nil {
				result.Set, result.RequestedBy, result.SafetySet = req.Set, req.RequestedBy, req.SafetySet
			}
		}
		if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
			copies, _ := filepath.Glob(dbPath + ".pre-restore-*")
			for i := len(copies) - 1; i >= 0; i-- {
				keep := copies[i]
				if strings.HasSuffix(keep, "-wal") || strings.HasSuffix(keep, "-shm") {
					continue
				}
				if err := os.Rename(keep, dbPath); err != nil {
					return nil, fmt.Errorf("recover interrupted restore: %w", err)
				}
				for _, suffix := range []string{"-wal", "-shm"} {
					if err := os.Rename(keep+suffix, dbPath+suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
						return nil, fmt.Errorf("recover interrupted restore sidecar: %w", err)
					}
				}
				break
			}
		} else if err == nil && dir != "" && validBackupName(result.Set) {
			current, currentErr := fingerprint(ctx, dbPath)
			requested, requestedErr := fingerprint(ctx, filepath.Join(dir, result.Set)+".db")
			if currentErr == nil && requestedErr == nil && reflect.DeepEqual(current, requested) {
				result.OK = true
				result.Error = ""
				result.Message = "Restore completed after an interrupted restart."
				log.Info("backup restored", "set", result.Set, "message", result.Message)
				finishRestoreResult(log, dataDir, taken, result)
				return result, nil
			}
		}
		log.Error("backup restore interrupted", "set", result.Set)
		finishRestoreResult(log, dataDir, taken, result)
		return result, nil
	}
	if err := os.Rename(requestPath, taken); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Error("claim restore request", "err", err)
		}
		return nil, nil
	}
	result := &web.RestoreResult{StartedAt: now.UTC()}
	data, err := os.ReadFile(taken)
	var req restoreRequest
	if err == nil {
		err = json.Unmarshal(data, &req)
	}
	result.Set, result.RequestedBy, result.SafetySet = req.Set, req.RequestedBy, req.SafetySet
	if err == nil && (!validBackupName(req.Set) || dir == "") {
		err = fmt.Errorf("invalid restore set or backups are off")
	}
	if err == nil {
		var m *backupManifest
		m, err = readManifest(filepath.Join(dir, req.Set))
		if err == nil {
			err = browserRestoreCompatible(ctx, filepath.Join(dir, req.Set), m)
		}
		if err == nil {
			if m.Policy.State != policyAbsent {
				result.PolicyOut = filepath.Join(dataDir, "policy-restored-"+now.UTC().Format("20060102-150405"))
			}
			// The restarted service runs with its configured policy directory, so the restored settings are checked
			// against that one; the archived files are unpacked for reference only.
			var configured *policy.Policy
			if configured, err = databasePolicyBase(policyDir); err == nil {
				err = restoreBackupFor(ctx, out, filepath.Join(dir, req.Set), dbPath, result.PolicyOut, configured.Dir, true, os.Getenv, now)
			}
		}
	}
	result.OK = err == nil
	if err != nil {
		result.Error = err.Error()
		log.Error("backup restore failed", "set", req.Set, "err", err)
	} else {
		keep := dbPath + ".pre-restore-" + now.UTC().Format("20060102-150405")
		if _, err := os.Stat(keep); err == nil {
			result.KeptDatabase = keep
		}
		configured, _ := databasePolicyBase(policyDir)
		result.Message = "Restored database. It runs with this service's policy directory (" + configured.Dir + ")"
		if result.PolicyOut != "" {
			result.Message += "; the archived policy files in " + result.PolicyOut + " are for reference only"
		}
		result.Message += "."
		log.Info("backup restored", "set", req.Set, "message", result.Message)
	}
	result.FinishedAt = time.Now().UTC()
	finishRestoreResult(log, dataDir, taken, result)
	return result, nil
}

func finishRestoreResult(log *slog.Logger, dataDir, taken string, result *web.RestoreResult) {
	if err := writeRestoreJSON(filepath.Join(dataDir, "restore-result.json"), result); err != nil {
		log.Error("write restore result", "err", err)
		return
	}
	if err := os.Remove(taken); err != nil {
		log.Error("remove claimed restore request", "err", err)
	}
}

// Only the result returned by this startup is passed here, never a historical result file.
func enqueueRestoredProviderSync(ctx context.Context, q *jobs.Queue, result *web.RestoreResult) error {
	if result == nil || !result.OK {
		return nil
	}
	_, _, err := q.Enqueue(ctx, provider.JobKind, provider.SyncPayload{}, jobs.EnqueueOptions{DedupeKey: provider.JobKind})
	return err
}
