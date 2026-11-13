package repos

import (
	"bytes"
	"fmt"
	"slices"
	"sort"

	"go.yaml.in/yaml/v3"
)

// OverlayPath is where a repository keeps its overlay on the base branch.
const OverlayPath = ".sayso.yml"

// Overlay is .sayso.yml (Appendix E). It is read from the base commit only.
type Overlay struct {
	Skill             string            `yaml:"skill"`
	SkaelSet          string            `yaml:"skael_set"`
	Profiles          map[string]string `yaml:"profiles"`
	MCP               *[]string         `yaml:"mcp"`
	Extensions        *[]string         `yaml:"extensions"`
	Ignore            []string          `yaml:"ignore"`
	Instructions      string            `yaml:"instructions"`
	Nits              string            `yaml:"nits"`
	Block             *bool             `yaml:"block"`
	MaxInlineComments int               `yaml:"max_inline_comments"`
	Image             string            `yaml:"image"`
	Toolchain         string            `yaml:"toolchain"`
	Restore           string            `yaml:"restore"`
	Setup             []string          `yaml:"setup"`
	Feeds             *[]string         `yaml:"feeds"`
	Execution         string            `yaml:"execution"`
}

// uiOnly are keys that only admins and maintainers set in the UI.
var uiOnly = map[string]bool{
	"mode": true, "budget": true, "round_budget_usd": true, "monthly_budget_usd": true, "override_teams": true,
	"providers": true, "skael_key": true, "skael_url": true, "challenge_min_severity": true, "max_concurrent_rounds": true,
}

var known = map[string]bool{
	"skill": true, "skael_set": true, "profiles": true, "mcp": true, "extensions": true, "ignore": true, "instructions": true,
	"nits": true, "block": true, "max_inline_comments": true, "image": true, "toolchain": true, "restore": true, "setup": true,
	"feeds": true, "execution": true,
}

// ParseOverlay reads .sayso.yml and returns warnings for unknown keys and
// keys that only the UI sets.
func ParseOverlay(b []byte) (Overlay, []string, error) {
	var o Overlay
	if len(bytes.TrimSpace(b)) == 0 {
		return o, nil, nil
	}
	if err := yaml.Unmarshal(b, &o); err != nil {
		return o, nil, fmt.Errorf("%s: %w", OverlayPath, err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return o, nil, err
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var warn []string
	for _, k := range keys {
		switch {
		case uiOnly[k]:
			warn = append(warn, fmt.Sprintf("%s: key %q is set in the UI only; the file value is ignored", OverlayPath, k))
		case !known[k]:
			warn = append(warn, fmt.Sprintf("%s: unknown key %q is ignored", OverlayPath, k))
		}
	}
	return o, warn, nil
}

// Source names where an effective value comes from.
const (
	SourceUI      = "ui"
	SourceFile    = "file"
	SourceDefault = "default"
)

// Effective is the configuration of a round for one repository after the
// overlay (§16.1). Sources tells where each value comes from.
type Effective struct {
	Execution         bool              `json:"execution"`
	ChallengeMin      string            `json:"challenge_min_severity"`
	Profiles          map[string]string `json:"profiles"`
	MCP               []string          `json:"mcp"`
	Extensions        []string          `json:"extensions"`
	Feeds             []string          `json:"feeds"`
	Skill             string            `json:"skill"`
	Ignore            []string          `json:"ignore"`
	Instructions      string            `json:"instructions"`
	Nits              string            `json:"nits"`
	Block             bool              `json:"block"`
	MaxInlineComments int               `json:"max_inline_comments"`
	Image             string            `json:"image"`
	Toolchain         string            `json:"toolchain"`
	Restore           string            `json:"restore"`
	Setup             []string          `json:"setup"`
	Sources           map[string]string `json:"sources"`
	Warnings          []string          `json:"warnings"`
}

// DefaultSkillPath is the repository skill when .sayso.yml names none (§8.4).
const DefaultSkillPath = ".agents/skills/pr-review/SKILL.md"

// Limits are the admin bounds that the overlay must stay inside.
type Limits struct {
	AllowedProfiles   []string
	MaxInlineComments int
	Extensions        []string // enabled for the repository
	Images            []string
}

// Resolve applies the overlay to the repository settings within the limits
// of Appendix E and the execution spec. The file can narrow lists, choose
// allowed profiles and add guidance; it never sets UI-only keys and never
// turns execution on.
func Resolve(repo Repository, o Overlay, fileWarnings []string, lim Limits) Effective {
	e := Effective{
		Execution:    repo.Execution,
		ChallengeMin: repo.ChallengeMinSeverity,
		Profiles:     map[string]string{"review": repo.ProfileReview, "challenge": repo.ProfileChallenge, "reply": repo.ProfileReply, "fix": repo.ProfileFix},
		MCP:          slices.Clone(repo.McpServers),
		Extensions:   slices.Clone(lim.Extensions),
		Feeds:        slices.Clone(repo.Feeds),
		Skill:        DefaultSkillPath,
		Nits:         "summary",
		Block:        true,
		Toolchain:    "auto",
		Restore:      "auto",
		Image:        repo.AgentImage,
		Sources: map[string]string{
			"execution": SourceUI, "challenge_min_severity": SourceUI, "profiles.review": SourceUI, "profiles.challenge": SourceUI, "profiles.reply": SourceUI, "profiles.fix": SourceUI, "mcp": SourceUI, "extensions": SourceUI,
			"feeds": SourceUI, "skill": SourceDefault, "nits": SourceDefault, "block": SourceDefault, "toolchain": SourceDefault,
			"restore": SourceDefault, "max_inline_comments": SourceDefault, "image": SourceUI,
		},
		Warnings:          append([]string{}, fileWarnings...),
		MaxInlineComments: lim.MaxInlineComments,
	}
	if e.Image == "" {
		e.Sources["image"] = SourceDefault
	}
	warn := func(format string, a ...any) { e.Warnings = append(e.Warnings, fmt.Sprintf(format, a...)) }
	switch o.Execution {
	case "":
	case "off":
		e.Execution = false
		e.Sources["execution"] = SourceFile
	default:
		warn("%s: execution %q is ignored; the file can only turn execution off", OverlayPath, o.Execution)
	}
	for stage, name := range o.Profiles {
		if _, ok := e.Profiles[stage]; !ok {
			warn("%s: profiles: unknown stage %q", OverlayPath, stage)
			continue
		}
		if !slices.Contains(lim.AllowedProfiles, name) {
			warn("%s: profiles: %q is not in the allowed list; the %s profile stays %s", OverlayPath, name, stage, e.Profiles[stage])
			continue
		}
		e.Profiles[stage] = name
		e.Sources["profiles."+stage] = SourceFile
	}
	narrow := func(key string, cur []string, want *[]string) []string {
		if want == nil {
			return cur
		}
		var out []string
		for _, n := range *want {
			if slices.Contains(cur, n) {
				out = append(out, n)
			} else {
				warn("%s: %s: %q is not allowed for this repository and is ignored", OverlayPath, key, n)
			}
		}
		e.Sources[key] = SourceFile
		if out == nil {
			out = []string{}
		}
		return out
	}
	e.MCP = narrow("mcp", e.MCP, o.MCP)
	e.Extensions = narrow("extensions", e.Extensions, o.Extensions)
	e.Feeds = narrow("feeds", e.Feeds, o.Feeds)
	if o.Skill != "" {
		e.Skill, e.Sources["skill"] = o.Skill, SourceFile
	}
	if len(o.Ignore) > 0 {
		e.Ignore, e.Sources["ignore"] = o.Ignore, SourceFile
	}
	if o.Instructions != "" {
		text := o.Instructions
		if len(text) > 4000 {
			text = text[:4000]
			warn("%s: instructions are longer than 4,000 characters and were cut", OverlayPath)
		}
		e.Instructions, e.Sources["instructions"] = text, SourceFile
	}
	switch o.Nits {
	case "":
	case "summary", "inline":
		e.Nits, e.Sources["nits"] = o.Nits, SourceFile
	default:
		warn("%s: nits %q is not summary or inline", OverlayPath, o.Nits)
	}
	if o.Block != nil {
		e.Block, e.Sources["block"] = *o.Block, SourceFile
	}
	if o.MaxInlineComments > 0 {
		if o.MaxInlineComments <= lim.MaxInlineComments {
			e.MaxInlineComments, e.Sources["max_inline_comments"] = o.MaxInlineComments, SourceFile
		} else {
			warn("%s: max_inline_comments %d is above the admin limit %d", OverlayPath, o.MaxInlineComments, lim.MaxInlineComments)
		}
	}
	if o.Image != "" {
		if slices.Contains(lim.Images, o.Image) {
			e.Image, e.Sources["image"] = o.Image, SourceFile
		} else {
			warn("%s: image %q is not in the admin's image list", OverlayPath, o.Image)
		}
	}
	for _, kv := range []struct {
		key string
		val string
		dst *string
	}{{"toolchain", o.Toolchain, &e.Toolchain}, {"restore", o.Restore, &e.Restore}} {
		switch kv.val {
		case "":
		case "auto", "none":
			*kv.dst, e.Sources[kv.key] = kv.val, SourceFile
		default:
			warn("%s: %s %q is not auto or none", OverlayPath, kv.key, kv.val)
		}
	}
	if len(o.Setup) > 0 {
		e.Setup, e.Sources["setup"] = o.Setup, SourceFile
	}
	if !e.Execution {
		// Execution off: nothing runs, so the file's toolchain, restore and setup are ignored.
		e.Toolchain, e.Restore, e.Setup = "none", "none", nil
	}
	return e
}
