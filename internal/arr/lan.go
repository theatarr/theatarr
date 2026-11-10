package arr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// The arrs live on the LAN, and every request carries the instance's API key. So the client talks to LAN addresses
// only: loopback, RFC 1918 (10/8, 172.16/12, 192.168/16), carrier-grade NAT space (100.64/10, which Tailscale and
// similar overlays use) or IPv6 ULA (fc00::/7). It checks twice:
//
//   - CheckLiteral, when the policy is validated and in New: an IP-literal host must be a LAN address (no DNS
//     lookups there, so tests and offline validation never touch the network);
//   - CheckHost, in `theatarr policy validate`: a name must resolve, and only to LAN addresses;
//   - the dialer, on the address it is about to connect to, so a name that resolves elsewhere later (DNS
//     rebinding, a changed record) is refused before the key is sent. No proxy is used.
//
// Arr traffic does not go through the gateway or the VPN: the arrs are on the LAN.

// ErrNotLAN is returned for an arr host that is not a LAN address.
var ErrNotLAN = errors.New("arr: not a LAN address (loopback, RFC 1918, 100.64.0.0/10 or ULA); the arr client sends its API key " +
	"only to the LAN")

// cgnat is the shared address space of RFC 6598, used by Tailscale and other overlay networks.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// LANAddr reports whether ip is loopback, RFC 1918, 100.64.0.0/10 or ULA.
func LANAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && (ip.IsLoopback() || ip.IsPrivate() || cgnat.Contains(ip))
}

// CheckLiteral refuses an IP-literal host (without port) that is not a LAN address. A name passes: it is checked when
// resolved (CheckHost) and before every connection.
func CheckLiteral(host string) error {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if ip, err := netip.ParseAddr(host); err == nil && !LANAddr(ip) {
		return fmt.Errorf("host %s: %w", host, ErrNotLAN)
	}
	return nil
}

// CheckHost resolves host (a name or an IP literal, without port) and refuses it unless it resolves and every
// address is a LAN address.
func CheckHost(ctx context.Context, host string) error {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if ip, err := netip.ParseAddr(host); err == nil {
		if !LANAddr(ip) {
			return fmt.Errorf("host %s: %w", host, ErrNotLAN)
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("arr: host %s does not resolve, so it cannot be checked as a LAN address: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("arr: host %s resolves to nothing", host)
	}
	for _, ip := range ips {
		if !LANAddr(ip) {
			return fmt.Errorf("host %s resolves to %s: %w", host, ip.Unmap(), ErrNotLAN)
		}
	}
	return nil
}

// lanControl refuses a connection to an address that is not a LAN address, after resolution and before connecting.
func lanControl(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("arr: dial %s: %w", address, err)
	}
	if !LANAddr(ap.Addr()) {
		return fmt.Errorf("dial %s: %w", ap.Addr().Unmap(), ErrNotLAN)
	}
	return nil
}

// lanTransport is http.DefaultTransport's settings, without a proxy, with a dialer that connects to LAN addresses
// only.
func lanTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: lanControl}
	t.DialContext = d.DialContext
	return t
}
