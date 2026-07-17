package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPGatewayRouteManagerRejectsUnconfirmedUpsert(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "empty", body: "", want: "empty gateway route observation"},
		{name: "missing hash", body: string([]byte{123, 34, 114, 111, 117, 116, 101, 95, 110, 97, 109, 101, 34, 58, 34, 99, 104, 97, 116, 34, 125}), want: "omitted observed config hash"},
		{name: "missing identity", body: string([]byte{123, 34, 103, 97, 116, 101, 119, 97, 121, 95, 99, 111, 110, 102, 105, 103, 95, 104, 97, 115, 104, 34, 58, 34, 104, 97, 115, 104, 34, 125}), want: "omitted route identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			manager := NewHTTPGatewayRouteManager(GatewayHTTPConfig{Endpoints: map[string]GatewayHTTPEndpointConfig{
				"local": {Type: "http", BaseURL: server.URL},
			}}, server.Client())

			obs, err := manager.UpsertRoute(t.Context(), "local", GatewayRouteSpec{RouteName: "chat", TargetURL: "http://backend:8000"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("UpsertRoute() = (%#v, %v), want error containing %q", obs, err, tc.want)
			}
			if obs != nil {
				t.Fatalf("UpsertRoute() observation = %#v, want nil", obs)
			}
		})
	}
}

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
	if got.RouteName != "chat" || got.TargetURL != "http://backend:8000" || got.Status != "unknown" || got.GatewayConfigHash != "" {
		t.Fatalf("unexpected get observation: %#v", got)
	}
	if err := manager.DeleteRoute(t.Context(), "local", "chat"); err != nil {
		t.Fatalf("delete route: %v", err)
	}
}
