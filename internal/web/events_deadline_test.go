package web

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

type deadlineWriter struct {
	*httptest.ResponseRecorder
	deadline time.Time
	writes   int
	t        *testing.T
}

func (w *deadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}
func (w *deadlineWriter) Write(b []byte) (int, error) {
	w.writes++
	if !w.deadline.After(time.Now()) || time.Until(w.deadline) > eventWriteTimeout {
		w.t.Fatalf("write without bounded deadline: %v", w.deadline)
	}
	return 0, errors.New("client stopped reading")
}

func TestEventsSetWriteDeadline(t *testing.T) {
	e := newEnv(t)
	rw := &deadlineWriter{ResponseRecorder: httptest.NewRecorder(), t: t}
	e.web.apiEvents(rw, httptest.NewRequest("GET", APIBase+"/events", nil))
	if rw.writes != 1 {
		t.Fatalf("stream did not stop on write error: %d writes", rw.writes)
	}
}
