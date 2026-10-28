package agent_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"testing"
)

// grain/task-398: what stops PublishedToolNames falling behind the
// server it describes.
//
// The list is computed from constructors rather than hand-copied, so it
// cannot misspell a tool -- but it can still miss one entirely, because
// missing one means nothing worse than not calling a constructor that
// `grain mcpserver` does call. That is the failure the hand-maintained
// copies had and the one this file is here to catch: a tool registered
// by the server, named by no framework's roster, and refused at the
// first call by claude's --strict-mcp-config (task 145 lost a CI triage
// to exactly that, with get_ci_log refused beside a working
// wait_for_checks).
//
// It compares the two files' *constructors*, not their tool names,
// because a constructor is the only handle a test has here: the names
// live inside functions this package cannot call without the daemon,
// sandbox and grants a real server is given. So the claim checked is
// narrow and mechanical -- "every tool-building function mcpserver.go
// calls, toolroster.go calls too, and nothing else" -- and it is the
// claim PublishedToolNames' own doc comment makes. The names those
// constructors return are covered next door, by
// toolroster_test.go's membership list, and end to end by
// tests/e2e/mcpserver_published_tools_test.go, which starts a real
// server and checks that nothing it advertises is absent from this list.
//
// It reads pkg/daemon/mcpserver.go rather than pkg/daemon as a whole: that
// file is the whole of the subcommand. pkg/daemon/capabilities.go also builds
// tools (grantTools, for orchestrator.Config.GrantTools), but those
// reach no agent at all -- no Framework left can consume in-process
// tools, see that field's own doc comment -- so they are deliberately
// unpublished, and run_host_command is not a name any roster should
// carry.
//
// Adding a tool means adding its constructor to both files, which is two
// appended lines in two places and no shared number or sentence to edit.
// A rewrite that defeats the pattern below -- registering tools from
// another file, or through a constructor not named *Tools -- fails here
// loudly rather than passing on having found nothing to compare.
func TestPublishedToolNamesCallsEveryConstructorTheServerRegisters(t *testing.T) {
	t.Parallel()
	server := toolConstructorsIn(t, filepath.Join("..", "daemon", "mcpserver.go"))
	roster := toolConstructorsIn(t, "toolroster.go")

	for _, name := range server {
		if slices.Contains(publishedByRegistration, name) {
			continue
		}
		if !slices.Contains(roster, name) {
			t.Errorf("pkg/daemon/mcpserver.go registers %s, which pkg/agent/toolroster.go never calls: "+
				"every tool it returns is a tool no run can be offered. Add it to PublishedToolNames.", name)
		}
	}
	for _, name := range roster {
		if !slices.Contains(server, name) {
			t.Errorf("pkg/agent/toolroster.go calls %s, which pkg/daemon/mcpserver.go never registers: "+
				"PublishedToolNames is publishing names no server advertises.", name)
		}
	}
}

// publishedByRegistration are the constructors whose names reach
// PublishedToolNames through RegisterPublishedToolNames rather than a
// call in toolroster.go, because their package imports this one: the
// agent API's generated tools, whose package checks the names are
// published (pkg/ui's TestGeneratedToolsMatchTheRouteTable).
var publishedByRegistration = []string{"ui.AgentAPITools"}

// toolConstructorsIn is every "pkg.SomethingTools(...)" call in one Go
// file, sorted and deduplicated, spelled the way the source spells it.
//
// Only calls qualified by one of the file's own imports count. That is
// what tells a tool constructor from the other things in these files
// whose names end in Tools -- registry.AnnounceRecordedTools, or
// mcpserver.go's own unexported helpers (pullRequestTools,
// grantedDaemonTools), whose bodies are in the same file and whose
// constructor calls are therefore found anyway.
func toolConstructorsIn(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	imported := map[string]bool{}
	for _, spec := range file.Imports {
		name := ""
		if spec.Name != nil {
			name = spec.Name.Name
		} else {
			// No alias: the package name is the last element of the
			// path for every import these two files make.
			quoted := spec.Path.Value
			name = filepath.Base(quoted[1 : len(quoted)-1])
		}
		imported[name] = true
	}

	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || !imported[pkg.Name] {
			return true
		}
		if len(sel.Sel.Name) < len("Tools") || sel.Sel.Name[len(sel.Sel.Name)-len("Tools"):] != "Tools" {
			return true
		}
		name := pkg.Name + "." + sel.Sel.Name
		if !slices.Contains(found, name) {
			found = append(found, name)
		}
		return true
	})

	if len(found) == 0 {
		t.Fatalf("found no tool constructors in %s at all -- either the file stopped building "+
			"its tools through exported pkg.SomethingTools calls, or this test is reading the "+
			"wrong file; whichever it is, it is comparing nothing", path)
	}
	slices.Sort(found)
	return found
}
