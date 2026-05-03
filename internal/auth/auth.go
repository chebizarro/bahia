package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// MiddlewareConfig controls NIP-98-only authentication behaviour.
type MiddlewareConfig struct {
	Enabled        bool
	NIP98Validator *NIP98Validator // required when Enabled is true
	NIP05Resolver  *NIP05Resolver  // nil = NIP-05 resolution disabled
}

// Middleware returns an HTTP middleware that validates NIP-98 authentication.
// When auth is disabled, all requests pass through with no Principal set.
func Middleware(enabled bool) func(http.Handler) http.Handler {
	return MiddlewareFromConfig(MiddlewareConfig{
		Enabled: enabled,
	})
}

// MiddlewareFromConfig validates protected requests with NIP-98 only.
func MiddlewareFromConfig(cfg MiddlewareConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !cfg.Enabled {
			return next
		}
		if cfg.NIP98Validator == nil {
			// Fail closed: if auth is enabled but no NIP-98 validator is configured, reject all requests.
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeAuthError(w, http.StatusInternalServerError, "auth enabled but NIP-98 validator not configured")
			})
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				writeAuthError(w, http.StatusUnauthorized, "missing Authorization header")
				return
			}

			if !strings.HasPrefix(authHeader, "Nostr ") {
				writeAuthError(w, http.StatusUnauthorized, "unsupported Authorization scheme")
				return
			}

			handleNostr(w, r, next, authHeader, cfg.NIP98Validator, cfg.NIP05Resolver)
		})
	}
}

// handleNostr validates a NIP-98 Nostr auth event and sets a Principal on the context.
func handleNostr(w http.ResponseWriter, r *http.Request, next http.Handler, authHeader string, validator *NIP98Validator, resolver *NIP05Resolver) {
	if validator == nil {
		writeAuthError(w, http.StatusInternalServerError, "auth enabled but NIP-98 validator not configured")
		return
	}

	token := strings.TrimPrefix(authHeader, "Nostr ")
	if token == "" {
		writeAuthError(w, http.StatusUnauthorized, "empty Nostr auth token")
		return
	}

	principal, err := validator.Validate(token, r)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, fmt.Sprintf("NIP-98 validation failed: %s", err.Error()))
		return
	}

	// Attempt NIP-05 resolution to enrich the principal.
	if resolver != nil && principal.PubKey != "" {
		if cached := resolver.GetCached(principal.PubKey); cached != "" {
			principal.NIP05 = cached
		}
	}

	ctx := ContextWithPrincipal(r.Context(), principal)
	next.ServeHTTP(w, r.WithContext(ctx))
}

func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
