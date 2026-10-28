package cli

// grain CI, wired to the machines this deployment actually has
// (pkg/ci is the engine; this is everything it needs a daemon for).
//
// pkg/ci states its own seams and imports neither the orchestrator nor
// the git proxy, deliberately: an agent run's sandbox hands out MCP
// tools and a git credential, a CI job's hands out a shell and a
// checkout, and a package that low has no business knowing how either is
// built. This file is where both types are in scope -- the same argument
// sandboxHealthAdapter and cycleTimesAdapter already make -- so this is
// where the adapter lives.
//
// CI capability authority comes only from operator-owned repo settings, never
// from workflow files or the invoking agent's grants. The adapter gives jobs a
// read-only checkout and provisions the repo's explicit CI capabilities using
// the same providers as agent runs. With no CI grants it places no material;
// ci_hostile_test.go exercises that default with a hostile workflow.

import (
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/daemon"
)

// ciGate is the whole of ci.Runner, which is what the UI/API server and
// the cycle are both handed, checked here rather than at the assignment
// so a change to either end fails to compile in this file.
var _ ci.Runner = daemon.CiGate{}
