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

// cgnat is the shared address space of carrier-grade NAT (RFC 6598).
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// blocked reports addresses a webhook must not reach unless private
// destinations are allowed: loopback, private, link-local (cloud metadata
// services live there), unspecified, multicast and shared address space.
func blocked(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() || cgnat.Contains(a)
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
