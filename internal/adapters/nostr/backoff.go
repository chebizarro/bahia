// Package nostr provides Nostr relay integration for publishing and subscribing to events.
package nostr

import (
	"math/rand"
	"time"
)

// Backoff implements exponential backoff with jitter for reconnection attempts.
type Backoff struct {
	// Initial delay (default: 1s).
	Initial time.Duration
	// Maximum delay (default: 2m).
	Max time.Duration
	// Multiplier for each attempt (default: 2.0).
	Multiplier float64
	// Jitter factor (0.0-1.0) - adds randomness to prevent thundering herd (default: 0.5).
	Jitter float64

	attempt int
}

// DefaultBackoff returns a Backoff with sensible defaults.
func DefaultBackoff() *Backoff {
	return &Backoff{
		Initial:    time.Second,
		Max:        2 * time.Minute,
		Multiplier: 2.0,
		Jitter:     0.5,
	}
}

// Next returns the next backoff duration and increments the attempt counter.
func (b *Backoff) Next() time.Duration {
	if b.Initial == 0 {
		b.Initial = time.Second
	}
	if b.Max == 0 {
		b.Max = 2 * time.Minute
	}
	if b.Multiplier == 0 {
		b.Multiplier = 2.0
	}
	if b.Jitter < 0 {
		b.Jitter = 0
	}

	// Calculate base delay with exponential increase.
	delay := float64(b.Initial) * pow(b.Multiplier, b.attempt)
	if delay > float64(b.Max) {
		delay = float64(b.Max)
	}

	// Add jitter: +/- jitter% of the delay.
	jitterRange := delay * b.Jitter
	jitter := (rand.Float64()*2 - 1) * jitterRange // -jitterRange to +jitterRange
	delay += jitter

	// Ensure delay stays within the configured bounds after jitter.
	if delay < float64(b.Initial)/2 {
		delay = float64(b.Initial) / 2
	}
	if delay > float64(b.Max) {
		delay = float64(b.Max)
	}

	b.attempt++
	return time.Duration(delay)
}

// Reset resets the attempt counter (call on successful connection).
func (b *Backoff) Reset() {
	b.attempt = 0
}

// Attempt returns the current attempt number.
func (b *Backoff) Attempt() int {
	return b.attempt
}

// pow calculates base^exp for float64.
func pow(base float64, exp int) float64 {
	result := 1.0
	for i := 0; i < exp; i++ {
		result *= base
	}
	return result
}
