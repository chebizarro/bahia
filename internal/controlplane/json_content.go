package controlplane

import "encoding/json"

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
