package loom

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

func planeFixture(t *testing.T) (domain.ExecutionPlaneEndpoint, domain.ExecutionPlaneDeployment, domain.ExecutionPlaneObservation, string, time.Time) {
	t.Helper()
	key, author := generatedKeyPair(t)
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	endpoint := domain.ExecutionPlaneEndpoint{HostID: uuid.New(), EndpointRef: uuid.New(), Author: author}
	capability := domain.ExecutionPlaneCapability{LifecycleClass: domain.VMLifecycleLoomQEMU, OS: domain.VMOSLinux, Architecture: "amd64", AgentProtocolVersion: "3"}
	p := domain.ExecutionPlaneDeployment{VirtualizationResourceMeta: domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: uuid.New(), OrgID: uuid.New(), Generation: 1, CreatedBy: author}, HostID: endpoint.HostID, ManagementEndpointRef: endpoint.EndpointRef, ManagementAuthor: author, WorkerPubKey: author, Desired: domain.ExecutionPlaneDesired{
		LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecycleLoomQEMU}, Package: domain.ExecutionPlanePackagePin{Digest: digest, Version: "1", Provenance: domain.VMProvenance{EventID: strings.Repeat("b", 64), Signer: author, Verified: true, VerifiedAt: now}},
		Configuration: domain.ExecutionPlaneConfiguration{Revision: digest, Network: domain.VMNetwork{Mode: domain.VMNetworkIsolated}}, ImagePins: []domain.ExecutionPlaneImagePin{{LifecycleClass: domain.VMLifecycleLoomQEMU, ImageID: uuid.New(), ManifestDigest: digest}}, ReservedCapacity: domain.VMCapacity{VCPU: 2, MemoryBytes: 4 << 30, DiskBytes: 10 << 30}, Concurrency: 2, ExpectedCapabilities: []domain.ExecutionPlaneCapability{capability}, State: domain.ExecutionPlaneEnabled, ProbePolicy: domain.DefaultExecutionPlaneProbePolicy(),
	}}
	o := domain.ExecutionPlaneObservation{VMObservationStamp: domain.VMObservationStamp{SchemaVersion: 1, ObservedGeneration: 1, SessionID: uuid.New(), Sequence: 2, ObservedAt: now}, PlaneID: p.ID, HostID: p.HostID, Author: author, LifecycleClasses: p.Desired.LifecycleClasses, Availability: domain.VMObservationAvailable, Drift: domain.VMDriftInSync, PackageDigest: digest, ConfigRevision: digest, ImagePins: p.Desired.ImagePins, ReservedCapacity: p.Desired.ReservedCapacity, Concurrency: 2, State: domain.ExecutionPlaneEnabled}
	o.Probe = &domain.ExecutionPlaneProbeEvidence{VMObservationStamp: o.VMObservationStamp, PlaneID: p.ID, Author: author, LifecycleClasses: p.Desired.LifecycleClasses, Successful: true, PackageDigest: digest, ConfigRevision: digest, ImagePins: p.Desired.ImagePins, Capabilities: p.Desired.ExpectedCapabilities}
	require.NoError(t, domain.ValidateExecutionPlaneDeployment(&p))
	return endpoint, p, o, key, now
}

func planeObservationEvent(t *testing.T, e domain.ExecutionPlaneEndpoint, o domain.ExecutionPlaneObservation, key string, now time.Time, extra ...nostr.Tag) *nostr.Event {
	t.Helper()
	data, err := json.Marshal(o)
	require.NoError(t, err)
	tags := append(planeTags(e), nostr.Tag{"d", PlaneStateCoordinate(o.PlaneID)})
	tags = append(tags, extra...)
	return signLoomEvent(t, key, kinds.CASControlState, now, tags, string(data))
}

func TestPlaneObservationStrictTrustBoundary(t *testing.T) {
	e, p, base, key, now := planeFixture(t)
	valid := planeObservationEvent(t, e, base, key, now)
	_, err := DecodePlaneObservation(valid, e, p.ID, now)
	require.NoError(t, err)
	for name, mutate := range map[string]func(*domain.ExecutionPlaneObservation){
		"wrong host":      func(o *domain.ExecutionPlaneObservation) { o.HostID = uuid.New() },
		"wrong author":    func(o *domain.ExecutionPlaneObservation) { o.Author = strings.Repeat("f", 64) },
		"missing session": func(o *domain.ExecutionPlaneObservation) { o.SessionID = uuid.Nil },
		"future evidence": func(o *domain.ExecutionPlaneObservation) { o.ObservedAt = now.Add(time.Second) },
		"wrong schema":    func(o *domain.ExecutionPlaneObservation) { o.SchemaVersion = 2 },
		"bad pins":        func(o *domain.ExecutionPlaneObservation) { o.PackageDigest = "latest" },
		"persistent class": func(o *domain.ExecutionPlaneObservation) {
			o.LifecycleClasses = []domain.VMLifecycleClass{domain.VMLifecyclePersistent}
		},
		"windows": func(o *domain.ExecutionPlaneObservation) {
			o.Probe.Capabilities = []domain.ExecutionPlaneCapability{{LifecycleClass: domain.VMLifecycleLoomQEMU, OS: domain.VMOSWindows, Architecture: "amd64", AgentProtocolVersion: "3"}}
		},
		"failed granting": func(o *domain.ExecutionPlaneObservation) { o.Probe.Successful = false },
		"future sequence": func(o *domain.ExecutionPlaneObservation) { o.Probe.Sequence++ },
		"probe session":   func(o *domain.ExecutionPlaneObservation) { o.Probe.SessionID = uuid.New() },
	} {
		t.Run(name, func(t *testing.T) {
			o := base
			q := *base.Probe
			o.Probe = &q
			mutate(&o)
			_, err := DecodePlaneObservation(planeObservationEvent(t, e, o, key, now), e, p.ID, now)
			require.Error(t, err)
		})
	}
	for name, mutate := range map[string]func(*nostr.Event){
		"tampered content":   func(ev *nostr.Event) { ev.Content += " " },
		"tampered signature": func(ev *nostr.Event) { ev.Sig[0] ^= 1 },
		"plaintext extra": func(ev *nostr.Event) {
			ev.Content = strings.TrimSuffix(ev.Content, "}") + `,"password":"secret"}`
			require.NoError(t, HexKeyCanonicalSigner{PrivateKey: key}.Sign(t.Context(), ev))
		},
		"duplicate routing": func(ev *nostr.Event) {
			ev.Tags = append(ev.Tags, nostr.Tag{"host", e.HostID.String()})
			require.NoError(t, HexKeyCanonicalSigner{PrivateKey: key}.Sign(t.Context(), ev))
		},
	} {
		t.Run(name, func(t *testing.T) {
			ev := planeObservationEvent(t, e, base, key, now)
			mutate(ev)
			_, err := DecodePlaneObservation(ev, e, p.ID, now)
			require.Error(t, err)
		})
	}
}

func TestPlaneSupportRequiresGovernedToolsAndClasses(t *testing.T) {
	e, _, _, key, now := planeFixture(t)
	support := domain.ExecutionPlaneSupport{ProtocolVersion: 1, Tools: []string{domain.ExecutionPlaneInspectTool, domain.ExecutionPlaneApplyTool, domain.ExecutionPlaneProbeTool}, LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecycleLoomQEMU}}
	encode := func(s domain.ExecutionPlaneSupport) *nostr.Event {
		b, _ := json.Marshal(s)
		return signLoomEvent(t, key, kinds.ContextVMServerAnnouncement, now, append(planeTags(e), nostr.Tag{"d", e.EndpointRef.String()}), string(b))
	}
	_, err := decodePlaneSupport(encode(support), e, now)
	require.NoError(t, err)
	support.Tools = support.Tools[:2]
	_, err = decodePlaneSupport(encode(support), e, now)
	require.Error(t, err)
	support.ProtocolVersion = 2
	require.False(t, SupportsPlaneClasses(support, []domain.VMLifecycleClass{domain.VMLifecycleLoomQEMU}))
}
