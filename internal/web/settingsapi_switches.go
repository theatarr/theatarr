package web

import (
	"net/http"

	"github.com/theatarr/theatarr/internal/settings"
)

func (w *Web) apiSettingsSwitches(rw http.ResponseWriter, r *http.Request) {
	gen := int64(0)
	var configured *settings.GlobalSwitches
	{
		snap, ok := w.settingsHead(rw, r)
		if !ok {
			return
		}
		gen, configured = snap.Generation, snap.Records.Switches
	}
	writeJSON(rw, http.StatusOK, map[string]any{"generation": gen, "switches": w.settingsSwitches(), "configured": configured})
}

func (w *Web) apiSettingsSwitchesWrite(confirm bool) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if w.refuseUnavailable(rw) {
			return
		}
		var body struct {
			Generation        *int64 `json:"generation"`
			Paused            *bool  `json:"paused"`
			Router            *bool  `json:"router_enabled"`
			Library           *bool  `json:"library_enabled"`
			Notify            *bool  `json:"notify_enabled"`
			ConfirmationToken string `json:"confirmation_token"`
		}
		if !decodeSettingsJSON(rw, r, &body) {
			return
		}
		if body.Router != nil || body.Library != nil || body.Notify != nil {
			writeJSON(rw, http.StatusBadRequest, apiError{Error: "use paused; notifications are the notify section's enabled field"})
			return
		}
		if body.Paused == nil {
			writeJSON(rw, http.StatusBadRequest, apiError{Error: "paused is required"})
			return
		}
		snap, ok := w.settingsAt(rw, r, body.Generation)
		if !ok {
			return
		}
		if snap.Records.Switches == nil {
			writeJSON(rw, http.StatusConflict, apiError{Error: "global switches have not been seeded; restart the service"})
			return
		}
		records := cloneRecords(snap.Records)
		value := settings.GlobalSwitches{Paused: *body.Paused}
		records.Switches = &value
		req := settings.SaveRequest{ExpectedGeneration: snap.Generation, Actor: settingsActor(r), Records: records,
			ConfirmationToken: confirmationToken(r, body.ConfirmationToken)}
		if confirm {
			proof, err := w.settings.Confirm(r.Context(), req)
			if err != nil {
				w.settingsError(rw, r, err, nil)
				return
			}
			writeJSON(rw, http.StatusOK, proof)
			return
		}
		w.saveSettings(rw, r, req, nil, "switches", "")
	}
}
