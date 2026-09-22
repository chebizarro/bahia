package router

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/openagentsinc/bahia/internal/api/handlers"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/readmodel"
)

// RegisterVirtualizationRoutes runs inside the authenticated, read-rate-limited
// API group. Each handler independently requires a principal and tenant RBAC,
// even when global authentication is disabled. There are no REST mutation routes.
func RegisterVirtualizationRoutes(r chi.Router, deps RouterDeps, tierGate func(http.Handler) http.Handler) {
	h := &handlers.VirtualizationHandler{Query: readmodel.VirtualizationQuery{Repository: deps.Virtualization}, RBAC: deps.RBAC}
	for path, kind := range map[string]domain.VirtualizationResourceKind{
		"/virtualization-hosts": domain.VirtualizationHostResource, "/vm-images": domain.VMImageResource, "/persistent-vms": domain.PersistentVMResource, "/execution-planes": domain.ExecutionPlaneResource, "/vm-checkpoints": domain.VMCheckpointResource, "/vm-exports": domain.VMExportResource, "/vm-operations": domain.VMOperationResource,
	} {
		if kind != domain.VMOperationResource {
			r.With(tierGate).Get(path, h.Read(kind, true))
		}
		r.With(tierGate).Get(path+"/{id}", h.Read(kind, false))
	}
}
