package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// DNSEndpointLister exposes materialized DNS endpoints to the MCP server.
type DNSEndpointLister interface {
	ListDNSEndpoints(ctx context.Context) ([]domain.DNSEndpoint, error)
}

// Resource represents an MCP resource entry exposed by the Bahia server.
type Resource struct {
	URI         string         `json:"uri"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	MIMEType    string         `json:"mimeType,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// GetResources returns discoverable MCP resources exposed by Bahia.
func (s *Server) GetResources(ctx context.Context) ([]Resource, error) {
	resources, err := s.listDNSResources(ctx)
	if err != nil {
		return nil, err
	}
	fipsResources, err := s.listFIPSMeshResources(ctx)
	if err != nil {
		return nil, err
	}
	resources = append(resources, fipsResources...)
	sort.SliceStable(resources, func(i, j int) bool {
		if resources[i].URI != resources[j].URI {
			return resources[i].URI < resources[j].URI
		}
		return resources[i].Name < resources[j].Name
	})
	return resources, nil
}

func (s *Server) listDNSResources(ctx context.Context) ([]Resource, error) {
	if s.dnsEndpoints == nil {
		return nil, nil
	}

	endpoints, err := s.dnsEndpoints.ListDNSEndpoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("list DNS endpoints for MCP resources: %w", err)
	}

	resources := make([]Resource, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint.Health != domain.HealthStatusHealthy {
			continue
		}
		fqdn := strings.TrimSpace(endpoint.FQDN)
		if fqdn == "" {
			continue
		}
		resources = append(resources, dnsEndpointResource(endpoint, fqdn))
	}

	sort.Slice(resources, func(i, j int) bool {
		if resources[i].URI != resources[j].URI {
			return resources[i].URI < resources[j].URI
		}
		return resources[i].Name < resources[j].Name
	})
	return resources, nil
}

func dnsEndpointResource(endpoint domain.DNSEndpoint, fqdn string) Resource {
	metadata := map[string]any{
		"protocol":     strings.TrimSpace(endpoint.Protocol),
		"address":      strings.TrimSpace(endpoint.Address),
		"port":         endpointPort(endpoint),
		"health":       string(endpoint.Health),
		"capabilities": append([]string(nil), endpoint.Capabilities...),
		"runtime":      strings.TrimSpace(endpoint.Runtime),
		"hardware":     strings.TrimSpace(endpoint.Hardware),
	}

	return Resource{
		URI:         "bahia://dns/endpoint/" + fqdn,
		Name:        dnsEndpointResourceName(endpoint),
		Description: dnsEndpointDescription(endpoint),
		MIMEType:    "application/json",
		Metadata:    metadata,
	}
}

func dnsEndpointResourceName(endpoint domain.DNSEndpoint) string {
	name := strings.TrimSpace(endpoint.Name)
	environment := strings.TrimSpace(endpoint.Environment)
	if environment == "" {
		return name
	}
	return name + "." + environment
}

func dnsEndpointDescription(endpoint domain.DNSEndpoint) string {
	parts := make([]string, 0, 3)
	if len(endpoint.Capabilities) > 0 {
		capabilities := append([]string(nil), endpoint.Capabilities...)
		sort.Strings(capabilities)
		parts = append(parts, "capabilities "+strings.Join(capabilities, ", "))
	}
	if runtime := strings.TrimSpace(endpoint.Runtime); runtime != "" {
		parts = append(parts, "runtime "+runtime)
	}
	if hardware := strings.TrimSpace(endpoint.Hardware); hardware != "" {
		parts = append(parts, "hardware "+hardware)
	}
	if len(parts) == 0 {
		return "Healthy DNS endpoint"
	}
	return "Healthy DNS endpoint with " + strings.Join(parts, "; ")
}

func endpointPort(endpoint domain.DNSEndpoint) any {
	if endpoint.Port == nil {
		return nil
	}
	return *endpoint.Port
}

func (s *Server) listFIPSMeshResources(ctx context.Context) ([]Resource, error) {
	nodes, err := s.listFIPSMeshNodes(ctx)
	if err != nil {
		return nil, err
	}
	resources := make([]Resource, 0, len(nodes))
	for _, node := range nodes {
		if strings.TrimSpace(node.DNSHostname) == "" {
			continue
		}
		resources = append(resources, fipsMeshNodeResource(node))
	}
	return resources, nil
}

func fipsMeshNodeResource(node fipsMeshNode) Resource {
	metadata := map[string]any{
		"worker_pubkey":       node.WorkerPubkey,
		"worker_npub":         node.WorkerNpub,
		"overlay_address":     node.OverlayAddress,
		"transport_endpoints": node.TransportEndpoints,
		"mesh_health":         node.MeshHealth,
		"dns_hostname":        node.DNSHostname,
		"health":              node.Health,
		"projection_status":   node.ProjectionStatus,
		"source":              node.Source,
	}
	return Resource{
		URI:         "bahia://fips/mesh/node/" + node.DNSHostname,
		Name:        firstNonEmpty(node.WorkerName, node.EndpointName, node.DNSHostname),
		Description: "FIPS mesh node projected from Bahia worker and DNS state",
		MIMEType:    "application/json",
		Metadata:    metadata,
	}
}
