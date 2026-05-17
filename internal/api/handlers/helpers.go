package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/api/middleware"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, dto.APIResponse{Data: data})
}

const migrationSunset = "Sat, 01 Aug 2026 00:00:00 GMT"

func SetDeprecationHeaders(w http.ResponseWriter) {
	writeDeprecationHeaders(w)
}

func writeDeprecationHeaders(w http.ResponseWriter) {
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Sunset", migrationSunset)
	w.Header().Set("Link", "</docs/control-planes.md>; rel=\"deprecation\"")
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, dto.APIResponse{Error: msg})
}

func writeMessage(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, dto.APIResponse{Message: msg})
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func uuidParam(r *http.Request, name string) (uuid.UUID, error) {
	return uuid.Parse(chi.URLParam(r, name))
}

func authzOrgID(r *http.Request) uuid.UUID {
	if authz := middleware.GetAuthz(r.Context()); authz != nil {
		return authz.OrgID
	}
	return uuid.Nil
}

func requireMember(w http.ResponseWriter, r *http.Request) bool {
	authz := middleware.GetAuthz(r.Context())
	if authz == nil {
		return true
	}
	if err := authz.RequireMember(); err != nil {
		writeError(w, http.StatusForbidden, "access denied")
		return false
	}
	return true
}

func serviceInAuthzOrg(w http.ResponseWriter, r *http.Request, svcOrgID uuid.UUID) bool {
	orgID := authzOrgID(r)
	if orgID == uuid.Nil {
		return true
	}
	if svcOrgID != orgID {
		writeError(w, http.StatusForbidden, "access denied")
		return false
	}
	return true
}

func authenticatedSubject(r *http.Request) (string, bool) {
	p := auth.GetPrincipal(r.Context())
	if p == nil || !p.IsAuthenticated() {
		return "", false
	}
	subject := strings.TrimSpace(p.Subject)
	if subject == "" {
		return "", false
	}
	return subject, true
}

func requirePermission(w http.ResponseWriter, r *http.Request, perm domain.Permission) bool {
	authz := middleware.GetAuthz(r.Context())
	if authz == nil {
		return true
	}
	if err := authz.RequireMember(); err != nil {
		writeError(w, http.StatusForbidden, "access denied")
		return false
	}
	if err := authz.RequirePermission(perm); err != nil {
		writeError(w, http.StatusForbidden, "access denied")
		return false
	}
	return true
}

func queryInt(r *http.Request, name string, defaultVal int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return defaultVal
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return i
}
