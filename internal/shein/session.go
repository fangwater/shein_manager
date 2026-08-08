package shein

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

const SessionCookieName = "shein_pnl_session"

type SessionVerifier struct {
	secret       []byte
	allowedUsers map[string]struct{}
	now          func() time.Time
}

func NewSessionVerifier(secretFile, configuredSecret, allowedUsers string) (*SessionVerifier, error) {
	var secret []byte
	if value := strings.TrimSpace(configuredSecret); value != "" {
		secret = []byte(value)
	} else {
		value, err := os.ReadFile(secretFile)
		if err != nil {
			return nil, errors.New("read SHEIN Web session secret: " + err.Error())
		}
		secret = []byte(strings.TrimSpace(string(value)))
	}
	if len(secret) == 0 {
		return nil, errors.New("SHEIN Web session secret is empty")
	}
	allowed := make(map[string]struct{})
	for _, user := range strings.Split(allowedUsers, ",") {
		if user = strings.TrimSpace(user); user != "" {
			allowed[user] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil, errors.New("SHEIN_GO_ALLOWED_USERS must contain at least one username")
	}
	return &SessionVerifier{secret: secret, allowedUsers: allowed, now: time.Now}, nil
}

func (v *SessionVerifier) Verify(value string) (string, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return "", false
	}
	suppliedSignature, err := hex.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, v.secret)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(suppliedSignature, mac.Sum(nil)) {
		return "", false
	}
	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	var payload struct {
		Username string `json:"username"`
		Expires  int64  `json:"expires"`
	}
	if err := json.Unmarshal(payloadRaw, &payload); err != nil || payload.Expires < v.now().Unix() {
		return "", false
	}
	_, allowed := v.allowedUsers[payload.Username]
	return payload.Username, allowed
}
