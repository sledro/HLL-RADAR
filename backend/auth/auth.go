package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return string(bytes), nil
}

func CheckPassword(hash, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

func GenerateInviteToken() (string, error) {
	return generateRandomHex(32)
}

func GenerateRefreshToken() (string, error) {
	return generateRandomHex(64)
}

// HashToken produces a SHA-256 hex digest of a token for storage.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func generateRandomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// DeviceFingerprint produces a SHA-256 hash of the client's User-Agent.
// This is embedded in JWT claims and stored alongside refresh tokens so that
// tokens cannot be used from a different browser/device.
// Note: IP is intentionally excluded to avoid lockouts on network changes.
func DeviceFingerprint(userAgent string) string {
	h := sha256.Sum256([]byte(userAgent))
	return hex.EncodeToString(h[:])
}

// RequestFingerprint extracts User-Agent from an HTTP request and returns the fingerprint.
func RequestFingerprint(r *http.Request) string {
	return DeviceFingerprint(r.UserAgent())
}

// ClientIP extracts the real client IP from a request, checking X-Forwarded-For
// and X-Real-IP headers (set by reverse proxies like Railway/nginx).
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For can contain multiple IPs; the first is the client
		if i := strings.Index(xff, ","); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	// Fall back to RemoteAddr (includes port)
	if i := strings.LastIndex(r.RemoteAddr, ":"); i > 0 {
		return r.RemoteAddr[:i]
	}
	return r.RemoteAddr
}
