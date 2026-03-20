// Package auth provides HTTP middleware for token-based authentication.
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"golang.org/x/crypto/ssh"
)

type contextKey string

const (
	// ContextKeyPayload is the context key for the authenticated token payload.
	ContextKeyPayload contextKey = "auth.payload"
)

// PayloadFromContext extracts the token payload from the request context.
func PayloadFromContext(ctx context.Context) (*TokenPayload, bool) {
	p, ok := ctx.Value(ContextKeyPayload).(*TokenPayload)
	return p, ok
}

// KeyResolver returns the trusted public keys for a given subject (user handle).
type KeyResolver func(ctx context.Context, subject string) ([]ssh.PublicKey, error)

// Middleware returns HTTP middleware that validates Bearer tokens.
// It extracts the token from the Authorization header, verifies it,
// and stores the payload in the request context.
func Middleware(resolve KeyResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if auth == "" {
				http.Error(w, "missing authorization header", http.StatusUnauthorized)
				return
			}

			if !strings.HasPrefix(auth, "Bearer ") {
				http.Error(w, "invalid authorization scheme", http.StatusUnauthorized)
				return
			}

			token := strings.TrimPrefix(auth, "Bearer ")

			// First decode payload without verification to get the subject
			parts := strings.SplitN(token, ".", 2)
			if len(parts) != 2 {
				http.Error(w, "invalid token format", http.StatusUnauthorized)
				return
			}

			// Peek at the subject to resolve keys
			payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
			if err != nil {
				http.Error(w, "invalid token payload", http.StatusUnauthorized)
				return
			}

			var peekPayload TokenPayload
			if err := json.Unmarshal(payloadJSON, &peekPayload); err != nil {
				http.Error(w, "invalid token payload", http.StatusUnauthorized)
				return
			}

			keys, err := resolve(r.Context(), peekPayload.Subject)
			if err != nil {
				http.Error(w, "failed to resolve keys", http.StatusInternalServerError)
				return
			}

			payload, err := VerifyToken(token, keys)
			if err != nil {
				http.Error(w, "invalid token: "+err.Error(), http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), ContextKeyPayload, payload)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
