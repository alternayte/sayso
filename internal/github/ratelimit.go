package github

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Priority orders GitHub calls when the rate limit is low (§6.1).
type Priority int

const (
	// High: check run updates, command replies, review publishing, sign-in.
	High Priority = iota
	// Low: reconciler listings and summary edits during a round.
	Low
)

// Clock is the time source of the rate limiter. Tests replace it.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// RealClock is the wall clock.
var RealClock Clock = realClock{}

// lowShare is the remaining share below which low-priority calls wait.
const lowShare = 0.10

type budget struct {
	limit, remaining int
	reset            time.Time
	blockedUntil     time.Time // secondary limit or retry-after
}

// RateLimits tracks the primary and secondary rate limits of each
// installation (REQ-GH-010). One value is shared by all clients of a process.
type RateLimits struct {
	Clock Clock
	mu    sync.Mutex
	by    map[int64]*budget
}

// NewRateLimits returns limits that use clock.
func NewRateLimits(clock Clock) *RateLimits {
	return &RateLimits{Clock: clock, by: map[int64]*budget{}}
}

// wait blocks until a call with priority p may go to installation inst.
func (l *RateLimits) wait(ctx context.Context, inst int64, p Priority) error {
	if l == nil {
		return nil
	}
	for {
		l.mu.Lock()
		b := l.by[inst]
		now := l.Clock.Now()
		var until time.Time
		if b != nil {
			if now.Before(b.blockedUntil) {
				until = b.blockedUntil
			}
			if p == Low && b.limit > 0 && float64(b.remaining) < lowShare*float64(b.limit) && now.Before(b.reset) && b.reset.After(until) {
				until = b.reset
			}
		}
		l.mu.Unlock()
		if until.IsZero() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-l.Clock.After(until.Sub(now)):
		}
	}
}

// observe reads the rate limit headers of a response.
func (l *RateLimits) observe(inst int64, resp *http.Response) {
	if l == nil || resp == nil {
		return
	}
	h := resp.Header
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.by[inst]
	if b == nil {
		b = &budget{}
		l.by[inst] = b
	}
	now := l.Clock.Now()
	if v, err := strconv.Atoi(h.Get("X-Ratelimit-Limit")); err == nil {
		b.limit = v
	}
	if v, err := strconv.Atoi(h.Get("X-Ratelimit-Remaining")); err == nil {
		b.remaining = v
	}
	if v, err := strconv.ParseInt(h.Get("X-Ratelimit-Reset"), 10, 64); err == nil {
		b.reset = time.Unix(v, 0)
	}
	if v, err := strconv.Atoi(h.Get("Retry-After")); err == nil {
		b.blockedUntil = now.Add(time.Duration(v) * time.Second)
	} else if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) && b.remaining == 0 && !b.reset.IsZero() {
		b.blockedUntil = b.reset
	}
}
