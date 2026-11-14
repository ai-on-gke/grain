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

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/agent/claude"
	"github.com/ai-on-gke/grain/pkg/agent/codex"
)

// The host CLI's spec (design/spec/cli/host.spec.md) carries one generated
// flag table per command, between
//
//	<!-- BEGIN GENERATED FLAGS: grain <path> -->
//	<!-- END GENERATED FLAGS -->
//
// TestHostCLISpecCoversEveryCommandAndFlag walks the commands this binary
// registers -- the argv[1] switches in Main and dispatchCLI, read out of
// main.go so a new command cannot be missed, plus hostSpecSubcommands --
// runs each with -h in a child of this test binary, and reads the flags
// out of the flag package's own PrintDefaults output. A command or flag
// the code has and the spec does not, or the reverse, fails it.
//
// GRAIN_UPDATE_CLI_SPEC=1 rewrites the tables in place (a command the
// spec has no table for is appended under "Unplaced commands", to be moved
// next to its prose).
const hostSpecPath = "../../design/spec/cli/host.spec.md"

// hostSpecSubcommands are the verbs a command takes as its own first
// argument, each with a flag set of its own.
var hostSpecSubcommands = map[string][]string{
	"access-code":        {"show", "rotate"},
	"capability-servers": {"list", "add", "remove", "enable", "disable", "bind-secret"},
	"ci":                 {"run", "list", "show", "watch", "wait", "retrigger", "test-history", "cancel", "cache", "faults", "artifact"},
	"connections":        {"import"},
	"controller":         {"bootstrap-github-app"},
	"declarations":       {"check"},
	"fs":                 {"ls", "cat", "read", "grep"},
	"gcp-key":            {"list", "get", "set", "delete"},
	"host": {"list", "pending", "accept", "reject", "codes", "invite", "rotate-code", "revoke-code",
		"rename", "update", "revoke", "delete", "delete-revoked", "rekey", "register", "install", "set-owner"},
	"init": {"workstation"},
	"pr":   {"adopt", "review", "status", "submit"},
	"qa":   {"status", "enable", "disable"},
	"hub": {"status", "list", "directory", "route", "show", "create", "register", "unregister",
		"update", "scopes", "rotate-credential", "upgrade", "resize", "delete", "restore", "stop", "start", "transfer", "accept-transfer",
		"delegating-apps list", "delegating-apps create", "delegating-apps disable", "delegating-apps enable",
		"grants list", "grants create", "grants revoke"},
	"release": {"approve", "reject", "rollback"},
	"repo": {"list", "add", "remove", "capabilities", "ci-capabilities", "prompt-extension",
		"setup-command", "unfixable-checks", "work-ownership", "workflows"},
	"runhost":       {"exec"},
	"sandbox":       {"probe"},
	"sandbox-image": {"check", "contract"},
	"secrets":       {"set", "delete", "list", "mint-gemini-key", "repo"},
	"setup":         {"gcp"},
	"skill":         {"install", "status"},
	"state": {"status", "repo", "sync", "resets clear", "reformat", "restore-snapshot", "prepare-upgrade",
		"check", "key show", "key path", "key import", "key retired", "key retired delete", "key wrap", "key unwrap"},
	"upgrade": {"status", "start", "cancel", "rollback"},
}

// hostSpecNoHelp are commands -h cannot be asked of: hooks that read a
// request on stdin and answer it whatever their arguments say.
var hostSpecNoHelp = []string{antigravity.HookSubcommand, claude.StopHookSubcommand, codex.DetachedSubcommand}

// hostSpecTopLevel reads the command names main.go's two switches match.
func hostSpecTopLevel(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	consts := map[string]string{
		"antigravity.HookSubcommand": antigravity.HookSubcommand,
		"claude.StopHookSubcommand":  claude.StopHookSubcommand,
		"codex.DetachedSubcommand":   codex.DetachedSubcommand,
	}
	seen := map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || (fn.Name.Name != "Main" && fn.Name.Name != "dispatchCLI") {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			// Only a switch on the command word: args[0] or cmd.
			tag := ""
			switch e := sw.Tag.(type) {
			case *ast.IndexExpr:
				if id, ok := e.X.(*ast.Ident); ok {
					tag = id.Name
				}
			case *ast.Ident:
				tag = e.Name
			}
			if tag != "args" && tag != "cmd" {
				return true
			}
			for _, s := range sw.Body.List {
				for _, e := range s.(*ast.CaseClause).List {
					switch v := e.(type) {
					case *ast.BasicLit:
						name, _ := strconv.Unquote(v.Value)
						seen[name] = true
					case *ast.SelectorExpr:
						key := v.X.(*ast.Ident).Name + "." + v.Sel.Name
						name, ok := consts[key]
						if !ok {
							t.Fatalf("main.go switches on %s: add it to hostSpecTopLevel's consts", key)
						}
						seen[name] = true
					}
				}
			}
			return true
		})
	}
	var names []string
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

type specFlag struct {
	Name, Type, Default, Env, Usage string
}

var (
	defaultsFlagLine = regexp.MustCompile(`^  -([^\s,]+)(?: (\S.*))?$`)
	shortFlagLine    = regexp.MustCompile(`^  -(\S)\t(.*)$`)
	usageDefault     = regexp.MustCompile(`\s*\(default (.*)\)$`)
	envMention       = regexp.MustCompile(`\$(GRAIN_[A-Z0-9_]+|[A-Z][A-Z0-9_]*_(?:TOKEN|AUDIENCE|USER|DIR|ENV))`)
)

// parseDefaults reads the flags out of flag.PrintDefaults output: a
// one-letter flag's line carries its usage after a tab, and any other
// flag's line is followed by usage lines indented "    \t". A usage text
// written by hand (aligned columns, "-in, -input") matches neither, and
// is left to TestHostCLISpecNamesEveryDefinedFlag.
func parseDefaults(out string) []specFlag {
	var flags []specFlag
	lines := strings.Split(out, "\n")
	for i := 0; i < len(lines); i++ {
		var f specFlag
		var usage []string
		if m := shortFlagLine.FindStringSubmatch(lines[i]); m != nil {
			f = specFlag{Name: m[1], Type: "bool"}
			usage = append(usage, m[2])
		} else if m := defaultsFlagLine.FindStringSubmatch(lines[i]); m != nil && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "    \t") {
			f = specFlag{Name: m[1], Type: "bool"}
			if m[2] != "" {
				f.Type = m[2]
			}
		} else {
			continue
		}
		for i+1 < len(lines) && strings.HasPrefix(lines[i+1], "    \t") {
			i++
			usage = append(usage, strings.TrimSpace(lines[i]))
		}
		u := strings.Join(usage, " ")
		// The flag package quotes a string's default; a parenthesis that
		// is not that shape is the usage text's own words.
		if dm := usageDefault.FindStringSubmatch(u); dm != nil && ((f.Type != "string" && f.Type != "value") || strings.HasPrefix(dm[1], `"`)) && !strings.Contains(dm[1], "; ") {
			f.Default = dm[1]
			u = strings.TrimSpace(u[:len(u)-len(dm[0])])
		}
		// A back-quoted word in a usage replaces the type name -h prints
		// (flag.UnquoteUsage), so the type is read off the default.
		if !slices.Contains([]string{"bool", "string", "int", "int64", "uint", "uint64", "float", "duration", "value"}, f.Type) {
			switch {
			case f.Default == "true" || f.Default == "false":
				f.Type = "bool"
			case regexp.MustCompile(`^-?[0-9]+$`).MatchString(f.Default):
				f.Type = "int"
			case strings.HasPrefix(f.Default, `"`):
				f.Type = "string"
			default:
				f.Type = "value"
			}
		}
		var envs []string
		for _, em := range envMention.FindAllStringSubmatch(u, -1) {
			if !slices.Contains(envs, "$"+em[1]) {
				envs = append(envs, "$"+em[1])
			}
		}
		f.Env = strings.Join(envs, ", ")
		f.Usage = u
		flags = append(flags, f)
	}
	return flags
}

// hostSpecHelp runs grain <path> -h in a child of this test binary.
func hostSpecHelp(t *testing.T, dir string, path []string) string {
	t.Helper()
	key := strings.Join(path, " ")
	hostSpecHelpCache.Lock()
	cached, ok := hostSpecHelpCache.m[key]
	hostSpecHelpCache.Unlock()
	if ok {
		return cached
	}
	argv, _ := json.Marshal(append(slices.Clone(path), "-h"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHostCLISpecHelper$")
	// Anything the child left holding its output open must not keep
	// CombinedOutput waiting past the deadline.
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = dir
	cmd.Env = []string{
		"GRAIN_CLISPEC_ARGV=" + string(argv),
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + filepath.Join(dir, "home"),
		"GRAIN_SANDBOX_CLI=0",
		"GRAIN_CLI_ENV=",
		"GRAIN_DATA_DIR=" + filepath.Join(dir, "data"),
		"XDG_CONFIG_HOME=" + filepath.Join(dir, "config"),
	}
	out, _ := cmd.CombinedOutput()
	if ctx.Err() != nil {
		// Say so, rather than leave a cut-off help to read as missing flags.
		t.Errorf("grain %s -h did not finish within 30s; it printed:\n%s", key, out)
		return string(out)
	}
	help := strings.ReplaceAll(strings.ReplaceAll(string(out), filepath.Join(dir, "data"), "$GRAIN_DATA_DIR"), dir, "<tmp>")
	hostSpecHelpCache.Lock()
	hostSpecHelpCache.m[key] = help
	hostSpecHelpCache.Unlock()
	return help
}

// hostSpecHelpCache keeps each command's -h, which hostSpecHelp has
// already made independent of the directory it ran in, so the spec test
// and the help-wording test (host_wording_test.go) start every child once
// between them rather than once each: under -race on a loaded machine the
// children are most of either test's time.
var hostSpecHelpCache = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

// TestHostCLISpecHelper is the child hostSpecHelp runs: grain itself.
func TestHostCLISpecHelper(t *testing.T) {
	raw := os.Getenv("GRAIN_CLISPEC_ARGV")
	if raw == "" {
		t.Skip("only runs as TestHostCLISpecCoversEveryCommandAndFlag's child")
	}
	var argv []string
	if err := json.Unmarshal([]byte(raw), &argv); err != nil {
		t.Fatal(err)
	}
	os.Args = append([]string{"grain"}, argv...)
	Main()
	os.Exit(0)
}

var globalFlagLine = regexp.MustCompile(`^  -([a-z-]+)(?: (string|duration|int))?\s+(.*)$`)

// globalSpecFlags reads runCLI's global flags out of its usage text, which
// is written by hand rather than printed by the flag package.
func globalSpecFlags() []specFlag {
	var flags []specFlag
	_, rest, _ := strings.Cut(usage, "Global flags")
	rest, _, _ = strings.Cut(rest, "Commands:")
	for _, line := range strings.Split(rest, "\n") {
		m := globalFlagLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		f := specFlag{Name: m[1], Type: m[2]}
		if f.Type == "" {
			f.Type = "bool"
		}
		flags = append(flags, f)
	}
	return flags
}

// hostSpecTables is what the code says: every command path's flags.
func hostSpecTables(t *testing.T) map[string][]specFlag {
	dir := t.TempDir()
	for _, d := range []string{"home", "data", "config"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tables := map[string][]specFlag{}
	// The global flags runCLI parses before a task command.
	tables["grain"] = globalSpecFlags()
	var paths [][]string
	for _, top := range hostSpecTopLevel(t) {
		if slices.Contains(hostSpecNoHelp, top) {
			tables["grain "+top] = nil
			continue
		}
		paths = append(paths, []string{top})
		for _, sub := range hostSpecSubcommands[top] {
			paths = append(paths, append([]string{top}, strings.Fields(sub)...))
		}
	}
	// One child per path, a few at a time: under -race each is slow.
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, path := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			flags := parseDefaults(hostSpecHelp(t, dir, path))
			mu.Lock()
			tables["grain "+strings.Join(path, " ")] = flags
			mu.Unlock()
		}()
	}
	wg.Wait()
	for top := range hostSpecSubcommands {
		if _, ok := tables["grain "+top]; !ok && !slices.ContainsFunc(hostSpecTopLevel(t), func(s string) bool { return s == top }) {
			t.Errorf("hostSpecSubcommands names %q, which main.go does not dispatch", top)
		}
	}
	return tables
}

var (
	generatedBegin = regexp.MustCompile(`(?m)^<!-- BEGIN GENERATED FLAGS: (.+?) -->\n`)
	generatedEnd   = "<!-- END GENERATED FLAGS -->"
	tableFlag      = regexp.MustCompile("(?m)^\\| `-{1,2}([^` ]+)`")
)

// renderFlagTable is one command's generated block body.
func renderFlagTable(flags []specFlag) string {
	if len(flags) == 0 {
		return "_No flags._\n"
	}
	cell := func(s string) string {
		s = strings.ReplaceAll(s, "|", `\|`)
		s = strings.ReplaceAll(s, "`", "'")
		return s
	}
	var b strings.Builder
	b.WriteString("| Flag | Type | Default | Env | Description |\n|---|---|---|---|---|\n")
	for _, f := range flags {
		def := ""
		if f.Default != "" {
			def = "`" + strings.ReplaceAll(f.Default, "`", "'") + "`"
		}
		fmt.Fprintf(&b, "| `-%s` | %s | %s | %s | %s |\n", f.Name, cell(f.Type), strings.ReplaceAll(def, "|", `\|`), f.Env, cell(f.Usage))
	}
	return b.String()
}

// specBlocks reads every generated block: path -> [start, end) of its body.
func specBlocks(t *testing.T, spec string) (map[string][2]int, []string) {
	t.Helper()
	blocks := map[string][2]int{}
	var order []string
	for _, m := range generatedBegin.FindAllStringSubmatchIndex(spec, -1) {
		path := spec[m[2]:m[3]]
		end := strings.Index(spec[m[1]:], generatedEnd)
		if end < 0 {
			t.Fatalf("%s: block %q has no %s", hostSpecPath, path, generatedEnd)
		}
		if _, dup := blocks[path]; dup {
			t.Errorf("spec has two generated blocks for %q", path)
		}
		blocks[path] = [2]int{m[1], m[1] + end}
		order = append(order, path)
	}
	return blocks, order
}

// updateSpecBlocks rewrites spec's blocks from tables, appending any
// missing under heading.
func updateSpecBlocks(t *testing.T, spec string, tables map[string]string) string {
	blocks, order := specBlocks(t, spec)
	for i := len(order) - 1; i >= 0; i-- {
		path := order[i]
		body, ok := tables[path]
		if !ok {
			body = "_Not registered by the code any more: remove this command._\n"
		}
		r := blocks[path]
		spec = spec[:r[0]] + body + spec[r[1]:]
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
			spec += fmt.Sprintf("\n### `%s`\n\n<!-- BEGIN GENERATED FLAGS: %s -->\n%s%s\n", path, path, tables[path], generatedEnd)
		}
	}
	return spec
}

// compareSpecBlocks reports every command and flag on one side only.
func compareSpecBlocks(t *testing.T, specPath, spec string, want map[string][]string) {
	t.Helper()
	blocks, _ := specBlocks(t, spec)
	for path, flags := range want {
		r, ok := blocks[path]
		if !ok {
			t.Errorf("%s: the code registers %q, which the spec has no generated flag block for", specPath, path)
			continue
		}
		var got []string
		for _, m := range tableFlag.FindAllStringSubmatch(spec[r[0]:r[1]], -1) {
			got = append(got, m[1])
		}
		for _, f := range flags {
			if !slices.Contains(got, f) {
				t.Errorf("%s: %q takes -%s, which its spec table does not list", specPath, path, f)
			}
		}
		for _, f := range got {
			if !slices.Contains(flags, f) {
				t.Errorf("%s: %q's spec table lists -%s, which the code does not take", specPath, path, f)
			}
		}
	}
	for path := range blocks {
		if _, ok := want[path]; !ok {
			t.Errorf("%s: the spec has a block for %q, which the code does not register", specPath, path)
		}
	}
	if t.Failed() {
		t.Log("regenerate the tables with GRAIN_UPDATE_CLI_SPEC=1 go test ./pkg/cli ./pkg/sandboxcli -run CLISpec, then describe the change in the prose beside them")
	}
}

func TestHostCLISpecCoversEveryCommandAndFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the grain binary once per command")
	}
	tables := hostSpecTables(t)
	raw, err := os.ReadFile(hostSpecPath)
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	if os.Getenv("GRAIN_UPDATE_CLI_SPEC") == "1" {
		rendered := map[string]string{}
		for path, flags := range tables {
			rendered[path] = renderFlagTable(flags)
		}
		if err := os.WriteFile(hostSpecPath, []byte(updateSpecBlocks(t, spec, rendered)), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want := map[string][]string{}
	for path, flags := range tables {
		want[path] = []string{}
		for _, f := range flags {
			want[path] = append(want[path], f.Name)
		}
	}
	compareSpecBlocks(t, hostSpecPath, spec, want)
}

// flagDefiners are the flag.FlagSet methods that define a flag, with the
// index of the flag's name among their arguments.
var flagDefiners = map[string]int{
	"String": 0, "Bool": 0, "Int": 0, "Int64": 0, "Uint": 0, "Uint64": 0, "Float64": 0, "Duration": 0, "Func": 0, "BoolFunc": 0,
	"StringVar": 1, "BoolVar": 1, "IntVar": 1, "Int64Var": 1, "UintVar": 1, "Uint64Var": 1, "Float64Var": 1, "DurationVar": 1, "Var": 1, "TextVar": 1,
}

// staticFlagNames is every flag name pkg/cli's own code defines with a
// literal, whichever command it belongs to: the backstop for a command
// whose -h prints a usage of its own rather than the flag package's
// defaults, which hostSpecTables cannot read flags out of.
func staticFlagNames(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			i, ok := flagDefiners[sel.Sel.Name]
			if !ok || len(call.Args) < i+2 {
				return true
			}
			// A flag-package global (flag.String) is not a command's flag.
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "flag" {
				return true
			}
			lit, ok := call.Args[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			name, _ := strconv.Unquote(lit.Value)
			if name == "" || strings.ContainsAny(name, " /:") {
				return true
			}
			if _, seen := names[name]; !seen {
				names[name] = fset.Position(lit.Pos()).String()
			}
			return true
		})
	}
	return names
}

// Every flag pkg/cli defines is named in the host spec somewhere -- in a
// generated table or in the prose of a command whose usage is its own.
func TestHostCLISpecNamesEveryDefinedFlag(t *testing.T) {
	raw, err := os.ReadFile(hostSpecPath)
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	for name, pos := range staticFlagNames(t) {
		if !strings.Contains(spec, "`-"+name+"`") && !strings.Contains(spec, "`-"+name+" ") && !strings.Contains(spec, "`--"+name) {
			t.Errorf("%s defines -%s, which %s never names", pos, name, hostSpecPath)
		}
	}
}
