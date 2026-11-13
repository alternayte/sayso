package publish

import (
	"strings"
	"testing"

	"github.com/alternayte/sayso/internal/findings"
	"github.com/alternayte/sayso/internal/github"
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

func TestDiffLines(t *testing.T) {
	patch := "@@ -1,3 +1,4 @@\n package money\n-old\n+new\n+added\n ctx\n@@ -20,2 +21,2 @@\n a\n+b"
	d := diffLines(githubFiles{{"internal/money/round.go", patch}}.files())["internal/money/round.go"]
	for _, l := range []int{1, 2, 3, 4, 21, 22} {
		if !d[l] {
			t.Errorf("line %d is not in the diff", l)
		}
	}
	if d[5] || d[20] {
		t.Error("lines outside the hunks are in the diff")
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
