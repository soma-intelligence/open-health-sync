// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
)

// MaxURLLength bounds an endpoint URL.
const MaxURLLength = 2048

// ErrURL wraps every refusal of an endpoint URL; its text says why.
var ErrURL = errors.New("url")

// CheckURL accepts an endpoint URL: absolute https with a host, no user info
// and no fragment. With allowPrivate (OHS_WEBHOOK_ALLOW_PRIVATE) plain http
// and private or loopback hosts are accepted too, for receivers on the same
// machine or network. Without it a host that is a private address literal or
// localhost is refused here, and the dialer refuses names that resolve to one.
func CheckURL(raw string, allowPrivate bool) error {
	if len(raw) > MaxURLLength {
		return fmt.Errorf("%w is at most %d characters", ErrURL, MaxURLLength)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf("%w must be an absolute URL, for example https://app.example.com/hooks/health", ErrURL)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && allowPrivate:
	case u.Scheme == "http":
		return fmt.Errorf("%w must use https (set OHS_WEBHOOK_ALLOW_PRIVATE=true to allow http on a private network)", ErrURL)
	default:
		return fmt.Errorf("%w must be http or https", ErrURL)
	}
	if u.User != nil {
		return fmt.Errorf("%w must not carry a user name or password", ErrURL)
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("%w must not have a fragment", ErrURL)
	}
	if allowPrivate {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("%w points at this machine (set OHS_WEBHOOK_ALLOW_PRIVATE=true to allow it)", ErrURL)
	}
	if a, err := netip.ParseAddr(host); err == nil && blocked(a) {
		return fmt.Errorf("%w points at a private or reserved address (set OHS_WEBHOOK_ALLOW_PRIVATE=true to allow it)", ErrURL)
	}
	return nil
}

// reserved are the IPv4 and IPv6 ranges that are neither public unicast nor
// covered by the netip predicates: shared address space of carrier-grade NAT
// (RFC 6598), "this network", IETF protocol assignments, benchmarking, the
// reserved class E block (broadcast included) and IPv6 site-local.
var reserved = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("fec0::/10"),
}

// Prefixes of IPv6 addresses that carry an IPv4 address: NAT64 (RFC 6052,
// RFC 8215) in their last 32 bits, 6to4 (RFC 3056) in bits 16 to 47, and the
// deprecated IPv4-compatible form (::a.b.c.d) in the last 32 bits.
var (
	lastIPv4 = []netip.Prefix{
		netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("::/96"),
	}
	sixToFour = netip.MustParsePrefix("2002::/16")
)

// blocked reports addresses a webhook must not reach unless private
// destinations are allowed: loopback, private, link-local (cloud metadata
// services live there), unspecified, multicast, the reserved ranges, and an
// IPv6 address that carries a blocked IPv4 address, which a NAT64 gateway or
// 6to4 relay would forward to it.
func blocked(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return true
	}
	for _, p := range reserved {
		if p.Contains(a) {
			return true
		}
	}
	if !a.Is6() {
		return false
	}
	b := a.As16()
	for _, p := range lastIPv4 {
		if p.Contains(a) {
			return blocked(netip.AddrFrom4([4]byte(b[12:16])))
		}
	}
	if sixToFour.Contains(a) {
		return blocked(netip.AddrFrom4([4]byte(b[2:6])))
	}
	return false
}

// errBlocked is the dial error for a refused address.
var errBlocked = errors.New("destination address is private or reserved (set OHS_WEBHOOK_ALLOW_PRIVATE=true to allow it)")

// dialControl refuses connections to blocked addresses. It runs after name
// resolution, on the address actually dialled, so a name that resolves (or
// rebinds) to a private address is refused too.
func dialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if blocked(a) {
		return errBlocked
	}
	return nil
}
