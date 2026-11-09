package auth

import (
	"sort"
	"sync"
	"time"
)

// Throttle slows login guessing. Failures are counted per client address and per username, separately, so neither
// spreading guesses over many accounts nor over many addresses gets around it. The first FreeFailures failures in
// Window cost nothing; each one after that locks the key for twice as long as the last, from 2 s up to MaxLock. A
// locked key is refused before the password is checked. Login counts each attempt as a failure before checking the
// password (Reserve), so attempts sent at once are counted as they arrive. A success clears both keys. State is in memory: a restart
// forgets it, which costs an attacker a restart they cannot cause.
//
// The table holds at most MaxEntries keys. When a failure finds it full, expired entries go first; if it is still
// full, entries are evicted down to 90%: unlocked ones first, the least recently failed first, then locked ones whose
// lock ends soonest. Evicting, not failing closed: refusing every unseen key while full would let anyone who can
// send 10,000 bad logins from spread addresses or usernames lock everyone else out, the real admin included. Eviction
// costs an attacker more than it gains: a key under attack is locked and recently used, so it is the last to go, and
// pushing it out takes thousands of fresh failures, each paying for a password hash.
type Throttle struct {
	FreeFailures int
	Window       time.Duration
	MaxLock      time.Duration
	// MaxEntries caps the keys held (default 10,000).
	MaxEntries int

	mu      sync.Mutex
	entries map[string]*throttleEntry
}

type throttleEntry struct {
	failures int
	last     time.Time
	until    time.Time
}

// NewThrottle returns a throttle with the defaults: 5 free failures per 15 minutes, locks up to 15 minutes.
func NewThrottle() *Throttle {
	return &Throttle{FreeFailures: 5, Window: 15 * time.Minute, MaxLock: 15 * time.Minute,
		MaxEntries: maxThrottleEntries, entries: map[string]*throttleEntry{}}
}

const (
	maxThrottleEntries = 10000
	// maxThrottleKey: longer keys (a username is the client's input) are stored as their SHA-256.
	maxThrottleKey = 128
)

func throttleKey(k string) string {
	if len(k) > maxThrottleKey {
		return "sha256:" + sha(k)
	}
	return k
}

// Locked returns when the latest lock on any of keys ends, if one is active.
func (t *Throttle) Locked(now time.Time, keys ...string) (until time.Time, locked bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, k := range keys {
		if e := t.entries[throttleKey(k)]; e != nil && e.until.After(now) && e.until.After(until) {
			until, locked = e.until, true
		}
	}
	return until, locked
}

// Fail counts a failure on each key.
func (t *Throttle) Fail(now time.Time, keys ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fail(now, keys...)
}

// Reserve is Locked and Fail in one step: when no key is locked it counts the attempt as a failure on each key now,
// before the password is checked, so a burst of attempts cannot all pass the check while the first is still being
// verified. A success clears the count (Succeed). When a key is locked nothing is counted.
func (t *Throttle) Reserve(now time.Time, keys ...string) (until time.Time, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, k := range keys {
		if e := t.entries[throttleKey(k)]; e != nil && e.until.After(now) && e.until.After(until) {
			until = e.until
		}
	}
	if !until.IsZero() {
		return until, false
	}
	t.fail(now, keys...)
	return time.Time{}, true
}

func (t *Throttle) fail(now time.Time, keys ...string) {
	for _, k := range keys {
		k = throttleKey(k)
		e := t.entries[k]
		if e == nil && len(t.entries) >= t.cap() {
			t.prune(now)
			if len(t.entries) >= t.cap() {
				t.evict(now)
			}
		}
		if e == nil || now.Sub(e.last) > t.Window && !e.until.After(now) {
			e = &throttleEntry{}
			t.entries[k] = e
		}
		e.failures++
		e.last = now
		if over := e.failures - t.FreeFailures; over > 0 {
			lock := 2 * time.Second
			for i := 1; i < over && lock < t.MaxLock; i++ {
				lock *= 2
			}
			e.until = now.Add(min(lock, t.MaxLock))
		}
	}
}

// Succeed clears the keys.
func (t *Throttle) Succeed(keys ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, k := range keys {
		delete(t.entries, throttleKey(k))
	}
}

func (t *Throttle) cap() int {
	if t.MaxEntries > 0 {
		return t.MaxEntries
	}
	return maxThrottleEntries
}

// evict drops entries down to 90% of the cap: unlocked ones first, least recently failed first; then locked ones
// whose lock ends soonest.
func (t *Throttle) evict(now time.Time) {
	type kv struct {
		k string
		e *throttleEntry
	}
	all := make([]kv, 0, len(t.entries))
	for k, e := range t.entries {
		all = append(all, kv{k, e})
	}
	sort.Slice(all, func(i, j int) bool {
		li, lj := all[i].e.until.After(now), all[j].e.until.After(now)
		switch {
		case li != lj:
			return !li
		case li:
			return all[i].e.until.Before(all[j].e.until)
		default:
			return all[i].e.last.Before(all[j].e.last)
		}
	})
	target := t.cap() * 9 / 10
	for _, x := range all {
		if len(t.entries) <= target {
			break
		}
		delete(t.entries, x.k)
	}
}

// size is the number of keys held (tests).
func (t *Throttle) size() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}

func (t *Throttle) prune(now time.Time) {
	for k, e := range t.entries {
		if now.Sub(e.last) > t.Window && !e.until.After(now) {
			delete(t.entries, k)
		}
	}
}
