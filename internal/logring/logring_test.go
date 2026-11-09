package logring

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRing(t *testing.T) {
	var out bytes.Buffer
	r := New(3)
	log := slog.New(r.Handler(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	web := log.With("component", "web")
	web.Info("one", "n", 1)
	log.With("component", "gateway").Warn("two", "password", "hunter2", "api_key", "theatarr_x", "key_name", "seerr")
	web.WithGroup("req").Error("three", "path", "/x")
	log.Debug("four")

	all := r.Recent(Filter{Level: slog.LevelDebug})
	if len(all) != 3 || all[0].Message != "two" || all[2].Message != "four" {
		t.Fatalf("ring kept %v", all)
	}
	if strings.Count(out.String(), "\n") != 4 {
		t.Errorf("the wrapped handler did not get every record: %s", out.String())
	}
	two := all[0]
	if two.Component != "gateway" || two.Attrs["password"] != "[redacted]" || two.Attrs["api_key"] != "[redacted]" ||
		two.Attrs["key_name"] != "seerr" {
		t.Errorf("two: %+v", two)
	}
	if all[1].Component != "web" || all[1].Attrs["req.path"] != "/x" || all[1].Level != "ERROR" {
		t.Errorf("three: %+v", all[1])
	}
	if got := r.Recent(Filter{}); len(got) != 2 {
		t.Errorf("info and up: %v", got)
	}
	if got := r.Recent(Filter{Level: slog.LevelDebug, Component: "web"}); len(got) != 1 || got[0].Message != "three" {
		t.Errorf("component web: %v", got)
	}
	if got := r.Recent(Filter{Level: slog.LevelDebug, After: all[1].Seq}); len(got) != 1 || got[0].Message != "four" {
		t.Errorf("after: %v", got)
	}
	if got := r.Recent(Filter{Level: slog.LevelDebug, Limit: 1}); len(got) != 1 || got[0].Message != "four" {
		t.Errorf("limit: %v", got)
	}
	if got := r.Recent(Filter{Level: slog.LevelDebug, Since: time.Now().Add(time.Hour)}); len(got) != 0 {
		t.Errorf("since: %v", got)
	}
	if c := r.Components(); strings.Join(c, ",") != "gateway,web" {
		t.Errorf("components %v", c)
	}
	if strings.Contains(out.String(), "[redacted]") {
		t.Error("redaction leaked into the wrapped handler")
	}
}

func TestPollLossHonorsFilters(t *testing.T) {
	r := New(2)
	r.add(Entry{Level: "INFO", Component: "web"})
	for range 5 {
		r.add(Entry{Level: "INFO", Component: "router"})
	}
	p := r.Poll(Filter{After: 1, Level: slog.LevelError, Component: "web"}, r.bootID)
	if p.Skipped != 0 {
		t.Fatalf("reported excluded logs as lost: %+v", p)
	}
}

func TestPollCountsOnlyMatchingLossAcrossPages(t *testing.T) {
	r := New(3)
	r.add(Entry{Level: "ERROR", Component: "web"})
	first := r.Poll(Filter{Level: slog.LevelError, Component: "web"}, r.bootID)
	r.add(Entry{Level: "ERROR", Component: "web"}) // overwritten matching record
	for range 5 {
		r.add(Entry{Level: "INFO", Component: "router"})
	}
	p := r.Poll(Filter{After: first.After, Seen: &first.Seen, Level: slog.LevelError, Component: "web"}, r.bootID)
	if p.Skipped != 1 || p.Seen != 2 {
		t.Fatalf("filtered loss: %+v", p)
	}
	p2 := r.Poll(Filter{After: p.After, Seen: &p.Seen, Level: slog.LevelError, Component: "web"}, r.bootID)
	if p2.Skipped != 0 {
		t.Fatalf("counted loss twice: %+v", p2)
	}
	restarted := New(3).Poll(Filter{After: p.After, Seen: &p.Seen, Level: slog.LevelError}, r.bootID)
	if restarted.Skipped != 0 || restarted.Seen != 0 {
		t.Fatalf("restart kept old counts: %+v", restarted)
	}
}
