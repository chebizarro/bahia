package controlplane

import (
	"context"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
)

const (
	defaultContextVMResultPublishTimeout = 60 * time.Second
	defaultContextVMResultInitialBackoff = 250 * time.Millisecond
	defaultContextVMResultMaxBackoff     = 5 * time.Second
)

type contextVMResultRetryConfig struct {
	timeout        time.Duration
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

func defaultContextVMResultRetryConfig() contextVMResultRetryConfig {
	return contextVMResultRetryConfig{
		timeout:        defaultContextVMResultPublishTimeout,
		initialBackoff: defaultContextVMResultInitialBackoff,
		maxBackoff:     defaultContextVMResultMaxBackoff,
	}
}

type detailedNostrEventPublisher interface {
	PublishWithResults(ctx context.Context, ev nostr.Event) ([]nostrpool.PublishResult, error)
}

type contextVMResultPublishFailure struct {
	attempts int
	outcomes []string
	cause    error
}

func (e *contextVMResultPublishFailure) Error() string {
	return fmt.Sprintf("terminal ContextVM result was not accepted after %d attempts: %v", e.attempts, e.cause)
}

func newContextVMResultPublishFailure(attempt int, outcomes []string, err error) *contextVMResultPublishFailure {
	if err == nil {
		err = fmt.Errorf("no relay accepted event")
	}
	prefixed := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		prefixed = append(prefixed, fmt.Sprintf("attempt=%d %s", attempt, outcome))
	}
	return &contextVMResultPublishFailure{attempts: attempt, outcomes: prefixed, cause: err}
}

func continueContextVMResultRetry(ctx context.Context, publisher NostrEventPublisher, event nostr.Event, cfg contextVMResultRetryConfig, failure *contextVMResultPublishFailure) error {
	if cfg.initialBackoff <= 0 {
		cfg.initialBackoff = defaultContextVMResultInitialBackoff
	}
	if cfg.maxBackoff < cfg.initialBackoff {
		cfg.maxBackoff = cfg.initialBackoff
	}

	backoff := cfg.initialBackoff
	for {
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			failure.cause = fmt.Errorf("%w: %v", ctx.Err(), failure.cause)
			return failure
		case <-timer.C:
		}

		failure.attempts++
		published, outcomes, err := publishContextVMResultAttempt(ctx, publisher, event)
		if published > 0 {
			return nil
		}
		for _, outcome := range outcomes {
			failure.outcomes = append(failure.outcomes, fmt.Sprintf("attempt=%d %s", failure.attempts, outcome))
		}
		if err == nil {
			err = fmt.Errorf("no relay accepted event")
		}
		failure.cause = err

		backoff *= 2
		if backoff > cfg.maxBackoff {
			backoff = cfg.maxBackoff
		}
	}
}

func publishContextVMResultAttempt(ctx context.Context, publisher NostrEventPublisher, event nostr.Event) (int, []string, error) {
	if detailed, ok := publisher.(detailedNostrEventPublisher); ok {
		results, err := detailed.PublishWithResults(ctx, event)
		published := 0
		outcomes := make([]string, 0, len(results))
		for _, result := range results {
			accepted := result.Accepted || result.IsDuplicate()
			if accepted {
				published++
			}
			outcome := fmt.Sprintf("relay=%s accepted=%t", result.RelayURL, accepted)
			if result.Reason != "" {
				outcome += " reason=" + result.Reason
			}
			if result.Error != nil {
				outcome += " error=" + result.Error.Error()
			}
			outcomes = append(outcomes, outcome)
		}
		return published, outcomes, err
	}
	published, err := publisher.Publish(ctx, event)
	outcome := fmt.Sprintf("accepted_relays=%d", published)
	if err != nil {
		outcome += " error=" + err.Error()
	}
	return published, []string{outcome}, err
}
