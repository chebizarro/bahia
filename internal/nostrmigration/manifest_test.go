package nostrmigration

import (
	"testing"

	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

func TestManifestCoversBahiaInventoryLegacyKinds(t *testing.T) {
	want := []int{}
	appendRange := func(start, end int) {
		for kind := start; kind <= end; kind++ {
			want = append(want, kind)
		}
	}
	appendRange(5941, 5945)
	want = append(want, 5980, 6006, 6941, 6981, 6982, 6983, 6984, 7941, 7942, 7943, 7944, 7945, 7980)
	for _, kind := range []int{5961, 5962, 5963, 5964, 5965, 5966, 5967, 5968, 5971, 5972, 5973, 5974, 5975, 5976, 5977, 5978, 5979, 5981, 5982, 5983, 5984, 5985, 5986, 5987, 5988, 5989, 5991, 5992, 5993, 5994, 5995, 5996, 5997, 5998, 5999, 6000, 6001, 6002, 6003, 6004, 6005} {
		want = append(want, kind)
	}
	for _, kind := range []int{6961, 6962, 6963, 6973, 6976, 6978, 6991, 6997} {
		want = append(want, kind)
	}
	for _, kind := range []int{7961, 7962, 7963, 7964, 7965, 7966, 7971, 7972, 7973, 7976, 7977, 7978, 7979, 7991, 7992, 7997} {
		want = append(want, kind)
	}
	appendRange(31000, 31024)
	appendRange(31100, 31105)
	want = append(want, 31310, 31311, 31400, 31401, 31402, 31403, 31404, 31410, 31411, 30350, 30351, 30352, 30353, 30360)
	appendRange(31961, 31978)
	appendRange(31980, 32003)
	appendRange(38390, 38399)
	appendRange(38400, 38423)
	want = append(want, 38430, 38431, 30002, 30078, 30079)

	for _, kind := range want {
		disp, ok := Lookup(kind)
		require.Truef(t, ok, "missing migration disposition for kind %d", kind)
		require.NotZero(t, disp.CanonicalKind, "canonical kind missing for %d", kind)
		require.NotEmpty(t, disp.Layer, "layer missing for %d", kind)
		require.NotEmpty(t, disp.Domain, "domain missing for %d", kind)
		require.NotEmpty(t, disp.Schema, "schema missing for %d", kind)
	}
}

func TestManifestCanonicalTargets(t *testing.T) {
	deploy, ok := Lookup(kinds.DeployRequest)
	require.True(t, ok)
	require.Equal(t, CanonicalContextVMMessage, deploy.CanonicalKind)
	require.Equal(t, "service/deploy", deploy.Method)

	serviceDelete, ok := Lookup(kinds.ServiceDelete)
	require.True(t, ok)
	require.Equal(t, CanonicalNIP09Delete, serviceDelete.CanonicalKind)
	require.True(t, serviceDelete.Delete)

	state, ok := Lookup(kinds.ServiceState)
	require.True(t, ok)
	require.Equal(t, CanonicalCASCPState, state.CanonicalKind)
	require.Equal(t, LayerState, state.Layer)

	audit, ok := Lookup(kinds.DeploymentCreated)
	require.True(t, ok)
	require.Equal(t, CanonicalCASAudit, audit.CanonicalKind)

	discovery, ok := Lookup(kinds.SystemDiscovery)
	require.True(t, ok)
	require.Equal(t, CanonicalContextVMDiscovery, discovery.CanonicalKind)
}
