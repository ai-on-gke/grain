package cli

// A force merge is an operator's action and nobody else's
// (grain/task-479, ui.Client.ForceMerge). This is that property as a
// test rather than as a sentence somebody has to read first.
//
// The risk is specific and it is structural. `grain mcpserver` is the
// process that serves a dispatched run's own tools, and it already holds
// a ui.HTTPClient to reach the daemon with -- the same client that now
// carries ForceMerge. Every tool a run is ever given is registered in
// mcpserver.go or comes out of pkg/mcp, so a tool that called it would be
// one line in one of those files and would look exactly like the dozen
// legitimate ones beside it. What it would mean is a run able to merge
// its own change past the checks, which is not an override of the queue's
// judgement at all: it is the judgement being made by the thing being
// judged.
//
// Scoped to those two, deliberately. cmd/grain's own `force-merge`
// subcommand is a person at a shell and is meant to exist; the UI's
// button and route are the same person through a browser. It is the
// agent-facing surface that must stay empty, and this says so of the
// agent-facing surface alone.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoAgentFacingToolCanForceAMerge(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	files := []string{filepath.Join(root, "pkg", "cli", "mcpserver.go")}
	entries, err := os.ReadDir(filepath.Join(root, "pkg", "mcp"))
	if err != nil {
		t.Fatalf("reading pkg/mcp: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(root, "pkg", "mcp", name))
	}
	if len(files) < 10 {
		t.Fatalf("scanned %d file(s), want the whole agent-facing surface: the paths are wrong", len(files))
	}
	for _, path := range files {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, forbidden := range []string{"ForceMerge", "force-merge", "force_merge"} {
			if strings.Contains(string(body), forbidden) {
				t.Errorf("%s mentions %q. A force merge is an operator's action: grain performs "+
					"one on an operator's recorded override and on nothing else, so no tool a run "+
					"is handed may ask for one. If this is deliberate, it is a decision about who "+
					"may override the merge predicate and it needs a person, not a passing test.",
					path, forbidden)
			}
		}
	}
}
