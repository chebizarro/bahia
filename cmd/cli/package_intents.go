package main

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func publishPackageMutation(cmd *cobra.Command, op string, mutation any) error {
	content, coordinate, err := packageMutationContent(op, mutation)
	if err != nil {
		return err
	}
	intentID, err := publishMutationIntent(cmd, "package", op, coordinate, "", "", content)
	if err != nil {
		return err
	}
	return outputSingle(map[string]string{"intent_id": intentID, "coordinate": coordinate})
}

func packageMutationContent(op string, mutation any) (map[string]interface{}, string, error) {
	content, err := jsonObject(mutation)
	if err != nil {
		return nil, "", err
	}
	var coordinate string
	switch op {
	case "repository-apply":
		raw, _ := content["repository_id"].(string)
		id, err := uuid.Parse(raw)
		if err == nil && id != uuid.Nil {
			content["id"] = id.String()
			coordinate = id.String()
		} else {
			name, _ := content["name"].(string)
			coordinate = strings.TrimSpace(name)
		}
		delete(content, "repository_id")
	case "repository-delete", "drift-detect":
		coordinate = packageRepoIdentity(content)
	case "publish", "yank":
		coordinate = packageArtifactCoordinate(content, "repository_id", "repository_name")
	case "promote":
		coordinate = packageArtifactCoordinate(content, "target_repository_id", "target_repository_name")
	default:
		return nil, "", fmt.Errorf("unsupported package mutation %q", op)
	}
	if coordinate == "" {
		return nil, "", fmt.Errorf("package %s requires a repository identity", op)
	}
	for _, field := range []string{"repository_id", "source_repository_id", "target_repository_id", "approval_id"} {
		if content[field] == uuid.Nil.String() {
			delete(content, field)
		}
	}
	return content, coordinate, nil
}

func packageRepoIdentity(content map[string]interface{}) string {
	if id, _ := content["repository_id"].(string); id != "" && id != uuid.Nil.String() {
		return id
	}
	if name, _ := content["repository_name"].(string); strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return ""
}

func packageArtifactCoordinate(content map[string]interface{}, idField, nameField string) string {
	id, _ := content[idField].(string)
	if id == uuid.Nil.String() || id == "" {
		id, _ = content[nameField].(string)
	}
	if id == "" {
		return ""
	}
	field := func(key string) string { value, _ := content[key].(string); return value }
	return "package:" + strings.Join([]string{id, field("namespace"), field("package_name"), field("version"), field("filename")}, ":")
}
