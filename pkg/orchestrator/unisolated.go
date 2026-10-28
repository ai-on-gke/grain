package orchestrator

import (
	"context"
	"fmt"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/policy"
)

// unisolatedSandboxes is implemented by a Sandboxes backend that isolates
// nothing from the host this daemon runs on -- HostSandboxes, whose runs
// execute as the daemon's own user, within reach of its data directory,
// its keys and its logins. Only this machine's own tasks are dispatched
// to one (checkUnisolated), and only they get its operator's
// -host-sandbox-env (Shape.OperatorEnv).
//
// A wrapper around HostSandboxes that does not forward the method is
// read as isolated: its runs are not held, and get no operator env
// either, since Shape.OperatorEnv is only ever set here.
type unisolatedSandboxes interface {
	Unisolated() bool
}

func isUnisolated(sandboxes Sandboxes) bool {
	u, ok := sandboxes.(unisolatedSandboxes)
	return ok && u.Unisolated()
}

// localTask reports whether task's root requester is this machine's
// own (policy.Policy.IsOwn): rootRequester's walk up the chain it was
// derived from, so a proposal, port or review of a stranger's task is
// theirs too, and a chain that cannot be read is
// nobody's own.
func localTask(ctx context.Context, store *model.Store, p *policy.Policy, task model.Task) (bool, error) {
	r, err := rootRequester(ctx, store, task)
	if err != nil {
		return false, err
	}
	return p.IsOwn(r), nil
}

// checkUnisolated refuses task on a deployment whose sandboxes isolate
// nothing from this host unless its tree of work is this machine's own,
// or local policy says otherwise (policy.Policy.CheckUnisolated). It
// answers nil on every other backend.
func checkUnisolated(ctx context.Context, deps Deps, task model.Task) error {
	if !isUnisolated(deps.Sandboxes) {
		return nil
	}
	local, err := localTask(ctx, deps.Store, deps.Policy, task)
	if err != nil {
		return fmt.Errorf("local policy cannot tell whose task this is: %w", err)
	}
	return deps.Policy.CheckUnisolated(local)
}
