package config

import (
	"bytes"
	"io"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// ChangeField is one changed field of an entry.
type ChangeField struct {
	Field string `json:"field"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

// ConfigChange is one entry that apply adds, changes or removes.
type ConfigChange struct {
	Op      string        `json:"op" enum:"add,change,remove"`
	Section string        `json:"section"`
	Name    string        `json:"name"`
	Fields  []ChangeField `json:"fields"`
}

// fields returns the YAML form of each field of v, by YAML key.
func fields(v any) map[string]string {
	b, _ := yaml.Marshal(v)
	var m map[string]any
	_ = yaml.Unmarshal(b, &m)
	out := map[string]string{}
	for k, x := range m {
		y, _ := yaml.Marshal(x)
		out[k] = strings.TrimSpace(string(y))
	}
	return out
}

func compare(section, name string, old, next any) *ConfigChange {
	if next == nil {
		return &ConfigChange{Op: "remove", Section: section, Name: name, Fields: []ChangeField{}}
	}
	nf := fields(next)
	if old == nil {
		c := &ConfigChange{Op: "add", Section: section, Name: name}
		for _, k := range sortedKeys(nf) {
			c.Fields = append(c.Fields, ChangeField{Field: k, New: nf[k]})
		}
		return c
	}
	of := fields(old)
	c := &ConfigChange{Op: "change", Section: section, Name: name}
	keys := map[string]bool{}
	for k := range of {
		keys[k] = true
	}
	for k := range nf {
		keys[k] = true
	}
	for _, k := range sortedKeys(keys) {
		if of[k] != nf[k] {
			c.Fields = append(c.Fields, ChangeField{Field: k, Old: of[k], New: nf[k]})
		}
	}
	if len(c.Fields) == 0 {
		return nil
	}
	return c
}

func diffSection[T any](out *[]ConfigChange, section string, cur, next []T, name func(T) string, removable bool) {
	if next == nil {
		return
	}
	have := map[string]T{}
	for _, x := range cur {
		have[name(x)] = x
	}
	seen := map[string]bool{}
	for _, x := range next {
		n := name(x)
		seen[n] = true
		var old any
		if o, ok := have[n]; ok {
			old = o
		}
		if c := compare(section, n, old, x); c != nil {
			*out = append(*out, *c)
		}
	}
	if !removable {
		return
	}
	for _, x := range cur {
		if n := name(x); !seen[n] {
			*out = append(*out, ConfigChange{Op: "remove", Section: section, Name: n, Fields: []ChangeField{}})
		}
	}
}

// Diff lists what apply changes to make cur equal to next.
func Diff(cur, next Doc) []ConfigChange {
	out := []ConfigChange{}
	if next.Settings != nil {
		for _, k := range sortedKeys(next.Settings) {
			o, ok := cur.Settings[k]
			if ok && reflect.DeepEqual(norm(o), norm(next.Settings[k])) {
				continue
			}
			c := ConfigChange{Op: "change", Section: "settings", Name: k, Fields: []ChangeField{{Field: "value", New: yamlText(next.Settings[k])}}}
			if ok {
				c.Fields[0].Old = yamlText(o)
			} else {
				c.Op = "add"
			}
			out = append(out, c)
		}
	}
	diffSection(&out, "providers", cur.Providers, next.Providers, func(x Provider) string { return x.Name }, true)
	diffSection(&out, "profiles", cur.Profiles, next.Profiles, func(x Profile) string { return x.Name }, true)
	diffSection(&out, "mcp_servers", cur.MCPServers, next.MCPServers, func(x MCPServer) string { return x.Name }, true)
	diffSection(&out, "extensions", cur.Extensions, next.Extensions, func(x Extension) string { return x.Name }, true)
	diffSection(&out, "package_feeds", cur.PackageFeeds, next.PackageFeeds, func(x Feed) string { return x.Name }, true)
	diffSection(&out, "stores", cur.Stores, next.Stores, func(x Store) string { return x.Name }, true)
	diffSection(&out, "agent_images", cur.AgentImages, next.AgentImages, func(x AgentImage) string { return x.Name }, true)
	diffSection(&out, "repositories", cur.Repositories, next.Repositories, func(x Repository) string { return strings.ToLower(x.FullName) }, false)
	diffSection(&out, "team_roles", cur.TeamRoles, next.TeamRoles, func(x TeamRole) string { return x.Team }, true)
	return out
}

func yamlText(v any) string {
	b, _ := yaml.Marshal(v)
	return strings.TrimSpace(string(b))
}

// norm makes a JSON value and the same YAML value compare equal.
func norm(v any) any {
	b, _ := yaml.Marshal(v)
	var x any
	_ = yaml.Unmarshal(b, &x)
	return x
}
