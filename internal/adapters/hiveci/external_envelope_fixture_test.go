package hiveci

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

func TestExternalHiveCIEnvelopesMatchCanonicalReaderFilters(t *testing.T) {
	data, err := os.ReadFile("../../../web/tests/fixtures/external-go-envelopes.json")
	require.NoError(t, err)
	var fixtures map[string]struct {
		Kind int        `json:"kind"`
		Tags nostr.Tags `json:"tags"`
	}
	require.NoError(t, json.Unmarshal(data, &fixtures))
	cases := []struct {
		name       string
		kind       int
		tag, value string
		filter     nostr.Filter
	}{
		{"hiveci_run", kinds.HiveCIWorkflowRun, "a", "30617:owner:astillero", workflowRunsForRepoFilter("30617:owner:astillero")},
		{"hiveci_result", kinds.HiveCIWorkflowResult, "e", strings.Repeat("c", 64), workflowResultsForRunFilter(strings.Repeat("c", 64))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := fixtures[tc.name]
			require.Equal(t, tc.kind, fixture.Kind)
			require.Equal(t, tc.value, func() string {
				for _, tag := range fixture.Tags {
					if tag[0] == tc.tag {
						return tag[1]
					}
				}
				return ""
			}())
			event := nostr.Event{Kind: nostr.Kind(fixture.Kind), Tags: fixture.Tags}
			require.True(t, tc.filter.Matches(event))
			if tc.name == "hiveci_run" {
				_, err := parseWorkflowRunEvent(&event)
				require.NoError(t, err)
			} else {
				_, _, err := parseWorkflowResultEvent(&event)
				require.NoError(t, err)
			}
			tc.filter.Tags[tc.tag] = []string{"wrong"}
			require.False(t, tc.filter.Matches(event))
		})
	}
}
