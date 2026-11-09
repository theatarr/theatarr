package errinbox

import (
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

func healthDigest(results []HealthCheck) string {
	var s string
	for _, r := range results {
		s += r.Source + "=" + r.Type + ";"
	}
	return s
}
