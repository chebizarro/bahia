// Package middleware provides HTTP middleware components.
package middleware

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
)

// authzContextKey is the context key for AuthzContext values.
type authzContextKey struct{}

// ContextWithAuthz returns a new context carrying the AuthzContext.
func ContextWithAuthz(ctx context.Context, authz *auth.AuthzContext) context.Context {
	return context.WithValue(ctx, authzContextKey{}, authz)
}

// GetAuthz extracts the AuthzContext from the context.
// Returns nil if no AuthzContext was set.
func GetAuthz(ctx context.Context) *auth.AuthzContext {
	authz, _ := ctx.Value(authzContextKey{}).(*auth.AuthzContext)
	return authz
}

// RBACConfig configures the RBAC middleware.
type RBACConfig struct {
	// RBAC is the authorization checker.
	RBAC *auth.RBAC
	// OrgIDParam is the URL param name for org ID (default: "orgId").
	OrgIDParam string
	// Required means authentication is required (returns 401 if not authenticated).
	Required bool
}

// RBAC returns middleware that loads the AuthzContext for the current request.
// It extracts the org ID from URL params and loads the user's membership.
func RBAC(cfg RBACConfig) func(http.Handler) http.Handler {
	if cfg.OrgIDParam == "" {
		cfg.OrgIDParam = "orgId"
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := auth.GetPrincipal(r.Context())

			// Check authentication requirement
			if cfg.Required && (p == nil || !p.IsAuthenticated()) {
				http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
				return
			}

			// Get org ID from URL params
			orgIDStr := chi.URLParam(r, cfg.OrgIDParam)
			if orgIDStr == "" {
				// No org context - just pass through
				next.ServeHTTP(w, r)
				return
			}

			orgID, err := uuid.Parse(orgIDStr)
			if err != nil {
				http.Error(w, `{"error":"invalid org ID"}`, http.StatusBadRequest)
				return
			}

			// Load authorization context
			authz, err := cfg.RBAC.LoadAuthzContext(r.Context(), p, orgID)
			if err != nil {
				http.Error(w, `{"error":"authorization check failed"}`, http.StatusInternalServerError)
				return
			}

			// Add to context
			ctx := ContextWithAuthz(r.Context(), authz)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole returns middleware that requires a minimum role.
func RequireRole(role domain.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authz := GetAuthz(r.Context())
			if authz == nil {
				http.Error(w, `{"error":"authorization context not loaded"}`, http.StatusInternalServerError)
				return
			}

			if err := authz.RequireRole(role); err != nil {
				http.Error(w, `{"error":"access denied"}`, http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequirePermission returns middleware that requires a specific permission.
func RequirePermission(perm domain.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authz := GetAuthz(r.Context())
			if authz == nil {
				http.Error(w, `{"error":"authorization context not loaded"}`, http.StatusInternalServerError)
				return
			}

			if err := authz.RequirePermission(perm); err != nil {
				http.Error(w, `{"error":"access denied"}`, http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireMember returns middleware that requires org membership.
func RequireMember() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authz := GetAuthz(r.Context())
			if authz == nil {
				http.Error(w, `{"error":"authorization context not loaded"}`, http.StatusInternalServerError)
				return
			}

			if err := authz.RequireMember(); err != nil {
				http.Error(w, `{"error":"access denied: not a member"}`, http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
