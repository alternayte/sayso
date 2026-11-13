//go:build e2e

package review_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	fakegh "github.com/alternayte/sayso/internal/fakes/github"
)

// commandsPR opens a pull request with the e2e-commands script in gate mode
// and waits for its first round.
func commandsPR(t *testing.T) (*env, *fakegh.Pull) {
	t.Helper()
	e := real(t, "gate")
	pr := e.script(t, "e2e-commands", map[string]string{"ledger/ledger.go": noCheck})
	waitFor(t, 4*time.Minute, "round 1", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })
	return e, pr
}

func (e *env) reacted(id int64, content string) bool {
	for _, r := range e.GitHub.CommentReactions() {
		if r.CommentID == id && r.Content == content {
			return true
		}
	}
	return false
}

func (e *env) top(t *testing.T, n, seq int) fakegh.Comment {
	t.Helper()
	var id int64
	if err := e.Deps.Pool.QueryRow(t.Context(), `SELECT comment_id FROM findings WHERE seq = $1`, seq).Scan(&id); err != nil {
		t.Fatalf("finding S-%d has no comment: %v", seq, err)
	}
	return e.thread(n, id)
}

func TestSCN_CMD_001_PermissionCheck(t *testing.T) {
	e, pr := commandsPR(t)
	bob := e.comment(t, pr.Number, "dev-bob", "/sayso review")
	waitFor(t, 30*time.Second, "the refusal", func() bool {
		for _, b := range e.botIssueComments(pr.Number) {
			if strings.Contains(b, "needs write permission") {
				return true
			}
		}
		return false
	})
	if e.reacted(bob, "+1") || e.rounds(t, "acme/payments", pr.Number, "") != 1 {
		t.Fatal("Bob's command started a round")
	}
	alice := e.comment(t, pr.Number, "dev-alice", "/sayso review")
	waitFor(t, 4*time.Minute, "Alice's round", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 2 })
	if !e.reacted(alice, "+1") {
		t.Fatal("Alice's command got no reaction")
	}
}

func TestSCN_CMD_002_Override(t *testing.T) {
	e, pr := commandsPR(t)
	head := e.head(pr.Number)
	if c := e.check(head); c.Conclusion != "failure" {
		t.Fatalf("check before the override %+v", c)
	}
	e.comment(t, pr.Number, "octo-admin", "/sayso override")
	waitFor(t, 30*time.Second, "the refusal", func() bool {
		for _, b := range e.botIssueComments(pr.Number) {
			if strings.Contains(b, "give a reason") {
				return true
			}
		}
		return false
	})
	e.comment(t, pr.Number, "octo-admin", "/sayso override the release owner accepts the risk")
	waitFor(t, 30*time.Second, "the override", func() bool {
		c := e.check(head)
		return c.Conclusion == "success" && c.Title == "Overridden by @octo-admin"
	})
	if e.Count(t, `SELECT count(*) FROM steering_signals WHERE type = 'overridden' AND actor_login = 'octo-admin'`) != 1 ||
		e.Count(t, `SELECT count(*) FROM audit_log WHERE action = 'round.override'`) != 1 {
		t.Fatal("no signal or audit row for the override")
	}
	// A new push gets a new check that follows the new round.
	e.push(t, pr.Number, map[string]string{"ledger/ledger.go": noCheck + "\n// more\n"})
	waitFor(t, 4*time.Minute, "round 2", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 2 })
	if c := e.check(e.head(pr.Number)); c.Conclusion != "failure" {
		t.Fatalf("check after the push %+v", c)
	}
}

func TestSCN_CMD_003_FixPullRequest(t *testing.T) {
	e, pr := commandsPR(t)
	e.comment(t, pr.Number, "dev-alice", "/sayso fix S-2")
	branch := "sayso/fix/" + strconv.Itoa(pr.Number) + "-2"
	var fix *fakegh.Pull
	waitFor(t, 4*time.Minute, "the fix pull request", func() bool {
		for _, p := range e.GitHub.Repo("acme/payments").Pulls {
			if p.HeadRef == branch {
				fix = p
				return true
			}
		}
		return false
	})
	head := e.GitHub.Repo("acme/payments").Pulls[pr.Number]
	if fix.BaseRef != head.HeadRef || !strings.HasSuffix(fix.Author, "[bot]") {
		t.Fatalf("fix pull request %+v", fix)
	}
	got, err := e.GitHub.FileAt("acme/payments", "ledger/ledger.go", branch)
	if err != nil || !strings.Contains(got, "// Post adds amount cents to the balance. Refunds pass negative amounts.") {
		t.Fatalf("the fix branch has no patch: %v\n%s", err, got)
	}
	time.Sleep(5 * time.Second)
	if n := e.rounds(t, "acme/payments", fix.Number, ""); n != 0 {
		t.Fatalf("the fix pull request got %d rounds", n)
	}
	if !strings.Contains(strings.Join(threadBodies(e, pr.Number, e.top(t, pr.Number, 2).ID), "\n"), "Sayso opened #"+strconv.Itoa(fix.Number)) {
		t.Fatal("the thread has no link to the fix")
	}
	// On a fork the command is refused.
	fork := e.script(t, "e2e-commands", map[string]string{"ledger/ledger.go": noCheck})
	waitFor(t, 4*time.Minute, "the fork round", func() bool { return e.rounds(t, "acme/payments", fork.Number, "done") == 1 })
	e.GitHub.SetFork("acme/payments", fork.Number)
	e.comment(t, fork.Number, "dev-alice", "/sayso fix S-2")
	waitFor(t, 30*time.Second, "the fork refusal", func() bool {
		for _, b := range e.botIssueComments(fork.Number) {
			if strings.Contains(b, "fork") {
				return true
			}
		}
		return false
	})
}

func threadBodies(e *env, n int, top int64) []string {
	var out []string
	for _, c := range e.GitHub.Thread(top) {
		out = append(out, c.Body)
	}
	return out
}

func (e *env) reply(t *testing.T, n int, top int64, author, body string) {
	t.Helper()
	if _, code := e.GitHub.AddReviewComment("acme/payments", n, author, body, "", 0, top); code != http.StatusOK {
		t.Fatalf("reply: %d", code)
	}
}

func (e *env) botReplies(top int64) []string {
	var out []string
	for _, c := range e.GitHub.Thread(top) {
		if c.ID != top && strings.HasSuffix(c.Author, "[bot]") {
			out = append(out, c.Body)
		}
	}
	return out
}

func TestSCN_CMD_004_Replies(t *testing.T) {
	e, pr := commandsPR(t)
	s1, s2, s3 := e.top(t, pr.Number, 1), e.top(t, pr.Number, 2), e.top(t, pr.Number, 3)
	e.reply(t, pr.Number, s1.ID, "dev-alice", "The API rejects negative amounts first.")
	e.reply(t, pr.Number, s2.ID, "dev-alice", "The comment is fine.")
	e.reply(t, pr.Number, s3.ID, "dev-alice", "What should I change?")
	waitFor(t, 6*time.Minute, "three replies", func() bool { return e.Count(t, `SELECT count(*) FROM agent_work WHERE status = 'done'`) == 3 })
	for seq, want := range map[int]string{1: "conceded", 2: "open", 3: "open"} {
		if n := e.Count(t, `SELECT count(*) FROM findings WHERE seq = $1 AND status = $2`, seq, want); n != 1 {
			t.Fatalf("S-%d is not %s", seq, want)
		}
	}
	if !e.thread(pr.Number, s1.ID).Resolved {
		t.Fatal("the conceded thread is not resolved")
	}
	// Two more replies on S-2 reach the limit; the next one gets the stop message.
	for i := 0; i < 2; i++ {
		e.reply(t, pr.Number, s2.ID, "dev-alice", "Still fine.")
		want := 2 + i
		waitFor(t, 4*time.Minute, "reply", func() bool { return len(e.botReplies(s2.ID)) == want })
	}
	e.reply(t, pr.Number, s2.ID, "dev-alice", "And now?")
	waitFor(t, 60*time.Second, "the stop message", func() bool {
		r := e.botReplies(s2.ID)
		return len(r) == 4 && r[3] == "I will look again after the next push."
	})
	// A reply in a thread that a person started gets no answer.
	own, code := e.GitHub.AddReviewComment("acme/payments", pr.Number, "dev-alice", "Note for later.", "ledger/ledger.go", 15, 0)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	e.reply(t, pr.Number, own, "dev-bob", "Agreed.")
	time.Sleep(5 * time.Second)
	if len(e.botReplies(own)) != 0 {
		t.Fatal("Sayso answered in a person's thread")
	}
}

func TestSCN_CMD_005_HumanCommentSignal(t *testing.T) {
	e, pr := commandsPR(t)
	before := len(e.GitHub.ReviewComments("acme/payments"))
	if _, code := e.GitHub.AddReviewComment("acme/payments", pr.Number, "dev-alice", "Should this log the refund?", "ledger/ledger.go", 20, 0); code != http.StatusOK {
		t.Fatal(code)
	}
	waitFor(t, 30*time.Second, "the signal", func() bool {
		return e.Count(t, `SELECT count(*) FROM steering_signals WHERE type = 'human_comment_unflagged' AND detail->>'path' = 'ledger/ledger.go'
			AND (detail->>'line')::int = 20 AND detail->>'excerpt' = 'Should this log the refund?' AND actor_login = 'dev-alice'`) == 1
	})
	time.Sleep(3 * time.Second)
	if after := len(e.GitHub.ReviewComments("acme/payments")); after != before+1 {
		t.Fatalf("Sayso posted %d comments", after-before-1)
	}
}

func TestSCN_PUB_007_NoApproveMergeOrForeignPush(t *testing.T) {
	e, pr := commandsPR(t)
	for _, c := range []string{"/sayso pause", "/sayso resume", "/sayso review", "/sayso explain S-3", "/sayso dismiss S-3 not needed", "/sayso override accepted", "/sayso fix S-2"} {
		e.comment(t, pr.Number, "octo-admin", c)
		time.Sleep(time.Second)
	}
	waitFor(t, 6*time.Minute, "the fix pull request", func() bool {
		for _, p := range e.GitHub.Repo("acme/payments").Pulls {
			if strings.HasPrefix(p.HeadRef, "sayso/fix/") {
				return true
			}
		}
		return false
	})
	e.reply(t, pr.Number, e.top(t, pr.Number, 1).ID, "dev-alice", "Is this right?")
	waitFor(t, 4*time.Minute, "the reply", func() bool { return e.Count(t, `SELECT count(*) FROM agent_work WHERE kind = 'reply' AND status = 'done'`) >= 2 })
	for _, r := range e.GitHub.Reviews() {
		if r.Event != "COMMENT" {
			t.Fatalf("review with event %s", r.Event)
		}
	}
	for _, p := range e.GitHub.Pushes() {
		if !strings.HasPrefix(p.Branch, "sayso/fix/") {
			t.Fatalf("push to %s", p.Branch)
		}
	}
	for _, r := range e.GitHub.Requests() {
		if strings.HasSuffix(r.Path, "/merge") || strings.Contains(r.Path, "/git/refs/heads/") && r.Method == http.MethodPatch {
			t.Fatalf("request %s %s", r.Method, r.Path)
		}
	}
}
