package app

import (
	"context"
	"fmt"
	"testing"

	"go.temporal.io/api/serviceerror"
)

// The runner check failed in CI when the server ended the wait first: the
// error is a service error, not context.DeadlineExceeded, and ctx has not
// expired yet.
func TestTimedOutCoversEveryDeadline(t *testing.T) {
	live := context.Background()
	for name, err := range map[string]error{
		"client deadline": fmt.Errorf("get: %w", context.DeadlineExceeded),
		"server deadline": serviceerror.NewDeadlineExceeded("context deadline exceeded"),
	} {
		if !timedOut(live, err) {
			t.Errorf("%s: not a timeout", name)
		}
	}
	if timedOut(live, serviceerror.NewUnavailable("connection refused")) {
		t.Error("an unavailable server is not a timeout")
	}
}
