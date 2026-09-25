package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/yaml.v3"
)

// Every leaf needs a reviewed classification, not just fields whose names
// happen to match a credential-name heuristic.
func TestConfigSchemaRequiresRedactionClassification(t *testing.T) {
	var visit func(reflect.Type)
	seen := map[reflect.Type]bool{}
	visit = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for i := range typ.NumField() {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			leaf := field.Type
			for leaf.Kind() == reflect.Pointer || leaf.Kind() == reflect.Slice || leaf.Kind() == reflect.Array || leaf.Kind() == reflect.Map {
				leaf = leaf.Elem()
			}
			if leaf.Kind() == reflect.Struct {
				if field.Tag.Get("secret") != "" {
					t.Errorf("%s.%s: classify leaves, not entire config subtrees", typ.Name(), field.Name)
				}
				visit(leaf)
				continue
			}
			switch field.Tag.Get("secret") {
			case "true", "false", "url", "env_values":
			default:
				t.Errorf("%s.%s: missing or invalid secret classification", typ.Name(), field.Name)
			}
		}
	}
	visit(reflect.TypeFor[Config]())
}

// Discover all config subtrees and fill every protected field automatically.
// Adding a tagged secret to a formerly public subtree must also protect direct
// rendering of that subtree, including value receivers and pointer receivers.
func TestEveryProtectedConfigSubtreeRedactsAllRenderers(t *testing.T) {
	value := reflect.New(reflect.TypeFor[Config]()).Elem()
	var visit func(reflect.Value, bool) bool
	visit = func(value reflect.Value, protected bool) bool {
		switch value.Kind() {
		case reflect.Pointer:
			value.Set(reflect.New(value.Type().Elem()))
			return visit(value.Elem(), protected)
		case reflect.Slice:
			value.Set(reflect.MakeSlice(value.Type(), 1, 1))
			return visit(value.Index(0), protected)
		case reflect.Array:
			return visit(value.Index(0), protected)
		case reflect.Map:
			entry := reflect.New(value.Type().Elem()).Elem()
			found := visit(entry, protected)
			value.Set(reflect.MakeMap(value.Type()))
			key := reflect.New(value.Type().Key()).Elem()
			key.SetString("diagnostic-key")
			value.SetMapIndex(key, entry)
			return found
		case reflect.Interface:
			if protected {
				value.Set(reflect.ValueOf(configSecretSentinel))
			}
		case reflect.String:
			if protected {
				value.SetString(configSecretSentinel)
			}
		case reflect.Struct:
			found := false
			for i := range value.NumField() {
				field := value.Type().Field(i)
				if !field.IsExported() {
					continue
				}
				policy := field.Tag.Get("secret")
				if visit(value.Field(i), protected || (policy != "" && policy != "false")) {
					found = true
				}
			}
			if found {
				t.Run(value.Type().Name(), func(t *testing.T) {
					assertConfigRenderersRedact(t, value.Interface())
					assertConfigRenderersRedact(t, value.Addr().Interface())
				})
			}
			return found
		}
		return protected
	}
	if !visit(value, false) {
		t.Fatal("no protected config fields discovered")
	}
}

func assertConfigRenderersRedact(t *testing.T, value any) {
	t.Helper()
	encodedJSON, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	encodedYAML, err := yaml.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	outputs := []string{string(encodedJSON), string(encodedYAML)}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		outputs = append(outputs, fmt.Sprintf(format, value))
	}
	var logs bytes.Buffer
	logger := zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&logs), zap.DebugLevel))
	logger.Info("config", zap.Any("value", value), zap.Reflect("reflected", value))
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("config", "value", value)
	slog.New(slog.NewTextHandler(&logs, nil)).Info("config", "value", value)
	outputs = append(outputs, logs.String())
	for _, output := range outputs {
		assertSecretAbsent(t, "config subtree", output, configSecretSentinel)
		if strings.Contains(output, fmt.Sprintf("%x", configSecretSentinel)) {
			t.Fatal("config subtree leaked hex-formatted secret")
		}
	}
}

func TestUnclassifiedFieldsFailClosed(t *testing.T) {
	// Deliberately unknown names: no password/token name heuristic may be needed.
	type futureConfig struct {
		NewField string
		Payload  []byte
		Typo     string `secret:"ture"`
		Public   string `secret:"false"`
	}
	value := futureConfig{NewField: configSecretSentinel, Payload: []byte(configSecretSentinel), Typo: configSecretSentinel, Public: "useful"}
	encoded, err := marshalRedactedJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	assertSecretAbsent(t, "new field", string(encoded), configSecretSentinel)
	if !strings.Contains(string(encoded), `"NewField":"[REDACTED]"`) || !strings.Contains(string(encoded), "useful") {
		t.Fatalf("unexpected default redaction: %s", encoded)
	}
}

func TestDependencyGiteaTokenIsRedacted(t *testing.T) {
	cfg := HiveCIDependencyGiteaConfig{BaseURL: "https://git.example.test", Token: configSecretSentinel}
	encodedJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	encodedYAML, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{string(encodedJSON), string(encodedYAML), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg)} {
		assertSecretAbsent(t, "dependency Gitea config", output, configSecretSentinel)
	}
}
