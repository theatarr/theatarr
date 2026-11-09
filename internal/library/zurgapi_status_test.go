package library

import (
	"encoding/json"
	"testing"
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
