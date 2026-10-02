package nostr

import (
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func TestCASControlStateProjectionFamiliesFilterRoundTrip(t *testing.T) {
	families := []struct {
		name string
		kind int
	}{
		{"service", KindServiceRegistry},
		{"environment", KindEnvironmentRegistry},
		{"llm", KindLLMRouteRegistry},
		{"artifact", KindArtifactRegistry},
		{"deployment", KindDeploymentIntentRegistry},
		{"build", KindBuildRegistry},
		{"policy", KindPolicyRegistry},
		{"package", KindPackageRepositoryRegistry},
		{"worker", KindWorkerState},
		{"dns", KindDNSZoneState},
		{"ml", KindMLModelRegistry},
		{"backup", KindBackupDefinitionRegistry},
	}

	for _, family := range families {
		t.Run(family.name, func(t *testing.T) {
			domainName, _ := canonicalStateDomain(family.kind)
			const dTag = "projection-id"
			event := gonostr.Event{
				Kind: gonostr.Kind(kinds.CASControlState),
				Tags: gonostr.Tags{
					{kinds.CASControlStateTagD, dTag},
					{kinds.CASControlStateTagDomain, domainName},
					{kinds.CASControlStateTagSchema, "bahia.cp-state.v1"},
				},
			}
			filter := gonostr.Filter{
				Kinds: []gonostr.Kind{gonostr.Kind(kinds.CASControlState)},
				Tags: gonostr.TagMap{
					kinds.CASControlStateTagD:      []string{dTag},
					kinds.CASControlStateTagDomain: []string{domainName},
					kinds.CASControlStateTagSchema: []string{"bahia.cp-state.v1"},
				},
			}
			if !filter.Matches(event) {
				t.Fatalf("%s projection did not match its canonical filter: tags=%v", family.name, event.Tags)
			}

			event.Tags[2][1] = "bahia.other.v1"
			if filter.Matches(event) {
				t.Fatalf("%s filter matched a projection with the wrong schema", family.name)
			}
		})
	}
}
