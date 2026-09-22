package domain

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestVMAdoptionMeasurementBindsAllEvidence(t *testing.T) {
	_, image, v := vmTestResources()
	m := VMAdoptionMeasurement{SchemaVersion: 1, Identity: v.Identity, Generation: v.Generation, ImageID: image.ID, ImageDigest: image.ManifestDigest, ConfigDigest: VMAdoptionConfigDigest(*v, vmTestDigest()), ProviderFingerprint: vmTestDigest(), StoragePoolRef: v.StoragePoolRef, Components: []VMAdoptionComponent{{VMComponent: VMComponent{Kind: VMComponentDisk, StorageRef: uuid.New(), Digest: vmTestDigest(), SizeBytes: 123}, StorageKey: vmTestDigest(), SourceDigest: vmTestDigest()}}}
	m.Digest = VMAdoptionDigest(m)
	require.NoError(t, ValidateVMAdoptionMeasurement(&m))
	encoded, err := json.Marshal(m)
	require.NoError(t, err)
	var roundTrip VMAdoptionMeasurement
	require.NoError(t, json.Unmarshal(encoded, &roundTrip))
	require.Equal(t, m, roundTrip)
	for _, change := range []func(*VMAdoptionMeasurement){func(m *VMAdoptionMeasurement) { m.Generation++ }, func(m *VMAdoptionMeasurement) { m.Identity.HostID = uuid.New() }, func(m *VMAdoptionMeasurement) { m.Components[0].SizeBytes++ }, func(m *VMAdoptionMeasurement) { m.ProviderEvidence = []string{vmTestDigest()} }} {
		var changed VMAdoptionMeasurement
		require.NoError(t, json.Unmarshal(encoded, &changed))
		change(&changed)
		require.Error(t, ValidateVMAdoptionMeasurement(&changed))
	}
	nilNetwork := *v
	nilNetwork.Network.PassthroughDeviceRefs = nil
	require.Equal(t, VMAdoptionConfigDigest(*v, vmTestDigest()), VMAdoptionConfigDigest(nilNetwork, vmTestDigest()))
	m.Components = append(m.Components, m.Components[0])
	m.Digest = VMAdoptionDigest(m)
	require.Error(t, ValidateVMAdoptionMeasurement(&m))
}
