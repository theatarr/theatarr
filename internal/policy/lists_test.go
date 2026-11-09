package policy

import (
	"testing"
	"time"
)

func TestListSettingsDefaultsAndMinimum(t *testing.T) {
	t.Parallel()
	p := &Policy{}
	if got := p.ListSettings().Interval; got != DefaultListInterval {
		t.Fatalf("default interval %v, want %v", got, DefaultListInterval)
	}
	p.Metadata = &Metadata{Lists: &Lists{Interval: Duration(time.Hour)}}
	if got := p.ListSettings().Interval; got != time.Hour {
		t.Fatalf("configured interval %v", got)
	}
	p.Metadata.Lists.Interval = Duration(time.Minute)
	e := &Errors{}
	p.validateLists(e)
	if err := e.err(); err == nil {
		t.Fatal("short list interval accepted")
	}
	p.Metadata.Lists.Interval = Duration(DefaultListInterval)
	e = &Errors{}
	p.validateLists(e)
	if err := e.err(); err != nil {
		t.Fatal(err)
	}
}
