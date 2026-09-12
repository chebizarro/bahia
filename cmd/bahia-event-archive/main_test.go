package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaimKindsRequiresExplicitScope(t *testing.T) {
	_, err := claimKinds(options{})
	require.ErrorContains(t, err, "explicit")

	kinds, err := claimKinds(options{kinds: "25910, 1059,25910"})
	require.NoError(t, err)
	require.Equal(t, []int{25910, 1059}, kinds)

	kinds, err = claimKinds(options{allKinds: true})
	require.NoError(t, err)
	require.Empty(t, kinds)

	_, err = claimKinds(options{allKinds: true, kinds: "4903"})
	require.ErrorContains(t, err, "either")
}
