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

package sandboxcli

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The in-sandbox CLI's spec (design/spec/cli/sandbox.spec.md) carries one
// generated flag table per command, between
//
//	<!-- BEGIN GENERATED FLAGS: grain <command> -->
//	<!-- END GENERATED FLAGS -->
//
// built from sandboxCommandFlags, the list run refuses every other flag
// against, so the table is exactly what a command accepts. A command or
// flag the code has and the spec does not, or the reverse, fails
// TestSandboxCLISpecCoversEveryCommandAndFlag; GRAIN_UPDATE_CLI_SPEC=1
// rewrites the tables (pkg/cli's clispec_test.go does the same for the
// host CLI).
const sandboxSpecPath = "../../design/spec/cli/sandbox.spec.md"

var (
	specBlockBegin = regexp.MustCompile(`(?m)^<!-- BEGIN GENERATED FLAGS: (.+?) -->\n`)
	specBlockEnd   = "<!-- END GENERATED FLAGS -->"
	specTableFlag  = regexp.MustCompile("(?m)^\\| `-{1,2}([^` ]+)`")
)

// sandboxSpecTables is every command path and the flags it takes.
func sandboxSpecTables() map[string][]string {
	tables := map[string][]string{"grain help": nil, "grain cap": nil, "grain screenshot": nil}
	for cmd, flags := range sandboxCommandFlags {
		tables["grain "+cmd] = slices.Clone(flags)
	}
	return tables
}

func sandboxFlagType(name string) string {
	switch {
	case sandboxBoolFlags[name]:
		return "bool"
	case slices.Contains(sandboxCountFlags, name):
		return "count (non-negative integer)"
	}
	return "string"
}

func renderSandboxTable(cmd string, flags []string) string {
	usage := strings.SplitN(sandboxCommandHelp[strings.TrimPrefix(cmd, "grain ")], "\n", 2)[0]
	var b strings.Builder
	if usage != "" {
		fmt.Fprintf(&b, "Usage: `%s`\n\n", usage)
	}
	if len(flags) == 0 {
		b.WriteString("_No flags._\n")
		return b.String()
	}
	b.WriteString("| Flag | Type |\n|---|---|\n")
	for _, f := range flags {
		dash := "--"
		if len(f) == 1 {
			dash = "-"
		}
		fmt.Fprintf(&b, "| `%s%s` | %s |\n", dash, f, sandboxFlagType(f))
	}
	return b.String()
}

func TestSandboxCLISpecCoversEveryCommandAndFlag(t *testing.T) {
	raw, err := os.ReadFile(sandboxSpecPath)
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	tables := sandboxSpecTables()
	type block struct{ start, end int }
	blocks := map[string]block{}
	var order []string
	for _, m := range specBlockBegin.FindAllStringSubmatchIndex(spec, -1) {
		path := spec[m[2]:m[3]]
		end := strings.Index(spec[m[1]:], specBlockEnd)
		if end < 0 {
			t.Fatalf("%s: block %q has no %s", sandboxSpecPath, path, specBlockEnd)
		}
		blocks[path] = block{m[1], m[1] + end}
		order = append(order, path)
	}
	if os.Getenv("GRAIN_UPDATE_CLI_SPEC") == "1" {
		for i := len(order) - 1; i >= 0; i-- {
			path, b := order[i], blocks[order[i]]
			body := "_Not registered by the code any more: remove this command._\n"
			if flags, ok := tables[path]; ok {
				body = renderSandboxTable(path, flags)
			}
			spec = spec[:b.start] + body + spec[b.end:]
		}
		var missing []string
		for path := range tables {
			if _, ok := blocks[path]; !ok {
				missing = append(missing, path)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			spec = strings.TrimRight(spec, "\n") + "\n\n## Unplaced commands\n"
			for _, path := range missing {
				spec += fmt.Sprintf("\n### `%s`\n\n<!-- BEGIN GENERATED FLAGS: %s -->\n%s%s\n", path, path, renderSandboxTable(path, tables[path]), specBlockEnd)
			}
		}
		if err := os.WriteFile(sandboxSpecPath, []byte(spec), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	for path, flags := range tables {
		b, ok := blocks[path]
		if !ok {
			t.Errorf("%s: the sandbox CLI answers %q, which the spec has no generated flag block for", sandboxSpecPath, path)
			continue
		}
		var got []string
		for _, m := range specTableFlag.FindAllStringSubmatch(spec[b.start:b.end], -1) {
			got = append(got, m[1])
		}
		for _, f := range flags {
			if !slices.Contains(got, f) {
				t.Errorf("%s: %q takes --%s, which its spec table does not list", sandboxSpecPath, path, f)
			}
		}
		for _, f := range got {
			if !slices.Contains(flags, f) {
				t.Errorf("%s: %q's spec table lists --%s, which the code does not take", sandboxSpecPath, path, f)
			}
		}
	}
	for path := range blocks {
		if _, ok := tables[path]; !ok {
			t.Errorf("%s: the spec has a block for %q, which the sandbox CLI does not answer", sandboxSpecPath, path)
		}
	}
	if t.Failed() {
		t.Log("regenerate the tables with GRAIN_UPDATE_CLI_SPEC=1 go test ./pkg/cli ./pkg/sandboxcli -run CLISpec, then describe the change in the prose beside them")
	}
}
