package app

import (
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
)

func TestSoulFactoryAgentMemoryConfigWiresTaskIDFile(t *testing.T) {
	got := soulFactoryAgentMemoryConfig(config.SoulFactoryConfig{AgentMemoryTaskIDFile: "/var/lib/bahia/agent-memory/task-ids.json"})
	if got.TaskIDFile != "/var/lib/bahia/agent-memory/task-ids.json" {
		t.Fatalf("TaskIDFile = %q", got.TaskIDFile)
	}
}
