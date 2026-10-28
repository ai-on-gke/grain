package lifecycle

// The one place this package and pkg/orchestrator meet.
//
// Neither imports the other, and that is worth keeping: pkg/lifecycle is
// policy over two interfaces (Images, Sandboxes) and stays testable at the
// speed policy deserves, while pkg/orchestrator owns VMs and knows nothing
// about task lifecycles. What joins them is this file, which turns a
// ServeRequest into whatever a backend's Serve takes and hands back what
// it returns.
//
// It is written against a function rather than an interface so that the
// caller supplying it -- cmd/grain, wiring a GranuleSandboxes in -- does the
// one import, and so that a deployment whose backend cannot serve reports
// that from the place that knows why (orchestrator.CannotServe) rather
// than from here.

import (
	"context"
	"fmt"

	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	lifecycleTokenEnv  = model.LifecycleTokenEnv
	lifecycleListenEnv = model.LifecycleListenEnv
)

// SandboxesFunc is a Sandboxes built from a function, for a caller that
// has a backend to adapt rather than a type to implement.
type SandboxesFunc func(ctx context.Context, req ServeRequest) (Serving, error)

func (f SandboxesFunc) Serve(ctx context.Context, req ServeRequest) (Serving, error) {
	return f(ctx, req)
}

// ServeEnvironment is the environment a serving sandbox's command runs
// with: the declaration's own literals, plus the two variables grain
// supplies.
//
// The token is the only thing authenticating the reducer, and the listen
// address is here so a server need not parse the declaration it was
// started from. Both are added after the declaration's own, and
// pkg/repodecl refuses a declaration that names either -- so this cannot
// be overwritten from a repository, and does not silently lose to one.
func ServeEnvironment(req ServeRequest) map[string]string {
	env := make(map[string]string, len(req.Declaration.Serve.Env)+2)
	for name, value := range req.Declaration.Serve.Env {
		env[name] = value
	}
	env[lifecycleTokenEnv] = req.Token
	env[lifecycleListenEnv] = req.Declaration.Serve.Listen
	return env
}

// UnsupportedSandboxes is a Sandboxes for a deployment whose backend
// cannot hold a serving sandbox.
//
// It exists so that such a deployment fails the way every other lifecycle
// failure fails -- held, with a reason on the repository's lifecycle, which
// an operator reads in the pane -- rather than by having a nil Sandboxes
// panic on the first observation of the first repository that declares one.
func UnsupportedSandboxes(reason error) Sandboxes {
	return SandboxesFunc(func(context.Context, ServeRequest) (Serving, error) {
		return nil, fmt.Errorf("no serving sandbox is available on this deployment: %w", reason)
	})
}
