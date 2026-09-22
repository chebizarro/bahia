//go:build !linux && !darwin

package firecracker

import (
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

func newColdFileWatch([]string) (coldFileWatch, error) {
	return nil, vm.ProviderError(domain.VMErrorUnsupported, nil)
}
