package mcp

// AssistantAsyncToolRequestMethods lists ContextVM methods whose assistant
// dispatch reconciliation is uncertain. Every async tool uses durable intent
// processor evidence, so the list is empty.
func AssistantAsyncToolRequestMethods() map[string][]string {
	return map[string][]string{}
}
