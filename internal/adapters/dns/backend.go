package dns

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// Backend is the pluggable boundary for DNS zone snapshots.
type Backend interface {
	BackendType() domain.DNSBackendType
	Health(ctx context.Context) error
	ListRecords(ctx context.Context, zone domain.DNSZone) ([]domain.DNSRecord, error)
	SyncZone(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord) error
}

// Resolver resolves configured DNS backends by durable backend reference.
type Resolver interface {
	Resolve(ref string) (Backend, bool)
	Refs() []string
}

// BackendRegistration binds a durable backend reference to a backend instance.
type BackendRegistration struct {
	Ref     string
	Backend Backend
}

// StaticResolver resolves a fixed set of DNS backends by reference.
type StaticResolver struct {
	backends map[string]Backend
}

// NewStaticResolver creates a static DNS backend resolver.
func NewStaticResolver(registrations ...BackendRegistration) (*StaticResolver, error) {
	resolver := &StaticResolver{backends: make(map[string]Backend, len(registrations))}
	for _, registration := range registrations {
		backend := registration.Backend
		if backend == nil {
			continue
		}
		ref := strings.TrimSpace(registration.Ref)
		if ref == "" {
			return nil, fmt.Errorf("DNS backend ref is required")
		}
		backendType := backend.BackendType()
		if !backendType.IsValid() {
			return nil, fmt.Errorf("DNS backend %q type %q is not valid", ref, backendType)
		}
		if _, exists := resolver.backends[ref]; exists {
			return nil, fmt.Errorf("duplicate DNS backend registration for %q", ref)
		}
		resolver.backends[ref] = backend
	}
	return resolver, nil
}

// MustStaticResolver creates a static resolver or panics if registrations are invalid.
func MustStaticResolver(registrations ...BackendRegistration) *StaticResolver {
	resolver, err := NewStaticResolver(registrations...)
	if err != nil {
		panic(err)
	}
	return resolver
}

func (r *StaticResolver) Resolve(ref string) (Backend, bool) {
	if r == nil || len(r.backends) == 0 {
		return nil, false
	}
	backend, ok := r.backends[strings.TrimSpace(ref)]
	return backend, ok
}

func (r *StaticResolver) Refs() []string {
	if r == nil || len(r.backends) == 0 {
		return nil
	}
	refs := make([]string, 0, len(r.backends))
	for ref := range r.backends {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

var _ Resolver = (*StaticResolver)(nil)
