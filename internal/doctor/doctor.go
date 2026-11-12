// Package doctor runs the diagnostics of §18.3 for `sayso doctor` and the
// Diagnostics page. Each feature adds its checks.
package doctor

import (
	"context"
	"sync"
	"time"
)

// Result is the outcome of one check.
type Result struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	MS     int64  `json:"ms"`
}

// Check is one named diagnostic. It returns a short detail on success and
// an error with the reason on failure.
type Check struct {
	Name string
	Run  func(ctx context.Context) (string, error)
}

// Run runs the checks in parallel, each with a 10-second limit, and keeps
// their order.
func Run(ctx context.Context, checks []Check) []Result {
	out := make([]Result, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			start := time.Now()
			detail, err := c.Run(cctx)
			r := Result{Name: c.Name, OK: err == nil, Detail: detail, MS: time.Since(start).Milliseconds()}
			if err != nil {
				r.Detail = err.Error()
			}
			out[i] = r
		}()
	}
	wg.Wait()
	return out
}

// AllOK reports whether every check passed.
func AllOK(rs []Result) bool {
	for _, r := range rs {
		if !r.OK {
			return false
		}
	}
	return true
}
