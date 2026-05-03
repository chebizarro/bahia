// Package auth provides NIP-98 authentication middleware.
//
// The Principal type is a unified identity representation for external
// NIP-98 auth and internal/system callers. Downstream handlers call
// GetPrincipal(ctx) instead of being coupled to transport details.
package auth

import "context"

// AuthMethod identifies how a request was authenticated.
type AuthMethod string

const (
	// MethodNone indicates no authentication (auth disabled).
	MethodNone AuthMethod = ""
	// MethodNIP98 indicates authentication via Nostr NIP-98 HTTP Auth event.
	MethodNIP98 AuthMethod = "nip98"
	// MethodSystem is used for internal / service-to-service calls.
	MethodSystem AuthMethod = "system"
)

// Principal represents an authenticated identity regardless of auth method.
type Principal struct {
	// Subject is the primary identifier (e.g. username, npub, service name).
	Subject string `json:"sub"`
	// Method is the authentication method used to establish identity.
	Method AuthMethod `json:"method"`
	// PubKey is the Nostr public key (hex), populated for NIP-98 principals.
	PubKey string `json:"pubkey,omitempty"`
	// NIP05 is the resolved NIP-05 identifier (e.g. "user@domain.com").
	// Empty if resolution failed or was not attempted.
	NIP05 string `json:"nip05,omitempty"`
	// Roles holds granted roles for future RBAC support.
	Roles []string `json:"roles,omitempty"`
}

// IsAuthenticated returns true if the principal was established by a real
// authentication method (not MethodNone).
func (p *Principal) IsAuthenticated() bool {
	return p != nil && p.Method != MethodNone
}

// HasRole checks whether the principal holds the given role.
func (p *Principal) HasRole(role string) bool {
	if p == nil {
		return false
	}
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// principalContextKey is the context key for Principal values.
type principalContextKey struct{}

// ContextWithPrincipal returns a new context carrying the given Principal.
func ContextWithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

// GetPrincipal extracts the authenticated Principal from the context.
// Returns nil when auth is disabled or no principal was set.
func GetPrincipal(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalContextKey{}).(*Principal)
	return p
}

// SystemPrincipal returns a Principal for internal/system operations.
func SystemPrincipal(name string) *Principal {
	return &Principal{
		Subject: name,
		Method:  MethodSystem,
		Roles:   []string{"admin"},
	}
}
