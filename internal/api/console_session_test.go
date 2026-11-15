// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSessionOriginAndSecure(t *testing.T) {
	req := func(host, origin string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	pub := "https://ohs.example.com"
	assert.True(t, sameOrigin(req("box:8080", ""), ""), "no Origin")
	assert.True(t, sameOrigin(req("box:8080", ""), pub), "no Origin with a public URL")
	assert.False(t, sameOrigin(req("box:8080", "null"), ""))
	assert.False(t, sameOrigin(req("box:8080", "null"), pub))
	assert.True(t, sameOrigin(req("10.0.0.5:8080", "https://ohs.example.com:443"), pub), "default port dropped")
	assert.True(t, sameOrigin(req("10.0.0.5:8080", "https://OHS.example.com"), pub), "case")
	assert.False(t, sameOrigin(req("10.0.0.5:8080", "http://ohs.example.com"), pub), "scheme")
	assert.False(t, sameOrigin(req("ohs.example.com", "https://ohs.example.com:8443"), pub), "port")
	assert.False(t, sameOrigin(req("10.0.0.5:8080", "https://10.0.0.5:8080"), pub), "r.Host ignored")
	assert.True(t, sameOrigin(req("box:8080", "http://box:8080"), ""))
	assert.False(t, sameOrigin(req("box:8080", "http://evil:8080"), ""))
	assert.False(t, sameOrigin(req("box:8080", "garbage"), ""))

	local := func(host string, tlsOn bool) *http.Request {
		r := req(host, "")
		if tlsOn {
			r.TLS = &tls.ConnectionState{}
		}
		return r
	}
	assert.False(t, cookieSecure(local("x", false), "http://localhost:8080", nil))
	assert.False(t, cookieSecure(local("x", false), "http://127.0.0.1", nil))
	assert.False(t, cookieSecure(local("x", false), "http://[::1]:8080", nil))
	assert.True(t, cookieSecure(local("x", false), "https://ohs.example.com", nil))
	assert.True(t, cookieSecure(local("x", false), "http://ohs.example.com", nil), "http public URL on a real host")
	assert.False(t, cookieSecure(local("localhost:8080", false), "", nil))
	assert.True(t, cookieSecure(local("box", true), "", nil))
	assert.True(t, cookieSecure(local("box", false), "", nil))
	assert.True(t, cookieSecure(local("localhost:8080", true), "", nil))

	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	proxied := func(peer string) *http.Request {
		r := local("192.168.1.10:8080", false)
		r.RemoteAddr = peer
		r.Header.Set("X-Forwarded-Proto", "https")
		return r
	}
	assert.True(t, plainHTTP(local("192.168.1.10:8080", false), "", nil))
	assert.False(t, plainHTTP(proxied("10.1.2.3:4000"), "", trusted), "https from a trusted proxy")
	assert.True(t, plainHTTP(proxied("203.0.113.9:4000"), "", trusted), "an untrusted peer cannot say https")
	assert.True(t, plainHTTP(local("192.168.1.10:8080", false), "http://192.168.1.10:8080", nil), "http public URL on a remote host")
	assert.True(t, plainHTTP(local("192.168.1.10:8080", false), "http://ohs.example.com", nil), "http public URL on a remote host")
	assert.True(t, plainHTTP(local("x", false), "http://%zz", nil), "unparsable public URL")
	assert.False(t, plainHTTP(local("192.168.1.10:8080", false), "https://ohs.example.com", nil), "https public URL")
	assert.False(t, plainHTTP(local("x", false), "http://localhost:8080", nil), "http public URL on this machine")
	assert.False(t, plainHTTP(local("x", false), "http://[::1]:8080", nil), "http public URL on this machine")
	assert.False(t, plainHTTP(local("localhost:8080", false), "", nil))
	assert.False(t, plainHTTP(local("192.168.1.10", true), "", nil))
	assert.True(t, cookieSecure(proxied("10.1.2.3:4000"), "", trusted))
}
