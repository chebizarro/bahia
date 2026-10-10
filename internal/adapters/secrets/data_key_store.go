package secrets

import (
	"context"
	"errors"

	"fiatjaf.com/nostr"
	"github.com/jackc/pgx/v5"
)

// loadSoleWrappedDataKey refuses ambiguous key state. A future rotation needs
// an explicit keyring selected by the ciphertext's key ID, not an implicit
// newest-key choice. It remains package-private until fenced runtime wiring.
func loadSoleWrappedDataKey(ctx context.Context, conn *pgx.Conn, keyer nostr.Keyer, expected nostr.PubKey) (*DataKey, error) {
	if conn == nil || keyer == nil || expected == nostr.ZeroPK {
		return nil, errors.New("invalid service-secret data-key loader configuration")
	}
	rows, err := conn.Query(ctx, `SELECT id, service_pubkey, wrapped_key FROM service_secret_data_keys ORDER BY created_at, id LIMIT 2`)
	if err != nil {
		return nil, errors.New("read wrapped service-secret data key")
	}
	var wrapped WrappedDataKey
	var pubkeyHex string
	if !rows.Next() {
		rows.Close()
		return nil, errors.New("wrapped service-secret data key missing")
	}
	if err := rows.Scan(&wrapped.ID, &pubkeyHex, &wrapped.WrappedHex); err != nil {
		rows.Close()
		return nil, errors.New("scan wrapped service-secret data key")
	}
	if rows.Next() || rows.Err() != nil {
		rows.Close()
		return nil, errors.New("multiple or unreadable wrapped service-secret data keys")
	}
	rows.Close()
	pubkey, err := nostr.PubKeyFromHex(pubkeyHex)
	if err != nil || pubkey != expected {
		return nil, errors.New("wrapped data-key service pubkey does not match existing service pubkey")
	}
	wrapped.ServiceKey = pubkey
	return openWrappedDataKey(ctx, keyer, wrapped, expected)
}
