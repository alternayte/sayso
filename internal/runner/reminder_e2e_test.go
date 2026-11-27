//go:build e2e

package runner_test

import (
	"slices"
	"strings"
	"testing"
)

func reminders(evs []event) []event {
	var out []event
	for _, e := range evs {
		if e.Type == "note" && e.Name == "reminder" {
			out = append(out, e)
		}
	}
	return out
}

func TestSCN_RUN_021_ReminderGetsTheAssessment(t *testing.T) {
	h := newHarness(t)
	o := h.start(t, runOpts{script: "[script:review-reminder]"})
	mustStatus(t, o, "done")
	if res := h.result(t, o.run.ID); res.Assessment == nil || len(res.ToolLimitStages) != 0 {
		t.Fatalf("result after the reminder: %+v", res)
	}
	r := reminders(h.events(t, o.run.ID))
	if len(r) != 1 || !strings.Contains(r[0].Preview, "set_assessment was not called: the model ended its turn without the call") {
		t.Fatalf("reminder events %+v", r)
	}
}

func TestSCN_RUN_022_ReminderNamesCutOutput(t *testing.T) {
	h := newHarness(t)
	o := h.start(t, runOpts{script: "[script:review-cut]"})
	mustStatus(t, o, "done")
	if r := reminders(h.events(t, o.run.ID)); len(r) != 1 || !strings.Contains(r[0].Preview, "cut off at the output token limit") {
		t.Fatalf("reminder events %+v", r)
	}
	var sent bool
	for _, req := range h.requests("review") {
		if strings.Contains(strings.Join(messageTexts(req), "\n"), "Your last message was cut off at the output token limit.") {
			sent = true
		}
	}
	if !sent {
		t.Fatal("no model request carries the cut-off reminder")
	}
}

func TestSCN_RUN_023_ReminderAfterToolLimit(t *testing.T) {
	h := newHarness(t)
	o := h.start(t, runOpts{script: "[script:review-tool-limit]", maxCalls: 2})
	mustStatus(t, o, "done")
	if res := h.result(t, o.run.ID); res.Assessment == nil || !slices.Equal(res.ToolLimitStages, []string{"review"}) {
		t.Fatalf("result after the limit: %+v", res)
	}
	var reads []string
	for _, e := range h.events(t, o.run.ID) {
		if e.Type == "tool_call" && e.Name == "read" {
			reads = append(reads, e.Preview)
		}
	}
	// Two reads run, the third reaches the limit, and the reminder may
	// not read.
	count := func(s string) (n int) {
		for _, r := range reads {
			if strings.Contains(r, s) {
				n++
			}
		}
		return n
	}
	if len(reads) != 4 || count("limit_reached: 2 tool calls") != 1 || count("Only the Sayso tools of this stage are available now") != 1 {
		t.Fatalf("read results %q", reads)
	}
}

func TestSCN_RUN_024_ModelErrorIsTheCause(t *testing.T) {
	h := newHarness(t)
	o := h.start(t, runOpts{script: "[script:review-model-400]"})
	mustStatus(t, o, "failed")
	if !strings.HasPrefix(o.run.ExitReason, "failed: review: ") || strings.Contains(o.run.ExitReason, "set_assessment") {
		t.Fatalf("exit reason %q", o.run.ExitReason)
	}
	if r := reminders(h.events(t, o.run.ID)); len(r) != 0 {
		t.Fatalf("a reminder after a model error: %+v", r)
	}
}

func TestSCN_RUN_025_ToolLimitWithoutOutput(t *testing.T) {
	h := newHarness(t)
	o := h.start(t, runOpts{script: "[script:review-tool-limit-silent]", maxCalls: 2})
	mustStatus(t, o, "failed")
	want := "limit_reached: review: set_assessment was not called after a reminder (the stage reached the limit of 2 tool calls)"
	if o.run.ExitReason != want {
		t.Fatalf("exit reason %q, want %q", o.run.ExitReason, want)
	}
}

// messageTexts are the string contents of the messages of a chat request.
func messageTexts(req map[string]any) []string {
	var out []string
	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		switch c := m.(map[string]any)["content"].(type) {
		case string:
			out = append(out, c)
		case []any:
			for _, p := range c {
				if s, ok := p.(map[string]any)["text"].(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out
}
