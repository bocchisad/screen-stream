package streamer

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"
)

const EnvAuthToken = "SCREEN_STREAM_TOKEN"

// ResolveAuthToken returns CLI token, else env, else empty (auth disabled).
func ResolveAuthToken(cli string) string {
	if t := strings.TrimSpace(cli); t != "" {
		return t
	}
	return strings.TrimSpace(os.Getenv(EnvAuthToken))
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
