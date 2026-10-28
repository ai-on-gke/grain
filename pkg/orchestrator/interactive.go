package orchestrator

import (
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// DefaultMaxInteractiveRuntime is Config.maxInteractiveRuntime's
// fallback: how long a run holding a conversation is allowed to last
// before grain ends it anyway.
//
// It is the answer to what a chat turn does to a run's budget, and the
// answer is that it does not come out of it. MaxRunRuntime is two hours
// of *working* -- sized against an agent nobody is watching, where every
// extra hour is an hour of unsupervised spend. A run that has said
// something and is waiting for a person to type is not spending anything
// at all: an agent blocked inside a tool call asks its provider for
// nothing, exactly as an idle console session does. One turn of chat can
// stand still for the best part of an hour (mcp.ChatWait), so a session
// held to two hours would be over after two exchanges somebody stepped
// away during -- which is not a shorter conversation, it is a
// conversation that stops working the moment it becomes one.
//
// Eight hours rather than the console's day because the two are not quite
// the same thing. A console session costs a sandbox and no tokens; this
// one has an agent in it that can also work between turns, so the number
// has to be small enough that a run which stops chatting and starts
// looping is bounded by something. It is a wall a session eventually
// meets rather than a budget it spends: what it protects against is a
// person who closed the tab, and what happens when it is reached is the
// ordinary parked ending -- the task keeps the conversation, reads
// awaiting_reply, and the next message dispatches a fresh run
// (mcp/chat_tool.go's own doc comment).
const DefaultMaxInteractiveRuntime = 8 * time.Hour

func (c Config) maxInteractiveRuntime() time.Duration {
	if c.MaxInteractiveRuntime > 0 {
		return c.MaxInteractiveRuntime
	}
	return DefaultMaxInteractiveRuntime
}

// sandboxClassFor is the Shape.Class a run of task is built with: a
// person waiting at the other end of an interactive task outranks every
// other sandbox, so on a cluster with no room left its pod preempts a CI
// job's rather than failing to schedule (K8sPriorityClasses).
func sandboxClassFor(task model.Task) SandboxClass {
	if task.Interactive {
		return SandboxClassInteractive
	}
	return SandboxClassAgent
}

// interactiveDispatch gives a task somebody is sitting with the longer
// clock a conversation needs, and every other task the deployment's own.
//
// Only ever upwards, and applied after consoleDispatch, so the two do not
// fight: a console session's day already covers anything a chat turn
// could wait for, and an interactive task that is also a console keeps
// it.
//
// Nothing else about the dispatch differs. The chat tool itself is turned
// on further down, where a run's tools are decided -- the forked
// mcpserver's own -interactive, from agent.RunConfig.Interactive -- and
// everything else about an interactive run (its sandbox, its checkout,
// its branch, the pull request its commits become) is the same machinery,
// for consoleDispatch's reason: RunDispatch cannot tell the two apart.
func interactiveDispatch(task model.Task, cfg Config) Config {
	if !task.Interactive {
		return cfg
	}
	if runtime := cfg.maxInteractiveRuntime(); runtime > cfg.maxRunRuntime() {
		cfg.MaxRunRuntime = runtime
	}
	return cfg
}
