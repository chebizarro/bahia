package app

import "strings"

func routeCanaryOverrideHealth(unmatched []string, scanned bool) HealthCheck {
	check := HealthCheck{Name: "route_canary_overrides", Status: HealthStatusPass, Message: "all route canary overrides match managed routes"}
	if !scanned {
		check.Status = HealthStatusWarn
		check.Message = "route canary overrides await managed route enumeration"
	} else if len(unmatched) > 0 {
		check.Status = HealthStatusWarn
		check.Message = "route canary overrides match no managed route"
		check.Details = map[string]string{"hostnames": strings.Join(unmatched, ",")}
	}
	return check
}
