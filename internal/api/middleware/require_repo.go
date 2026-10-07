package middleware

import (
	"encoding/json"
	"net/http"
	"reflect"

	"go.uber.org/zap"
)

// RequireRepo returns middleware that responds with 503 when the dependency is
// nil. Routes whose backing repository is absent (e.g. because Postgres is not
// configured) must be unreachable, not panic-prone. This handles
// ModePolicy/TierGate.
func RequireRepo(dep any) func(http.Handler) http.Handler {
	if isNilDep(dep) {
		return func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				if err := json.NewEncoder(w).Encode(map[string]any{
					"error":  "dependency unavailable",
					"reason": "required repository is not configured",
				}); err != nil {
					zap.L().Error("failed to encode require-repo response", zap.Error(err))
				}
			})
		}
	}
	return func(next http.Handler) http.Handler { return next }
}

func isNilDep(dep any) bool {
	if dep == nil {
		return true
	}
	v := reflect.ValueOf(dep)
	return (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil()
}
