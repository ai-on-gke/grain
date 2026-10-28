package hostd

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// PlanEnsure is EnsureBuiltins without the file: data with every entry
// of builtins it has no capabilities entry for added, keyed by name. An
// entry already there is never changed, however it differs from grain's
// default -- that is reported in Differs instead -- and nothing else in
// the file moves: comments, indentation and order are the operator's.
// It returns data itself when nothing is missing, and when the file's
// shape is not one it edits (Manual says what to add by hand).
func PlanEnsure(data []byte, command string, builtins []BuiltinEntry) ([]byte, EnsureResult, error) {
	var res EnsureResult
	before, err := ParseAllowlist(data)
	if err != nil {
		return data, res, fmt.Errorf("not editing an allowlist hostd refuses: %w", err)
	}
	var missing []BuiltinEntry
	for _, b := range builtins {
		have, ok := before.Capabilities[b.Name]
		if !ok {
			missing = append(missing, b)
			continue
		}
		if d := differences(have, b.Entry(command)); d != "" {
			res.Differs = append(res.Differs, fmt.Sprintf("%s: %s", b.Name, d))
		}
	}
	if len(missing) == 0 {
		return data, res, nil
	}
	manual := func() ([]byte, EnsureResult, error) {
		res.Manual = renderEntries(missing, command, "  ", "  ")
		return data, res, nil
	}
	out, ok := insertEntries(data, missing, command)
	if !ok {
		return manual()
	}
	after, err := ParseAllowlist(out)
	if err != nil || !sameExcept(before, after, missing) {
		return manual()
	}
	for _, b := range missing {
		if e := after.Capabilities[b.Name]; e == nil || differences(e, b.Entry(command)) != "" {
			return manual()
		}
		res.Added = append(res.Added, b.Name)
	}
	return out, res, nil
}

func renderEntries(entries []BuiltinEntry, command, indent, step string) string {
	var s strings.Builder
	for _, b := range entries {
		s.WriteString(b.YAML(command, indent, step))
	}
	return s.String()
}

// sameExcept reports whether after is before plus added and nothing else.
func sameExcept(before, after *Allowlist, added []BuiltinEntry) bool {
	if !reflect.DeepEqual(before.CLIs, after.CLIs) || before.DrainCap != after.DrainCap {
		return false
	}
	if len(after.Capabilities) != len(before.Capabilities)+len(added) {
		return false
	}
	for name, e := range before.Capabilities {
		if !reflect.DeepEqual(e, after.Capabilities[name]) {
			return false
		}
	}
	return true
}

// insertEntries writes missing into data's capabilities mapping as text,
// matching the indentation the mapping already uses, and reports false
// for a shape it does not edit.
func insertEntries(data []byte, missing []BuiltinEntry, command string) ([]byte, bool) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
		lines[n-1] += "\n"
	}
	appendSection := func() ([]byte, bool) {
		return []byte(strings.Join(lines, "") + "capabilities:\n" + renderEntries(missing, command, "  ", "  ")), true
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return appendSection()
	}
	root := doc.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		return appendSection()
	}
	if root.Kind != yaml.MappingNode || root.Style&yaml.FlowStyle != 0 {
		return nil, false
	}
	at := -1
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "capabilities" {
			at = i
		}
	}
	if at < 0 {
		return appendSection()
	}
	key, val := root.Content[at], root.Content[at+1]
	indent, step := "  ", "  "
	keyLine := key.Line - 1
	// "capabilities:" (null), "capabilities: ~" or "capabilities: {}" on
	// its own line becomes a block mapping, keeping a comment it has.
	blockKey := func() bool {
		if val.Line != key.Line {
			return false
		}
		head := "capabilities:"
		if c := key.LineComment + val.LineComment; c != "" {
			head += " " + c
		}
		lines[keyLine] = head + "\n"
		return true
	}
	switch {
	case val.Kind == yaml.ScalarNode && val.Tag == "!!null":
		if val.Value != "" && !blockKey() {
			return nil, false
		}
	case val.Kind == yaml.MappingNode && val.Style&yaml.FlowStyle != 0:
		if len(val.Content) != 0 || !blockKey() {
			return nil, false
		}
	case val.Kind == yaml.MappingNode:
		first := val.Content[0]
		indent = strings.Repeat(" ", first.Column-1)
		if v := val.Content[1]; v.Kind == yaml.MappingNode && v.Style&yaml.FlowStyle == 0 && len(v.Content) > 0 && v.Content[0].Column > first.Column {
			step = strings.Repeat(" ", v.Content[0].Column-first.Column)
		}
	default:
		return nil, false
	}
	if key.Column != 1 {
		return nil, false
	}
	// After the mapping's last line of content: before the next top-level
	// key and any comments or blank lines that lead into it, or at the end
	// of the file.
	insert := len(lines)
	if at+2 < len(root.Content) {
		insert = root.Content[at+2].Line - 1
		if hc := root.Content[at+2].HeadComment; hc != "" {
			insert -= strings.Count(hc, "\n") + 1
		}
		floor := lastLine(val)
		if floor < key.Line {
			floor = key.Line
		}
		for insert > floor && strings.TrimSpace(lines[insert-1]) == "" {
			insert--
		}
		if insert < floor {
			return nil, false
		}
	}
	text := renderEntries(missing, command, indent, step)
	var out bytes.Buffer
	out.WriteString(strings.Join(lines[:insert], ""))
	out.WriteString(text)
	out.WriteString(strings.Join(lines[insert:], ""))
	return out.Bytes(), true
}

// lastLine is the last line (1-based) any node under n starts on.
func lastLine(n *yaml.Node) int {
	l := n.Line
	for _, c := range n.Content {
		if cl := lastLine(c); cl > l {
			l = cl
		}
	}
	return l
}

// errSymlink is EnsureBuiltins' refusal of a symlinked allowlist.
var errSymlink = errors.New("is a symlink; grain hostd refuses one, and grain will not write through it")
