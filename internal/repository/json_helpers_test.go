package repository

import (
	"testing"
)

func TestMarshalJSON_ValidMap(t *testing.T) {
	m := map[string]any{"key": "value", "num": 42}
	b, err := marshalJSON(m, "test")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("expected non-empty bytes")
	}
}

func TestMarshalJSON_Nil(t *testing.T) {
	b, err := marshalJSON(nil, "test")
	if err != nil {
		t.Fatalf("expected no error for nil, got: %v", err)
	}
	if string(b) != "null" {
		t.Errorf("expected \"null\", got %q", string(b))
	}
}

func TestMarshalJSON_Unmarshallable(t *testing.T) {
	// Channels cannot be marshaled to JSON.
	ch := make(chan int)
	_, err := marshalJSON(ch, "bad_field")
	if err == nil {
		t.Fatal("expected error for channel type")
	}
	// Error should include the field name.
	if !containsStr(err.Error(), "bad_field") {
		t.Errorf("error should mention field name, got: %v", err)
	}
}

func TestUnmarshalJSON_ValidJSON(t *testing.T) {
	data := []byte(`{"key":"value"}`)
	var m map[string]any
	err := unmarshalJSON(data, &m, "test")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if m["key"] != "value" {
		t.Errorf("expected key=value, got %v", m["key"])
	}
}

func TestUnmarshalJSON_NilData(t *testing.T) {
	var m map[string]any
	err := unmarshalJSON(nil, &m, "test")
	if err != nil {
		t.Fatalf("expected no error for nil data, got: %v", err)
	}
	if m != nil {
		t.Errorf("expected nil map, got %v", m)
	}
}

func TestUnmarshalJSON_EmptyData(t *testing.T) {
	var m map[string]any
	err := unmarshalJSON([]byte{}, &m, "test")
	if err != nil {
		t.Fatalf("expected no error for empty data, got: %v", err)
	}
}

func TestUnmarshalJSON_InvalidJSON(t *testing.T) {
	data := []byte(`{not valid json}`)
	var m map[string]any
	err := unmarshalJSON(data, &m, "bad_field")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !containsStr(err.Error(), "bad_field") {
		t.Errorf("error should mention field name, got: %v", err)
	}
}

func TestUnmarshalJSON_NullLiteral(t *testing.T) {
	// PostgreSQL may store JSON null.
	data := []byte("null")
	var m map[string]any
	err := unmarshalJSON(data, &m, "test")
	if err != nil {
		t.Fatalf("expected no error for JSON null, got: %v", err)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
