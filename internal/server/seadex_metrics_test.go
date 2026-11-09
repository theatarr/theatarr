package server

import "testing"

func TestSeaDexLockWaitMetric(t *testing.T) {
	metrics, err := NewRegistry("test").Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range metrics {
		if metric.GetName() == "theatarr_seadex_lock_wait_seconds" {
			return
		}
	}
	t.Fatal("SeaDex lock wait histogram missing from server registry")
}
