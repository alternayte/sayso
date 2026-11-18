package runner_test

import (
	"strings"
	"testing"

	"github.com/alternayte/sayso/internal/runner"
)

func TestParseScheduling(t *testing.T) {
	ts, err := runner.ParseTolerations(`[{"key":"CriticalAddonsOnly","operator":"Equal","value":"linuxapps","effect":"NoSchedule"},{"operator":"Exists"}]`)
	if err != nil || len(ts) != 2 || ts[0].Value != "linuxapps" {
		t.Fatalf("tolerations %+v, %v", ts, err)
	}
	if ts, err := runner.ParseTolerations(""); err != nil || ts != nil {
		t.Fatalf("empty tolerations %+v, %v", ts, err)
	}
	for in, want := range map[string]string{
		`{"key":"a"}`:                                   "not a JSON array",
		`[{"key":"a","effect":"NoSchedul"}]`:            "effect",
		`[{"key":"a","operatr":"Exists"}]`:              "unknown field",
		`[{"operator":"Equal","value":"x"}]`:            "needs a key",
		`[{"key":"a","operator":"Exists","value":"x"}]`: "takes no value",
		`[{"key":"a","tolerationSeconds":5}]`:           "NoExecute",
		`[] []`:                                         "text after",
	} {
		if _, err := runner.ParseTolerations(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseTolerations(%s) = %v, want %q", in, err, want)
		}
	}
	sel, err := runner.ParseNodeSelector(`{"kubernetes.io/os":"linux","agentpool":"acme"}`)
	if err != nil || sel["agentpool"] != "acme" {
		t.Fatalf("node selector %v, %v", sel, err)
	}
	for _, in := range []string{`["linux"]`, `{"a":1}`, `{"":"x"}`} {
		if _, err := runner.ParseNodeSelector(in); err == nil {
			t.Errorf("ParseNodeSelector(%s) has no error", in)
		}
	}
}
