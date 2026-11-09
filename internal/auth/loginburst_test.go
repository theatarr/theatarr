package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// slowHashing makes hashes (and the dummy hash for unknown names) slow enough that a burst of logins overlaps.
func slowHashing(t *testing.T) {
	activeArgon = argonParams{memory: 8 * 1024, time: 8, threads: 1, keyLen: 32}
	t.Cleanup(func() { activeArgon = testArgon })
}

// burst runs n logins at once and counts the answers.
func burst(s *Service, n int, login func(i int) (string, string, string)) (invalid, throttled int, other []error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			user, pw, addr := login(i)
			<-start
			_, err := s.Login(context.Background(), user, pw, addr, "")
			var thr *Throttled
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, ErrInvalid):
				invalid++
			case errors.As(err, &thr):
				throttled++
			default:
				other = append(other, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	return
}

// Password checks are bounded: a burst of unknown names from different addresses (no throttle key repeats) does
// not run one 64 MiB hash each at once; the logins over the bound get the throttled answer at once.
func TestLoginBurstBounded(t *testing.T) {
	slowHashing(t)
	st := openStore(t)
	s, _ := newService(t, st, &clock{t: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)})
	const n = 16
	invalid, throttled, other := burst(s, n, func(i int) (string, string, string) {
		return fmt.Sprintf("nobody%d", i), "guess", fmt.Sprintf("192.0.2.%d", i+1)
	})
	if len(other) > 0 {
		t.Fatalf("unexpected answers: %v", other)
	}
	if invalid > n/2 || throttled < n/2 {
		t.Errorf("%d of %d concurrent logins ran a password check (%d throttled); want at most a few", invalid, n,
			throttled)
	}
}

// The attempt is counted before the hash: with no free failures, two logins at once for one account and address
// cannot both reach the check.
func TestLoginReservesAttempt(t *testing.T) {
	slowHashing(t)
	st := openStore(t)
	c := &clock{t: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	s, _ := newService(t, st, c)
	s.thr.FreeFailures = 0
	for _, user := range []string{"admin", "nobody"} {
		invalid, throttled, other := burst(s, 2, func(int) (string, string, string) { return user, "guess", "192.0.2.7" })
		if len(other) > 0 || invalid != 1 || throttled != 1 {
			t.Errorf("%s: %d checked, %d throttled, %v; want one each", user, invalid, throttled, other)
		}
		c.add(time.Hour)
	}
}
