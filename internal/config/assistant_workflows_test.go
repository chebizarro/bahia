package config

import (
	"reflect"
	"testing"
)

func TestAssistantAvailableWorkflows(t *testing.T) {
	cases := []struct {
		cfg  AssistantConfig
		want []string
	}{
		{cfg: AssistantConfig{}, want: []string{}},
		{cfg: AssistantConfig{LLMModel: "planner"}, want: []string{}},
		{cfg: AssistantConfig{Enabled: true, LLMModel: "planner"}, want: []string{AssistantWorkflowBatch, AssistantWorkflowIterative}},
		{cfg: AssistantConfig{Enabled: true, LLMModel: "  "}, want: []string{AssistantWorkflowIterative}},
	}
	for _, tc := range cases {
		if got := tc.cfg.AvailableWorkflows(); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("AvailableWorkflows(%+v) = %#v, want %#v", tc.cfg, got, tc.want)
		}
	}
}
