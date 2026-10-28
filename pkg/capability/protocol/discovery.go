// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package protocol

import (
	"fmt"
	"strings"
)

const (
	mintPrefix    = "mint_"
	releasePrefix = "release_"
)

// Tool is one entry of a tools/list result, as much of it as discovery
// reads (§4.1.3); title, outputSchema and annotations are ignored.
type Tool struct {
	Name string `json:"name"`
	// Title is MCP's display name for the tool, where a server gives one.
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
	// Annotations are MCP's hints about the tool; grain reads only their
	// title, to say on a task's row what a call is doing
	// (EndpointRoster.Labels).
	Annotations *ToolAnnotations `json:"annotations,omitempty"`
}

// ToolAnnotations is the part of a tool's MCP annotations grain reads.
type ToolAnnotations struct {
	Title string `json:"title,omitempty"`
}

// MethodInfo is one capability method a server advertises.
type MethodInfo struct {
	Name        string
	Description string
	// ArgsSchema is inputSchema.properties.args, or nil when the mint
	// tool declares none (which ValidateArgs reads as "no arguments").
	ArgsSchema map[string]any
	// Releasable says the server advertises release_<Name>; a method
	// without it is stateless and its teardown is a no-op (§4.1.2).
	Releasable bool
}

// Discover reads a server's methods out of its tools/list (§4.1.2). Tools
// with neither verb prefix are not capability methods and are ignored. A
// mint_ tool with a non-conforming suffix or an unusable inputSchema, a
// duplicate mint_<method>, or a release_<method> without its mint_ is a
// discovery error: the server must not be attached.
func Discover(tools []Tool) (map[string]*MethodInfo, error) {
	methods := map[string]*MethodInfo{}
	var releases []string
	seenRelease := map[string]bool{}
	for _, t := range tools {
		switch {
		case strings.HasPrefix(t.Name, mintPrefix):
			name := strings.TrimPrefix(t.Name, mintPrefix)
			if !ValidMethod(name) {
				return nil, fmt.Errorf("tool %q: method %q does not match %s", t.Name, name, methodPattern)
			}
			if methods[name] != nil {
				return nil, fmt.Errorf("tool %q is advertised twice", t.Name)
			}
			args, err := mintArgsSchema(t)
			if err != nil {
				return nil, err
			}
			methods[name] = &MethodInfo{Name: name, Description: t.Description, ArgsSchema: args}
		case strings.HasPrefix(t.Name, releasePrefix):
			name := strings.TrimPrefix(t.Name, releasePrefix)
			if seenRelease[name] {
				return nil, fmt.Errorf("tool %q is advertised twice", t.Name)
			}
			if !requires(t.InputSchema, "attempt_id") {
				return nil, fmt.Errorf("tool %q: inputSchema must be an object requiring attempt_id", t.Name)
			}
			seenRelease[name] = true
			releases = append(releases, name)
		}
	}
	for _, name := range releases {
		m := methods[name]
		if m == nil {
			return nil, fmt.Errorf("tool %q has no matching %s%s", releasePrefix+name, mintPrefix, name)
		}
		m.Releasable = true
	}
	return methods, nil
}

// mintArgsSchema checks a mint_ tool's inputSchema (§4.1.3) and returns its
// properties.args.
func mintArgsSchema(t Tool) (map[string]any, error) {
	for _, field := range []string{"attempt_id", "deadline", "args"} {
		if !requires(t.InputSchema, field) {
			return nil, fmt.Errorf("tool %q: inputSchema must be an object requiring attempt_id, deadline and args", t.Name)
		}
	}
	props, _ := t.InputSchema["properties"].(map[string]any)
	raw, present := props["args"]
	if !present {
		return nil, nil
	}
	args, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tool %q: inputSchema.properties.args must be a schema object", t.Name)
	}
	if ty, ok := args["type"]; ok && ty != "object" {
		return nil, fmt.Errorf("tool %q: inputSchema.properties.args must have type object", t.Name)
	}
	// $defs a pydantic model emits sit at the inputSchema's root, not
	// args'; carry them across so ValidateArgs can resolve args' $refs
	// against the args schema alone.
	for _, key := range []string{"$defs", "definitions"} {
		if defs, ok := t.InputSchema[key]; ok {
			if _, own := args[key]; !own {
				merged := make(map[string]any, len(args)+1)
				for k, v := range args {
					merged[k] = v
				}
				merged[key] = defs
				args = merged
			}
		}
	}
	// A schema using a keyword ValidateArgs would not enforce is refused
	// at attach time rather than enforced as something looser (§4.3.2).
	args, err := NormalizeArgsSchema(args)
	if err != nil {
		return nil, fmt.Errorf("tool %q: inputSchema.properties.args: %v", t.Name, err)
	}
	return args, nil
}

func requires(schema map[string]any, field string) bool {
	if schema == nil {
		return false
	}
	if ty, ok := schema["type"]; ok && ty != "object" {
		return false
	}
	req, _ := schema["required"].([]any)
	for _, r := range req {
		if r == field {
			return true
		}
	}
	if req, ok := schema["required"].([]string); ok {
		for _, r := range req {
			if r == field {
				return true
			}
		}
	}
	return false
}
