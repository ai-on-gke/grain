# Three pieces

grain is one binary because a single operator on one VM is what it was
built for, and that is still right. But it is three things, and this
document says which three, which direction they depend on each other,
and how far the code already agrees -- so that the next thing to leave
the process (a public-facing receiver for CI verdicts, as it happens)
leaves along a boundary that was already there rather than one drawn
for it.

The three:

- **sandboxes** -- give me a machine, with this shape, and take it back.
- **forge** -- what is true about this branch, may it land, and land it.
  Git hosting, the merge, and CI, since CI's output is a verdict on a
  commit.
- **tasks** -- what should happen next. The task model, dispatch, the
  agent frameworks, the UI, and every decision that names a task.

## The direction

```
tasks ──▶ forge ──▶ sandboxes
  └───────────────▶ sandboxes      (an agent run needs one too)
```

Nothing points up. The forge does not know what a task is; sandboxes do
not know what a branch is. This is the one rule, and everything below is
either evidence that the code already keeps it or the list of places it
does not.

The reason it is the rule and not a preference: a piece that only points
down can be replaced, run elsewhere, or tested alone, and a piece that
points up cannot -- it drags the thing above it along.

## Where the code already agrees

Checked by reading imports rather than by intuition.

**Sandboxes are done.** `pkg/granule`, `pkg/sandboximage`
import nothing from `pkg/model`.

**The forge is nearly clean.** `pkg/github` imports no `model`.
`pkg/mcp` does not either, which is why `grain mcpserver` already runs
as a separate process talking to the daemon over HTTP.

**The task loop already consumes the other two through interfaces.**
`orchestrator.Sandboxes`, `Deps.CI`, `github.Client`,
`gitproxy.Authorizer`, `gitproxy.Forwarder`, `agent.Framework`. Nothing
in the cycle reaches into a concrete sandbox or forge type. A process
boundary is therefore a new implementation of an interface that exists,
not an extraction.

## Where it does not: three threads

Each is a place the forge points up at tasks. Named precisely because
they are the whole of the work.

1. **`gitproxy` asks the task model who a sandbox is.** `ModelAuthorizer`
   resolves sandbox → run → task → `Target` and `Reads`, and that is the
   authorization decision for every fetch and push. It is already behind
   the `Authorizer` interface, and `ReadOnlyScopeAuthorizer` is already a
   second implementation, so the shape is right; the store read is what
   has to become a question asked of the task side.

2. **`ci` keeps its runs in the task store.** `model.CIRun` and
   `runstore_stored.go`, plus `model.RepoWorkflow` for the workflows
   declared on grain's side rather than in a repository. Half of CI's
   state is already off-store -- `DirCache` is deliberately "not a table
   in model.Store" (pkg/ci's own doc comment) -- and the other half
   should follow. Runs are ephemeral; they leave the state repository's
   export when they leave the store, and that is a scope decision to
   make on purpose rather than a side effect to discover.

3. **`orchestrator/sync.go` is both pieces in one function.** `syncEntry`
   reads a pull request's health and, in the same 200 lines, writes task
   comments, files repair tasks and escalates to a person. This is the
   one place the split cuts *through* code rather than along a seam, and
   the one refactor that actually costs something.

## The merge queue, which is the thing to get right

The queue has two halves, and they belong to different pieces.

*What is true and what may land* -- the health of a branch, whether it
merges cleanly, doing the merge -- is the forge's. *Which branch is next,
and what to do when it is red* -- `queueOrder` is keyed by task ID,
`requeueForRepair` files a task, `escalateToUser` writes a comment -- is
tasks'.

So the forge offers three things about a branch and no more:

```
Health(branch)     what CI and the base say about it
Mergeable(branch)  clean, conflicted, behind
Merge(branch)      land it, or say why not
```

and the task side decides. That is a smaller forge than "the merge queue
lives in the forge" and the right one: the larger version would have the
forge filing repair tasks, which is pointing up.

`queueEntry.observe` is already very nearly the first half on its own --
it reads and returns, and everything that acts is in `syncEntry` after
it. The split is mostly a matter of not letting the second call the
store.

## Separable is not separate

Two different things, and the README's "one container onto one VM" is
right to hold the line on the second: three daemons to babysit is a real
cost for one operator.

But that is a deployment choice. Multiple binaries can ship in one
container under one supervisor. The target is **separable by construction** -- the
direction above, kept by a test that fails when `pkg/forge/...` imports
`pkg/model`, which is cheap and permanent -- with deploying separately as
an option exercised when a piece needs it.

One piece needs it now. The receiver that GitHub Actions reports
verdicts to (docs/leaving-the-github-api.md) is the one public-facing
thing this deployment will have, and it should not share a process with
every credential grain holds. It is the first piece of the forge to live
on its own, and the reason to have the boundary drawn before it does.

## What to do first

Not a split. Fix the direction inside the module, in this order:

1. **`sync.go`** into a forge-side `Observe(branch) → Health` with no
   store access, and a task-side loop that acts on it.
2. **`gitproxy.Authorizer`** gains an implementation that asks the daemon
   over HTTP, beside `ModelAuthorizer`.
3. **CI owns its run store.**
4. **The import test.**

Then the receiver, in its own process, against a boundary that exists.

## Not decided

- **Whether git and CI are one piece or two.** They are one here because
  a verdict is forge vocabulary and because CI's consumer is the merge.
  The argument for two is only that CI is the heaviest user of sandboxes
  and might want to scale alone. Nothing above depends on the answer.
- **Where the forge's refs live.** docs/leaving-the-github-api.md takes
  that up; this document only requires that whoever answers
  `Health`/`Mergeable`/`Merge` does not need a task to do it.
