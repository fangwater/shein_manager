package shein

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func TestSessionVerifierAcceptsPythonCompatibleCookie(t *testing.T) {
	verifier, err := NewSessionVerifier("", "session-secret", "pyy,operations")
	if err != nil {
		t.Fatal(err)
	}
	verifier.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	payload := []byte(`{"expires":1700003600,"username":"pyy"}`)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte("session-secret"))
	_, _ = mac.Write([]byte(encoded))
	cookie := fmt.Sprintf("%s.%s", encoded, hex.EncodeToString(mac.Sum(nil)))
	username, ok := verifier.Verify(cookie)
	if !ok || username != "pyy" {
		t.Fatalf("Verify() = %q, %v", username, ok)
	}
}

func TestSessionVerifierRejectsExpiredAndUnauthorizedUsers(t *testing.T) {
	verifier, err := NewSessionVerifier("", "session-secret", "pyy")
	if err != nil {
		t.Fatal(err)
	}
	verifier.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	for _, payload := range []string{
		`{"expires":1699999999,"username":"pyy"}`,
		`{"expires":1700003600,"username":"temu-test"}`,
	} {
		encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
		mac := hmac.New(sha256.New, []byte("session-secret"))
		_, _ = mac.Write([]byte(encoded))
		cookie := fmt.Sprintf("%s.%s", encoded, hex.EncodeToString(mac.Sum(nil)))
		if _, ok := verifier.Verify(cookie); ok {
			t.Fatalf("unexpectedly accepted %s", payload)
		}
	}
}
