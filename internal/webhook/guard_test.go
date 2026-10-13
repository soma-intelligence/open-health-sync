// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckURL(t *testing.T) {
	ok := []string{"https://app.example.com/hooks/health", "https://8.8.8.8/x", "https://[2001:4860::8888]/x"}
	for _, u := range ok {
		if err := CheckURL(u, false); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	refused := []string{
		"", "app.example.com/x", "ftp://app.example.com", "http://app.example.com",
		"https://user:pw@app.example.com", "https://app.example.com/#frag",
		"https://localhost/x", "https://api.localhost/x", "https://127.0.0.1/x", "https://10.1.2.3/x",
		"https://192.168.1.10/x", "https://169.254.169.254/latest", "https://[::1]/x", "https://[fd00::1]/x",
		"https://100.64.1.1/x", "https://0.0.0.0/x", "https://" + strings.Repeat("a", MaxURLLength) + ".com",
	}
	for _, u := range refused {
		if err := CheckURL(u, false); !errors.Is(err, ErrURL) {
			t.Errorf("%s: %v, want a refusal", u, err)
		}
	}
	for _, u := range []string{"http://localhost:3000/hook", "http://10.0.0.5/hook", "http://app:8000/hook"} {
		if err := CheckURL(u, true); err != nil {
			t.Errorf("allow private %s: %v", u, err)
		}
	}
}

// TestClientRefusesPrivateDestinations dials a loopback test server: the
// guarded client refuses it after resolution, the permissive one reaches it.
func TestClientRefusesPrivateDestinations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	url := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) // a name, not a literal

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, url, nil)
	if _, err := NewClient(false).Do(req); err == nil || !strings.Contains(err.Error(), "private or reserved") {
		t.Fatalf("guarded client: %v", err)
	}
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodPost, url, nil)
	resp, err := NewClient(true).Do(req)
	if err != nil {
		t.Fatalf("permissive client: %v", err)
	}
	resp.Body.Close()
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound)
	}))
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	resp, err := NewClient(true).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d, want the 302 itself", resp.StatusCode)
	}
}
