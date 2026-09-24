package main

import (
	"encoding/json"
	"testing"

	"github.com/openagentsinc/bahia/internal/kinds"
)

func TestSeedCorpusSoulFactoryInterop(t *testing.T) {
	events, err := seedCorpus("ws://127.0.0.1:48639")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []int{kinds.SoulFactoryTemplate, kinds.SoulFactoryAgentSoul, kinds.SoulFactoryDraft, kinds.SoulFactoryRuntimeCapability} {
		count := 0
		for _, event := range events {
			if int(event.Kind) == kind {
				count++
			}
		}
		if count != 1 {
			t.Errorf("expected one SoulFactory event of kind %d, got %d", kind, count)
		}
	}
}

func TestSeedCorpusAdvertisesBackendMembershipProbe(t *testing.T) {
	events, err := seedCorpus("ws://127.0.0.1:48639")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if int(event.Kind) != kinds.ContextVMServerAnnouncement {
			continue
		}
		var info struct {
			Features map[string]bool `json:"features"`
		}
		if err := json.Unmarshal([]byte(event.Content), &info); err != nil {
			t.Fatal(err)
		}
		if !info.Features["direct_nostr_http_auth"] {
			t.Fatal("browser fixture must establish backend membership before protected routes render")
		}
		return
	}
	t.Fatal("missing system discovery event")
}
