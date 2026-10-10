//go:build integration

package secrets

import (
	"bytes"
	"context"
	"os"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

// This test uses session-local TEMP tables, never the database's permanent
// service-secret tables. Supply a disposable PostgreSQL 16 database URL.
func TestRekeyStoredSecretsPG16(t *testing.T) {
	dsn := os.Getenv("BAHIA_REKEY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BAHIA_REKEY_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn.Close(ctx)
	var version int
	require.NoError(t, conn.QueryRow(ctx, `SELECT current_setting('server_version_num')::integer`).Scan(&version))
	require.GreaterOrEqual(t, version, 160000)
	_, err = conn.Exec(ctx, `CREATE TEMP TABLE service_secrets (
		id uuid PRIMARY KEY, version integer NOT NULL, encrypted_value bytea NOT NULL,
		encryption_method varchar(20) NOT NULL);
		CREATE TEMP TABLE secret_versions (
		id uuid PRIMARY KEY, secret_id uuid NOT NULL, version integer NOT NULL,
		encrypted_value bytea NOT NULL, encryption_method varchar(20) NOT NULL);
		CREATE TEMP TABLE service_secret_data_keys (
		id uuid PRIMARY KEY, service_pubkey char(64) NOT NULL, wrapped_key text NOT NULL,
		created_at timestamptz NOT NULL DEFAULT now());
	`)
	require.NoError(t, err)
	serviceKey := nostr.Generate()
	legacy, err := NewEncryptor(serviceKey.Hex())
	require.NoError(t, err)
	keyer := localWrapKeyer{key: serviceKey}
	id := uuid.New()
	old, err := legacy.Encrypt("retained private value", domain.EncryptionAES256)
	require.NoError(t, err)
	newer, err := legacy.Encrypt("current private value", domain.EncryptionNIP44)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO service_secrets VALUES ($1,2,$2,'nip44')`, id, newer)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO secret_versions VALUES ($1,$2,1,$3,'aes256gcm'),($4,$2,2,$5,'nip44')`,
		uuid.New(), id, old, uuid.New(), newer)
	require.NoError(t, err)

	// Bounded failure must leave all three tables untouched.
	_, err = rekeyStoredSecrets(ctx, conn, legacy, keyer, serviceKey.Public(), 1)
	require.ErrorContains(t, err, "exceeds --max-rows")
	var keyCount int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM service_secret_data_keys`).Scan(&keyCount))
	require.Zero(t, keyCount)
	var method string
	require.NoError(t, conn.QueryRow(ctx, `SELECT encryption_method FROM service_secrets WHERE id=$1`, id).Scan(&method))
	require.Equal(t, "nip44", method)

	report, err := rekeyStoredSecrets(ctx, conn, legacy, keyer, serviceKey.Public(), 2)
	require.NoError(t, err)
	require.Equal(t, rekeyReport{Secrets: 1, Versions: 2}, report)
	var wrapped WrappedDataKey
	var pubkeyHex string
	require.NoError(t, conn.QueryRow(ctx, `SELECT id,service_pubkey,wrapped_key FROM service_secret_data_keys`).Scan(&wrapped.ID, &pubkeyHex, &wrapped.WrappedHex))
	require.Equal(t, serviceKey.Public().Hex(), pubkeyHex)
	wrapped.ServiceKey = serviceKey.Public()
	opened, err := openWrappedDataKey(ctx, keyer, wrapped, serviceKey.Public())
	require.NoError(t, err)
	loaded, err := loadSoleWrappedDataKey(ctx, conn, keyer, serviceKey.Public())
	require.NoError(t, err)
	require.Equal(t, opened.ID(), loaded.ID())
	_, err = loadSoleWrappedDataKey(ctx, conn, keyer, nostr.Generate().Public())
	require.Error(t, err)
	rows, err := conn.Query(ctx, `SELECT version,encrypted_value,encryption_method FROM secret_versions ORDER BY version`)
	require.NoError(t, err)
	for rows.Next() {
		var v int
		var cipher []byte
		var gotMethod string
		require.NoError(t, rows.Scan(&v, &cipher, &gotMethod))
		require.Equal(t, string(domain.EncryptionAES256V2), gotMethod)
		plain, openErr := opened.Open(id, v, cipher)
		require.NoError(t, openErr)
		if v == 1 {
			require.Equal(t, []byte("retained private value"), plain)
		} else {
			require.Equal(t, []byte("current private value"), plain)
		}
	}
	require.NoError(t, rows.Err())
	rows.Close()
	var current []byte
	require.NoError(t, conn.QueryRow(ctx, `SELECT encrypted_value,encryption_method FROM service_secrets WHERE id=$1`, id).Scan(&current, &method))
	require.Equal(t, string(domain.EncryptionAES256V2), method)
	plain, err := opened.Open(id, 2, current)
	require.NoError(t, err)
	require.Equal(t, []byte("current private value"), plain)
	require.False(t, bytes.Equal(newer, current))
	_, err = rekeyStoredSecrets(ctx, conn, legacy, keyer, serviceKey.Public(), 2)
	require.ErrorContains(t, err, "not fresh")
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM service_secret_data_keys`).Scan(&keyCount))
	require.Equal(t, 1, keyCount)

	// An unclassifiable retained version must not partially migrate a valid
	// current row or create a wrapped key.
	_, err = conn.Exec(ctx, `TRUNCATE service_secret_data_keys, secret_versions, service_secrets`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO service_secrets VALUES ($1,1,$2,'aes256gcm')`, id, old)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO secret_versions VALUES ($1,$2,1,$3,'mystery')`, uuid.New(), id, old)
	require.NoError(t, err)
	_, err = rekeyStoredSecrets(ctx, conn, legacy, keyer, serviceKey.Public(), 2)
	require.ErrorContains(t, err, "unclassifiable")
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM service_secret_data_keys`).Scan(&keyCount))
	require.Zero(t, keyCount)
	require.NoError(t, conn.QueryRow(ctx, `SELECT encryption_method FROM service_secrets WHERE id=$1`, id).Scan(&method))
	require.Equal(t, "aes256gcm", method)
}
