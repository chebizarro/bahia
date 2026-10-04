package controlplane

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/openagentsinc/bahia/internal/service"
)

type adoptionScanEventRequest struct {
	Targets []adoptionEventTarget `json:"targets"`
	Offset  int                   `json:"offset,omitempty"`
	Limit   int                   `json:"limit,omitempty"`
}

type adoptionImportEventRequest struct {
	Targets    []adoptionEventTarget    `json:"targets"`
	Selections []adoptionEventSelection `json:"selections,omitempty"`
	ImportAll  bool                     `json:"import_all,omitempty"`
	OrgID      string                   `json:"org_id,omitempty"`
}

type adoptionEventTarget struct {
	Name            string  `json:"name"`
	EndpointRef     string  `json:"endpoint_ref"`
	EnvironmentName string  `json:"environment_name,omitempty"`
	DockerHost      *string `json:"docker_host,omitempty"`
}

type adoptionEventSelection struct {
	TargetName          string `json:"target_name"`
	ContainerID         string `json:"container_id"`
	ServiceNameOverride string `json:"service_name_override,omitempty"`
}

func mapAdoptionEventTargets(targets []adoptionEventTarget) ([]service.AdoptionTarget, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("at least one target is required")
	}
	mapped := make([]service.AdoptionTarget, 0, len(targets))
	seen := map[string]struct{}{}
	for _, target := range targets {
		if target.DockerHost != nil {
			return nil, fmt.Errorf("target docker_host is forbidden for signer-first adoption; use endpoint_ref")
		}
		name := normalizeAdoptionEventName(target.Name)
		if name == "" {
			return nil, fmt.Errorf("target name is required")
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("target names must be unique after normalization")
		}
		seen[name] = struct{}{}
		endpointRef := strings.TrimSpace(target.EndpointRef)
		if endpointRef == "" {
			return nil, fmt.Errorf("target endpoint_ref is required for signer-first adoption")
		}
		environmentName := normalizeAdoptionEventName(target.EnvironmentName)
		if strings.TrimSpace(target.EnvironmentName) != "" && environmentName == "" {
			return nil, fmt.Errorf("target environment_name is invalid")
		}
		mapped = append(mapped, service.AdoptionTarget{Name: name, EndpointRef: endpointRef, EnvironmentName: environmentName})
	}
	return mapped, nil
}

func mapAdoptionEventSelections(selections []adoptionEventSelection) ([]service.AdoptionSelection, error) {
	mapped := make([]service.AdoptionSelection, 0, len(selections))
	seen := map[string]struct{}{}
	for _, selection := range selections {
		targetName := normalizeAdoptionEventName(selection.TargetName)
		containerID := strings.TrimSpace(selection.ContainerID)
		if targetName == "" {
			return nil, fmt.Errorf("selection target_name is required")
		}
		if containerID == "" {
			return nil, fmt.Errorf("selection container_id is required")
		}
		serviceNameOverride := normalizeAdoptionEventName(selection.ServiceNameOverride)
		if strings.TrimSpace(selection.ServiceNameOverride) != "" && serviceNameOverride == "" {
			return nil, fmt.Errorf("selection service_name_override is invalid")
		}
		key := targetName + "/" + containerID
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("selection entries must be unique")
		}
		seen[key] = struct{}{}
		mapped = append(mapped, service.AdoptionSelection{TargetName: targetName, ContainerID: containerID, ServiceNameOverride: serviceNameOverride})
	}
	return mapped, nil
}

var invalidAdoptionEventNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

func normalizeAdoptionEventName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = invalidAdoptionEventNameChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	return name
}
