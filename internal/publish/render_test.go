package publish

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/alternayte/sayso/internal/findings"
	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/runner"
)

func TestSCN_PUB_004_CheckConclusion(t *testing.T) {
	cases := []struct{ mode, status, verdict, want string }{
		{"gate", "done", findings.VerdictBlocking, "failure"},
		{"comment", "done", findings.VerdictBlocking, "neutral"},
		{"gate", "done", findings.VerdictChangesSuggested, "neutral"},
		{"comment", "done", findings.VerdictChangesSuggested, "neutral"},
		{"gate", "done", findings.VerdictLooksGood, "success"},
		{"comment", "done", findings.VerdictLooksGood, "success"},
		{"gate", "failed", "", "neutral"},
		{"gate", "budget_exceeded", "", "neutral"},
		{"gate", "cancelled", "", "neutral"},
		{"gate", "failed", findings.VerdictBlocking, "neutral"},
	}
	for _, c := range cases {
		if got := Conclusion(c.mode, c.status, c.verdict); got != c.want {
			t.Errorf("Conclusion(%s, %s, %s) = %s, want %s", c.mode, c.status, c.verdict, got, c.want)
		}
	}
}

func TestSafeNeutralizesMentionsAndHTML(t *testing.T) {
	got := Safe("Ask @octo-admin and @acme/platform. <script>alert(1)</script> mail a@b.c `@x`")
	for _, want := range []string{"`@\u2060octo-admin`", "`@\u2060acme/platform`", "&lt;script&gt;", "a@\u2060b.c", "`@\u2060x`"} {
		if !strings.Contains(got, want) {
			t.Errorf("Safe: %q has no %q", got, want)
		}
	}
	if strings.Contains(got, "<script>") || strings.Contains(got, " @octo-admin") {
		t.Errorf("Safe: %q", got)
	}
}

func TestDiffHunks(t *testing.T) {
	patch := "@@ -1,3 +1,4 @@\n package money\n-old\n+new\n+added\n ctx\n@@ -20,2 +21,2 @@\n a\n+b"
	got := diffHunks(githubFiles{{"internal/money/round.go", patch}}.files())["internal/money/round.go"]
	want := []hunk{{1, 4}, {21, 22}}
	if !slices.Equal(got, want) {
		t.Fatalf("hunks %v, want %v", got, want)
	}
}

// SCN-PUB-009: a range across hunks is clipped to one hunk, and a clipped
// comment has no suggestion block.
func TestSCN_PUB_009_RangeStaysInOneHunk(t *testing.T) {
	hs := []hunk{{8, 15}, {35, 45}}
	cases := []struct {
		start, end, from, to int
		clipped, ok          bool
	}{
		{10, 40, 35, 40, true, true},
		{10, 12, 10, 12, false, true},
		{10, 20, 10, 15, true, true},
		{16, 30, 0, 0, false, false},
	}
	for _, c := range cases {
		from, to, clipped, ok := anchor(hs, c.start, c.end)
		if from != c.from || to != c.to || clipped != c.clipped || ok != c.ok {
			t.Errorf("anchor(%d-%d) = %d-%d %v %v", c.start, c.end, from, to, clipped, ok)
		}
	}
	f := findings.Finding{Seq: 1, Severity: "major", Title: "t", StartLine: 10, EndLine: 40, Suggestion: "x := 1"}
	if body := RenderFinding(f, true); strings.Contains(body, "```suggestion") {
		t.Errorf("clipped comment has a suggestion block:\n%s", body)
	}
	if body := RenderFinding(f, false); !strings.Contains(body, "```suggestion") {
		t.Errorf("comment has no suggestion block:\n%s", body)
	}
}

type githubFile struct{ name, patch string }
type githubFiles []githubFile

func (g githubFiles) files() []github.PullFile {
	out := make([]github.PullFile, 0, len(g))
	for _, f := range g {
		out = append(out, github.PullFile{Filename: f.name, Patch: f.patch})
	}
	return out
}

// SCN-PUB-010: findings without an inline comment become annotations.
func TestSCN_PUB_010_AnnotationsForFindingsWithoutComment(t *testing.T) {
	mk := func(seq int, sev, path string, start, end int) findings.Finding {
		return findings.Finding{ID: uuid.New(), Seq: seq, Severity: sev, Title: "t", Path: path, StartLine: start, EndLine: end, Status: findings.StatusOpen}
	}
	inDiff := mk(1, "major", "a.go", 3, 3)
	overLimit := mk(2, "minor", "a.go", 4, 4)
	offDiff := mk(3, "blocker", "a.go", 90, 92)
	ledger := []findings.Finding{inDiff, overLimit, offDiff}
	r := &round{FullName: "acme/payments", Number: 1, Status: "done", HeadSHA: "abc",
		Outcome: &findings.Outcome{New: []uuid.UUID{inDiff.ID, overLimit.ID, offDiff.ID}}}
	comments, _, as, links := plan(r, ledger, map[string][]hunk{"a.go": {{1, 10}}}, 1, "https://github.com")
	if len(comments) != 1 || comments[0].FindingID != inDiff.ID.String() {
		t.Fatalf("comments %+v", comments)
	}
	if len(as) != 2 || as[0].FindingID != offDiff.ID.String() || as[0].Level != "failure" || as[0].StartLine != 90 || as[0].EndLine != 92 ||
		as[1].FindingID != overLimit.ID.String() || as[1].Level != "notice" {
		t.Fatalf("annotations %+v", as)
	}
	if !strings.HasSuffix(links[offDiff.ID.String()], "#L90-L92") {
		t.Fatalf("link %s", links[offDiff.ID.String()])
	}
}

func TestSCN_PUB_021_ToolLimitLine(t *testing.T) {
	a := &runner.Assessment{TLDR: "Small change.", Risk: "low"}
	line := "The agent reached the tool-call limit in the review stage. The review can be incomplete."
	sum := RenderSummary(Summary{Status: "done", HeadSHA: "abc1234", Verdict: findings.VerdictLooksGood, Assessment: a, ToolLimitStages: []string{"review"}})
	if !strings.Contains(sum, line) {
		t.Fatalf("summary without the limit line:\n%s", sum)
	}
	c := RenderCheck("gate", "done", findings.VerdictLooksGood, "", a, []string{"review"}, nil)
	if !strings.Contains(c.Summary, line) || c.Conclusion != "success" {
		t.Fatalf("check %+v", c)
	}
	if c := RenderCheck("gate", "done", findings.VerdictLooksGood, "", a, nil, nil); strings.Contains(c.Summary, "tool-call limit") {
		t.Fatalf("check without a limit has the line: %+v", c)
	}
}
