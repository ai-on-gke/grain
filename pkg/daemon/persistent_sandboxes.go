package daemon

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/tunnel/hostport"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// persistentSandboxes supervises this deployment's persistent sandboxes
// (model.PersistentSandbox): the reconcile loop's "persistent-sandboxes"
// step starts and stops them through it, and the UI reports and controls
// what it holds. Package-level for standingSandboxes' reason -- the loop
// and the UI server are built in different functions and must name one
// supervisor.
//
// A sandbox gets a fetch-only git proxy scope on its one repository from
// the registry CI jobs use, and its host port is bound on every interface
// of this host.
var persistentSandboxes = func() *orchestrator.PersistentSandboxSupervisor {
	s := orchestrator.NewPersistentSandboxSupervisor()
	s.GrantRead = CiReadScopes.Grant
	s.DropRead = CiReadScopes.Drop
	s.Ports = hostport.New("")
	return s
}()

// reservePersistentPorts tells the supervisor which ports grain itself
// listens on, so no persistent sandbox's host_port is bound over them.
func reservePersistentPorts(cfg Config) {
	var ports []int
	for _, addr := range []string{cfg.UiAddr, cfg.ExecutorAddr} {
		if _, p, err := net.SplitHostPort(addr); err == nil {
			if n, err := strconv.Atoi(p); err == nil && n != 0 {
				ports = append(ports, n)
			}
		}
	}
	persistentSandboxes.ReservedPorts = ports
}

// persistentSandboxAdapter is ui.PersistentSandboxes over the supervisor
// and the store its definitions are read from.
type persistentSandboxAdapter struct {
	store *model.Store
	sup   *orchestrator.PersistentSandboxSupervisor
}

var _ ui.PersistentSandboxes = persistentSandboxAdapter{}

func (a persistentSandboxAdapter) List(ctx context.Context) ([]model.PersistentSandboxStatus, error) {
	defs, err := a.store.ListPersistentSandboxes(ctx)
	if err != nil {
		return nil, err
	}
	return a.sup.List(defs), nil
}

func (a persistentSandboxAdapter) Logs(name string) (string, bool) { return a.sup.Logs(name) }

func (a persistentSandboxAdapter) Control(ctx context.Context, name, action string) error {
	def, err := a.store.GetPersistentSandbox(ctx, name)
	if err != nil {
		return err
	}
	if def == nil {
		return fmt.Errorf("no persistent sandbox %q is defined", name)
	}
	switch action {
	case "restart":
		return a.sup.Restart(name)
	case "rebuild":
		return a.sup.Rebuild(name)
	case "stop":
		a.sup.Stop(name)
		return nil
	case "start":
		a.sup.Start(name)
		return nil
	}
	return fmt.Errorf("unknown action %q", action)
}

func (a persistentSandboxAdapter) Dial(ctx context.Context, name string) (net.Conn, error) {
	return a.sup.Dial(ctx, name)
}

func (a persistentSandboxAdapter) StripPrefix(name string) (bool, bool) {
	return a.sup.StripPrefix(name)
}
