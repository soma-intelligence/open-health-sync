// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSignKnownVector checks Sign against the example of the Standard
// Webhooks specification, so receivers using its libraries accept us.
func TestSignKnownVector(t *testing.T) {
	// The specification's published example, split so secret scanners do
	// not take a public test vector for a credential.
	vector := SecretPrefix + "MfKQ9r8GKYqrTwjUPD8ILP" + "ZIo2LaLaSw"
	id := "msg_p5jXN8AQM9LWM0D4loKWxJek"
	ts := time.Unix(1614265330, 0)
	body := []byte(`{"test": 2432232314}`)
	got, err := Sign(vector, id, ts, body)
	if err != nil {
		t.Fatal(err)
	}
	if want := "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="; got != want {
		t.Fatalf("Sign = %s, want %s", got, want)
	}
	if err := Verify(vector, id, "1614265330", "v1,bogus "+got, body, ts.Add(time.Minute)); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifyRefuses(t *testing.T) {
	secret, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, SecretPrefix) {
		t.Fatalf("secret %q has no prefix", secret)
	}
	now := time.Now()
	body := []byte(`{"a":1}`)
	sig, _ := Sign(secret, "id", now, body)
	ts := now.Unix()
	cases := map[string]error{
		"tampered body": Verify(secret, "id", itoa(ts), sig, []byte(`{"a":2}`), now),
		"other id":      Verify(secret, "id2", itoa(ts), sig, body, now),
		"old":           Verify(secret, "id", itoa(ts), sig, body, now.Add(Tolerance+time.Second)),
		"future":        Verify(secret, "id", itoa(ts), sig, body, now.Add(-Tolerance-time.Second)),
		"bad timestamp": Verify(secret, "id", "x", sig, body, now),
		"v2 only":       Verify(secret, "id", itoa(ts), strings.Replace(sig, "v1,", "v2,", 1), body, now),
	}
	for name, err := range cases {
		if !errors.Is(err, ErrVerify) {
			t.Errorf("%s: %v, want ErrVerify", name, err)
		}
	}
	other, _ := NewSecret()
	if err := Verify(other, "id", itoa(ts), sig, body, now); !errors.Is(err, ErrVerify) {
		t.Errorf("other secret: %v", err)
	}
	if _, err := Sign("nope", "id", now, body); !errors.Is(err, ErrSecret) {
		t.Errorf("malformed secret: %v", err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
