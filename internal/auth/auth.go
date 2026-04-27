package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ClaimsKey is the legacy context key for JWT claims.
// Deprecated: use GetPrincipal() instead.
type contextKey string

const ClaimsContextKey contextKey = "auth_claims"

// Claims represents the JWT payload claims.
type Claims struct {
	Subject   string `json:"sub"`
	ExpiresAt int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`
	Issuer    string `json:"iss"`
	// PubKey is an optional Nostr public key claim.
	PubKey string `json:"pubkey,omitempty"`
}

// MiddlewareConfig controls multi-method authentication behaviour.
type MiddlewareConfig struct {
	Enabled        bool
	JWTSecret      string
	NIP98Validator *NIP98Validator // nil = NIP-98 disabled
	NIP05Resolver  *NIP05Resolver  // nil = NIP-05 resolution disabled
}

// Middleware returns an HTTP middleware that validates authentication and
// populates the request context with both a Principal and legacy Claims.
// When auth is disabled, all requests pass through with no Principal set.
func Middleware(enabled bool, jwtSecret string) func(http.Handler) http.Handler {
	return MiddlewareFromConfig(MiddlewareConfig{
		Enabled:   enabled,
		JWTSecret: jwtSecret,
	})
}

// MiddlewareFromConfig is the multi-method variant of Middleware.
func MiddlewareFromConfig(cfg MiddlewareConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !cfg.Enabled {
			return next
		}

		if cfg.JWTSecret == "" {
			// Fail closed: if auth is enabled but no secret is configured, reject all requests.
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `{"error":"auth enabled but jwt_secret not configured"}`, http.StatusInternalServerError)
			})
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, `{"error":"missing Authorization header"}`, http.StatusUnauthorized)
				return
			}

			// Dispatch based on auth scheme.
			switch {
			case strings.HasPrefix(authHeader, "Bearer "):
				handleBearer(w, r, next, authHeader, cfg.JWTSecret)
			case strings.HasPrefix(authHeader, "Nostr "):
				handleNostr(w, r, next, authHeader, cfg.NIP98Validator, cfg.NIP05Resolver)
			default:
				http.Error(w, `{"error":"unsupported Authorization scheme"}`, http.StatusUnauthorized)
			}
		})
	}
}

// handleBearer validates a Bearer JWT token and sets both Principal and Claims on the context.
func handleBearer(w http.ResponseWriter, r *http.Request, next http.Handler, authHeader, jwtSecret string) {
	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	if tokenString == "" {
		http.Error(w, `{"error":"empty bearer token"}`, http.StatusUnauthorized)
		return
	}

	claims, err := ValidateToken(tokenString, jwtSecret)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusUnauthorized)
		return
	}

	// Build a Principal from the JWT claims.
	principal := &Principal{
		Subject: claims.Subject,
		Method:  MethodJWT,
		PubKey:  claims.PubKey,
	}

	// Set both Principal (new) and Claims (legacy) on the context.
	ctx := ContextWithPrincipal(r.Context(), principal)
	ctx = context.WithValue(ctx, ClaimsContextKey, claims)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// handleNostr validates a NIP-98 Nostr auth event and sets a Principal on the context.
func handleNostr(w http.ResponseWriter, r *http.Request, next http.Handler, authHeader string, validator *NIP98Validator, resolver *NIP05Resolver) {
	if validator == nil {
		http.Error(w, `{"error":"NIP-98 auth not configured"}`, http.StatusUnauthorized)
		return
	}

	token := strings.TrimPrefix(authHeader, "Nostr ")
	if token == "" {
		http.Error(w, `{"error":"empty Nostr auth token"}`, http.StatusUnauthorized)
		return
	}

	principal, err := validator.Validate(token, r)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"NIP-98 validation failed: %s"}`, err.Error()), http.StatusUnauthorized)
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

// ValidateToken parses and validates a JWT token string using HMAC-SHA256.
// Returns the claims if valid, or an error describing why validation failed.
func ValidateToken(tokenString, secret string) (*Claims, error) {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid token format")
	}

	headerB64, payloadB64, signatureB64 := parts[0], parts[1], parts[2]

	// Verify the header declares HS256.
	headerJSON, err := base64.RawURLEncoding.DecodeString(headerB64)
	if err != nil {
		return nil, fmt.Errorf("invalid token header encoding")
	}

	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("invalid token header")
	}
	if header.Alg != "HS256" {
		return nil, fmt.Errorf("unsupported signing algorithm: %s", header.Alg)
	}

	// Verify signature.
	signingInput := headerB64 + "." + payloadB64
	expectedSig, err := computeHMACSHA256(signingInput, secret)
	if err != nil {
		return nil, fmt.Errorf("signature computation failed")
	}

	actualSig, err := base64.RawURLEncoding.DecodeString(signatureB64)
	if err != nil {
		return nil, fmt.Errorf("invalid token signature encoding")
	}

	if !hmac.Equal(expectedSig, actualSig) {
		return nil, fmt.Errorf("invalid token signature")
	}

	// Decode and validate claims.
	payloadJSON, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, fmt.Errorf("invalid token payload encoding")
	}

	var claims Claims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return nil, fmt.Errorf("invalid token payload")
	}

	// Check expiration.
	if claims.ExpiresAt > 0 && time.Now().Unix() > claims.ExpiresAt {
		return nil, fmt.Errorf("token expired")
	}

	return &claims, nil
}

// GenerateToken creates a signed JWT token for the given subject.
// This is primarily useful for testing; production systems should use a proper auth service.
func GenerateToken(subject, secret string, expiry time.Duration) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

	now := time.Now().Unix()
	claims := Claims{
		Subject:   subject,
		IssuedAt:  now,
		ExpiresAt: now + int64(expiry.Seconds()),
		Issuer:    "bahia",
	}

	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshaling claims: %w", err)
	}

	payload := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := header + "." + payload

	sig, err := computeHMACSHA256(signingInput, secret)
	if err != nil {
		return "", fmt.Errorf("computing signature: %w", err)
	}

	signature := base64.RawURLEncoding.EncodeToString(sig)
	return signingInput + "." + signature, nil
}

// GetClaims extracts the JWT claims from the request context.
// Deprecated: use GetPrincipal() for method-agnostic identity.
// Returns nil if no claims are present (e.g. auth is disabled or NIP-98 was used).
func GetClaims(ctx context.Context) *Claims {
	claims, _ := ctx.Value(ClaimsContextKey).(*Claims)
	return claims
}

func computeHMACSHA256(input, secret string) ([]byte, error) {
	mac := hmac.New(sha256.New, []byte(secret))
	_, err := mac.Write([]byte(input))
	if err != nil {
		return nil, err
	}
	return mac.Sum(nil), nil
}
