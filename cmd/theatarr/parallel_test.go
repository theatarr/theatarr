package main

import (
	"testing"
	"time"
)

func TestParseCompareWindow(t *testing.T) {
	for _, tc := range []struct {
		text string
		want time.Duration
	}{{"7d", 168 * time.Hour}, {"36h", 36 * time.Hour}, {"1.5d", 36 * time.Hour}} {
		got, err := parseCompareWindow(tc.text)
		if err != nil || got != tc.want {
			t.Errorf("%s = %v, %v; want %v", tc.text, got, err, tc.want)
		}
	}
	for _, text := range []string{"bad", "7dd", "1h2d", "999999999d"} {
		if _, err := parseCompareWindow(text); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
}
