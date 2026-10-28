package forge_test

import (
	"go/build"
	"strings"
	"testing"
)

// forbidden is what this package may not depend on, at any depth, and
// why. docs/three-pieces.md is the argument; this is the part of it that
// fails a build rather than being remembered.
//
// pkg/model is the task model: tasks, observations, the store, and the
// state repository's schema. A forge that imports it is a forge that can
// read a task, and a forge that can read a task will eventually write
// one -- which is the upward pointer the whole split exists to prevent.
// The two model types this package genuinely needs the *shape* of,
// PrHealth and PullRequestRef, are duplicated here as Health and Ref
// with identical values, deliberately (see the package comment).
//
// pkg/orchestrator is the task loop itself, and the direction is the
// same one the other way round.
//
// pkg/ci is the awkward one, and it is forbidden for now rather than on
// principle: grain CI's verdict belongs to the forge conceptually, but
// pkg/ci currently keeps its runs in model.Store (docs/three-pieces.md's
// second thread), so importing it would drag pkg/model in behind it.
// When CI owns its own run store this entry is the one to revisit.
var forbidden = map[string]string{
	"github.com/ai-on-gke/grain/pkg/model":        "the task model: a forge that can read a task will eventually write one",
	"github.com/ai-on-gke/grain/pkg/orchestrator": "the task loop; the direction is tasks -> forge, never back",
	"github.com/ai-on-gke/grain/pkg/ci":           "reaches pkg/model for its run store, so it would drag the task model in",
}

// TestForgeDoesNotDependOnTasks walks this package's imports, transitively
// within this module, and fails naming the path that reached something
// forbidden -- because "pkg/forge imports pkg/model" is not the useful
// half of the message when the import is three packages deep.
func TestForgeDoesNotDependOnTasks(t *testing.T) {
	const root = "github.com/ai-on-gke/grain/pkg/forge"
	const module = "github.com/bwsalmon/grain/"

	seen := map[string]bool{}
	var walk func(path string, from []string)
	walk = func(path string, from []string) {
		if seen[path] {
			return
		}
		seen[path] = true
		if why, bad := forbidden[path]; bad {
			t.Errorf("%s reaches %s (%s).\n\ndocs/three-pieces.md: tasks point at the forge, the "+
				"forge points at sandboxes, nothing points up.", strings.Join(from, "\n  -> "), path, why)
			return
		}
		pkg, err := build.Import(path, "", 0)
		if err != nil {
			// A package that will not load is the compiler's problem to
			// report, not this test's: failing here as well would bury
			// the real error under a second one.
			return
		}
		for _, imp := range pkg.Imports {
			if !strings.HasPrefix(imp, module) {
				continue // the standard library, and this module has no others
			}
			walk(imp, append(append([]string{}, from...), imp))
		}
	}
	walk(root, []string{root})
}
