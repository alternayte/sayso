package guidance

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/alternayte/sayso/internal/runner"
)

var update = flag.Bool("update", false, "write the golden files")

type fixture struct {
	Execution           bool           `json:"execution"`
	Skill               string         `json:"skill"`
	OverlayInstructions string         `json:"overlay_instructions"`
	Prompts             runner.Prompts `json:"prompts"`
}

// The runtime's assembler checks the same golden files (agent/src/prompts.test.ts).
func TestAssembleMatchesTheRuntime(t *testing.T) {
	b, err := os.ReadFile("../../tests/prompts/fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"review", "challenge", "reply", "fix"} {
		got := Assemble(f.Prompts, f.OverlayInstructions, f.Execution, stage, f.Skill).Text
		path := "../../tests/prompts/" + stage + ".golden.txt"
		if *update {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Fatalf("%s: the assembled prompt differs from %s", stage, path)
		}
	}
}
