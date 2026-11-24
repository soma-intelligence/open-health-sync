package logging

import "testing"

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"":              "",
		"abc":           "\u2026(3)",
		"12345678":      "\u2026(8)",
		"sk-1234567890": "sk-1\u2026(13)",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Fatalf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}
