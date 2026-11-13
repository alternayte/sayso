package review

import "testing"

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
