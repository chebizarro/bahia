package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/openagentsinc/bahia/internal/auth"
)

// OperatorAccessConfig configures system-operator authorization for privileged routes.
type OperatorAccessConfig struct {
	AllowedSubjects []string
	AllowedPubkeys  []string
	AllowedEmails   []string
	NIP05Resolver   *auth.NIP05Resolver
}

// RequireOperator requires an authenticated principal whose identity appears in
// at least one configured operator allowlist.
func RequireOperator(cfg OperatorAccessConfig) func(http.Handler) http.Handler {
	subjects := setFrom(cfg.AllowedSubjects, false)
	pubkeys := setFrom(cfg.AllowedPubkeys, true)
	emails := setFrom(cfg.AllowedEmails, true)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := auth.GetPrincipal(r.Context())
			if p == nil || !p.IsAuthenticated() {
				writeMiddlewareError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			if !operatorAllowed(r.Context(), p, subjects, pubkeys, emails, cfg.NIP05Resolver) {
				writeMiddlewareError(w, http.StatusForbidden, "operator access required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func operatorAllowed(ctx context.Context, p *auth.Principal, subjects, pubkeys, emails map[string]struct{}, resolver *auth.NIP05Resolver) bool {
	if p == nil {
		return false
	}
	if _, ok := subjects[strings.TrimSpace(p.Subject)]; ok {
		return true
	}
	if _, ok := pubkeys[strings.ToLower(strings.TrimSpace(p.PubKey))]; ok && p.PubKey != "" {
		return true
	}
	nip05 := strings.ToLower(strings.TrimSpace(p.NIP05))
	if _, ok := emails[nip05]; ok && nip05 != "" {
		return true
	}
	subjectEmail := strings.ToLower(strings.TrimSpace(p.Subject))
	if strings.Contains(subjectEmail, "@") {
		if _, ok := emails[subjectEmail]; ok {
			return true
		}
	}
	if resolver != nil && p.PubKey != "" {
		for email := range emails {
			if resolver.Verify(ctx, email, p.PubKey) {
				return true
			}
		}
	}
	return false
}

func setFrom(values []string, lower bool) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if lower {
			value = strings.ToLower(value)
		}
		out[value] = struct{}{}
	}
	return out
}

func writeMiddlewareError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
