package httplog

import (
	"net/http/httptest"
	"testing"
)

func TestSanitizeRequestIDAcceptsPlainIDs(t *testing.T) {
	for _, in := range []string{
		"abc123",
		"9f8b7c6d-5e4f-3a2b-1c0d-9e8f7a6b5c4d",
		"req_42-A",
	} {
		if got := sanitizeRequestID(in); got != in {
			t.Fatalf("sanitizeRequestID(%q) = %q, want it kept", in, got)
		}
	}
}

func TestSanitizeRequestIDRejectsInjectionAndOversize(t *testing.T) {
	// A client-supplied id lands verbatim in a log line and in a response
	// header. Anything that could break either one out of its field is dropped.
	for _, in := range []string{
		"",
		"has space",
		"has\nnewline",
		"has\"quote",
		"has{brace}",
		string(make([]byte, 200)),
	} {
		if got := sanitizeRequestID(in); got != "" {
			t.Fatalf("sanitizeRequestID(%q) = %q, want it rejected", in, got)
		}
	}
}

func TestRequestIDReusesAValidIncomingHeader(t *testing.T) {
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set(RequestIDHeader, "client-supplied-1")

	if got := requestIDFromRequest(r); got != "client-supplied-1" {
		t.Fatalf("got %q, want the client's id reused", got)
	}
}

func TestRequestIDMintsWhenAbsentOrHostile(t *testing.T) {
	bare := httptest.NewRequest("GET", "/x", nil)
	first := requestIDFromRequest(bare)
	if first == "" {
		t.Fatal("no id minted")
	}

	second := requestIDFromRequest(httptest.NewRequest("GET", "/x", nil))
	if first == second {
		t.Fatalf("minted ids collide: %q", first)
	}

	hostile := httptest.NewRequest("GET", "/x", nil)
	hostile.Header.Set(RequestIDHeader, "bad\nvalue")
	if got := requestIDFromRequest(hostile); got == "bad\nvalue" || got == "" {
		t.Fatalf("hostile header not replaced, got %q", got)
	}
}
