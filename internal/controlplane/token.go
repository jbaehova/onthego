package controlplane

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type claims struct {
	Subject  string `json:"sub"`
	DeviceID string `json:"device_id"`
	Type     string `json:"type"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

func signToken(key []byte, value claims) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(header)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	unsigned := encodedHeader + "." + encodedPayload
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verifyToken(key []byte, raw, expectedType string, now time.Time) (claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return claims{}, errors.New("invalid token")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return claims{}, errors.New("invalid token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}, errors.New("invalid token payload")
	}
	var value claims
	if err := json.Unmarshal(payload, &value); err != nil {
		return claims{}, err
	}
	if value.Type != expectedType || value.Subject == "" || value.DeviceID == "" || value.Expires <= now.Unix() {
		return claims{}, errors.New("token expired or has the wrong type")
	}
	return value, nil
}
