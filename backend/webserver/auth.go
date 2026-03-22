package webserver

import (
	"encoding/json"
	"fmt"
	"hll-radar/auth"
	"hll-radar/config"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
)

// cacheEntry holds a cached CRCON auth validation result.
type cacheEntry struct {
	valid   bool
	expires time.Time
}

// sessionCache is an in-process TTL cache mapping sessionid → validity.
type sessionCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

func newSessionCache() *sessionCache {
	c := &sessionCache{entries: make(map[string]cacheEntry)}
	go c.cleanup()
	return c
}

// get returns (valid, found).
func (c *sessionCache) get(sessionID string) (bool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[sessionID]
	if !ok || time.Now().After(entry.expires) {
		return false, false
	}
	return entry.valid, true
}

func (c *sessionCache) set(sessionID string, valid bool, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[sessionID] = cacheEntry{valid: valid, expires: time.Now().Add(ttl)}
}

// cleanup removes expired entries every 30 seconds.
func (c *sessionCache) cleanup() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		for k, v := range c.entries {
			if now.After(v.expires) {
				delete(c.entries, k)
			}
		}
		c.mu.Unlock()
	}
}

// crconIsLoggedInResponse matches CRCON's GET /api/is_logged_in JSON shape.
type crconIsLoggedInResponse struct {
	Result struct {
		Authenticated bool `json:"authenticated"`
	} `json:"result"`
}

// validateWithCRCON calls CRCON to check whether the session is authenticated.
func validateWithCRCON(sessionID string, crconURL string) (bool, error) {
	client := &http.Client{Timeout: 5 * time.Second}

	req, err := http.NewRequest("GET", crconURL+"/api/is_logged_in", nil)
	if err != nil {
		return false, fmt.Errorf("creating request: %w", err)
	}
	req.AddCookie(&http.Cookie{Name: "sessionid", Value: sessionID})

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("calling CRCON: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("CRCON returned status %d", resp.StatusCode)
	}

	var result crconIsLoggedInResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("decoding CRCON response: %w", err)
	}

	return result.Result.Authenticated, nil
}

// authCache is the package-level session cache, initialized once.
var authCache = newSessionCache()

// crconAuthMiddleware validates the CRCON sessionid cookie on every request.
func crconAuthMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip if auth is disabled
			if !viper.GetBool("crcon.enabled") {
				next.ServeHTTP(w, r)
				return
			}

			// Skip OPTIONS (CORS preflight)
			if r.Method == "OPTIONS" {
				next.ServeHTTP(w, r)
				return
			}

			// Whitelist paths that don't require auth
			if r.URL.Path == "/health" || r.URL.Path == "/api/v1/auth/status" {
				next.ServeHTTP(w, r)
				return
			}

			cookie, err := r.Cookie("sessionid")
			if err != nil || cookie.Value == "" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{
					"error": "Authentication required. Please log in to CRCON first.",
				})
				return
			}

			sessionID := cookie.Value

			// Check cache first
			if valid, found := authCache.get(sessionID); found {
				if valid {
					next.ServeHTTP(w, r)
					return
				}
				writeJSON(w, http.StatusUnauthorized, map[string]string{
					"error": "Session is not authenticated. Please log in to CRCON.",
				})
				return
			}

			// Validate with CRCON
			crconURL := viper.GetString("crcon.url")
			ttl := time.Duration(viper.GetInt("crcon.cache_ttl_seconds")) * time.Second
			if ttl == 0 {
				ttl = 60 * time.Second
			}

			valid, err := validateWithCRCON(sessionID, crconURL)
			if err != nil {
				logger.Error("CRCON auth validation failed", "error", err)
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{
					"error": "Unable to validate session with CRCON. Please try again.",
				})
				return
			}

			// Cache the result
			authCache.set(sessionID, valid, ttl)

			if !valid {
				writeJSON(w, http.StatusUnauthorized, map[string]string{
					"error": "Session is not authenticated. Please log in to CRCON.",
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// writeJSON is a small helper to write a JSON response with a status code.
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// jwtAuthMiddleware validates JWT Bearer tokens on every request (hosted mode).
func jwtAuthMiddleware(logger *slog.Logger, jwtSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip OPTIONS (CORS preflight)
			if r.Method == "OPTIONS" {
				next.ServeHTTP(w, r)
				return
			}

			// Whitelist paths that don't require auth
			path := r.URL.Path
			if path == "/health" ||
				strings.HasPrefix(path, "/api/v1/auth/") ||
				path == "/ws" ||
				!strings.HasPrefix(path, "/api/") {
				next.ServeHTTP(w, r)
				return
			}

			// Extract Bearer token
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{
					"error": "Authentication required. Please log in.",
				})
				return
			}

			tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
			claims, err := auth.ValidateToken(tokenStr, jwtSecret)
			if err != nil {
				logger.Debug("JWT validation failed", "error", err)
				writeJSON(w, http.StatusUnauthorized, map[string]string{
					"error": "Invalid or expired token.",
				})
				return
			}

			// Set auth context
			ctx := auth.SetAuthContext(r.Context(), claims.UserID, claims.OrgID, claims.Role)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// requireOwner is a handler wrapper that checks for owner role.
func requireOwner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !config.IsHostedMode() {
			next(w, r)
			return
		}
		if !auth.IsOwner(r.Context()) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "Owner access required.",
			})
			return
		}
		next(w, r)
	}
}
