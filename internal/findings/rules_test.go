package findings

import (
	"testing"

	"github.com/google/uuid"

	"github.com/alternayte/sayso/internal/runner"
)

func TestSCN_FND_001_BlockingRule(t *testing.T) {
	confirmed := &runner.Challenge{Outcome: "confirmed"}
	cases := []struct {
		severity, evidence string
		challenge          *runner.Challenge
		block, want        bool
	}{
		{"blocker", "test", confirmed, true, true},
		{"blocker", "trace", confirmed, true, true},
		{"blocker", "call_path", confirmed, true, true},
		{"blocker", "reasoning", confirmed, true, false},
		{"major", "test", confirmed, true, false},
		{"minor", "test", confirmed, true, false},
		{"nit", "test", confirmed, true, false},
		{"blocker", "test", nil, true, false},
		{"blocker", "test", &runner.Challenge{Outcome: "refuted"}, true, false},
		{"blocker", "test", &runner.Challenge{Outcome: "downgraded"}, true, false},
		{"blocker", "test", confirmed, false, false},
	}
	for _, c := range cases {
		if got := Blocking(c.severity, c.evidence, c.challenge, c.block); got != c.want {
			t.Errorf("Blocking(%s, %s, %v, block=%v) = %v", c.severity, c.evidence, c.challenge, c.block, got)
		}
	}
}

func TestSCN_FND_002_Fingerprint(t *testing.T) {
	repo := uuid.MustParse("0190a000-0000-7000-8000-0000000000a1")
	a := Fingerprint(repo, "correctness", "internal/money/round.go", "Rounding drops 1 cent on negative amounts")
	for _, title := range []string{
		"rounding drops 2 cent on negative amounts!",
		"Negative amounts: rounding drops 1 cent",
		"ROUNDING DROPS CENT, ON NEGATIVE AMOUNTS (3)",
	} {
		if b := Fingerprint(repo, "correctness", "./internal/money/round.go", title); b != a {
			t.Errorf("title %q gives another fingerprint", title)
		}
	}
	if Fingerprint(repo, "correctness", "internal/money/format.go", "Rounding drops 1 cent on negative amounts") == a {
		t.Error("another path gives the same fingerprint")
	}
}

func TestSimilarity(t *testing.T) {
	if s := Similarity(NormalizeTitle("Rounding drops a cent"), NormalizeTitle("rounding drops cent")); s != 1 {
		t.Errorf("same words: %v", s)
	}
	if s := Similarity(NormalizeTitle("Rounding drops a cent on refunds"), NormalizeTitle("Rounding drops a cent")); s < 0.6 {
		t.Errorf("close titles: %v", s)
	}
	if s := Similarity(NormalizeTitle("Rounding drops a cent"), NormalizeTitle("Missing index on orders")); s >= 0.6 {
		t.Errorf("other titles: %v", s)
	}
}

func TestVerdict(t *testing.T) {
	if v := Verdict(nil); v != VerdictLooksGood {
		t.Error(v)
	}
	if v := Verdict([]Finding{{Severity: "nit"}}); v != VerdictLooksGood {
		t.Error(v)
	}
	if v := Verdict([]Finding{{Severity: "nit"}, {Severity: "minor"}}); v != VerdictChangesSuggested {
		t.Error(v)
	}
	if v := Verdict([]Finding{{Severity: "minor"}, {Severity: "blocker", Blocking: true}}); v != VerdictBlocking {
		t.Error(v)
	}
}
