package models

import (
	"context"
	"errors"
	"sync"
	"time"
)

// LimitWait is how long the proxy waits for provider capacity before it
// answers 429 (§7.3).
const LimitWait = 60 * time.Second

// ErrNoCapacity means the provider limits stayed full for LimitWait.
var ErrNoCapacity = errors.New("provider limits are full")

// Clock is the time source of the limits. Tests replace it.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type wallClock struct{}

func (wallClock) Now() time.Time                         { return time.Now() }
func (wallClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// limiter enforces max_concurrency and tokens_per_minute per provider in
// this API process.
type limiter struct {
	clock Clock
	mu    sync.Mutex
	by    map[string]*providerLimit
	wake  chan struct{}
}

type spend struct {
	at     time.Time
	tokens int64
}

type providerLimit struct {
	inFlight int
	window   []*spend
}

func newLimiter() *limiter {
	return &limiter{clock: wallClock{}, by: map[string]*providerLimit{}, wake: make(chan struct{})}
}

// SetClock replaces the clock of the provider limits.
func (s *Service) SetClock(c Clock) { s.limits.clock = c }

// grant is one admitted request. Settle sets the real token count.
type grant struct {
	l    *limiter
	name string
	sp   *spend
}

func (g *grant) Settle(tokens int64) {
	g.l.mu.Lock()
	g.sp.tokens = tokens
	g.l.mu.Unlock()
}

func (g *grant) Release() {
	g.l.mu.Lock()
	g.l.by[g.name].inFlight--
	close(g.l.wake)
	g.l.wake = make(chan struct{})
	g.l.mu.Unlock()
}

// acquire waits until the provider has a free request slot and token
// budget for the estimate, for at most LimitWait.
func (l *limiter) acquire(ctx context.Context, c Config, estimate int64) (*grant, error) {
	deadline := l.clock.Now().Add(LimitWait)
	for {
		l.mu.Lock()
		p := l.by[c.Name]
		if p == nil {
			p = &providerLimit{}
			l.by[c.Name] = p
		}
		now := l.clock.Now()
		cut := now.Add(-time.Minute)
		var used int64
		kept := p.window[:0]
		var oldest time.Time
		for _, sp := range p.window {
			if sp.at.After(cut) {
				kept = append(kept, sp)
				used += sp.tokens
				if oldest.IsZero() || sp.at.Before(oldest) {
					oldest = sp.at
				}
			}
		}
		p.window = kept
		slot := c.MaxConcurrency == 0 || p.inFlight < c.MaxConcurrency
		tokens := c.TokensPerMinute == 0 || used+estimate <= int64(c.TokensPerMinute)
		if slot && tokens {
			p.inFlight++
			sp := &spend{at: now, tokens: estimate}
			p.window = append(p.window, sp)
			l.mu.Unlock()
			return &grant{l: l, name: c.Name, sp: sp}, nil
		}
		wake := l.wake
		l.mu.Unlock()
		if !now.Before(deadline) {
			return nil, ErrNoCapacity
		}
		next := deadline.Sub(now)
		if !tokens && !oldest.IsZero() {
			if d := oldest.Add(time.Minute).Sub(now); d > 0 && d < next {
				next = d
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		case <-l.clock.After(next):
		}
	}
}
