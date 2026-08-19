package signet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

const (
	defaultConnectAttemptTimeout = 15 * time.Second
	defaultReconnectMinBackoff   = time.Second
	defaultReconnectMaxBackoff   = 30 * time.Second
	defaultConnectionHeartbeat   = 30 * time.Second
	defaultReconnectJitter       = 0.2
)

// ManagedClient is the lifecycle surface required by ConnectionManager.
type ManagedClient interface {
	Connect(context.Context) error
	Ping(context.Context) error
	IsConnected() bool
	Close() error
}

// ConnectionManagerConfig controls bounded attempts and reconnect behavior.
type ConnectionManagerConfig struct {
	Name              string
	AttemptTimeout    time.Duration
	MinBackoff        time.Duration
	MaxBackoff        time.Duration
	HeartbeatInterval time.Duration
	JitterFraction    float64
	Logger            *slog.Logger
}

// ConnectionState is a point-in-time view of a managed Signet dependency.
type ConnectionState struct {
	Name        string
	Connected   bool
	LastError   string
	LastAttempt time.Time
	LastSuccess time.Time
}

// ConnectionManager owns repeated Signet connection lifetimes. Each attempt
// receives a fresh context without a deadline because ConnectBunker retains it
// after success; a separate timer cancels failed or stale attempts.
type ConnectionManager struct {
	client ManagedClient
	cfg    ConnectionManagerConfig

	mu      sync.RWMutex
	state   ConnectionState
	changes chan ConnectionState
}

func NewConnectionManager(client ManagedClient, cfg ConnectionManagerConfig) *ConnectionManager {
	if cfg.Name == "" {
		cfg.Name = "signet"
	}
	if cfg.AttemptTimeout <= 0 {
		if configured, ok := client.(interface{ ConnectAttemptTimeout() time.Duration }); ok {
			cfg.AttemptTimeout = configured.ConnectAttemptTimeout()
		}
		if cfg.AttemptTimeout <= 0 {
			cfg.AttemptTimeout = defaultConnectAttemptTimeout
		}
	}
	if cfg.MinBackoff <= 0 {
		cfg.MinBackoff = defaultReconnectMinBackoff
	}
	if cfg.MaxBackoff < cfg.MinBackoff {
		cfg.MaxBackoff = defaultReconnectMaxBackoff
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = defaultConnectionHeartbeat
	}
	if cfg.JitterFraction == 0 {
		cfg.JitterFraction = defaultReconnectJitter
	} else if cfg.JitterFraction < 0 {
		cfg.JitterFraction = 0
	}
	if cfg.JitterFraction > 1 {
		cfg.JitterFraction = 1
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &ConnectionManager{client: client, cfg: cfg, state: ConnectionState{Name: cfg.Name}, changes: make(chan ConnectionState, 16)}
}

func (m *ConnectionManager) Name() string { return "signet-" + m.cfg.Name }

func (m *ConnectionManager) State() ConnectionState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state := m.state
	if m.client != nil {
		state.Connected = m.client.IsConnected()
	}
	return state
}

// Changes reports connection state transitions for readiness-driven runners and tests.
func (m *ConnectionManager) Changes() <-chan ConnectionState { return m.changes }

func (m *ConnectionManager) Run(ctx context.Context) error {
	if m == nil || m.client == nil {
		return fmt.Errorf("Signet connection manager client is not configured")
	}
	backoff := m.cfg.MinBackoff
	for {
		if ctx.Err() != nil {
			_ = m.client.Close()
			return nil
		}
		cancelLifetime, err := m.connectAttempt(ctx)
		if err == nil {
			backoff = m.cfg.MinBackoff
			err = m.monitorConnection(ctx)
		}
		if cancelLifetime != nil {
			cancelLifetime()
		}
		_ = m.client.Close()
		if ctx.Err() != nil {
			return nil
		}
		m.recordFailure(err)
		m.cfg.Logger.Warn("Signet connection unavailable; retrying", "signer", m.cfg.Name, "error", err)
		if err := waitContext(ctx, m.jitter(backoff)); err != nil {
			return nil
		}
		if backoff < m.cfg.MaxBackoff {
			backoff *= 2
			if backoff > m.cfg.MaxBackoff {
				backoff = m.cfg.MaxBackoff
			}
		}
	}
}

func (m *ConnectionManager) connectAttempt(ctx context.Context) (context.CancelFunc, error) {
	m.mu.Lock()
	m.state.LastAttempt = time.Now().UTC()
	m.publishStateLocked()
	m.mu.Unlock()

	lifetimeCtx, cancelLifetime := deadlineFreeLifetime(ctx)
	result := make(chan error, 1)
	go func() {
		result <- m.client.Connect(lifetimeCtx)
	}()
	timer := time.NewTimer(m.cfg.AttemptTimeout)
	defer timer.Stop()
	select {
	case err := <-result:
		if err != nil {
			cancelLifetime()
			return nil, err
		}
		m.mu.Lock()
		m.state.Connected = true
		m.state.LastError = ""
		m.state.LastSuccess = time.Now().UTC()
		m.publishStateLocked()
		m.mu.Unlock()
		return cancelLifetime, nil
	case <-timer.C:
		cancelLifetime()
		// Do not start another attempt until this one has observed cancellation
		// and exited. This keeps exactly one Connect call active per manager.
		<-result
		return nil, fmt.Errorf("Signet connection attempt timed out after %s", m.cfg.AttemptTimeout)
	case <-ctx.Done():
		cancelLifetime()
		<-result
		return nil, ctx.Err()
	}
}

func deadlineFreeLifetime(parent context.Context) (context.Context, context.CancelFunc) {
	// ConnectBunker retains this context for the full subscription lifetime, so
	// parent values are preserved but its deadline/cancellation are detached.
	// Cancellation is then forwarded explicitly without copying the deadline.
	lifetime, cancelLifetime := context.WithCancel(context.WithoutCancel(parent))
	stopForwarding := context.AfterFunc(parent, cancelLifetime)
	return lifetime, func() {
		stopForwarding()
		cancelLifetime()
	}
}

func (m *ConnectionManager) monitorConnection(ctx context.Context) error {
	ticker := time.NewTicker(m.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, m.cfg.AttemptTimeout)
			err := m.client.Ping(pingCtx)
			cancel()
			if err != nil {
				return err
			}
		}
	}
}

func (m *ConnectionManager) recordFailure(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Connected = false
	if err != nil && !errors.Is(err, context.Canceled) {
		m.state.LastError = err.Error()
	}
	m.publishStateLocked()
}

func (m *ConnectionManager) publishStateLocked() {
	select {
	case m.changes <- m.state:
	default:
	}
}

func (m *ConnectionManager) jitter(base time.Duration) time.Duration {
	if m.cfg.JitterFraction == 0 {
		return base
	}
	delta := (rand.Float64()*2 - 1) * m.cfg.JitterFraction
	return time.Duration(float64(base) * (1 + delta))
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
