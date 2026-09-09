package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

const redactedValue = "[REDACTED]"

// Redacted returns a diagnostic view of the configuration in which protected
// values are replaced while non-secret settings remain available for incident
// diagnosis. The serialization and formatting methods below make this safe
// view the default for Config and every secret-bearing config subtree.
func (c Config) Redacted() any {
	return buildRedactedValue(reflect.ValueOf(c), "diagnostic")
}

func buildRedactedValue(value reflect.Value, tagName string) any {
	if !value.IsValid() {
		return nil
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}

	switch value.Kind() {
	case reflect.Struct:
		result := make(map[string]any)
		typeOfValue := value.Type()
		for i := 0; i < value.NumField(); i++ {
			fieldType := typeOfValue.Field(i)
			if !fieldType.IsExported() {
				continue
			}
			name, skip := redactedFieldName(fieldType, tagName)
			if skip {
				continue
			}
			fieldValue := value.Field(i)
			if fieldType.Anonymous && tagName == "json" && fieldType.Tag.Get("json") == "" {
				if embedded, ok := buildRedactedValue(fieldValue, tagName).(map[string]any); ok {
					for embeddedName, embeddedValue := range embedded {
						result[embeddedName] = embeddedValue
					}
					continue
				}
			}
			switch fieldType.Tag.Get("secret") {
			case "true":
				result[name] = buildProtectedValue(fieldValue)
				continue
			case "env_values":
				result[name] = buildProtectedEnvironment(fieldValue)
				continue
			case "url":
				result[name] = buildProtectedURL(fieldValue)
				continue
			}
			result[name] = buildRedactedValue(fieldValue, tagName)
		}
		return result
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		result := make(map[string]any, value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			result[fmt.Sprint(iterator.Key().Interface())] = buildRedactedValue(iterator.Value(), tagName)
		}
		return result
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return nil
		}
		result := make([]any, value.Len())
		for i := 0; i < value.Len(); i++ {
			result[i] = buildRedactedValue(value.Index(i), tagName)
		}
		return result
	default:
		return value.Interface()
	}
}

func redactedFieldName(field reflect.StructField, tagName string) (string, bool) {
	if tagName == "diagnostic" {
		return field.Name, false
	}
	tag := field.Tag.Get(tagName)
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" {
		return "", true
	}
	if name != "" {
		return name, false
	}
	if tagName == "yaml" {
		return strings.ToLower(field.Name), false
	}
	return field.Name, false
}

func buildProtectedValue(value reflect.Value) any {
	if !value.IsValid() {
		return nil
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if value.IsZero() {
		return value.Interface()
	}

	switch value.Kind() {
	case reflect.Map:
		result := make(map[string]any, value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			result[fmt.Sprint(iterator.Key().Interface())] = buildProtectedValue(iterator.Value())
		}
		return result
	case reflect.Slice, reflect.Array:
		result := make([]any, value.Len())
		for i := 0; i < value.Len(); i++ {
			result[i] = buildProtectedValue(value.Index(i))
		}
		return result
	default:
		return redactedValue
	}
}

func buildProtectedEnvironment(value reflect.Value) any {
	if value.Kind() != reflect.Slice || value.IsNil() {
		return buildProtectedValue(value)
	}
	result := make([]any, value.Len())
	for i := 0; i < value.Len(); i++ {
		entry := value.Index(i).String()
		key, _, found := strings.Cut(entry, "=")
		if !found {
			result[i] = buildProtectedValue(value.Index(i))
			continue
		}
		result[i] = key + "=" + redactedValue
	}
	return result
}

func buildProtectedURL(value reflect.Value) any {
	if value.Kind() != reflect.String || value.String() == "" {
		return buildProtectedValue(value)
	}
	parsed, err := url.Parse(value.String())
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return redactedValue
	}
	parsed.User = nil
	parsed.Path = "/" + redactedValue
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func stringRedactedConfig(value any) string {
	return fmt.Sprint(buildRedactedValue(reflect.ValueOf(value), "diagnostic"))
}

func formatRedactedConfig(state fmt.State, verb rune, value any) {
	formattedValue := buildRedactedValue(reflect.ValueOf(value), "diagnostic")
	if verb != 'v' {
		formattedValue = fmt.Sprint(formattedValue)
	}
	_, _ = fmt.Fprintf(state, formatDirective(state, verb), formattedValue)
}

func formatDirective(state fmt.State, verb rune) string {
	var format strings.Builder
	format.WriteByte('%')
	for _, flag := range []byte{'#', '0', '+', '-', ' '} {
		if state.Flag(int(flag)) {
			format.WriteByte(flag)
		}
	}
	if width, ok := state.Width(); ok {
		format.WriteString(strconv.Itoa(width))
	}
	if precision, ok := state.Precision(); ok {
		format.WriteByte('.')
		format.WriteString(strconv.Itoa(precision))
	}
	format.WriteRune(verb)
	return format.String()
}

func marshalRedactedJSON(value any) ([]byte, error) {
	return json.Marshal(buildRedactedValue(reflect.ValueOf(value), "json"))
}

func marshalRedactedYAML(value any) (any, error) {
	return buildRedactedValue(reflect.ValueOf(value), "yaml"), nil
}

// RedactError removes raw and commonly encoded secret representations from an
// error. It intentionally returns a new opaque error so callers cannot unwrap
// back to credential-bearing text.
func RedactError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	variants := make(map[string]struct{})
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for _, variant := range secretRepresentations(secret) {
			if variant != "" {
				variants[variant] = struct{}{}
			}
		}
	}
	ordered := make([]string, 0, len(variants))
	for variant := range variants {
		ordered = append(ordered, variant)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, variant := range ordered {
		message = strings.ReplaceAll(message, variant, redactedValue)
	}
	return fmt.Errorf("%s", message)
}

// RedactError removes the configured database credential from parse,
// construction, and connectivity errors while retaining useful endpoint and
// failure details.
func (c DBConfig) RedactError(err error) error {
	return RedactError(err, c.Password)
}

func secretRepresentations(secret string) []string {
	queryEscaped := url.QueryEscape(secret)
	pathEscaped := url.PathEscape(secret)
	userinfo := strings.TrimPrefix(url.UserPassword("_", secret).String(), "_:")
	variants := []string{
		secret,
		queryEscaped,
		pathEscaped,
		userinfo,
		html.EscapeString(secret),
		base64.StdEncoding.EncodeToString([]byte(secret)),
		base64.RawStdEncoding.EncodeToString([]byte(secret)),
	}
	if encoded, err := json.Marshal(secret); err == nil && len(encoded) >= 2 {
		variants = append(variants, string(encoded[1:len(encoded)-1]))
	}
	if quoted := strconv.Quote(secret); len(quoted) >= 2 {
		variants = append(variants, quoted[1:len(quoted)-1])
	}
	for _, encoded := range []string{queryEscaped, pathEscaped, userinfo} {
		variants = append(variants, lowerPercentEscapes(encoded))
	}
	return variants
}

func lowerPercentEscapes(value string) string {
	bytes := []byte(value)
	for i := 0; i+2 < len(bytes); i++ {
		if bytes[i] != '%' {
			continue
		}
		for j := i + 1; j <= i+2; j++ {
			if bytes[j] >= 'A' && bytes[j] <= 'F' {
				bytes[j] += 'a' - 'A'
			}
		}
		i += 2
	}
	return string(bytes)
}

func (c Config) String() string {
	return stringRedactedConfig(c)
}

func (c Config) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, c)
}

func (c Config) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(c)
}

func (c Config) MarshalYAML() (any, error) {
	return marshalRedactedYAML(c)
}

func (w WorkerCleanupConfig) String() string {
	return stringRedactedConfig(w)
}

func (w WorkerCleanupConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, w)
}

func (w WorkerCleanupConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(w)
}

func (w WorkerCleanupConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(w)
}

func (e EdgeRoutingConfig) String() string {
	return stringRedactedConfig(e)
}

func (e EdgeRoutingConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, e)
}

func (e EdgeRoutingConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(e)
}

func (e EdgeRoutingConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(e)
}

func (i InternalRoutingConfig) String() string {
	return stringRedactedConfig(i)
}

func (i InternalRoutingConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, i)
}

func (i InternalRoutingConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(i)
}

func (i InternalRoutingConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(i)
}

func (d DNSConfig) String() string {
	return stringRedactedConfig(d)
}

func (d DNSConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, d)
}

func (d DNSConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(d)
}

func (d DNSConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(d)
}

func (d DNSBackendConfig) String() string {
	return stringRedactedConfig(d)
}

func (d DNSBackendConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, d)
}

func (d DNSBackendConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(d)
}

func (d DNSBackendConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(d)
}

func (s SoulFactoryConfig) String() string {
	return stringRedactedConfig(s)
}

func (s SoulFactoryConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, s)
}

func (s SoulFactoryConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(s)
}

func (s SoulFactoryConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(s)
}

func (a AssistantConfig) String() string {
	return stringRedactedConfig(a)
}

func (a AssistantConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, a)
}

func (a AssistantConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(a)
}

func (a AssistantConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(a)
}

func (a AssistantAgenticConfig) String() string {
	return stringRedactedConfig(a)
}

func (a AssistantAgenticConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, a)
}

func (a AssistantAgenticConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(a)
}

func (a AssistantAgenticConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(a)
}

func (a AssistantMCPConfig) String() string {
	return stringRedactedConfig(a)
}

func (a AssistantMCPConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, a)
}

func (a AssistantMCPConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(a)
}

func (a AssistantMCPConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(a)
}

func (a AssistantExternalMCPServerConfig) String() string {
	return stringRedactedConfig(a)
}

func (a AssistantExternalMCPServerConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, a)
}

func (a AssistantExternalMCPServerConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(a)
}

func (a AssistantExternalMCPServerConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(a)
}

func (p PackageBackendConfig) String() string {
	return stringRedactedConfig(p)
}

func (p PackageBackendConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, p)
}

func (p PackageBackendConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(p)
}

func (p PackageBackendConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(p)
}

func (l LLMControlplaneConfig) String() string {
	return stringRedactedConfig(l)
}

func (l LLMControlplaneConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, l)
}

func (l LLMControlplaneConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(l)
}

func (l LLMControlplaneConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(l)
}

func (l LLMGatewayEndpointConfig) String() string {
	return stringRedactedConfig(l)
}

func (l LLMGatewayEndpointConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, l)
}

func (l LLMGatewayEndpointConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(l)
}

func (l LLMGatewayEndpointConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(l)
}

func (r RegistryAdapterConfig) String() string {
	return stringRedactedConfig(r)
}

func (r RegistryAdapterConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, r)
}

func (r RegistryAdapterConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(r)
}

func (r RegistryAdapterConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(r)
}

func (d DBConfig) String() string {
	return stringRedactedConfig(d)
}

func (d DBConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, d)
}

func (d DBConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(d)
}

func (d DBConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(d)
}

func (h HarborConfig) String() string {
	return stringRedactedConfig(h)
}

func (h HarborConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, h)
}

func (h HarborConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(h)
}

func (h HarborConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(h)
}

func (l LoomConfig) String() string {
	return stringRedactedConfig(l)
}

func (l LoomConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, l)
}

func (l LoomConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(l)
}

func (l LoomConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(l)
}

func (l LoomCanonicalProjectionConfig) String() string {
	return stringRedactedConfig(l)
}

func (l LoomCanonicalProjectionConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, l)
}

func (l LoomCanonicalProjectionConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(l)
}

func (l LoomCanonicalProjectionConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(l)
}

func (n NostrConfig) String() string {
	return stringRedactedConfig(n)
}

func (n NostrConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, n)
}

func (n NostrConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(n)
}

func (n NostrConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(n)
}

func (r RelaySidecarConfig) String() string {
	return stringRedactedConfig(r)
}

func (r RelaySidecarConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, r)
}

func (r RelaySidecarConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(r)
}

func (r RelaySidecarConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(r)
}

func (r RelayAdministrationConfig) String() string {
	return stringRedactedConfig(r)
}

func (r RelayAdministrationConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, r)
}

func (r RelayAdministrationConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(r)
}

func (r RelayAdministrationConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(r)
}

func (b BlossomConfig) String() string {
	return stringRedactedConfig(b)
}

func (b BlossomConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, b)
}

func (b BlossomConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(b)
}

func (b BlossomConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(b)
}

func (o OCIServerConfig) String() string {
	return stringRedactedConfig(o)
}

func (o OCIServerConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, o)
}

func (o OCIServerConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(o)
}

func (o OCIServerConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(o)
}

func (o OCIServiceAccountConfig) String() string {
	return stringRedactedConfig(o)
}

func (o OCIServiceAccountConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, o)
}

func (o OCIServiceAccountConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(o)
}

func (o OCIServiceAccountConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(o)
}

func (h HiveCIConfig) String() string {
	return stringRedactedConfig(h)
}

func (h HiveCIConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, h)
}

func (h HiveCIConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(h)
}

func (h HiveCIConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(h)
}

func (h HiveCIInitiatorConfig) String() string {
	return stringRedactedConfig(h)
}

func (h HiveCIInitiatorConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, h)
}

func (h HiveCIInitiatorConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(h)
}

func (h HiveCIInitiatorConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(h)
}

func (q QdrantConfig) String() string {
	return stringRedactedConfig(q)
}

func (q QdrantConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, q)
}

func (q QdrantConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(q)
}

func (q QdrantConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(q)
}

func (n NotificationsConfig) String() string {
	return stringRedactedConfig(n)
}

func (n NotificationsConfig) Format(state fmt.State, verb rune) {
	formatRedactedConfig(state, verb, n)
}

func (n NotificationsConfig) MarshalJSON() ([]byte, error) {
	return marshalRedactedJSON(n)
}

func (n NotificationsConfig) MarshalYAML() (any, error) {
	return marshalRedactedYAML(n)
}
