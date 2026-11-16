package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// ListsBudget is the target key and budget of JSON import list fetches (ListGet).
const ListsBudget = "import-lists"

// listsBudget: several lists at each sync, and Preview, but no flood: 10 a minute, 300 a day.
var listsBudget = policy.Budget{Requests: 10, Per: policy.Duration(time.Minute), Burst: 5, Daily: 300}

// ErrListRedirect is a JSON list address that answers with a redirect: the list is read from its direct address only.
var ErrListRedirect = errors.New("the address answers with a redirect; enter the address it redirects to")

// ListGet fetches a JSON import list (docs/design/arrview.md, Lists) through the repository fetch's path (syncGet):
// VPN-guarded like an indexer, on its own shared budget, at most maxBytes, following no redirect (ErrListRedirect).
// The address must be public: unlike a source probe, a LAN host the live allowlist holds is refused too.
func (g *Gateway) ListGet(ctx context.Context, base *policy.Policy, rawURL string, maxBytes int64) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return nil, &Refused{Reason: ReasonHost, Target: ListsBudget, Detail: "not a URL"}
	}
	host := strings.Trim(u.Hostname(), "[]")
	addrs := []netip.Addr{}
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = append(addrs, ip)
	} else if g.lookupIP != nil {
		addrs, err = g.lookupIP(ctx, host)
		if err != nil {
			return nil, err
		}
	} else if addrs, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host); err != nil {
		return nil, err
	}
	for _, ip := range addrs {
		if !publicAddr(ip) {
			return nil, &Refused{Reason: ReasonHost, Target: ListsBudget,
				Detail: "a private, loopback or link-local address: a list is read from the internet only"}
		}
	}
	res, err := g.syncGet(ctx, base, ListsBudget, rawURL, maxBytes, "application/json", &g.runtime.listsOnce,
		&g.runtime.listsBucket, listsBudget)
	var refused *Refused
	if errors.As(err, &refused) && strings.HasPrefix(refused.Detail, "redirect to ") {
		return nil, ErrListRedirect
	}
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("the list answered HTTP " + res.Status)
	}
	return io.ReadAll(res.Body)
}
