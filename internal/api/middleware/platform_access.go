package middleware

import (
	"net/http"
	"strings"

	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PlatformAccessConfig defines the coarse authorization boundary that must be
// crossed before any control-plane route is evaluated. Resource middleware
// still enforces membership in the exact target organization.
type PlatformAccessConfig struct {
	RBAC                  *auth.RBAC
	BootstrapOwnerPubkeys []string
	AllowUnaffiliated     func(*http.Request) bool
	MinimumRole           domain.Role
}

// PlatformAccess rejects authenticated but unknown principals. A valid Nostr
// signature establishes identity; it does not grant Bahia access.
func PlatformAccess(cfg PlatformAccessConfig) func(http.Handler) http.Handler {
	bootstrapOwners := make(map[string]struct{}, len(cfg.BootstrapOwnerPubkeys))
	for _, pubkey := range cfg.BootstrapOwnerPubkeys {
		pubkey = strings.ToLower(strings.TrimSpace(pubkey))
		if pubkey != "" {
			bootstrapOwners[pubkey] = struct{}{}
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := auth.GetPrincipal(r.Context())
			if principal == nil || !principal.IsAuthenticated() || strings.TrimSpace(principal.PubKey) == "" {
				http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
				return
			}
			if cfg.AllowUnaffiliated != nil && cfg.AllowUnaffiliated(r) {
				next.ServeHTTP(w, r)
				return
			}
			pubkey := strings.ToLower(strings.TrimSpace(principal.PubKey))
			if _, ok := bootstrapOwners[pubkey]; ok {
				next.ServeHTTP(w, r)
				return
			}
			if cfg.RBAC == nil {
				http.Error(w, `{"error":"authorization not configured"}`, http.StatusInternalServerError)
				return
			}
			memberships, err := cfg.RBAC.GetUserOrgs(r.Context(), pubkey)
			if err != nil {
				http.Error(w, `{"error":"authorization check failed"}`, http.StatusInternalServerError)
				return
			}
			if len(memberships) == 0 {
				http.Error(w, `{"error":"access denied: no organization membership"}`, http.StatusForbidden)
				return
			}
			if cfg.MinimumRole != "" {
				for _, membership := range memberships {
					if domain.HasAtLeastRole(membership.Role, cfg.MinimumRole) {
						next.ServeHTTP(w, r)
						return
					}
				}
				http.Error(w, `{"error":"access denied: insufficient platform role"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
