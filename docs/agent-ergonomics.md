# Agent ergonomics: what a run actually hits, and what to do about it

**Status: proposal**, except where a finding is marked **Done** with the
task that landed it. This is a review of grain's agent-facing
surface — the tools a dispatched run holds, the
prompt it is given, and what grain records about how it went — written
from the code as it stands, with a proposal per finding. The follow-up
tasks filed alongside it are named at the bottom.

The question behind it is narrow: **where does a run lose turns, context
or work to grain itself, rather than to the task it was given?** Every
finding below is something a competent agent cannot fix from inside the
sandbox, because it is grain that is telling it the wrong thing, telling
it nothing, or throwing away the evidence that would say which.

The findings are ordered by what they cost a run, not by how hard they
are to fix. The first four are all cheap.

---

## 1. The escape-hatch tools tell every production run their calls were mocked

`ask_question`, `comment_on_issue` and `propose_task` answer a real
dispatch with, respectively:

- `"Recorded (mocked -- no GitHub comment was posted). In a real dispatch
  this ends your turn and waits on a human reply; treat it the same way
  here."` (`pkg/mcp/mock_tools.go:122`)
- `"Recorded (mocked -- no GitHub comment was posted)."` (`:158`)
- `"Recorded (N proposed task(s) so far this run, mocked -- no GitHub
  issue was filed)."` (`:243`)

None of that is true of a real run. `cmd/grain/mcpserver.go` registers
`mcp.NewMockTools(&mcp.MockSink{})` with a sink nobody reads, but the
calls are not recovered from that sink: `orchestrator.ProcessResult`
reads them off `agent.Result.ToolCalls`, parsed out of the CLI's own
transcript, and relays each one for real — a question becomes a
`model.Comment` and parks the task on it, a comment becomes the task's
closing note, a proposal becomes a real `model.Task` with real
`depends_on` edges (`pkg/orchestrator/finish.go`, `ProcessResult` and
`relayProposedTasks`). The sink's "mocked" wording is a leftover from
when there was genuinely nothing downstream of it, and the comment in
`mcpserver.go` saying so is still there.

The descriptions have the same drift in the other direction: they
describe v1's workflow — a GitHub issue, a trigger label to re-apply, an
issue filed per proposal — and grain has had none of those since tasks
became rows. A task's conversation is in grain's own UI, and that is
where the human replies.

What it costs: an agent that is told its question went nowhere has three
bad options and takes one of them — ask again, work around the block it
was supposed to park on, or downgrade the question into its final text,
which nothing relays at all. The same goes for a closing comment on an
investigation task: told the comment was mocked, the sensible move looks
like writing the answer into a file nobody will read.

**Proposal.** Rewrite the four descriptions and the four confirmation
texts to describe what v2 actually does, in v2's vocabulary (task,
conversation, task queue — not issue, label, GitHub). Keep the "this ends
your turn" contract on `ask_question`, which is real. `MockSink` stays
exactly as it is; it is a test seam, and the fix is to stop narrating the
seam to the agent.

**Done** (grain/task-149). All four texts now describe the relay: a
question is relayed into the task's own conversation when the run
finishes and parks the task there until a human replies, whose reply
requeues it; a comment lands in the same conversation and is the closing
note when nothing was pushed and nothing asked; a proposal is filed as a
real task, unapproved, that a human approves in grain's UI. Each of the
three also says that only the *first* such call in a run is relayed
(`firstToolCallArg`), which nothing said before. `MockSink` is unchanged
and is no longer mentioned to the agent at all;
`pkg/mcp/mock_tools_test.go` pins both the wording that has to be there
and the v1 vocabulary that must not come back, because nothing else in
the repo fails when this drifts — the cost lands only on a run that
believes it.

## 2. `add_review_comment` is registered on every run and relayed by nothing

Its description promises "a draft code review ... posted once you finish
... a human opens the draft review and submits it themselves".
`ProcessResult`'s own doc comment says the truth: *"`add_review_comment`
is not relayed here at all"* — there is no PR in hand on an ordinary
dispatch, and the `/review-intent` dispatch that would have one is not
built. A run that spends twenty turns writing careful review feedback
gets that feedback counted in its outcome detail and discarded.

**Proposal.** Two halves, either of which is an improvement on its own:

- Register it only for a dispatch that is actually a review — which means
  `grain mcpserver` needs a flag saying so, the same way
  `-pr-repo`/`-pr-branch` scope the CI tools. A tool that is not on the
  roster costs nothing; a tool that is on the roster and lies costs a
  run's whole budget.
- Until review dispatches exist, have its confirmation text say where the
  feedback really lands (this run's outcome, and nowhere else), so an
  agent can decide to put it in a `comment_on_issue` call instead.

**Done** (grain/task-149), the second half: both its description and its
confirmation stopped promising a draft review nobody would open.

**Done** (grain/task-322), the thing behind it. Review dispatches exist
now (grain/task-284), and a review task has a pull request two links
away — `LinkProposedBy` to the task it reviews, `LinkFixes` from there to
the pull request — so `orchestrator.relayReviewFeedback` posts a run's
`add_review_comment` calls as a real draft review on it, repeats them on
the reviewed task's own conversation (a draft is visible only to the
credential that created it), and takes that change off automatic merge
until a human has read them. A run with no pull request behind it has
them relayed into its own task's conversation rather than dropped. The
texts say both destinations.

The first half — registering the review wording only for a dispatch that
is actually a review, behind a `grain mcpserver` flag — is still not
done, and is now worth doing for the first time: something could set that
flag. What it would buy is a description that names *this* run's pull
request instead of describing two cases, which is all a static
description can do from a process that cannot see the store.

## 3. A `run_command` that timed out does not say so

Both transports bound the command — `defaultRunCommandTimeout` is five
minutes and applies even when the caller passes no `timeout`
(`pkg/mcp/sandbox_tools.go`) — and neither tells the agent when that
bound is what ended the command:

- local (`sandbox_tools.go:187`): the context deadline kills the process
  group, `cmd.Run` returns a killed-by-signal `*exec.ExitError`, and the
  answer is `exit=-1` with whatever partial output there was. `exit=-1`
  is also what "the command could not be started at all" looks like.
- remote (`ssh_tools.go:65`): the guest-side `timeout N bash -c ...`
  exits 124, and the answer is a bare `exit=124`.

Neither answer names the timeout, says what it was, or says that a larger
one can be asked for — and since the default applies when the agent
passed nothing, the number that just killed its build is one it never
chose and cannot see.

What it costs: the failure mode is re-running the same long command
verbatim, twice, before concluding something is wrong with the sandbox.
It is the single cheapest fix in this document.

**Proposal.** In both handlers, when the command was ended by the bound
rather than by itself, append one line: *"the command was killed after
300s (run_command's default bound); pass a larger `timeout` — up to
600000 ms — or narrow the command"*. Locally that is
`ctx.Err() == context.DeadlineExceeded`; remotely it is exit 124, which
is `timeout`'s own reserved status. `runCommandTimeout` already computes
the number, and both handlers already share it.

**Done** (grain/task-150). `runCommandBound`
(`pkg/mcp/run_command_result.go`) carries the resolved bound *and* where
it came from, and both handlers append its notice when the bound is what
ended the command: "Killed after 300s by run_command's default bound,
which this call did not choose — it passed no `timeout` … Pass a larger
`timeout` (milliseconds, up to 600000) or narrow the command", or the
same line naming the call's own `timeout` when it had one. Locally the
trigger is `ctx.Err()` being `DeadlineExceeded` *and* the bound having
actually elapsed, so a run-deadline cancellation that fired first is not
reported as this bound and cannot name the wrong number; remotely it is
exit 124, joined by the exit 137 that finding 5's SIGKILL escalation
produces. See README.md's "What `run_command` says when it, not the
command, ended it".

## 4. No tool result has an upper bound on its size

`run_command` returns the whole of stdout and stderr
(`sandbox_tools.go:187`, `ssh_tools.go:65`). `read_file` with no `limit`
returns the whole file, line-numbered (`numberedRange`,
`sandbox_tools.go:210`). Nothing anywhere caps either.

One `go test ./...` on a failing suite, one `git log -p`, one `grep -r`
that matched more than expected, one 250 KB `README.md` read without a
`limit` — each of those is a large fraction of the run's context spent in
a single turn, and the run cannot get it back.

What happens past that point is currently left to whichever CLI is
driving the run. `claude` caps an MCP tool's output itself
(`MAX_MCP_OUTPUT_TOKENS`) and rejects a result that exceeds it, so an
oversized answer costs the run the wall clock the command took and buys
it nothing at all; `agy` is a different CLI with its own answer. grain
already reaches into exactly this set of knobs where it needs a
guarantee — `mcpToolTimeout` sets `MCP_TOOL_TIMEOUT` so that a
`wait_for_checks` call is not killed before it can report
(`pkg/agent/claude/claude.go:331-363`) — and sets nothing for output. So
the size at which a tool result stops working is a per-framework default
grain has not chosen, does not know, and cannot explain to the agent when
it is hit.

Capping in the server fixes it once for every framework, and is the only
place that can produce the thing that makes a truncated answer usable: a
statement of what was cut and how to get the rest.

**Proposal.** Bound every tool result where it is formatted, keeping the
head and the tail and saying what was dropped: *"... 412 KB elided (of
430 KB); re-run narrowed, or redirect to a file and read it with
`read_file`'s `offset`/`limit`"*. Both `run_command` handlers already
build their text with the same format string, and `numberedRange` is
already shared by both `read_file`s, so this is two call sites and a
helper. Size the cap from the data in finding 11 rather than from taste;
64 KB is a defensible starting guess.

**Done** (grain/task-150), at 64 KB (`mcp.maxToolResultBytes`,
`pkg/mcp/result_size.go`) and at those two call sites. `run_command`'s
two streams share the one budget rather than getting one each, with a
stream that fits lending its remainder to the other; head and tail are
kept, never just the head, since the verdict is in the last lines and the
invocation in the first. The notice between them says how much went, that
grain cut it rather than the command, and the call that gets it back —
narrow the command or redirect it to a file, and for `read_file` the
`cat -n` numbering either side of the cut is the `offset` and `limit` to
ask for, which is why the cut snaps to line boundaries. The number was a
guess and deliberately a `var`, for finding 11's telemetry to set.

**Re-sized** (grain/task-183) to 16 KB, from that telemetry over the 90
days to 2026-09-04 — 23 runs of this deployment, 1,254 `run_command` and
`read_file` answers. 64 KB had never once fired (the largest answer in
the window was 44,860 bytes), so nothing was ever elided and the
question of whether a run takes the notice's advice went unanswered
rather than answered badly; `run_command` is at p95 `<=8191` and p50
`<=1023`, and `read_file`, the heavier tail, at p95 `<=32767`. 16 KB is
also antigravity's own per-result default, and `agy` is the default
framework — which is what settles one cap rather than one per tool, a
framework's limit being per result. It is still a `var`: one
deployment's distribution is not two deployments disagreeing. The
numbers are quoted in `pkg/mcp/result_size.go`'s doc comment.

**Extended** (grain/task-155) to every tool rather than the two. Nothing
else had been capped here on the reading that the other big answers bound
themselves; an audit of the roster found that several do not, or bound
themselves per job and not per answer — the three GitHub CI reports quote
up to four failing jobs' logs at the wire limit each, `list_ci_runs`
rendered every run the deployment remembers with each job's error text
(a guest dump, for the flake this deployment hits most),
`read_memory_document` returns a note that may be four times the cap,
`chat` returns whatever a person typed, and a proxied tool returns
whatever its own server said. `Registry.handleCall` now elides every
answer at `maxResultTextBytes`, a quarter above the per-tool cap so a
tool that caps itself is not cut twice, and `Tool.ElisionAdvice` is how
each tool names the call that returns less. See "No single answer may eat
the run's context" (`pkg/mcp/result_size.go`).

**Measured** (grain/task-181). Both of the moves above were argued from
the size percentiles, and the cap they set censors them: every recorded
size is now at or under it, so `p95 <=16383` no longer separates a tool
whose answers fit from one whose answers are all being cut down to fit.
The census counts the cuts themselves as well — `task_run_tool.elided`
and a histogram of the bytes each cut dropped, read off the elision
notice by `mcp.ResultElision` the way the timeout rate reads finding 3's
notice, and applied to every tool since the backstop reaches every tool.
`metrics.ToolUse.Cut` reports cut calls, their share of that tool's
calls, and the bytes dropped at the median and p95 cut, beside the byte
buckets in both `grain metrics` and the Metrics pane. So the next move of
`maxToolResultBytes` is argued from whether the cap fires and by how much,
rather than from a distribution that stops at it.

## 5. The remote `run_command` has no process-group discipline

`procgroup` exists because killing a command has to kill everything it
forked, and the local `run_command` uses it. The remote one has no
equivalent: it runs `timeout N bash -c '...'` through `granule exec`, and
`timeout` signals only its direct child.

The suspected consequence — worth confirming on a real guest before
fixing — is that a command which backgrounds anything holding the SSH
channel's stdout (`npm run dev &`, a spawned server, a daemonized helper)
does not return when the foreground command finishes. It returns when the
outer `timeout` fires, which is up to five minutes later, and then
reports `exit=124` for a command that actually succeeded. That is finding
3's confusing answer arriving on top of five wasted minutes.

**Proposal.** Confirm it with a test that backgrounds a long sleep inside
a real sandbox VM (the shape `procgroup`'s own test already has). If
confirmed, `timeout --foreground --kill-after=5s` plus a sentence in the
tool description about redirecting a backgrounded process's output is the
whole fix.

**Done** (grain/task-150) — confirmed, and it turned out otherwise, which
is why it was worth measuring on a real guest first. Three results, all
from a real grain sandbox running the exact `timeout N bash -c 'cd … && …'`
this path builds:

- **The premise is wrong.** Plain `timeout` puts the command in a process
  group it leads and signals that whole group, so it already reaches
  everything the command forked: `timeout 2 bash -c './gc.sh & sleep 10'`
  leaves no surviving `gc.sh`. `--foreground` is what would have broken
  that — its own documentation says "children of COMMAND will not be
  timed out" — so it is deliberately *not* used. The remote path has the
  discipline `procgroup` gives the local one; it just gets it from
  `timeout` rather than from grain.
- **The suspected hang does not happen.** A backgrounded process
  inheriting the channel's stdout returns in about two milliseconds, not
  in five minutes, because the Go SSH client `granule exec` uses returns
  on the exit-status message rather than waiting for the streams to
  close — unlike an OpenSSH client, which is where that folklore comes
  from. No `exit=124` for a command that succeeded, and so no sentence in
  the tool description either: it would have described a hazard this
  transport does not have.
- **A real hole, found on the way.** Plain `timeout N` against a command
  that traps SIGTERM does not bound anything: it waits for that command
  to finish of its own accord — a full minute for a minute-long sleep —
  and only then reports 124. `--kill-after=5s` (`runCommandKillGrace`) is
  the fix, and the `exit=137` it produces gets finding 3's treatment.
  Reporting that 137 at all needs a trailing `exit $?` in the wrapper:
  bash otherwise replaces itself with `timeout`, which the group SIGKILL
  then kills, and death by signal is not something an exit-status message
  can carry.

Separately, the remote path had no bound on the *call* as opposed to the
command — nothing this side can make a guest answer — so it now carries a
local deadline of the bound plus 30s (`sshRunCommandGrace`) with a notice
of its own. See README.md's "What `run_command` says when it, not the
command, ended it".

## 6. The git proxy denies a push and a fetch with the same sentence

A denied request comes back as `"<owner>/<repo> is not in scope for this
sandbox"` (`pkg/gitproxy/core.go:103`) — the same words whether the repo
is unknown to the task entirely or is one of `task.Reads`, which the
agent may fetch and may not push (`authorize.go`). The message names
neither the action nor the repo the sandbox *may* push to.

**Proposal.** Say which of the two it is, and name the write target:
*"pushing to x/y is not allowed for this sandbox; it may fetch x/y, and
may push only to a/b"*. The authorizer has all three facts at the point
it says no.

## 7. A run is never told how long it has

`Config.MaxRunRuntime` cancels `framework.Run`'s context outright, and
its default is two hours (`pkg/orchestrator/orchestrator.go:385`).
`BuildPrompt` (`run.go:154`) never mentions it. Neither does any tool.

A run that does not know a deadline exists has no reason to commit early,
no reason to push a half-finished branch before reaching for one more
refactor, and no way to choose between "wait 15 minutes for CI" and
"finish now". `salvagePushedBranch` rescues what was *pushed* when the
clock runs out; everything only committed, and everything only edited, is
in a sandbox that is about to be destroyed.

**Proposal.** Two parts, in order of value:

- One sentence in `BuildPrompt`, with the real number: *"This run will be
  cancelled after 2h0m; commit and push work as you finish it rather than
  saving it all for the end."* It is exactly the class of fact
  `BuildPrompt` already exists to state — grain's own, not the agent's to
  guess, the same argument its doc comment makes for the branch name.
- Once less than some fraction of the budget remains, append a line to
  every tool result saying how long is left. A run does not read the
  prompt again at turn 200, and the deadline matters most exactly when
  the prompt is furthest away.

**Done** (grain/task-151), both halves, and both say what to do about the
deadline rather than only what it is: `BuildPrompt` takes
`cfg.maxRunRuntime()` and states it with the "push each piece as it
works" instruction that follows from it, and
`mcp.Registry.AnnounceDeadline` puts the time remaining on every tool
result — failed ones included — once the run is within
`mcp.RunDeadlineNoticeWindow` (20 minutes) of the wall, escalating in the
last five. The deadline reaches the forked mcpserver as `-run-deadline`,
read off the very ctx `framework.Run` is given
(`agent.RunDeadlineArgs`), so there is no second copy of the number to
drift from the one that actually cancels the run. See README.md's
"Telling a run how long it has".

**Also done** (grain/task-156): `wait_for_checks` acts on that deadline
instead of only reporting it. The registry puts it on the ctx each
handler runs under, and the wait is clamped to what is left of the run
less two minutes to act on the answer — a run eight minutes from the
wall that asked for fifteen used to spend the whole of its remaining
life inside one tool call and be cancelled mid-wait, having seen no
verdict. The clamp is stated on the report, a clamped wait that times
out is told the run's clock ran out rather than to retry with a longer
one, and a call with no room left for a wait at all answers immediately
with "there is no time to wait on CI" instead of blocking for thirty
seconds and reporting nothing.

## 8. Attempt N is told nothing about attempt N−1

A redispatch gets the task, the conversation (`commentThreadSection`),
its attachments, the prompt extensions, and a checkout that continues the
previous attempt's branch (`prepareCheckout` — already correct, and
deliberately so). It is told nothing about what the previous attempt
*did* or how it ended, even though the store has all of it: each
`task_run` row carries `outcome`, `detail` — which since `outcomeOf`
includes the tool census for every ending, not just the failures — and
the full `transcript`.

So attempt 2 opens on a branch with commits it did not make and no
account of what they were for or why the attempt that made them stopped.
The commonest waste is re-doing the diagnosis attempt 1 already paid for;
the sharpest is re-attempting exactly the thing that timed out.

**Proposal.** A `previousAttemptsSection` in `BuildPrompt`: for each prior
run, its attempt number, outcome, `detail`, and the commits it left on
the branch (`git log <base>..<branch>` is already available where the
checkout is prepared). Bounded to the last few attempts and to a couple
of hundred bytes of detail each — the goal is orientation, not a
transcript store. The transcript itself stays where it is; it is prose,
per-framework and unbounded, and none of that belongs in a prompt.

**Done** (grain/task-152). `BuildPrompt` takes an
`orchestrator.History` — the attempts before this one (`previousAttempts`,
`store.Runs` with this run's own row filtered out, since `dispatch.Cycle`
writes that row before `RunDispatch` ever sees the dispatch), the commits
they left on the branch (`checkoutCommits`, run through the same sandbox
tool `prepareCheckout` clones with), and the conversation — and renders
it as `previousAttemptsSection`: per attempt its number, outcome and
`detail`, then the branch's own commits newest first. Bounded by
`maxPreviousAttempts` (3), `maxAttemptDetail` (240 bytes, one line) and
`maxBranchCommits` (10, with a pointer at `git log` for the rest);
`RunDispatch` asks for one commit more than the list holds so "there is
more" needs no second read. The store read is fatal like the two reads
either side of it, the git read is best effort and silent, and it is
skipped altogether on a first attempt.

The ordering constraint is why `commentThreadSection` moved out of
`prepareCapabilities` and into `BuildPrompt`: both history sections now
sit together, immediately after the sentences naming the checkout and the
branch they explain, and ahead of the commit-message, CI and budget
paragraphs. See README.md's "Telling attempt N what attempt N−1 did".

## 9. There is no way to say how to set a repo up

`RepoConfig` carries `DefaultCapabilities` and `PromptExtension`
(`pkg/model/repo_config.go`) and nothing else. A repo whose tests need
`make deps`, a `node_modules`, a Python venv or a generated file has no
way to say so except in prose in its prompt extension, which every run
then re-discovers by running the wrong command first.

**Proposal.** `RepoConfig.SetupCommand`, run by `prepareCheckout` after
the clone and before the agent's first turn, with its exit status and
tail of output stated in the prompt (a setup that failed is something the
agent must know, not something to hide). It has to be re-run by the
`recreate_sandbox` path too, which restores the checkout and would
otherwise hand back a directory that no longer builds. This is the one
finding here with real surface area — a new field, a settings form, a UI
row — which is why it is ninth rather than third.

**Done** (grain/task-154). `RepoConfig.SetupCommand` is run by
`prepareCheckout` through the same `run_command` tool the clone goes
through, so it works on either sandbox backend, and by
`restoreCheckout` on the `recreate_sandbox` path, whose answer names it
among what was restored — or warns, with the exit status and the tail,
when it failed there. The prompt gets `setupSection`: the command, its
exit status, the tail of its output, and the sentence that separates a
broken checkout from a broken change. A failed setup is deliberately not
a failed dispatch (grain cannot know whether a broken `make deps` is
fatal to the task in hand, and the run can find out in one command); a
setup still running at `setupCommandTimeout` — ten minutes, `mcp`'s own
ceiling for a sandbox tool call — is, since it has told nobody anything
and would otherwise burn the run's whole budget inside a tool call the
agent never sees. The surface area landed with it: the column and its
migration, `GET`/`PUT /api/repos/{owner}/{name}/setup-command` beside
the two routes already there, a box on the repo page, `grain repo
setup-command`, and `reposWithSetupCommand` on `GET /api/config` so a
repo whose only configuration is the command is still reachable.

## 9a. A run cannot wait for another task

`depends_on` only gates dispatch: a queued task does not start until what
it names has closed. A run that discovered mid-way that it had to wait for
somebody else's work -- the v2.0.0 release (#4943) had to pause before
promoting until #4944 had published the image -- could end and file a
successor, ask a human to say when, or poll with task tools most runs do
not hold.

**Done** (task 4945): `wait_for_task`, on every dispatched run's roster
(`pkg/mcp/wait_for_task_tool.go`). It takes `task_id` (plus `task_ids`),
`until` (`closed`, the default, or `merged`), `timeout_seconds`, `park`
and `force`, and works in two modes:

- **Block** when the target is already running and should finish within
  minutes. The call polls until every target reaches the state or the
  timeout passes, and returns each one's state, outcome (`merged`,
  `closed_unmerged`, `completed`, `cancelled`, `failed`) and closing note.
  Blocked time is credited back to the run's deadline through the same
  ledger and per-run cap as `wait_for_checks`.
- **Park** when the wait is longer: `park: true`, or a timeout the run
  has no room for. The daemon adds the targets to the task's own
  depends-on links (`POST /api/tasks/{id}/wait-on`, `ui.Client.WaitOn`).
  `processResult` records the run as `parked` (which no failure streak
  counts) and leaves the task queued. The scheduler dispatches it again
  once the targets close, or once a person removes the links. The next
  prompt says it resumed from a park and how each target ended
  (`parkedResumeSection`). The sandbox does not survive a park, so a park
  with unpushed commits or uncommitted changes is refused unless
  `force: true` is set.
- **Park until a time** when the hold is only a clock ("hold off for 1
  hour while we do repairs"): `resume_at`, a duration (`"1h"`) or an RFC
  3339 time at most a week away, with no `task_id`. The daemon defers the
  task with that wake time (`POST /api/tasks/{id}/park-until`,
  `ui.Client.ParkUntil`) -- the same `DeferredUntil` that `propose_task`'s
  `not_before` sets -- and `reconcileWake` queues it again once the time
  has passed. The run is recorded as `parked` with the time, and the next
  prompt says so. The unpushed-work refusal applies here too.

The refusals are the dependency picker's own (`dependsOnLinks`): the task
itself, an unknown id, a standing chat or director session, or a task
that already waits on this one. A waiting run sees only a target's title,
state, outcome and its runs' closing note, never its transcript. When a
run is *filing* follow-up work, `propose_task`'s `depends_on` is still
the right tool. `wait_for_task` is for the run that has to wait itself.

## 9b. A UI change can pass every check and still look wrong

**Done (task 4981)** for this repository. UI tasks passed on jsdom and DOM
assertions while the real page was wrong. In #4980 the header rail's tab
counts sat off their labels and "Add repo" floated mid-rail. Nothing in
the loop rendered the page, and nothing could have shown it to the agent:
the sandbox's `read_file` returns a PNG's bytes, not the image.

`.grain/settings.yml`'s `prompt-extension` now carries a "UI changes"
rule. Any change touching `ui/` has to:

- render the affected pages over `grain demo` at desktop and phone widths,
  in light and dark (`make ui-screenshots`, `ui/e2e/screenshot.mjs`);
- look at the result, with `LOOK=1` having a vision model describe the
  shots for a run that cannot see images itself;
- pin layout rules with bounding-box specs;
- attach the contact sheet to its closing note.

`docs/ui-screenshots.md` has the details. What remains open is grain-wide:
an image in a closing note reaches the task's conversation but not the
GitHub pull request's description, which grain builds from commit messages
alone.

## 10. Minor, and worth doing while nearby

- **`run_command` has no `workdir` argument.** Every call in a
  repo-bearing task starts `cd work && `. It is a small tax paid on every
  single call of the most-used tool, and the local handler already sets
  `cmd.Dir`, the remote one already composes a `cd`.
- **`edit_file`'s "String not found" answer** (`sandbox_tools.go`) reports
  the string and nothing else. Whitespace and indentation are the usual
  cause, and the file is already in memory: saying "no exact match; the
  closest line is N" would end the guess-again loop that follows.
- **`read_file` has no default `limit`.** Even with finding 4's cap in
  place, a default window (with the total line count stated, so the agent
  knows to ask for more) is the more useful shape.

---

## Metrics: everything above was argued from anecdote

*(Findings 11, 12 and 13 below are **done**, grain/task-153 — the three
of them are one change. What this section describes as missing is what
was missing when it was written.)*

`pkg/metrics` measures what the deployment delivers — throughput, latency
by stage, run outcomes, backlog, cycle behaviour. It measures nothing
about what happens *inside* a run, which is where every finding above
lives. The evidence for this document is code-reading and individual
transcripts, and it should not have had to be.

The data is very nearly there already. `outcomeOf` and `toolCallSummary`
(`pkg/orchestrator/finish.go:314`) build a per-tool census with error
counts for **every** run, successful ones included — and then render it
into English and store it in `task_run.detail`, a column a human reads
one row at a time. Nothing aggregates it, and nothing can, because it is
prose.

### 11. Persist the tool census structurally, and report on it

Store what `toolCallSummary` already computes — per run, per tool: calls,
errored calls — as data rather than a sentence (a `task_run_tool` table,
or a JSON column alongside `detail`, which the migration ladder in
`pkg/model/store.go` makes routine). Then report, over a window:

- **calls per run**, and **errored calls as a share of all calls**. The
  ordinary shape of agentic work is a handful of errors; a deployment
  drifting upward is a deployment whose tools got worse.
- **error rate per tool.** `edit_file`'s is finding 10's second bullet
  measured; `run_command`'s is the sandbox's health; a spike in one tool
  is a specific, actionable thing in a way that a run's outcome is not.
- **tool result sizes** (p50/p95/max, per tool). This is what sizes
  finding 4's cap with data instead of a guess, and it names the commands
  that blow a run's context.
- **`run_command` timeout rate**, once finding 3 makes timeouts
  distinguishable at all.

**Done** (grain/task-153), with 12 and 13. `task_run_tool` holds the
census one row per run per tool — calls, errored calls, calls the tool's
own bound cut off, result bytes and their base-2 histogram — written once
by `RunDispatch` from the `agent.Result` that is about to be discarded,
best-effort and logged rather than surfaced, since a measurement that
cannot be taken is not a reason to fail the run being measured.
`task_run.detail`'s sentence is untouched. `metrics.Report.Tools` reports
all four bullets over a window, and `grain metrics` and the Metrics pane
both print it. The size percentiles are bounds and are named `AtMost`:
an exact one needs a stored row per *call*, and a bound within an octave
is enough to size `mcp.maxToolResultBytes`, which is what it went on to
do: finding 4's 64 KB guess is 16 KB (grain/task-183) because of what
these bounds said over a real deployment's 90 days. The timeout rate reads
finding 3's own notice back (`mcp.RunCommandTimedOut`), matching a marker
that notice is built from, and counts neither the hedged `exit=137` nor a
stalled transport as a timeout. See README.md's "Measuring what a run does
with its tools".

**Extended** (grain/task-181) with the count the size bounds stopped
being able to give once finding 4's cap was in force: `elided` and
`dropped_buckets` beside them, and `metrics.ToolUse.Cut` over the window.
A distribution truncated at a cap describes the cap; the cuts describe
the tools.

### 12. Split the endings that are ergonomics problems out of `Outcomes`

`metrics.Runs.Outcomes` counts `task_run.outcome` strings, and the
interesting distinctions are inside them. `"cancelled"` is both "a human
closed the task" and "the run hit the two-hour wall" — the first is
routine and the second is finding 7 happening. `"no_action"` is a run
that had tools, used them, and produced nothing, which is the purest
measure of the agent surface failing, and it is currently one key in a
map next to `"succeeded"`.

Report them as their own series: runs ended by the runtime cap, runs
ended by exhausting `MaxAgentTurns`, `no_action` runs, and runs paused by
a usage limit. Each has a different fix, and today they are
indistinguishable in aggregate.

**Done** (grain/task-153). `metrics.Runs.Endings` counts the same runs by
`model.RunEnding` — `runtime_cap`, `task_closed`, `turns_exhausted`,
`no_action`, `usage_limit`, `setup_failed`, `succeeded`, `failed`, and a
`cancelled` for a cancellation that names neither of its two causes, so
that neither of those series is inflated by a run that might not belong
to it. `Runs.Outcomes` still counts the words verbatim, and the two
readings sit next to each other on purpose. Nothing new is stored for it:
the distinction was always in `task_run.detail`, and what was missing was
a way to read that back as a fact. The sentences are now built and matched
in one place (`model.RuntimeCapDetail`, `model.TaskClosedDetail`, and
`agent.MaxTurnsExceeded` for the phrase all three frameworks return),
each with a test on the round trip, because a reworded sentence would
otherwise leave a series quietly reading zero forever.

### 13. Measure the CI loop the prompt sends every run around

`BuildPrompt` now tells every run to push, call `wait_for_checks`, fix
and push again, and that loop is unmeasured end to end. Worth counting:
how often a wait ends in each of its four verdicts (failed, passed,
nothing registered, clock ran out), how long it blocks, and how many
pushes a run makes before its checks go green. A deployment where most
waits end with "still running when the clock ran out" is one where
`DefaultWaitForChecksTimeout` is set wrong for its CI, and nothing today
would ever show that.

**Done** (grain/task-153). `task_run_check_wait` holds one row per
`wait_for_checks` call — a run makes a handful, not hundreds — with the
verdict it reached, how long it blocked, and how many pushes the run had
made before it; `metrics.Report.Checks` reports the verdict counts, the
blocked distribution and pushes-to-green, read off the *first* passing
wait in each run. The verdict comes from `mcp.ReadCheckWait`, which lives
beside the report it reads and matches phrases that report is built from;
an answer that is not one of the four (a branch not pushed yet, a CI that
could not be read) counts as none of them rather than being folded into
the nearest. A push is the `git push` in a `run_command`'s own arguments,
and only from a call that did not error, so the number under-counts
rather than over-counts.

---

## Follow-up tasks

Filed as separate proposals, each depending on this document:

1. Truthful escape-hatch tool descriptions and confirmations (findings 1
   and 2) — **done**, grain/task-149.
2. `run_command`: say when a command timed out, and bound result size
   (findings 3, 4, and the check in 5) — **done**, grain/task-150.
3. Tell a run its wall-clock budget (finding 7) — **done**,
   grain/task-151.
4. Tell a redispatched run what its previous attempts did (finding 8) —
   **done**, grain/task-152.
5. Per-run tool telemetry, and the metrics over it (findings 11, 12, 13)
   — **done**, grain/task-153.
6. A per-repo setup command (finding 9) — **done**, grain/task-154.

Findings 6 and 10 are small enough to fold into whichever of those
touches the same file first.

Each is proposed for human review rather than auto-merge: all six change
what every dispatched run sees or how its behaviour is recorded, and that
is not a surface to change unread.
