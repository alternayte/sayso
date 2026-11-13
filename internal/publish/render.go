// Package publish writes the outcome of a round to GitHub: the summary
// comment, one review with inline comments, replies, thread states and the
// check run (§10). It never submits APPROVE or REQUEST_CHANGES (SI-07).
package publish

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/alternayte/sayso/internal/findings"
	"github.com/alternayte/sayso/internal/runner"
)

// Markers find what Sayso posted before (§10.5).
const (
	SummaryMarker = "<!-- sayso:summary -->"
	findingMarker = "<!-- sayso:finding:%s -->"
)

// FindingMarker is the marker at the end of an inline comment.
func FindingMarker(id string) string { return fmt.Sprintf(findingMarker, id) }

var mention = regexp.MustCompile(`(^|[^\w` + "`" + `])@([A-Za-z0-9](?:[A-Za-z0-9-]{0,38})(?:/[A-Za-z0-9_-]+)?)`)

// Safe makes agent text safe to post (SI-16): HTML is escaped, so no raw
// HTML or comment marker reaches GitHub, and each @mention is in code
// formatting, so it notifies nobody.
func Safe(s string) string {
	s = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	return mention.ReplaceAllString(s, "$1`@$2`")
}

// sha7 is the short form of a commit SHA.
func sha7(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// StatusLine is the first line of the summary (§10.2).
func StatusLine(status, head, reason string, paused bool) string {
	switch {
	case paused:
		return "Paused"
	case status == "queued" || status == "running" || status == "publishing":
		return "Reviewing " + sha7(head) + "…"
	case status == "done":
		return "Reviewed " + sha7(head)
	case status == "cancelled":
		return Safe(reason)
	case status == "budget_exceeded":
		return "Review failed: the round budget is spent"
	default:
		return "Review failed: " + Safe(reason)
	}
}

// Summary is the data of the summary comment.
type Summary struct {
	Status     string
	HeadSHA    string
	Reason     string
	Paused     bool
	Verdict    string
	Assessment *runner.Assessment
	// Open are the open findings of the review.
	Open []findings.Finding
	// Since are the findings that changed in this round, by change.
	Fixed, Withdrawn, Dismissed, Reopened []findings.Finding
	// Links maps a finding ID to the URL of its inline comment or its file line.
	Links    map[string]string
	RoundURL string
	Skill    *runner.SkillRef
	Profiles map[string]string
}

var verdictText = map[string]string{
	findings.VerdictBlocking:         "Blocking",
	findings.VerdictChangesSuggested: "Changes suggested",
	findings.VerdictLooksGood:        "Looks good",
}

// SortFindings orders blocking findings first, then by severity and seq.
func SortFindings(fs []findings.Finding) {
	slices.SortStableFunc(fs, func(a, b findings.Finding) int {
		if a.Blocking != b.Blocking {
			if a.Blocking {
				return -1
			}
			return 1
		}
		if d := findings.SeverityRank(a.Severity) - findings.SeverityRank(b.Severity); d != 0 {
			return d
		}
		return a.Seq - b.Seq
	})
}

func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(Safe(s), "|", "\\|"), "\n", " ")
}

// RenderSummary renders the summary comment in the order of §10.2.
func RenderSummary(s Summary) string {
	var b strings.Builder
	b.WriteString(SummaryMarker + "\n")
	b.WriteString("**Sayso** · " + StatusLine(s.Status, s.HeadSHA, s.Reason, s.Paused) + "\n\n")
	if s.Assessment != nil && s.Verdict != "" {
		fmt.Fprintf(&b, "**Verdict:** %s · **Risk:** %s", verdictText[s.Verdict], s.Assessment.Risk)
		if len(s.Assessment.RiskReasons) > 0 {
			b.WriteString(" — " + Safe(strings.Join(s.Assessment.RiskReasons, "; ")))
		}
		b.WriteString("\n\n")
		b.WriteString("**TL;DR:** " + Safe(s.Assessment.TLDR) + "\n\n")
		if len(s.Assessment.WhereToLook) > 0 {
			b.WriteString("**Where to look**\n\n")
			for _, w := range s.Assessment.WhereToLook[:min(6, len(s.Assessment.WhereToLook))] {
				fmt.Fprintf(&b, "- `%s`: %s\n", strings.ReplaceAll(w.Target, "`", ""), Safe(w.Why))
			}
			b.WriteString("\n")
		}
		if len(s.Assessment.Impact) > 0 {
			b.WriteString("**Impact**\n\n")
			for _, i := range s.Assessment.Impact[:min(6, len(s.Assessment.Impact))] {
				fmt.Fprintf(&b, "- `%s`: %s\n", strings.ReplaceAll(i.Area, "`", ""), Safe(i.Why))
			}
			b.WriteString("\n")
		}
	}
	open := slices.Clone(s.Open)
	SortFindings(open)
	counts := map[string]int{}
	for _, f := range open {
		counts[f.Severity]++
	}
	fmt.Fprintf(&b, "**Findings:** %d blocker · %d major · %d minor · %d nit\n\n", counts["blocker"], counts["major"], counts["minor"], counts["nit"])
	if len(open) > 0 {
		b.WriteString("| ID | Severity | Title | Link |\n|---|---|---|---|\n")
		for _, f := range open {
			sev := f.Severity
			if f.Blocking {
				sev += " (blocking)"
			}
			link := "—"
			if u := s.Links[f.ID.String()]; u != "" {
				link = fmt.Sprintf("[%s:%d](%s)", cell(f.Path), f.StartLine, u)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", f.Label(), sev, cell(f.Title), link)
		}
		b.WriteString("\n")
	}
	since := []struct {
		name string
		fs   []findings.Finding
	}{{"Fixed", s.Fixed}, {"Withdrawn", s.Withdrawn}, {"Dismissed", s.Dismissed}, {"Reopened", s.Reopened}}
	var lines []string
	for _, g := range since {
		if len(g.fs) == 0 {
			continue
		}
		var ids []string
		for _, f := range g.fs {
			ids = append(ids, f.Label())
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", g.name, strings.Join(ids, ", ")))
	}
	if len(lines) > 0 {
		b.WriteString("**Since last review**\n\n" + strings.Join(lines, "\n") + "\n\n")
	}
	b.WriteString("---\n")
	var foot []string
	if s.RoundURL != "" {
		foot = append(foot, fmt.Sprintf("[Round](%s)", s.RoundURL))
	}
	if s.Skill != nil {
		v := s.Skill.Version
		if len(v) > 12 {
			v = sha7(v)
		}
		foot = append(foot, fmt.Sprintf("skill %s %s %s", s.Skill.Source, s.Skill.Name, v))
	}
	if len(s.Profiles) > 0 {
		var ps []string
		for _, k := range []string{"review", "challenge", "reply", "fix"} {
			if v := s.Profiles[k]; v != "" {
				ps = append(ps, k+": "+v)
			}
		}
		foot = append(foot, "profiles "+strings.Join(ps, ", "))
	}
	b.WriteString("<sub>" + strings.Join(foot, " · ") + "</sub>\n\n")
	b.WriteString("<sub>Commands: `/sayso review` · `/sayso pause` · `/sayso resume` · `/sayso dismiss S-<n> <reason>` · `/sayso override <reason>` · `/sayso fix S-<n>` · `/sayso explain S-<n>`</sub>\n")
	return b.String()
}

// RenderFinding is the body of an inline comment (§10.3).
func RenderFinding(f findings.Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s · %s** %s\n\n", f.Label(), f.Severity, Safe(f.Title))
	if f.Body != "" {
		b.WriteString(Safe(f.Body) + "\n\n")
	}
	ev := "Evidence (" + f.Evidence.Kind + ")"
	if f.Evidence.TestPath != "" {
		ev += ", `" + strings.ReplaceAll(f.Evidence.TestPath, "`", "") + "`"
	}
	b.WriteString("_" + ev + ":_ " + Safe(f.Evidence.Detail) + "\n\n")
	if f.Suggestion != "" {
		// A suggestion block is code; only the fence is escaped.
		b.WriteString("```suggestion\n" + strings.ReplaceAll(f.Suggestion, "```", "` ` `") + "\n```\n\n")
	}
	b.WriteString(FindingMarker(f.ID.String()))
	return b.String()
}

// Check is the output of the check run.
type Check struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	Title      string `json:"title"`
	Summary    string `json:"summary"`
}

// Conclusion maps a round to a check conclusion (§10.4).
func Conclusion(mode, status, verdict string) string {
	switch {
	case status == "cancelled" || status == "failed" || status == "budget_exceeded":
		return "neutral"
	case verdict == findings.VerdictBlocking && mode == "gate":
		return "failure"
	case verdict == findings.VerdictLooksGood:
		return "success"
	default:
		return "neutral"
	}
}

// RenderCheck is the finished check run of a round.
func RenderCheck(mode, status, verdict, reason string, a *runner.Assessment, open []findings.Finding) Check {
	c := Check{Status: "completed", Conclusion: Conclusion(mode, status, verdict)}
	switch status {
	case "done":
		risk := ""
		if a != nil {
			risk = a.Risk
		}
		c.Title = fmt.Sprintf("%s · risk %s", verdictText[verdict], risk)
		var b strings.Builder
		if a != nil {
			b.WriteString(Safe(a.TLDR) + "\n\n")
		}
		var blocking []string
		for _, f := range open {
			if f.Blocking {
				blocking = append(blocking, fmt.Sprintf("- **%s** %s (`%s:%d`)", f.Label(), Safe(f.Title), strings.ReplaceAll(f.Path, "`", ""), f.StartLine))
			}
		}
		if len(blocking) > 0 {
			b.WriteString("**Blocking findings**\n\n" + strings.Join(blocking, "\n") + "\n")
		}
		c.Summary = b.String()
	case "budget_exceeded":
		c.Title, c.Summary = "Review stopped: budget spent", "The round budget is spent. Raise the budget of the profile or run `/sayso review`."
	case "cancelled":
		// Sayso writes this reason ("Cancelled by @login"); it is not agent text.
		c.Title, c.Summary = reason, reason
	default:
		c.Title, c.Summary = "Review failed", Safe(reason)
	}
	return c
}
