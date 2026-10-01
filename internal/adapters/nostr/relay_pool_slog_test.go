package nostr

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestNewSlogZapLoggerForwardsLevelsNamesAndFields(t *testing.T) {
	var out bytes.Buffer
	logger := NewSlogZapLogger(slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo})))
	pool := logger.Named("relay-pool").With(zap.String("pool", "signet"))

	pool.Debug("below the slog level")
	require.Empty(t, out.String(), "slog's level gates zap entries")

	pool.Warn("relay refused subscription", zap.String("relay", "wss://r.example"), zap.Int("retries", 5))
	var record map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &record))
	require.Equal(t, "WARN", record["level"])
	require.Equal(t, "relay refused subscription", record["msg"])
	require.Equal(t, "relay-pool", record["logger"])
	require.Equal(t, "signet", record["pool"])
	require.Equal(t, "wss://r.example", record["relay"])
	require.EqualValues(t, 5, record["retries"])
}
