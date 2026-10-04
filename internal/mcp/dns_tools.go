package mcp

import (
	"github.com/openagentsinc/bahia/internal/domain"
)

func dnsToolDefinitions() []Tool {
	return []Tool{
		{Name: "bahia_dns_list_endpoints", Description: "List current DNS endpoints from the DNS read model", InputSchema: objectSchema(map[string]interface{}{
			"limit":  map[string]interface{}{"type": "integer"},
			"offset": map[string]interface{}{"type": "integer"},
		})},
		{Name: "bahia_dns_list_drift", Description: "List recent DNS endpoint drift from the DNS read model", InputSchema: objectSchema(map[string]interface{}{
			"limit":  map[string]interface{}{"type": "integer"},
			"offset": map[string]interface{}{"type": "integer"},
		})},
		{Name: "bahia_assistant_dns_list_endpoints", Description: "Assistant-safe DNS endpoint read model query", InputSchema: objectSchema(map[string]interface{}{
			"limit":  integerProp,
			"offset": integerProp,
		})},
		{Name: "bahia_assistant_dns_list_drift", Description: "Assistant-safe DNS drift read model query", InputSchema: objectSchema(map[string]interface{}{
			"limit":  integerProp,
			"offset": integerProp,
		})},
	}
}

func pageDNSEndpointsForMCP(endpoints []domain.DNSEndpoint, args map[string]interface{}) []domain.DNSEndpoint {
	limit := optionalIntArg(args, "limit", len(endpoints))
	offset := optionalIntArg(args, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	if offset >= len(endpoints) {
		return []domain.DNSEndpoint{}
	}
	if limit <= 0 || offset+limit > len(endpoints) {
		limit = len(endpoints) - offset
	}
	return endpoints[offset : offset+limit]
}
