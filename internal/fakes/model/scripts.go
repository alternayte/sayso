package model

import (
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Scripts are YAML files in scripts/. A script answers when its marker is
// in the system prompt or a message, for example "[script:review-blocker]"
// in the repository guidance. Each stage has its own list of steps; the step
// of a request is the number of assistant turns that came before it.
//
//go:embed scripts/*.yaml
var scriptFS embed.FS

// Script is one scripted conversation.
type Script struct {
	Marker string            `yaml:"marker"`
	Stages map[string][]Step `yaml:"stages"`
}

// Step is one model turn.
type Step struct {
	Text    string     `yaml:"text"`
	Tools   []ToolCall `yaml:"tools"`
	DelayMS int        `yaml:"delay_ms"`
}

// ToolCall is one scripted tool call.
type ToolCall struct {
	Name string         `yaml:"name"`
	Args map[string]any `yaml:"args"`
}

var stageRe = regexp.MustCompile(`## Stage: (\w+)`)

// step returns the step of a stage and turn. A finished script answers with
// a final text.
func (s *Script) step(system string, turns int) Step {
	stage := "review"
	if m := stageRe.FindStringSubmatch(system); m != nil {
		stage = m[1]
	}
	steps := s.Stages[stage]
	if turns < len(steps) {
		return steps[turns]
	}
	return Step{Text: "Done."}
}

// LoadScripts reads the embedded scripts.
func LoadScripts() ([]*Script, error) {
	var out []*Script
	err := fs.WalkDir(scriptFS, "scripts", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := scriptFS.ReadFile(p)
		if err != nil {
			return err
		}
		var s Script
		if err := yaml.Unmarshal(b, &s); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if s.Marker == "" {
			return fmt.Errorf("%s: marker is required", p)
		}
		out = append(out, &s)
		return nil
	})
	return out, err
}

// script returns the script whose marker is in the text.
func (f *Fake) script(text string) *Script {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.scripts == nil {
		s, err := LoadScripts()
		if err != nil {
			panic(err)
		}
		f.scripts = s
	}
	for _, s := range f.scripts {
		if strings.Contains(text, s.Marker) {
			return s
		}
	}
	return nil
}
