package daemon

import (
	"context"
	"log"
	"slices"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
)

// hostdDrainHold is why a run on frameworks has to wait because its CLI
// runs on the host through grain hostd and hostd is draining for its own
// upgrade (hostdUpdating), or "" when it can be dispatched. Every one of
// frameworks has to run on the host for the run to wait: frameworks are
// the ones the run might be built on (AgentPool.Availability), and one
// that runs in the container can start whatever hostd is doing.
//
// Without it, each dispatch of a queued host run during a drain built a
// sandbox, started the CLI, was refused by hostd (agent.ErrHostdDraining)
// and ended interrupted, once a cycle for as long as the drain lasted.
// Held here instead, the task waits in the queue saying why and starts
// once, on the hostd that replaces this one.
//
// A hostd that does not answer holds nothing: that run fails saying so,
// as Settings says a host run does while hostd is down. asked is whether
// hostd was asked at all.
func hostdDrainHold(ctx context.Context, live Config, frameworks ...string) (why string, asked bool) {
	if len(frameworks) == 0 {
		return "", false
	}
	for _, fw := range frameworks {
		if !runsThroughHostd(live, fw) {
			return "", false
		}
	}
	return askHostdDraining(ctx, live, cliBinaryName(model.NormalizeAgentFrameworkName(frameworks[0])))
}

// runsThroughHostd is whether a run on framework fw starts its CLI on the
// host through grain hostd, and so cannot start while hostd drains.
func runsThroughHostd(live Config, fw string) bool {
	exec := live.hostExecutor()
	if exec.Kind != HostExecutorHostd {
		return false
	}
	fw = model.NormalizeAgentFrameworkName(fw)
	switch location, _ := live.CLIRunSetting(fw); location {
	case model.CLIRunHost:
		ok, _ := exec.runsOnHost(fw)
		return ok
	case model.CLIRunDetached:
		ok, _ := exec.runsDetached(fw)
		return ok
	}
	return false
}

// askHostdDraining asks hostd whether it is draining, saying why cli has
// to wait if it is. asked is whether hostd answered.
func askHostdDraining(ctx context.Context, live Config, cli string) (why string, asked bool) {
	hctx, cancel := context.WithTimeout(ctx, hostdStatusTimeout)
	defer cancel()
	peer, err := hostd.Client{Socket: live.hostExecutor().Socket, Build: daemonBuild()}.Hello(hctx)
	if err != nil || !peer.Draining {
		return "", err == nil
	}
	return cli + " runs on the host, and " + hostdUpdating, true
}

// hostdDrainingFrameworks is, while grain hostd drains, the frameworks of
// live's connections whose CLI it would have started, and why a run on
// them waits; nil and "" otherwise. The pool passes those connections
// over (candidatesLocked), so a run that may also go to a connection
// running its CLI in the container goes there instead of being refused by
// hostd, and one that may not waits saying why. hostd is asked only when
// some connection runs through it.
func (p *AgentPool) hostdDrainingFrameworks(ctx context.Context, live Config) (map[string]bool, string) {
	var onHost []string
	for _, b := range live.AgentBackends {
		fw := model.NormalizeAgentFrameworkName(b.Framework)
		if !slices.Contains(onHost, fw) && runsThroughHostd(live, fw) {
			onHost = append(onHost, fw)
		}
	}
	if len(onHost) == 0 {
		return nil, ""
	}
	why, asked := askHostdDraining(ctx, live, cliBinaryName(onHost[0]))
	p.noteHostdHold(why, asked)
	if why == "" {
		return nil, ""
	}
	draining := make(map[string]bool, len(onHost))
	for _, fw := range onHost {
		draining[fw] = true
	}
	return draining, why
}

// hostdHold is hostdDrainHold for the pool, logging once when host runs
// start waiting on a drain and once when they stop, rather than once a
// task a cycle for as long as the drain lasts.
func (p *AgentPool) hostdHold(ctx context.Context, live Config, frameworks ...string) string {
	why, asked := hostdDrainHold(ctx, live, frameworks...)
	p.noteHostdHold(why, asked)
	return why
}

// noteHostdHold logs a drain hold starting (why set) or ending (hostd
// asked, and no longer draining), each once.
func (p *AgentPool) noteHostdHold(why string, asked bool) {
	if why != "" {
		if !p.hostdDraining.Swap(true) {
			log.Printf("grain daemon: holding host runs in the queue, or running them on connections in the container: %s", why)
		}
		return
	}
	if asked && p.hostdDraining.Swap(false) {
		log.Printf("grain daemon: grain hostd is no longer draining; host runs dispatch again")
	}
}
