package mcp

import (
	"context"
	"encoding/json"
	"errors"

	userdocs "github.com/openagentsinc/bahia/internal/docs"
)

// DocsBasePath is the base path where user-guide docs live.
// This can be overridden via configuration.
var DocsBasePath = userdocs.DefaultBasePath

// listDocsResources returns MCP resources for user-guide documentation.
func (s *Server) listDocsResources(ctx context.Context) ([]Resource, error) {
	catalog, err := docsService().Catalog(ctx)
	if err != nil {
		return nil, err
	}

	resources := make([]Resource, 0, len(catalog))
	for _, item := range catalog {
		resources = append(resources, Resource{
			URI:         "bahia://docs/" + item.Topic,
			Name:        "docs:" + item.Topic,
			Description: item.Title,
			MIMEType:    "text/markdown",
			Metadata: map[string]any{
				"path":     item.SourcePath,
				"category": item.Category,
				"href":     item.Href,
			},
		})
	}
	return resources, nil
}

// ReadDocsResource reads a documentation resource by name.
// This is exposed as an MCP tool for agents.
func (s *Server) ReadDocsResource(ctx context.Context, name string) (string, error) {
	doc, err := docsService().Read(ctx, name)
	if err != nil {
		return "", err
	}
	return doc.Markdown, nil
}

// handleDocsRead handles the bahia_docs_read tool.
func (s *Server) handleDocsRead(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	topic, ok := args["topic"].(string)
	if !ok || topic == "" {
		return errorResult("topic is required"), nil
	}

	content, err := s.ReadDocsResource(ctx, topic)
	if err != nil {
		if errors.Is(err, userdocs.ErrInvalidTopic) {
			return errorResult("invalid documentation topic: " + topic), nil
		}
		if errors.Is(err, userdocs.ErrNotFound) {
			return errorResult("documentation topic not found: " + topic), nil
		}
		return errorResult("failed to read documentation topic: " + topic), nil
	}

	return &ToolResult{
		Content: []Content{
			{Type: "text", Text: content},
		},
	}, nil
}

// handleDocsList handles the bahia_docs_list tool.
func (s *Server) handleDocsList(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	catalog, err := docsService().Catalog(ctx)
	if err != nil {
		return errorResult("failed to list documentation topics"), nil
	}

	topics := make([]string, 0, len(catalog))
	for _, item := range catalog {
		topics = append(topics, item.Topic)
	}

	result := map[string]interface{}{
		"topics":  topics,
		"catalog": catalog,
		"count":   len(topics),
		"hint":    "Use bahia_docs_list to discover topics, then bahia_docs_read with a topic name. Start with 'index' for an overview.",
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
			Description: "Read Bahia user documentation by topic. Use bahia_docs_list first to discover the current scanned catalog instead of relying on a static topic list.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"topic": map[string]interface{}{
						"type":        "string",
						"description": "Documentation topic to read. Use bahia_docs_list to discover current topics; 'index' is the overview.",
					},
				},
				"required": []string{"topic"},
			},
		},
		{
			Name:        "bahia_docs_list",
			Description: "List available Bahia documentation topics from the central docs catalog.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}
}

// DocsResourceCatalog returns documentation topics for agent discovery.
func DocsResourceCatalog() []string {
	catalog, err := docsService().Catalog(context.Background())
	if err != nil {
		return nil
	}
	topics := make([]string, 0, len(catalog))
	for _, item := range catalog {
		topics = append(topics, item.Topic)
	}
	return topics
}

func docsService() userdocs.Service {
	return userdocs.New(DocsBasePath)
}
