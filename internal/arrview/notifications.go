package arrview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/store"
)

// Notifications (NotificationController, ProviderControllerBase @ a96bf7c / cab419a): clients install webhooks
// through a view (Ruddarr installs its push relay; a Usenet bridge may register one). They are stored per view in
// arrview_notification and internal/notify sends that view's arr-shaped payloads to them, when notify.enabled is
// true and the URL's host is allowed by the policy (notify.allow_hosts). Only the Webhook implementation is
// served. Secret values (the password field, and the URL's and headers' credentials: notifications_mask.go) are
// written masked, and a masked value sent back keeps the stored one while the URL keeps its scheme and host.

const maskedValue = "********"

// notificationFlags are the on*/supportsOn* switches per flavour, and whether we send the event.
var notificationFlags = map[string][]struct {
	name string
	sent bool
}{
	Radarr: {{"Grab", true}, {"Download", true}, {"Upgrade", true}, {"Rename", true}, {"MovieAdded", true},
		{"MovieDelete", true}, {"MovieFileDelete", true}, {"MovieFileDeleteForUpgrade", false}, {"HealthIssue", true},
		{"HealthRestored", true}, {"ApplicationUpdate", false}, {"ManualInteractionRequired", true}},
	Sonarr: {{"Grab", true}, {"Download", true}, {"Upgrade", true}, {"ImportComplete", false}, {"Rename", true},
		{"SeriesAdd", true}, {"SeriesDelete", true}, {"EpisodeFileDelete", true},
		{"EpisodeFileDeleteForUpgrade", false}, {"HealthIssue", true}, {"HealthRestored", true},
		{"ApplicationUpdate", false}, {"ManualInteractionRequired", true}},
}

// webhookFields is the Webhook's settings (WebhookSettings): url, method (1 POST, 2 PUT), username, password,
// headers.
func webhookFields() []field {
	return []field{
		{Order: 0, Name: "url", Label: "Webhook URL", Type: "url"},
		{Order: 1, Name: "method", Label: "Method", Value: 1, Type: "select"},
		{Order: 2, Name: "username", Label: "Username", Type: "textbox", Privacy: "userName"},
		{Order: 3, Name: "password", Label: "Password", Type: "password", Privacy: "password"},
		{Order: 4, Name: "headers", Label: "Headers", Value: []any{}, Type: "keyValueList", Advanced: true},
	}
}

// notificationOut completes a stored resource for the client: id, implementation names, the supports flags,
// every on* flag present, masked secrets, and a message when it will not be sent.
func (s *Service) notificationOut(v *View, id int64, res map[string]any) map[string]any {
	out := map[string]any{}
	for k, x := range res {
		out[k] = x
	}
	out["id"] = id
	out["implementationName"] = "Webhook"
	out["infoLink"] = "https://wiki.servarr.com/" + v.Flavour + "/supported#webhook"
	if _, ok := out["tags"]; !ok {
		out["tags"] = []int64{}
	}
	for _, f := range notificationFlags[v.Flavour] {
		out["supportsOn"+f.name] = f.sent
		if _, ok := out["on"+f.name]; !ok {
			out["on"+f.name] = false
		}
	}
	if _, ok := out["includeHealthWarnings"]; !ok {
		out["includeHealthWarnings"] = false
	}
	if fs, ok := out["fields"].([]any); ok {
		masked := make([]any, 0, len(fs))
		for _, f := range fs {
			m, ok := f.(map[string]any)
			if !ok {
				continue
			}
			c := map[string]any{}
			for k, x := range m {
				c[k] = x
			}
			switch n, _ := c["name"].(string); {
			case strings.EqualFold(n, "password"):
				if str, _ := c["value"].(string); str != "" {
					c["value"] = maskedValue
				}
			case strings.EqualFold(n, "url"):
				if str, ok := c["value"].(string); ok {
					c["value"] = maskURL(str)
				}
			case strings.EqualFold(n, "headers"):
				c["value"] = maskHeaders(c["value"])
			}
			masked = append(masked, c)
		}
		out["fields"] = masked
	}
	if why := s.notSent(res); why != "" {
		out["message"] = map[string]any{"message": why, "type": "warning"}
	} else {
		delete(out, "message")
	}
	return out
}

// fieldValue reads a field of a stored resource.
func fieldValue(res map[string]any, name string) any {
	fs, _ := res["fields"].([]any)
	for _, f := range fs {
		if m, ok := f.(map[string]any); ok {
			if n, _ := m["name"].(string); strings.EqualFold(n, name) {
				return m["value"]
			}
		}
	}
	return nil
}

// notSent says why a stored notification would not be sent, or "".
func (s *Service) notSent(res map[string]any) string {
	n := s.pol.NotifySettings()
	if !n.Enabled {
		return "Stored, not sent: notify.enabled is false in the policy"
	}
	u, _ := fieldValue(res, "url").(string)
	pu, err := url.Parse(u)
	if err != nil || pu.Host == "" {
		return "Stored, not sent: no valid url"
	}
	if !s.pol.NotifyHostAllowed(pu) {
		return "Stored, not sent: host " + pu.Host + " is not in notify.allow_hosts"
	}
	return ""
}

func (s *Service) loadNotifications(ctx context.Context, v *View, id int64) (map[int64]map[string]any, []int64, error) {
	q := `SELECT id, resource FROM arrview_notification WHERE view = ?`
	args := []any{v.Key}
	if id > 0 {
		q += ` AND id = ?`
		args = append(args, id)
	}
	rows, err := s.st.Reader().QueryContext(ctx, q+` ORDER BY name, id`, args...)
	if err != nil {
		return nil, nil, err
	}
	out := map[int64]map[string]any{}
	var order []int64
	for rows.Next() {
		var nid int64
		var raw string
		if err := rows.Scan(&nid, &raw); err != nil {
			rows.Close()
			return nil, nil, err
		}
		m := map[string]any{}
		_ = json.Unmarshal([]byte(raw), &m)
		out[nid] = m
		order = append(order, nid)
	}
	return out, order, closeRows(rows)
}

// notificationTags maps tag ids to the view's notifications that carry them (tag/detail).
func (s *Service) notificationTags(ctx context.Context, v *View) (map[int64][]int64, error) {
	ns, order, err := s.loadNotifications(ctx, v, 0)
	if err != nil {
		return nil, err
	}
	out := map[int64][]int64{}
	for _, id := range order {
		if ts, ok := ns[id]["tags"].([]any); ok {
			for _, t := range ts {
				if f, ok := t.(float64); ok {
					out[int64(f)] = append(out[int64(f)], id)
				}
			}
		}
	}
	return out, nil
}

// GET notification and notification/{id}.
func (s *Service) notifications(w http.ResponseWriter, r *request) {
	id, one := r.pathID(1)
	ns, order, err := s.loadNotifications(r.Context(), r.v, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if one {
		if len(order) == 0 {
			notFound(w, "Notification with ID "+strconv.FormatInt(id, 10)+" does not exist")
			return
		}
		writeJSON(w, http.StatusOK, s.notificationOut(r.v, id, ns[id]))
		return
	}
	out := make([]map[string]any, 0, len(order))
	for _, nid := range order {
		out = append(out, s.notificationOut(r.v, nid, ns[nid]))
	}
	writeJSON(w, http.StatusOK, out)
}

// GET notification/schema: the Webhook template.
func (s *Service) notificationSchema(w http.ResponseWriter, r *request) {
	res := map[string]any{"name": "", "implementation": "Webhook", "configContract": "WebhookSettings",
		"fields": toAnyFields(webhookFields()), "presets": []any{}}
	out := s.notificationOut(r.v, 0, res)
	delete(out, "id")
	delete(out, "message")
	writeJSON(w, http.StatusOK, []map[string]any{out})
}

func toAnyFields(fs []field) []any {
	b, _ := json.Marshal(fs)
	var out []any
	_ = json.Unmarshal(b, &out)
	return out
}

// notificationIn validates a POST or PUT body: name and implementation (Webhook only) and configContract set; it
// returns the resource to store (server-set keys dropped).
func notificationIn(body map[string]any) (map[string]any, string, string) {
	name, _ := body["name"].(string)
	impl, _ := body["implementation"].(string)
	contract, _ := body["configContract"].(string)
	switch {
	case strings.TrimSpace(name) == "":
		return nil, "Name", "'Name' must not be empty."
	case impl == "":
		return nil, "Implementation", "'Implementation' must not be empty."
	case !strings.EqualFold(impl, "Webhook"):
		return nil, "Implementation", "Only the Webhook notification is supported by theatarr"
	case contract == "":
		return nil, "ConfigContract", "'Config Contract' must not be empty."
	}
	if _, ok := body["fields"].([]any); !ok {
		return nil, "Fields", "'Fields' must not be empty."
	}
	res := map[string]any{}
	for k, x := range body {
		switch {
		case k == "id", k == "implementationName", k == "infoLink", k == "message", k == "presets",
			strings.HasPrefix(k, "supportsOn"):
			continue
		}
		res[k] = x
	}
	res["implementation"] = "Webhook"
	return res, "", ""
}

// keepSecrets puts the stored secrets (password, the URL's credentials, header values) back where the client sent
// the mask, when the URL keeps the stored one's scheme and host. Pointed elsewhere, the masks stay as sent.
func keepSecrets(res, old map[string]any) {
	oldURL, _ := fieldValue(old, "url").(string)
	newURL, _ := fieldValue(res, "url").(string)
	if !notificationURLs.SameTarget(oldURL, newURL) {
		return
	}
	fs, _ := res["fields"].([]any)
	for _, f := range fs {
		m, ok := f.(map[string]any)
		if !ok {
			continue
		}
		switch n, _ := m["name"].(string); {
		case strings.EqualFold(n, "password") && m["value"] == maskedValue:
			m["value"] = fieldValue(old, "password")
		case strings.EqualFold(n, "url"):
			if str, ok := m["value"].(string); ok {
				m["value"] = unmaskURL(str, oldURL)
			}
		case strings.EqualFold(n, "headers"):
			unmaskHeaders(m["value"], fieldValue(old, "headers"))
		}
	}
}

func anyOn(res map[string]any) bool {
	for k, x := range res {
		if strings.HasPrefix(k, "on") && x == true {
			return true
		}
	}
	return false
}

// POST notification (201) and PUT notification/{id} (202). As the arrs do, an enabled notification is tested
// first unless forceSave=true; here only when it would be sent at all (notify.enabled and an allowed host), so a
// policy that keeps webhooks off never blocks a client's install. A failed test is the arrs' 400.
func (s *Service) saveNotification(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	var body map[string]any
	if err := decodeBody(r, &body); err != nil {
		badRequest(w, "", err.Error(), nil)
		return
	}
	res, prop, msg := notificationIn(body)
	if res == nil {
		badRequest(w, prop, msg, nil)
		return
	}
	var id int64
	if r.Method == http.MethodPut {
		if pid, ok := r.pathID(1); ok {
			id = pid
		} else if f, ok := body["id"].(float64); ok {
			id = int64(f)
		}
		ns, _, err := s.loadNotifications(ctx, r.v, id)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		old := ns[id]
		if old == nil {
			notFound(w, "Notification with ID "+strconv.FormatInt(id, 10)+" does not exist")
			return
		}
		keepSecrets(res, old)
	}
	if !r.queryBool("forceSave", false) && anyOn(res) && s.notSent(res) == "" && s.notifier != nil {
		b, _ := json.Marshal(res)
		if err := s.notifier.TestNotification(ctx, r.v.Key, b); err != nil {
			badRequest(w, "Url", "Unable to send test message: "+err.Error(), nil)
			return
		}
	}
	b, _ := json.Marshal(res)
	name, _ := res["name"].(string)
	now := store.Millis(s.now())
	code := http.StatusCreated
	if id > 0 {
		code = http.StatusAccepted
		if _, err := s.st.Writer().ExecContext(ctx, `UPDATE arrview_notification SET name = ?, resource = ?,
			updated_at = ? WHERE id = ? AND view = ?`, name, string(b), now, id, r.v.Key); err != nil {
			s.internal(w, r, err)
			return
		}
	} else {
		x, err := s.st.Writer().ExecContext(ctx, `INSERT INTO arrview_notification (view, name, implementation,
			resource, created_at, updated_at) VALUES (?, ?, 'Webhook', ?, ?, ?)`, r.v.Key, name, string(b), now, now)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		id, _ = x.LastInsertId()
	}
	s.log.Info("notification saved", "view", r.v.Key, "id", id, "name", name)
	writeJSON(w, code, s.notificationOut(r.v, id, res))
}

// DELETE notification/{id}: 200 {}.
func (s *Service) deleteNotification(w http.ResponseWriter, r *request) {
	id, _ := r.pathID(1)
	x, err := s.st.Writer().ExecContext(r.Context(), `DELETE FROM arrview_notification WHERE id = ? AND view = ?`, id,
		r.v.Key)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if n, _ := x.RowsAffected(); n == 0 {
		notFound(w, "Notification with ID "+strconv.FormatInt(id, 10)+" does not exist")
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

// POST notification/test: send the Test payload to the body's settings now. 200 "{}" on success; 400 with the
// reason (webhooks off, host not allowed, or the target's answer).
func (s *Service) testNotification(w http.ResponseWriter, r *request) {
	var body map[string]any
	if err := decodeBody(r, &body); err != nil {
		badRequest(w, "", err.Error(), nil)
		return
	}
	res, prop, msg := notificationIn(body)
	if res == nil {
		badRequest(w, prop, msg, nil)
		return
	}
	if f, ok := body["id"].(float64); ok && f > 0 {
		ns, _, err := s.loadNotifications(r.Context(), r.v, int64(f))
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if old := ns[int64(f)]; old != nil {
			keepSecrets(res, old)
		}
	}
	if why := s.notSent(res); why != "" {
		badRequest(w, "Url", strings.TrimPrefix(why, "Stored, not sent: "), nil)
		return
	}
	if s.notifier == nil {
		badRequest(w, "Url", "the notifier is not running", nil)
		return
	}
	b, _ := json.Marshal(res)
	if err := s.notifier.TestNotification(r.Context(), r.v.Key, b); err != nil {
		badRequest(w, "Url", "Unable to send test message: "+err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, "{}")
}
