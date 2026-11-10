package errinbox

import (
	"context"
	"encoding/json"
	"time"
)

// HealthCheck is one health check result as the inbox reads it (health.Result's source, type and message).
type HealthCheck struct {
	Source  string    `json:"source"`
	Name    string    `json:"name,omitempty"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
	At      time.Time `json:"checked_at,omitzero"`
}

// healthSnapshotEvery is how often an unchanged health snapshot is written again.
const healthSnapshotEvery = 10 * time.Minute

// ObserveHealth compares results with the previous call: a check that turned error (from ok, notice or warning, or
// at its first observation) is recorded as a health event; a check that was error and is not any more resolves that
// fingerprint automatically, noting the recovery time, unless someone ignored it or it was seen again after the
// recovery. Warnings and notices are left to the health screen. The latest results are also kept as a snapshot
// for theatarr debug bundle.
func (in *Inbox) ObserveHealth(results []HealthCheck) {
	now := in.opts.Now()
	in.mu.Lock()
	defer in.mu.Unlock()
	if !in.healthRead {
		in.healthRead = true
		in.loadOpenHealth()
	}
	for _, r := range results {
		fp, failing := in.health[r.Source]
		switch {
		case r.Type == "error" && !failing:
			ev := Event{Source: SourceHealth, Component: r.Source, Level: "error", Message: r.Message, Time: now,
				Context: map[string]string{"check": r.Source, "name": r.Name}}
			in.health[r.Source] = in.prepare(ev).fp
			in.enqueue(item{ev: ev})
		case r.Type != "error" && failing:
			delete(in.health, r.Source)
			in.enqueue(item{resolve: fp, resolveAfter: now,
				resolveNote: "recovered " + now.UTC().Format(time.RFC3339) + ": " + r.Type + " " + r.Message})
		}
	}
	b, err := json.Marshal(map[string]any{"at": now.UTC(), "checks": results})
	if err != nil {
		return
	}
	snap := string(b)
	if changed := healthDigest(results) != in.healthState; changed || now.Sub(in.healthAt) >= healthSnapshotEvery {
		in.healthState, in.healthAt = healthDigest(results), now
		in.enqueue(item{stateKey: "health", stateValue: snap})
	}
}

// loadOpenHealth fills in.health with the health rows still open from earlier runs, so a check that failed before a
// restart and is fine now resolves its row. Later rows win for a check with several.
func (in *Inbox) loadOpenHealth() {
	ctx := Quiet(context.Background())
	rows, err := in.st.Reader().QueryContext(ctx, `SELECT component, fingerprint FROM error_event
		WHERE source = ? AND status IN ('new', 'seen') ORDER BY last_seen, id`, SourceHealth)
	if err != nil {
		in.log.WarnContext(ctx, "error inbox: open health rows not read", "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var check, fp string
		if rows.Scan(&check, &fp) == nil {
			in.health[check] = fp
		}
	}
}

func healthDigest(results []HealthCheck) string {
	var s string
	for _, r := range results {
		s += r.Source + "=" + r.Type + ";"
	}
	return s
}
