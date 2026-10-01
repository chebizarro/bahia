package service

import "time"

// SetRelayProjectionCacheClock replaces the cache's clock in tests.
func SetRelayProjectionCacheClock(c *RelayProjectionCache, now func() time.Time) {
	c.now = now
}
