# The director

grain's standing interactive session: a chat that is always there, in the
rail under the inbox, with an agent that can read this deployment's own
record and file work against it.

Shipped and running (grain/task-415). This document is what it is, what
it may do, and the three decisions behind it that are not obvious from
the code.

## What it is

**One task that is never finished.** Everything else about the director
is an ordinary task: it is dispatched by `dispatch.Cycle`, it gets an
ordinary sandbox, it holds capabilities the same way, its runs are
recorded the same way, and what it proposes is a proposal like any other.
The exemption is `orchestrator.reconcileConsoleSessions`, and it is three
rules:

- **filed approved**, without anybody approving it — a session a person
  has to approve before they can talk to it is not there when they need
  it, and what would be approved was decided by whoever configured it;
- **never done** — a run that ends without asking a question would
  ordinarily complete the task, and a completed task cannot be spoken to
  again, so a completed session is un-completed and parked back on its
  own conversation;
- **never retired for failing** — three failed runs in a row take an
  ordinary task out of dispatch, which for a session would mean the chat
  quietly stopping answering.

**Parked between exchanges, not queued.** A queued session would be
dispatched by the very next cycle, complete, be re-queued, and be
dispatched again: an agent talking to itself with a sandbox each time.
Parked on its own conversation, it is dispatched exactly when somebody
says something, because a reply is what un-parks a task.

**It is a config row, not a task somebody filed.**
`model.Config.ConsoleSessions` names the sessions this deployment keeps
open; a deployment that has never chosen gets exactly one, the director.
More than one is allowed and costs nothing — each is a task with its own
conversation, found by its own tag.

## What a deployment chooses, and what grain chooses

The same split the memory agent's system template makes, for the same
reason. What the director *is* — the prompt saying what it is for, and
what to do rather than doing it in the chat — is code
(`model.DirectorSessionBody`), reviewed in this repository. A session
holding this deployment's own host under it must not be rewritable into a
different agent through a settings form.

What a deployment chooses is the envelope, and all of it is in the pane
the chat is in:

| setting | shipped as | what it decides |
|---|---|---|
| `Capabilities` | `api:self-debug` and `https://grain.dev/capabilities/grain#source` (formerly `self-debug`), `https://grain.dev/capabilities/grain#journal` (formerly `grain-log`), `api:deployment:read` and `…/grain#health` (formerly `deployment-health`), `https://grain.dev/capabilities/config-map#config-map={"config_maps":["bootstrap"]}` (formerly `bootstrap-playbooks`), `api:task-admin`, `api:capabilities:read`, `api:capabilities:detach`, `api:settings:read`, `api:repos:read`, `api:releases:read`, `api:schedules:read`, `api:templates:read`, `api:suites:read`, `api:dispatch:read` (plus any capability a wrapper distribution registers as on by default) | what the agent may reach |
| `ReadsAll` | true (every session) | whether it reads every repository this deployment works in |
| `DeclinedDefaults` | none | which of the defaults every session holds (`api:ci-read`) it opts out of |
| `NetworkAccess` | none (`model.DefaultConsoleSessions`, `director-capabilities.spec.md` §3.1.6). On a backend that cannot isolate a sandbox's network (host directory, container, plain Kubernetes pods), `model.ConsoleSessionNetworkAccess` files the director open instead, since that backend would refuse every exchange. A build that registers a default-on capability method needing egress from the sandbox itself (`capability.Method.SandboxEgress`) ships it open | whether its sandbox reaches the internet. The director's state-repository checkout, its `ReadsAll` clones and the in-sandbox `grain` CLI all go through the git proxy, and its `api_*` tools run on grain's side, so closing its network takes away nothing it uses |
| `Writes` | `grain/config` (the director; other sessions none) | where it may push: for the director, candidate `grain/config/<task-id>/*` branches for `propose_config_change` |
| `TasksApproved` | true (the director, `director-capabilities.spec.md` §3.1.5; other sessions false) | whether the tasks it files are queued or wait for a person |
| `Title` | "Director" | what the rail calls it |

**How a chat gets its name.** The director's title is the one above. A
session opened from "New session" has no title of its own when it is
created: it is named from its first message
(`model.ConsoleSessionTitleFromMessage` — the first sentence, cut to
eight words and `model.ConsoleSessionTitleMaxLength` runes), which is a
heuristic on an opening line and says so by being short. The agent on
the other end is what knows, a few turns in, what the conversation is
about, so a session's run is offered one tool an ordinary run is not:
`rename_session(title)` (pkg/mcp), registered by `grain mcpserver` only
for a task that is a console session and written by the daemon through
the same path `PUT /api/console-sessions/{id}` takes when a person edits
the title (`ui.Client.UpdateConsoleSession`, reached as
`PUT /api/tasks/{id}/session-title` so a run can only ever rename its
own). The reconciler carries the title onto the task as it does for any
other edit, and the rail's poll of the sessions list shows it at once.
A session records who chose its title (`ConsoleSession.TitleSource`):
`heuristic` when it was read off the first message, `person` when it
was typed on the new-session form or edited in the settings pane
(`PUT /api/console-sessions/{id}`), and `agent` once `rename_session`
has named it. `model.DirectorSessionBody` asks the agent to name the
conversation only while the source is `heuristic` — once, early in its
first exchange, with a short overview of the subject ("Nightly CI
failure" rather than "Hi, can you look at why the nightly") — and the
daemon refuses `rename_session` for a session an agent or a person has
already named, so a later run whose turn is on another topic cannot
retitle the chat. A person can still rename any session from the UI.
Sessions saved before the field existed count as `heuristic` only if
their title is exactly what their first message would give, and as
`person` otherwise; the director's name is the deployment's and is never
offered for renaming.

Two capabilities are deliberately not shipped on and are one tick away:
**`https://grain.dev/capabilities/grain#host-exec`** (formerly `self-repair`),
which lets a run change things on grain's own host and is the one grant here
that can break the deployment it is talking about, and **`api:memory`**
(formerly `memory`), which writes the standing instructions every later run is
given.

A wrapper distribution can register further capability methods as on by default
for the director (`pkg/capability.Method.DefaultOn`); each shows in
the director's pane like any other, and can be unticked there. A deployment
that already saved its `director` row before newer shipped default
capabilities existed keeps its saved list rather than having new grants
added silently; the director's pane offers **Adopt shipped defaults**
(`adoptShippedDefaults: true` on `PUT /api/console-sessions/director`),
which adds missing shipped defaults while keeping custom grants
(`expectCapabilities` guards capability saves against concurrent edits with
`409` `code: "stale"`).

## What every session reads by default

A session exists to answer "what is going on in this deployment", so every
session — the director and any session an operator creates, including ones
stored before this default existed — reads all of it without anybody adding
repositories one at a time (`pkg/model/console_session_reach.go`):

- **Every repository this deployment works in.** Each `repo_config` row,
  plus each repository `target_repos` names outright (not by pattern), less
  any whose work ownership is `off` (retired) or that `target_repos` does
  not allow (`model.ConsoleSessionWorkingRepos`). This is resolved again
  on every cycle, so a repository added to `repo_config` later is readable
  on the next one and a retired one stops being readable. These are
  reads: the session may clone and fetch, never push. The state
  repository and the session's own `Reads` still apply on top. The pane
  lists them apart from the reads a session names itself (`autoReads` on
  `GET /api/console-sessions`). Set `readsAll: false` (or `reads_all:
  false`) on the session in `console_sessions` to opt it out. The opt-out
  is a setting, read every cycle, so it holds until the setting changes.
- **CI for every repository.** `api:ci-read`, unscoped, granted
  `GrantByDefault`, so `grain ci list/show/log` and `grain pr status
  --task/--ref` read grain CI and GitHub Actions runs on every repository.
  This is not `grain-ci`, which is a repository's CI Mode and cannot be
  attached by hand. A session's task is given the grant once
  (`ConsoleSessionConfigSnapshot.OfferedDefaults`), so an operator who
  removes it from the task keeps it removed across cycles and restarts of
  the daemon. Unticking it in the session's pane also records it in the
  session's `declinedDefaults`, so it stays off when the session's task is
  filed again. Ticking it brings it back.

Writes are never a default, with one exception. A session files tasks
rather than pushing, and what it may push to is `Writes`, named one
repository at a time. The shipped director holds `grain/config` there, so
it is offered `propose_config_change`: its `/home/debian/grain-state`
checkout may push candidate branches under `grain/config/<task-id>/`
(never the default branch), each validated, classified by the trust
boundary, and landed only on a person's approval of its pinned head. That
write is not passed on: any proposal whose repo or `writes` names
`grain/config` is filed for a person whatever the session's permission
(`model.ConfigRepoProposalHold`).

`TasksApproved` ships `true` on the director (`director-capabilities.spec.md`
§3.1.5) and `false` on other sessions; a deployment that saved its own
`console_sessions` row keeps what it saved (`model.ConsoleSessionsOr`), and
unticking the box (or setting `model.ConsoleSessionTasksUnapprovedTag` on the
active task) turns it off. Because a chat is a fast way to ask for a lot of
work, what auto-queueing may set going is bounded per turn and held whenever a
proposal widens reach or targets `grain/config`.

Ticked, it is one predicate — `ConsoleSession.QueuesProposals` — and
three readers: the prompt the session is given
(`model.DirectorSessionBody`), the gate on a proposal filed while its run
is still going (`ui.Client.CreateTask`, which is the path a session's
proposals actually take), and the gate on one replayed at the end of a
run (`orchestrator.relayProposedTasks`). It is written that way because
it was not: the middle one did not exist, so the director was told its
tasks were queued and every one of them was filed `proposed`
(grain/task-311). A proposal queued this way is approved as
`ConsoleSessionPrincipal`, is not bounded by `model.AutoSubmitLimits`'
depth or lifetime fan-out — every task a session files is one somebody
just asked for in a message, and bounding those over a day would bound
the person — but is bounded per conversation turn: past
`AutoSubmitLimits.TurnFanOut` (10) proposals since the last message a
person sent the session, the rest are filed for a person with a note
saying why. It inherits no auto-merge,
since a session that may queue work has been granted nothing about
anybody's branch.

The permission is a ceiling the session may stay under, not a decision
taken out of its hands (grain/task-439). `propose_task` takes
`hold_for_approval`: a held proposal is filed `proposed` whatever the
permission would have done, in both filing paths, so one conversation can
file work that runs on its own beside work that waits for the operator.
It only ever lowers what the call may do, and applies to that call alone
— what the held task proposes in turn is judged on its own permission.
A proposal the permission would have queued and that was held anyway is
tagged `model.HeldForApprovalTag`, carries a note in its body, and its
row reads "held for approval by <task>", so the operator can tell a
deliberate hold from a proposal the permission did not cover. The tool
result says which happened and why — queued on the session's permission,
held on request, or what the permission does not cover
(`model.AutoApprovalRefusal`) — and the prompt asks the session to say in
the chat which it chose.

Who may tick it is anybody with the member or admin role on the
session's team, not only a system administrator (grain/task-3797). A
member already approves that team's proposals, and the tick grants
nothing past that: a proposal that asks for a capability or repository
the session does not hold is still filed for a person. The capabilities,
network access and repositories themselves stay a system
administrator's to set. A viewer or proposer on the team can read the
session's conversation but cannot send it anything or change it.

## Three decisions worth knowing about

**The capabilities are in the chat pane rather than in Settings.** Half
of what anybody asks a director is something it can only do with a
capability it may not have, and the answer — "I would need self-repair
for that" — is useless if acting on it means finding a dialog and a tab
inside it. The grant is a tick box under the message that asked for it,
and the reconciler carries it onto the session's task. A run keeps the
capabilities, repositories, network access and model it started with,
though, and a session's run can stay open across many messages while its
agent waits for a reply. So a change saved while a run is open reaches
the agent when its next run starts: the pane says so, and offers **Apply
now**, which stops the open run (the conversation stays) so the next
message starts a fresh one with what was saved.

**It is a chat, not a terminal.** grain has both: `pkg/console` runs an
agent's own CLI on a pseudo-terminal for a task with `hosting: console`
(formerly the `console` capability). A standing session is the other one — comments in, comments
out, the same conversation grain already carries in both directions —
because that is what survives a run ending, reads on a phone, and needs
no session manager to still exist tomorrow. `TaskChat.jsx`'s own doc
comment has the full trade.

**It keeps its sandbox and agent warm between exchanges for an idle TTL.**
Acquiring a fresh VM/pod, re-cloning, reinstalling packages, and rebuilding
the full prompt on every message is too slow for an interactive conversation.
Instead, when a session turn ends (`person-closes` goal or awaiting reply),
grain keeps the session warm in two layers controlled by **Settings →
Sandboxes → Session sandbox idle TTL** (`grain_config.session_sandbox_idle_ttl_ms`,
CLI flag `-session-sandbox-idle-ttl`, default `30m`; set to `-1s` to disable):

1. **In-process session continuation (`continueSessionTurns`):** Where the
   agent framework returns a resumable session token (`ResumableSession`) and
   the run is executing asynchronously, ending a turn relays the agent's
   reply into the conversation immediately, marks the sandbox as an `"idle
   session"`, and keeps the run waiting in-process up to the idle TTL for the
   person's next message. When a reply arrives within the TTL, the same run
   resumes the CLI session in-process with the new message — no new dispatch,
   no sandbox acquisition, no clone, and no prompt rebuild.
2. **Warm sandbox pool fallback (`session_sandboxes.go`):** When a run does
   end (usage limit, crash, daemon restart, or synchronous single-turn cycle)
   while the session rests awaiting a reply, the orchestrator holds the
   sandbox in a warm pool for up to the idle TTL instead of releasing it. A
   new message within the TTL dispatches onto the *same* sandbox (preserving
   its filesystem, installed packages, clones, and uncommitted changes), and
   the prompt (`BuildPrompt`, `BuildSuspendedPrompt`, and
   `agent.SuspendedReusedSandboxPreamble`) explicitly tells the agent that
   **the sandbox is the same one** so it does not redo setup.

A session's run is admitted the moment it is ready, however full the
worker pool and the sandbox budget are. Only the host disk floor, a cluster
with no node for the pod, or having no connection to run its model on can
hold it, and a refusal is logged as a warning that names the gate
([Sandbox budget](sandbox-budget.md#sessions-are-never-held-by-the-budget)).

Held session sandboxes are counted honestly against the deployment's sandbox
budget and `MaxWorkers`, shown on **System → Sandboxes** with kind and status
`"idle session"`, and released when the idle TTL expires, when the session
task is closed or paused, or under capacity pressure (where the oldest idle
session sandbox is evicted first so new work can be admitted). When a warm
sandbox has been reclaimed before the next message arrives, the next dispatch
acquires a fresh sandbox and its prompt tells the agent that **the sandbox is
not the same one**.

## Acting through grain's API

The director can do anything a person can do in the UI if its session
holds the scope. It asks for scopes as `api:<scope>` or `api:<preset>`
capabilities, granted in the session's capability pane, and any such
grant attaches the `grain-controller` MCP server, whose
`grain_explore_scope` and `grain_invoke` reach every route under the
scopes it holds. For example:

- `api:task-admin` to reorder, edit, retry and close tasks and re-run
  their CI;
- `api:ci-read` to read job logs;
- `api:release-manager` to run schedules and cut and promote release
  candidates.

Its prompt tells it to ask the person for a scope by name rather than
work around a missing one. It also says that dangerous scopes, and every
call under one, need a person and a reason. Every call is attributed to
the session's task. See [agent-api.md](agent-api.md).

Five scopes are out of the director's reach whatever it is granted:
`merge:force`, `settings:write`, `secrets:write`, `connections:write` and
`repos:write`. This is grain's built-in floor for the director
(`model.DirectorFloorRefusal`;
[director-capabilities.spec.md](../design/spec/director/director-capabilities.spec.md)
§3.3, G7), and it applies with or without a `settings/capabilities.yml`.
It covers every grant reaching one of those scopes, including a preset
or scope set that expands to one, such as `api:grain/operator`. Grain
refuses such a grant when it is attached to the director's task, refuses
to save it in the session pane, holds the director at dispatch while it
holds one, and refuses those routes at the API gate. A rule in
`settings/capabilities.yml` can take more away from the director, but it
cannot give these back.

## Starting a fresh one

Close the session's task. grain does not reopen a conversation somebody
deliberately ended, but the session is still configured, so the next
cycle files a new task for it: a fresh conversation, with the title,
body, capabilities, network access and repositories rebuilt from the
same settings row, and the rail's entry pointing at it as soon as it
exists. The closed task keeps its whole record — every message and every
run — and is readable like any other task, linked from the new one
(`model.LinkContinues`, provenance only: it blocks nothing, or the fresh
session would never be dispatched).

One thing does travel: the most recent handover the closed
conversation's runs wrote (`model.Comment.Handover`), posted onto the
new task by grain itself and marked a handover there too, so the one
message written to survive a thread being cut survives this as well.
Nothing else of the old thread comes with it — carrying more would carry
exactly what the conversation bound exists to stop carrying, and the old
chat is one click away.

The chat pane offers this rather than leaving you to find the close:
beside the capabilities toggle, a "Start a fresh chat" button appears
when the last exchange says the conversation is costing its runs — a
prompt past `model.PromptWarnTokens`, or a thread grain had to cut the
middle out of — and never on how old the chat is. It says what a fresh
one costs and what it keeps before it closes anything, and grain never
takes the decision on anybody's behalf. That is the whole of what "split
this chat" means here, and it is why there is no separate mechanism for
it.

## Unification: The Director as the `grain/config` Repo Manager

The Director's standing session model is the foundational primitive for all repository
governance across Grain:
- **The Director is the Repo Manager for `grain/config`**: It operates as the root of the
  governance hierarchy, managing deployment-wide settings, repository onboarding, global
  quotas, and operator conversations.
- **Application Repo Managers**: Every registered repository receives an equivalent standing,
  long-lived Repo Manager session (specialized by `.grain/manager.md`), driving task triage,
  CI diagnostics, speculative merge queues, and two-sandbox dynamic preview provisioning.

