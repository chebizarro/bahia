package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadSoulFactoryTimeoutConfig(t *testing.T, yaml string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	return Load(path)
}

// soul_factory.runtime_result_timeout and soul_factory.reply_timeout default
// to the SoulFactory wait bounds, load from YAML or env, treat zero as the
// default and reject negative values.
func TestSoulFactoryTimeoutKeys(t *testing.T) {
	defaults := Defaults()
	if defaults.SoulFactory.RuntimeResultTimeout != 5*time.Minute || defaults.SoulFactory.ReplyTimeout != 15*time.Minute {
		t.Fatalf("defaults = runtime_result_timeout %s reply_timeout %s, want 5m and 15m",
			defaults.SoulFactory.RuntimeResultTimeout, defaults.SoulFactory.ReplyTimeout)
	}

	for _, key := range []string{"runtime_result_timeout", "reply_timeout"} {
		_, err := loadSoulFactoryTimeoutConfig(t, "soul_factory:\n  "+key+": -1s\n")
		if err == nil || !strings.Contains(err.Error(), "soul_factory."+key) {
			t.Fatalf("negative %s error = %v, want a validation error naming it", key, err)
		}
	}

	t.Setenv("BAHIA_SOUL_FACTORY_REPLY_TIMEOUT", "20m")
	cfg, err := loadSoulFactoryTimeoutConfig(t, "soul_factory:\n  runtime_result_timeout: 7m\n")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SoulFactory.RuntimeResultTimeout != 7*time.Minute || cfg.SoulFactory.ReplyTimeout != 20*time.Minute {
		t.Fatalf("loaded = runtime_result_timeout %s reply_timeout %s, want 7m (yaml) and 20m (env)",
			cfg.SoulFactory.RuntimeResultTimeout, cfg.SoulFactory.ReplyTimeout)
	}

	t.Setenv("BAHIA_SOUL_FACTORY_REPLY_TIMEOUT", "0s")
	cfg, err = loadSoulFactoryTimeoutConfig(t, "soul_factory:\n  runtime_result_timeout: 0s\n")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SoulFactory.RuntimeResultTimeout != 5*time.Minute || cfg.SoulFactory.ReplyTimeout != 15*time.Minute {
		t.Fatalf("zero values = runtime_result_timeout %s reply_timeout %s, want the defaults",
			cfg.SoulFactory.RuntimeResultTimeout, cfg.SoulFactory.ReplyTimeout)
	}

}
