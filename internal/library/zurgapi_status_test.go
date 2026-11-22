package library

import (
	"encoding/json"
	"testing"
	"time"
)

func TestZurgBlockedReadStatus(t *testing.T) {
	for _, value := range []string{`"429"`, `429`} {
		var stats ZurgStats
		if err := json.Unmarshal([]byte(`{"active_streams":2,"blocked_reads":[{"path":"movie.mkv","status":`+value+`,"reason":"busy","attempts":3}]}`), &stats); err != nil {
			t.Fatal(err)
		}
		if stats.ActiveStreams != 2 || len(stats.BlockedReads) != 1 || stats.BlockedReads[0].Status != "429" || stats.BlockedReads[0].Reason != "busy" {
			t.Fatalf("lost stats: %+v", stats)
		}
	}
}

// zurg 48c6d3c adds a failure object to a blocked read; it is read when there, its code and retry time in either
// form, and a value it cannot read leaves the field empty rather than losing the stats.
func TestZurgBlockedReadFailure(t *testing.T) {
	for _, tc := range []struct{ failure, code, retry string }{
		{`{"scope":"account","provider":"rd","next_retry":"2026-11-21T14:05:00Z","provider_code":"too_many_requests"}`, "too_many_requests", "2026-11-21T14:05:00Z"},
		{`{"scope":"file","provider":"ad","provider_code":34}`, "34", ""},
		{`{"scope":"account","provider":"tb","next_retry":1763733900}`, "", ""},
	} {
		var stats ZurgStats
		if err := json.Unmarshal([]byte(`{"blocked_reads":[{"path":"movie.mkv","status":"blocked","failure":`+tc.failure+`}]}`), &stats); err != nil {
			t.Fatal(err)
		}
		f := stats.BlockedReads[0].Failure
		retry := ""
		if f != nil && !f.NextRetry.IsZero() {
			retry = f.NextRetry.UTC().Format(time.RFC3339)
		}
		if f == nil || f.Scope == "" || f.Provider == "" || f.ProviderCode != tc.code || retry != tc.retry {
			t.Errorf("%s: %+v", tc.failure, f)
		}
	}
	var stats ZurgStats
	if err := json.Unmarshal([]byte(`{"blocked_reads":[{"path":"movie.mkv","status":"blocked"}]}`), &stats); err != nil ||
		stats.BlockedReads[0].Failure != nil {
		t.Fatalf("without failure: %+v %v", stats, err)
	}
}
