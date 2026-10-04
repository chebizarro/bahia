package mcp

import (
	"context"
	"fmt"
	"sort"

	"github.com/openagentsinc/bahia/internal/domain"
)

func (s *Server) handleDNSListEndpoints(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if legacyFor(s).DNSEndpoints == nil {
		return errorResult("DNS endpoint lister is not configured"), nil
	}
	endpoints, err := legacyFor(s).DNSEndpoints.ListDNSEndpoints(ctx)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list DNS endpoints: %v", err)), nil
	}
	page := pageDNSEndpointsForMCP(endpoints, args)
	return jsonResult(map[string]any{"endpoints": page, "total": len(endpoints)})
}

func (s *Server) handleDNSListDrift(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if legacyFor(s).DNSEndpoints == nil {
		return errorResult("DNS endpoint lister is not configured"), nil
	}
	endpoints, err := legacyFor(s).DNSEndpoints.ListDNSEndpoints(ctx)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list DNS drift: %v", err)), nil
	}
	drifted := make([]domain.DNSEndpoint, 0)
	for _, endpoint := range endpoints {
		if endpoint.DriftStatus != "" && endpoint.DriftStatus != domain.DriftStatusInSync {
			drifted = append(drifted, endpoint)
		}
	}
	sort.SliceStable(drifted, func(i, j int) bool {
		return drifted[i].MaterializedAt.After(drifted[j].MaterializedAt)
	})
	page := pageDNSEndpointsForMCP(drifted, args)
	return jsonResult(map[string]any{"drift": page, "total": len(drifted)})
}
