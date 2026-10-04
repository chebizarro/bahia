package mcp

func mlToolDefinitions() []Tool {
	return []Tool{
		{Name: "bahia_ml_import_model", Description: "Import an ML model through the in-process intent pipeline", InputSchema: objectSchema(map[string]interface{}{
			"idempotency_key": map[string]interface{}{"type": "string"},
			"org_id":          map[string]interface{}{"type": "string", "description": "Optional fleet organization UUID"},
			"model":           map[string]interface{}{"type": "string", "description": "Model coordinate such as model:<slug>"},
			"model_version":   map[string]interface{}{"type": "string", "description": "Model version coordinate such as model-version:<slug>:<version>"},
			"source":          map[string]interface{}{"type": "string", "description": "Source family, e.g. huggingface"},
			"uri":             map[string]interface{}{"type": "string", "description": "Source URI"},
			"source_uri":      map[string]interface{}{"type": "string", "description": "Source URI (preferred alias)"},
			"revision":        map[string]interface{}{"type": "string"},
			"task":            map[string]interface{}{"type": "string"},
		})},
		{Name: "bahia_ml_run_recipe", Description: "Run a canonical ML recipe through the in-process intent pipeline", InputSchema: objectSchema(map[string]interface{}{
			"idempotency_key": map[string]interface{}{"type": "string"},
			"org_id":          map[string]interface{}{"type": "string", "description": "Optional fleet organization UUID"},
			"recipe":          map[string]interface{}{"type": "string", "description": "Recipe coordinate such as recipe:<name>:<version>"},
			"recipe_id":       map[string]interface{}{"type": "string", "description": "Canonical recipe UUID (alternative to recipe)"},
			"inputs":          map[string]interface{}{"type": "object"},
			"parameters":      map[string]interface{}{"type": "object"},
		})},
		{Name: "bahia_ml_deploy", Description: "Deploy an ML inference endpoint through the in-process intent pipeline", InputSchema: objectSchema(map[string]interface{}{
			"idempotency_key":    map[string]interface{}{"type": "string"},
			"endpoint":           map[string]interface{}{"type": "string", "description": "Endpoint coordinate endpoint:<name>:<environment>"},
			"endpoint_id":        map[string]interface{}{"type": "string"},
			"model_version":      map[string]interface{}{"type": "string", "description": "Model version coordinate model-version:<slug>:<version>"},
			"model_version_id":   map[string]interface{}{"type": "string"},
			"runtime_preference": map[string]interface{}{"type": "string"},
			"runtime":            map[string]interface{}{"type": "string"},
		})},
		{Name: "bahia_ml_rollback", Description: "Roll back an ML inference endpoint through the in-process intent pipeline", InputSchema: objectSchema(map[string]interface{}{
			"idempotency_key": map[string]interface{}{"type": "string"},
			"endpoint":        map[string]interface{}{"type": "string"},
			"endpoint_id":     map[string]interface{}{"type": "string"},
			"requested_by":    map[string]interface{}{"type": "string"},
		})},
		{Name: "bahia_ml_list_state", Description: "List generic ML inference endpoint state read models", InputSchema: objectSchema(map[string]interface{}{})},
		{Name: "bahia_ml_get_state", Description: "Get generic ML inference state for an endpoint/environment", InputSchema: objectSchema(map[string]interface{}{
			"endpoint_id":    map[string]interface{}{"type": "string"},
			"environment_id": map[string]interface{}{"type": "string"},
		}, "endpoint_id", "environment_id")},
		{Name: "bahia_ml_get_provenance", Description: "Get ML artifact provenance edges for an artifact ref", InputSchema: objectSchema(map[string]interface{}{
			"artifact_id": map[string]interface{}{"type": "string"},
		}, "artifact_id")},
	}
}
