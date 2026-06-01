package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// DocsBasePath is the base path where user-guide docs live.
// This can be overridden via configuration.
var DocsBasePath = "docs/user-guide"

// listDocsResources returns MCP resources for user-guide documentation.
func (s *Server) listDocsResources(ctx context.Context) ([]Resource, error) {
	basePath := DocsBasePath

	// Check if docs directory exists
	if _, err := os.Stat(basePath); os.IsNotExist(err) {
		return nil, nil
	}

	var resources []Resource

	err := filepath.WalkDir(basePath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}

		// Convert path to resource name
		relPath, _ := filepath.Rel(basePath, path)
		name := strings.TrimSuffix(relPath, ".md")
		name = strings.ReplaceAll(name, string(filepath.Separator), "-")

		// Create URI
		uri := "bahia://docs/" + name

		// Generate description from path
		description := docsResourceDescription(name)

		resources = append(resources, Resource{
			URI:         uri,
			Name:        "docs:" + name,
			Description: description,
			MIMEType:    "text/markdown",
			Metadata: map[string]any{
				"path":     path,
				"category": docsResourceCategory(relPath),
			},
		})

		return nil
	})

	if err != nil {
		return nil, err
	}

	return resources, nil
}

// ReadDocsResource reads a documentation resource by name.
// This is exposed as an MCP tool for agents.
func (s *Server) ReadDocsResource(ctx context.Context, name string) (string, error) {
	basePath := DocsBasePath

	// Try direct path first (e.g., "index" -> "index.md")
	fullPath := filepath.Join(basePath, name+".md")
	content, err := os.ReadFile(fullPath)
	if err == nil {
		return string(content), nil
	}

	// Try features subdirectory for feature docs (e.g., "features-services" -> "features/services.md")
	if strings.HasPrefix(name, "features-") {
		featureName := strings.TrimPrefix(name, "features-")
		fullPath = filepath.Join(basePath, "features", featureName+".md")
		content, err = os.ReadFile(fullPath)
		if err == nil {
			return string(content), nil
		}
	}

	// Try converting hyphens to path separators as fallback
	path := strings.ReplaceAll(name, "-", string(filepath.Separator)) + ".md"
	fullPath = filepath.Join(basePath, path)
	content, err = os.ReadFile(fullPath)
	if err == nil {
		return string(content), nil
	}

	return "", err
}

func docsResourceDescription(name string) string {
	// Remove features- prefix for lookup
	base := name
	if strings.HasPrefix(name, "features-") {
		base = strings.TrimPrefix(name, "features-")
	}

	descriptions := map[string]string{
		"index":             "Bahia user documentation index and overview",
		"getting-started":   "Getting started with Bahia - installation and first deployment",
		"core-concepts":     "Core concepts - services, environments, artifacts, Nostr model",
		"nostr-integration": "How Nostr powers the Bahia control plane",
		"mcp-tools":         "MCP tools reference for AI agent integration",
		"cli-reference":     "CLI command reference",
		"troubleshooting":   "Common issues and solutions",
		"services":          "Service management - create, deploy, and manage applications",
		"environments":      "Environment management - staging, production targets",
		"deployments":       "Deployment workflow - intents, approvals, runs",
		"artifacts":         "Artifact management - container images and SBOM",
		"notifications":     "Notification channels - webhooks, Slack, email, Nostr",
		"organizations":     "Organization management - teams and access control",
		"llm-routes":        "LLM route management and deployment",
		"ml-models":         "ML model registry, recipes, and inference",
		"souls":             "Soul Factory - AI agent provisioning",
		"workers":           "Loom workers - deployment execution",
		"backup":            "Backup definitions, policies, and recovery",
		"dns":               "DNS zone and endpoint management",
		"packages":          "Package repository management",
		"policies":          "Deployment policies and SBOM requirements",
		"payments":          "Cost estimation and payment history",
	}

	if desc, ok := descriptions[base]; ok {
		return desc
	}
	return "Bahia documentation: " + name
}

func docsResourceCategory(path string) string {
	if strings.HasPrefix(path, "features") {
		return "feature"
	}
	return "guide"
}

// handleDocsRead handles the bahia_docs_read tool.
func (s *Server) handleDocsRead(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	topic, ok := args["topic"].(string)
	if !ok || topic == "" {
		return errorResult("topic is required"), nil
	}

	content, err := s.ReadDocsResource(ctx, topic)
	if err != nil {
		return errorResult("documentation topic not found: " + topic), nil
	}

	return &ToolResult{
		Content: []Content{
			{Type: "text", Text: content},
		},
	}, nil
}

// handleDocsList handles the bahia_docs_list tool.
func (s *Server) handleDocsList(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	topics := DocsResourceCatalog()

	result := map[string]interface{}{
		"topics": topics,
		"count":  len(topics),
		"hint":   "Use bahia_docs_read with a topic name to read the documentation. Start with 'index' for an overview.",
	}

	data, err := json.Marshal(result)
	if err != nil {
		return errorResult("failed to marshal topics"), nil
	}

	return &ToolResult{
		Content: []Content{
			{Type: "text", Text: string(data)},
		},
	}, nil
}

// docsToolDefinitions returns MCP tool definitions for documentation access.
func docsToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "bahia_docs_read",
			Description: "Read Bahia user documentation. Use this to learn about Bahia features, MCP tools, CLI commands, Nostr integration, and troubleshooting. Available topics: index, getting-started, core-concepts, nostr-integration, mcp-tools, cli-reference, troubleshooting, features-services, features-environments, features-deployments, features-artifacts, features-notifications, features-organizations, features-llm-routes, features-ml-models, features-souls, features-workers, features-backup, features-dns, features-packages, features-policies, features-payments",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"topic": map[string]interface{}{
						"type":        "string",
						"description": "Documentation topic to read. Use 'index' for the overview, or specific topics like 'getting-started', 'features-services', 'mcp-tools', etc.",
					},
				},
				"required": []string{"topic"},
			},
		},
		{
			Name:        "bahia_docs_list",
			Description: "List available Bahia documentation topics",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}
}

// DocsResourceCatalog returns documentation topics for agent discovery.
func DocsResourceCatalog() []string {
	return []string{
		"index",
		"getting-started",
		"core-concepts",
		"nostr-integration",
		"mcp-tools",
		"cli-reference",
		"troubleshooting",
		"features-services",
		"features-environments",
		"features-deployments",
		"features-artifacts",
		"features-notifications",
		"features-organizations",
		"features-llm-routes",
		"features-ml-models",
		"features-souls",
		"features-workers",
		"features-backup",
		"features-dns",
		"features-packages",
		"features-policies",
		"features-payments",
	}
}
