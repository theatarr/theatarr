package web

import (
	"net/http"
)

// apiNotifyTargetTest sends a saved notification target's test message now (POST settings/notify/targets/{name}/test):
// a Discord or ntfy target gets "Theatarr test message", others their test payloads. It answers {ok} or {ok: false, error}.
// The saved destination is what is tested; notify.enabled must be on.
func (w *Web) apiNotifyTargetTest(rw http.ResponseWriter, r *http.Request) {
	if w.notifyTest == nil {
		writeJSON(rw, http.StatusOK, map[string]any{"ok": false, "error": "The notifier is not running."})
		return
	}
	if err := w.notifyTest(r.Context(), r.PathValue("name")); err != nil {
		writeJSON(rw, http.StatusOK, map[string]any{"ok": false, "error": "Not sent: " + err.Error()})
		return
	}
	w.log.Info("notification test sent", "target", r.PathValue("name"))
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}
