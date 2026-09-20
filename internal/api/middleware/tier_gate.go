// Package middleware provides HTTP middleware for the Bahia API.
package middleware

import (
	"encoding/json"
	"net/http"
	"reflect"

	"go.uber.org/zap"
)

// TierGate returns 503 for routes whose required tier is above the active mode policy tier.
type routeErrorBodier interface {
	RouteErrorBody(requiredTier int) map[string]any
}

func TierGate(policy any, requiredTier int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if policy == nil || routeEnabled(policy, requiredTier) {
				next.ServeHTTP(w, r)
				return
			}

			body := policy.(routeErrorBodier).RouteErrorBody(requiredTier)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			if err := json.NewEncoder(w).Encode(body); err != nil {
				zap.L().Error("failed to encode tier gate response", zap.Error(err))
			}
		})
	}
}

func routeEnabled(policy any, requiredTier int) bool {
	value := reflect.ValueOf(policy)
	if !value.IsValid() || (value.Kind() == reflect.Pointer && value.IsNil()) {
		return true
	}
	method := value.MethodByName("RouteEnabled")
	if !method.IsValid() || method.Type().NumIn() != 1 || method.Type().NumOut() != 1 || method.Type().Out(0).Kind() != reflect.Bool {
		return true
	}
	argType := method.Type().In(0)
	arg := reflect.ValueOf(requiredTier)
	if arg.Type().ConvertibleTo(argType) {
		out := method.Call([]reflect.Value{arg.Convert(argType)})
		return out[0].Bool()
	}
	return true
}
