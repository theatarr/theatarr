package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
)

const BackupYAMLNote = "Restore from the command line (docs/restore.md): this install reads its settings from policy files."

type BackupSet struct {
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"created_at"`
	Schema      int64     `json:"schema"`
	Bytes       int64     `json:"bytes"`
	PolicyState string    `json:"policy_state"`
	Complete    bool      `json:"complete"`
	Restorable  bool      `json:"restorable"`
	WhyNot      string    `json:"why_not"`
}

type RestoreResult struct {
	Set          string    `json:"set"`
	OK           bool      `json:"ok"`
	Error        string    `json:"error"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	RequestedBy  string    `json:"requested_by"`
	SafetySet    string    `json:"safety_set"`
	KeptDatabase string    `json:"kept_database"`
	PolicyOut    string    `json:"policy_out"`
	Message      string    `json:"message"`
}

type BackupList struct {
	Dir            string         `json:"dir"`
	Keep           int            `json:"keep"`
	Hour           int            `json:"hour"`
	Enabled        bool           `json:"enabled"`
	SettingsSource string         `json:"settings_source"`
	Sets           []BackupSet    `json:"sets"`
	LastRestore    *RestoreResult `json:"last_restore"`
}

// Backups uses the command's backup and manifest helpers, shared with nightly backups and CLI restore.
type Backups interface {
	List(context.Context) (*BackupList, error)
	Now(context.Context) (string, error)
	RequestRestore(context.Context, string, string) (string, error)
}

type BackupError struct {
	Status  int
	Message string
}

func (e *BackupError) Error() string { return e.Message }

func (w *Web) mountBackupsAPI(mux *http.ServeMux) {
	b := APIBase + "/system/backups"
	w.handle(mux, "GET "+b, SignedIn, auth.PermAdmin, w.apiBackups)
	w.handle(mux, "POST "+b, SignedIn, auth.PermAdmin, w.apiBackupNow)
	w.handle(mux, "POST "+b+"/{name}/restore", SignedIn, auth.PermAdmin, w.apiBackupRestore)
}
func (w *Web) backupError(rw http.ResponseWriter, err error) {
	var e *BackupError
	if errors.As(err, &e) {
		writeJSON(rw, e.Status, apiError{Error: e.Message})
		return
	}
	w.internal(rw, err)
}
func (w *Web) hasBackups(rw http.ResponseWriter) bool {
	if w.backups != nil {
		return true
	}
	writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "backups are unavailable"})
	return false
}
func (w *Web) apiBackups(rw http.ResponseWriter, r *http.Request) {
	if !w.hasBackups(rw) {
		return
	}
	list, err := w.backups.List(r.Context())
	if err != nil {
		w.backupError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, list)
}
func (w *Web) apiBackupNow(rw http.ResponseWriter, r *http.Request) {
	if !w.hasBackups(rw) {
		return
	}
	name, err := w.backups.Now(r.Context())
	if err != nil {
		w.backupError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]string{"name": name})
}
func (w *Web) apiBackupRestore(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(rw, err.Error())
		return
	}
	name := r.PathValue("name")
	if body.Confirm != name {
		badRequest(rw, "confirm must match the backup name exactly")
		return
	}
	if !w.hasBackups(rw) {
		return
	}
	if w.restart == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "restart is unavailable"})
		return
	}
	safety, err := w.backups.RequestRestore(r.Context(), name, actor(r))
	if err != nil {
		w.backupError(rw, err)
		return
	}
	writeJSON(rw, http.StatusAccepted, map[string]string{"safety_set": safety,
		"message": "Theatarr is restarting to restore " + name + ". This page reconnects when it is back."})
	// Flush before starting the delay so the browser can enter its reconnect state first.
	_ = http.NewResponseController(rw).Flush()
	time.AfterFunc(time.Second, w.restart)
}
