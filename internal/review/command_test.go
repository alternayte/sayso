package review

import (
	"slices"
	"testing"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		body, name string
		finding    int
		reason     string
		err        bool
	}{
		{"/sayso review", "review", 0, "", false},
		{"/sayso dismiss S-2 the caller checks the amount\nmore text", "dismiss", 2, "the caller checks the amount", false},
		{"/sayso dismiss S-2", "dismiss", 2, "", true},
		{"/sayso override", "override", 0, "", true},
		{"/sayso override release blocker accepted", "override", 0, "release blocker accepted", false},
		{"/sayso fix s-3", "fix", 3, "", false},
		{"/sayso explain", "explain", 0, "", true},
		{"/sayso merge", "merge", 0, "", true},
		{"/sayso", "", 0, "", true},
	}
	for _, c := range cases {
		got := ParseCommand(c.body)
		if got.Name != c.name || got.Finding != c.finding || got.Reason != c.reason || (got.Err != "") != c.err {
			t.Errorf("%q: %+v", c.body, got)
		}
	}
}

func TestAllowed(t *testing.T) {
	if Allowed("review", "read") || !Allowed("review", "write") || Allowed("override", "write") || !Allowed("override", "maintain") || !Allowed("explain", "read") {
		t.Fatal("permission table")
	}
}

func TestParseFix(t *testing.T) {
	cases := []struct {
		body       string
		findings   []int
		all, minor bool
		err        bool
	}{
		{"/sayso fix S-1 s-3 S-1", []int{1, 3}, false, false, false},
		{"/sayso fix all", nil, true, false, false},
		{"/sayso fix all --minor", nil, true, true, false},
		{"/sayso fix all --nits", nil, true, false, true},
		{"/sayso fix S-1 three", nil, false, false, true},
		{"/sayso fix", nil, false, false, true},
	}
	for _, c := range cases {
		got := ParseCommand(c.body)
		if (got.Err != "") != c.err || got.All != c.all || got.Minor != c.minor || (!c.err && !slices.Equal(got.Findings, c.findings)) {
			t.Errorf("%q: %+v", c.body, got)
		}
	}
}
