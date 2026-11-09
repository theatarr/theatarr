package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/web"
)

// serveSettingsTester runs the admin API's source tests (web.SettingsTester). Each test runs through a probe
// gateway (gateway.Probe) built on the active runtime's gateway for the tested snapshot (saved or candidate): it
// holds the test's authorization, charging and health accounting. Budget buckets and windows, back-offs and egress
// checks are the live ones, so a test spends budget, is refused while a source backs off or its required VPN is unconfirmed,
// and never resets any of that state. Nothing is published or saved.
type serveSettingsTester struct {
	manager *settings.RuntimeCoordinator
	st      *store.Store
	log     *slog.Logger
	lookup  settings.LookupSecret
}

// settingsProbeQueries are tried in order; the first a source supports is its test.
var settingsProbeQueries = []indexer.Query{
	{Type: indexer.QueryRSS, RSSLimit: 5},
	{Type: indexer.QueryMovie, IMDb: "tt0111161", Title: "The Shawshank Redemption", Year: 1994},
	{Type: indexer.QueryText, Title: "The Shawshank Redemption"},
}

// dmmProbeQueries test a DMM source with a movie search, which goes to its configured path: its RSS reads only the
// path's rd/ad providers, so an RSS test would pass while every search fails (no TorBox key linked at DMM: 102).
// It names no title: with one, a failing movie search falls back to a text search, which could pass.
var dmmProbeQueries = []indexer.Query{{Type: indexer.QueryMovie, IMDb: "tt0133093"}}

// dmmProbePageCap keeps a DMM test to one small page.
const dmmProbePageCap = 10

func (t serveSettingsTester) TestSource(ctx context.Context, req web.SettingsTestRequest) web.SettingsTestOutcome {
	internal := web.SettingsTestOutcome{Status: "failed", Category: "internal"}
	snap, entity, key := req.Snapshot, req.Entity, req.Key
	if t.manager == nil || snap == nil || snap.Policy == nil {
		return internal
	}
	active := t.manager.Current()
	if active == nil {
		return internal
	}
	current, _ := active.Components.(*serveRuntime)
	if current == nil || current.Gateway == nil {
		return internal
	}
	target := gateway.Target{Class: gateway.ClassIndexer, Key: key}
	if entity == "providers" {
		target.Class = gateway.ClassProvider
	}
	// The saved source, if there is one: its budget is charged and its address bounds where its credential goes.
	saved := req.Saved
	if saved == nil {
		saved = snap
	}
	savedURL, savedBudget, exists := settingsSourceConfig(saved.Policy, entity, key)
	candURL, _, _ := settingsSourceConfig(snap.Policy, entity, key)
	if req.Candidate() && exists && !req.NewSecret && (settingsAddress(savedURL) != settingsAddress(candURL) ||
		!sameIndexerHosts(saved.Policy, snap.Policy, entity, key)) {
		// The web layer refuses this with a 422 first; the stored credential never leaves for another address.
		return web.SettingsTestOutcome{Status: "refused", Category: "host"}
	}
	var secret string
	if err := snap.WithSecret(entity, key, t.lookup, func(v string) error { secret = v; return nil }); err != nil && !req.Candidate() {
		return internal
	}
	var secrets []string
	if secret != "" {
		secrets = append(secrets, secret)
	}
	gw, err := current.Gateway.Probe(ctx, snap.Policy, gateway.ProbeOptions{Target: target, Budget: savedBudget,
		Candidate: req.Candidate(), Secrets: secrets, Logger: t.log, Version: buildinfo.Version})
	if err != nil {
		var refused *gateway.Refused
		if errors.As(err, &refused) {
			return classifySourceError(ctx, err)
		}
		return internal
	}
	// Pin the test gateway so every request of the test, redirects included, goes through it.
	ctx = settings.WithRuntime(ctx, &settings.Runtime{Generation: active.Generation, ProviderEpoch: active.ProviderEpoch,
		Policy: snap.Policy, Components: &serveRuntime{Gateway: gw}})
	switch entity {
	case "indexers":
		return testIndexerSource(ctx, snap.Policy, gw, key, secret, t.log)
	case "providers":
		return testProviderSource(ctx, snap.Policy, gw, key, secret)
	}
	return internal
}

// sameIndexerHosts reports whether a cardigann indexer's further hosts (which get its credential too) are the same in
// both policies.
func sameIndexerHosts(a, b *policy.Policy, entity, key string) bool {
	if entity != "indexers" || a == nil || b == nil || a.Indexers[key] == nil || b.Indexers[key] == nil {
		return true
	}
	x, y := slices.Sorted(slices.Values(a.Indexers[key].Hosts)), slices.Sorted(slices.Values(b.Indexers[key].Hosts))
	return slices.EqualFunc(x, y, strings.EqualFold)
}

// settingsSourceConfig is a source's URL and budget in pol, and whether it exists there.
func settingsSourceConfig(pol *policy.Policy, entity, key string) (string, string, bool) {
	if pol == nil {
		return "", "", false
	}
	switch entity {
	case "indexers":
		if ix := pol.Indexers[key]; ix != nil {
			return ix.URL, ix.Budget, true
		}
	case "providers":
		if pv := pol.Providers[key]; pv != nil {
			return pv.URL, pv.Budget, true
		}
	}
	return "", "", false
}

// settingsAddress is a URL's lower-case scheme and host:port (explicit or the scheme default).
func settingsAddress(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[scheme]
	}
	return scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

func testIndexerSource(ctx context.Context, pol *policy.Policy, gw *gateway.Gateway, key, secret string, log *slog.Logger) web.SettingsTestOutcome {
	ix := pol.Indexers[key]
	if ix == nil {
		return web.SettingsTestOutcome{Status: "failed", Category: "internal"}
	}
	keys := map[string]string{}
	if secret != "" {
		keys[key] = secret
	} else if ix.APIKeyEnv != "" {
		return web.SettingsTestOutcome{Status: "failed", Category: "credential"}
	}
	queries := settingsProbeQueries
	if ix.Kind == "dmm" {
		probe := *ix
		probe.PageCap = dmmProbePageCap
		ix, queries = &probe, dmmProbeQueries
	}
	// A cardigann candidate runs on a throwaway engine: no live session is used or saved.
	svc, err := indexer.New(&policy.Policy{Indexers: map[string]*policy.Indexer{key: ix}}, gw,
		indexer.Options{Logger: log, APIKeys: keys, CacheTTL: -1, Cardigann: cardigannManager().Throwaway()})
	if err != nil {
		return web.SettingsTestOutcome{Status: "failed", Category: "internal"}
	}
	sr := svc.Searcher(key)
	ctx = gateway.WithPurpose(ctx, gateway.PurposeTest)
	for _, q := range queries {
		if !sr.Supports(q) {
			continue
		}
		res, err := sr.Search(ctx, q)
		var tzErr *indexer.TorznabError
		if ix.Kind == "dmm" && errors.As(err, &tzErr) && tzErr.Code == "102" {
			// DMM's "no key linked" for a provider in the path (TorBox, Premiumize, Offcloud): a fixed message, not
			// DMM's text.
			return web.SettingsTestOutcome{Status: "failed", Category: "dmm_provider_key"}
		}
		if err != nil {
			return classifySourceError(ctx, err)
		}
		n := len(res.Releases)
		return web.SettingsTestOutcome{Status: "ok", Category: "ok", Results: &n}
	}
	return web.SettingsTestOutcome{Status: "failed", Category: "unsupported"}
}

func testProviderSource(ctx context.Context, pol *policy.Policy, gw *gateway.Gateway, key, secret string) web.SettingsTestOutcome {
	pv := pol.Providers[key]
	if pv == nil {
		return web.SettingsTestOutcome{Status: "failed", Category: "internal"}
	}
	if secret == "" {
		return web.SettingsTestOutcome{Status: "failed", Category: "credential"}
	}
	client, err := provider.New(key, pv, secret, gw)
	if err != nil {
		return web.SettingsTestOutcome{Status: "failed", Category: "internal"}
	}
	account, err := client.Account(ctx)
	if err != nil {
		return classifySourceError(ctx, err)
	}
	return web.SettingsTestOutcome{Status: "ok", Category: "ok",
		Account: &web.SettingsTestAccount{Premium: account.Premium, Expires: account.Expires}}
}

// classifySourceError reduces an error to a category; upstream text never leaves this function.
func classifySourceError(ctx context.Context, err error) web.SettingsTestOutcome {
	var refused *gateway.Refused
	var httpErr *indexer.HTTPError
	var apiErr *provider.APIError
	switch {
	case errors.As(err, &refused):
		return web.SettingsTestOutcome{Status: "refused", Category: string(refused.Reason), RetryAt: refused.RetryAt}
	case errors.Is(err, provider.ErrNoKey):
		return web.SettingsTestOutcome{Status: "failed", Category: "credential"}
	case errors.Is(err, provider.ErrAuth):
		out := web.SettingsTestOutcome{Status: "failed", Category: "auth"}
		if errors.As(err, &apiErr) {
			out.HTTPStatus = apiErr.Status
		}
		return out
	case errors.As(err, &httpErr):
		category := "http"
		if httpErr.Status == http.StatusUnauthorized || httpErr.Status == http.StatusForbidden {
			category = "auth"
		}
		return web.SettingsTestOutcome{Status: "failed", Category: category, HTTPStatus: httpErr.Status}
	case errors.As(err, &apiErr):
		return web.SettingsTestOutcome{Status: "failed", Category: "http", HTTPStatus: apiErr.Status}
	case errors.Is(err, indexer.ErrUnsupported):
		return web.SettingsTestOutcome{Status: "failed", Category: "unsupported"}
	case errors.Is(err, context.DeadlineExceeded), ctx.Err() != nil:
		return web.SettingsTestOutcome{Status: "failed", Category: "timeout"}
	}
	return web.SettingsTestOutcome{Status: "failed", Category: "connectivity"}
}

// newSettingsTestLimits is shared by every runtime generation's web layer.
func newSettingsTestLimits() *web.SettingsTestLimits {
	return &web.SettingsTestLimits{Timeout: 20 * time.Second, Concurrency: 4, Cooldown: 30 * time.Second, CandidateRate: 10}
}
