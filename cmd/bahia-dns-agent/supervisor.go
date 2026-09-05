package main

import (
	"context"
	"math/rand"
	"time"
)

type restartPolicy struct {
	Initial       time.Duration
	Maximum       time.Duration
	HealthyPeriod time.Duration
	Jitter        float64
}

func defaultRestartPolicy() restartPolicy {
	return restartPolicy{Initial: time.Second, Maximum: time.Minute, HealthyPeriod: time.Minute, Jitter: 0.2}
}

func (p restartPolicy) next(base, runDuration time.Duration, random float64) (wait, nextBase time.Duration) {
	if p.Initial <= 0 {
		p.Initial = time.Second
	}
	if p.Maximum < p.Initial {
		p.Maximum = p.Initial
	}
	if base < p.Initial || runDuration >= p.HealthyPeriod {
		base = p.Initial
	}
	if random < 0 {
		random = 0
	}
	if random > 1 {
		random = 1
	}
	factor := 1 + p.Jitter*(2*random-1)
	wait = time.Duration(float64(base) * factor)
	if wait < 0 {
		wait = 0
	}
	nextBase = base * 2
	if nextBase > p.Maximum || nextBase < base {
		nextBase = p.Maximum
	}
	return wait, nextBase
}

func supervise(ctx context.Context, run func(context.Context) error, policy restartPolicy, report func(error, time.Duration)) error {
	base := policy.Initial
	for {
		started := time.Now()
		err := run(ctx)
		if ctx.Err() != nil {
			return nil
		}
		wait, nextBase := policy.next(base, time.Since(started), rand.Float64())
		if report != nil {
			report(err, wait)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
		base = nextBase
	}
}
