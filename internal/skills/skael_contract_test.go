//go:build contract

package skills_test

import (
	"testing"

	"github.com/alternayte/sayso/internal/skills/skaeltest"
)

func TestSkaelSeed(t *testing.T) {
	s := skaeltest.Start(t)
	if s.AgentKey == "" || s.ReporterKey == "" {
		t.Fatal("no keys")
	}
}
