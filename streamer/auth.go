package streamer

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strings"
)

const EnvAuthToken = "SCREEN_STREAM_TOKEN"

// ResolveAuthToken returns CLI token, else env, else empty.
func ResolveAuthToken(cli string) string {
	if t := strings.TrimSpace(cli); t != "" {
		return t
	}
	return strings.TrimSpace(os.Getenv(EnvAuthToken))
}

// RequireAuthToken fails if token is empty (hub/send/recv/preview).
func RequireAuthToken(token string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("auth token required: set -token or %s", EnvAuthToken)
	}
	return nil
}

// CheckRequestToken validates Bearer or ?token= against expected.
// Empty expected → allow (dev mode).
func CheckRequestToken(r *http.Request, expected string) bool {
	if expected == "" {
		return true
	}
	got := ""
	if ah := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(ah), "bearer ") {
		got = strings.TrimSpace(ah[7:])
	}
	if got == "" {
		got = r.URL.Query().Get("token")
	}
	if got == "" {
		got = r.Header.Get("X-Screen-Stream-Token")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func requireToken(next http.HandlerFunc, token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !CheckRequestToken(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
