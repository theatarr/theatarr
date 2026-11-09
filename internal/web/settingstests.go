package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/settings"
)

// SettingsTester makes one outbound check of a source in a settings snapshot (saved or candidate) through the
// gateway, so the VPN guard, allowlist, budgets and back-offs apply. It returns categories only, never upstream
// text. cmd/theatarr implements it over the active runtime's gateway.
type SettingsTester interface {
	TestSource(ctx context.Context, req SettingsTestRequest) SettingsTestOutcome
}

// SettingsTestRequest is one source test.
type SettingsTestRequest struct {
	Entity, Key string
	// Snapshot holds the configuration tested: the stored head for a saved source, or an unsaved candidate.
	Snapshot *settings.Snapshot
	// Saved is the stored head a candidate was built over; nil when Snapshot is the saved source as saved.
	Saved *settings.Snapshot
	// NewSecret: the candidate request carries a credential of its own.
	NewSecret bool
}

// Candidate reports whether the request tests an unsaved configuration.
func (r SettingsTestRequest) Candidate() bool { return r.Saved != nil }

// SettingsTestOutcome is what a tester reports.
type SettingsTestOutcome struct {
	// Status is ok, failed (the source answered badly or could not be reached) or refused (the gateway did not send).
	Status string
	// Category is ok, auth, credential, http, connectivity, timeout, unsupported, or a gateway refusal reason
	// (vpn, budget, backoff, host, unknown_target, too_large).
	Category   string
	HTTPStatus int
	// Results is the number of releases an indexer probe returned.
	Results *int
	RetryAt time.Time
	Account *SettingsTestAccount
}

// SettingsTestAccount is the nonsecret part of a debrid account check.
type SettingsTestAccount struct {
	Premium bool      `json:"premium"`
	Expires time.Time `json:"expires,omitzero"`
}

// SettingsTestResult is one test in an answer.
type SettingsTestResult struct {
	ID     int64  `json:"id,omitempty"`
	Entity string `json:"entity"`
	Key    string `json:"key"`
	// Candidate marks a test of an unsaved configuration.
	Candidate bool `json:"candidate,omitempty"`
	// Status is ok, failed, refused, timeout, cooldown (tested too recently) or busy (no test slot free in time).
	Status     string               `json:"status"`
	Category   string               `json:"category"`
	Message    string               `json:"message"`
	HTTPStatus int                  `json:"http_status,omitempty"`
	LatencyMS  int64                `json:"latency_ms"`
	Results    *int                 `json:"results,omitempty"`
	Account    *SettingsTestAccount `json:"account,omitempty"`
	RetryAt    *time.Time           `json:"retry_at,omitempty"`
	TestedAt   time.Time            `json:"tested_at"`
}

var testMessages = map[string]string{
	"ok":           "reachable and the credential was accepted",
	"auth":         "the source refused the credential",
	"credential":   "no credential is set for this source",
	"http":         "the source answered with an error status",
	"connectivity": "could not reach the source",
	"dmm_provider_key": "DMM: no key linked for a provider in this path (error 102); link the TorBox key in DMM " +
		"or remove tb from the path",
	"timeout":        "no answer within the test timeout",
	"unsupported":    "this source has no test query",
	"vpn":            "refused: the VPN egress is not confirmed for this source",
	"budget":         "refused: the source's request budget has no room now",
	"backoff":        "refused: the source is backing off after errors or rate limits",
	"host":           "refused: the host is not allowed",
	"unknown_target": "refused: the gateway does not know this source",
	"too_large":      "refused: the answer was too large",
	"cooldown":       "tested recently; wait before testing again",
	"candidate_rate": "too many unsaved configurations tested recently; wait before testing again",
	"busy":           "too many tests are running; try again shortly",
	"internal":       "the test could not run",
}

// SettingsTestMessage is the message a test result carries for a category ("" for an unknown one).
func SettingsTestMessage(category string) string { return testMessages[category] }

// SettingsTestLimits bound source tests: a per-item timeout, a cap on tests running at once across every request,
// a cooldown between tests of the same source and a shared rate limit on tests of unsaved configurations. serve
// shares one value across runtime generations.
type SettingsTestLimits struct {
	// Timeout bounds one source's test, budget waits included. Default 20s.
	Timeout time.Duration
	// Concurrency caps tests running at once. Default 4.
	Concurrency int
	// Cooldown is the minimum time between two tests of the same source. A saved source is identified by its
	// record; a candidate by what the server derives from it: scheme, host, port and account (an in-memory keyed
	// hash of its credential), never the key the caller chose. Default 30s; negative disables it.
	Cooldown time.Duration
	// CandidateRate caps candidate tests per minute, shared by every caller. Default 10; negative disables it.
	CandidateRate int
	// Now replaces the clock (tests).
	Now func() time.Time

	once       sync.Once
	sem        chan struct{}
	mu         sync.Mutex
	started    map[string]time.Time
	pruned     time.Time
	candidates []time.Time // candidate test starts in the last minute
	account    []byte      // per-process key for the account hashes
	results    map[string]SettingsTestResult
}

func (l *SettingsTestLimits) init() {
	l.once.Do(func() {
		if l.Timeout <= 0 {
			l.Timeout = 20 * time.Second
		}
		if l.Concurrency <= 0 {
			l.Concurrency = 4
		}
		if l.Cooldown == 0 {
			l.Cooldown = 30 * time.Second
		}
		if l.CandidateRate == 0 {
			l.CandidateRate = 10
		}
		l.account = make([]byte, 32)
		rand.Read(l.account)
		if l.Now == nil {
			l.Now = time.Now
		}
		l.sem = make(chan struct{}, l.Concurrency)
		l.started = map[string]time.Time{}
		l.results = map[string]SettingsTestResult{}
	})
}

// reserve claims a test slot for ref unless its cooldown, or for a candidate the shared candidate rate, says
// wait. It returns the refusal category and when to retry.
func (l *SettingsTestLimits) reserve(ref string, candidate bool) (string, time.Time, bool) {
	l.init()
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	// Expire entries whose cooldown is over, at most once per cooldown period.
	if l.Cooldown <= 0 || !now.Before(l.pruned.Add(l.Cooldown)) {
		for k, t := range l.started {
			if l.Cooldown <= 0 || !now.Before(t.Add(l.Cooldown)) {
				delete(l.started, k)
			}
		}
		l.pruned = now
	}
	if l.Cooldown > 0 {
		if last, ok := l.started[ref]; ok && now.Before(last.Add(l.Cooldown)) {
			return "cooldown", last.Add(l.Cooldown), false
		}
	}
	if candidate && l.CandidateRate > 0 {
		recent := l.candidates[:0]
		for _, t := range l.candidates {
			if now.Before(t.Add(time.Minute)) {
				recent = append(recent, t)
			}
		}
		l.candidates = recent
		if len(recent) >= l.CandidateRate {
			return "candidate_rate", recent[0].Add(time.Minute), false
		}
		l.candidates = append(l.candidates, now)
	}
	if l.Cooldown > 0 {
		l.started[ref] = now
	}
	return "", time.Time{}, true
}

// candidateRef is a candidate's cooldown identity: entity, address (settings.SourceAddress) and a keyed hash of its
// credential.
// The key is random per process and the hash stays in memory, so it is never shown or stored.
func (l *SettingsTestLimits) candidateRef(entity, address, secret string) string {
	l.init()
	mac := hmac.New(sha256.New, l.account)
	mac.Write([]byte(entity + "\x00" + address + "\x00" + secret))
	return "candidate:" + entity + "/" + address + "/" + hex.EncodeToString(mac.Sum(nil)[:16])
}

func (l *SettingsTestLimits) remember(res SettingsTestResult) {
	l.mu.Lock()
	l.results[res.Entity+"/"+res.Key] = res
	l.mu.Unlock()
}

func (l *SettingsTestLimits) last(entity, key string) (SettingsTestResult, bool) {
	if l == nil {
		return SettingsTestResult{}, false
	}
	l.init()
	l.mu.Lock()
	defer l.mu.Unlock()
	res, ok := l.results[entity+"/"+key]
	return res, ok
}

type testItem struct {
	id  int64
	req SettingsTestRequest
	// ref is the cooldown identity.
	ref string
}

func savedItem(snap *settings.Snapshot, entity string, id int64, key string) testItem {
	return testItem{id: id, req: SettingsTestRequest{Entity: entity, Key: key, Snapshot: snap}, ref: "saved:" + entity + "/" + key}
}

// runTests tests items with at most Concurrency at once (shared with other requests), each bounded by Timeout and
// the cooldown. Every test is audited as an outbound action. A test never saves settings.
func (w *Web) runTests(ctx context.Context, actor string, items []testItem) []SettingsTestResult {
	l := w.settingsTests
	l.init()
	results := make([]SettingsTestResult, len(items))
	work := make(chan int)
	var wg sync.WaitGroup
	for range min(l.Concurrency, len(items)) {
		wg.Go(func() {
			for i := range work {
				results[i] = w.runTest(ctx, items[i])
			}
		})
	}
	for i := range items {
		work <- i
	}
	close(work)
	wg.Wait()
	for _, res := range results {
		if res.Status == "cooldown" || res.Status == "busy" {
			continue
		}
		candidate := res.Candidate
		if !candidate {
			l.remember(res)
		}
		op := "test"
		if candidate {
			op = "test-candidate"
		}
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err := w.settings.RecordAction(actx, actor, res.Entity, res.Key, op, map[string]any{"status": res.Status,
			"category": res.Category, "http_status": res.HTTPStatus, "latency_ms": res.LatencyMS})
		cancel()
		if err != nil {
			w.log.Warn("settings: test not audited", "entity", res.Entity, "key", res.Key, "err", err)
		}
	}
	return results
}

func (w *Web) runTest(ctx context.Context, item testItem) SettingsTestResult {
	l := w.settingsTests
	res := SettingsTestResult{ID: item.id, Entity: item.req.Entity, Key: item.req.Key, Candidate: item.req.Candidate(), TestedAt: l.Now().UTC()}
	finish := func(status, category string) SettingsTestResult {
		res.Status, res.Category, res.Message = status, category, testMessages[category]
		return res
	}
	if category, retry, ok := l.reserve(item.ref, item.req.Candidate()); !ok {
		res.RetryAt = &retry
		return finish("cooldown", category)
	}
	// Waiting for a slot counts against the item's timeout, so a request never waits longer than one item would run.
	wait := time.NewTimer(l.Timeout)
	defer wait.Stop()
	select {
	case l.sem <- struct{}{}:
	case <-wait.C:
		return finish("busy", "busy")
	case <-ctx.Done():
		return finish("busy", "busy")
	}
	itemCtx, cancel := context.WithTimeout(ctx, l.Timeout)
	defer cancel()
	done := make(chan SettingsTestOutcome, 1)
	start := time.Now()
	go func() {
		// The slot is released only when the tester returns, so the cap holds even for a tester that is slow to stop.
		defer func() { <-l.sem }()
		done <- w.settingsTester.TestSource(itemCtx, item.req)
	}()
	select {
	case out := <-done:
		res.LatencyMS = time.Since(start).Milliseconds()
		res.HTTPStatus, res.Results, res.Account = out.HTTPStatus, out.Results, out.Account
		if !out.RetryAt.IsZero() {
			t := out.RetryAt.UTC()
			res.RetryAt = &t
		}
		if itemCtx.Err() != nil && out.Status != "ok" && out.Status != "refused" {
			return finish("timeout", "timeout")
		}
		category := out.Category
		if _, known := testMessages[category]; !known {
			category = "internal"
		}
		status := out.Status
		if status != "ok" && status != "refused" {
			status = "failed"
		}
		return finish(status, category)
	case <-itemCtx.Done():
		res.LatencyMS = time.Since(start).Milliseconds()
		return finish("timeout", "timeout")
	}
}

func (w *Web) testsReady(rw http.ResponseWriter) bool {
	if w.refuseUnavailable(rw) {
		return false
	}
	if w.settingsTester == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "source tests are not available"})
		return false
	}
	return true
}

// writeTestResult answers one test; a cooldown is 429 with Retry-After.
func writeTestResult(rw http.ResponseWriter, res SettingsTestResult, now time.Time) {
	if res.Status == "cooldown" || res.Status == "busy" {
		secs := 5
		if res.RetryAt != nil {
			secs = max(1, int(res.RetryAt.Sub(now).Seconds()+0.999))
		}
		rw.Header().Set("Retry-After", strconv.Itoa(secs))
		writeJSON(rw, http.StatusTooManyRequests, res)
		return
	}
	writeJSON(rw, http.StatusOK, res)
}

// apiSettingsTest tests one saved source: POST /settings/{indexers|providers}/{id}/test. Disabled sources can be
// tested; a test never enables one.
func (w *Web) apiSettingsTest(entity string) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if !w.testsReady(rw) {
			return
		}
		id, ok := pathID(r)
		if !ok {
			notFound(rw)
			return
		}
		snap, ok := w.settingsHead(rw, r)
		if !ok {
			return
		}
		key := recordKey(snap, entity, id)
		if key == "" {
			notFound(rw)
			return
		}
		res := w.runTests(r.Context(), settingsActor(r), []testItem{savedItem(snap, entity, id, key)})
		writeTestResult(rw, res[0], w.settingsTests.Now())
	}
}

// apiSettingsTestCandidate tests an unsaved configuration: the body is a create (key) or an update (id) as for the
// write routes. The candidate is validated like a save but nothing is stored or published.
func (w *Web) apiSettingsTestCandidate(entity string) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if !w.testsReady(rw) {
			return
		}
		body, ok := decodeSettingsBody(rw, r)
		if !ok {
			return
		}
		if body.OrphanActiveDownloads != nil {
			badRequest(rw, "orphan_active_downloads does not apply to a test")
			return
		}
		snap, ok := w.settingsHead(rw, r)
		if !ok {
			return
		}
		if body.Generation != nil && *body.Generation != snap.Generation {
			writeStale(rw, snap.Generation)
			return
		}
		create := body.ID == 0
		if create && (body.Key == "" || len(body.Config) == 0) {
			badRequest(rw, "a candidate needs an id (changes to a saved record) or a key and config (a new record)")
			return
		}
		req, scrub, err := applyBody(snap, entity, body.ID, body, create)
		if err != nil {
			writeBodyError(rw, err, scrub)
			return
		}
		key := body.Key
		if !create {
			key = recordKey(snap, entity, body.ID)
		}
		req.Actor = settingsActor(r)
		candidate, err := w.settings.Candidate(r.Context(), req)
		if err != nil {
			w.settingsError(rw, r, err, scrub)
			return
		}
		// A test sends the stored credential only to the saved source's address (settings.SourceAddress, the rule a save
		// confirms by): an unsaved address is tested with a credential sent along.
		address := settings.SourceAddress(candidate.Policy, entity, key)
		if !create && body.Secret == nil && address != settings.SourceAddress(snap.Policy, entity, key) {
			writeJSON(rw, http.StatusUnprocessableEntity, apiError{Error: "send the credential again to test a new address: " +
				"a test only sends the stored credential to the saved source's address (kind, scheme, host, port, definition and hosts)"})
			return
		}
		var secret string
		_ = candidate.WithSecret(entity, key, w.settingsLookup, func(v string) error { secret = v; return nil })
		item := testItem{id: body.ID, ref: w.settingsTests.candidateRef(entity, address, secret),
			req: SettingsTestRequest{Entity: entity, Key: key, Snapshot: candidate, Saved: snap, NewSecret: body.Secret != nil}}
		res := w.runTests(r.Context(), req.Actor, []testItem{item})
		writeTestResult(rw, res[0], w.settingsTests.Now())
	}
}

// apiSettingsTestAll tests every enabled source of one kind ({"include_disabled": true} adds disabled ones),
// bounded like single tests; sources in their cooldown are reported, not tested.
func (w *Web) apiSettingsTestAll(entity string) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if !w.testsReady(rw) {
			return
		}
		var body struct {
			IncludeDisabled bool `json:"include_disabled"`
		}
		if !decodeSettingsJSON(rw, r, &body) {
			return
		}
		snap, ok := w.settingsHead(rw, r)
		if !ok {
			return
		}
		var items []testItem
		for _, rec := range w.settingsRecords(r.Context(), snap, entity) {
			if body.IncludeDisabled || rec.Enabled == nil || *rec.Enabled {
				items = append(items, savedItem(snap, entity, rec.ID, rec.Key))
			}
		}
		results := w.runTests(r.Context(), settingsActor(r), items)
		if results == nil {
			results = []SettingsTestResult{}
		}
		writeJSON(rw, http.StatusOK, map[string]any{"generation": snap.Generation, "results": results})
	}
}
