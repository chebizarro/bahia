package mcp

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/openagentsinc/bahia/internal/domain"
)

func fipsToolDefinitions() []Tool {
	return []Tool{
		{Name: "bahia_fips_list_mesh_nodes", Description: "List FIPS mesh nodes from Bahia worker and DNS projection state", InputSchema: dnsObjectSchema(map[string]interface{}{
			"limit":  map[string]interface{}{"type": "integer"},
			"offset": map[string]interface{}{"type": "integer"},
		})},
		{Name: "bahia_fips_mesh_status", Description: "Summarize current FIPS mesh health and DNS projection status", InputSchema: dnsObjectSchema(map[string]interface{}{})},
	}
}

type fipsMeshNode struct {
	WorkerPubkey       string                         `json:"worker_pubkey,omitempty"`
	WorkerNpub         string                         `json:"worker_npub,omitempty"`
	WorkerName         string                         `json:"worker_name,omitempty"`
	OverlayAddress     string                         `json:"overlay_address,omitempty"`
	TransportEndpoints []domain.FIPSTransportEndpoint `json:"transport_endpoints,omitempty"`
	MeshHealth         *fipsMeshHealth                `json:"mesh_health,omitempty"`
	DNSHostname        string                         `json:"dns_hostname,omitempty"`
	EndpointName       string                         `json:"endpoint_name,omitempty"`
	Zone               string                         `json:"zone,omitempty"`
	Health             string                         `json:"health,omitempty"`
	ProjectionStatus   string                         `json:"projection_status,omitempty"`
	Source             string                         `json:"source,omitempty"`
	MaterializedAt     string                         `json:"materialized_at,omitempty"`
	Metadata           map[string]any                 `json:"metadata,omitempty"`
}

type fipsMeshHealth struct {
	RTTMillis         int64   `json:"rtt_millis"`
	Loss              float64 `json:"loss"`
	JitterMillis      int64   `json:"jitter_millis"`
	Goodput           uint64  `json:"goodput"`
	LastReport        string  `json:"last_report,omitempty"`
	ProjectionHealthy bool    `json:"projection_healthy"`
}

func (s *Server) handleFIPSListMeshNodes(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	nodes, err := s.listFIPSMeshNodes(ctx)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	page := pageFIPSMeshNodes(nodes, args)
	return jsonResult(map[string]any{"nodes": page, "total": len(nodes)})
}

func (s *Server) handleFIPSMeshStatus(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	nodes, err := s.listFIPSMeshNodes(ctx)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	statusCounts := map[string]int{}
	projectionCounts := map[string]int{}
	healthyProjected := 0
	for _, node := range nodes {
		statusCounts[node.Health]++
		projectionCounts[node.ProjectionStatus]++
		if node.Health == string(domain.HealthStatusHealthy) && node.ProjectionStatus == string(domain.DriftStatusInSync) && node.DNSHostname != "" {
			healthyProjected++
		}
	}
	return jsonResult(map[string]any{
		"total_nodes":             len(nodes),
		"healthy_projected_nodes": healthyProjected,
		"health_counts":           statusCounts,
		"projection_counts":       projectionCounts,
		"nodes":                   nodes,
	})
}

func (s *Server) listFIPSMeshNodes(ctx context.Context) ([]fipsMeshNode, error) {
	workersByPubkey, err := s.fipsWorkersByPubkey(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	nodes := []fipsMeshNode{}

	if s.dnsEndpoints != nil {
		endpoints, err := s.dnsEndpoints.ListDNSEndpoints(ctx)
		if err != nil {
			return nil, fmt.Errorf("list DNS endpoints for FIPS mesh MCP tools: %w", err)
		}
		for _, endpoint := range endpoints {
			if endpoint.Family != domain.DNSEndpointFamilyMesh {
				continue
			}
			worker, _ := workersByPubkey[strings.TrimSpace(endpoint.WorkerPubkey)]
			node := fipsMeshNodeFromEndpoint(endpoint, worker)
			nodes = append(nodes, node)
			if node.WorkerPubkey != "" {
				seen[node.WorkerPubkey] = struct{}{}
			}
		}
	}

	for pubkey, worker := range workersByPubkey {
		if _, ok := seen[pubkey]; ok {
			continue
		}
		if strings.TrimSpace(worker.FIPSOverlayAddr) == "" && len(worker.FIPSEndpoints) == 0 {
			continue
		}
		nodes = append(nodes, fipsMeshNodeFromWorker(worker))
	}

	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].DNSHostname != nodes[j].DNSHostname {
			return nodes[i].DNSHostname < nodes[j].DNSHostname
		}
		if nodes[i].WorkerPubkey != nodes[j].WorkerPubkey {
			return nodes[i].WorkerPubkey < nodes[j].WorkerPubkey
		}
		return nodes[i].EndpointName < nodes[j].EndpointName
	})
	return nodes, nil
}

func (s *Server) fipsWorkersByPubkey(ctx context.Context) (map[string]domain.Worker, error) {
	workersByPubkey := map[string]domain.Worker{}
	if s.workers == nil {
		return workersByPubkey, nil
	}
	workers, err := s.workers.List(ctx, "", 0)
	if err != nil {
		return nil, fmt.Errorf("list workers for FIPS mesh MCP tools: %w", err)
	}
	for _, worker := range workers {
		pubkey := strings.TrimSpace(worker.PubKey)
		if pubkey == "" {
			continue
		}
		workersByPubkey[pubkey] = worker
	}
	return workersByPubkey, nil
}

func fipsMeshNodeFromEndpoint(endpoint domain.DNSEndpoint, worker domain.Worker) fipsMeshNode {
	pubkey := strings.TrimSpace(endpoint.WorkerPubkey)
	if pubkey == "" {
		pubkey = strings.TrimSpace(worker.PubKey)
	}
	overlay := strings.TrimSpace(endpoint.Address)
	if overlay == "" {
		overlay = strings.TrimSpace(worker.FIPSOverlayAddr)
	}
	transportEndpoints := append([]domain.FIPSTransportEndpoint(nil), worker.FIPSEndpoints...)
	if len(transportEndpoints) == 0 {
		transportEndpoints = transportEndpointsFromMetadata(endpoint.Metadata)
	}
	return fipsMeshNode{
		WorkerPubkey:       pubkey,
		WorkerNpub:         npubFromPubkey(pubkey),
		WorkerName:         firstNonEmpty(strings.TrimSpace(worker.Name), strings.TrimSpace(endpoint.Name)),
		OverlayAddress:     overlay,
		TransportEndpoints: transportEndpoints,
		MeshHealth:         fipsMeshHealthFromDomain(worker.MeshHealth),
		DNSHostname:        strings.TrimSpace(endpoint.FQDN),
		EndpointName:       strings.TrimSpace(endpoint.Name),
		Zone:               strings.TrimSpace(endpoint.Zone),
		Health:             string(endpoint.Health),
		ProjectionStatus:   string(endpoint.DriftStatus),
		Source:             strings.TrimSpace(endpoint.Source),
		MaterializedAt:     formatOptionalTime(endpoint.MaterializedAt),
		Metadata:           endpoint.Metadata,
	}
}

func fipsMeshNodeFromWorker(worker domain.Worker) fipsMeshNode {
	pubkey := strings.TrimSpace(worker.PubKey)
	return fipsMeshNode{
		WorkerPubkey:       pubkey,
		WorkerNpub:         npubFromPubkey(pubkey),
		WorkerName:         strings.TrimSpace(worker.Name),
		OverlayAddress:     strings.TrimSpace(worker.FIPSOverlayAddr),
		TransportEndpoints: append([]domain.FIPSTransportEndpoint(nil), worker.FIPSEndpoints...),
		MeshHealth:         fipsMeshHealthFromDomain(worker.MeshHealth),
		Health:             string(worker.Status),
		ProjectionStatus:   "not_projected",
		Source:             "worker_fips_state",
	}
}

func fipsMeshHealthFromDomain(health *domain.MeshHealth) *fipsMeshHealth {
	if health == nil {
		return nil
	}
	return &fipsMeshHealth{
		RTTMillis:         health.RTT.Milliseconds(),
		Loss:              health.Loss,
		JitterMillis:      health.Jitter.Milliseconds(),
		Goodput:           health.Goodput,
		LastReport:        formatOptionalTime(health.LastReport),
		ProjectionHealthy: health.Loss <= 0.5 && health.RTT <= 5*time.Second,
	}
}

func transportEndpointsFromMetadata(metadata map[string]any) []domain.FIPSTransportEndpoint {
	if metadata == nil {
		return nil
	}
	for _, key := range []string{"fips_endpoints", "transport_endpoints"} {
		raw, ok := metadata[key]
		if !ok {
			continue
		}
		items, ok := raw.([]domain.FIPSTransportEndpoint)
		if ok {
			return append([]domain.FIPSTransportEndpoint(nil), items...)
		}
		list, ok := raw.([]any)
		if !ok {
			continue
		}
		out := make([]domain.FIPSTransportEndpoint, 0, len(list))
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			transport, _ := m["transport"].(string)
			address, _ := m["address"].(string)
			if strings.TrimSpace(transport) == "" && strings.TrimSpace(address) == "" {
				continue
			}
			out = append(out, domain.FIPSTransportEndpoint{Transport: strings.TrimSpace(transport), Address: strings.TrimSpace(address)})
		}
		return out
	}
	return nil
}

func pageFIPSMeshNodes(nodes []fipsMeshNode, args map[string]interface{}) []fipsMeshNode {
	limit := optionalIntArg(args, "limit", len(nodes))
	offset := optionalIntArg(args, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	if offset >= len(nodes) {
		return []fipsMeshNode{}
	}
	if limit <= 0 || offset+limit > len(nodes) {
		limit = len(nodes) - offset
	}
	return nodes[offset : offset+limit]
}

func npubFromPubkey(pubkey string) string {
	pubkey = strings.TrimSpace(pubkey)
	if pubkey == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(pubkey), "npub1") {
		return pubkey
	}
	if len(pubkey) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(pubkey); err != nil {
		return ""
	}
	npub, err := nip19.EncodePublicKey(pubkey)
	if err != nil {
		return ""
	}
	return npub
}

func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
