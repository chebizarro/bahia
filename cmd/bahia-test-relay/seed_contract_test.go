package main

import (
	"context"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/fipsbridge"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// These tests prove the relay's seed corpus is the producer contract, by
// running it through the validation and decoding the real consumers use.

func seedEvents(t *testing.T) []nostr.Event {
	t.Helper()
	events, err := seedCorpus("ws://127.0.0.1:48639")
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func seedServicePubkey() nostr.PubKey {
	return nostr.MustSecretKeyFromHex(serviceSecretHex).Public()
}

func firstTag(ev nostr.Event, key string) string {
	if tag := ev.Tags.Find(key); len(tag) >= 2 {
		return tag[1]
	}
	return ""
}

func TestSeedCorpusPassesInboundValidation(t *testing.T) {
	for _, ev := range seedEvents(t) {
		if err := nostradapter.ValidateInboundEvent(&ev, time.Now().UTC(), nostradapter.InboundEventMaxFutureSkew); err != nil {
			t.Errorf("seed kind %d d=%q fails inbound validation: %v", ev.Kind, firstTag(ev, "d"), err)
		}
	}
}

// TestSeedControlStateMatchesProducerEnvelope checks every projected record
// carries the envelope the projector would stamp on (legacy_kind, d), and that
// no seed uses a per-family schema the projector never sends.
func TestSeedControlStateMatchesProducerEnvelope(t *testing.T) {
	seededLegacyKinds := map[int]bool{}
	for _, ev := range seedEvents(t) {
		if int(ev.Kind) != kinds.CASControlState {
			continue
		}
		legacyValue := firstTag(ev, kinds.CASControlStateTagLegacyKind)
		if legacyValue == "" {
			// Only non-projected records (the assistant session) may omit the envelope.
			if firstTag(ev, kinds.CASControlStateTagDomain) != "assistant" {
				t.Errorf("30900 seed d=%q has no legacy_kind; tags=%v", firstTag(ev, "d"), ev.Tags)
			}
			continue
		}
		legacyKind, err := strconv.Atoi(legacyValue)
		if err != nil {
			t.Fatalf("legacy_kind %q: %v", legacyValue, err)
		}
		seededLegacyKinds[legacyKind] = true
		if got := firstTag(ev, kinds.CASControlStateTagDeleted); got != "false" {
			t.Errorf("legacy_kind %d seed deleted=%q, want \"false\"", legacyKind, got)
		}
		if topic, ok := controlPlaneWorkerTopics[legacyKind]; ok {
			// The control plane's worker publishers stamp the cp-state
			// envelope themselves, including the family's t topic.
			want := nostr.Tags{
				{kinds.CASControlStateTagD, firstTag(ev, "d")},
				{kinds.CASControlStateTagDomain, kinds.WorkerDomain},
				{kinds.CASControlStateTagSchema, kinds.CASControlStateSchema},
				{kinds.CASControlStateTagLegacyKind, legacyValue},
				{kinds.CASControlStateTagDeleted, "false"},
				{"t", topic},
			}
			for i, tag := range want {
				if i >= len(ev.Tags) || !slices.Equal(ev.Tags[i], tag) {
					t.Errorf("worker legacy_kind %d: tag[%d] = %v, want %v", legacyKind, i, ev.Tags, tag)
				}
			}
			continue
		}
		wireKind, envelope := nostradapter.ControlStateEnvelope(legacyKind, firstTag(ev, "d"), false)
		if wireKind != int(ev.Kind) {
			t.Errorf("legacy_kind %d: wire kind %d, projector uses %d", legacyKind, ev.Kind, wireKind)
		}
		if len(ev.Tags) < len(envelope) {
			t.Fatalf("legacy_kind %d: tags %v shorter than envelope %v", legacyKind, ev.Tags, envelope)
		}
		for i, want := range envelope {
			if !slices.Equal(ev.Tags[i], want) {
				t.Errorf("legacy_kind %d: tag[%d] = %v, projector envelope has %v", legacyKind, i, ev.Tags[i], want)
			}
		}
		if got := firstTag(ev, kinds.CASControlStateTagSchema); got != kinds.CASControlStateSchema {
			t.Errorf("legacy_kind %d: schema %q, want %q", legacyKind, got, kinds.CASControlStateSchema)
		}
	}
	for _, want := range []int{kinds.CPStateFamilyWorkerState.LegacyKind(), kinds.CPStateFamilyWorkerAssignment.LegacyKind(), kinds.CPStateFamilyWorkerDrain.LegacyKind(), kinds.CPStateFamilyWorkerEligibility.LegacyKind(), kinds.CPStateFamilyWorkerCleanup.LegacyKind(), kinds.ServiceRegistry, kinds.DNSZoneState, kinds.DNSEndpointState, kinds.DNSPolicyState, kinds.DNSBackendState} {
		if !seededLegacyKinds[want] {
			t.Errorf("seed corpus has no record for legacy_kind %d", want)
		}
	}
}

// controlPlaneWorkerTopics are the worker families the control plane (not the
// projector) publishes, with the t topic each carries.
var controlPlaneWorkerTopics = map[int]string{
	kinds.CPStateFamilyWorkerState.LegacyKind():   kinds.WorkerStateTopic,
	kinds.CPStateFamilyWorkerCleanup.LegacyKind(): kinds.WorkerCleanupTopic,
}

// TestSeedWorkerStateMatchesTopicFilter runs the web fips-mesh and cleanup
// REQ shapes: worker state and cleanup execution are found by author and
// single-letter t topic, each topic matching exactly its own family.
func TestSeedWorkerStateMatchesTopicFilter(t *testing.T) {
	for legacyKind, topic := range controlPlaneWorkerTopics {
		filter := nostr.Filter{
			Kinds:   []nostr.Kind{nostr.Kind(kinds.CASControlState)},
			Authors: []nostr.PubKey{seedServicePubkey()},
			Tags:    nostr.TagMap{"t": {topic}},
		}
		matched := 0
		for _, ev := range seedEvents(t) {
			if !filter.Matches(ev) {
				continue
			}
			matched++
			if got := firstTag(ev, kinds.CASControlStateTagLegacyKind); got != strconv.Itoa(legacyKind) {
				t.Errorf("#t=%s matched legacy_kind %s", topic, got)
			}
		}
		if matched != 1 {
			t.Errorf("#t=%s matched %d seeds, want 1", topic, matched)
		}
	}
}

// TestSeedDNSStateMatchesTopicFilter runs the web/fipsbridge REQ shape: DNS
// state is found by author and single-letter t topic, not #domain.
func TestSeedDNSStateMatchesTopicFilter(t *testing.T) {
	topicByLegacyKind := map[string]string{
		strconv.Itoa(kinds.DNSZoneState):     kinds.DNSZoneTopic,
		strconv.Itoa(kinds.DNSEndpointState): kinds.DNSEndpointTopic,
		strconv.Itoa(kinds.DNSPolicyState):   kinds.DNSPolicyTopic,
		strconv.Itoa(kinds.DNSBackendState):  kinds.DNSBackendTopic,
	}
	filter := nostr.Filter{
		Kinds:   []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{seedServicePubkey()},
		Tags:    nostr.TagMap{"t": {kinds.DNSZoneTopic, kinds.DNSEndpointTopic, kinds.DNSPolicyTopic, kinds.DNSBackendTopic}},
	}
	matched := map[string]bool{}
	for _, ev := range seedEvents(t) {
		isDNS := int(ev.Kind) == kinds.CASControlState && firstTag(ev, kinds.CASControlStateTagDomain) == kinds.DNSDomain
		if filter.Matches(ev) != isDNS {
			t.Errorf("#t filter match=%t for d=%q domain=%q", !isDNS, firstTag(ev, "d"), firstTag(ev, kinds.CASControlStateTagDomain))
			continue
		}
		if !isDNS {
			continue
		}
		legacyKind := firstTag(ev, kinds.CASControlStateTagLegacyKind)
		if !ev.Tags.Has("t") || !slices.ContainsFunc(ev.Tags, func(tag nostr.Tag) bool {
			return len(tag) >= 2 && tag[0] == "t" && tag[1] == topicByLegacyKind[legacyKind]
		}) {
			t.Errorf("DNS legacy_kind %s seed lacks t=%s", legacyKind, topicByLegacyKind[legacyKind])
		}
		matched[legacyKind] = true
	}
	if len(matched) != len(topicByLegacyKind) {
		t.Fatalf("#t filter matched DNS families %v, want all of %v", matched, topicByLegacyKind)
	}
}

// TestSeedDNSEndpointsAcceptedByFIPSBridge feeds the endpoint seed to the
// fipsbridge consumer, which validates signature, envelope and author.
func TestSeedDNSEndpointsAcceptedByFIPSBridge(t *testing.T) {
	bridge, err := fipsbridge.NewBridge(fipsbridge.Config{
		BahiaPubkey: seedServicePubkey().Hex(),
		RelayURLs:   []string{"ws://127.0.0.1:48639"},
		HostsPath:   filepath.Join(t.TempDir(), "hosts"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	endpoints := 0
	for _, ev := range seedEvents(t) {
		if firstTag(ev, kinds.CASControlStateTagLegacyKind) != strconv.Itoa(kinds.DNSEndpointState) {
			continue
		}
		endpoints++
		if err := bridge.HandleEvent(context.Background(), &ev); err != nil {
			t.Fatalf("fipsbridge rejected endpoint seed: %v", err)
		}
		endpoint, err := fipsbridge.ParseEndpointEvent(&ev)
		if err != nil {
			t.Fatal(err)
		}
		if endpoint.Tombstone || endpoint.FQDN != seedDNSEndpointFQDN || endpoint.Npub == "" || endpoint.Health != "healthy" {
			t.Fatalf("parsed endpoint = %+v", endpoint)
		}

		forged := nostr.Event{Kind: ev.Kind, CreatedAt: ev.CreatedAt + 1, Tags: ev.Tags, Content: ev.Content}
		if err := forged.Sign(nostr.MustSecretKeyFromHex(operatorSecretHex)); err != nil {
			t.Fatal(err)
		}
		if err := bridge.HandleEvent(context.Background(), &forged); err == nil {
			t.Fatal("fipsbridge accepted an endpoint record signed by a non-service author")
		}
	}
	if endpoints != 1 {
		t.Fatalf("seeded %d DNS endpoint records, want 1", endpoints)
	}
}

// TestSeedProjectedRecordsDecodeThroughCatalog decodes each projected record
// the way the replay bootstrapper does: with the catalog decoder for its wire
// kind, which routes worker cp-state families by legacy_kind.
func TestSeedProjectedRecordsDecodeThroughCatalog(t *testing.T) {
	catalog := nostradapter.NewKindCatalog()
	decodedWorker := false
	for _, ev := range seedEvents(t) {
		legacyKind, err := strconv.Atoi(firstTag(ev, kinds.CASControlStateTagLegacyKind))
		if err != nil {
			continue
		}
		decode, ok := catalog.Decoder(int(ev.Kind))
		if !ok {
			t.Fatalf("no catalog decoder for wire kind %d", ev.Kind)
		}
		decoded, err := decode(&ev)
		if err != nil {
			t.Errorf("catalog decoder rejected legacy_kind %d seed d=%q: %v", legacyKind, firstTag(ev, "d"), err)
			continue
		}
		if legacyKind == kinds.CPStateFamilyWorkerState.LegacyKind() {
			if decoded.Worker == nil || decoded.Worker.Worker == nil || decoded.Worker.Worker.Name != "worker-one" {
				t.Fatalf("decoded worker = %+v", decoded.Worker)
			}
			decodedWorker = true
		}
	}
	if !decodedWorker {
		t.Fatal("worker state seed was not decoded")
	}
}
