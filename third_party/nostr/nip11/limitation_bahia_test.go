package nip11

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLimitationOmitsZeroCreatedAtLowerLimit: a zero lower limit is left out
// (a relay without a uniform lower bound must not advertise one of 0 seconds),
// a non-zero one is kept, and the limitation stays valid JSON when no field
// precedes the upper limit.
func TestLimitationOmitsZeroCreatedAtLowerLimit(t *testing.T) {
	encoded, err := json.Marshal(RelayInformationDocument{Limitation: &RelayLimitationDocument{CreatedAtUpperLimit: 600}})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "created_at_lower_limit")
	var fields struct {
		Limitation map[string]any `json:"limitation"`
	}
	require.NoError(t, json.Unmarshal(encoded, &fields), string(encoded))
	require.EqualValues(t, 600, fields.Limitation["created_at_upper_limit"])

	encoded, err = json.Marshal(RelayInformationDocument{Limitation: &RelayLimitationDocument{MaxLimit: 10, CreatedAtLowerLimit: 3600, CreatedAtUpperLimit: 600}})
	require.NoError(t, err)
	var decoded RelayInformationDocument
	require.NoError(t, json.Unmarshal(encoded, &decoded), string(encoded))
	require.EqualValues(t, 3600, decoded.Limitation.CreatedAtLowerLimit)
	require.EqualValues(t, 600, decoded.Limitation.CreatedAtUpperLimit)
	require.Equal(t, 10, decoded.Limitation.MaxLimit)
}
