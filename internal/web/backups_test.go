package web

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
)

type fakeBackups struct {
	err       error
	requested int
}

func (f *fakeBackups) List(context.Context) (*BackupList, error) {
	return &BackupList{Sets: []BackupSet{{Name: "incomplete", WhyNot: "no manifest"}}}, nil
}
func (f *fakeBackups) Now(context.Context) (string, error) { return "safety", f.err }
func (f *fakeBackups) RequestRestore(context.Context, string, string) (string, error) {
	f.requested++
	return "safety", f.err
}
func TestSystemBackupsAPI(t *testing.T) {
	t.Parallel()
	fake := &fakeBackups{}
	restarted := make(chan time.Time, 2)
	e := newEnvWith(t, func(c *Config) { c.Backups = fake; c.Restart = func() { restarted <- time.Now() } })
	key := e.apiKey()
	post := func(confirm string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", APIBase+"/system/backups/theatarr-20261102-030000/restore", strings.NewReader(`{"confirm":"`+confirm+`"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(auth.APIKeyHeader, key)
		return e.do(r)
	}
	if rec := post("wrong"); rec.Code != 400 || fake.requested != 0 {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}
	for _, status := range []int{409, 422, 404} {
		fake.err = &BackupError{Status: status, Message: "refused"}
		if rec := post("theatarr-20261102-030000"); rec.Code != status {
			t.Fatalf("status: %d want %d", rec.Code, status)
		}
	}
	fake.err = nil
	var list BackupList
	if status := e.getKey(APIBase+"/system/backups", key, &list); status != 200 || len(list.Sets) != 1 || list.Sets[0].Restorable {
		t.Fatalf("list: %d %+v", status, list)
	}
	rec := post("theatarr-20261102-030000")
	returned := time.Now()
	if rec.Code != 202 || !rec.Flushed || !strings.Contains(rec.Body.String(), `"safety_set":"safety"`) {
		t.Fatalf("accepted: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case <-restarted:
		t.Fatal("restart before response")
	default:
	}
	select {
	case at := <-restarted:
		if at.Sub(returned) < 900*time.Millisecond {
			t.Fatal("restart was not delayed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no restart")
	}
	select {
	case <-restarted:
		t.Fatal("second restart")
	case <-time.After(50 * time.Millisecond):
	}
}
