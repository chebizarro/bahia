// Package middleware provides HTTP middleware for the Bahia API.
package middleware

import (
	"encoding/json"
	"net/http"
	"reflect"
)

// TierGate returns 503 for routes whose required tier is above the active mode policy tier.
func TierGate(policy any, requiredTier int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if policy == nil || routeEnabled(policy, requiredTier) {
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":         "route unavailable in current mode",
				"mode":          stringField(policy, "RequestedMode"),
				"active_tier":   intField(policy, "ActiveTier"),
				"required_tier": requiredTier,
			})
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

func stringField(policy any, name string) string {
	field := policyField(policy, name)
	if !field.IsValid() {
		return ""
	}
	if field.Kind() == reflect.String {
		return field.String()
	}
	if field.Type().ConvertibleTo(reflect.TypeOf("")) {
		return field.Convert(reflect.TypeOf("")).String()
	}
	return ""
}

func intField(policy any, name string) int {
	field := policyField(policy, name)
	if !field.IsValid() {
		return 0
	}
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(field.Int())
	default:
		return 0
	}
}

func policyField(policy any, name string) reflect.Value {
	value := reflect.ValueOf(policy)
	if !value.IsValid() {
		return reflect.Value{}
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return reflect.Value{}
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	return value.FieldByName(name)
}
