package review

import (
	"regexp"
	"strconv"
	"strings"
)

// Command is a parsed /sayso comment (§11.1).
type Command struct {
	Name    string
	Finding int
	// Findings are the findings of a fix, Finding first. All selects the
	// open blocker and major findings; Minor adds minor ones.
	Findings   []int
	All, Minor bool
	Reason     string
	// Err is why the comment is not a valid command.
	Err string
}

var findingArg = regexp.MustCompile(`^S-(\d+)$`)

// commandNames maps each command to whether it names a finding and needs a reason.
var commandNames = map[string]struct{ finding, reason bool }{
	"review": {}, "pause": {}, "resume": {},
	"dismiss":  {finding: true, reason: true},
	"override": {reason: true},
	"fix":      {finding: true},
	"explain":  {finding: true},
}

// ParseCommand reads the first line of a comment that starts with /sayso.
func ParseCommand(body string) Command {
	line, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	fields := strings.Fields(line)
	if len(fields) == 0 || fields[0] != "/sayso" {
		return Command{Err: "the comment does not start with /sayso"}
	}
	if len(fields) < 2 {
		return Command{Err: "name a command: review, pause, resume, dismiss, override, fix or explain"}
	}
	c := Command{Name: strings.ToLower(fields[1])}
	spec, ok := commandNames[c.Name]
	if !ok {
		return Command{Name: c.Name, Err: "unknown command " + c.Name + "; use review, pause, resume, dismiss, override, fix or explain"}
	}
	rest := fields[2:]
	if c.Name == "fix" {
		return parseFix(c, rest)
	}
	if spec.finding {
		if len(rest) == 0 || !findingArg.MatchString(strings.ToUpper(rest[0])) {
			c.Err = "name the finding, for example /sayso " + c.Name + " S-2"
			return c
		}
		c.Finding, _ = strconv.Atoi(findingArg.FindStringSubmatch(strings.ToUpper(rest[0]))[1])
		rest = rest[1:]
	}
	c.Reason = strings.TrimSpace(strings.Join(rest, " "))
	if spec.reason && c.Reason == "" {
		c.Err = "give a reason, for example /sayso " + c.Name + example(spec.finding) + " the caller checks the amount"
	}
	return c
}

// parseFix reads /sayso fix S-1 S-3 …, /sayso fix all and /sayso fix all --minor.
func parseFix(c Command, args []string) Command {
	const usage = "name the findings, for example /sayso fix S-2 S-3, or use /sayso fix all"
	if len(args) == 0 {
		c.Err = usage
		return c
	}
	if strings.EqualFold(args[0], "all") {
		c.All = true
		for _, a := range args[1:] {
			if a != "--minor" {
				c.Err = "unknown option " + a + "; /sayso fix all takes only --minor"
				return c
			}
			c.Minor = true
		}
		return c
	}
	seen := map[int]bool{}
	for _, a := range args {
		m := findingArg.FindStringSubmatch(strings.ToUpper(a))
		if m == nil {
			c.Err = usage
			return c
		}
		n, _ := strconv.Atoi(m[1])
		if !seen[n] {
			seen[n] = true
			c.Findings = append(c.Findings, n)
		}
	}
	c.Finding = c.Findings[0]
	return c
}

func example(finding bool) string {
	if finding {
		return " S-2"
	}
	return ""
}

// permissionRank orders GitHub repository permissions.
var permissionRank = map[string]int{"none": 0, "read": 1, "triage": 2, "write": 3, "maintain": 4, "admin": 5}

// needs is the permission each command needs (§11.1); override can also come from a team.
var needs = map[string]string{"review": "write", "pause": "write", "resume": "write", "dismiss": "write", "override": "maintain", "fix": "write", "explain": "read"}

// Allowed reports whether a permission may run a command.
func Allowed(command, permission string) bool {
	return permissionRank[permission] >= permissionRank[needs[command]]
}
