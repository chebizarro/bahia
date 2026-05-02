package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPGatewayRouteManagerUpsertGetDelete(t *testing.T) {
	var seenAuth string
	var stored GatewayRouteSpec
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/v1/routes/chat" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodPut:
			if err := json.NewDecoder(r.Body).Decode(&stored); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"route_name":          stored.RouteName,
				"public_model":        stored.PublicModel,
				"path":                stored.Path,
				"target_url":          stored.TargetURL,
				"gateway_config_hash": stored.ManagedConfigHash(),
				"status":              "synced",
			})
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(stored)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	manager := NewHTTPGatewayRouteManager(GatewayHTTPConfig{Endpoints: map[string]GatewayHTTPEndpointConfig{
		"local": {Type: "http", BaseURL: server.URL, AuthToken: "secret"},
	}}, server.Client())

	spec := GatewayRouteSpec{RouteName: "chat", PublicModel: "bahia-chat", TargetURL: "http://backend:8000", Headers: map[string]string{"X-Test": "yes"}}
	obs, err := manager.UpsertRoute(t.Context(), "local", spec)
	if err != nil {
		t.Fatalf("upsert route: %v", err)
	}
	if seenAuth != "Bearer secret" {
		t.Fatalf("auth header = %q", seenAuth)
	}
	if obs.Status != "synced" || obs.GatewayConfigHash != spec.ManagedConfigHash() {
		t.Fatalf("unexpected observation: %#v", obs)
	}
	if stored.Path != "/v1/models/chat" {
		t.Fatalf("canonical path not sent: %#v", stored)
	}

	got, err := manager.GetRoute(t.Context(), "local", "chat")
	if err != nil {
		t.Fatalf("get route: %v", err)
	}
	if got.RouteName != "chat" || got.TargetURL != "http://backend:8000" || got.Status != "synced" {
		t.Fatalf("unexpected get observation: %#v", got)
	}
	if err := manager.DeleteRoute(t.Context(), "local", "chat"); err != nil {
		t.Fatalf("delete route: %v", err)
	}
}
