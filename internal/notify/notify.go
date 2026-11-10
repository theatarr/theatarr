// Package notify sends outbound webhooks (docs/design/devices.md): the arr-shaped payloads of each view
// (Grab, Download/Upgrade, Rename, MovieDelete/SeriesDelete, MovieFileDelete/EpisodeFileDelete, Health/HealthRestored,
// SeriesAdd/MovieAdded, ManualInteractionRequired, Test) to
// the notifications clients installed through that view (Ruddarr's push relay) and to the policy's targets, and
// Theatarr's own per-version events (eventType theatarr.*) to policy targets that ask for them. Targets with a
// message format (discord, ntfy) get Theatarr's own messages instead (message.go).
//
// # Never blocking
//
// Emit only writes deliveries to the outbox (notify_delivery, migration 00020); a sender goroutine (Run) posts them
// and retries failures with exponential back-off (30 s doubling, at most 1 h, notify.retries times). Pipeline
// events (a grab when a provider took a download, a download or upgrade when the library linked one) are found by
// polling the database. The common library rename writer records moves transactionally for the scanner.
// Downloads and title adds older than the notifier's first run are never announced.
//
// # Gates
//
// Every request goes through the gateway (class webhook): the host must be a policy target's or listed in
// notify.allow_hosts, and budgets and back-offs apply. With notify.enabled false (the default) deliveries are
// recorded as dropped and nothing leaves the process. An installed notification whose host is not allowed is
// dropped too, with the reason.
package notify

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Doer is the gateway.
type Doer interface {
	Do(ctx context.Context, t gateway.Target, req *http.Request) (*http.Response, error)
}

// Payloader builds the arr-shaped bodies of an event per view (arrview.Service).
type Payloader interface {
	Payloads(ctx context.Context, ev Event) ([]Payload, error)
}

// Config is what New needs.
type Config struct {
	Store    *store.Store
	Policy   *policy.Policy
	Doer     Doer
	Payloads Payloader
	// Health lists the current health messages (the views' health); nil: no health events.
	Health func(ctx context.Context) []HealthItem
	Logger *slog.Logger
	Now    func() time.Time
	// Getenv reads the targets' auth_header_env. Default: the Getenv of the settings generation in force (PolicyFor).
	Getenv func(string) string
	// PolicyFor is the policy of the settings generation an operation runs under: notify.enabled and the targets
	// change on a settings reload without a restart. nil, or a nil answer, uses Policy.
	PolicyFor func(context.Context) *policy.Policy
	// Jobs queues the daily digest (DigestJob; RegisterJobs registers its handler). nil: no digest.
	Jobs *jobs.Queue
}

// Notifier records and sends webhooks.
type Notifier struct {
	st     *store.Store
	pol    *policy.Policy
	set    policy.NotifySettings
	doer   Doer
	pay    Payloader
	health func(ctx context.Context) []HealthItem
	log    *slog.Logger
	now    func() time.Time
	getenv func(string) string
	polFor func(context.Context) *policy.Policy
	q      *jobs.Queue
}

// New builds the notifier.
func New(cfg Config) (*Notifier, error) {
	if cfg.Store == nil || cfg.Policy == nil {
		return nil, errors.New("notify: store and policy are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Notifier{st: cfg.Store, pol: cfg.Policy, set: cfg.Policy.NotifySettings(), doer: cfg.Doer,
		pay: cfg.Payloads, health: cfg.Health, log: cfg.Logger.With("component", "notify"), now: cfg.Now,
		getenv: cfg.Getenv, polFor: cfg.PolicyFor, q: cfg.Jobs}, nil
}

func (n *Notifier) enabled(ctx context.Context) bool {
	if n.polFor != nil {
		if p := n.polFor(ctx); p != nil {
			return p.Notify != nil && p.Notify.Enabled
		}
	}
	return n.set.Enabled
}

// policy is the policy of the settings generation in force for ctx (Config.PolicyFor), else the startup one.
func (n *Notifier) policy(ctx context.Context) *policy.Policy {
	if n.polFor != nil {
		if p := n.polFor(ctx); p != nil {
			return p
		}
	}
	return n.pol
}

// settings is the notify section in force for ctx: its targets, events, formats, allow-hosts, retries and poll
// follow a settings save without a restart.
func (n *Notifier) settings(ctx context.Context) policy.NotifySettings {
	if n.polFor != nil {
		if p := n.polFor(ctx); p != nil {
			return p.NotifySettings()
		}
	}
	return n.set
}

// LogState says at start whether webhooks leave the process.
func (n *Notifier) LogState() {
	if !n.enabled(context.Background()) {
		n.log.Warn("WEBHOOKS DISABLED: notify.enabled is false; events are recorded as dropped, nothing is sent")
		return
	}
	set := n.settings(context.Background())
	keys := make([]string, 0, len(set.Targets))
	for k := range set.Targets {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	n.log.Info("webhooks enabled", "targets", keys, "allow_hosts", set.AllowHosts)
}

// delivery is one row to write.
type delivery struct {
	target, event, view string
	body                any
}

// Emit records the deliveries of an event: the arr-shaped payloads of each view to the notifications installed in
// it and the policy targets listing it, and the native payload to the native targets. It never sends; errors are
// logged, never returned (the pipeline goes on).
func (n *Notifier) Emit(ctx context.Context, ev Event) {
	if ev.At.IsZero() {
		ev.At = n.now()
	}
	ds, err := n.deliveries(ctx, ev)
	if err == nil {
		err = n.record(ctx, ds)
	}
	if err != nil {
		n.log.Warn("webhook event not recorded", "event", ev.Kind, "title", ev.TitleID, "err", err)
	}
}

func (n *Notifier) deliveries(ctx context.Context, ev Event) ([]delivery, error) {
	var out []delivery
	set := n.settings(ctx)
	if ev.Kind == ManualInteractionRequired {
		switch ev.Reason {
		case "zurg has not confirmed the download's files", "Episode mapping requires review":
		default:
			ev.Reason = "Import held for review"
		}
	}
	if ev.Kind == Health || ev.Kind == HealthRestored || ev.Kind == SeriesAdd || ev.Kind == MovieAdded || ev.Kind == Rename {
		ev.Reason = ""
	}
	if ev.Request == nil && ev.Error == nil && ev.Digest == nil && n.pay != nil && !(ev.Kind == Test && ev.Target != "") {
		ps, err := n.pay.Payloads(ctx, ev)
		if err != nil {
			return nil, err
		}
		installed, err := LoadNotifications(ctx, n.st.Reader())
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			for _, in := range installed {
				if in.View != p.View || ev.NotificationID > 0 && in.ID != ev.NotificationID {
					continue
				}
				if (ev.Kind == Health || ev.Kind == HealthRestored) && len(ev.Health) > 0 {
					h := ev.Health[0]
					if h.Type != "error" && (h.Type != "warning" || !in.On["includeHealthWarnings"]) {
						continue
					}
				}
				if p.Flag == "" || in.On[strings.TrimPrefix(p.Flag, "on")] {
					out = append(out, delivery{target: "notification:" + strconv.FormatInt(in.ID, 10),
						event: p.EventType, view: p.View, body: p.Body})
				}
			}
			if ev.NotificationID > 0 {
				continue
			}
			for _, k := range sortedKeys(set.Targets) {
				t := set.Targets[k]
				if slices.Contains(t.Views, p.View) && wants(t, ev.Kind) {
					out = append(out, delivery{target: "policy:" + k, event: p.EventType, view: p.View,
						body: p.Body})
				}
			}
		}
	}
	if ev.NotificationID > 0 {
		return out, nil
	}
	var native, discord, ntfy any
	for _, k := range sortedKeys(set.Targets) {
		t := set.Targets[k]
		isDiscord, isNtfy := t.Format == policy.NotifyFormatDiscord, t.Format == policy.NotifyFormatNtfy
		isMessage := isDiscord || isNtfy
		// Errors and the digest go to message targets (Discord, ntfy) only; a message target gets only its kinds.
		if isMessage && !messageWants(ev) || !isMessage && (!t.Native || ev.Error != nil || ev.Digest != nil) {
			continue
		}
		if !wants(t, ev.Kind) || ev.Target != "" && ev.Target != k {
			continue
		}
		if len(t.Versions) > 0 && ev.Version != "" && !slices.Contains(t.Versions, ev.Version) {
			continue
		}
		if len(t.Versions) > 0 && ev.Request != nil {
			match := false
			for _, v := range ev.Request.Versions {
				if slices.Contains(t.Versions, v.Key) {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		if isDiscord {
			if discord == nil {
				b, err := n.discordPayload(ctx, ev)
				if err != nil {
					return nil, err
				}
				discord = b
			}
			out = append(out, delivery{target: "policy:" + k, event: "discord." + string(ev.Kind), body: discord})
			continue
		}
		if isNtfy {
			if ntfy == nil {
				b, err := n.ntfyPayload(ctx, ev)
				if err != nil {
					return nil, err
				}
				ntfy = b
			}
			out = append(out, delivery{target: "policy:" + k, event: "ntfy." + string(ev.Kind), body: ntfy})
			continue
		}
		if native == nil {
			b, err := n.nativePayload(ctx, ev)
			if err != nil {
				return nil, err
			}
			native = b
		}
		out = append(out, delivery{target: "policy:" + k, event: "theatarr." + string(ev.Kind),
			body: native})
	}
	return out, nil
}

func wants(t *policy.NotifyTarget, k Kind) bool {
	return len(t.Events) == 0 || slices.Contains(t.Events, k.PolicyEvent()) || k == Test
}

func (n *Notifier) record(ctx context.Context, ds []delivery) error {
	if len(ds) == 0 {
		return nil
	}
	return n.st.WriteTx(ctx, func(tx *sql.Tx) error {
		return n.recordTx(ctx, tx, ds)
	})
}

func (n *Notifier) recordTx(ctx context.Context, tx *sql.Tx, ds []delivery) error {
	now := store.Millis(n.now())
	state, why := "pending", ""
	if !n.enabled(ctx) {
		state, why = "dropped", "notify.enabled is false"
	}
	for _, d := range ds {
		b, err := json.Marshal(d.body)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notify_delivery (target, event, view, payload, state,
				next_at, last_error, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, d.target, d.event, d.view,
			string(b), state, now, why, now); err != nil {
			return err
		}
	}
	return nil
}

// nativePayload is Theatarr's own event body: the version, the title, the download, episodes, file, reason, health.
func (n *Notifier) nativePayload(ctx context.Context, ev Event) (map[string]any, error) {
	b := map[string]any{"eventType": "theatarr." + string(ev.Kind), "instanceName": "theatarr",
		"date": ev.At.UTC().Format(time.RFC3339)}
	if ev.Version != "" {
		b["version"] = ev.Version
	}
	if ev.Reason != "" {
		b["reason"] = ev.Reason
	}
	if ev.Request != nil {
		requestPayload(b, ev)
	}
	if len(ev.Health) > 0 || ev.Kind == Health {
		b["health"] = append([]HealthItem{}, ev.Health...)
	}
	db := n.st.Reader()
	if ev.TitleID > 0 {
		var kind, name, imdb string
		var year, tmdb, tvdb int
		err := db.QueryRowContext(ctx, `SELECT kind, name, COALESCE(year, 0), COALESCE(tmdb_id, 0), COALESCE(tvdb_id, 0),
			COALESCE(imdb_id, '') FROM title WHERE id = ?`, ev.TitleID).Scan(&kind, &name, &year, &tmdb, &tvdb, &imdb)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		b["title"] = map[string]any{"id": ev.TitleID, "kind": kind, "name": name, "year": year, "tmdbId": tmdb,
			"tvdbId": tvdb, "imdbId": imdb}
	}
	if ev.DownloadID > 0 {
		var rel, idx, provider, hash, quality, state string
		var score int
		err := db.QueryRowContext(ctx, `SELECT release_title, indexer, COALESCE(provider, ''), info_hash, quality, score,
			state FROM download WHERE id = ?`, ev.DownloadID).Scan(&rel, &idx, &provider, &hash, &quality, &score, &state)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		b["download"] = map[string]any{"quality": quality, "release": rel, "indexer": idx, "infoHash": hash,
			"id": ev.DownloadID, "provider": provider, "score": score, "state": state}
	}
	if len(ev.Episodes) > 0 {
		args := make([]any, len(ev.Episodes))
		for i, id := range ev.Episodes {
			args[i] = id
		}
		rows, err := db.QueryContext(ctx, `SELECT id, season, number FROM episode WHERE id IN (`+
			store.Placeholders(len(args))+`) ORDER BY season, number`, args...)
		if err != nil {
			return nil, err
		}
		var eps []map[string]any
		for rows.Next() {
			var id int64
			var s, e int
			if rows.Scan(&id, &s, &e) == nil {
				eps = append(eps, map[string]any{"id": id, "season": s, "number": e})
			}
		}
		rows.Close()
		b["episodes"] = eps
	}
	if ev.File != nil {
		b["file"] = map[string]any{"quality": ev.File.Quality, "path": ev.File.Path, "size": ev.File.Size}
	}
	return b, nil
}

// TestNotification sends the view's Test payload to an installed notification's settings (raw is its
// NotificationResource) right away, as the arrs' Test does. The policy gates apply.
func (n *Notifier) TestNotification(ctx context.Context, view string, raw json.RawMessage) error {
	if !n.enabled(ctx) {
		return errors.New("notify.enabled is false")
	}
	in, err := ParseNotification(0, view, raw)
	if err != nil {
		return err
	}
	if n.pay == nil {
		return errors.New("no payloads")
	}
	ps, err := n.pay.Payloads(ctx, Event{Kind: Test, View: view, At: n.now()})
	if err != nil || len(ps) == 0 {
		return fmt.Errorf("no test payload for view %s", view)
	}
	b, _ := json.Marshal(ps[0].Body)
	_, err = n.sendInstalled(ctx, in, b)
	return err
}

// Test records the Test payload for one policy target (the native body for a native target, and each of its
// views' bodies). It is sent with the next round.
func (n *Notifier) Test(ctx context.Context, target string) error {
	t := n.settings(ctx).Targets[target]
	if t == nil {
		return fmt.Errorf("notify: no target %q", target)
	}
	ds, err := n.testDeliveries(ctx, target, t)
	if err != nil {
		return err
	}
	return n.record(ctx, ds)
}

// testDeliveries are the Test bodies of one policy target: each of its views', the native one for a native target,
// and the test message for a Discord or ntfy target.
func (n *Notifier) testDeliveries(ctx context.Context, target string, t *policy.NotifyTarget) ([]delivery, error) {
	var ds []delivery
	if t.Format == policy.NotifyFormatDiscord {
		b, err := n.discordPayload(ctx, Event{Kind: Test, At: n.now()})
		if err != nil {
			return nil, err
		}
		return []delivery{{target: "policy:" + target, event: "discord.test", body: b}}, nil
	}
	if t.Format == policy.NotifyFormatNtfy {
		b, err := n.ntfyPayload(ctx, Event{Kind: Test, At: n.now()})
		if err != nil {
			return nil, err
		}
		return []delivery{{target: "policy:" + target, event: "ntfy.test", body: b}}, nil
	}
	if n.pay != nil {
		for _, v := range t.Views {
			ps, err := n.pay.Payloads(ctx, Event{Kind: Test, View: v, At: n.now()})
			if err != nil {
				return nil, err
			}
			for _, p := range ps {
				ds = append(ds, delivery{target: "policy:" + target, event: p.EventType, view: p.View, body: p.Body})
			}
		}
	}
	if t.Native {
		b, err := n.nativePayload(ctx, Event{Kind: Test, At: n.now()})
		if err != nil {
			return nil, err
		}
		ds = append(ds, delivery{target: "policy:" + target, event: "theatarr.test", body: b})
	}
	return ds, nil
}

// SendTest posts a policy target's Test bodies right away (the settings page's Send test), outside the outbox, and
// returns the first failure. notify.enabled and the gateway's gates apply.
func (n *Notifier) SendTest(ctx context.Context, target string) error {
	if !n.enabled(ctx) {
		return errors.New("notifications are off (notify.enabled is false)")
	}
	t := n.settings(ctx).Targets[target]
	if t == nil {
		return fmt.Errorf("no saved destination %q", target)
	}
	ds, err := n.testDeliveries(ctx, target, t)
	if err != nil {
		return err
	}
	for _, d := range ds {
		b, err := json.Marshal(d.body)
		if err != nil {
			return err
		}
		if _, err := n.sendPolicy(ctx, target, b); err != nil {
			return err
		}
	}
	return nil
}

// errStatus is a non-2xx answer.
type errStatus struct{ code int }

func (e errStatus) Error() string { return "status " + strconv.Itoa(e.code) }

// sendInstalled posts a body to an installed notification (basic auth from username/password, its headers).
func (n *Notifier) sendInstalled(ctx context.Context, in *Notification, body []byte) (int, error) {
	u, err := url.Parse(in.URL)
	if err != nil || u.Host == "" {
		return 0, errors.New("bad url")
	}
	if !n.policy(ctx).NotifyHostAllowed(u) {
		return 0, errNotAllowed{host: u.Host}
	}
	req, err := http.NewRequestWithContext(ctx, in.Method, in.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	for k, v := range in.Headers {
		req.Header.Set(k, v)
	}
	if in.Username != "" || in.Password != "" {
		req.SetBasicAuth(in.Username, in.Password)
	}
	return n.do(ctx, gateway.Target{Class: gateway.ClassWebhook, Key: gateway.InstalledWebhookKey}, req)
}

type errNotAllowed struct{ host string }

func (e errNotAllowed) Error() string { return "host " + e.host + " is not in notify.allow_hosts" }

func (n *Notifier) sendPolicy(ctx context.Context, key string, body []byte) (int, error) {
	// The target and its credential come from one generation: the one in force for ctx.
	p := n.policy(ctx)
	t := p.NotifySettings().Targets[key]
	if t == nil {
		return 0, errGone
	}
	// An ntfy target is POSTed at its server address, with the topic of its URL in the body.
	isNtfy, to := t.Format == policy.NotifyFormatNtfy, t.URL
	if isNtfy {
		var err error
		if to, body, err = ntfyRequest(t.URL, body); err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, to, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	getenv := p.Getenv
	if n.getenv != nil {
		getenv = n.getenv
	}
	if t.AuthHeaderEnv != "" {
		if v := getenv(t.AuthHeaderEnv); v != "" {
			if isNtfy {
				v = ntfyAuthorization(v)
			}
			req.Header.Set("Authorization", v)
		}
	}
	return n.do(ctx, gateway.Target{Class: gateway.ClassWebhook, Key: key}, req)
}

var errGone = errors.New("target gone")

func (n *Notifier) do(ctx context.Context, t gateway.Target, req *http.Request) (int, error) {
	if n.doer == nil {
		return 0, errors.New("no gateway")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := n.doer.Do(ctx, t, req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, errStatus{resp.StatusCode}
	}
	return resp.StatusCode, nil
}

// backoff is the wait before retry number attempt (1-based): 30 s doubling, at most 1 h.
func backoff(attempt int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < attempt && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

// SendDue posts the deliveries that are due, oldest first (at most limit), and reports how many were sent.
func (n *Notifier) SendDue(ctx context.Context, limit int) (int, error) {
	now := n.now()
	rows, err := n.st.Reader().QueryContext(ctx, `SELECT id, target, payload, attempts FROM notify_delivery
		WHERE state = 'pending' AND next_at <= ? ORDER BY next_at, id LIMIT ?`, store.Millis(now), limit)
	if err != nil {
		return 0, err
	}
	type due struct {
		id       int64
		target   string
		payload  []byte
		attempts int
	}
	var ds []due
	for rows.Next() {
		var d due
		var p string
		if err := rows.Scan(&d.id, &d.target, &p, &d.attempts); err != nil {
			rows.Close()
			return 0, err
		}
		d.payload = []byte(p)
		ds = append(ds, d)
	}
	rows.Close()
	sent := 0
	var installed map[int64]*Notification
	for _, d := range ds {
		if ctx.Err() != nil {
			return sent, ctx.Err()
		}
		if !n.enabled(ctx) {
			n.finish(ctx, d.id, "dropped", 0, "notify.enabled is false", d.attempts)
			continue
		}
		var code int
		var err error
		kind, ref, _ := strings.Cut(d.target, ":")
		switch kind {
		case "notification":
			if installed == nil {
				all, lerr := LoadNotifications(ctx, n.st.Reader())
				if lerr != nil {
					return sent, lerr
				}
				installed = map[int64]*Notification{}
				for _, x := range all {
					installed[x.ID] = x
				}
			}
			id, _ := strconv.ParseInt(ref, 10, 64)
			if in := installed[id]; in != nil {
				code, err = n.sendInstalled(ctx, in, d.payload)
			} else {
				err = errGone
			}
		case "policy":
			code, err = n.sendPolicy(ctx, ref, d.payload)
		default:
			err = errGone
		}
		var na errNotAllowed
		switch {
		case err == nil:
			sent++
			n.finish(ctx, d.id, "sent", code, "", d.attempts+1)
		case errors.Is(err, errGone), errors.As(err, &na):
			n.finish(ctx, d.id, "dropped", code, err.Error(), d.attempts)
		default:
			attempts := d.attempts + 1
			if attempts > n.settings(ctx).Retries {
				n.finish(ctx, d.id, "failed", code, err.Error(), attempts)
				n.log.Warn("webhook failed", "delivery", d.id, "target", d.target, "attempts", attempts, "err", err)
				continue
			}
			next := now.Add(backoff(attempts))
			var ref *gateway.Refused
			if errors.As(err, &ref) && ref.RetryAt.After(next) {
				next = ref.RetryAt
			}
			_, uerr := n.st.Writer().ExecContext(ctx, `UPDATE notify_delivery SET attempts = ?, next_at = ?,
				last_status = ?, last_error = ? WHERE id = ?`, attempts, store.Millis(next), nullCode(code),
				err.Error(), d.id)
			if uerr != nil {
				return sent, uerr
			}
		}
	}
	return sent, nil
}

func nullCode(c int) any {
	if c == 0 {
		return nil
	}
	return c
}

func (n *Notifier) finish(ctx context.Context, id int64, state string, code int, why string, attempts int) {
	var sentAt any
	if state == "sent" {
		sentAt = store.Millis(n.now())
	}
	if _, err := n.st.Writer().ExecContext(ctx, `UPDATE notify_delivery SET state = ?, last_status = ?, last_error = ?,
		attempts = ?, sent_at = ? WHERE id = ?`, state, nullCode(code), why, attempts, sentAt, id); err != nil {
		n.log.Warn("webhook delivery update", "id", id, "err", err)
	}
}

// Scan finds the pipeline events since the last scan and emits them: a grab when a provider took one of our
// downloads (added_at), a download or upgrade when the library linked one (imported_at; upgrade when it moved an
// older file aside), and a health change. Each is emitted once (notify_event).
func (n *Notifier) Scan(ctx context.Context) error {
	since, err := n.since(ctx)
	if err != nil {
		return err
	}
	db := n.st.Reader()
	type dl struct {
		id, title int64
		version   string
		eps       []int64
	}
	read := func(q string) ([]dl, error) {
		rows, err := db.QueryContext(ctx, q, since)
		if err != nil {
			return nil, err
		}
		var out []dl
		for rows.Next() {
			var d dl
			var eps string
			if err := rows.Scan(&d.id, &d.title, &d.version, &eps); err != nil {
				rows.Close()
				return nil, err
			}
			_ = json.Unmarshal([]byte(eps), &d.eps)
			out = append(out, d)
		}
		return out, rows.Close()
	}
	grabs, err := read(`SELECT id, title_id, version_key, episodes FROM download d WHERE added_at >= ?
		AND NOT EXISTS (SELECT 1 FROM notify_event e WHERE e.kind = 'grab' AND e.ref = d.id) ORDER BY id`)
	if err != nil {
		return err
	}
	// The claim and the deliveries commit together (emitOnce), so an event whose preparation fails is
	// tried again at the next scan instead of being marked sent. One failing event does not hold up the others.
	for _, d := range grabs {
		ev := Event{Kind: Grab, TitleID: d.title, Version: d.version, DownloadID: d.id, Episodes: d.eps}
		if err := n.emitOnce(ctx, "grab", d.id, ev); err != nil {
			n.log.Warn("webhook event not recorded; retried at the next scan", "event", ev.Kind, "download", d.id, "err", err)
		}
	}
	imports, err := read(`SELECT id, title_id, version_key, episodes FROM download d WHERE imported_at >= ?
		AND state = 'imported' AND NOT EXISTS (SELECT 1 FROM notify_event e WHERE e.kind = 'download' AND e.ref = d.id)
		ORDER BY id`)
	if err != nil {
		return err
	}
	for _, d := range imports {
		var upg int
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM library_action WHERE download_id = ? AND kind = 'replace'
			AND mode = 'applied'`, d.id).Scan(&upg)
		k := Download
		if upg > 0 {
			k = Upgrade
		}
		ev := Event{Kind: k, TitleID: d.title, Version: d.version, DownloadID: d.id, Episodes: d.eps}
		if err := n.emitOnce(ctx, "download", d.id, ev); err != nil {
			n.log.Warn("webhook event not recorded; retried at the next scan", "event", ev.Kind, "download", d.id, "err", err)
		}
	}
	if err := n.scanAdds(ctx, since); err != nil {
		return err
	}
	if err := n.scanRenames(ctx); err != nil {
		return err
	}
	if err := n.scanHolds(ctx, since); err != nil {
		return err
	}
	if err := n.scanErrors(ctx); err != nil {
		return err
	}
	return n.scanHealth(ctx)
}

func (n *Notifier) scanHealth(ctx context.Context) error {
	if n.health == nil {
		return nil
	}
	items := n.health(ctx)
	var raw string
	err := n.st.Reader().QueryRowContext(ctx, `SELECT value FROM notify_state WHERE key = 'health_checks'`).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	var previous []HealthItem
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &previous); err != nil {
			return err
		}
	}
	current, old := map[string]HealthItem{}, map[string]HealthItem{}
	for _, h := range items {
		if h.Type != "ok" {
			current[h.Source] = h
		}
	}
	for _, h := range previous {
		old[h.Source] = h
	}
	var events []Event
	for _, source := range sortedKeys(current) {
		h := current[source]
		if p, ok := old[source]; !ok || p.Type != h.Type {
			ev := Event{Kind: Health, Health: []HealthItem{h}, At: n.now()}
			if ok {
				ev.HealthWas = &p
			}
			events = append(events, ev)
		}
	}
	for _, source := range sortedKeys(old) {
		if _, ok := current[source]; !ok {
			events = append(events, Event{Kind: HealthRestored, Health: []HealthItem{old[source]}, At: n.now()})
		}
	}
	// Save moving counts and ages for recovery without announcing them every poll.
	latest := make([]HealthItem, 0, len(current))
	for _, source := range sortedKeys(current) {
		latest = append(latest, current[source])
	}
	data, err := json.Marshal(latest)
	if err != nil {
		return err
	}
	var ds []delivery
	for _, ev := range events {
		out, err := n.deliveries(ctx, ev)
		if err != nil {
			return err
		}
		ds = append(ds, out...)
	}
	return n.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := n.recordTx(ctx, tx, ds); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO notify_state (key, value, updated_at) VALUES ('health_checks', ?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			string(data), store.Millis(n.now()))
		return err
	})
}

// since is when the notifier first ran: older downloads are never announced.
func (n *Notifier) since(ctx context.Context) (int64, error) {
	var v string
	err := n.st.Reader().QueryRowContext(ctx, `SELECT value FROM notify_state WHERE key = 'since'`).Scan(&v)
	if err == sql.ErrNoRows {
		now := store.Millis(n.now())
		err := n.st.WriteTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO notify_state (key, value, updated_at) VALUES ('since', ?, ?)`,
				strconv.FormatInt(now, 10), now); err != nil {
				return err
			}
			// Existing titles are the baseline, including ones created in the same millisecond as startup.
			_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO notify_event (kind, ref, created_at)
				SELECT (CASE t.kind WHEN 'movie' THEN 'movie_added:' ELSE 'series_add:' END) || v.policy_key,
				v.title_id, ? FROM version v JOIN title t ON t.id = v.title_id`, now)
			return err
		})
		return now, err
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

// Run scans and sends every notify.poll until ctx ends.
func (n *Notifier) Run(ctx context.Context) {
	poll := n.settings(ctx).Poll
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		// A saved notify.poll applies from the next round.
		if next := n.settings(ctx).Poll; next != poll && next > 0 {
			poll = next
			t.Reset(poll)
		}
		if err := n.Scan(ctx); err != nil && ctx.Err() == nil {
			n.log.Warn("webhook scan", "err", err)
		}
		if err := n.queueDigest(ctx); err != nil && ctx.Err() == nil {
			n.log.Warn("digest not queued", "err", err)
		}
		if _, err := n.SendDue(ctx, 50); err != nil && ctx.Err() == nil {
			n.log.Warn("webhook send", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
