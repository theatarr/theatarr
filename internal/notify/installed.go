package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
)

// fieldValue reads a field of a stored NotificationResource.
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

// Notification is an installed notification as the notifier sends to it.
type Notification struct {
	ID       int64
	View     string
	Name     string
	URL      string
	Method   string // POST or PUT
	Username string
	Password string
	Headers  map[string]string
	// On are the enabled switches (Grab, Download, Upgrade, MovieDelete, ...), with includeHealthWarnings.
	On map[string]bool
}

// ParseNotification reads a stored resource's settings.
func ParseNotification(id int64, view string, raw []byte) (*Notification, error) {
	res := map[string]any{}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	n := &Notification{ID: id, View: view, Method: http.MethodPost, Headers: map[string]string{}, On: map[string]bool{}}
	n.Name, _ = res["name"].(string)
	n.URL, _ = fieldValue(res, "url").(string)
	if n.URL == "" {
		return nil, errors.New("no url")
	}
	switch m := fieldValue(res, "method").(type) {
	case float64:
		if m == 2 {
			n.Method = http.MethodPut
		}
	case string:
		if m == "2" || strings.EqualFold(m, "put") {
			n.Method = http.MethodPut
		}
	}
	n.Username, _ = fieldValue(res, "username").(string)
	n.Password, _ = fieldValue(res, "password").(string)
	if hs, ok := fieldValue(res, "headers").([]any); ok {
		for _, h := range hs {
			if m, ok := h.(map[string]any); ok {
				k, _ := m["key"].(string)
				v, _ := m["value"].(string)
				if k != "" {
					n.Headers[k] = v
				}
			}
		}
	}
	keys := make([]string, 0, len(res))
	for k := range res {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.HasPrefix(k, "on") || k == "includeHealthWarnings" {
			if b, ok := res[k].(bool); ok && b {
				n.On[strings.TrimPrefix(k, "on")] = true
			}
		}
	}
	return n, nil
}

// LoadNotifications reads every notification installed through a view (arrview_notification).
func LoadNotifications(ctx context.Context, db *sql.DB) ([]*Notification, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, view, resource FROM arrview_notification ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var out []*Notification
	for rows.Next() {
		var id int64
		var view, raw string
		if err := rows.Scan(&id, &view, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		if n, err := ParseNotification(id, view, []byte(raw)); err == nil {
			out = append(out, n)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	return out, rows.Close()
}
