package reconcile

import (
	"reflect"
	"sort"

	"github.com/openagentsinc/bahia/internal/domain"
)

// VMObservationMaterialChange ignores samples, heartbeat times and fencing
// counters, but includes each independent state axis and public access changes.
func VMObservationMaterialChange(before, after *domain.VMObservation) bool {
	if before == nil || after == nil {
		return before != after
	}
	normalize := func(in domain.VMObservation) domain.VMObservation {
		in.VMObservationStamp = domain.VMObservationStamp{}
		in.RuntimeObservedAt = nil
		in.Usage = nil
		in.Connections = append([]domain.VMPublicConnection(nil), in.Connections...)
		sort.Slice(in.Connections, func(i, j int) bool {
			a, b := in.Connections[i], in.Connections[j]
			if a.Protocol != b.Protocol {
				return a.Protocol < b.Protocol
			}
			if a.Address != b.Address {
				return a.Address < b.Address
			}
			if a.Port != b.Port {
				return a.Port < b.Port
			}
			if a.Username != b.Username {
				return a.Username < b.Username
			}
			return a.ResourceID.String() < b.ResourceID.String()
		})
		return in
	}
	return !reflect.DeepEqual(normalize(*before), normalize(*after))
}
