package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/readmodel"
	"github.com/openagentsinc/bahia/internal/repository"
)

type VirtualizationHandler struct {
	Query readmodel.VirtualizationQuery
	RBAC  *auth.RBAC
}

func (h *VirtualizationHandler) Read(kind domain.VirtualizationResourceKind, list bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := auth.GetPrincipal(r.Context())
		if p == nil || !p.IsAuthenticated() {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if _, err := nostr.PubKeyFromHex(p.PubKey); err != nil {
			writeError(w, http.StatusUnauthorized, "authenticated public key required")
			return
		}
		org, err := uuid.Parse(r.URL.Query().Get("org_id"))
		if err != nil || org == uuid.Nil {
			writeError(w, http.StatusBadRequest, "org_id is required")
			return
		}
		if h.RBAC == nil {
			writeError(w, http.StatusServiceUnavailable, "virtualization authorization unavailable")
			return
		}
		if err := h.RBAC.CheckPermission(r.Context(), p, org, domain.PermReadDeployments); err != nil {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		var data any
		if list {
			limit, offset := 50, 0
			if s := r.URL.Query().Get("limit"); s != "" {
				limit, err = strconv.Atoi(s)
				if err != nil {
					writeError(w, 400, "invalid pagination")
					return
				}
			}
			if s := r.URL.Query().Get("offset"); s != "" {
				offset, err = strconv.Atoi(s)
				if err != nil {
					writeError(w, 400, "invalid pagination")
					return
				}
			}
			data, err = h.Query.List(r.Context(), org, kind, limit, offset)
		} else {
			id, e := uuidParam(r, "id")
			if e != nil || id == uuid.Nil {
				writeError(w, 400, "invalid resource id")
				return
			}
			data, err = h.Query.Get(r.Context(), org, kind, id)
		}
		if err != nil {
			switch {
			case errors.Is(err, repository.ErrNotFound):
				writeError(w, 404, "resource not found")
			case errors.Is(err, domain.ErrInvalidValue):
				writeError(w, 400, "invalid virtualization query")
			default:
				writeError(w, 503, "virtualization unavailable")
			}
			return
		}
		writeData(w, http.StatusOK, data)
	}
}
