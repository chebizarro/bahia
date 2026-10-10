package secrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/openagentsinc/bahia/internal/domain"
)

type rekeyReport struct {
	Secrets  int `json:"secrets"`
	Versions int `json:"versions"`
}

type legacySecretCipher interface {
	Decrypt([]byte, domain.EncryptionMethod) (string, error)
}

type rekeyRow struct {
	id         uuid.UUID
	secretID   uuid.UUID
	version    int
	ciphertext []byte
	method     domain.EncryptionMethod
}

// rekeyStoredSecrets is deliberately package-private until all production
// readers and writers support v2. No application or command may call it yet.
// One serializable transaction replaces both historical tables and records
// the wrapped key, or changes nothing.
func rekeyStoredSecrets(ctx context.Context, conn *pgx.Conn, legacy legacySecretCipher, fenced nostr.Keyer, service nostr.PubKey, maxRows int) (rekeyReport, error) {
	if conn == nil || legacy == nil || fenced == nil || service == nostr.ZeroPK || maxRows < 1 || maxRows > 100000 {
		return rekeyReport{}, errors.New("invalid offline service-secret rekey configuration")
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadWrite})
	if err != nil {
		return rekeyReport{}, errors.New("begin offline service-secret rekey transaction")
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `LOCK TABLE service_secrets, secret_versions, service_secret_data_keys IN ACCESS EXCLUSIVE MODE`); err != nil {
		return rekeyReport{}, errors.New("lock service-secret tables for offline rekey")
	}
	var priorKey bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM service_secret_data_keys)`).Scan(&priorKey); err != nil || priorKey {
		return rekeyReport{}, errors.New("service-secret data-key state is not fresh; rekey refused")
	}
	current, err := loadRekeyRows(ctx, tx, `SELECT id, id, version, encrypted_value, encryption_method FROM service_secrets ORDER BY id LIMIT $1`, maxRows)
	if err != nil {
		return rekeyReport{}, err
	}
	history, err := loadRekeyRows(ctx, tx, `SELECT id, secret_id, version, encrypted_value, encryption_method FROM secret_versions ORDER BY id LIMIT $1`, maxRows)
	if err != nil {
		return rekeyReport{}, err
	}
	if len(current) == 0 && len(history) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return rekeyReport{}, errors.New("commit empty offline service-secret rekey")
		}
		return rekeyReport{}, nil
	}
	byVersion := make(map[uuid.UUID]map[int]rekeyRow, len(current))
	currentIDs := make(map[uuid.UUID]struct{}, len(current))
	for _, row := range current {
		currentIDs[row.id] = struct{}{}
	}
	for _, row := range history {
		if _, ok := currentIDs[row.secretID]; !ok {
			return rekeyReport{}, errors.New("retained secret version has no current service secret")
		}
		if byVersion[row.secretID] == nil {
			byVersion[row.secretID] = make(map[int]rekeyRow)
		}
		if _, exists := byVersion[row.secretID][row.version]; exists {
			return rekeyReport{}, errors.New("duplicate retained service-secret version")
		}
		byVersion[row.secretID][row.version] = row
	}
	for _, row := range current {
		match, ok := byVersion[row.id][row.version]
		if !ok || row.method != match.method || !bytes.Equal(row.ciphertext, match.ciphertext) {
			return rekeyReport{}, errors.New("current service-secret payload does not match its retained version")
		}
	}
	wrapped, key, err := newWrappedDataKey(ctx, fenced, service)
	if err != nil {
		return rekeyReport{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO service_secret_data_keys (id, service_pubkey, wrapped_key) VALUES ($1,$2,$3)`, wrapped.ID, service.Hex(), wrapped.WrappedCiphertext); err != nil {
		return rekeyReport{}, errors.New("store wrapped service-secret data key")
	}
	for _, row := range history {
		plain, err := legacy.Decrypt(row.ciphertext, row.method)
		if err != nil {
			return rekeyReport{}, errors.New("legacy secret-version decryption failed; transaction rolled back")
		}
		sealed, err := key.Seal(row.secretID, row.version, []byte(plain))
		if err != nil {
			return rekeyReport{}, errors.New("seal secret version with random data key")
		}
		result, err := tx.Exec(ctx, `UPDATE secret_versions SET encrypted_value=$1, encryption_method=$2 WHERE id=$3 AND encrypted_value=$4 AND encryption_method=$5`, sealed, string(domain.EncryptionAES256V2), row.id, row.ciphertext, string(row.method))
		if err != nil || result.RowsAffected() != 1 {
			return rekeyReport{}, errors.New("secret-version rekey update failed; transaction rolled back")
		}
	}
	for _, row := range current {
		plain, err := legacy.Decrypt(row.ciphertext, row.method)
		if err != nil {
			return rekeyReport{}, errors.New("current service-secret decryption failed; transaction rolled back")
		}
		sealed, err := key.Seal(row.id, row.version, []byte(plain))
		if err != nil {
			return rekeyReport{}, errors.New("seal current service secret with random data key")
		}
		result, err := tx.Exec(ctx, `UPDATE service_secrets SET encrypted_value=$1, encryption_method=$2 WHERE id=$3 AND encrypted_value=$4 AND encryption_method=$5`, sealed, string(domain.EncryptionAES256V2), row.id, row.ciphertext, string(row.method))
		if err != nil || result.RowsAffected() != 1 {
			return rekeyReport{}, errors.New("current service-secret rekey update failed; transaction rolled back")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return rekeyReport{}, errors.New("commit offline service-secret rekey")
	}
	return rekeyReport{Secrets: len(current), Versions: len(history)}, nil
}

func loadRekeyRows(ctx context.Context, tx pgx.Tx, sql string, maxRows int) ([]rekeyRow, error) {
	rows, err := tx.Query(ctx, sql, maxRows+1)
	if err != nil {
		return nil, errors.New("read offline service-secret rekey rows")
	}
	defer rows.Close()
	result := make([]rekeyRow, 0)
	for rows.Next() {
		if len(result) == maxRows {
			return nil, fmt.Errorf("service-secret rekey exceeds --max-rows %d", maxRows)
		}
		var row rekeyRow
		var method string
		if err := rows.Scan(&row.id, &row.secretID, &row.version, &row.ciphertext, &method); err != nil {
			return nil, errors.New("scan offline service-secret rekey row")
		}
		row.method = domain.EncryptionMethod(method)
		if row.id == uuid.Nil || row.secretID == uuid.Nil || row.version <= 0 || len(row.ciphertext) == 0 || len(row.ciphertext) > maxServiceSecretBytes+64 ||
			(row.method != domain.EncryptionNIP44 && row.method != domain.EncryptionAES256) {
			return nil, errors.New("unclassifiable or already migrated service-secret row; transaction rolled back")
		}
		result = append(result, row)
	}
	if rows.Err() != nil {
		return nil, errors.New("iterate offline service-secret rekey rows")
	}
	return result, nil
}
