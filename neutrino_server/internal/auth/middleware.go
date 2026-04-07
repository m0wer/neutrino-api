package auth

import (
	"net/http"
	"strings"

	"github.com/btcsuite/btclog"
)

// Middleware returns an HTTP middleware that requires a valid Bearer token
// in the Authorization header.  Requests without a valid token receive a
// 401 Unauthorized response.
func Middleware(expectedToken string, logger btclog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				logger.Warnf("Auth: missing Authorization header from %s %s", r.Method, r.URL.Path)
				http.Error(w, `{"error":"authorization required"}`, http.StatusUnauthorized)
				return
			}

			// Expect "Bearer <token>".
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				logger.Warnf("Auth: malformed Authorization header from %s %s", r.Method, r.URL.Path)
				http.Error(w, `{"error":"invalid authorization format"}`, http.StatusUnauthorized)
				return
			}

			if !ValidateToken(parts[1], expectedToken) {
				logger.Warnf("Auth: invalid token from %s %s", r.Method, r.URL.Path)
				http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
