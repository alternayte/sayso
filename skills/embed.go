// Package skills holds the built-in review skill (§8.4).
package skills

import (
	"embed"
	"io/fs"
)

//go:embed pr-review
var builtin embed.FS

// BuiltinName is the name of the built-in review skill.
const BuiltinName = "pr-review"

// Builtin returns the files of the built-in skill folder by relative path.
func Builtin() (map[string]string, error) {
	out := map[string]string{}
	err := fs.WalkDir(builtin, BuiltinName, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := builtin.ReadFile(p)
		if err != nil {
			return err
		}
		out[p[len(BuiltinName)+1:]] = string(b)
		return nil
	})
	return out, err
}
