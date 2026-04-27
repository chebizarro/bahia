package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgNotificationRepository is a PostgreSQL implementation of NotificationRepository.
type PgNotificationRepository struct {
	pool *pgxpool.Pool
}

// NewPgNotificationRepository creates a new PgNotificationRepository.
func NewPgNotificationRepository(pool *pgxpool.Pool) *PgNotificationRepository {
	return &PgNotificationRepository{pool: pool}
}

// --- Channels ---

func (r *PgNotificationRepository) CreateChannel(ctx context.Context, ch *domain.NotificationChannel) error {
	if ch.ID == uuid.Nil {
		ch.ID = uuid.New()
	}
	configJSON, _ := json.Marshal(ch.Config)
	filterJSON, _ := json.Marshal(ch.EventFilter)

	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_channels (id, name, channel_type, config, event_filter, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now(), now())
	`, ch.ID, ch.Name, string(ch.ChannelType), configJSON, filterJSON, ch.Enabled)
	if err != nil {
		return fmt.Errorf("creating notification channel: %w", err)
	}
	return nil
}

func (r *PgNotificationRepository) GetChannelByID(ctx context.Context, id uuid.UUID) (*domain.NotificationChannel, error) {
	ch := &domain.NotificationChannel{}
	var chType string
	var configJSON, filterJSON []byte

	err := r.pool.QueryRow(ctx, `
		SELECT id, name, channel_type, config, event_filter, enabled, created_at, updated_at
		FROM notification_channels WHERE id = $1
	`, id).Scan(&ch.ID, &ch.Name, &chType, &configJSON, &filterJSON, &ch.Enabled, &ch.CreatedAt, &ch.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting channel: %w", err)
	}
	ch.ChannelType = domain.ChannelType(chType)
	_ = json.Unmarshal(configJSON, &ch.Config)
	_ = json.Unmarshal(filterJSON, &ch.EventFilter)
	return ch, nil
}

func (r *PgNotificationRepository) ListChannels(ctx context.Context, enabledOnly bool) ([]domain.NotificationChannel, error) {
	query := `SELECT id, name, channel_type, config, event_filter, enabled, created_at, updated_at
		FROM notification_channels`
	if enabledOnly {
		query += ` WHERE enabled = true`
	}
	query += ` ORDER BY name`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing channels: %w", err)
	}
	defer rows.Close()

	var channels []domain.NotificationChannel
	for rows.Next() {
		var ch domain.NotificationChannel
		var chType string
		var configJSON, filterJSON []byte
		if err := rows.Scan(&ch.ID, &ch.Name, &chType, &configJSON, &filterJSON,
			&ch.Enabled, &ch.CreatedAt, &ch.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning channel: %w", err)
		}
		ch.ChannelType = domain.ChannelType(chType)
		_ = json.Unmarshal(configJSON, &ch.Config)
		_ = json.Unmarshal(filterJSON, &ch.EventFilter)
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

func (r *PgNotificationRepository) UpdateChannel(ctx context.Context, ch *domain.NotificationChannel) error {
	configJSON, _ := json.Marshal(ch.Config)
	filterJSON, _ := json.Marshal(ch.EventFilter)

	_, err := r.pool.Exec(ctx, `
		UPDATE notification_channels SET name = $1, channel_type = $2, config = $3,
			event_filter = $4, enabled = $5, updated_at = now()
		WHERE id = $6
	`, ch.Name, string(ch.ChannelType), configJSON, filterJSON, ch.Enabled, ch.ID)
	if err != nil {
		return fmt.Errorf("updating channel: %w", err)
	}
	return nil
}

func (r *PgNotificationRepository) DeleteChannel(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM notification_channels WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting channel: %w", err)
	}
	return nil
}

// --- Log ---

func (r *PgNotificationRepository) CreateLog(ctx context.Context, log *domain.NotificationLog) error {
	if log.ID == uuid.Nil {
		log.ID = uuid.New()
	}
	payloadJSON, _ := json.Marshal(log.Payload)

	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_log (id, channel_id, event_type, payload, status, attempts, last_error, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now(), now())
	`, log.ID, log.ChannelID, log.EventType, payloadJSON, string(log.Status), log.Attempts, log.LastError)
	if err != nil {
		return fmt.Errorf("creating notification log: %w", err)
	}
	return nil
}

func (r *PgNotificationRepository) UpdateLog(ctx context.Context, log *domain.NotificationLog) error {
	payloadJSON, _ := json.Marshal(log.Payload)

	_, err := r.pool.Exec(ctx, `
		UPDATE notification_log SET status = $1, attempts = $2, last_error = $3, payload = $4, updated_at = now()
		WHERE id = $5
	`, string(log.Status), log.Attempts, log.LastError, payloadJSON, log.ID)
	if err != nil {
		return fmt.Errorf("updating notification log: %w", err)
	}
	return nil
}

func (r *PgNotificationRepository) ListLogsByChannel(ctx context.Context, channelID uuid.UUID, limit int) ([]domain.NotificationLog, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, channel_id, event_type, payload, status, attempts, last_error, created_at, updated_at
		FROM notification_log WHERE channel_id = $1
		ORDER BY created_at DESC LIMIT $2
	`, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing logs: %w", err)
	}
	defer rows.Close()
	return scanNotificationLogs(rows)
}

func (r *PgNotificationRepository) ListRecentLogs(ctx context.Context, limit int) ([]domain.NotificationLog, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, channel_id, event_type, payload, status, attempts, last_error, created_at, updated_at
		FROM notification_log
		ORDER BY created_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing recent logs: %w", err)
	}
	defer rows.Close()
	return scanNotificationLogs(rows)
}

func (r *PgNotificationRepository) ListRetryable(ctx context.Context, maxAttempts int) ([]domain.NotificationLog, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, channel_id, event_type, payload, status, attempts, last_error, created_at, updated_at
		FROM notification_log
		WHERE status IN ('retrying', 'pending') AND attempts < $1
		ORDER BY created_at ASC LIMIT 100
	`, maxAttempts)
	if err != nil {
		return nil, fmt.Errorf("listing retryable: %w", err)
	}
	defer rows.Close()
	return scanNotificationLogs(rows)
}

func scanNotificationLogs(rows pgx.Rows) ([]domain.NotificationLog, error) {
	var logs []domain.NotificationLog
	for rows.Next() {
		var l domain.NotificationLog
		var status string
		var payloadJSON []byte
		if err := rows.Scan(&l.ID, &l.ChannelID, &l.EventType, &payloadJSON,
			&status, &l.Attempts, &l.LastError, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning log: %w", err)
		}
		l.Status = domain.NotificationStatus(status)
		_ = json.Unmarshal(payloadJSON, &l.Payload)
		logs = append(logs, l)
	}
	return logs, rows.Err()
}
