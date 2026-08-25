package soulfactory

import (
	"strings"
	"testing"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestFleetConfigEventValidationAndTrustedAuthor(t *testing.T) {
	author := strings.Repeat("a", 64)
	pubkey, err := nostr.PubKeyFromHex(author)
	if err != nil {
		t.Fatal(err)
	}
	document := FleetConfigDocument{
		Schema: SoulFactoryFleetConfigSchema,
		Template: map[string]interface{}{
			"logging": map[string]interface{}{"level": "info"},
			"auth": map[string]interface{}{"profiles": map[string]interface{}{
				"operator": map[string]interface{}{"apiKey": "${OPENCLAW_API_KEY}"},
			}},
		},
		Defaults: FleetConfigDefaults{
			Model:           "provider/fleet-model",
			Bindings:        []string{"slack:ops"},
			RequiredPlugins: []string{"nostr=npm:openclaw-nostr@1.0.0"},
		},
	}
	event, err := BuildFleetConfigEvent(document)
	if err != nil {
		t.Fatalf("BuildFleetConfigEvent() error = %v", err)
	}
	event.PubKey = pubkey
	if event.Kind != nostr.Kind(domain.KindSoulFleetConfig) ||
		tagValue(event.Tags, tagParameterizedD) != SoulFactoryFleetConfigIdentifier ||
		tagValue(event.Tags, "schema") != SoulFactoryFleetConfigSchema {
		t.Fatalf("fleet event shape = kind %d tags %#v", event.Kind, event.Tags)
	}
	snapshot, err := ParseFleetConfigEvent(event, []string{author})
	if err != nil {
		t.Fatalf("ParseFleetConfigEvent() error = %v", err)
	}
	if snapshot.Author != author || snapshot.Document.Defaults.Model != "provider/fleet-model" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if _, err := ParseFleetConfigEvent(event, []string{strings.Repeat("b", 64)}); err == nil || !strings.Contains(err.Error(), "trusted operator") {
		t.Fatalf("untrusted parse error = %v", err)
	}
}

func TestFleetConfigValidationRejectsUnknownSectionsAndConcreteSecrets(t *testing.T) {
	tests := []struct {
		name     string
		document FleetConfigDocument
		want     string
	}{
		{
			name: "unknown top-level section",
			document: FleetConfigDocument{
				Schema:   SoulFactoryFleetConfigSchema,
				Template: map[string]interface{}{"identity": map[string]interface{}{"name": "not-openclaw"}},
			},
			want: "not allowed",
		},
		{
			name: "concrete secret",
			document: FleetConfigDocument{
				Schema:   SoulFactoryFleetConfigSchema,
				Template: map[string]interface{}{"gateway": map[string]interface{}{"auth": map[string]interface{}{"token": "literal-secret"}}},
			},
			want: "${VAR}",
		},
		{
			name: "ambiguous plugin requirement",
			document: FleetConfigDocument{
				Schema:   SoulFactoryFleetConfigSchema,
				Template: map[string]interface{}{},
				Defaults: FleetConfigDefaults{RequiredPlugins: []string{"nostr"}},
			},
			want: "plugin-id=install-source",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFleetConfigDocument(tc.document)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateFleetConfigDocument() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNewestFleetConfigEventUsesTimestampThenEventID(t *testing.T) {
	firstID, _ := nostr.IDFromHex(strings.Repeat("1", 64))
	secondID, _ := nostr.IDFromHex(strings.Repeat("2", 64))
	events := []*nostr.Event{
		{ID: firstID, Kind: nostr.Kind(domain.KindSoulFleetConfig), CreatedAt: nostr.Timestamp(42)},
		{ID: secondID, Kind: nostr.Kind(domain.KindSoulFleetConfig), CreatedAt: nostr.Timestamp(42)},
	}
	if got := newestFleetConfigEvent(events); got == nil || got.ID != secondID {
		t.Fatalf("newestFleetConfigEvent() = %#v", got)
	}
}
