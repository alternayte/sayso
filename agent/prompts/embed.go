// Package prompts embeds the contract layer of the agent runtime, so the
// API can show the assembled prompt (§8.9) with the same text.
package prompts

import "embed"

// FS holds the contract files: contract.md and one file per stage.
//
//go:embed *.md
var FS embed.FS
