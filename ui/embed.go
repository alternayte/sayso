// Package ui embeds the built single-page app from ui/dist.
package ui

import "embed"

//go:embed all:dist
var Dist embed.FS
