package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type bootstrapSeedKind int

const (
	bootstrapString bootstrapSeedKind = iota
	bootstrapBool
	bootstrapInt
	bootstrapStrings
)

type bootstrapSeed struct {
	path    []string
	envKeys []string
	kind    bootstrapSeedKind
}

var mutablePolicyBootstrapSeeds = []bootstrapSeed{
	{[]string{"nostr", "sidecar", "enabled"}, []string{"BAHIA_NOSTR__SIDECAR__ENABLED"}, bootstrapBool},
	{[]string{"nostr", "sidecar", "public_url"}, []string{"BAHIA_NOSTR__SIDECAR__PUBLIC_URL"}, bootstrapString},
	{[]string{"nostr", "sidecar", "backend_url"}, []string{"BAHIA_NOSTR__SIDECAR__BACKEND_URL"}, bootstrapString},
	{[]string{"nostr", "sidecar", "max_query_limit"}, []string{"BAHIA_NOSTR__SIDECAR__MAX_QUERY_LIMIT"}, bootstrapInt},
	{[]string{"nostr", "contextvm_relays"}, []string{"BAHIA_NOSTR__CONTEXTVM_RELAYS", "BAHIA_NOSTR_CONTEXTVM_RELAYS"}, bootstrapStrings},
	{[]string{"reconcile", "enabled"}, []string{"BAHIA_RECONCILE__ENABLED", "BAHIA_RECONCILE_ENABLED"}, bootstrapBool},
}

// seedMutablePolicy persists legacy environment seeds only for keys absent from
// the mounted YAML document. It returns environment names that must be ignored
// by the normal koanf environment provider because the file now owns them.
func seedMutablePolicy(configPath string) (map[string]struct{}, error) {
	protected := make(map[string]struct{})
	if strings.TrimSpace(configPath) == "" {
		return protected, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading config for mutable policy bootstrap: %w", err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parsing config for mutable policy bootstrap: %w", err)
	}
	root, err := yamlMappingRoot(&document)
	if err != nil {
		return nil, err
	}

	changed := false
	for _, seed := range mutablePolicyBootstrapSeeds {
		if yamlPathExists(root, seed.path) {
			protectSeedEnv(protected, seed)
			continue
		}
		raw, ok := firstSeedEnvironment(seed.envKeys)
		if !ok {
			continue
		}
		value, err := bootstrapYAMLValue(raw, seed.kind)
		if err != nil {
			return nil, fmt.Errorf("invalid bootstrap seed %s: %w", seed.envKeys[0], err)
		}
		setYAMLPath(root, seed.path, value)
		protectSeedEnv(protected, seed)
		changed = true
	}

	if changed {
		encoded, err := yaml.Marshal(&document)
		if err != nil {
			return nil, fmt.Errorf("encoding bootstrapped config: %w", err)
		}
		if err := writeConfigAtomic(configPath, encoded); err != nil {
			return nil, fmt.Errorf("persisting mutable policy bootstrap: %w", err)
		}
	}
	return protected, nil
}

func protectSeedEnv(protected map[string]struct{}, seed bootstrapSeed) {
	for _, key := range seed.envKeys {
		protected[key] = struct{}{}
	}
}

func firstSeedEnvironment(keys []string) (string, bool) {
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			return value, true
		}
	}
	return "", false
}

func yamlMappingRoot(document *yaml.Node) (*yaml.Node, error) {
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config bootstrap requires a YAML mapping document")
	}
	return document.Content[0], nil
}

func yamlPathExists(root *yaml.Node, path []string) bool {
	current := root
	for _, key := range path {
		next := yamlMappingValue(current, key)
		if next == nil {
			return false
		}
		current = next
	}
	return true
}

func yamlMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func setYAMLPath(root *yaml.Node, path []string, value *yaml.Node) {
	current := root
	for _, key := range path[:len(path)-1] {
		next := yamlMappingValue(current, key)
		if next == nil {
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			current.Content = append(current.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, next)
		}
		current = next
	}
	key := path[len(path)-1]
	current.Content = append(current.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func bootstrapYAMLValue(raw string, kind bootstrapSeedKind) (*yaml.Node, error) {
	raw = strings.TrimSpace(raw)
	switch kind {
	case bootstrapBool:
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, err
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(value)}, nil
	case bootstrapInt:
		value, err := strconv.Atoi(raw)
		if err != nil {
			return nil, err
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(value)}, nil
	case bootstrapStrings:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range strings.Split(raw, ",") {
			if item = strings.TrimSpace(item); item != "" {
				node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: item})
			}
		}
		return node, nil
	default:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: raw}, nil
	}
}

func writeConfigAtomic(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".bahia-config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
