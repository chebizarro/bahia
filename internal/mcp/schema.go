package mcp

func objectSchema(props map[string]interface{}, required ...string) map[string]interface{} {
	schema := map[string]interface{}{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

var (
	stringProp  = map[string]interface{}{"type": "string"}
	integerProp = map[string]interface{}{"type": "integer"}
	objectProp  = map[string]interface{}{"type": "object"}
	boolProp    = map[string]interface{}{"type": "boolean"}
)