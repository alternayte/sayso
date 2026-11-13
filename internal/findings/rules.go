// Package findings holds the ledger of a review: the blocking rule, the
// fingerprint, matching of new findings to the ledger and status changes
// (§9).
package findings

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/alternayte/sayso/internal/runner"
)

// Statuses of a finding (§9.4).
const (
	StatusOpen            = "open"
	StatusFixed           = "fixed"
	StatusWithdrawn       = "withdrawn"
	StatusDismissed       = "dismissed"
	StatusConceded        = "conceded"
	StatusResolvedByHuman = "resolved_by_human"
)

// Verdicts of a round.
const (
	VerdictBlocking         = "blocking"
	VerdictChangesSuggested = "changes_suggested"
	VerdictLooksGood        = "looks_good"
)

var severityRank = map[string]int{"blocker": 0, "major": 1, "minor": 2, "nit": 3}

// SeverityRank orders severities from blocker (0) to nit (3).
func SeverityRank(s string) int {
	if r, ok := severityRank[s]; ok {
		return r
	}
	return len(severityRank)
}

var severities = []string{"blocker", "major", "minor", "nit"}

// ApplyChallenge applies a challenge outcome to a candidate's severity
// (§9.1 as the execution and challenge spec changes it): refuted is
// withdrawn; downgraded drops one level, and a downgraded nit is withdrawn.
func ApplyChallenge(severity string, c *runner.Challenge) (string, bool) {
	if c == nil {
		return severity, false
	}
	switch c.Outcome {
	case "refuted":
		return severity, true
	case "downgraded":
		i := SeverityRank(severity)
		if i+1 >= len(severities) {
			return severity, true
		}
		return severities[i+1], false
	}
	return severity, false
}

// Blocking applies the rule of §9.1: severity blocker, evidence that is not
// reasoning alone, confirmed by the challenge stage, and an overlay that
// allows blocking.
func Blocking(severity, evidenceKind string, challenge *runner.Challenge, overlayBlock bool) bool {
	return severity == "blocker" &&
		(evidenceKind == "test" || evidenceKind == "trace" || evidenceKind == "call_path") &&
		challenge != nil && challenge.Outcome == "confirmed" &&
		overlayBlock
}

// Verdict is blocking when an open finding blocks, changes_suggested when
// an open finding above nit is left, and looks_good otherwise.
func Verdict(open []Finding) string {
	v := VerdictLooksGood
	for _, f := range open {
		if f.Blocking {
			return VerdictBlocking
		}
		if f.Severity != "nit" {
			v = VerdictChangesSuggested
		}
	}
	return v
}

var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "in": true, "on": true, "at": true, "to": true, "for": true, "and": true,
	"or": true, "is": true, "are": true, "be": true, "by": true, "with": true, "from": true, "this": true, "that": true,
	"it": true, "as": true, "not": true, "no": true, "when": true, "if": true,
}

// NormalizeTitle lowercases the title, removes punctuation, digits and stop
// words, and sorts the words (§9.2).
func NormalizeTitle(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case unicode.IsLetter(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	var words []string
	for _, w := range strings.Fields(b.String()) {
		if !stopWords[w] {
			words = append(words, w)
		}
	}
	slices.Sort(words)
	return strings.Join(words, " ")
}

// NormalizePath removes a leading "./" and "/" and uses forward slashes.
func NormalizePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	for strings.HasPrefix(p, "./") || strings.HasPrefix(p, "/") {
		p = strings.TrimPrefix(strings.TrimPrefix(p, "./"), "/")
	}
	return p
}

// Fingerprint is sha256(repository_id, category, normalized path,
// normalized title) (§9.2).
func Fingerprint(repositoryID uuid.UUID, category, path, title string) string {
	h := sha256.New()
	for _, part := range []string{repositoryID.String(), strings.ToLower(strings.TrimSpace(category)), NormalizePath(path), NormalizeTitle(title)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// trigrams returns the trigram set of s as pg_trgm builds it: each word is
// padded with two spaces in front and one behind.
func trigrams(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(s) {
		p := []rune("  " + w + " ")
		for i := 0; i+3 <= len(p); i++ {
			out[string(p[i:i+3])] = true
		}
	}
	return out
}

// Similarity is the trigram similarity of two normalized titles, as
// pg_trgm's similarity(): shared trigrams over all distinct trigrams.
func Similarity(a, b string) float64 {
	ta, tb := trigrams(a), trigrams(b)
	if len(ta) == 0 && len(tb) == 0 {
		return 0
	}
	shared := 0
	for t := range ta {
		if tb[t] {
			shared++
		}
	}
	return float64(shared) / float64(len(ta)+len(tb)-shared)
}
