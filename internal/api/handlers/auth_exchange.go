package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/auth"
)

// AuthExchangeHandler handles Nostr-to-JWT authentication exchange.
type AuthExchangeHandler struct {
	jwtSecret string
	validator *auth.NIP98Validator
}

// NewAuthExchangeHandler creates a new auth exchange handler.
func NewAuthExchangeHandler(jwtSecret string) *AuthExchangeHandler {
	return &AuthExchangeHandler{
		jwtSecret: jwtSecret,
		validator: auth.NewNIP98Validator(auth.NIP98Config{
			MaxSkew: 5 * time.Minute,
		}),
	}
}

// exchangeRequest is the incoming request payload.
type exchangeRequest struct {
	Event nostr.Event `json:"event"`
}

// exchangeResponse is the outgoing response payload.
type exchangeResponse struct {
	Token      string              `json:"token"`
	ExpiresAt  int64               `json:"expires_at"`
	Principal  exchangePrincipal   `json:"principal"`
}

type exchangePrincipal struct {
	Pubkey string `json:"pubkey"`
	Role   string `json:"role"`
}

// Exchange validates a NIP-98 signed event and issues a JWT token.
// POST /api/v1/auth/nostr
func (h *AuthExchangeHandler) Exchange(w http.ResponseWriter, r *http.Request) {
	var req exchangeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %s", err.Error()))
		return
	}

	// Validate event kind
	if req.Event.Kind != 27235 {
		writeError(w, http.StatusBadRequest, "event must be kind 27235 (NIP-98 HTTP Auth)")
		return
	}

	// Verify event signature
	ok, err := req.Event.CheckSignature()
	if err != nil || !ok {
		writeError(w, http.StatusUnauthorized, "invalid event signature")
		return
	}

	// Check created_at is within 5 minutes
	now := time.Now()
	eventTime := time.Unix(int64(req.Event.CreatedAt), 0)
	if now.Sub(eventTime) > 5*time.Minute || eventTime.After(now.Add(time.Minute)) {
		writeError(w, http.StatusUnauthorized, "event created_at must be within 5 minutes")
		return
	}

	// Validate required tags
	var hasMethod, hasURL bool
	var method, url string

	for _, tag := range req.Event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "method":
			hasMethod = true
			method = tag[1]
		case "u":
			hasURL = true
			url = tag[1]
		}
	}

	if !hasMethod {
		writeError(w, http.StatusBadRequest, "event missing 'method' tag")
		return
	}

	if !hasURL {
		writeError(w, http.StatusBadRequest, "event missing 'u' tag")
		return
	}

	// Validate method matches
	if method != "POST" {
		writeError(w, http.StatusBadRequest, "method tag must be POST")
		return
	}

	// Validate URL matches (allow both relative and absolute)
	expectedPath := "/api/v1/auth/nostr"
	if url != expectedPath && url != r.URL.String() {
		// Also try to match if the URL ends with our expected path
		if len(url) < len(expectedPath) || url[len(url)-len(expectedPath):] != expectedPath {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("u tag must match %s", expectedPath))
			return
		}
	}

	// Generate JWT token with pubkey claim
	// Use a 24-hour expiry for the JWT
	expiry := 24 * time.Hour
	token, err := auth.GenerateTokenWithPubKey(req.Event.PubKey, req.Event.PubKey, h.jwtSecret, expiry)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	expiresAt := now.Add(expiry).Unix()

	resp := exchangeResponse{
		Token:     token,
		ExpiresAt: expiresAt,
		Principal: exchangePrincipal{
			Pubkey: req.Event.PubKey,
			Role:   "user", // Default role; extend as needed
		},
	}

	writeData(w, http.StatusOK, resp)
}
