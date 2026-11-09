package web

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/logring"
)

type LogFile struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

func logFileNumber(name string) (int, bool) {
	if name == "theatarr.log" {
		return 0, true
	}
	s, ok := strings.CutPrefix(name, "theatarr.log.")
	n, err := strconv.Atoi(s)
	return n, ok && err == nil && n > 0 && strconv.Itoa(n) == s
}

func (w *Web) apiLogFiles(rw http.ResponseWriter, r *http.Request) {
	out := struct {
		Items []LogFile `json:"items"`
	}{Items: []LogFile{}}
	if w.logDir != "" {
		entries, err := os.ReadDir(w.logDir)
		if err != nil && !os.IsNotExist(err) {
			w.internal(rw, err)
			return
		}
		for _, e := range entries {
			if _, ok := logFileNumber(e.Name()); !ok {
				continue
			}
			info, err := e.Info()
			if err != nil {
				w.internal(rw, err)
				return
			}
			if !info.Mode().IsRegular() {
				continue
			}
			out.Items = append(out.Items, LogFile{Name: e.Name(), Size: info.Size(), Modified: info.ModTime()})
		}
	}
	slices.SortFunc(out.Items, func(a, b LogFile) int {
		x, _ := logFileNumber(a.Name)
		y, _ := logFileNumber(b.Name)
		return x - y
	})
	writeJSON(rw, http.StatusOK, out)
}

func (w *Web) apiLogFile(rw http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := logFileNumber(name); !ok || w.logDir == "" {
		notFound(rw)
		return
	}
	path := filepath.Join(w.logDir, name)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	if !info.Mode().IsRegular() {
		notFound(rw)
		return
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	defer f.Close()
	rw.Header().Set("Content-Type", "application/x-ndjson")
	rw.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(rw, r, name, info.ModTime(), f)
}

func (w *Web) apiLogLevel(rw http.ResponseWriter, r *http.Request) {
	if w.logLevel == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "runtime log level unavailable"})
		return
	}
	w.logMu.Lock()
	defer w.logMu.Unlock()
	if r.Method == http.MethodPut {
		var body struct {
			Level string `json:"level"`
		}
		if err := decodeJSON(r, &body); err != nil {
			badRequest(rw, "invalid JSON: "+err.Error())
			return
		}
		previous := w.logLevel.Level()
		if err := w.logLevel.Set(body.Level); err != nil {
			badRequest(rw, err.Error())
			return
		}
		var level slog.Level
		_ = level.UnmarshalText([]byte(w.logLevel.Level()))
		// Record the audit even when the new threshold suppresses info.
		w.log.Log(r.Context(), max(slog.LevelInfo, level), "log level changed",
			"from", previous, "to", w.logLevel.Level(), "by", auth.FromContext(r.Context()).UserID)
	}
	writeJSON(rw, http.StatusOK, map[string]string{"level": w.logLevel.Level()})
}

// LogPage is GET /logs. A page is oldest first; More means another page is ready at After in the same boot.
type LogPage struct {
	logring.Page
	Components []string `json:"components"`
}

func (w *Web) apiLogs(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := logring.Filter{Level: slog.LevelInfo, Component: q.Get("component"), Limit: 500}
	if lv := q.Get("level"); lv != "" {
		if err := f.Level.UnmarshalText([]byte(strings.ToUpper(lv))); err != nil {
			badRequest(rw, "level must be debug, info, warn or error")
			return
		}
	}
	if s := q.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			badRequest(rw, "since must be an RFC 3339 time")
			return
		}
		f.Since = t
	}
	var err error
	if f.Limit, err = intParam(r, "limit", 500); err != nil {
		badRequest(rw, err.Error())
		return
	}
	after, err := intParam(r, "after", 0)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	f.After = int64(after)
	if q.Has("seen") {
		seen, err := strconv.ParseInt(q.Get("seen"), 10, 64)
		if err != nil || seen < 0 {
			badRequest(rw, "seen must be a nonnegative integer")
			return
		}
		f.Seen = &seen
	}
	if f.Limit == 0 || f.Limit > 2000 {
		f.Limit = 2000
	}
	out := LogPage{Page: logring.Page{Items: []logring.Entry{}}, Components: []string{}}
	if w.logs != nil {
		out.Page = w.logs.Poll(f, q.Get("boot_id"))
		out.Components = w.logs.Components()
	}
	writeJSON(rw, http.StatusOK, out)
}
