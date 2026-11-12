# Host/operator `grain` CLI specification

**Status:** draft for review. Normative.
**Part of:** [`cli.spec.md`](cli.spec.md). Read that first for dispatch ([#CLI-1](cli.spec.md#CLI-1)), configuration ([#CLI-3](cli.spec.md#CLI-3)–[#CLI-5](cli.spec.md#CLI-5)), auth ([#CLI-6](cli.spec.md#CLI-6)), connection errors ([#CLI-7](cli.spec.md#CLI-7)), output ([#CLI-8](cli.spec.md#CLI-8)), exit codes ([#CLI-10](cli.spec.md#CLI-10)), flag and help conventions ([#CLI-11](cli.spec.md#CLI-11), [#CLI-12](cli.spec.md#CLI-12)) and the coverage guard ([#CLI-14](cli.spec.md#CLI-14)).
**Code:** `cmd/grain`, `pkg/cli`. The task commands call `pkg/ui.HTTPClient` against the daemon's `/api/…`.

Each table between `BEGIN/END GENERATED FLAGS` markers is generated from `grain <path> -h` ([#CLI-14](cli.spec.md#CLI-14)). It lists every flag with its type, default and the environment variables its usage text names. Where a default depends on the host (CPU count, home directory, `$GRAIN_DATA_DIR`), the table shows the value under the guard's clean environment.

A type of `value` means `-h` does not name one. Either a back-quoted word in the usage replaced the type name (`flag.UnquoteUsage`) and the default does not reveal it, or the flag is a custom `flag.Value` (repeatable lists and the like).

Commands with a hand-written usage list their flags in prose next to an empty table ([#CLI-12](cli.spec.md#CLI-12)).

Every task command below needs a credential the daemon accepts ([#CLI-6](cli.spec.md#CLI-6)). **Scope** names the daemon-side rule where it is narrower than "any signed-in caller": *admin* for deployment configuration, and *person* for an act an agent is refused.

---

## 1. Command index {#CLI-20}

The binary **MUST** register exactly the commands below. The guard compares this set with `pkg/cli/main.go`'s two switches.

| Kind | Commands |
|---|---|
| Task CLI: tasks | `list`, `get`, `create`, `update`, `approve`, `withdraw`, `defer`, `undefer`, `capability`, `comment`, `reply`, `close`/`delete`, `reopen`, `retry`, `retry-merge`, `force-merge`, `run-now`, `pause`, `resume`, `inbox`, `wait`, `logs` |
| Task CLI: deployment | `config`, `settings`, `qa …`, `sandbox-budget`, `sandbox probe`, `repo …`, `gcp-key …`, `capability-servers …`, `hostds …`, `access list|allow|role|deny`, `git-ambient`, `git-connections`, `git-login`, `default-model`, `broadcast`/`notify`, `release approve\|reject`, `metrics`, `failures`, `ci …`, `fs ls\|cat\|read\|grep`, `pr adopt\|review\|status`, `declarations check` |
| Task CLI: sign-in | `login`, `logout`, `whoami`, `context list\|use\|show\|delete` |
| Modes: services | `daemon`, `demo`, `mcpserver`, `runhost exec`, `hostd`, `host-script`, `capability <server>` |
| Modes: local administration | `access-code`, `secrets`, `connections import`, `state`, `backup`, `export`, `import`, `repo-case`, `upgrade-postgres` (= `upgrade-sqlite-to-postgres`, `db upgrade-postgres`), `db check`, `controller bootstrap-github-app`, `setup gcp`, `init`, `init workstation`, `sync` |
| Modes: build information | `version`, `schema-version`, `release-notes`, `sandbox-image`, `container-sandbox-image`, `k8s-sandbox-image`, `image` |
| Modes: hooks forked by a run | `agy-tool-hook`, `claude-stop-hook`, `codex-detached` |

**Pinned by:** `pkg/cli/clispec_test.go`: `TestHostCLISpecCoversEveryCommandAndFlag`.

---

## 2. Task CLI global flags and connection {#CLI-21}

`grain [global flags] <command> [args]`. Global flags **MUST** come before the command. Their behaviour is [#CLI-4](cli.spec.md#CLI-4), [#CLI-6](cli.spec.md#CLI-6) and [#CLI-7](cli.spec.md#CLI-7).
- `-server` defaults to `$GRAIN_SERVER`, else the active context's server, else cli.env, else `http://127.0.0.1:4242` ([#XC-3](../external-cli/external-cli.spec.md#XC-3)).
- `-context` names the context to use; it defaults to `$GRAIN_CONTEXT`, else config.json's `current` ([#XC-2](../external-cli/external-cli.spec.md#XC-2)).
- `-token` falls back to `$GRAIN_TOKEN`, `$GRAIN_AUTH_TOKEN`, `$GRAIN_ID_TOKEN`, `$GRAIN_TUNNEL_TOKEN`, then the active context's credential ([#XC-4](../external-cli/external-cli.spec.md#XC-4)).
- `-audience` defaults to `$GRAIN_AUDIENCE`, else `$GRAIN_TUNNEL_AUDIENCE`.
- `-as` defaults to `$GRAIN_USER`.
- `-json` switches every task command to JSON ([#CLI-8](cli.spec.md#CLI-8)).

<!-- BEGIN GENERATED FLAGS: grain -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-server` | string |  |  |  |
| `-connect-timeout` | duration |  |  |  |
| `-timeout` | duration |  |  |  |
| `-token` | string |  |  |  |
| `-audience` | string |  |  |  |
| `-as` | string |  |  |  |
| `-json` | bool |  |  |  |
| `-context` | string |  |  |  |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/cli_flags_test.go`: `TestRunCLI_GlobalFlags`, `TestRunCLI_EnvVarDefaults`, `TestRunCLI_UsageIncludesNewFlags`.

---

## 3. Task commands

Each command parses its own flags, with flags allowed after the task id ([#CLI-11](cli.spec.md#CLI-11)). A missing id is `a task id is required`. A command that changes a task **MUST** then print the task as `respond` does: `GET /api/tasks/{id}`, printed as a task block, or with `-json` as the `ui.Task` object. The exception is `update` without `-depends-on`/`-no-depends-on`, which prints the task its `PATCH` answers with and does not read it again.

### 3.1 `grain list` {#CLI-22}

`grain list [flags]` calls `GET /api/config` (the list's configuration and declared custom fields), then `GET /api/tasks`. When no `-filter`/shorthand is given and the deployment has no configured list default, it also reads `GET /api/settings` for the hidden-state settings (`deploymentSettings`), and treats a failed read as empty settings. It prints one aligned line per task, marking a queued task that waits on an unfinished dependency with `(waits on <ids>)` in the title column so the `id`, `state` and `repo` columns stay aligned. Closed and deferred tasks are left out unless `-closed`, `-deferred` or a filter naming that state asks for them, and the CLI says what it left out.
- `-filter attribute:comparison:value` is repeatable, in the UI's link spelling. Shorthands such as `-state`, `-repo` and `-capability` are conditions too.
- `-sort attribute[:asc|desc]` is repeatable, and the sort is stable.
- `grain list -h` prints the attribute and comparison vocabulary, including declared custom fields.
- Two provenance conditions sit outside the vocabulary the UI shares ([external-cli #XC-18](../external-cli/external-cli.spec.md#XC-18)): `derived_from:eq:<owner/name#pr or its URL>` (a `derived-from` link, or a `Derived from:` line in the body) and `filed_via:has:<substring>` (case-insensitive over `filedVia`). `GET /api/tasks` takes the same as `?derived_from=` and `?filed_via=`.
- A value nobody can act on is refused before the daemon is asked.

With `-json` it prints the `ui.Task` array.

<!-- BEGIN GENERATED FLAGS: grain list -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-author` | string |  |  | shorthand for -filter author:any:VALUE |
| `-auto-merge` | bool |  |  | only tasks that merge themselves (-auto-merge=false for only those needing a human) |
| `-base` | string |  |  | shorthand for -filter base:any:VALUE (or "none" for the tasks taking the repo's default) |
| `-blocked` | bool |  |  | only tasks waiting on another task (-blocked=false for only the unblocked ones) |
| `-capability` | string |  |  | shorthand for -filter capability:any:VALUE (or "none" for the tasks carrying none) |
| `-closed` | bool |  |  | list closed tasks too (-closed=false to leave them out where the deployment shows them by default) |
| `-deferred` | bool |  |  | list deferred tasks too |
| `-external-ref` | string |  |  | shorthand for -filter externalRef:any:VALUE (e.g. CA-204, or "none" for tasks with no external reference) |
| `-filter` | value |  |  | only tasks matching this condition, written attribute:comparison:value -- repeatable, and every one of them has to hold |
| `-interactive` | bool |  |  | only live chats (-interactive=false for only background tasks) |
| `-origin` | string |  |  | shorthand for -filter origin:any:VALUE (hand, external, proposed, imported, schedule, suite, fix, review) |
| `-owner` | string |  |  | shorthand for -filter owner:any:VALUE (or "me" for your own tasks) |
| `-repo` | string |  |  | shorthand for -filter repo:any:VALUE (owner/name, or "none" for the tasks with no target repo) |
| `-search` | string |  |  | only tasks whose title or id contains this text, case-insensitively |
| `-sort` | value |  |  | print in this order: attribute:asc or attribute:desc keys, comma-separated, the first deciding and the rest breaking its ties |
| `-state` | string |  |  | shorthand for -filter state:any:VALUE (proposed, queued, running, awaiting_reply, failed, awaiting_submit, completed, deferred, closed, or asked for awaiting_reply and awaiting_submit both) |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/list_test.go`: `TestConditionsNarrowTheListing`, `TestShorthandFlagsAreConditions`, `TestSortKeysOrderTheListing`, `TestConditionsRejectWhatNobodyCanActOn`; `pkg/cli/list_hidden_test.go`: `TestListHidesClosedAndDeferredTasksUnlessAsked`; `pkg/cli/list_declared_test.go`: `TestListHelpNamesTheDeclaredFields`; `pkg/cli/list_vocabulary_test.go`; `pkg/cli/list_test.go`: `TestListDerivedFromAndFiledViaFiltersAndGetCommentsSince`.

### 3.2 `grain get <id>` / `grain show <id>` {#CLI-23}

Calls `GET /api/tasks/{id}` and prints the task block (including `owner:` and, when dependencies are declared, `depends on:` with each dependency's state), the failure streak, a pending credential request and the conversation. `grain show` is an alias for `grain get`. `-json` prints `ui.TaskDetail`. `-comments-since <RFC3339>` (`?comments_since=`) keeps only the conversation items created strictly after that time, for a polling loop ([external-cli #XC-18](../external-cli/external-cli.spec.md#XC-18)).

<!-- BEGIN GENERATED FLAGS: grain get -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-comments-since` | string |  |  | only the conversation items created strictly after this RFC 3339 time -- a polling loop's cursor |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain show -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-comments-since` | string |  |  | only the conversation items created strictly after this RFC 3339 time -- a polling loop's cursor |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/args_test.go`: `TestCommandsRefuseArgumentsTheyHaveNoUseFor`.

### 3.3 `grain create -title T [flags]` {#CLI-24}

Calls `POST /api/tasks` (`HTTPClient.CreateTask`) and prints the new task.
- `-title` is required.
- `-position front|end` picks the backlog end and is remembered for the next create.
- `-field name=value` sets a declared custom field and is repeatable; `grain create -h` lists the fields.
- `-model` must be a model this deployment has (`TestCreateRefusesAModelThisDeploymentDoesNotHave`).
- A task filed unapproved prints a note saying so.
- `-body-file <path|->` reads the description from a file, or stdin for `-`; naming `-body` as well exits `2`. `-design <path>` (repeatable) records a design or spec file as a `design` provenance link (`ui.Task.Designs`) ([external-cli #XC-19](../external-cli/external-cli.spec.md#XC-19)).

<!-- BEGIN GENERATED FLAGS: grain create -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-approve` | bool |  |  | queue the task immediately instead of filing it as a proposal awaiting approval |
| `-attach` | value |  |  | path to a local file to attach to the task (repeatable) |
| `-author` | string |  |  | who asked for this task, recorded as its requester (defaults to the daemon's own actor) |
| `-auto-merge` | bool |  |  | merge this task's pull request automatically once it reads clean |
| `-base` | string |  |  | base branch, if not the target repo's default |
| `-body` | string |  |  | task description |
| `-body-file` | string |  |  | read the task description from this path (- for stdin) instead of -body |
| `-capability` | value |  |  | capability to attach (repeatable): a capability ID, a capability reference (https://...[#method]), or a reference with its arguments as ref={json} |
| `-depends-on` | value |  |  | task this one must wait on before it is dispatched -- 12, #12, task-12, grain/task-12 or the task's page URL (repeatable) |
| `-design` | value |  |  | committed-context path ([owner/name:]path[@rev]) this task implements, pinned to a commit SHA (repeatable) |
| `-external-ref` | string |  |  | external work reference (e.g. CA-204), inherited by tasks spawned from this one |
| `-field` | value |  |  | name=value for one of the fields this deployment declares (repeatable; "name=" clears it, and a field holding a set takes the flag more than once or a comma-separated list) |
| `-interactive` | bool |  |  | file this as a live chat rather than a change to run unattended (bwsalmon/agents#539); implies -approve and dispatches ahead of the backlog |
| `-issue` | int |  |  | GitHub issue number associated with this task |
| `-model` | string |  |  | ID of the model this task runs on (Settings -> Models), disabled ones included -- unset takes whichever model the rotation reaches |
| `-network-access` | string |  |  | how much of the internet this task's sandbox may reach: "open" or "none" -- unset takes the deployment's own default |
| `-no-repo` | bool |  |  | file this task with no repo at all, rather than falling back to the deployment default |
| `-owner` | string |  |  | account responsible for this task (defaults to the caller) |
| `-position` | string |  |  | which end of the backlog to file it at: "front" to run ahead of everything already queued, "end" to queue behind it. Unset files it wherever the last task added chose, and leaves that choice alone |
| `-priority` | value |  |  | task priority, 1 or more (default 5; higher numbers mean higher priority) (default 5) |
| `-read` | value |  |  | owner/name of a repo this task's run may read but never push to (repeatable) |
| `-repo` | string |  |  | owner/name of the repo this task's work targets |
| `-review-template` | string |  |  | ID of the template to file a review from once this task's work is done -- a second agent on the same branch, before the merge |
| `-title` | string |  |  | task title (required) |
| `-write` | value |  |  | owner/name[@branch] of another repo this task may land work in, by proposing a task against it (repeatable) |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/create_position_test.go`: `TestBacklogPosition`; `pkg/cli/create_model_test.go`; `pkg/cli/create_author_test.go`: `TestCreateAuthorFilesTheTaskAsItsRequester`; `pkg/cli/create_unapproved_test.go`: `TestUnapprovedNote`; `pkg/cli/taskcmd_priority_test.go`: `TestPriorityFlagReadsTheAPIsSpellings`; `pkg/cli/taskcmd_test.go`: `TestCreateBodyFileStdinConflictAndDesignLink`.

### 3.4 `grain update <id> [flags]` {#CLI-25}

Calls `PATCH /api/tasks/{id}` (`UpdateTask`) and prints the task the `PATCH` returns. Only the flags given are sent. `-depends-on`/`-no-depends-on` instead call `POST /api/tasks/{id}/depends-on` (`SetDependency`, `{"id", "attach"}`) and then read the task with `GET /api/tasks/{id}`.

<!-- BEGIN GENERATED FLAGS: grain update -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-auto-merge` | bool |  |  | whether the task auto-merges once clean |
| `-base` | string |  |  | new base branch |
| `-body` | string |  |  | new task description |
| `-connection` | string |  |  | ID of the one connection this task's next run must use -- empty takes the pin off; pass it with -model when the pinned connection does not serve the new model |
| `-depends-on` | value |  |  | task this one must wait on before it is dispatched -- 12, #12, task-12, grain/task-12 or the task's page URL (repeatable) |
| `-design` | value |  |  | committed-context path ([owner/name:]path[@rev]) this task implements (repeatable) -- replaces the whole set; pass -design "" to clear |
| `-external-ref` | string |  |  | new external work reference (e.g. CA-204) -- empty clears it |
| `-field` | value |  |  | name=value for one of the fields this deployment declares (repeatable; "name=" clears it, and a field holding a set takes the flag more than once or a comma-separated list) |
| `-issue` | int |  |  | new associated GitHub issue number |
| `-model` | string |  |  | model this task's next run asks for (Settings -> Models) -- empty takes the deployment's default |
| `-no-depends-on` | value |  |  | task this one should no longer wait on, in any form -depends-on takes (repeatable) |
| `-owner` | string |  |  | transfer task ownership to this account |
| `-priority` | value |  |  | new task priority, 1 or more (default 5; higher numbers mean higher priority) (default 5) |
| `-read` | value |  |  | owner/name of a repo this task's run may read but never push to (repeatable) -- replaces the whole set |
| `-repo` | string |  |  | new target repo (owner/name) |
| `-review-template` | string |  |  | ID of the template to file a review from once this task's work is done -- empty detaches the review it has |
| `-title` | string |  |  | new task title |
| `-write` | value |  |  | owner/name[@branch] of another repo this task may land work in (repeatable) -- replaces the whole set |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/taskcmd_priority_test.go`: `TestPriorityFlagReadsTheAPIsSpellings`; others *untested*.

### 3.5 State changes with no flags of their own {#CLI-26}

Each of these takes `<id>`, calls the route, and prints the task. `approve` and `run-now` also take `-reason`, sent as `X-Grain-Acting-Reason`: the agent API gate refuses an external agent credential (`gma_`) that approves without one, and run-now of an unapproved task is an approval:

| Command | Route | Scope |
|---|---|---|
| `approve <id>` | `POST /api/tasks/{id}/approve` (proposed → queued) | admin/person |
| `withdraw <id>` | `POST /api/tasks/{id}/withdraw-approval` | admin/person |
| `undefer <id>` | `POST /api/tasks/{id}/undefer` | |
| `retry <id>` | `POST /api/tasks/{id}/retry`: clears the retry cap | |
| `retry-merge <id>` | `POST /api/tasks/{id}/retry-merge` | |
| `run-now <id>` | `POST /api/tasks/{id}/run-now` | |
| `compact [-handover-file f] <id>` | `POST /api/tasks/{id}/compact`. With a handover the task is compacted now, through every comment so far; without one its next run is asked to compact first (`design/design/durable-tasks.design.md#DT-15`) | |
| `brief [-file f -reason r] <id>` | `GET /api/tasks/{id}/context` to show the current brief, handover age and compaction cursor; with `-file`, `POST /api/tasks/{id}/brief` replaces the brief (an empty file reverts to the original description) | |
| `rollover <id>` | `POST /api/tasks/{id}/rollover`. Files a successor carrying the task's current brief, handover and settings, moves every reference to the task onto it, and closes the task as `rolled_over`; refused for a closed task, a running one, a console session, and a task that is not durable (`design/design/durable-tasks.design.md#DT-15`) | |
| `resume <id>` | `POST /api/tasks/{id}/resume` | |
| `capability <id> <capability-id> attach\|detach` | `POST /api/tasks/{id}/capabilities`. A bad verb gets `third argument must be attach or detach, got "<x>"`; fewer than three arguments get `usage: grain capability <id> <capability-id> attach\|detach` | admin |

<!-- BEGIN GENERATED FLAGS: grain approve -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-reason` | string |  |  | why: sent as X-Grain-Acting-Reason, which an external agent credential (gma_) must give to approve |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain withdraw -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain undefer -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain retry -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain retry-merge -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain run-now -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-reason` | string |  |  | why: sent as X-Grain-Acting-Reason, which an external agent credential (gma_) must give when run-now approves an unapproved task |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain compact -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-handover-file` | string |  |  | compact now, to the handover in this file (- for stdin); without it, the task's next run is asked to compact first |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain brief -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-file` | string |  |  | replace the current brief with this file (- for stdin); an empty file reverts to the original description |
| `-reason` | string |  |  | why the brief changed (required with -file) |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain rollover -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain resume -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain capability -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/list_cli_test.go`: `TestDeferAndUndeferMoveATaskAndBack`; `pkg/cli/args_test.go`: `TestCommandsRefuseArgumentsTheyHaveNoUseFor`; others *untested*.

### 3.6 `grain defer`, `comment`, `close`/`delete`, `reopen` {#CLI-27}

- `defer <id>` calls `POST /api/tasks/{id}/defer`.
- `comment <id> <body…>` calls `POST /api/tasks/{id}/comments`. Every word after the id is the body, and a flag written after the id is refused. On a closed task the comment reaches no agent, and the command says so on stderr; `-reopen` sends `{"reopen": true}`, which reopens the task with the comment so a run is queued to answer it.
- `close <id>` (alias `delete`) calls `POST /api/tasks/{id}/close`. `-close-pull-request` also closes the open pull request unmerged, and `-reason` is kept in history.
- `reopen <id>` calls `POST /api/tasks/{id}/reopen` with `{"reason"}`.

<!-- BEGIN GENERATED FLAGS: grain defer -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-until` | string |  |  | wake time: pick this task back up on its own at or after this instant (a date, 2026-09-14, or an RFC 3339 time). Omitted, the task stays aside until you undefer it. |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain comment -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-attach` | value |  |  | path to a local file to attach to the comment (repeatable) |
| `-reopen` | bool |  |  | reopen the task if it is closed, so the comment reaches an agent (a comment on a closed task otherwise reaches none) |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain close -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-close-pull-request` | bool |  |  | close the task's pull request on GitHub too, without merging it (the branch and its commits are left untouched) |
| `-reason` | string |  |  | why, kept with the close in the task's history |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain delete -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-close-pull-request` | bool |  |  | close the task's pull request on GitHub too, without merging it (the branch and its commits are left untouched) |
| `-reason` | string |  |  | why, kept with the close in the task's history |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain reopen -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-reason` | string |  |  | why, kept with the reopen in the task's history |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/comment_flag_order_test.go`: `TestCommentRefusesAFlagWrittenAfterTheTaskID`; `pkg/cli/args_test.go`: `TestCommentStillTakesItsBodyAsWords`.

### 3.7 `grain force-merge <id> <reason…>` {#CLI-28}

Calls `POST /api/tasks/{id}/force-merge` with the reason. It overrides the merge queue, is recorded on the task and the pull request, and is refused when the branch does not merge cleanly. `-head <sha>` pins the reviewed commit, and the override is withdrawn if anything else is pushed first. **Scope:** person. No agent-facing tool can force a merge.

<!-- BEGIN GENERATED FLAGS: grain force-merge -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-head` | string |  |  | the head commit you reviewed; the override is for it alone |
| `-override-gates` | string |  |  | comma-separated unsatisfied gates to override (e.g. security-review, review, ci); required when any review gate is pending or has findings |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/force_merge_is_not_a_tool_test.go`: `TestNoAgentFacingToolCanForceAMerge`.

### 3.8 `grain pause` {#CLI-29}

The command has two forms:
- **Without an id**, it reads why nothing is dispatching: `GET /api/pause` and `GET /api/pipeline-pause`.
  - `-set [-reason r]` stops dispatch (`POST /api/pipeline-pause`).
  - `-resume` lifts it (`DELETE /api/pipeline-pause`).
  - `-lift [-connection c [-model m]]` lifts agent usage-limit pauses (`DELETE /api/pause`).
  - Prints a human summary, or the JSON reading.
- **With `<id>`**, it pauses one task (`POST /api/tasks/{id}/pause`). A running task gets 2m to push and write its handover; `-now` skips that. A reason written without its flag is refused.

<!-- BEGIN GENERATED FLAGS: grain pause -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-connection` | string |  |  | with -lift, lift only this model connection's pauses |
| `-lift` | bool |  |  | lift the agent usage-limit pauses you may lift: dispatch resumes on the next reconcile tick |
| `-model` | string |  |  | with -lift -connection, lift only the pause on this model of that connection |
| `-now` | bool |  |  | with a task id: skip a running task's grace period and stop its run at once |
| `-reason` | string |  |  | why, alongside -set: shown in this reading and on the UI's banner |
| `-resume` | bool |  |  | let this deployment pick work up again, lifting the pipeline pause |
| `-set` | bool |  |  | stop this deployment picking new tasks up -- runs already in flight are left to finish |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/pause_test.go`: `TestPrintAgentPause`, `TestPrintAgentPauseJSON`, `TestRunCLIPauseReadsAndLifts`, `TestRunCLIPausesAndResumesThePipeline`, `TestRunCLIPauseWithNoGateWired`; `pkg/cli/args_test.go`: `TestPauseRefusesAReasonWrittenWithoutItsFlag`.

### 3.9 `grain reply`, `inbox`, `wait` and `logs` {#CLI-65}

The external-agent workflow's answering and polling verbs ([external-cli #XC-20..#XC-23](../external-cli/external-cli.spec.md#XC-20)).
- `reply <id> [<body...> | -body-file <path|->]` answers the question task `<id>`'s run parked on (`POST /api/tasks/{id}/reply`). It is refused with exit `2`, posting nothing, when no `ask_question` is parked and when the task waits on a `request_secret`, which names `/tasks/<id>` and its encrypted input instead; `comment` posts a general note either way.
- `inbox [-repo R] [-kind question|secret|notification|merge-held|proposal]` lists what needs a person (`GET /api/inbox`) on the repositories the caller reads, one item per line with its root-relative UI path.
- `wait <id...> [-until closed|merged|awaiting-reply|ci-done|any[,…]] [-timeout 30m]` re-issues `GET /api/tasks/wait` slices of at most 60 s. It exits `0` when a condition is met, `1` when `-until merged` (without `closed`) meets a task that closed unmerged or `-until closed` a failed one, and `5` when `-timeout` (the global flag, written after the verb; `30m` when unset) runs out, printing the `grain wait …` line that carries on.
- `logs <id> [-attempt N] [-tail N] [-follow]` prints the latest (or `N`th) attempt's activity and transcript; `-follow` streams new lines until the attempt finishes, exiting `1` when it errored.

<!-- BEGIN GENERATED FLAGS: grain reply -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-attach` | value |  |  | path to a local file to attach to the reply (repeatable) |
| `-body-file` | string |  |  | read the reply from this path (- for stdin) instead of the words after the id |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain inbox -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-kind` | string |  |  | only items of this kind: question, secret, notification, merge-held, proposal |
| `-repo` | string |  |  | only this repository's (owner/name) items |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain wait -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-until` | string | `"closed"` |  | what to wait for, comma-separated for the first of several: closed, merged, awaiting-reply, ci-done, any |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain logs -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-attempt` | int | `the latest` |  | the attempt to read, 1 for the first |
| `-follow` | bool |  |  | stream new lines until the attempt finishes (exit 0 when it ends normally, 1 when it errored) |
| `-tail` | int |  |  | only the last N lines of the transcript |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/reply_test.go`: `TestGrainReplyAnswersQuestionRefusesIdleAndRefusesSecretRequest`; `pkg/cli/inbox_test.go`: `TestGrainInboxMatchesActionableCategoriesWithinRepoGrants`; `pkg/cli/wait_test.go`: `TestTasksWaitEndpointWakesOnConditionsEarnsNoWaitCreditAndExits5OnTimeout`; `pkg/cli/logs_test.go`: `TestGrainLogsStreamsAndExitsOnAttemptCompletion`.

---

## 4. Deployment commands (task CLI)

### 4.1 `grain config` {#CLI-30}

Calls `GET /api/config` and prints the capabilities this deployment offers.

<!-- BEGIN GENERATED FLAGS: grain config -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** *untested*.

### 4.2 `grain settings [flags]` {#CLI-31}

With no setting flags it prints the stored configuration (`GET /api/settings`). With any flag it sends `PUT /api/settings` with only the given fields and prints the result. Agent-key checks go through `POST /api/agent-keys/{framework}/check`. **Scope:** admin.

<!-- BEGIN GENERATED FLAGS: grain settings -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-actions-cache` | bool | `true` |  | whether this deployment's CI jobs are handed its GitHub Actions cache, so a cache-from: type=gha build is warm across runs. Every branch of a repository shares one |
| `-actions-cache-size-gb` | int |  |  | maximum Actions cache size in GiB (0 uses grain's default) |
| `-add-admin` | string |  |  | removed and ignored: whoever holds the access code is the deployment's owner |
| `-agent-framework` | string |  |  | removed and ignored: models are chosen through model connections (Settings -> Models) |
| `-check-agent-key` | string |  |  | test one agent framework's credential (antigravity, claude, codex) against the vendor that issued it, and print what came back -- changes nothing |
| `-ci-age-boost` | string |  |  | how long a queueing grain CI job waits to gain one step of rank, as a Go duration -- the floor that stops lower-ranked work starving (empty uses grain's default) |
| `-ci-artifact-days` | int |  |  | how many days a grain CI run's artifacts are kept before the sweep drops them (0 uses grain's default) |
| `-ci-artifact-file-mb` | int |  |  | largest single grain CI artifact kept, in MiB -- one past it is refused inside the job's own sandbox, so it is never copied out (0 uses grain's default) |
| `-ci-artifact-mb` | int |  |  | ceiling on the whole grain CI artifact directory, in MiB -- the oldest runs' artifacts are swept whole until it fits (0 uses grain's default) |
| `-ci-artifact-run-mb` | int |  |  | ceiling on what one grain CI run's artifacts may come to, in MiB (0 uses grain's default) |
| `-ci-cache-days` | int |  |  | how many days an unasked-for grain CI job result is kept before the cache's sweep drops it (0 uses grain's default) |
| `-ci-cache-entries` | int |  |  | ceiling on how many entries the grain CI job cache holds, alongside -ci-cache-mb (0 uses grain's default) |
| `-ci-cache-mb` | int |  |  | ceiling on the whole grain CI job cache directory, in MiB -- the oldest entries are swept until it fits (0 uses grain's default) |
| `-ci-jobs-per-run` | int |  |  | maximum grain CI jobs one run may hold sandboxes for at once (0 sets no per-run limit) |
| `-ci-reserve-after` | string |  |  | how long the sandbox budget must have been refusing a grain CI job before capacity is held for it rather than given to smaller jobs, as a Go duration (empty uses grain's default, negative turns reservation off) |
| `-ci-run-history` | int |  |  | how many concluded grain CI runs keep their record -- id, ref, commit and every verdict (0 uses grain's default, negative keeps every run) |
| `-ci-run-output-kb` | int |  |  | largest amount of step output one grain CI run may store, in KiB (0 uses grain's default) |
| `-ci-step-output-days` | int |  |  | how many days a grain CI run's step output may live, whatever room there is under the ceiling (0 uses grain's default, negative bounds it by size alone) |
| `-ci-step-output-mb` | int |  |  | ceiling on the grain CI step output kept across the whole history, in MiB -- whole runs' output is swept oldest first until it fits (0 uses grain's default, negative keeps every byte) |
| `-claude-model` | string |  |  | removed and ignored: models are chosen through model connections (Settings -> Models) |
| `-codex-model` | string |  |  | removed and ignored: models are chosen through model connections (Settings -> Models) |
| `-default-capabilities` | string |  |  | comma-separated capability IDs every new task is filed holding -- empty files each task with only what it asks for |
| `-deployment-id` | string |  |  | this deployment's id, which {deployment} in the branch template expands to -- lowercase letters, digits and hyphens; empty uses the random default |
| `-environment-name` | string |  |  | what this deployment is called in the UI, e.g. staging -- empty leaves it unnamed and shows nothing |
| `-gcp-agent-account-count` | int |  |  | the number of agent service accounts for gcp-key |
| `-gcp-agent-service-account` | string |  |  | the narrow agent service account gcp-key mints keys for |
| `-gcp-project` | string |  |  | GCP project the gcp-key/gemini-key capabilities mint into |
| `-gemini-effort` | string |  |  | removed and ignored: models are chosen through model connections (Settings -> Models) |
| `-gemini-model` | string |  |  | removed and ignored: models are chosen through model connections (Settings -> Models) |
| `-github-host` | string |  |  | GitHub API host (the one setting here that needs a daemon restart to take effect) |
| `-github-insecure-http` | bool |  |  | speak plain HTTP to -github-host instead of HTTPS (needs a daemon restart to take effect) |
| `-iap-backend-service` | string |  |  | GCP Compute backend service name for dynamic IAP IAM reconciliation |
| `-iap-resource` | string |  |  | IAP web resource whose IAP access is reconciled, e.g. projects/<NUMBER>/iap_web/compute/services/<SERVICE>; empty or "none" turns that off |
| `-k8s-container` | string |  |  | container name in the Kubernetes Deployment for self-upgrade |
| `-k8s-deployment` | string |  |  | Kubernetes Deployment name for self-upgrade |
| `-k8s-git-proxy-host` | string |  |  | host address Kubernetes sandboxes reach the git and model proxies at |
| `-k8s-granule-image` | string |  |  | packed guest image for Kubernetes Granule sandbox pods |
| `-k8s-kubectl` | string |  |  | kubectl binary path for Kubernetes sandboxes (empty uses kubectl on PATH) |
| `-k8s-namespace` | string |  |  | Kubernetes namespace sandbox pods are created in |
| `-k8s-network-policy` | bool |  |  | whether cluster CNI enforces NetworkPolicy, which network:none runs and external capability grants need |
| `-k8s-node-selector` | string |  |  | comma-separated key=value nodeSelector entries for Kubernetes sandbox pods |
| `-k8s-priority-class-agent` | string |  |  | PriorityClass for agent runs' and persistent sandboxes' Kubernetes sandbox pods; empty sets none |
| `-k8s-priority-class-ci` | string |  |  | PriorityClass for CI jobs' Kubernetes sandbox pods; empty sets none |
| `-k8s-priority-class-interactive` | string |  |  | PriorityClass for interactive tasks' Kubernetes sandbox pods; empty sets none |
| `-k8s-sandbox-image` | string |  |  | container image for Kubernetes sandbox pods |
| `-k8s-sandbox-privileged` | bool |  |  | run Kubernetes sandbox containers privileged |
| `-k8s-sandbox-service` | string |  |  | ClusterIP Service fronting the git and model proxies for Kubernetes sandboxes |
| `-k8s-sandbox-service-account` | string |  |  | ServiceAccount for Kubernetes sandbox pods |
| `-k8s-workspace` | string |  |  | working directory inside each Kubernetes sandbox pod |
| `-max-agent-turns` | int |  |  | cap on model/tool round trips per run (0 = uncapped; runs are bounded by wall-clock runtime instead) |
| `-max-ci-jobs` | int |  |  | deprecated and ignored: this deployment's sandbox budget is what bounds the host |
| `-max-mergers` | int |  |  | deprecated and ignored: this deployment's sandbox budget is what bounds the host |
| `-max-merges-per-repo` | int |  |  | maximum ready pull requests one repository may merge per queue cycle |
| `-max-workers` | int |  |  | maximum number of ordinary tasks dispatched at once |
| `-merge-batch-size` | int |  |  | deprecated and ignored: the merge queue no longer batches |
| `-merge-gather-window` | string |  |  | deprecated and ignored: the merge queue no longer batches |
| `-network-access` | string |  |  | how much of the internet a run's sandbox reaches unless its task says otherwise: "open" or "none" -- none needs granule sandboxes, and empty means open |
| `-package-cache` | bool | `true` |  | whether every sandbox's package managers are pointed at this deployment's package cache |
| `-package-cache-size-gb` | int |  |  | maximum package cache size in GiB (0 uses grain's default) |
| `-poll-interval` | string |  |  | how often the daemon runs a reconcile cycle, e.g. 10s |
| `-prompt-extension` | string |  |  | standing instructions appended to every run's prompt on this deployment -- empty adds nothing |
| `-sandbox-backend` | string |  |  | sandbox isolation method to use after the daemon restarts: container, granule, kubernetes, kubernetes-granule, host, none |
| `-sandbox-cpu-overcommit` | float | `2` |  | how much more vCPU than the sandbox host has may be committed to sandboxes at once; 0 leaves vCPU bounded by the run counts alone |
| `-sandbox-cpus` | int |  |  | deployment-wide default vCPU count for a granule-managed sandbox VM; 0 uses grain's own default, 2 |
| `-sandbox-disk-floor-min-mb` | int | `5120` |  | the least that floor is ever worth, in MiB, whatever the share comes to on a small volume -- a tenth of a 20 GB store disk is 2 GB, which is not enough room to notice a runaway write in |
| `-sandbox-disk-floor-share` | float | `0.1` |  | how much of each volume this deployment writes to is kept free: new CI jobs are held and agent dispatch deferred while free space is below it. A share of the volume, so 0.1 is a tenth; never more than a quarter is kept free however this is set, and 0 with a minimum of 0 holds nothing back at all |
| `-sandbox-disk-gb` | int |  |  | deployment-wide default root disk size, in GiB, for a granule-managed sandbox VM; 0 uses grain's own default, 30 |
| `-sandbox-disk-overcommit` | float | `2` |  | the same ratio for sandbox disk -- how many times over the sandbox volume (or a cluster's allocatable ephemeral storage), less its free-space floor, sandbox disks may be promised; on Kubernetes it also scales each pod's ephemeral-storage request. 0 leaves disk bounded by the floor alone |
| `-sandbox-image` | string |  |  | custom OCI sandbox image for the active backend, built FROM grain's guest-base or k8s-sandbox-base image (empty uses the image this build is stamped for; see docs/sandbox-image-contract.md) |
| `-sandbox-image-mirror` | string |  |  | OCI repository (without tag, e.g. us-docker.pkg.dev/<cluster-project>/grain/guest) into which the build-stamped guest sandbox image is mirrored on upgrade and startup so a cluster in another GCP organization can pull it |
| `-sandbox-memory-mb` | int |  |  | deployment-wide default guest memory, in MiB, for a granule-managed sandbox VM; 0 uses grain's own default, 8192 |
| `-sandbox-memory-overcommit` | float | `1` |  | the same ratio for memory -- 1 promises exactly what the host has, and the way a host finds memory it has already promised is to swap |
| `-sandbox-memory-reserve-mb` | int | `-1` |  | memory kept back from that budget, in MiB, for everything on the sandbox host that is not a sandbox: the daemon, the container runtime, each VM's own qemu overhead and the page cache they read through. Negative uses grain's own, an eighth of the machine and never less than 2 GiB |
| `-sandbox-overcommit` | string |  |  | overcommit mode: fixed holds the configured ratios, auto adjusts them from host CPU/memory PSI within the bounds below |
| `-sandbox-overcommit-cpu-max` | float | `6` |  | upper bound on effective CPU overcommit in auto mode (0 uses grain's default, 6.0) |
| `-sandbox-overcommit-cpu-min` | float | `1` |  | lower bound on effective CPU overcommit in auto mode (0 uses grain's default, 1.0) |
| `-sandbox-overcommit-cpu-pressure-target` | float | `25` |  | target CPU PSI some avg60 percentage in auto mode (0 uses grain's default, 25) |
| `-sandbox-overcommit-memory-max` | float | `2` |  | upper bound on effective memory overcommit in auto mode (0 uses grain's default, 2.0) |
| `-sandbox-overcommit-memory-min` | float | `1` |  | lower bound on effective memory overcommit in auto mode (0 uses grain's default, 1.0) |
| `-sandbox-overcommit-memory-pressure-target` | float | `5` |  | target memory PSI some avg60 percentage in auto mode (0 uses grain's default, 5) |
| `-sandbox-overcommit-mode` | string |  |  | alias for -sandbox-overcommit (fixed or auto) |
| `-session-sandbox-idle-ttl` | string |  |  | how long an interactive session task's sandbox (and in-process agent session) stays warm between turns awaiting a reply (e.g. 30m; empty uses grain's default, -1s disables warm retention) |
| `-task-branch-template` | string |  |  | branch template for new tasks, using {id} and optionally {deployment}, {instance} or {slug} (a few words of the task's title, only at the end after {id} and a hyphen, as in grain/{deployment}/task-{id}-{slug}) -- empty uses grain/task-{id} |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/settings_prompt_extension_test.go`, `pkg/cli/settings_sandbox_shape_test.go`, `pkg/cli/settings_environment_name_test.go`, `pkg/cli/settings_capabilities_test.go`; `pkg/cli/sync_test.go`: `TestSettingsDiffRowsCoverEveryUpdatableSetting`.

### 4.3 `grain sandbox-budget` and `grain sandbox probe` {#CLI-32}

- `sandbox-budget` reads `GET /api/settings` and says whether one sandbox of this deployment's shape fits its budget, and which settings would fit when it does not.
- `sandbox probe -id <id> [-access dialout|kubectl] -- <command> [args…]` calls `POST /api/sandbox-probe`: build a throwaway sandbox, run the command over the guest-access channel, print its output, delete the sandbox. It is available on `-k8s-granule-sandboxes` deployments only. A failing command fails the CLI, and a missing `-id` or command is refused.

<!-- BEGIN GENERATED FLAGS: grain sandbox-budget -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain sandbox -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain sandbox probe -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-access` | string |  |  | the guest-access channel the deployment must be using: dialout or kubectl (default: whichever it uses) |
| `-id` | string |  |  | names the probe; the sandbox is probe-<id> and its pod's name carries it (required) |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/sandbox_probe_cmd_test.go`: `TestSandboxProbeSendsTheCommandAndPrintsItsOutput`, `TestSandboxProbeFailsWhenTheCommandDoes`, `TestSandboxProbeRefusesAMissingIDOrCommand`.

### 4.4 `grain repo <subcommand>` {#CLI-33}

Bare `grain repo` prints its usage and fails with `a repo subcommand is required`. **Scope:** admin for writes.

| Subcommand | Request |
|---|---|
| `list` | `GET /api/config`, then `GET /api/tasks` (`ListRepos` counts each repo's tasks): one line per repo |
| `add <owner/name>` | `POST /api/repos` (`-clone-url` optional) |
| `remove <owner/name>` | `DELETE /api/repos/{owner}/{name}` |
| `capabilities [-set a,b] <repo>` | read, or `PUT`, `/api/repos/{owner}/{name}/capabilities` |
| `ci-capabilities [-set a,b] <repo>` | the capabilities granted to CI jobs. Without `-set` this and the next three read `GET /api/repos/{owner}/{name}/capabilities` |
| `prompt-extension [-set text] <repo>` | the repo's standing instructions |
| `setup-command [-set cmd] <repo>` | the shell run in a fresh checkout |
| `unfixable-checks [-set a,b] <repo>` | checks no commit can turn green |
| `work-ownership [-set on\|off\|unset] [-item id] <repo>` | `/api/repos/{owner}/{name}/work-ownership` |
| `workflows <repo>` | `/api/repos/{owner}/{name}/workflows` |

<!-- BEGIN GENERATED FLAGS: grain repo -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain repo list -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain repo add -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-clone-url` | string |  |  | non-GitHub clone URL; uses ambient sandbox credentials |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain repo remove -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain repo capabilities -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-set` | string |  |  | comma-separated capability IDs -- empty clears this set |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain repo ci-capabilities -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-set` | string |  |  | comma-separated capability IDs -- empty clears this set |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain repo prompt-extension -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-set` | string |  |  | standing instructions appended to the deployment's for a run against this repo -- empty clears the repo's own |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain repo setup-command -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-set` | string |  |  | shell run in a fresh checkout of this repo before an agent's first turn -- empty clears it |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain repo unfixable-checks -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-set` | string |  |  | comma-separated check names nothing pushed to a branch here clears -- empty withdraws them all |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain repo work-ownership -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-item` | string |  |  | one work item to set instead of the repository default: a schedule's id, or "release" |
| `-set` | string |  |  | "on", "off", or "unset" to go back to having said nothing -- which is not the same as "off" |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain repo workflows -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/repo_test.go`: `TestRunCLIDispatchesTheRepoFamily`, `TestParseRepoCapabilities`, `TestParseRepoWorkOwnership`, `TestRepoUnfixableChecksPrintsWhatTheListDoesNotSay`.

### 4.5 `grain gcp-key list|get|set|delete` {#CLI-34}

The routes are `GET /api/gcp-keys`, `GET /api/gcp-keys/{name}`, `PUT /api/gcp-keys` and `DELETE /api/gcp-keys/{name}`. `ls` and `rm` are aliases. **Scope:** admin.

<!-- BEGIN GENERATED FLAGS: grain gcp-key -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain gcp-key list -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain gcp-key get -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain gcp-key set -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-account` | string |  |  | GCP agent service account email |
| `-count` | int | `1` |  | number of agent service accounts |
| `-description` | string |  |  | human-readable description of the key (kept as it is when omitted; pass --description= to clear it) |
| `-max-age` | string |  |  | maximum key age duration (e.g. 1h, 30m) |
| `-minter` | string |  |  | minter secret name (defaults to gcp-key-minter-<name>) |
| `-project` | string |  |  | GCP project ID |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain gcp-key delete -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** *untested*.

### 4.6 `grain capability-servers …` {#CLI-35}

These manage the deployment's capability servers (mcpServers):
- `list` (`ls`) calls `GET /api/capability-servers`;
- `add` (`set`) calls `PUT /api/capability-servers`;
- `remove` (`rm`), `enable` and `disable` call `POST /api/capability-servers/{action}`;
- `bind-secret` calls `POST /api/capability-servers/bind-secret`.

**Scope:** admin.

<!-- BEGIN GENERATED FLAGS: grain capability-servers -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain capability-servers list -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain capability-servers add -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-arg` | value |  |  | an argument to --command (repeatable) |
| `-command` | string |  |  | stdio server: the executable (a bare name on PATH, a clean absolute path, or a path relative to grain/config) |
| `-disabled` | bool |  |  | save the entry disabled |
| `-env` | value |  |  | NAME=VALUE for a stdio server's environment (repeatable) |
| `-header` | value |  |  | NAME=VALUE sent to an HTTP server (repeatable) |
| `-url` | string |  |  | HTTP server: its control-plane serverUrl |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain capability-servers remove -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain capability-servers enable -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain capability-servers disable -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain capability-servers bind-secret -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-env` | string |  |  | the env entry to bind |
| `-header` | string |  |  | the header to bind |
| `-prefix` | string |  |  | literal text before the reference, as in "Bearer " |
| `-secret` | string |  |  | the secret name, <name> or <secret>/<key> |
| `-value-stdin` | bool |  |  | store the secret's value, read from stdin, before binding it |
<!-- END GENERATED FLAGS -->

**Pinned by:** *untested*.

### 4.7 `grain host list|pending|accept|reject|codes|invite|rotate-code|revoke-code|rename|update|revoke|delete|delete-revoked|rekey|register|install|set-owner` {#CLI-36}

This is the host registry and its enrollment handshake (Settings -> Hosts; docs/host-placement.md). A host joins with one shared enrollment code (`grain host -server <url> -join <code>`) as a verified person and waits for the deployment owner to accept it; a legacy host is registered with its owner, the identity it dials in as through IAP. `grain hostds` (and `grain hostd <verb>`) are hidden aliases that keep working; a bare `grain hostds` still lists.
- `pending` calls `GET /api/hostds/pending` and prints each pending join request with its enrolling identity, host details and key fingerprint.
- `accept [-name -labels -capacity] <id>` calls `POST /api/hostds/pending/{id}/accept`; `reject <id>` calls `POST /api/hostds/pending/{id}/reject`. Both are the deployment owner's alone and audited.
- `codes` calls `GET /api/hostds/enrollment-codes`; `invite` creates one (`POST /api/hostds/enrollment-codes`) and prints the shared `--join` snippet; `rotate-code` and `revoke-code` call `.../{name}/rotate` and `.../{name}/revoke`. Revoking a code stops new joins and leaves accepted hosts alone.
- `rename <old> <new>` calls `POST /api/hostds/{name}/rename`; `update` calls `PATCH /api/hostds/{name}` (owner, labels, capacity); `revoke` calls `POST /api/hostds/{name}/revoke` and drops a live connection; `delete` calls `DELETE /api/hostds/{name}` for a revoked host that no model connection or capability server still names, clearing the usage-limit pauses keyed to it; `delete-revoked` calls `POST /api/hostds/delete-revoked` and deletes every revoked host nothing still names, listing the ones it skipped.
- `rekey <name>` moves a host registered with a legacy per-host token to a key-bound credential through the same owner-accepted join handshake.
- `list` (`ls`) calls `GET /api/hostds`.
- `register` (`create`) calls `POST /api/hostds`. It prints the token once, then the install instructions the daemon rendered (`install.lines`, `install.note`; `ui.RegisterHostdInstall`), which never carry the token.
- `install <name>` calls `GET /api/hostds/{name}/install` and prints the same instructions again, without rotating the token.
- `set-owner <name> <owner>` calls `PATCH /api/hostds/{name}`.
- `delete <name>` (`rm`) calls `DELETE /api/hostds/{name}`. It removes a revoked host and its tokens from the registry, so the name can be registered again. The daemon refuses a live host ("revoke it first"), `local`, and a host a model connection's `hostd`/`hostdFallback` or a capability server's `hostd` still names by name, listing each; the CLI prints that refusal as its error. It prints the usage-limit pauses keyed to the host that were cleared with it.
- `delete-revoked` calls `POST /api/hostds/delete-revoked`: it deletes every revoked host nothing still names and prints the ones it skipped, with why.

**Scope:** admin.

<!-- BEGIN GENERATED FLAGS: grain host list -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain host register -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-label` | value |  |  | k=v the host is selected by (repeatable) |
| `-owner` | string |  |  | the host's owner: the email or service account it connects as through IAP |
| `-token-file` | string |  |  | write the host's token to this file (mode 0600) rather than print it |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain host set-owner -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain host install -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain host pending -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain host accept -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-capacity` | int | `-1` |  | maximum concurrent agent CLI runs on this host (0 for unlimited) |
| `-label` | value |  |  | k=v label for the accepted host (repeatable) |
| `-labels` | string |  |  | comma-separated k=v labels for the accepted host |
| `-name` | string |  |  | name for the accepted host (defaults to sanitized hostname) |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain host reject -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain host codes -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain host invite -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-allow` | string |  |  | comma-separated allowed principals (user:<email>, group:<email>, github:<login>, org:<org>) |
| `-capacity` | int |  |  | default capacity for hosts enrolled with this code |
| `-expires` | string |  |  | expiry duration (e.g. 24h, 168h) or RFC3339 timestamp when creating a code |
| `-label` | value |  |  | default k=v label for hosts enrolled with this code (repeatable) |
| `-labels` | string |  |  | default comma-separated k=v labels for hosts enrolled with this code |
| `-max-uses` | int |  |  | maximum number of join requests this code may file (0 for unlimited) |
| `-name` | string |  |  | create a new enrollment code with this name (if omitted, shows the active snippet) |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain host rotate-code -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain host revoke-code -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain host rename -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain host update -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-capacity` | int | `-1` |  | set maximum concurrent agent CLI runs (0 for unlimited) |
| `-label` | value |  |  | k=v label for the host (repeatable) |
| `-labels` | string |  |  | replace labels with comma-separated k=v pairs |
| `-owner` | string |  |  | update the host's owner email or service account |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain host revoke -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain host delete -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain host rekey -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-key-file` | string |  |  | file where the host's Ed25519 private key (mode 0600) will be generated or loaded |
| `-token-file` | string |  | $GRAIN_HOSTD_TOKEN | file holding the host's current token, and where the new key-bound credential is saved ($GRAIN_HOSTD_TOKEN when unset) |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain host delete-revoked -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/hostds_test.go`: `TestHostdsRegisterAndSetOwner`, `TestHostdsDelete`.

### 4.8 `grain git-connections`, `grain git-ambient`, `grain git-login`, `grain default-model` {#CLI-37}

- `git-connections` reads `GET /api/deployment` and prints one `host credential` line per git connection.
- `git-ambient [-host h] [-kind github|git] [-base-url u] [-proxy u] [-source gh|git|auto] [-if-none]` calls `POST /api/deployment/git-connections`. It adds an ambient connection that asks this host's own `gh`/`git` login for a token on demand. It is for a daemon on the bare machine only.
- `git-login [-web|-from-gh|-with-token] [-host h] [-base-url u] [-proxy u] [-scope s] [-author name] [-client-id id] [-scopes list]` delegates the caller's own GitHub HTTPS token to the deployment and registers it with `POST /api/deployment/git-connections`, with signing on, so pull requests and comments made with it are signed `Signed by <Name> (via Grain)`. `-web` runs the GitHub OAuth Device Flow, `-from-gh` takes `gh auth token`, and `-with-token` reads the token from standard input; at most one may be given, and with none it uses an active `gh` login and falls back to the Device Flow. It has no `login` alias: `grain login` is the sign-in command ([#CLI-108](#CLI-108)). See `design/design/git-login.design.md`.
- `default-model [-if-none|-if-unserved] <model>` sends `PUT /api/deployment {"defaultModel"}`. Without either flag that is its only call. With `-if-none` or `-if-unserved` it first reads `GET /api/deployment` and leaves a set default alone (`This deployment already runs on <m> by default: leaving it as it is.`). `-if-unserved` also reads `GET /api/settings` when a default is set, and replaces it when no enabled connection serves it.

**Scope:** admin.

<!-- BEGIN GENERATED FLAGS: grain git-connections -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain git-ambient -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-base-url` | string |  |  | the host's scheme://host[:port], when that is not https on the default port |
| `-host` | string | `"github.com"` |  | git host the login is for |
| `-if-none` | bool |  |  | do nothing when the deployment already has a git connection on that host |
| `-kind` | string | `"github"` |  | the host's kind: github (a forge API) or git (clone, fetch and push only) |
| `-proxy` | string |  |  | the http(s)://host:port proxy the host is reached through, if any (read from the source's own tool when empty, for a registered source that has one) |
| `-source` | string | `"auto"` |  | which of this host's tools to ask: gh, git, auto (auto: a registered source on its own hosts; gh, then git, elsewhere) |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain git-login -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-author` | string |  |  | explicit name to sign pull requests and comments with (defaults to your GitHub profile name or login) |
| `-base-url` | string |  |  | the host's scheme://host[:port], when that is not https on the default port |
| `-client-id` | string | `"178c6fc778ccc68e1d6a"` | $GRAIN_GITHUB_CLIENT_ID | GitHub OAuth Client ID for -web device flow ($GRAIN_GITHUB_CLIENT_ID) |
| `-from-gh` | value |  |  | delegate the token from this machine's gh auth token login |
| `-host` | string | `"github.com"` |  | git host to authenticate against |
| `-proxy` | string |  |  | the http(s)://host:port proxy the host is reached through, if any |
| `-scope` | string |  |  | optional owner or owner/repo scope (empty sets the deployment-wide default on -host) |
| `-scopes` | string | `"repo,read:org,workflow"` |  | comma-separated OAuth scopes requested by -web |
| `-web` | bool |  |  | authenticate in a web browser using GitHub OAuth Device Flow |
| `-with-token` | bool |  |  | read the HTTPS token from standard input |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain default-model -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-if-none` | bool |  |  | do nothing when the deployment already has a default model |
| `-if-unserved` | bool |  |  | do nothing when the deployment already has a default model one of its enabled connections serves |
<!-- END GENERATED FLAGS -->

**Pinned by:** *untested* in `pkg/cli`; the routes are pinned in `pkg/ui`.

### 4.9 `grain broadcast` / `grain notify` {#CLI-38}

`broadcast [-all] <message…>` (alias `notify`) calls `POST /api/deployment/broadcast`. It delivers to running task agents, or with `-all` to queued and awaiting-reply tasks too. The sandbox CLI's `grain notify` is a different command ([`sandbox.spec.md` #CLI-98](sandbox.spec.md#CLI-98)).

<!-- BEGIN GENERATED FLAGS: grain broadcast -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-all` | bool |  |  | deliver to queued and awaiting-reply tasks too, not only running task agents |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain notify -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-all` | bool |  |  | deliver to queued and awaiting-reply tasks too, not only running task agents |
<!-- END GENERATED FLAGS -->

**Pinned by:** *untested*.

### 4.9a `grain qa` {#CLI-112}

Inspects or updates the deployment-wide quality control engine and single-instance pull-request rollout configuration (`GET /api/settings` and `POST /api/settings`, matching `Settings -> Tasks`).

- `qa` or `qa status` reads `GET /api/settings` and prints whether quality control is off or enabled alongside the active flow package, rollout percentage, passive control-arm shadow scoring flag, experiment epoch, and regime ID.
- `qa enable [-flow id] [-rollout pct] [-shadow-control=bool] [-epoch n]` enables quality control via `POST /api/settings`. Only flags explicitly passed are overwritten; when enabling an unconfigured deployment (`0%` rollout) without `-rollout`, the rollout defaults to `30%`.
- `qa disable` sets `qaEnabled` to `false` via `POST /api/settings`, leaving the stored flow, rollout percentage, shadow-control flag, and epoch intact.

**Scope:** `settings:read` (`status`), `settings:write` (`enable`, `disable`).

<!-- BEGIN GENERATED FLAGS: grain qa -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain qa status -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain qa enable -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-epoch` | int |  |  | experiment epoch (>=1) to re-randomize sticky work-tree assignments |
| `-flow` | string |  |  | quality flow package (golden-v1-minimal, deterministic-s3-only, repo-custom) |
| `-rollout` | int | `30` |  | percentage of work-tree PRs assigned to treatment (0..100); defaults to 30 when unconfigured |
| `-shadow-control` | bool | `true` |  | run passive shadow checks on control-arm PRs |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain qa disable -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/qa_test.go`: `TestQACommandStatusEnableDisableAndFlagPreservation`.

### 4.10 `grain release approve|reject|rollback vX.Y.Z` {#CLI-39}

`approve` and `reject` call `POST /api/repos/{owner}/{name}/pending-releases/{version}/approve` or `…/reject` (`DecideRelease`). `rollback` calls `POST /api/releases/{version}/rollback` (`RollbackRelease`), passing `-to` (`vA.B.C`) and `-reason` when given; `-bad` is an alias for the positional `vX.Y.Z`. `-repo` defaults to grain's own source repository (`ai-on-gke/grain`). On a repository that publishes from GitHub Actions (`ci_mode: github`) the answer is a signed dispatch of `.github/workflows/promote-release.yml`, and the command prints the dispatch instead of a grain CI run.

`grain release approval-key` prints the deployment's release-approval public key (`GET /api/release-approval-key`): the value for the GitHub repository variable `GRAIN_RELEASE_APPROVAL_KEY`, which that workflow verifies an Inbox approval against. A daemon started with `-release-approval-kms-key` signs with that Cloud KMS key version and reports its public key; changing the flag (or the store-held key it otherwise uses) means updating the variable to match.

Errors, each printed after the usage:
- an unknown verb: `unknown release command "<v>": approve, reject or rollback`;
- the wrong number of arguments: `grain release <verb> takes the pending release, vX.Y.Z` (or `grain release rollback takes the release to retract, vX.Y.Z`, or passing both `-bad` and a positional version);
- `-short-soak-ok` or `-allow-breaking` outside approve: `-short-soak-ok is for grain release approve` / `-allow-breaking is for grain release approve`;
- `-bad`, `-to` or `-reason` outside rollback: `-bad is for grain release rollback` / `-to is for grain release rollback` / `-reason is for grain release rollback`.

`-allow-breaking` approves a release that carries breaking changes without being the major release `RELEASE_MAJOR` asks for; it reaches the approval as `allow_breaking: true`.

With no arguments it prints the usage and returns `flag: help requested`. **Scope:** person; an agent is refused.

<!-- BEGIN GENERATED FLAGS: grain release -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-allow-breaking` | bool |  |  | approve only: accept a release that carries breaking changes without being the major release RELEASE_MAJOR asks for |
| `-bad` | string |  |  | rollback only: the promoted release to retract (vX.Y.Z), when not passed positionally |
| `-reason` | string |  |  | rollback only: why the release is retracted, recorded on its vX.Y.Z-retracted tag |
| `-repo` | string | `"ai-on-gke/grain"` |  | the repository whose release this is |
| `-short-soak-ok` | bool |  |  | approve only: accept a release whose staging soak was under the minimum (its notification says SHORT SOAK) |
| `-to` | string |  |  | rollback only: the earlier release (vA.B.C) latest goes back to |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain release approve -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-allow-breaking` | bool |  |  | approve only: accept a release that carries breaking changes without being the major release RELEASE_MAJOR asks for |
| `-bad` | string |  |  | rollback only: the promoted release to retract (vX.Y.Z), when not passed positionally |
| `-reason` | string |  |  | rollback only: why the release is retracted, recorded on its vX.Y.Z-retracted tag |
| `-repo` | string | `"ai-on-gke/grain"` |  | the repository whose release this is |
| `-short-soak-ok` | bool |  |  | approve only: accept a release whose staging soak was under the minimum (its notification says SHORT SOAK) |
| `-to` | string |  |  | rollback only: the earlier release (vA.B.C) latest goes back to |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain release reject -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-allow-breaking` | bool |  |  | approve only: accept a release that carries breaking changes without being the major release RELEASE_MAJOR asks for |
| `-bad` | string |  |  | rollback only: the promoted release to retract (vX.Y.Z), when not passed positionally |
| `-reason` | string |  |  | rollback only: why the release is retracted, recorded on its vX.Y.Z-retracted tag |
| `-repo` | string | `"ai-on-gke/grain"` |  | the repository whose release this is |
| `-short-soak-ok` | bool |  |  | approve only: accept a release whose staging soak was under the minimum (its notification says SHORT SOAK) |
| `-to` | string |  |  | rollback only: the earlier release (vA.B.C) latest goes back to |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain release rollback -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-allow-breaking` | bool |  |  | approve only: accept a release that carries breaking changes without being the major release RELEASE_MAJOR asks for |
| `-bad` | string |  |  | rollback only: the promoted release to retract (vX.Y.Z), when not passed positionally |
| `-reason` | string |  |  | rollback only: why the release is retracted, recorded on its vX.Y.Z-retracted tag |
| `-repo` | string | `"ai-on-gke/grain"` |  | the repository whose release this is |
| `-short-soak-ok` | bool |  |  | approve only: accept a release whose staging soak was under the minimum (its notification says SHORT SOAK) |
| `-to` | string |  |  | rollback only: the earlier release (vA.B.C) latest goes back to |
<!-- END GENERATED FLAGS -->

**Pinned by:** `TestGrainReleaseRollbackCLI` in `pkg/cli/release_test.go`.

### 4.10a `grain upgrade [status|start|cancel|rollback]` {#CLI-111}

Manages in-place upgrades of a running `grain daemon` deployment (`GET /api/upgrade`, `POST /api/upgrade`, `DELETE /api/upgrade/current`, `DELETE /api/upgrade/scheduled`). Requires `deployment:upgrade` on mutating subcommands (`deployment:read` for `status`).

- `grain upgrade` / `grain upgrade status` reads `GET /api/upgrade` and prints the running image or commit, any pending scheduled upgrade, whether an in-progress upgrade is still cancellable (with any remaining manual grace window or the point-of-no-return message), and the image or commit the last completed upgrade replaced.
- `grain upgrade start [-after-tasks-finish] [-image <ref>] [<branch>]` posts `POST /api/upgrade` to start an immediate upgrade (subject to `-upgrade-grace-window`) or schedule one after active tasks and CI jobs drain.
- `grain upgrade cancel [-reason <why>] [-scheduled]` cancels an upgrade currently in progress via `DELETE /api/upgrade/current`, recording the operator and reason in `config_audit` and ending progress in `cancelled`. If no upgrade is running and a scheduled upgrade is waiting to drain, it cancels `DELETE /api/upgrade/scheduled` (`-scheduled` cancels only a scheduled upgrade). Once the upgrade has reached its point of no return, the daemon returns `409 Conflict` (`restarting onto <target> — too late to cancel; upgrade back to <previous> afterwards`).
- `grain upgrade rollback` posts `POST /api/upgrade` with `rollback: true`, starting a rollback onto the build the last completed upgrade replaced: its digest-pinned `previousImage`, or on source deployments its `previousCommit`, checked out by object id. The daemon reads the target from its own status file, and a rollback always starts now (it cannot be scheduled).
- For Grain Hub entries: `connected` instances run `pkg/upgrade` and support per-instance `grain -context <name> upgrade cancel`; hosted `instance` VMs run with `-disable-self-upgrade` and upgrade externally by VM replacement (`grain hub upgrade <name>`).

<!-- BEGIN GENERATED FLAGS: grain upgrade -->
_No flags._
<!-- END GENERATED FLAGS -->

#### `grain upgrade status`

<!-- BEGIN GENERATED FLAGS: grain upgrade status -->
_No flags._
<!-- END GENERATED FLAGS -->

#### `grain upgrade start`

<!-- BEGIN GENERATED FLAGS: grain upgrade start -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-after-tasks-finish` | bool |  |  | hold the queue and wait for running tasks and CI jobs to finish before upgrading |
| `-image` | string |  |  | explicit image repository, repo:tag or repo@sha256:<digest> to pull (-upgrade-image deployments only) |
<!-- END GENERATED FLAGS -->

#### `grain upgrade cancel`

<!-- BEGIN GENERATED FLAGS: grain upgrade cancel -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-reason` | string |  |  | why the upgrade is being cancelled (recorded in the deployment's audit log) |
| `-scheduled` | bool |  |  | cancel only a scheduled upgrade waiting for tasks to finish |
<!-- END GENERATED FLAGS -->

#### `grain upgrade rollback`

<!-- BEGIN GENERATED FLAGS: grain upgrade rollback -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `TestGrainUpgradeCLI` in `pkg/cli/upgrade_cmd_test.go`.

### 4.11 `grain metrics`, `grain failures`, `grain usage` and `grain health` {#CLI-40}

- `metrics [-window 7d | -since t -until t] [-repo R]… [-author A]…` calls `GET /api/metrics`. It prints throughput, latency, prompt sizes and sandbox cost, and says what it was scoped to. An impossible span is refused before the daemon is asked.
- `failures [-window 7d] [-suspect]` calls `GET /api/unmatched-failures` and prints what runs said when they failed for a reason grain could not name.
- `usage` calls `GET /api/usage` and prints token usage per connection and model: used in the window, the limit (configured, or learned from a provider's refusal and marked `~`), what is left, the next reset, the burn rate and the workers the remainder sustains, then the deployment rollup, the advisory estimate and warnings for default-list models about to run out. `-mode`, `-min-workers`, `-retention-days`, `-budget` (repeatable, replacing every budget) and `-clear-budgets` change the settings with `PUT /api/usage/settings` first. `-json` prints the whole response.
- `health` calls `GET /api/sandboxes` and prints its `counters`: each section a layer built on grain registered with `ui.RegisterHealthCounters`, sorted by key. They are in-process counts since the daemon started. `-json` prints the whole response.

<!-- BEGIN GENERATED FLAGS: grain metrics -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-author` | value |  |  | only tasks attributed to this author (repeatable -- any of them) |
| `-repo` | value |  |  | only tasks whose work lands in this repo, owner/name (repeatable -- any of them) |
| `-since` | string |  |  | measure from this moment instead of a window: a date like 2026-01-31, or an RFC 3339 timestamp (a bare date is midnight UTC) |
| `-until` | string |  |  | measure up to this moment rather than up to now |
| `-window` | string |  |  | how far back to measure: a duration like 36h, or a count of days or weeks like 7d (default 7d) |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain failures -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-limit` | int |  |  | at most this many failures (default: the server's own bound) |
| `-suspect` | bool |  |  | only the ones that read like a provider refusing the run |
| `-window` | string |  |  | how far back to read: a duration like 36h, or a count of days or weeks like 7d (default 7d) |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain usage -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-budget` | value |  |  | set a budget, connection[/model]=tokens/window (e.g. claude-1/opus=2000000/5h); repeatable, replaces every budget |
| `-clear-budgets` | bool |  |  | remove every configured budget |
| `-min-workers` | int | `-1` |  | set the floor the automatic cap never goes below; unset reads as 1 |
| `-mode` | string |  |  | set the concurrency mode: advisory (estimate only, the default), auto (also cap workers at the estimate) or off |
| `-retention-days` | int | `-1` |  | set how many days per-run usage is kept; unset reads as 30 |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain health -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/metrics_test.go`: `TestPrintMetricsSaysWhatItWasScopedTo`, `TestRunCLIMetricsScopesTheReport`, `TestRunCLIMetricsTakesAnAbsoluteSpan`, `TestCmdMetricsRejectsAnImpossibleSpanBeforeAsking`; `pkg/cli/failures_test.go`: `TestPrintFailures`, `TestPrintFailuresJSON`; `pkg/cli/health_test.go`: `TestGrainHealthShowsRegisteredCountersUntilUnregistered`; `pkg/cli/usage_cmd_test.go`: `TestParseBudget`, `TestGrainUsageShowsAndSets`.

### 4.12 `grain ci …` {#CLI-62}

This is grain's own CI, from the operator's side.
- A bare `grain ci` lists, like `list`.
- `run` calls `POST /api/ci/runs`.
- `list` calls `GET /api/ci/runs`. `-repo`, `-ref`, `-conclusion`, `-job` and `-since` narrow it, and `-sort`/`-order` order it. A span is resolved before it is sent.
- `show [-follow] <id>` calls `GET /api/ci/runs/{id}` and leads with the failure.
- `watch <id>` follows a run in flight.
- `cancel <id>` calls `DELETE /api/ci/runs/{id}`.
- `cache [clear]` calls `GET`/`DELETE /api/ci/cache`.
- `faults [-repo r] [-source s]` calls `GET /api/ci/faults`.
- `artifact` (`artifacts`) downloads from `/api/ci/runs/{id}/artifacts/{job}/{name}`.
- `wait (<run-id> | -pr <url|owner/name#n> | -ref <branch> -repo <owner/name>) [-jobs j1,j2]` ([external-cli #XC-26](../external-cli/external-cli.spec.md#XC-26)) polls `GET /api/ci/runs/{id}` until the run, or every job `-jobs` names, has a verdict. `-pr` and `-ref` attach to the newest run on that branch still in flight, else the newest; `-pr` finds the branch from the grain task tracking the pull request (`GET /api/tasks`). It waits the global `-timeout` (default `30m`) as a whole and accrues no wait credit. It prints the sandbox's wait answer (`gitproxy.CLIWaitResponse`, as [`grain ci wait` in a sandbox](sandbox.spec.md#CLI-91) reads it) plus `repo`, `ref` and `failing` (each failing job's step, failing tests and any shared fault from `GET /api/ci/faults`). It exits `0` green, `1` on `failure`/`cancelled`/`timed_out`, and `5` when `-timeout` runs out while the run is still going.
- `retrigger <run-id> [-jobs j1,j2]` (`ci:write`) reruns the run's failed jobs (or the named ones) once, uncached: `POST /api/ci/runs` with the run's repository, branch, commit and push/pull_request event, and `recheck`. A GitHub Actions run (`gha:<owner>:<name>:<run>`) is re-run on GitHub instead, as its next attempt: `POST /api/ci/runs/{id}/rerun` (`ci:write` on its repository) -- its failed and cancelled jobs, or the `-jobs` named -- bound to 3 re-runs per run at one commit. The sandbox's form is [#CLI-92](sandbox.spec.md#CLI-92).
- `test-history <test-name> [-repo r] [-limit N] [-provider grain|github-actions]` reads up to `-limit` (at most 100) of the newest runs, each in full, and prints `ci.QueryTestHistory`'s report, the [sandbox's `grain ci test-history`](sandbox.spec.md#CLI-89) rendering (`-json`: the report). For a `-repo` whose CI mode is `github` (`GET /api/repos/{owner}/{name}/capabilities`) and no `-provider`, it reads that repository's GitHub Actions runs alone -- each read in full carries the failing log tails grain recorded of its jobs -- and notes how many grain CI runs it left out; the report names each provider's runs and their window, and says that Actions runs count failures only.

<!-- BEGIN GENERATED FLAGS: grain ci -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-compact` | bool |  |  | each run as its id, ref, commit and conclusion and a line per job that did not succeed (the route's view=compact; with -json, its body) |
| `-conclusion` | value |  |  | only runs that stand this way: one of failure, errored, cancelled, skipped, success, running, pending (repeatable, or comma-separated, for several) |
| `-job` | string |  |  | only runs that have this job, by name or id -- whatever the job did |
| `-limit` | int | `20` |  | how many of the newest runs to show; 0 for as many as the daemon answers one listing with |
| `-order` | string | `"desc"` |  | which way that order points: desc (newest, or longest, first) or asc |
| `-ref` | string |  |  | only branches whose name contains this ("task-231" finds "grain/task-231") |
| `-repo` | string |  |  | only this repository's runs, as owner/name |
| `-since` | string |  |  | only runs started since then: a span back from now (24h, 7d, 90m) or a date (2026-09-17, or an RFC 3339 timestamp) |
| `-sort` | string |  |  | order by started, finished, duration (default started) |
| `-until` | string |  |  | only runs started before then, spelled as -since is; the two together are a window |
| `-workflow` | string |  |  | only runs of this workflow, by file name (e.g. nightly.yml), stem, path, or display name |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain ci run -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-branch` | string |  |  | the branch to test (required) |
| `-commit` | string |  |  | the SHA that branch is at, if it is known: what makes the result mean something afterwards, and what a cache is keyed by |
| `-event` | string |  |  | which event to run as: push, pull_request or workflow_dispatch (default: a manual dispatch without release secrets; name workflow_dispatch to get them) |
| `-job` | value |  |  | run one job id (repeatable; use workflow-path:job-id when ambiguous) |
| `-no-cache` | bool |  |  | bypass the CI result cache and re-run all matching jobs (alias for -recheck) |
| `-output` | string | `"failed"` |  | with -watch, whose output to print when it is over: failed, all, or none |
| `-recheck` | bool |  |  | bypass the CI result cache and re-run all matching jobs |
| `-repo` | string |  |  | the repository to test, as owner/name (required) |
| `-watch` | bool |  |  | wait for the run to finish, then print it -- and exit non-zero if it did not succeed |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain ci list -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-compact` | bool |  |  | each run as its id, ref, commit and conclusion and a line per job that did not succeed (the route's view=compact; with -json, its body) |
| `-conclusion` | value |  |  | only runs that stand this way: one of failure, errored, cancelled, skipped, success, running, pending (repeatable, or comma-separated, for several) |
| `-job` | string |  |  | only runs that have this job, by name or id -- whatever the job did |
| `-limit` | int | `20` |  | how many of the newest runs to show; 0 for as many as the daemon answers one listing with |
| `-order` | string | `"desc"` |  | which way that order points: desc (newest, or longest, first) or asc |
| `-ref` | string |  |  | only branches whose name contains this ("task-231" finds "grain/task-231") |
| `-repo` | string |  |  | only this repository's runs, as owner/name |
| `-since` | string |  |  | only runs started since then: a span back from now (24h, 7d, 90m) or a date (2026-09-17, or an RFC 3339 timestamp) |
| `-sort` | string |  |  | order by started, finished, duration (default started) |
| `-until` | string |  |  | only runs started before then, spelled as -since is; the two together are a window |
| `-workflow` | string |  |  | only runs of this workflow, by file name (e.g. nightly.yml), stem, path, or display name |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain ci show -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-follow` | bool |  |  | keep printing the run's output as it arrives, until it is over -- then print the summary and exit non-zero if it did not succeed (with -json: wait, then print the finished run once) |
| `-output` | string | `"failed"` |  | whose output to print: failed (the steps that failed, and the one running), all, or none |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain ci watch -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-follow` | bool | `true` |  | keep printing the run's output as it arrives, until it is over -- then print the summary and exit non-zero if it did not succeed (with -json: wait, then print the finished run once) |
| `-output` | string | `"failed"` |  | whose output to print: failed (the steps that failed, and the one running), all, or none |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain ci cancel -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-reason` | string |  |  | an optional note on why this run is being stopped |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain ci cache -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain ci faults -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-repo` | string |  |  | only this repository's shared faults, as owner/name |
| `-source` | string |  |  | only faults from this CI source: grain, github, or all |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain ci wait -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-jobs` | string |  |  | only these jobs, by name or id, comma-separated: the verdict is theirs alone |
| `-pr` | string |  |  | wait on the newest run of this pull request's branch: a GitHub URL or owner/name#n |
| `-ref` | string |  |  | wait on the newest run of this branch (needs -repo) |
| `-repo` | string |  |  | the repository -ref is a branch of, as owner/name |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain ci retrigger -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-jobs` | string |  |  | rerun these jobs, by id, comma-separated, rather than the ones that failed |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain ci test-history -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-limit` | int | `20` |  | how many of the newest runs to look through (at most 100) |
| `-provider` | string |  |  | only this CI's runs: grain or github-actions (default: github-actions for a -repo whose CI is GitHub Actions, grain otherwise) |
| `-repo` | string |  |  | only this repository's runs, as owner/name (default: every repository's) |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain ci artifact -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-o` | string |  |  | where to write it: a path, or - for standard output (default: the artifact's own file name, in the working directory) |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/cicmd_test.go`: `TestCICommandDispatch`, `TestCIRunRecheckFlags`, `TestCIListSendsTheWholeFilterVocabularyToTheDaemon`, `TestCIListResolvesASpanBeforeSendingIt`, `TestCIListRefusesAWordItDoesNotKnow`, `TestPrintCIRunLeadsWithTheFailure`, `TestPrintCIRunsJSON`, `TestExternalCIWaitVerdictsSharedFaultsZeroWaitCreditAndExitCodes`, `TestExternalCIRetriggerRunsUncachedFailedJobs`, `TestExternalCITestHistoryFormattingAndJSON`.

### 4.12a `grain merge-queue [-repo owner/name]` {#CLI-107}

The merge queue from a terminal ([external-cli #XC-27](../external-cli/external-cli.spec.md#XC-27)): queued pull requests, the batches and CI runs testing them, and held merge gates. It reads `deploy://merge-queue` over `GET /api/fs/deploy/merge-queue`, the file a [sandbox's `grain merge-queue`](sandbox.spec.md#CLI-95) reads over its proxy.
- It needs `ci:read` or `deployment:read`. Under `ci:read` alone the daemon leaves out every repository the caller may not read.
- `-repo` is sent as `?repo=` and narrows that further; it cannot widen it.
- `-json` is sent as `?format=json` and prints the report the text is rendered from (`mcp.MergeQueueReport`).
- It only reads. `grain force-merge` stays behind `merge:force`.

<!-- BEGIN GENERATED FLAGS: grain merge-queue -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-repo` | string |  |  | only this repository's queue, as owner/name |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/mergequeue_test.go`: `TestExternalMergeQueueFiltersByRepoAndJSON`.

### 4.13 `grain declarations check [dir]` {#CLI-42}

Reads a checkout's `.grain/templates`, `.grain/suites` and `.grain/schedules` and prints what they declare. It contacts no daemon. Every problem is reported at once and the exit is non-zero. The directory defaults to the working directory, and the repo defaults to `origin`'s. `-json` carries the verdict.

<!-- BEGIN GENERATED FLAGS: grain declarations -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-next` | int | `3` |  | how many upcoming firings to work out for each declared schedule (0 for none) |
| `-repo` | string |  |  | the repository these declarations belong to, as owner/name (default: the checkout's own "origin" remote) |
| `-zone` | string | `"UTC"` |  | IANA zone to preview schedule time of day against when no explicit time zone is declared |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain declarations check -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-next` | int | `3` |  | how many upcoming firings to work out for each declared schedule (0 for none) |
| `-repo` | string |  |  | the repository these declarations belong to, as owner/name (default: the checkout's own "origin" remote) |
| `-zone` | string | `"UTC"` |  | IANA zone to preview schedule time of day against when no explicit time zone is declared |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/declcmd_test.go`: `TestDeclarationsCheckPrintsWhatATreeDeclares`, `TestDeclarationsCheckReportsEveryProblemAtOnce`, `TestDeclarationsCheckJSONCarriesTheVerdict`, `TestDeclarationsCheckDefaultsToTheWorkingDirectory`.

### 4.14 `grain fs ls|cat|read|grep <uri>` {#CLI-64}

Reads grain's namespace (`ci://`, `task://`, `deploy://`, `config://`, `memory://`, `src://`) over the daemon's `GET /api/fs/<scheme>/<path>`, the way a sandbox's `grain ls`, `grain read` and `grain grep` read it over the sandbox proxy's `/fs/` ([external-cli #XC-24/#XC-25](../external-cli/external-cli.spec.md#XC-25)). Both share `pkg/nsclient`: the URI parsing (an inline query such as `ci://o/r/<run>?grep=FAIL` included), the query each verb sends, and how the answer is printed.
- `ls <uri>` lists a directory (`?list`); `-glob` narrows it and `-json` asks for the listing as JSON.
- `cat <uri>` prints a file; `read` is the same command. `-tail`, `-offset` and `-limit` count bytes, `-lines N` keeps the last N lines, and `-follow` keeps reading a file that is still growing.
- `grep <regex> <uri>` searches a file or subtree with line numbers; `-i`, `-C`, `-glob` and `-max` are the sandbox's own. A walk the namespace cut short before it found anything exits 1. `-json` prints the answer as one object.
- A refusal is the daemon's own (a missing scope, a repository the credential does not reach, `src://` to a `gma_` token without `deployment:src`), with the task CLI's exit codes.

<!-- BEGIN GENERATED FLAGS: grain fs -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain fs ls -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-glob` | string |  |  | list only the entries matching this pattern |
| `-json` | bool |  |  | print JSON |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain fs cat -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-follow` | bool |  |  | keep reading a file that is still growing |
| `-json` | bool |  |  | print JSON |
| `-limit` | string |  |  | at most N bytes |
| `-lines` | int |  |  | only the last N lines |
| `-offset` | string |  |  | start N bytes in |
| `-tail` | string |  |  | only the last N bytes |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain fs read -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-follow` | bool |  |  | keep reading a file that is still growing |
| `-json` | bool |  |  | print JSON |
| `-limit` | string |  |  | at most N bytes |
| `-lines` | int |  |  | only the last N lines |
| `-offset` | string |  |  | start N bytes in |
| `-tail` | string |  |  | only the last N bytes |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain fs grep -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-C` | string |  |  | lines of context around each match |
| `-glob` | string |  |  | search only the files matching this pattern |
| `-i` | bool |  |  | match case-insensitively |
| `-json` | bool |  |  | print JSON |
| `-max` | string |  |  | stop after N matches |
| `-n` | bool | `true` |  | print line numbers (always on) |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/fscmd_test.go`: `TestGrainFsLsCatReadGrepSharesClientWithSandboxCLI`; `pkg/ui/api_fs_test.go`: `TestAPIFsPerSchemeScopesRepoLimitsAndExternalSrcRefusal`, `TestAPIFsSecretRedactionRangeAndQueryParityWithSandboxProxy`.

---

### 4.15 `grain pr adopt|review|status|submit <pr>` {#CLI-65}

The external half of pull-request coordination ([`external-cli.spec.md`](../external-cli/external-cli.spec.md) #XC-28, #XC-29). `<pr>` is `owner/name#n` or the pull request's GitHub URL; anything else **MUST** fail with a usage error before any request is sent. Every verb takes `-json` and the global `-json`. The in-sandbox `grain pr` ([`sandbox.spec.md`](sandbox.spec.md)) is a different command about the task's own branch.

1. `grain pr adopt <pr> [-mode review-only|finish] [-spec <path>] [-design <path>]` calls `POST /api/pull-requests/adopt` (scope `merge:write`, and `tasks:write` on the pull request's repository) and prints the shepherd task's ID, state and mode. `-mode` defaults to `review-only`; `finish` on a pull request a review-only shepherd already holds upgrades that task in place. A filing that lands `proposed` says it waits for a person.
2. `grain pr review <pr> [-design] [-spec <path>]` calls `POST /api/pull-requests/review` (scope `tasks:write`, and `repos:read` on the repository) and prints the review task. `-design` asks for a design review: `design/spec/contributions/contributions.spec.md` conventions and overlapping designs under `design/`.
3. `grain pr status <pr> [-threads]` calls `GET /api/pull-requests/status` (scope `ci:read`, and `tasks:read` on the repository) and prints the head SHA, the shepherd task and mode, the latest grain CI run and its failing jobs, each merge gate and its state, each grain review with its state and finding count, the merge-queue gate, and with `-threads` every inline review thread's file, line and body.
4. `grain pr submit <pr> [-override-gates <gates> -reason <why> [-head <sha>]]` calls `POST /api/pull-requests/submit`. Without `-override-gates`, it needs `tasks:write` on the repository, records the caller's Submit approval only, and never skips or cancels any required review gate. With `-override-gates`, it needs `merge:force` on the repository and a non-empty `-reason`, explicitly names every unsatisfied gate being skipped (such as `security-review`, `review`, `ci`), and records the audited override on the task and pull request while keeping any running reviews alive post-merge.

#### `grain pr`

<!-- BEGIN GENERATED FLAGS: grain pr -->
_No flags._
<!-- END GENERATED FLAGS -->

#### `grain pr adopt`

<!-- BEGIN GENERATED FLAGS: grain pr adopt -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-design` | string |  |  | the design the change implements, as a repository path under design/design/ |
| `-json` | bool |  |  | print the shepherd task as JSON |
| `-mode` | string | `"review-only"` |  | review-only (grain reviews and gates the pull request without pushing) or finish (it also pushes fixes) |
| `-spec` | string |  |  | the specification the change implements, as a repository path under design/spec/ |
<!-- END GENERATED FLAGS -->

#### `grain pr review`

<!-- BEGIN GENERATED FLAGS: grain pr review -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-design` | bool |  |  | review the design: design-folder conventions (contributions.spec.md) and overlapping designs under design/ |
| `-json` | bool |  |  | print the review task as JSON |
| `-spec` | string |  |  | a specification to check the change's conformance against, as a repository path under design/ |
<!-- END GENERATED FLAGS -->

#### `grain pr status`

<!-- BEGIN GENERATED FLAGS: grain pr status -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-json` | bool |  |  | print the status as JSON |
| `-threads` | bool |  |  | list every inline review thread's file, line and body |
<!-- END GENERATED FLAGS -->

#### `grain pr submit`

<!-- BEGIN GENERATED FLAGS: grain pr submit -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-head` | string |  |  | the head commit SHA reviewed when overriding gates |
| `-json` | bool |  |  | print the submit result as JSON |
| `-override-gates` | string |  |  | comma-separated unsatisfied gates to override under merge:force (e.g. security-review, review, ci); never implied |
| `-reason` | string |  |  | required reason when -override-gates is set, recorded on the task and pull request |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/prcmd_test.go`, `pkg/ui/pull_requests_api_test.go`: `TestGrainPRReviewDesignAndPRStatusThreadsJSON`; `pkg/orchestrator/takeover_adopt_test.go`: `TestAdoptPullRequestReviewOnlyVsFinishUpgradeInPlace`, `TestAdoptPullRequestLinksSpecAndDesignIntoConformanceReviewAndProposalGate`, `TestPullRequestReviewDesignAndStatusThreads`.

---

### 4.16 `grain login`, `logout`, `whoami` and `grain context` {#CLI-108}

These sign a workstation in to a deployment as a named context, and say who it is signed in as. Their behaviour is [#XC-1](../external-cli/external-cli.spec.md#XC-1), [#XC-2](../external-cli/external-cli.spec.md#XC-2), [#XC-7](../external-cli/external-cli.spec.md#XC-7), [#XC-8](../external-cli/external-cli.spec.md#XC-8) and [#XC-9](../external-cli/external-cli.spec.md#XC-9).
- `grain context` needs no daemon. It reads and writes `$XDG_CONFIG_HOME/grain/config.json` and the credentials kept beside it.
- `grain login` defaults `--scope-set` to `grain/external-dev` and `--expires` to `30d`. `--auth` names `access-code` or a source a wrapper registered with `cli.RegisterCredentialSource`.
- `--token-stdin` stores the secret it reads as the context's credential, so `grain login` refuses it beside `--issue-token` with exit `2` (`token_stdin_issue_token`) and names the form that mints with an access code: `grain -token CODE login <url> --issue-token`.
- It refuses `--token-stdin` beside `--refresh` the same way (exit `2`, `token_stdin_refresh`), and `--scope-set`, `--repos` or `--expires` without `--issue-token` (exit `2`, `needs_issue_token`): each would otherwise be ignored. A refused login writes nothing.
- Signed in as a person through IAP (a Google ID token) or a registered credential source, with none of `--issue-token`, `--token-stdin` or `--refresh` given, `grain login` mints and keeps a personal `--scope-set` token as `--issue-token` would, so the CLI stops needing the proxy's sign-in or the access code day to day. `--issue-token=false` keeps a context that signs in through the proxy every time; a deployment with `externalAgentApi` off gets that context and a note on stderr.
- `grain logout --local` forgets the credential without revoking its token on the server.

#### `grain context list|use|show|delete`

<!-- BEGIN GENERATED FLAGS: grain context -->
_No flags._
<!-- END GENERATED FLAGS -->

#### `grain login <url>`

<!-- BEGIN GENERATED FLAGS: grain login -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-auth` | string |  |  | how to sign in: access-code, or a credential source this build registers |
| `-expires` | string | `"30d"` |  | how long --issue-token's token lasts (30d, 12h, ...) |
| `-issue-token` | bool |  |  | mint a scoped gma_ token for this workstation and keep it (the default when IAP or a credential source signs in a person; --issue-token=false declines) |
| `-name` | string |  |  | the context's name (default: from the server's host name) |
| `-refresh` | bool |  |  | re-mint the context's expired or expiring token |
| `-repos` | string |  |  | comma-separated repositories --issue-token limits the token to |
| `-scope-set` | string | `"grain/external-dev"` |  | the scope set --issue-token mints |
| `-token-stdin` | bool |  |  | read a gma_ token (or, with --auth access-code, the access code) from stdin and store it; not with --issue-token or --refresh |
<!-- END GENERATED FLAGS -->

#### `grain logout [<ctx>]`

<!-- BEGIN GENERATED FLAGS: grain logout -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-local` | bool |  |  | only forget the credential here; do not revoke the token on the server |
<!-- END GENERATED FLAGS -->

#### `grain whoami`

<!-- BEGIN GENERATED FLAGS: grain whoami -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/context_test.go`: `TestContextConfigXDGMode0600AndNoRawSecrets`; `pkg/cli/cliconn_test.go`: `TestContextCommandsAndServerPrecedence`; `pkg/cli/credentials_test.go`: `TestCredentialStoreKeyringFallbackAndRefusesWorldReadableFile`; `pkg/cli/login_test.go`: `TestGrainLoginIssueTokenSelfServiceNonAdminAndLogoutRevokes`; `pkg/cli/credsource_test.go`: `TestRegisterCredentialSourceAndHTTPClientRefusesLoginRedirectAndHTML`.

### 4.16a `grain skill install|status` and `grain mcp` {#CLI-110}

Installs and checks the workstation coding-agent skill guide (`SKILL.md`) and serves the optional local stdio 13-tool external coordination MCP server ([external-cli #XC-30](../external-cli/external-cli.spec.md#XC-30) and [#XC-31](../external-cli/external-cli.spec.md#XC-31)).
- `grain skill install` combines the embedded `pkg/cli/skill/SKILL.md.tmpl` static CLI guide with live deployment metadata from `GET /api/auth/whoami`, `GET /api/config`, and `GET /api/templates`, embeds the CLI version and SHA-256 deployment digest, refuses to write any bearer token or secret, and with `-mcp` registers `grain mcp` in the target agent's MCP configuration file.
- `grain skill status` checks recorded or standard skill paths for the active context and reports `installed`, `stale` (`version_mismatch` or `deployment_digest_mismatch`), or `missing`. Ordinary CLI calls emit at most a single-line notice on `stderr` when the recorded skill is stale, never touching `stdout`.
- `grain mcp [-context <name>]` speaks JSON-RPC 2.0 over `stdin`/`stdout` (distinct from in-sandbox `grain mcpserver`) exposing the 13 external coordination tools (`propose_task`, `read_grain_task`, `list_grain_tasks`, `wait_for_task`, `reply_to_task`, `comment_on_task`, `grain_inbox`, `ci_runs`, `ci_log`, `merge_queue`, `fs_read`, `fs_grep`, and `adopt_pull_request`) over the CLI's shared `ui.HTTPClient` and context credentials, slicing `wait_for_task` below MCP client tool timeouts and paging `list_grain_tasks`, `grain_inbox`, and `ci_runs` (`limit`, `cursor`, `q`; answers carry `total`, `truncated`, `nextCursor`) so each default answer fits the ~10 KB tool budget ([external-cli #XC-31](../external-cli/external-cli.spec.md#XC-31)).

#### `grain skill`

<!-- BEGIN GENERATED FLAGS: grain skill -->
_No flags._
<!-- END GENERATED FLAGS -->

#### `grain skill install`

<!-- BEGIN GENERATED FLAGS: grain skill install -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-claude` | bool |  |  | install for Claude Code (~/.claude/skills/grain/SKILL.md, or .claude/skills/grain/SKILL.md with -project) |
| `-codex` | bool |  |  | install for Codex CLI (~/.codex/skills/grain/SKILL.md, or .agents/skills/grain/SKILL.md with -project) |
| `-dir` | string |  |  | write SKILL.md into this directory |
| `-gemini` | bool |  |  | install for Gemini CLI (~/.gemini/skills/grain/SKILL.md, or .gemini/skills/grain/SKILL.md with -project) |
| `-jetski` | bool |  |  | install for Jetski (~/.gemini/jetski/skills/grain/SKILL.md, or .agents/skills/grain/SKILL.md with -project) |
| `-json` | bool |  |  | print installation summary as JSON |
| `-mcp` | bool |  |  | register grain mcp in the target agent's MCP server configuration |
| `-project` | bool |  |  | write into the current repository/project skill directory instead of the user home directory |
<!-- END GENERATED FLAGS -->

#### `grain skill status`

<!-- BEGIN GENERATED FLAGS: grain skill status -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-json` | bool |  |  | print status as JSON |
<!-- END GENERATED FLAGS -->

#### `grain mcp`

<!-- BEGIN GENERATED FLAGS: grain mcp -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-context` | value |  | $GRAIN_CONTEXT | the named context to use ($GRAIN_CONTEXT; see grain context list) |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/skill_test.go`: `TestGrainSkillInstallTargetPathsIdempotencyAndContainsNoSecrets`, `TestGrainSkillStatusDetectsVersionAndDeploymentDigestStaleness`, `TestStaleSkillStderrNoticeNeverCorruptsJSONStdout`; `pkg/cli/mcp_external_test.go`: `TestGrainMCPStdioToolsCallSharedClientAndSliceWaitForTask`.

---

### 4.17 `grain hub <subcommand>` {#CLI-109}

Self-registers a grain deployment in a grain hub (`design/design/hub.design.md`), queries the organization directory and path router, and manages hosted (`instance`) and self-registered (`connected`) entries.
- `status` (also bare `grain hub`) calls `GET /api/registry` and prints connection state, hub URL, entry metadata, last heartbeat, active and awaiting-reply task counts, any heartbeat error or scope conflicts, and advertised `(repo, path_prefix, role)` scopes.
- `list` calls `GET /api/entries` for the caller's entries (or `-all` for admins), with optional `-owner`, `-owner-group`, `-kind`, `-status`, `-listed`, `-conflict`, and `-include-deleted` filters, appending `detail=<status_detail>` when an entry carries a lifecycle or health detail.
- `directory` calls `GET /api/directory` across listed catalog entries with optional `-q`, `-repo`, `-tag`, `-status`, `-kind`, `-host-kind`, `-owner`, and `-limit` filters.
- `route` calls `GET /api/route` to resolve the primary and ranked candidate entries covering `-repo` and `-path` (with optional `-include-offline`).
- `show` calls `GET /api/entries/{name}` and prints full metadata, lifecycle step and `status_detail`/`health_detail` (`Step:`, `Detail:`, `Health:`), boot disk size (`Disk:`), tunnel status, scopes, conflicts, and credential metadata.
- `create` calls `POST /api/entries` (`kind = "instance"`) to provision a hosted VM entry (generating a random name when `<name>` is omitted).
- `register` creates or updates a `connected` entry on the hub (`POST /api/entries` or `PATCH /api/entries/{name}` / `POST /api/entries/{name}/credentials/rotate`), updates `Registry` in `POST /api/settings` (clearing any previous hub URL before swapping credentials when retargeting), stores any minted `grc_...` registration credential in the daemon's secret store under `registry-registration-token` (`PUT /api/secrets/registry-registration-token/value`), and runs an immediate heartbeat via `POST /api/registry/heartbeat`. `-standalone` registers on the hub without contacting a local daemon and prints the `-hub-connect` configuration snippet.
- `unregister` deletes the entry on the hub (`DELETE /api/entries/{name}` unless `-keep-remote` is given), clears `Registry` in `POST /api/settings`, and deletes `registry-registration-token` from the daemon's secret store.
- `update`, `scopes`, `rotate-credential`, `upgrade`, `resize`, `delete`, `restore`, `transfer`, and `accept-transfer` manage entry catalog fields, declared scopes, `grc_...` credentials, VM lifecycle replacements, soft-deletion/restoration, and two-step ownership transfers.
- `stop` and `start` call `POST /api/entries/{name}/stop` and `POST /api/entries/{name}/start` on a hosted `instance` entry the caller manages: `stop` moves it to `stopping` → `stopped` (no VM, externalised state kept) and `start` boots it again (a visit to a stopped entry also wakes it). Both are human-only on the hub (a delegating app is refused), take the same target flags as `upgrade`, and print the entry's new lifecycle `state`; a `connected` entry is refused with `invalid_entry_kind`.

**Scope:** `status` requires `api:read`; `register` and `unregister` require `admin` on the local daemon (unless `-standalone` is set); remote hub control-plane subcommands authenticate directly to `-hub-url` with OIDC or `-hub-token`.

<!-- BEGIN GENERATED FLAGS: grain hub -->
_No flags._
<!-- END GENERATED FLAGS -->

#### `grain hub status`

<!-- BEGIN GENERATED FLAGS: grain hub status -->
_No flags._
<!-- END GENERATED FLAGS -->

#### `grain hub list`

<!-- BEGIN GENERATED FLAGS: grain hub list -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-all` | bool |  |  | list all entries across the hub (admin only) |
| `-conflict` | bool |  |  | filter entries that have active primary scope conflicts |
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-include-deleted` | bool |  |  | include soft-deleted entries within their 30-day reservation window |
| `-kind` | string |  |  | filter by entry kind: instance or connected |
| `-listed` | bool |  |  | filter by listed state |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-owner` | string |  |  | filter by owner email |
| `-owner-group` | string |  |  | filter by co-owning group |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
| `-status` | string |  |  | filter by status: online, degraded, or offline |
<!-- END GENERATED FLAGS -->

#### `grain hub directory`

<!-- BEGIN GENERATED FLAGS: grain hub directory -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-host-kind` | string |  |  | filter by host kind: registry-vm, workstation, kubernetes, or other |
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-kind` | string |  |  | filter by entry kind: instance or connected |
| `-limit` | int |  |  | maximum number of entries to return |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-owner` | string |  |  | filter by owner email |
| `-q` | string |  |  | free-text search across name, display name, purpose, owner, tags, and scopes |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
| `-repo` | string |  |  | filter by repository (<owner/name>) |
| `-status` | string |  |  | filter by status: online, degraded, or offline |
| `-tag` | string |  |  | filter by catalog tag |
<!-- END GENERATED FLAGS -->

#### `grain hub route`

<!-- BEGIN GENERATED FLAGS: grain hub route -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-include-offline` | bool |  |  | include offline candidates in route resolution |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-path` | string |  |  | path within repository |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
| `-repo` | string |  |  | repository (<owner/name>) |
<!-- END GENERATED FLAGS -->

#### `grain hub show`

<!-- BEGIN GENERATED FLAGS: grain hub show -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
<!-- END GENERATED FLAGS -->

#### `grain hub create`

<!-- BEGIN GENERATED FLAGS: grain hub create -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-allow-ssh` | bool | `true` |  | enable OS Login break-glass SSH access |
| `-disk-gb` | int |  |  | boot disk size in GB (20..4000; defaults to shape default) |
| `-display-name` | string |  |  | human-readable display name in the hub directory |
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-listed` | bool |  |  | include this entry in default hub directory listings |
| `-name` | string |  |  | entry name (defaults to a random generated name when omitted) |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-owner-group` | string |  |  | optional co-owning group principal |
| `-purpose` | string |  |  | short description of this hosted grain's purpose |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
| `-scope` | value |  |  | declared repository scope <owner/repo>[:<path_prefix>[:<primary\|secondary>]] (repeatable) |
| `-shape` | string |  |  | VM shape name (defaults to hub default shape) |
| `-ship-logs` | bool |  |  | enable fleet log shipping |
| `-tags` | string |  |  | comma-separated catalog tags |
| `-zone` | string |  |  | optional GCE zone override |
<!-- END GENERATED FLAGS -->

#### `grain hub register`

<!-- BEGIN GENERATED FLAGS: grain hub register -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-connected-principal` | string |  |  | optional workload service account email authorized to connect on behalf of this entry |
| `-display-name` | string |  |  | human-readable display name in the hub directory |
| `-entry` | string |  | $GRAIN_HUB_ENTRY | entry name in the Grain Hub ($GRAIN_HUB_ENTRY; or positional <entry>) |
| `-host-kind` | string |  |  | connected entry host kind: "workstation", "kubernetes", or "other" |
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-listed` | bool | `true` |  | include this entry in default hub directory listings |
| `-mint-token` | bool |  |  | mint a fresh grc_... registration credential for this connected entry |
| `-oidc` | bool |  |  | use OIDC ID tokens for registration and daemon heartbeats instead of minting a grc_... token |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-owner-group` | string |  |  | optional group principal that co-owns this entry |
| `-purpose` | string |  |  | short description of this grain's purpose |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
| `-rotate-token` | bool |  |  | rotate an existing connected entry's grc_... registration credential |
| `-scope` | value |  |  | declared repository scope <owner/repo>[:<path_prefix>[:<primary\|secondary>]] (repeatable) |
| `-standalone` | bool |  |  | register on the hub without writing settings or secrets to a local grain daemon |
| `-tags` | string |  |  | comma-separated catalog tags |
| `-token-file` | string |  |  | with -standalone, write a minted grc_... token to this file (mode 0600) instead of printing it |
<!-- END GENERATED FLAGS -->

#### `grain hub unregister`

<!-- BEGIN GENERATED FLAGS: grain hub unregister -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-entry` | string |  | $GRAIN_HUB_ENTRY | entry name in the Grain Hub ($GRAIN_HUB_ENTRY; or positional <entry>) |
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for deleting the entry on the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-keep-remote` | bool |  |  | clear the daemon's local hub settings and credential without deleting the entry on the hub |
| `-oidc` | bool |  |  | authenticate remote entry deletion with an OIDC ID token |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
| `-standalone` | bool |  |  | delete the entry on the hub without contacting a local grain daemon |
<!-- END GENERATED FLAGS -->

#### `grain hub update`

<!-- BEGIN GENERATED FLAGS: grain hub update -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-allow-ssh` | bool |  |  | enable or disable break-glass SSH |
| `-connected-principal` | string |  |  | connected workload principal |
| `-display-name` | string |  |  | human-readable display name |
| `-host-kind` | string |  |  | connected entry host kind: workstation, kubernetes, or other |
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-listed` | bool |  |  | mark entry listed in directory |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-owner-group` | string |  |  | co-owning group principal |
| `-purpose` | string |  |  | short purpose description |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
| `-shape` | string |  |  | target VM shape |
| `-ship-logs` | bool |  |  | enable or disable fleet log shipping |
| `-tags` | string |  |  | comma-separated catalog tags |
| `-unlisted` | bool |  |  | mark entry unlisted in directory |
<!-- END GENERATED FLAGS -->

#### `grain hub scopes`

<!-- BEGIN GENERATED FLAGS: grain hub scopes -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-clear` | bool |  |  | clear all declared scope overrides on the entry |
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
| `-set` | value |  |  | declared scope <owner/repo>[:<path_prefix>[:<primary\|secondary>]] (repeatable) |
<!-- END GENERATED FLAGS -->

#### `grain hub rotate-credential`

<!-- BEGIN GENERATED FLAGS: grain hub rotate-credential -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-cred-id` | string |  |  | specific credential UUID to replace or expire |
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-label` | string |  |  | optional label for the new grc_... credential |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-overlap` | duration |  |  | grace period before previous credentials expire (for example 1h) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
| `-revoke-previous` | bool |  |  | immediately revoke previous active credentials |
| `-token-file` | string |  |  | write the newly minted grc_... token to this file (mode 0600) |
<!-- END GENERATED FLAGS -->

#### `grain hub upgrade`

<!-- BEGIN GENERATED FLAGS: grain hub upgrade -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
| `-version` | string |  |  | target grain release version for hosted VM replacement (defaults to hub's current pinned release; connected entries run in-place upgrades via "grain -context <name> upgrade") |
<!-- END GENERATED FLAGS -->

#### `grain hub resize`

<!-- BEGIN GENERATED FLAGS: grain hub resize -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-disk-gb` | int |  |  | boot disk size in GB (20..4000; defaults to shape default) |
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
| `-shape` | string |  |  | target VM shape name |
| `-version` | string |  |  | optional target grain version to apply during replacement |
<!-- END GENERATED FLAGS -->

#### `grain hub delete`

<!-- BEGIN GENERATED FLAGS: grain hub delete -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-confirm` | string |  |  | type the entry name to confirm deletion |
| `-delete-disk` | bool |  |  | immediately purge the instance database/disk instead of retaining it for the 30-day restore window |
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
| `-yes` | bool |  |  | confirm deletion without repeating the entry name |
<!-- END GENERATED FLAGS -->

#### `grain hub restore`

<!-- BEGIN GENERATED FLAGS: grain hub restore -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
<!-- END GENERATED FLAGS -->

#### `grain hub stop`

<!-- BEGIN GENERATED FLAGS: grain hub stop -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
<!-- END GENERATED FLAGS -->

#### `grain hub start`

<!-- BEGIN GENERATED FLAGS: grain hub start -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
<!-- END GENERATED FLAGS -->

#### `grain hub transfer`

<!-- BEGIN GENERATED FLAGS: grain hub transfer -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-cancel` | bool |  |  | withdraw the entry's pending transfer offer instead of making one |
| `-clear-owner-group` | bool |  |  | clear the entry's co-owning group |
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-owner-group` | string |  |  | optional co-owning group to set alongside transfer |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
| `-to` | string |  |  | email of the new individual owner to offer transfer to |
<!-- END GENERATED FLAGS -->

#### `grain hub accept-transfer`

<!-- BEGIN GENERATED FLAGS: grain hub accept-transfer -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
<!-- END GENERATED FLAGS -->

#### Delegating apps and consent grants

`grain hub delegating-apps list|create|disable|enable` is the registry-admin surface for `registry.delegating_apps` (`GET`/`POST /api/admin/delegating-apps`, `PATCH /api/admin/delegating-apps/<name>`): `create` takes the app's short name and `-principal`, a service-account email with or without the `serviceAccount:` prefix (the registry stores the prefixed form, so a bare email can no longer register an app that reads as `app_not_registered`). `grain hub grants list|create|revoke` is the person-facing surface for `registry.delegation_grants` (`GET`/`POST /api/delegation-grants`, `DELETE /api/delegation-grants/<grant-id|app>`): every grant is the signed-in person's own, and `revoke` takes a `dlg_…` ID or the app's name. Both verbs take the same `-hub-url` / `-hub-token` / `-oidc-audience` target flags as the rest of `grain hub`; `help` (or no verb) prints each group's usage.

#### `grain hub delegating-apps create`

<!-- BEGIN GENERATED FLAGS: grain hub delegating-apps create -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-description` | string |  |  | what the app does with the access it is granted |
| `-display-name` | string |  |  | name people see when granting consent (defaults to the app name) |
| `-homepage` | string |  |  | URL describing the app |
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-max-access` | string | `"write"` |  | most access a grant to this app may carry: read or write |
| `-max-grant-ttl` | duration |  |  | longest a grant to this app may last before it has to be renewed; 0 means 90 days |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-owner` | string |  |  | person accountable for the app (defaults to you) |
| `-owner-group` | string |  |  | optional group co-owning the app |
| `-principal` | string |  |  | service account the app calls as: an email, or serviceAccount:<email> |
| `-redirect-uri` | value |  |  | consent redirect URI the app may use (repeatable) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
| `-require-subject-token` | bool |  |  | require a subject token alongside on-behalf-of requests |
<!-- END GENERATED FLAGS -->

#### `grain hub delegating-apps disable`

<!-- BEGIN GENERATED FLAGS: grain hub delegating-apps disable -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
<!-- END GENERATED FLAGS -->

#### `grain hub delegating-apps enable`

<!-- BEGIN GENERATED FLAGS: grain hub delegating-apps enable -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
<!-- END GENERATED FLAGS -->

#### `grain hub delegating-apps list`

<!-- BEGIN GENERATED FLAGS: grain hub delegating-apps list -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
<!-- END GENERATED FLAGS -->

#### `grain hub grants create`

<!-- BEGIN GENERATED FLAGS: grain hub grants create -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-access` | string |  |  | read or write (defaults to the app's max access) |
| `-entry` | value |  |  | entry the app may reach on your behalf, or "*" for every entry you can reach (repeatable) |
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
| `-ttl` | duration |  |  | how long the grant lasts, capped at the app's max grant TTL; 0 means 30 days |
<!-- END GENERATED FLAGS -->

#### `grain hub grants list`

<!-- BEGIN GENERATED FLAGS: grain hub grants list -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-include-revoked` | bool |  |  | also list grants you have revoked |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
<!-- END GENERATED FLAGS -->

#### `grain hub grants revoke`

<!-- BEGIN GENERATED FLAGS: grain hub grants revoke -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-hub-token` | string |  | $GRAIN_HUB_TOKEN | bearer token for authenticating to the hub ($GRAIN_HUB_TOKEN) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub ($GRAIN_HUB_URL) |
| `-oidc-audience` | string |  |  | OIDC ID token audience for the hub (defaults to -hub-url) |
| `-registry-token` | value |  |  | deprecated: renamed to -hub-token |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/hub_test.go`: `TestHubCLIRegisterStatusUnregister`, `TestHubCLISubcommands`, `TestHubCLIStopStart`, `TestDaemonHubFlags`; `pkg/cli/hub_delegation_test.go`: `TestHubCLIDelegatingAppsAndGrants`.

---

## 5. Service modes

### 5.1 `grain daemon [flags]` {#CLI-43}

This runs `pkg/orchestrator`'s cycle on a timer and serves the UI and JSON API (`-ui-addr`, default `0.0.0.0:4242`). It is the only process that opens the store. Its flags configure the store, sandboxes, frameworks, CI, auth (`-ui-require-verifier`, `-ui-require-iap`, `-ui-hub-issuer`, `-ui-hub-audience`, `-ui-hub-keys-url`, `-ui-allowed-principals`; see [`authentication.spec.md`](../authentication/authentication.spec.md); `-host-accounts` and `-api-socket` for the unix socket the host CLI is recorded through by account, `docs/access-code.md`) and the agent CLI run locations (`-claude-run`, `-codex-run`, `-agy-run`; see `cli-run-location.design.md`). `daemon postgres` selects the PostgreSQL store path. The help text is pinned byte for byte.

<!-- BEGIN GENERATED FLAGS: grain daemon -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-actions-cache-dir` | string |  |  | root directory for the GitHub Actions cache -- defaults to <sandbox-dir>/cache/actions when -sandbox-dir is set, and <data-dir>/cache/actions otherwise |
| `-actions-cache-size-gb` | int | `20` |  | how much disk, in GiB, this deployment's GitHub Actions cache may use before it starts evicting the entries nothing has read for longest. It is what makes a cache-from: type=gha build warm across CI runs, and every branch of a repository shares one. 0 seeds a deployment that keeps no such cache. Seeds a fresh database only: the stored setting (Settings -> Sandbox, 'Actions cache') wins from then on |
| `-agy-path` | string |  |  | path to the Antigravity CLI binary (agy) agent/antigravity runs as a subprocess; empty resolves "agy" against $PATH instead. Only used by a run driven by the "antigravity" framework |
| `-agy-run` | string |  |  | where the agy CLI runs: "container" (a subprocess in the daemon's own container) or "host" (through the host agent, or a child of a daemon on the bare machine). Settings -> Models overrides it; empty is container in a container and host on the bare machine. "auto" is accepted and ignored for one release |
| `-allow-unisolated-host-sandboxes` | bool |  |  | let -host-sandboxes start on a daemon others can reach, accepting that everyone who can file a task there may run anything on this host as this daemon's user |
| `-api-socket` | string |  |  | unix socket to serve the API on as well, for host accounts -host-accounts or -deployment-owner names (default <data-dir>/api/grain.sock, served only when some account is mapped; "none" turns it off) |
| `-as` | string |  |  | principal the UI/API attributes tasks and comments it creates to (defaults to the OS user) |
| `-backup-gcs-bucket` | string |  |  | GCS bucket for scheduled backups; when set, upload to GCS instead of Google Drive |
| `-backup-google-credentials-file` | string |  |  | Google credential JSON for scheduled backups; omitted uses Application Default Credentials |
| `-backup-google-drive-folder` | string | `"root"` |  | Google Drive folder ID for scheduled backups |
| `-backup-interval` | duration |  |  | how often to upload the database and config repository to Google Drive or GCS; 0 disables scheduled backups |
| `-checkpost-expect` | string |  |  | which reporting checks make a repository's answer complete, as owner/name=check,check;owner/other=check -- a repository not named here is always read from GitHub |
| `-checkpost-url` | string |  |  | base url of this deployment's checkpost service, which CI reports verdicts to; empty asks GitHub for check runs as before |
| `-ci` | bool | `true` |  | run grain's own CI: the .grain/workflows a repository declares, in this deployment's own sandboxes, for the repos whose CI mode is "Use Grain CI" (Settings -> Repos), which also grants their tasks the https://grain.dev/capabilities/grain#ci={} grant. -ci=false turns the engine off entirely, so no repository's workflows run however its CI mode is set, and the CI API reports that this deployment has none. A job's sandbox is built the way a run's is and given no credential beyond a read-only checkout of the one repository under test (pkg/ci, "What a workflow file is trusted with"). |
| `-ci-artifact-max-age` | duration | `336h0m0s` |  | how long a run's CI artifacts are kept before the sweep drops them; 0 uses grain's own default, 336h0m0s, which is about how long a red run stays worth investigating. See -ci-artifact-max-bytes for what this group holds. |
| `-ci-artifact-max-bytes` | int | `2147483648` |  | ceiling on the whole CI artifact directory under -data-dir, in bytes -- the oldest runs' artifacts are swept whole until it fits (0 uses grain's own default, 2048MB). A job that does not succeed and declares 'artifacts:' has those paths copied out of its sandbox before the sandbox is destroyed, which is the only way a file survives a grain CI job at all; nothing here can change a verdict, so every bound in this group trades disk against how often somebody has to reproduce a failure by hand. Stored on the config row at first start and edited in Settings from then on, like the -ci-cache-max-* group beside it |
| `-ci-artifact-max-entry-bytes` | int | `33554432` |  | largest single CI artifact worth keeping, in bytes; one past it is refused inside the job's own sandbox -- so it is never copied out -- and the run records how big it was and that it was not kept. 0 uses grain's own default, 32MB. See -ci-artifact-max-bytes for what this group holds. |
| `-ci-artifact-max-run-bytes` | int | `134217728` |  | ceiling on what one CI run's artifacts may add up to, in bytes: one push of a workflow whose dozen jobs all fail and all keep a trace is the shape this bounds. 0 uses grain's own default, 128MB. See -ci-artifact-max-bytes for what this group holds. |
| `-ci-cache-max-age` | duration | `720h0m0s` |  | how long an unasked-for CI job result is kept before the cache's sweep drops it; 0 uses grain's own default, 720h0m0s. See -ci-cache-max-bytes for what this group is and is not about. |
| `-ci-cache-max-bytes` | int | `268435456` |  | ceiling on the whole CI job cache directory under -data-dir, in bytes -- the oldest entries are swept until it fits (0 uses grain's own default, 256MB). Lower it on a deployment whose data disk is small, raise it on one with room, where a bigger cache is simply more jobs that do not run twice. Nothing in this group can change an answer: a cache key is a promise that the work is identical, so every bound here trades disk against how often a job is re-run and no value of one can make CI report wrongly. Stored on this deployment's config row the first time it starts; change it in Settings (or 'grain settings') after that, where it binds the next sweep rather than the next restart -- 'grain ci cache' reports what is held against it, and 'grain ci cache clear' empties it now |
| `-ci-cache-max-entries` | int | `4096` |  | ceiling on how many entries the CI job cache holds, alongside -ci-cache-max-bytes: the entries are one flat directory and a hit is one open() in it, so small results would otherwise reach six figures long before they reached the byte ceiling. 0 uses grain's own default, 4096. See -ci-cache-max-bytes for what this group is and is not about. |
| `-ci-cache-max-entry-bytes` | int | `4194304` |  | largest single CI job result worth keeping, in bytes; a result past it is declined and logged rather than stored, and its job runs again next time. 0 uses grain's own default, 4MB, which is a job of a dozen or so maximally noisy steps. See -ci-cache-max-bytes for what this group is and is not about. |
| `-ci-run-history` | value |  |  | how many concluded grain CI runs keep their record -- the id, ref, commit and every job's and step's verdict, which is what "was this job red last week too?" is answered from and is the cheap half of a run. 0 uses grain's own default, 5000, which is a fortnight of a deployment starting a run every few minutes; a negative number keeps every run forever. Stored on this deployment's config row the first time it starts; change it in Settings (or 'grain settings') after that, where it takes effect without a restart. |
| `-ci-run-output-kb` | value |  |  | largest amount of step output one grain CI run may store, in KiB, spent on the steps a reader opens first: a run past it keeps the failing steps' logs and a note in place of the rest, and a run larger than the whole -ci-step-output-mb ceiling has its output declined rather than sweeping every older run's away. 0 uses grain's own default, 256KiB. Stored on this deployment's config row the first time it starts; change it in Settings (or 'grain settings') after that, where it takes effect without a restart. |
| `-ci-step-output-days` | value |  |  | how many days a grain CI run's step output may live, whatever room there is under -ci-step-output-mb. 0 uses grain's own default, 14 days; a negative number bounds output by size alone. Stored on this deployment's config row the first time it starts; change it in Settings (or 'grain settings') after that, where it takes effect without a restart. |
| `-ci-step-output-mb` | value |  |  | ceiling on the step output grain CI keeps across its whole history, in MiB -- whole runs' output is swept, oldest first, until what is left fits, and the runs themselves stay where they are. This is the expensive half and the only one that makes a red run diagnosable rather than merely countable, so it is bounded by disk rather than by a count of runs: what one run prints is between nothing and the -ci-run-output-kb cap. 0 uses grain's own default, 64MiB, which is three or four days of a deployment starting 300 runs a day; a negative number keeps every byte forever. Stored on this deployment's config row the first time it starts; change it in Settings (or 'grain settings') after that, where it takes effect without a restart. |
| `-ci-wake-backstop` | duration | `6h0m0s` |  | how long a task that resume_later deferred on a CI verdict may wait before grain wakes it anyway, with a note on the task and a stall notification; a negative duration, such as -1s, disables the backstop |
| `-claude-oauth-token-file` | string |  |  | file holding the Claude Code OAuth token the agent authenticates as, passed to the claude subprocess as CLAUDE_CODE_OAUTH_TOKEN. Optional, and the exact counterpart of -gemini-api-key-file above: the UI stores one as the "claude-oauth-token" secret and that wins over this file |
| `-claude-path` | string |  |  | path to the claude CLI binary agent/claude runs as a subprocess; empty resolves "claude" against $PATH instead. Only used when the agent-framework setting is "claude" |
| `-claude-run` | string |  |  | where the claude CLI runs: "container" (a subprocess in the daemon's own container) or "host" (through the host agent, or a child of a daemon on the bare machine). Settings -> Models overrides it; empty is container in a container and host on the bare machine. "auto" is accepted and ignored for one release. claude also takes "detached": the image's claude, through the host agent's claude-detached entry, so a daemon upgrade does not interrupt it |
| `-cli-container-engine` | string |  |  | deprecated and ignored: agent CLIs no longer run in a per-run container of an image |
| `-cli-image` | string |  |  | deprecated and ignored: agent CLIs no longer run in a per-run container of an image |
| `-cli-image-file` | string |  |  | deprecated and ignored: agent CLIs no longer run in a per-run container of an image |
| `-codex-auth-file` | value |  |  | shared auth.json written by codex login for ChatGPT authentication. Used only when no OpenAI API key is configured. The file must be writable so Codex can persist refreshed tokens; Grain links it into each private per-run CODEX_HOME |
| `-codex-path` | string |  |  | path to the codex CLI binary agent/codex runs as a subprocess; empty resolves "codex" against $PATH instead. Only used when the agent-framework setting is "codex" |
| `-codex-run` | string |  |  | where the codex CLI runs: "container" (a subprocess in the daemon's own container) or "host" (through the host agent, or a child of a daemon on the bare machine). Settings -> Models overrides it; empty is container in a container and host on the bare machine. "auto" is accepted and ignored for one release. codex also takes "detached": the image's codex, through the host agent's codex-detached entry, so a daemon upgrade does not interrupt it |
| `-container-git-proxy-host` | string |  |  | host address grain services are reachable at from local sandbox containers |
| `-container-sandbox-cap-add` | string |  |  | comma-separated Linux capabilities handed back to local sandbox containers, which otherwise run with every capability dropped and no-new-privileges set; the stock sandbox image needs none |
| `-container-sandbox-fence` | string | `"auto"` |  | auto, require or off: whether local sandbox containers are fenced off from this host, its networks and link-local (the metadata server) by a per-container nftables chain. auto fences when nft can be run and otherwise runs containers unfenced and refuses external capability grants; require refuses a container that cannot be fenced; off never fences and refuses external capability grants |
| `-container-sandbox-image` | string | `"docker.io/grainai/sandbox-k8s:latest"` |  | image used by local container sandboxes |
| `-container-sandbox-network` | string |  |  | Docker network local sandbox containers join. Empty uses grain-sandboxes, a bridge grain creates with inter-container traffic disabled; any other name must already exist ("bridge" is Docker's shared default network) |
| `-container-sandboxes` | bool |  |  | dispatch each run into its own local Docker container |
| `-data-dir` | string |  |  | root directory for the store and secrets -- the state a redeploy must not lose, and the one directory to back up (required; scripts/setup.sh installs it at /var/lib/grain/data) |
| `-db-driver` | string | `"sqlite"` |  | database backend driver ("sqlite" or "postgres"); defaults to sqlite |
| `-db-dsn` | string |  |  | database connection string / DSN or path; for sqlite defaults to <data-dir>/store |
| `-default-environment-name` | string |  | $GRAIN_DEFAULT_ENVIRONMENT_NAME | the environment name (the UI's tab title and sidebar badge) this deployment takes when it has none, or still has the one this flag gave it before: never one its operator set in Settings or its state repo. A Grain Hub sets it to a hosted instance's entry display name ($GRAIN_DEFAULT_ENVIRONMENT_NAME when unset) |
| `-default-target-repo` | string |  |  | owner/name a task created through the UI/API with no repo of its own targets |
| `-deployment-owner` | string |  | $GRAIN_DEPLOYMENT_OWNER | human email address that owns this deployment for RBAC (IsDeploymentOwner) and holds an immutable grain/operator floor on the access allowlist regardless of the OS user the daemon runs as ($GRAIN_DEPLOYMENT_OWNER when unset) |
| `-disable-native-sandboxes` | bool |  |  | select sandbox backend "none": run the control plane only -- no sandbox is created, so no task runs (for tests and walkthroughs) |
| `-disable-self-upgrade` | bool |  | $GRAIN_DISABLE_SELF_UPGRADE | disable in-VM self-upgrade actions (pkg/upgrade, automatic/scheduled upgrades, and SQLite-to-PostgreSQL cutover) and hide in-VM upgrade controls in the UI in favour of external lifecycle management ($GRAIN_DISABLE_SELF_UPGRADE when unset) |
| `-docker-root-dir` | value |  |  | path to the local docker engine's own data root, for the UI's host disk figures only (grain/task-148): the sandbox image and every granule VM's disk overlay are written there rather than under -sandbox-dir, so on a deployment that gives sandboxes a volume of their own it is a disk worth a figure. Empty asks docker info for it once at startup, which is right whenever the daemon can reach the docker socket and sees that path where dockerd does; name it here when it cannot, or when this process sees the same directory at a path of its own. A path this process cannot stat is logged once and left out, not reported as a broken figure -- a containerised daemon is usually shown docker's socket and not the tree behind it |
| `-flag-names` | value |  |  | print every flag this build's grain daemon accepts, one per line -- the name, then "bool" or "value" for whether it takes an argument -- and exit. Not configuration: it is what an upgrade asks a newly pulled image before cutting over to it, so that a systemd unit passing a flag the new build dropped is reported then rather than discovered at the next start (pkg/upgrade/image.go) |
| `-gcp-agent-account-count` | int |  |  | the number of agent service accounts to use for gcp-key (default 1) (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) (default 1) |
| `-gcp-agent-service-account` | string |  |  | the narrow agent service account gcp-key mints keys for (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-gcp-project` | string |  |  | GCP project the gcp-key/gemini-key capabilities mint into; empty disables both (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-gemini-api-key-file` | string |  |  | file holding the Gemini API key the agent runs as. Optional now that the key can be set from the UI instead (Settings -> Models, stored as the "gemini-api-key" secret): a key set there wins, and this file is what a deployment seeded one with before that existed. With neither, a run driven by the gemini framework fails as setup-failed saying so, rather than the daemon refusing to start |
| `-github-host` | string | `"github.com"` |  | GitHub git host -- what the proxy forwards to and, via github.APIHost, where REST calls go; override to point at a mock for local testing (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-github-insecure-http` | bool |  |  | speak plain HTTP to -github-host instead of HTTPS (mock servers only) (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-github-webhook-addr` | string |  |  | local address (e.g. :4244) to listen on directly for signed GitHub App webhooks using GITHUB_WEBHOOK_SECRET or a GitHub App credential's webhook_secret; mutually exclusive with -github-webhook-url |
| `-github-webhook-url` | string |  |  | base URL of this deployment's github-webhook gateway service (e.g. http://grain-webhook:8080), signed with GITHUB_WEBHOOK_RELAY_SECRET; empty disables remote webhook long-polling |
| `-granule-base-ip` | string |  |  | ignored, and removed in a later release. A granule guest takes over the address the container runtime assigned its container, and granule's own -ip is "deprecated and ignored"; there has been no way to assign one from here for some time. Assigning a sandbox's address is coming back by a different route -- pkg/sandboxaddr, reaching the VM as a docker run option on a user-defined network -- which is not this flag. |
| `-granule-base-port` | int |  |  | ignored, and removed in a later release. granule publishes ports on the sandbox itself (-docker-run-opt -p, or -publish-declared) rather than forwarding one per VM, and its own -port is "deprecated and ignored". |
| `-granule-command` | value |  |  | how to invoke granule, when it is not a granule on this machine's PATH: the whole command, space-separated, with granule's own arguments appended to it. granule builds only for Linux (its netshim is netlink, tc and tuntap), so a grain running on macOS has no granule to exec and reaches one in a local Linux VM instead -- -granule-command="limactl shell grain-sbx -- granule", or an ssh, or a docker exec. Setting it also moves granule's state directory to that machine, which is why grain then asks 'granule vm list' what VMs exist rather than reading -granule-state-dir itself (granule.Command.Exists). Left empty, granule is exec'd here, which is every deployment that runs grain and granule on one host. |
| `-granule-create-arg` | value |  |  | one argument appended verbatim to the granule vm create <name> -state-dir <dir> that builds a run's VM -- repeat for every flag and value bwsalmon/granule's own 'granule vm create -h' calls for beyond a name and -state-dir (disk mode, guest user, resolver, ...), e.g. -granule-create-arg=-disk-mode=overlay -granule-create-arg=-guest-user -granule-create-arg=debian, which is what scripts/setup.sh passes for terraform/gcp. The guest image itself -- the repository's Granulefile, built and published by scripts/release-images.sh (see scripts/granule/README.md) -- comes from -sandbox-image or this binary's own stamp rather than from here. Only used with -granule-sandboxes. Under -granule-net nat, prefer -granule-base-ip/-granule-base-port over putting -ip/-port here: they are appended last, so they win over this list. |
| `-granule-exec-key` | value |  |  | path, *inside the VM's container*, of the private key granule exec authenticates to the guest with. Optional, and normally left unset: 'granule run' generates a keypair for each guest it boots and hands the guest the public half on its kernel command line, so the default path already holds a key that guest authorizes (bwsalmon/granule's internal/guestkey). Set this only for a custom guest image that authorizes a key of its own instead -- e.g. /images/granule-exec-key for a key placed in the directory -granule-create-arg's own -images-hostpath mounts read-only at /images. |
| `-granule-git-proxy-host` | value |  |  | host (no port) this daemon's git proxy is reachable at from inside a granule VM's own guest, in place of the loopback address the proxy binds to by default -- required with -granule-sandboxes. A granule VM (GranuleConfig.createArgs always builds one against -backend docker) runs its guest in its own network namespace behind netshim's NAT (third_party/granule/internal/netshim), with its own 127.0.0.1 that never reaches this process's -- so a clone against the default 127.0.0.1 proxy URL fails the moment it leaves the guest (bwsalmon/agents#567: "Failed to connect to 127.0.0.1 ... Couldn't connect to server"). Setting this makes startGitProxy bind on every interface instead of just loopback, and advertise this host to every run's sandbox in loopback's place -- typically the docker bridge gateway address the guest's own outbound NAT routes through to reach this host (see netshim's masqueradeExprs); run docker network inspect bridge (or whichever network the granule VM containers join) and read "Gateway" if unsure -- commonly 172.17.0.1. |
| `-granule-net` | string | `"flat"` |  | ignored, and removed in a later release. The granule this build vendors has only the former "flat" behaviour -- its own -net says "deprecated: only the former flat behaviour exists now, and it is the default" -- so there is nothing left to select. A start that is passed this says so and carries on. |
| `-granule-ready-timeout` | duration |  |  | how long Acquire waits for a freshly created granule VM's guest to answer and carry a default route before giving the sandbox up; 0 uses the built-in two minutes. Worth raising wherever a guest boots slowly for reasons that are not grain's: nested virtualisation is the one measured here, where kernel and initramfs alone took 109s on an arm64 Mac under Lima and the default route landed at ~126s, two seconds past the built-in deadline, so every acquire failed with "vsock port 1024 is not being listened on" -- which reads as a networking fault and is a budget one. The same deadline has bitten before for a different slow boot (grain/task-593: 254 failed creations in one window), which is why it is a flag now rather than a constant. |
| `-granule-sandbox-cidr` | string |  |  | address pool every sandbox VM is given an address out of, as a CIDR -- for example 10.128.0.0/16. Empty (the default) is how every deployment has run until now: grain names no address, docker assigns one from whichever network the VM container lands on, and grain reads it back afterwards. Setting this makes the address grain's own choice, which is what lets the host isolation rules be written without first asking docker what happened, lets a sandbox be identified by where it is as well as by the token it carries, and stops two sandbox hosts handing out the same 172.17.0.0/16. Addresses are allocated in blocks of sandboxaddr.FamilySize, one block per sandbox and everything that sandbox goes on to create, so a whole family is one aligned CIDR and so destroying a parent frees its children with it. Requires -granule-sandboxes, and a pool big enough for the sandboxes this deployment runs at once: a /16 holds 2047 families of 32. |
| `-granule-sandbox-network` | string | `"grain-sandboxes"` |  | name of the user-defined docker network those addresses live on, created with -granule-sandbox-cidr as its subnet if it is not already there. It has to be user-defined and it has to be this deployment's: docker honours "--ip" only on a network it was given a subnet for and never on the default bridge, so there is no arrangement in which grain assigns an address on 172.17.0.0/16. Only used with -granule-sandbox-cidr. |
| `-granule-sandboxes` | bool |  |  | dispatch onto real bwsalmon/granule-managed VMs (one per run, named orchestrator.VMNamePrefix+<run id>) over SSH, instead of local host directories. This was -granule-vm-name-prefix, whose value both opted in and named the VMs; the name is a constant now, since a VM name has 11 bytes to live in and a run id needs nine of them, leaving nothing an operator could usefully choose -- see orchestrator.VMNamePrefix |
| `-granule-ssh-user` | string |  |  | username to SSH into each granule VM as (required with -granule-sandboxes) |
| `-granule-state-dir` | string | `"/var/lib/granule/vms"` |  | granule's VM state directory (only used with -granule-sandboxes) |
| `-granule-workspace` | string |  |  | working directory run_command/read_file/edit_file/write_file operate in on each granule VM (required with -granule-sandboxes) |
| `-grit-client-token` | value |  |  | client token (<id>.<secret>) sent in Grit-Client when -grit-url is set |
| `-grit-experimental` | bool |  |  | allow -grit-url, knowing it runs no repository's workflows, takes grit's attestation key from grit itself, and has no reverse-tunnel mode: for developing the grit split, not for a deployment's CI |
| `-grit-url` | value |  |  | EXPERIMENTAL, refused without -grit-experimental: remote grit server base url (e.g. http://127.0.0.1:4244); when unset, CI runs in-process. It runs none of a repository's workflows yet (design/design/grit-split.design.md, GS-3 to GS-5 deferred) |
| `-host-accounts` | string |  | $GRAIN_HOST_ACCOUNTS | comma-separated uid=email pairs naming the person each host account's requests on the API socket are recorded as (the daemon's own account maps to -deployment-owner unless named here; $GRAIN_HOST_ACCOUNTS when unset) |
| `-host-sandbox-env` | value |  |  | NAME or NAME=VALUE, repeatable, only with -host-sandboxes: an environment variable every host-sandbox run of this machine's own tasks (not anybody else's) gets on top of the default allowlist -- NAME alone passes this daemon's own value, NAME=VALUE sets that one. For ambient workstation credentials a run should use as you: SSH_AUTH_SOCK, HTTPS_PROXY/HTTP_PROXY/NO_PROXY, CLOUDSDK_CONFIG=$HOME/.config/gcloud. Every run then acts with that identity. HOME cannot be set (it stays pinned to the run's directory, which keeps its git credentials its own). Values reach runs through a 0600 file and are redacted from what run_command returns, but a NAME=VALUE entry sits on this daemon's own command line, so one with credentials in a URL (user:pass@) is refused: export it and pass NAME alone |
| `-host-sandboxes` | bool |  |  | dispatch each run into its own plain directory under -sandbox-dir on this host, with NO isolation: runs execute as this daemon's user, with this host's tools and network. For a single-user workstation; refused on a daemon others can reach (a non-loopback -ui-addr, IAP, -run-host-image) unless -allow-unisolated-host-sandboxes is also passed |
| `-hostd-reattach-timeout` | duration | `15m0s` |  | how long a run's agent CLI, running detached on a host whose connection drops, waits for that same host to reconnect before the run fails as an infrastructure failure (no strike against the task); re-attached in time, it carries on from where its output stood. Runs queued for a host that is not connected wait for it whatever this is. 0 fails at once |
| `-hostd-socket` | string |  |  | the host agent's unix socket, for a daemon in a container whose host CLIs run through the host agent; empty uses /run/grain-hostd/hostd.sock when its directory is mounted into the container. Refused on Kubernetes, which has no host |
| `-hostd-token-file` | string |  | $GRAIN_HOSTD_TOKEN | deprecated, for one release: file holding a shared token a host on another machine dials in to /api/hostd/connect with (grain host -server <this daemon's URL> -token-file ...), as the one host -hostd-token-name names: its host CLIs and run: host capability servers are then this daemon's host executor, Kubernetes included. $GRAIN_HOSTD_TOKEN when unset. Register each host in the host registry (/api/hostds) instead, which gives it a token of its own |
| `-hostd-token-name` | string | `"remote"` |  | the host name a host dialing in with -hostd-token-file's shared token is known by |
| `-hostd-token-owner` | string |  |  | the owner of -hostd-token-name's host: the email or service account a host dialing in with the shared token through IAP (or another identity verifier's proxy) must be verified as. A remote host with no owner is refused; one over loopback is not |
| `-hub-auth-mode` | string |  | $GRAIN_HUB_TOKEN | credential mode for hub heartbeats: "token" (secret registry-registration-token, -hub-token-file, or $GRAIN_HUB_TOKEN) or "oidc" (ID token for -hub-oidc-audience) (seeds stored configuration on first use; edit in Settings or with 'grain hub' after that) |
| `-hub-connect` | bool |  | $GRAIN_HUB_CONNECT | supervise an outbound wss://<hub-host>/api/entries/<name>/connect tunnel and serve accepted mux streams in-process behind the hub assertion verifier gate ($GRAIN_HUB_CONNECT; requires -ui-hub-issuer, -ui-hub-audience, and -ui-hub-keys-url) |
| `-hub-display-name` | string |  |  | human-readable display name advertised in the hub directory (seeds stored configuration on first use; edit in Settings or with 'grain hub' after that) |
| `-hub-entry` | string |  | $GRAIN_HUB_ENTRY | entry name this daemon registers and heartbeats as in the Grain Hub ($GRAIN_HUB_ENTRY) (seeds stored configuration on first use; edit in Settings or with 'grain hub' after that) |
| `-hub-host-kind` | string |  |  | connected entry host kind advertised to the hub: "workstation", "kubernetes", or "other" (defaults to the active sandbox backend) (seeds stored configuration on first use; edit in Settings or with 'grain hub' after that) |
| `-hub-listed` | bool | `true` |  | include this connected entry in default hub directory listings (seeds stored configuration on first use; edit in Settings or with 'grain hub' after that) |
| `-hub-oidc-audience` | string |  |  | OIDC ID token audience when -hub-auth-mode=oidc (defaults to -hub-url) (seeds stored configuration on first use; edit in Settings or with 'grain hub' after that) |
| `-hub-purpose` | string |  |  | short description of this grain deployment's purpose in the hub directory (seeds stored configuration on first use; edit in Settings or with 'grain hub' after that) |
| `-hub-token-file` | string |  | $GRAIN_HUB_TOKEN | fallback file holding a grc_... registration credential when secret registry-registration-token is unset ($GRAIN_HUB_TOKEN is also consulted) |
| `-hub-url` | string |  | $GRAIN_HUB_URL | base URL of the Grain Hub this daemon self-registers with ($GRAIN_HUB_URL; empty disables heartbeats) (seeds stored configuration on first use; edit in Settings or with 'grain hub' after that) |
| `-iap-backend-service` | string |  |  | GCP Compute backend service name for dynamic IAP IAM reconciliation; empty disables IAP reconciliation (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-iap-resource` | string |  |  | IAP web resource whose IAP access is reconciled from the teams and access list too, e.g. projects/<NUMBER>/iap_web/compute/services/<SERVICE>; empty or "none" disables it (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-k8s-container` | string | `"grain"` |  | name of grain's own container within that Deployment (only used with -k8s-sandboxes and -upgrade-image) (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-k8s-deployment` | string | `"grain"` |  | name of the Kubernetes Deployment running grain itself, which the Upgrade button repoints at a new image (only used with -k8s-sandboxes and -upgrade-image). deploy/kubernetes/grain.yaml calls it "grain" (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-k8s-git-proxy-host` | string |  | $GRAIN_POD_IP | address (no port) this daemon's git proxy and model proxies are reachable at from inside a sandbox pod, in place of the loopback address the proxy binds to by default -- required with -k8s-sandboxes, the same way -granule-git-proxy-host is required with -granule-sandboxes and for the same reason: a pod's 127.0.0.1 is its own. In-cluster this is grain's own pod IP, which every other pod can route to directly, so it defaults to $GRAIN_POD_IP when that is set -- deploy/kubernetes/deployment.yaml fills it from the downward API (status.podIP); see -k8s-sandbox-service (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-k8s-granule-access` | string | `"kubectl"` |  | k8s-granule only: how grain runs steps in a sandbox's guest -- "kubectl" (granule exec over kubectl exec) or "dialout" (the wrapper's channel to -sandbox-access-addr's acceptor; refused unless grain runs in-cluster). design/design/grain-on-gke.design.md#GKE-3 |
| `-k8s-granule-image` | string | `"docker.io/grainai/sandbox:latest"` |  | the packed guest image every pod this backend creates runs: a self-contained image with its own guest disk and kernel already baked in -- the shape Granulefile's own build produces, not the thin exec target -k8s-sandbox-image names and, just as importantly, not "granule:vendored" either (grain init's own -granule-runtime-image default, what scripts/granule/build-granule-image.sh produces, which has no guest disk and cannot boot at all -- confirmed against a real cluster, where a pod running it exits immediately). Required with -k8s-granule-sandboxes; no context-free default to pick, since nothing publishes a packed guest image under a stable tag yet -- docs/k8s-granule-design.md §4 (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-k8s-granule-reattach-timeout` | duration | `2m0s` |  | k8s-granule dialout only: how long a step waits for a dropped channel to reconnect before the sandbox is lost (a CI job errors with "infra: sandbox channel lost" and is re-run; an agent's tool call fails and its sandbox is rebuilt) |
| `-k8s-granule-sandboxes` | bool |  |  | dispatch into privileged Kubernetes pods, each booting a real cloud-hypervisor VM over /dev/kvm (one per run), instead of -k8s-sandboxes' plain pods or -granule-sandboxes' single-host VMs. Reuses -k8s-namespace/-k8s-kubectl/-k8s-workspace/-k8s-node-selector/-k8s-git-proxy-host, which mean the same thing on either Kubernetes backend; see -k8s-granule-image for the one that does not. docs/k8s-granule-design.md |
| `-k8s-kubeconfig` | string |  |  | kubeconfig path passed to kubectl for -k8s-sandboxes and -k8s-granule-sandboxes; when empty and KUBECONFIG is unset, defaults to <data-dir>/kubeconfig or /var/lib/grain/kubeconfig if present |
| `-k8s-kubectl` | string |  |  | kubectl binary pkg/k8s and the exec transport run (only used with -k8s-sandboxes); empty finds "kubectl" on PATH (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-k8s-namespace` | string | `"grain"` |  | namespace sandbox pods are created in (only used with -k8s-sandboxes). Two deployments sharing a cluster must not share this: the sweep for pods a dead process or a failed delete left behind (K8sSandboxes.SweepOrphans, at startup and on every reconcile tick) deletes every grain sandbox pod in the namespace it is not itself holding (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-k8s-network-policy` | bool |  |  | assert that this cluster's CNI actually enforces NetworkPolicy objects, and that deploy/kubernetes/networkpolicy.yaml is installed. Without it a task asking for no network access is refused rather than silently given a networked pod, since a policy object is accepted by every cluster and enforced by only some, and attempts holding an external capability grant are refused because nothing then keeps an open-network pod off the metadata server and the cluster. Only used with -k8s-sandboxes and -k8s-granule-sandboxes (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-k8s-node-selector` | value |  |  | one "key=value" nodeSelector entry for sandbox pods -- repeat for each. Pins sandboxes to a node pool sized for build workloads, or one whose nodes offer nested virtualization. Only used with -k8s-sandboxes (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-k8s-priority-class-agent` | string | `"grain-agent"` |  | PriorityClass for the sandbox pods of ordinary agent runs and persistent sandboxes; empty sets none (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-k8s-priority-class-ci` | string | `"grain-ci"` |  | PriorityClass for CI jobs' sandbox pods; empty sets none. The lowest of the three, so on a full cluster these are what an interactive session preempts, and a preempted job is retried (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-k8s-priority-class-interactive` | string | `"grain-interactive"` |  | PriorityClass for the sandbox pods of interactive tasks (Director sessions, chats); empty sets none. deploy/kubernetes/ defines it, and the daemon creates grain's own classes when they are missing -- a name the cluster still has no PriorityClass for is left off the pods, with an alert (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-k8s-run-host-rwx` | bool |  |  | the -k8s-run-host-volume claims are ReadWriteMany, so run pods may use any node; off, they are ReadWriteOnce and run pods are pinned to this pod's node |
| `-k8s-run-host-service-account` | string | `"grain"` |  | the service account run pods run as |
| `-k8s-run-host-volume` | value |  |  | CLAIM:PATH, repeatable: a claim this pod mounts at PATH; run pods get the spool, staging and credential directories as subPaths of these, at the same paths |
| `-k8s-sandbox-image` | string | `"docker.io/grainai/sandbox-k8s:latest"` |  | container image every sandbox pod runs; defaults to the image stamped into this build. It is the whole of what a dispatched agent finds when it lands. It needs the agent framework's own CLI, git, and a shell; Dockerfile.k8s-sandbox in this repo builds the published one (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-k8s-sandbox-privileged` | bool |  |  | run sandbox containers privileged (only used with -k8s-sandboxes). This gives every dispatched agent root on the node it lands on; it is here because a sandbox that has to run docker or kind cannot work without it, not because it is a reasonable default (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-k8s-sandbox-service` | string |  |  | name of a ClusterIP Service in -k8s-namespace that selects this daemon's pod and forwards the git proxy's and model proxies' ports from the -sandbox-port-base block (so it needs that block). When set, sandbox pods are handed the Service's cluster IP for those, in place of -k8s-git-proxy-host, so a sandbox that outlives this pod -- a run the next daemon pod adopts after a rollout -- can still push and still reach its model. The daemon resolves it at startup and refuses to start if it cannot. deploy/kubernetes/grain.yaml names its grain-sandbox Service. Only used with -k8s-sandboxes or -k8s-granule-sandboxes (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-k8s-sandbox-service-account` | string |  |  | ServiceAccount for sandbox pods (only used with -k8s-sandboxes). Normally left empty: nothing in a sandbox should be talking to the API server, and no token is mounted into one regardless (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-k8s-sandboxes` | value |  |  | dispatch into Kubernetes pods (one per run, named orchestrator.PodNamePrefix+<run id>) reached with kubectl exec, instead of local containers or granule VMs. Credentials are kubectl's own: in-cluster that is this pod's ServiceAccount token, which deploy/kubernetes/ binds the pod and pod/exec permissions this needs to. A pod shares its node's kernel, so this is weaker isolation than -granule-sandboxes |
| `-k8s-workspace` | string |  |  | working directory run_command/read_file/edit_file/write_file operate in inside each sandbox pod (required with -k8s-sandboxes) (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-kontur-base-port` | int |  |  | former name of -granule-base-port, still accepted so existing systemd unit files keep working |
| `-kontur-create-arg` | value |  |  | former name of -granule-create-arg, still accepted so existing systemd unit files keep working |
| `-kontur-exec-key` | string |  |  | former name of -granule-exec-key, still accepted so existing systemd unit files keep working |
| `-kontur-git-proxy-host` | string |  |  | former name of -granule-git-proxy-host, still accepted so existing systemd unit files keep working |
| `-kontur-net` | string |  |  | former name of -granule-net, still accepted so existing systemd unit files keep working |
| `-kontur-sandboxes` | bool |  |  | former name of -granule-sandboxes, still accepted so existing systemd unit files keep working |
| `-kontur-ssh-user` | string |  |  | former name of -granule-ssh-user, still accepted so existing systemd unit files keep working |
| `-kontur-state-dir` | string |  |  | former name of -granule-state-dir, still accepted so existing systemd unit files keep working |
| `-kontur-workspace` | string |  |  | former name of -granule-workspace, still accepted so existing systemd unit files keep working |
| `-local-scripts` | value |  |  | NAME=/absolute/path/local-scripts.yaml, repeatable: a host-managed local-scripts declaration, each of whose scripts a task holding its host-script grant is served (https://grain.dev/capabilities/host-script#host-script {script}; docs/local-scripts.md). Every other declaration is read from the state repository's local-scripts/NAME/local-scripts.yaml |
| `-management-url` | string |  | $GRAIN_MANAGEMENT_URL | optional external http:// or https:// URL where this deployment's lifecycle and upgrades are managed (for example its registry entry page), shown in place of in-VM self-upgrade controls when -disable-self-upgrade is set ($GRAIN_MANAGEMENT_URL when unset) |
| `-max-agent-turns` | int |  |  | cap on model/tool round trips per run (0 = uncapped; runs are bounded by wall-clock runtime instead) (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-max-ci-jobs` | int |  |  | deprecated and ignored; configure max CI jobs in Settings |
| `-max-concurrent` | int |  |  | former name of -max-workers, still accepted so an existing unit file keeps working; pass one or the other, not both |
| `-max-mergers` | int | `1` |  | deprecated and ignored: this deployment's sandbox budget is what bounds the host (model.Ledger) |
| `-max-workers` | int | `10` |  | maximum number of ordinary tasks dispatched at once -- the worker half of the concurrency dispatch.Cycle fills (model.Limits) (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-model-backend` | string |  |  | agy, claude or codex: the same as -agent-framework (agy is antigravity) |
| `-model-proxy-max-concurrent` | int | `16` |  | how many requests one run may have open through each model proxy at once, a streamed reply counting until it finishes. The next waits up to -model-proxy-slot-wait for one, then is refused with a 429 in the vendor's own error shape, so one run's token cannot monopolize this deployment's shared credential. 0 or less means the default |
| `-model-proxy-slot-wait` | duration | `1m0s` |  | how long a request over -model-proxy-max-concurrent waits for a slot before its 429, so stock vendor clients ride out the cap. 0 means the default; a negative duration, such as -1s, refuses at once |
| `-model-proxy-unrestricted` | bool |  |  | let the model proxies forward every method, path and query parameter of a vendor's API with this deployment's credential, rather than only its model-inference endpoints (messages, responses, chat completions, generateContent and the model listings). Off by default: a sandbox token then cannot reach a vendor's files, batch, fine-tuning or organization APIs. Turn it on only for a deployment whose runs deliberately need those |
| `-native-sandboxes` | bool | `true` |  | enable native sandboxes on this host; set to false to disable native sandboxes |
| `-object-store` | string |  | $GRAIN_OBJECT_STORE | object store URI (gs://<bucket>/instances/<name>/ or file:///abs/dir) for flushing run transcripts, uploaded CI artifacts, and durable agent-sessions archives with local read-through caching (defaults to $GRAIN_OBJECT_STORE) |
| `-openai-api-key-file` | string |  |  | file holding the OpenAI API key the agent authenticates as, passed to the codex subprocess as OPENAI_API_KEY. Optional, and the exact counterpart of the two files above: the UI stores one as the "openai-api-key" secret and that wins over this file |
| `-package-cache-apt-upstream` | value |  |  | fetch one intercepted archive from a mirror instead of from itself, as "archive-host=base-url" (deb.debian.org=http://mirror.internal/debian) -- repeat for each. The host is one this cache already serves, and is what sandboxes go on asking for; the URL is what this service fetches from, with the request's own path appended. An archive not named here is fetched from itself |
| `-package-cache-dir` | string |  |  | root directory for the package cache -- defaults to <sandbox-dir>/cache/packages when -sandbox-dir is set, and <data-dir>/cache/packages otherwise |
| `-package-cache-docker-registry` | string |  |  | the registry this cache mirrors for Docker Hub, for a deployment running a pull-through cache of its own (https://registry.internal). Defaults to https://registry-1.docker.io |
| `-package-cache-gcr-registry` | string |  |  | the registry this cache mirrors for Google Container Registry, for a deployment running a pull-through cache of its own (https://registry.internal/gcr). Defaults to https://gcr.io |
| `-package-cache-ghcr-registry` | string |  |  | the registry this cache mirrors for GitHub Container Registry, for a deployment running a pull-through cache of its own (https://registry.internal/ghcr). Defaults to https://ghcr.io |
| `-package-cache-go-proxy` | string |  |  | the Go module proxy this cache forwards to, for a deployment running an Athens of its own (http://athens.internal). Defaults to https://proxy.golang.org |
| `-package-cache-npm-registry` | string |  |  | the npm registry this cache proxies, for a deployment running a mirror of its own (http://mirror.internal/npm). Defaults to https://registry.npmjs.org |
| `-package-cache-pypi-index` | string |  |  | the PyPI simple index this cache proxies, for a deployment running a mirror of its own (http://mirror.internal/pypi/simple). Defaults to https://pypi.org/simple |
| `-package-cache-size-gb` | int | `20` |  | how much disk, in GiB, this deployment's package cache may use before it starts evicting the packages it fetched longest ago. 0 leaves it at the default |
| `-policy` | string |  |  | local policy file (grain_policy.yml) every task must fit before it gets a sandbox; empty enforces none (docs/local-policy.md) |
| `-poll-interval` | duration | `10s` |  | how often to run a reconcile cycle (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-postgres-url` | string |  |  | PostgreSQL connection URL (alias for -db-driver=postgres -db-dsn=URL) |
| `-reboot-cmd` | value |  |  | one argument of the command the UI's reboot-host button runs -- repeat for every argument. Defaults to sudo systemctl reboot. |
| `-registry-auth-mode` | value |  |  | deprecated: renamed to -hub-auth-mode |
| `-registry-connect` | bool |  |  | deprecated: renamed to -hub-connect |
| `-registry-display-name` | value |  |  | deprecated: renamed to -hub-display-name |
| `-registry-entry` | value |  |  | deprecated: renamed to -hub-entry |
| `-registry-host-kind` | value |  |  | deprecated: renamed to -hub-host-kind |
| `-registry-listed` | bool |  |  | deprecated: renamed to -hub-listed |
| `-registry-oidc-audience` | value |  |  | deprecated: renamed to -hub-oidc-audience |
| `-registry-purpose` | value |  |  | deprecated: renamed to -hub-purpose |
| `-registry-token-file` | value |  |  | deprecated: renamed to -hub-token-file |
| `-registry-url` | value |  |  | deprecated: renamed to -hub-url |
| `-relay-audience` | string |  |  | removed: the rendezvous relay is gone. The daemon refuses to start with this set |
| `-relay-auth-token-file` | string |  |  | removed: the rendezvous relay is gone. The daemon refuses to start with this set |
| `-relay-iap-jwt-audience` | string |  |  | removed: the rendezvous relay is gone. The daemon refuses to start with this set |
| `-relay-session-lifetime` | string |  |  | removed: the rendezvous relay is gone. The daemon refuses to start with this set |
| `-relay-token` | string |  |  | removed: the rendezvous relay is gone. The daemon refuses to start with this set |
| `-relay-url` | string |  |  | removed: the rendezvous relay is gone. The daemon refuses to start with this set |
| `-release-approval-kms-key` | string |  | $GRAIN_RELEASE_APPROVAL_KMS_KEY | Cloud KMS EC_SIGN_P256_SHA256 key version (projects/<p>/locations/<l>/keyRings/<r>/cryptoKeys/<k>/cryptoKeyVersions/<n>) to sign Inbox release approvals with, so the private key never leaves KMS; empty keeps the key in the secrets store (secret release-approval-key). Changing it changes the public key grain release approval-key reports, so each repository's GRAIN_RELEASE_APPROVAL_KEY variable must be updated to match; defaults to $GRAIN_RELEASE_APPROVAL_KMS_KEY |
| `-repair-reserve-sandboxes` | int | `1` |  | how many standard agent sandboxes' worth of that budget is kept for base-branch repairs -- tasks marked fixes_base_branch and the CI confirming their branch green -- so a red base branch's repair starts on a saturated deployment instead of queueing behind the work it blocks. 0 reserves nothing; a repair the reserve cannot hold may still pre-empt work blocked on the same base (model.Budget.RepairReserve) |
| `-repo` | value |  |  | a repository this workstation works on, as a local checkout's path or owner/name or URL; repeatable. Each is added to the -policy file's repos, added to the default team's target repositories when that list restricts them, and the first is -default-target-repo unless that is set |
| `-restart-cmd` | value |  |  | one argument of the command the UI's restart-grain button runs to restart this daemon alone, without rebooting the host -- repeat for every argument. Defaults to sudo systemctl restart grain-daemon.service. |
| `-run-host-credential-dir` | string |  |  | the shared credential directory every run host mounts read-write, as a directory and at the same path (only used with -run-host-image) |
| `-run-host-daemon-url` | string |  |  | the API address a run host's mcpserver is given, when not this process's own: a run pod needs one that outlives the daemon pod, e.g. http://grain.grain.svc |
| `-run-host-dir` | string |  |  | where run hosts' spools and staging live, seen at the same path by the daemon and every run host (default <data-dir>/runhosts; under -k8s-run-host-rwx, on that volume) |
| `-run-host-image` | string |  |  | drive each claude run in a run host of this image: a container that is a sibling of the daemon, not a child of it, writing to a spool the daemon tails (pkg/runhost, design/design/non-disruptive-upgrade.design.md). Normally the deployment image itself ("self" under -k8s-sandboxes reads it off this Deployment). Empty runs every agent CLI as a child of the daemon, as before. Not with -container-sandboxes or -host-sandboxes: a run host cannot reach either sandbox |
| `-sandbox-access-addr` | string |  |  | k8s-granule only: address to run the sandbox-access acceptor on (":4244"), which every sandbox pod's wrapper then dials out to over mTLS with its own certificate and token (pkg/sandboxaccess, design/design/grain-on-gke.design.md "Sandbox access"); empty leaves it off and builds pods as before |
| `-sandbox-access-ca-secret` | string |  |  | Secret Manager secret (projects/<p>/secrets/<s>) holding the sandbox-access CA, read through Workload Identity and generated into it on first start; empty keeps the CA in <data-dir>/sandbox-access-ca.pem |
| `-sandbox-access-dial` | string |  |  | the address sandbox wrappers dial the acceptor at; empty is grain-access.<-k8s-namespace>.svc:4244, the Service deploy/kubernetes/grain-granule.yaml creates |
| `-sandbox-backend` | string |  |  | container, granule, kubernetes, kubernetes-granule, host or none: the same as -container-sandboxes, -granule-sandboxes, -k8s-sandboxes, -k8s-granule-sandboxes, -host-sandboxes or -disable-native-sandboxes |
| `-sandbox-cpu-overcommit` | float | `2` |  | how much more vCPU than this host has may be committed to sandboxes at once -- 2 promises a 28-core host 56 vCPU of agent and CI sandboxes. Some CPU overcommitment is right: a run spends most of its wall time waiting on a model or a network rather than on a core. 0 leaves vCPU bounded by the run counts alone (model.Budget) (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-sandbox-cpus` | value |  |  | deployment-wide default vCPU count for a granule-managed sandbox VM, passed as granule vm create's own -cpus (only used with -granule-sandboxes); 0 uses grain's own default, 2. Overridable per task from the UI/API (model.Task.SandboxCPUs) (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-sandbox-dir` | string |  |  | root directory for this deployment's disposable scratch space -- host-sandbox run directories, and the package and Actions caches default under it, and the UI reports its disk as the sandboxes' own (scripts/setup.sh installs it at /var/lib/grain/scratch, beside -data-dir's /var/lib/grain/data). Deliberately a separate flag from -data-dir rather than a subdirectory of it (bwsalmon/agents#587): what lives here is disposable, unlike the store and secrets under -data-dir, so it is never backed up and belongs on storage -- ideally a disk of its own -- that a VM wipe or redeploy is free to discard |
| `-sandbox-disk-floor-min-mb` | int | `5120` |  | the least that floor is ever worth, whatever the share comes to on a small volume -- a tenth of a 20 GB store disk is 2 GB, which is not enough room to notice a runaway write in (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-sandbox-disk-floor-share` | float | `0.1` |  | how much of the volume this deployment writes to (-data-dir, and -sandbox-dir when that is a different filesystem) is kept free: new CI jobs are held and agent dispatch deferred while free space is below it. A share rather than a size because a fixed number is wrong on both a 100 GB and a 4 TB volume. Never more than a quarter of the volume, whatever this and -sandbox-disk-floor-min-mb come to. 0 with a min of 0 leaves a filling disk to hold nothing back (model.DiskFloor) (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-sandbox-disk-gb` | value |  |  | deployment-wide default root disk size, in GiB, for a granule-managed sandbox VM, passed as granule vm create's own -disk-size-mb (only used with -granule-sandboxes); 0 uses grain's own default, 30 -- every VM is sized explicitly, rather than being left as large as the guest image behind it the way it was before. Overridable per task from the UI/API (model.Task.SandboxDiskGB) (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-sandbox-disk-overcommit` | float | `2` |  | the same ratio for sandbox disk: how many times over the sandbox volume (less its free-space floor), or a cluster's allocatable ephemeral storage, sandbox disks may be promised. A sandbox disk is a thin overlay a run mostly never writes; on Kubernetes this also scales each pod's ephemeral-storage request down while its limit stays the full disk. 0 leaves disk bounded by the free-space floor alone (model.Budget) (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-sandbox-image` | value |  |  | the sandbox container image every granule VM is created from, overriding the reference stamped into this build at build time (grain sandbox-image). Left empty (the default), each start resolves that stamp, so an upgrade -- which pulls the new grain image and the sandbox image that grain names -- moves both halves together. Set it only to run something else on purpose: a mirror, a private copy, or a guest held at an older commit. This is where such a pin belongs; a -granule-create-arg -kontur-image naming another tag of the stamp's own repository is read as the install-time value an older scripts/setup.sh wrote unconditionally, and the stamp is used instead (chooseSandboxImage). Only used with -granule-sandboxes. |
| `-sandbox-image-mirror` | string |  |  | OCI repository (without tag, e.g. us-docker.pkg.dev/<cluster-project>/grain/guest) into which off-cluster deployments mirror the build-stamped sandbox guest image on upgrade and startup, so a cluster in another GCP organization can pull the commit-matched guest from its own registry. This host keeps only the current and previous mirrored tags; the registry is not pruned -- give it a cleanup policy (docs/sandbox-image-mirror.md). |
| `-sandbox-memory-mb` | value |  |  | deployment-wide default guest memory, in MiB, for a granule-managed sandbox VM, passed as granule vm create's own -memory-mb (only used with -granule-sandboxes); 0 uses grain's own default, 8192. Overridable per task from the UI/API (model.Task.SandboxMemoryMB) (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-sandbox-memory-overcommit` | float | `1` |  | the same ratio for memory, and 1 by default rather than 2: the way a host finds memory it has already promised is to swap, and a deployment that swaps has lost more throughput than the extra sandbox was worth. 0 leaves memory bounded by the run counts alone (model.Budget) (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-sandbox-memory-reserve-mb` | int | `-1` |  | memory kept back from that budget for everything on this host that is not a sandbox -- this daemon, the container runtime, each VM's own qemu overhead and the page cache they all read through. Taken off the top, before the overcommit ratio, so raising the ratio never eats into it. -1 takes an eighth of the machine, never less than 8 GiB and never more than half of it (model.DefaultReserveMemoryMB) (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig) |
| `-sandbox-port-base` | int | `9728` |  | first port of the block every sandbox-facing service binds in: the git proxy, the model proxies, the package cache and the Actions cache each take a reserved offset within it (sandboxnet.go's own sandboxPortOffsets). It is one aligned range of 256 ports, so what has to reach these services -- a firewall rule, a security group, a VPN ACL -- is one rule that a seventh service does not change. Must be a multiple of 256, and should stay below the ephemeral range this host allocates from (/proc/sys/net/ipv4/ip_local_port_range, 32768 by default) so the block cannot collide with this daemon's own outbound connections. 0 restores the behaviour before the block existed: every service on whatever ephemeral port the kernel gives it, renumbered on every restart -- which is why a sandbox that outlives a restart holds addresses that have moved. Not used by the package cache's interception listeners or its resolver, whose ports (443, 80, 53) are fixed by their protocols. |
| `-secrets-backend` | string | `"file"` | $GRAIN_SECRETS_BACKEND | secret store backend ("file" or "secretmanager"); defaults to $GRAIN_SECRETS_BACKEND, then "file" |
| `-secrets-sm-prefix` | string |  | $GRAIN_SECRETS_SM_PREFIX | Secret Manager resource prefix (projects/<project>/secrets/<id-prefix>) when -secrets-backend=secretmanager; defaults to $GRAIN_SECRETS_SM_PREFIX |
| `-shutdown-cmd` | value |  |  | one argument of the command the UI's shut-down-host button runs -- repeat for every argument. Defaults to sudo systemctl poweroff. |
| `-tunnel-gateway-url` | string |  |  | removed: the tunnel gateway is gone. The daemon refuses to start with this set |
| `-tunnel-token` | string |  |  | removed: the tunnel gateway is gone. The daemon refuses to start with this set |
| `-ui-access-session-lifetime` | duration | `2160h0m0s` |  | on this single-tenant daemon, how long a browser stays signed in after entering the access code without using the UI; each use renews it. Rotating the code (grain access-code rotate) ends every session at once |
| `-ui-addr` | string | `"0.0.0.0:4242"` |  | address to serve the UI/API on, in-process, over this same store -- empty disables it |
| `-ui-allowed-hosts` | string |  |  | comma-separated host names the UI/API answers to besides IP literals, localhost and this machine's own hostname ("grain.example.com", "*.example.com"); a request with any other Host header is refused with 421, which is what stops a DNS-rebinding page reaching a loopback-bound API (grain#2380). Checked on every request when this is set or -ui-addr names a specific address; on an every-interface -ui-addr with this unset, on loopback requests only. "*" turns the check off |
| `-ui-allowed-principals` | string |  |  | deprecated: imported once on first start when the database allowlist is empty (user:<email> and group:<email> only; serviceAccount: and domain: entries are logged and dropped), then ignored. Manage allowed users and groups in Settings -> Access or with 'grain access' |
| `-ui-cloud-identity-endpoint` | string |  | $GRAIN_CLOUD_IDENTITY_ENDPOINT | resolve allowed group: entries against this Cloud Identity API base URL (a test stand-in) rather than https://cloudidentity.googleapis.com; defaults to $GRAIN_CLOUD_IDENTITY_ENDPOINT |
| `-ui-hub-audience` | string |  | $GRAIN_HUB_AUDIENCE | the single audience URL a signed X-Grain-Hub-Assertion must carry (e.g. "https://<name>.<instance-domain>"); defaults to $GRAIN_HUB_AUDIENCE |
| `-ui-hub-issuer` | string |  | $GRAIN_HUB_ISSUER | the exact issuer URL a signed X-Grain-Hub-Assertion must carry for the UI/API to accept the identity in it (e.g. "https://grain.example.com"); defaults to $GRAIN_HUB_ISSUER. All three of -ui-hub-issuer, -ui-hub-audience, and -ui-hub-keys-url must be set to enable the "registry" IdentityVerifier |
| `-ui-hub-keys-url` | string |  | $GRAIN_HUB_KEYS_URL | where the hub's ES256 signing keys are published as a JWK set (e.g. "https://grain.example.com/.well-known/jwks.json"); defaults to $GRAIN_HUB_KEYS_URL |
| `-ui-iap-audience` | string |  | $GRAIN_IAP_AUDIENCE | the audience a signed X-Goog-IAP-JWT-Assertion must carry for the UI/API to accept the identity in it, or several separated by commas -- IAP's /projects/NUMBER/global/backendServices/ID or /projects/NUMBER/apps/PROJECT, or what a BeyondCorp tunnel's assertion names (the proxy's own docs); defaults to $GRAIN_IAP_AUDIENCE. With none, no assertion is accepted |
| `-ui-iap-issuer` | string |  | $GRAIN_IAP_ISSUER | the issuer a signed assertion must carry; defaults to $GRAIN_IAP_ISSUER, then https://cloud.google.com/iap |
| `-ui-iap-keys-url` | string |  | $GRAIN_IAP_KEYS_URL | where the signing keys for those assertions are published (IAP's PEM map or a JWK set); defaults to $GRAIN_IAP_KEYS_URL, then https://www.gstatic.com/iap/verify/public_key |
| `-ui-open` | bool |  |  | open the UI in the system's default browser once it's listening |
| `-ui-registry-audience` | value |  |  | deprecated: renamed to -ui-hub-audience |
| `-ui-registry-issuer` | value |  |  | deprecated: renamed to -ui-hub-issuer |
| `-ui-registry-keys-url` | value |  |  | deprecated: renamed to -ui-hub-keys-url |
| `-ui-require-iap` | bool |  | $GRAIN_IAP_AUDIENCE, $GRAIN_REQUIRE_IAP | accept the access code (bearer or grain_access cookie) only from loopback with no forwarding or proxy headers, and serve no "Enter access code" page, so network callers sign in through IAP alone (docs/access-code.md); needs -ui-iap-audience or $GRAIN_IAP_AUDIENCE. Defaults to $GRAIN_REQUIRE_IAP |
| `-ui-require-verifier` | string |  | $GRAIN_REQUIRE_VERIFIER | require network callers to sign in through the named IdentityVerifier (e.g. "iap", "registry", or a registered verifier): accept the access code (bearer or grain_access cookie) only from loopback with no forwarding or proxy headers (or through a verifier that declares AccessCodeWins), and serve no "Enter access code" page (-ui-require-iap is an alias for -ui-require-verifier=iap). Defaults to $GRAIN_REQUIRE_VERIFIER |
| `-upgrade-allowed-branches` | string | `"main,candidate,rc,latest,v[0-9]*.[0-9]*.[0-9]*"` |  | comma-separated branches the UI's Upgrade button, a scheduled upgrade or an agent may upgrade this deployment to, each a path.Match pattern ('*' never crosses a '/'; e.g. main,release/*), or ** for any branch at all; any other branch is refused. The default is grain's own release names -- main, candidate, latest and vX.Y.Z release tags -- which only grain's merge queue and release cut move; a task or feature branch needs listing here |
| `-upgrade-drain-window` | duration |  |  | maximum time a scheduled upgrade waits for active tasks and CI runs to drain before proceeding with the upgrade (0 waits indefinitely for runs to complete normally) |
| `-upgrade-grace-window` | duration | `10s` | $GRAIN_UPGRADE_GRACE_WINDOW | grace window between confirming a manual upgrade and starting its work, during which the Upgrade pane shows a countdown with Cancel (0 disables the countdown; $GRAIN_UPGRADE_GRACE_WINDOW when unset) |
| `-upgrade-image` | string |  |  | image repository, with no tag, that CI publishes a tag per branch to (e.g. docker.io/grainai/grain); set, the UI's Upgrade button pulls a branch's image instead of building one, and -upgrade-image-ref-file is required |
| `-upgrade-image-ref-file` | string |  |  | file a successful -upgrade-image upgrade rewrites with the image the service should run, as one GRAIN_IMAGE=<ref> line for grain-daemon.service to read as an EnvironmentFile (required with -upgrade-image) |
| `-upgrade-install-path` | string |  |  | where a successful upgrade's binary is installed to (required with -upgrade-src-dir) |
| `-upgrade-restart-cmd` | value |  |  | one argument of the command run at the end of a successful upgrade to bring the new build up -- repeat for every argument, e.g. -upgrade-restart-cmd=sudo -upgrade-restart-cmd=systemctl -upgrade-restart-cmd=restart -upgrade-restart-cmd=grain-daemon.service. Omitted entirely, the upgrade still happens but nothing brings the new build up on its own. Used by both upgrade paths below. |
| `-upgrade-reviewed-merge-logins` | string |  |  | comma-separated forge logins the -upgrade-reviewed-merge-repo repository's grain merges as; a merge by one of them counts as reviewed |
| `-upgrade-reviewed-merge-repo` | string |  |  | owner/name (github.com) or host/owner/name (a GitHub Enterprise Server, asked at https://<host>/api/v3) of the repository -upgrade-image is built from: refuse an upgrade whose image's commit did not reach its main or release/v* through a reviewed merge -- one grain made (-upgrade-reviewed-merge-logins) or one the forge's review record covers -- with every first-parent commit since the running build's. Needs -upgrade-verify-cmd. Omitted, nothing is asked |
| `-upgrade-reviewed-merge-token-file` | string |  |  | file holding a read token for the forge API -upgrade-reviewed-merge-repo is asked through (omitted, it is read through the host's own forge tool when a command-backed ambient source claims its host, and anonymously otherwise) |
| `-upgrade-src-dir` | string |  |  | git checkout of bwsalmon/grain to fetch/build from when the UI's Upgrade button is used; empty disables the feature |
| `-upgrade-verify-cmd` | value |  |  | one argument of the command that verifies an -upgrade-image image's signature before anything runs it -- repeat for every argument, e.g. -upgrade-verify-cmd=cosign -upgrade-verify-cmd=verify -upgrade-verify-cmd=--key=/etc/grain/cosign.pub -upgrade-verify-cmd=--insecure-ignore-tlog=true (docs/image-signing.md). The image is appended (by digest on a host, by tag in a cluster, where the command must print cosign's JSON); a non-zero exit refuses the upgrade. Omitted, upgrades go ahead unverified and say so. |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/daemon_help_test.go`: `TestDaemonHelpUnchanged`; `pkg/cli/daemon_*_test.go`.

### 5.2 `grain demo [flags]` {#CLI-44}

Serves the task UI on localhost over a throwaway store of fake tasks (SQLite by default, or PostgreSQL under `-db-driver=postgres`, using `-db-dsn` when given or an embedded server in the throwaway directory). Every caller signs in with the demo's access code, kept in `-access-code-file` when given.

<!-- BEGIN GENERATED FLAGS: grain demo -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-access-code-file` | string |  |  | keep the demo's access code, which every caller, loopback included, signs in with, in this file -- written there if it is not already -- rather than beside its throwaway store, where it is logged |
| `-addr` | string | `"127.0.0.1:4290"` |  | address to serve the demo UI on |
| `-agent-paused` | bool |  |  | serve a deployment whose agent dispatch is paused on a provider's usage limit |
| `-as` | string |  |  | principal to attribute the fake tasks to (defaults to the OS user) |
| `-base-branch-ci` | bool |  |  | with -github-sim and -fake-ci, let POST /api/e2e/base-branch/{red,green} {"branch": ...} (loopback only) turn a branch red or green on a -fake-ci verdict, through grain's own base-branch reconciler and dispatch's linking step, dispatching nothing |
| `-config-change` | bool |  |  | serve a real, throwaway grain/config exported from the demo's store, and seed two tasks parked on proposal branches of it (propose_config_change), so the config-change card's Approve & Apply merges into its main and Reject deletes the branch |
| `-db-driver` | string | `"sqlite"` |  | database backend driver for the throwaway store ("sqlite" or "postgres") |
| `-db-dsn` | string |  |  | PostgreSQL connection URL under -db-driver=postgres; empty starts an embedded server in the throwaway directory |
| `-default-target-repo` | string | `"acme/widgets"` |  | owner/name the fake tasks target |
| `-disable-self-upgrade` | bool |  |  | as on grain daemon: disable in-VM self-upgrade actions and hide in-VM upgrade controls in the UI |
| `-external-config-branch` | string | `"main"` |  | with -external-config-repo, the branch of that repository configuration is read from |
| `-external-config-repo` | string |  |  | serve a deployment whose configuration is managed in this external config repository (a remote URL): every configuration write is refused with 409 and the exact edit to commit there instead (docs/external-config-repo.md) |
| `-fake-ci` | bool |  |  | wire a grain CI stub that accepts and reports runs but never executes anything |
| `-fork-pull-request` | bool |  |  | with -github-sim, also seed two pull requests from the fork contrib/<repo>: contrib:fix/readme-typo (#41, open) and contrib:feat/old-idea (#42, closed), each head at refs/pull/N/head, for filing a task against a fork head label |
| `-git-proxy` | bool |  |  | with -github-sim, serve grain's git proxy in front of the simulated repo on loopback, and POST /api/e2e/git-proxy/runs to start a task's run and mint its sandbox a token to push with |
| `-github-api` | string |  |  | host:port of a GitHub API stub on loopback, spoken to over plain HTTP: the Settings → Git Test buttons, the New task pickers and filing's reach refusal all ask it what the deployment's credential can reach |
| `-github-sim` | bool |  |  | wire a simulated GitHub whose -default-target-repo has branches main and release/1.0 |
| `-github-sim-public` | bool |  |  | with -git-proxy, let the simulated GitHub serve its repo to an anonymous fetch, as GitHub does a public repository; an anonymous push is still refused |
| `-github-webhook-addr` | string |  |  | local address (e.g. :4244) to listen on directly for signed GitHub App webhooks using GITHUB_WEBHOOK_SECRET or a GitHub App credential's webhook_secret; mutually exclusive with -github-webhook-url |
| `-github-webhook-url` | string |  |  | base URL of this deployment's github-webhook gateway service (e.g. http://grain-webhook:8080), signed with GITHUB_WEBHOOK_RELAY_SECRET; empty disables remote webhook long-polling |
| `-host-executor` | string |  |  | resolve Settings' run locations (Models and Capability servers) as this deployment would: empty for a development daemon on the bare machine, "kubernetes" (no host executor), "none" (a container with no host agent), "hostd-down" (a container whose host agent is configured but not answering), "hostd" (a container with a real host agent on a throwaway allowlist offering agy and corp-search), "hostd-remote" (a Kubernetes daemon with a real host agent dialed in) or "hostd-remote-down" (none dialed in) |
| `-host-mode-install` | bool |  |  | show the notice an unmigrated host-mode install shows (banner, System -> Health, Upgrade), with the CLIs the default bare-machine run locations leave nowhere to run |
| `-human-review` | bool |  |  | seed a change a review held for a person's decision (request_human_review), waiting for Submit or Release hold |
| `-management-url` | string |  |  | as on grain daemon: optional external http:// or https:// URL shown where upgrades are managed externally |
| `-mechanical-port` | bool |  |  | seed a change merged onto main and the port grain cherry-picked to candidate itself, with no agent run |
| `-merge-queue-states` | bool |  |  | also seed two pull requests the merge queue is waiting on: one for its base branch to go green, one for an approval GitHub requires |
| `-merge-rules` | bool |  |  | seed acme/widgets' merge rules (the docs/declarations.md example) and two deploy/** changes the merge queue classified under them |
| `-model-catalog` | string |  |  | host:port of an Anthropic model listing stub (GET /v1/models) on loopback: Claude connections that advertise no models are given what it lists, as a deployment's are given what Anthropic lists |
| `-model-discovery-interval` | duration | `the deployment's 10m` |  | how often -model-catalog's discovery asks the provider again |
| `-notifications` | bool |  |  | run the reconcile loop's notification sweep every -reconcile interval (default 2s), so an open notification whose clears_when (task, base-green or ttl) has come true is cleared on its own, as on a daemon's cycle |
| `-observability` | bool |  |  | serve the live CI and sandbox observability demo |
| `-open` | bool | `true` |  | open the UI in the system's default browser once it's listening |
| `-qualification` | bool |  |  | seed a release candidate of -default-target-repo whose qualification run is waiting for approval |
| `-reconcile` | duration |  |  | run the reconcile loop's suite step every interval, and its release and issue-import steps against -github-sim's simulated GitHub too, logging a failed cycle as the daemon does (default off; agents still never run) |
| `-review-questions` | bool |  |  | seed a change whose review left questions for its owner, held on their decision, and send it back to the queue once they reply, as a daemon would |
| `-scheduled-workflows` | bool |  |  | with -github-sim and -fake-ci, add a candidate branch to the simulated repo, read its .grain declarations and start and settle its workflow schedules' runs every -reconcile interval (default 2s), and let POST /api/e2e/fake-ci/runs/{id}/finish end a run |
| `-schedules` | bool | `2s), so a schedule that comes due fires, or records why it was skipped, on its own (what it files is still never dispatched` |  | run the reconcile loop's schedule step every -reconcile interval |
| `-secrets-store` | bool |  |  | wire a real secrets store in the demo's throwaway directory, so secrets and release secrets can be set and removed |
| `-sensitive-hold` | bool |  |  | seed a change held by a sensitive path trigger, and a failed trigger declaration read |
| `-staging` | bool |  |  | run two fake persistent sandboxes, one on a /staging/ sub-path and one on a host port, for the rail's Staging group |
| `-stalled-tasks` | bool |  |  | hand the UI the orchestrator's stall deadlines and seed a queued task, a run and a deferral past them, for the Inbox's stuck-task group |
| `-ui-allowed-principals` | string |  |  | deprecated: imported once on first start when the database allowlist is empty (user:<email> and group:<email> only; serviceAccount: and domain: entries are logged and dropped), then ignored. Manage allowed users and groups in Settings -> Access or with 'grain access' |
| `-ui-cloud-identity-endpoint` | string |  |  | as on grain daemon: resolve allowed group: entries against this Cloud Identity API base URL (a test stand-in) rather than https://cloudidentity.googleapis.com |
| `-ui-hub-audience` | string |  |  | as on grain daemon: the single audience URL a signed X-Grain-Hub-Assertion must carry |
| `-ui-hub-issuer` | string |  |  | as on grain daemon: the exact issuer URL a signed X-Grain-Hub-Assertion must carry |
| `-ui-hub-keys-url` | string |  |  | as on grain daemon: where the hub's ES256 signing keys are published as a JWK set |
| `-ui-iap-audience` | string |  |  | as on grain daemon: the audience a signed X-Goog-IAP-JWT-Assertion must carry for the demo to sign its caller in from it |
| `-ui-iap-issuer` | string |  |  | as on grain daemon: the issuer a signed assertion must carry (default https://cloud.google.com/iap) |
| `-ui-iap-keys-url` | string |  |  | as on grain daemon: where the assertions' signing keys are published (default https://www.gstatic.com/iap/verify/public_key) |
| `-ui-registry-audience` | value |  |  | deprecated: renamed to -ui-hub-audience |
| `-ui-registry-issuer` | value |  |  | deprecated: renamed to -ui-hub-issuer |
| `-ui-registry-keys-url` | value |  |  | deprecated: renamed to -ui-hub-keys-url |
| `-ui-require-iap` | bool |  |  | as on grain daemon: accept the access code only from loopback with no forwarding headers; needs -ui-iap-audience |
| `-ui-require-verifier` | string |  |  | as on grain daemon: require network callers to sign in through the named IdentityVerifier (-ui-require-iap is an alias for -ui-require-verifier=iap) |
| `-unconfigured` | bool |  |  | serve a deployment with no agent model and no git credential configured, as a fresh install has |
| `-upgrade-allowed-branches` | string | `"main,candidate,rc,latest,v[0-9]*.[0-9]*.[0-9]*"` |  | comma-separated branches (path.Match patterns, or ** for any) -upgrade-image may upgrade to |
| `-upgrade-image` | string |  |  | wire a real image-mode upgrader pulling from this repository (localhost:5000/x/grain): the Upgrade tab lists its tags and an upgrade really pulls, verifies and health-checks the image, but never restarts anything |
| `-upgrade-verify-cmd` | value |  |  | one argument of the command that verifies an -upgrade-image image's signature, repeated for every argument, as on grain daemon |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/demo_flags_test.go`: `TestParseDemoFlagsExternalConfigRepo`, `TestParseDemoFlagsGitHubWebhooks`.

### 5.3 `grain mcpserver [flags]` {#CLI-45}

Speaks MCP over stdin/stdout against the sandbox tools. A running daemon forks it, re-invoking its own binary, for each agent run.

<!-- BEGIN GENERATED FLAGS: grain mcpserver -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-addenda-after` | int |  |  | the id of the last comment on this run's task already quoted in the run's prompt (orchestrator.commentThreadSection), passed on by the Framework that forked this process (agent.AddendaArgs). With -server/-task, anything a person posts on that task after it rides back to the run appended to its next tool result, so a correction typed while the run is in flight is read within the turn rather than at the task's next dispatch. Left at zero on a task whose prompt quotes no conversation, which is every first dispatch. |
| `-container` | string |  |  | name of a local Docker sandbox container to run tools in |
| `-data-dir` | string |  |  | grain's own data directory on this controller, holding secrets/github -- the credential pull_request_status reads GitHub with. Required with -pr-repo. |
| `-exec-key` | value |  |  | path, *inside -granule-vm's container*, of the private key granule exec authenticates to the guest with. Optional: left unset, 'granule exec' uses the keypair 'granule run' generates for that guest at boot, which is what a stock bwsalmon/granule guest image authorizes (see its internal/guestkey). Set it only for a custom guest image that authorizes a key of its own instead. |
| `-github-host` | string | `"github.com"` |  | git host whose REST API pull_request_status reads (github.APIHost maps it to the API host) |
| `-github-insecure-http` | bool |  |  | reach -github-host over plain HTTP instead of HTTPS -- for a local mock GitHub in a live test, never for a real deployment |
| `-grain-secrets-dir` | string |  |  | directory holding this deployment's own secrets (cmd/grain's secretsDir: -data-dir/secrets), read on this host so that read_grain_log can redact those values out of the journal before returning any of it. Only used with -grant https://grain.dev/capabilities/grain#journal, and never served to a run -- what it buys is the ability to recognize a credential in a log line and remove it. Unset, or unreadable, read_grain_log refuses every call rather than answering with an unredacted journal. |
| `-grain-src-dir` | string |  |  | directory holding grain's own source, read-only (pkg/cli/daemon.go's sourceDir: the copy baked into the deployment image, or -upgrade-src-dir's checkout). |
| `-grain-state-dir` | string |  |  | controller-local synchronized deployment state repository; only used with -grant https://grain.dev/capabilities/grain#health |
| `-grant` | value |  |  | name of a capability grant this run's task holds, whose tools this server should serve; repeat the flag for each. "https://grain.dev/capabilities/api#self-debug" (the tasks:read and deployment:read scopes; the retired "self-debug" and "api:self-debug" read as it) serves the four task readers, read_pipeline_changes and read_grain_metrics over -server; "https://grain.dev/capabilities/grain#journal" (formerly "grain-log") serves read_grain_log over this host's journal, redacted with -grain-secrets-dir. Passed by a Framework from the grants on the run's own task (agent.RunConfig.Grants, agent.GrantArgs). |
| `-granule-vm` | string |  |  | name of a granule-managed VM (bwsalmon/granule) to run the sandbox tools against over SSH, instead of a local directory (mutually exclusive with -sandbox-root) |
| `-interactive` | bool |  |  | this run's task is a live chat somebody is sitting with (model.Task.Interactive, passed on by the Framework that forked this process -- agent.InteractiveArgs). With -server/-task it adds the chat tool: the run says something in the task's own conversation and blocks until a person answers, inside one tool call. It is gated because the tool promises somebody is there: on a background run every call would stand still for an hour before finding out nobody was. |
| `-k8s-granule` | value |  |  | reach -k8s-pod's guest VM through granule exec -- over 'kubectl exec' |
| `-k8s-kubeconfig` | string |  |  | kubeconfig path passed to kubectl when reaching -k8s-pod |
| `-k8s-namespace` | string |  |  | namespace of -k8s-pod; empty is kubectl's own default |
| `-k8s-pod` | value |  |  | name of a Kubernetes sandbox pod to run tools in, over kubectl exec (with -k8s-namespace and -workspace) |
| `-mcp-surface` | value |  |  | which tools to serve: "control" (the sandbox tools, the run-ending and escalation tools and the model-only grants' tools; everything else is a grain CLI command inside the sandbox) or "full" (every tool, for a client that cannot shell out and for tests). Empty is control when a sandbox flag is set, full otherwise (design/design/sandbox-first-agent-surface.design.md §6) |
| `-pr-base` | string |  |  | branch -pr-branch merges into, which base_branch_status reads CI for so a run can tell a failure its own change caused from one it inherited (model.Task's own Base). Unset -- what an ordinary task has -- that tool reads -pr-repo's default branch instead, which is the base such a task really has. |
| `-pr-base-incident` | string |  |  | notice of an active incident on the base branch, if any |
| `-pr-branch` | string |  |  | branch within -pr-repo pull_request_status and wait_for_checks report on -- this run's own branch, and the only one they can ever read (required with -pr-repo) |
| `-pr-credential` | string |  |  | name of the credential in -data-dir's secrets/github that this run's branch is pushed with (its team's git connection for -pr-repo, or its own github-credential grant), which pull_request_status, base_branch_status, the issue tools and query_merge_queue then read GitHub as. Unset, they read with whatever the deployment's owner/repo ladder (credentials.json) names, which on a deployment whose credentials are all team connections is no credential at all. |
| `-pr-ignore-github-ci` | bool |  |  | say that -pr-repo has declared GitHub's check runs are not a source of verdicts for it -- grain CI (.grain/workflows) is its gate (model.RepoConfig.IgnoreGitHubCI). pull_request_status and base_branch_status then say so instead of listing checks the merge queue is not reading and does not wait on. |
| `-pr-number` | int |  |  | daemon-pinned external pull request number within -pr-repo (from model.LinkExternalPR) that pull_request_status, reply_to_pull_request_comment and resolve_review_thread scope to when -pr-branch has no open pull request of its own |
| `-pr-repair` | bool |  |  | mark this run as the merge queue's repair of -pr-branch, which changes what base_branch_status tells it about breakage it inherited: a repair has to fix a failing check whether or not its own commits caused it, where every other run is told to leave the base's own breakage alone (orchestrator.mergeRepairRun). |
| `-pr-repo` | string |  |  | owner/name of the repository pull_request_status and wait_for_checks report on. Unset leaves both tools registered but answering that this run has no repo, which is what a task with no target really does have. |
| `-pr-upstream-repo` | string |  |  | owner/name of the upstream repository that pull requests for -pr-repo are opened and tracked against (model.RepoConfig.UpstreamRepo) |
| `-propose-repo` | value |  |  | owner/name of a repository this deployment works in, which a propose_task call may name in its repo argument; repeat the flag for each, most recently worked in first. They are listed in that argument's own description, so a run can see which repositories are on offer while it is deciding what to write there. Passed by the Framework that forked this process from the list the daemon read out of its store (model.Store.WorkRepos, agent.RunConfig.ProposeRepos). Unset, the description names the rule without the list, exactly as it read before this flag existed. |
| `-recorded-tools` | string |  |  | path to append full tool call records (JSON lines) as tools run |
| `-run-deadline` | string |  |  | RFC3339 time at which grain cancels the run this server serves -- the deadline on the context orchestrator.RunDispatch gives framework.Run, passed on by the Framework that forked this process (agent.RunDeadlineArgs). Set, every tool result carries how much of it is left once the run is inside mcp.RunDeadlineNoticeWindow of it; unset, results read exactly as they otherwise would. |
| `-sandbox-env` | value |  |  | NAME=VALUE, repeatable, only with -sandbox-root: an environment variable run_command sets on top of its allowlist (mcp.NewSandboxTools). Passed by the Framework that forked this process from the daemon's -host-sandbox-env (agent.RunConfig.SandboxEnv); HOME is refused, since it stays pinned to the sandbox root. Its values sit on this process's argv, readable by every user on the host, so a Framework passes -sandbox-env-file instead; this is for running the server by hand |
| `-sandbox-env-file` | string |  |  | path of a 0600 file of NUL-terminated NAME=VALUE entries (mcp.WriteSandboxEnvFile), only with -sandbox-root: what -sandbox-env sets, without the values on this process's argv. Passed by the Framework that forked this process (agent.RunConfig.SandboxEnvFile), naming the file the daemon wrote its -host-sandbox-env to |
| `-sandbox-hide` | value |  |  | absolute path, repeatable, only with -sandbox-root: a file or directory run_command runs every command with hidden, under bubblewrap, refusing to run one where it cannot (mcp.NewHostSandboxTools). Passed by the Framework that forked this process (agent.RunConfig.SandboxHide): a single-tenant daemon's data directory, which holds its access code |
| `-sandbox-root` | string |  |  | directory the local sandbox tools are confined to (mutually exclusive with -granule-vm) |
| `-server` | string |  |  | base URL of the "grain daemon" this server's run was dispatched by, e.g. http://127.0.0.1:4242 -- with -task, adds the open_pull_request, recreate_sandbox and update_status tools, each of which asks that daemon to act on the run's own task rather than acting from here |
| `-ssh-user` | string |  |  | username to SSH into -granule-vm as (required with -granule-vm) |
| `-task` | string |  |  | id of the task this server's run belongs to (required with -server) |
| `-tool-budget` | bool |  |  | count this run's tool calls and result bytes against a durable judgement task's per-wake budget (mcp.ToolBudgetSoftCalls and the rest, design/design/durable-tasks.design.md#DT-13), passed on by the Framework that forked this process (agent.GrantArgs, for agent.RunConfig.ToolBudget). Unset, nothing is counted. |
| `-tool-budget-pipeline-cursor` | bool |  |  | with -tool-budget: this run is a pipeline steward wake, whose handover the budget's notice asks to carry the pipeline-changes cursor (agent.RunConfig.ToolBudgetPipelineCursor) |
| `-workspace` | string |  |  | working directory run_command/read_file/edit_file/write_file operate in on -granule-vm (required with -granule-vm) |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/testmain_test.go` (forked-subcommand handling); flags *untested* here.

### 5.4 `grain runhost exec -spool DIR [-deadline RFC3339]` {#CLI-46}

A run host's entrypoint: run one spooled agent CLI invocation. The usage is hand-written: `-spool` is the spool directory, `-deadline` the RFC 3339 hard ceiling. A bare `grain runhost` prints `usage: grain runhost exec -spool DIR [-deadline RFC3339]`.

<!-- BEGIN GENERATED FLAGS: grain runhost -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain runhost exec -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-deadline` | string |  |  | the hard ceiling this run host enforces on itself (RFC 3339) |
| `-spool` | string |  |  | the run host's spool directory |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/runhost` tests.

### 5.5 `grain host [flags]` {#CLI-47}

On the bare machine, `grain host` runs the host agent: the allowlisted host CLIs and capability servers a containerised daemon asks for. The allowlist (`-allowlist`, default `/etc/grain/hostd.yaml`) must be root-owned and not group- or world-writable.

`grain hostd` is a hidden alias for it, kept for installed units and scripts. `grain host` followed by a registry verb (`list`, `register`, `install`, `set-owner`) is §4.7 instead.

<!-- BEGIN GENERATED FLAGS: grain host -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-allowlist` | string | `"/etc/grain/hostd.yaml"` |  | the allowlist of what this host will run; must be root-owned and not group- or world-writable |
| `-build` | bool |  |  | print this binary's build and exit: what a host agent asks of the version its current link names |
| `-ca-file` | string |  |  | with -server: PEM file of the certificate authority to verify the daemon's certificate against. Implies -tls |
| `-check` | bool |  |  | check the allowlist, print what it offers, and exit |
| `-command` | string | `"/usr/local/bin/grain"` |  | with -ensure-builtins: the grain binary on the host the added entries run |
| `-disconnect-grace` | duration | `10s` |  | how long a process may outlive the daemon connection that started it |
| `-drain` | bool |  |  | ask the host agent serving -socket to drain for an upgrade, and exit: it refuses new starts, waits for what it runs (up to the allowlist's drain_cap), then exits for systemd to restart it on the new binary. A host agent speaking this build's protocol with handover re-execs onto the new binary in place while keeping live detached runs up; one without handover cuts over at the first moment nothing runs, draining only if no such moment comes within drain_cap. SIGUSR1 to the host agent drains |
| `-ensure-builtins` | bool |  |  | add to -allowlist the capabilities entries grain's own servers need in this build (host-git, grain-gcp, ... and the edition's, registered by the build's edition) that it has no entry of that name for, print each one added, and exit. An entry already there is never changed, only reported when it differs from the default. Run as root on the host, by grain-hostd.service before each start and by the upgrade step; the host agent re-reads the allowlist on every request, so nothing needs restarting |
| `-iap-audience` | string |  |  | with -server: the OAuth client ID of the GCP Identity-Aware Proxy in front of the daemon, to send its OIDC token |
| `-join` | string |  | $GRAIN_HOST_JOIN_CODE | with -server: enroll this host using an enrollment code from Settings -> Hosts -> Invite hosts ($GRAIN_HOST_JOIN_CODE), generating -key-file and waiting for the deployment owner to accept it |
| `-join-only` | bool |  |  | with -join or -rekey: exit 0 once accepted and the credential is written to -token-file, without starting the server |
| `-key-file` | string |  |  | with -server: the file holding this host's Ed25519 private key (mode 0600), generated by -join or -rekey |
| `-name` | string |  |  | with -server: the registry name this host expects to be (/api/hostds). Optional: the daemon names it from its token, and refuses the connection when this says otherwise |
| `-rekey` | string |  |  | with -server: migrate an existing per-host token for the named host to a key-bound credential (-key-file), accepted by the deployment owner |
| `-server` | string |  |  | the URL of a grain daemon this host cannot share a socket with (https://grain.example.com, http://127.0.0.1:4242): the host agent keeps a connection open to its /api/hostd/connect and serves the daemon over it, as well as listening on -socket (set -socket= for no local listener) |
| `-socket` | string | `"/run/grain-hostd/hostd.sock"` |  | the unix socket to listen on (mode 0600); bind mount it into the daemon's container at the same path |
| `-state-dir` | string | `"<tmp>/home/.local/state/grain-hostd"` |  | where the host agent records the process groups it started, so a restarted host agent reaps what the last one left |
| `-status` | bool |  |  | ask the host agent serving -socket whether it answers and what it offers, print that on one line, then one line per process it holds (a detached run's id, run, task and pid), and exit: 1 when it does not answer. scripts/setup.sh's readiness report runs it |
| `-tls` | bool |  |  | with -server: dial over TLS even for an http:// URL (an https:// one always is) |
| `-tls-server-name` | string |  |  | with -server: server name to verify the daemon's certificate against when it differs from the -server host. Implies -tls |
| `-token-file` | string |  | $GRAIN_HOSTD_TOKEN, $GRAIN_TOKEN | with -server: the file holding this host's credential -- issued on acceptance or when registered in Settings -> Hosts (or the daemon's deprecated shared -hostd-token-file); $GRAIN_HOSTD_TOKEN or $GRAIN_TOKEN when unset |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/hostd` tests; [`authentication.spec.md` §8](../authentication/authentication.spec.md).

### 5.6 `grain host-script` {#CLI-48}

In a sandbox, `host-script` runs a granted host script on grain's side (`pkg/hostscriptcli`):
- `grain host-script <name> [args…]` runs it without forwarding standard input;
- `grain host-script --stdin <name> [args…]` runs it and forwards standard input;
- `<name> --grain-help` or `--help <name>` prints the script's description and usage;
- `--list` (also `-list`, `--grain-list`) lists the scripts granted to the sandbox.

<!-- BEGIN GENERATED FLAGS: grain host-script -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/hostscriptcli` tests.

### 5.7 `grain capability <server> [flags]` {#CLI-49}

When `<server>` names an in-tree capability server (`intree.Lookup`), this drops Linux capabilities and serves it over stdin/stdout.
`servesCapability` takes the mode only when the first argument already names an in-tree server. So from the command line, `grain capability` with no arguments, or with an unknown first argument, is the task command of [#CLI-26](#CLI-26) and fails with `grain: usage: grain capability <id> <capability-id> attach|detach`. `serveCapability`'s own `usage: grain capability <a|b|…> [flags]` and `no in-tree capability "<x>"; there are: …` errors cannot be reached through `cmd/grain`.

The server's flags are its own package's, outside this spec ([#CLI-14](cli.spec.md#CLI-14)).

**Pinned by:** `pkg/cli/capability_serve_test.go`.

### 5.8 Hooks a run forks {#CLI-50}

These read one request on stdin and answer on stdout. Their arguments are ignored (with a warning), so the guard does not run them:
- `agy-tool-hook` answers agy's PreToolUse hooks;
- `claude-stop-hook` answers Claude Code's Stop hooks;
- `codex-detached` is the detached codex runner.

<!-- BEGIN GENERATED FLAGS: grain agy-tool-hook -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain claude-stop-hook -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain codex-detached -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/agyhook_test.go`; `pkg/agent/claude` stop-hook tests.

---

## 6. Local administration modes

These edit a colocated deployment's files on disk, or the host itself, rather than calling the API. The exceptions are `sync` and `init`, which also talk to a daemon. `-data-dir` resolution is [#CLI-5](cli.spec.md#CLI-5).

### 6.1 `grain access-code [-data-dir DIR] [show|rotate]` {#CLI-41}

- `show` (the default) prints the access code: the daemon's one credential, also used as the CLI's bearer token.
- `rotate` replaces the code, prints the new one, and ends every UI session and bearer use of the old one within a second. It refuses when there is no existing installation.

The code is read from `DIR/local-api-token`, else `$GRAIN_LOCAL_API_TOKEN_FILE`, else `local-api-token` in `$GRAIN_DATA_DIR` or cli.env's. Flags (hand-written usage): `-data-dir` (string).

<!-- BEGIN GENERATED FLAGS: grain access-code -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain access-code show -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain access-code rotate -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/accesscode_test.go`: `TestAccessCodeShowsAndRotatesTheCode`, `TestAccessCodeRotateNeedsAnExistingInstallation`.

### 6.2 `grain secrets -data-dir DIR <command>` {#CLI-51}

This edits `<data-dir>/secrets` on disk. The usage is hand-written. Flags:

| Command | Flags |
|---|---|
| `secrets` | `-data-dir` (string; default `$GRAIN_DATA_DIR`; required for `file` backend), `-secrets-backend` (string; `"file"` or `"secretmanager"`, default `$GRAIN_SECRETS_BACKEND`, then `"file"`), `-secrets-sm-prefix` (string; default `$GRAIN_SECRETS_SM_PREFIX`; `projects/<project>/secrets/<id-prefix>`, required when `-secrets-backend=secretmanager`), `-db-driver` (string; default `$GRAIN_DB_DRIVER`, then the data dir's record, then sqlite), `-db-dsn` (string; default `$GRAIN_DB_DSN`, then the data dir's record) |
| `list` | none. Prints every secret and its keys, never values |
| `set [-value-file F] <secret> <key>` | `-value-file` (string; default: read stdin) |
| `delete <secret> [key]` | none |
| `mint-gemini-key -project P` | `-project` (required), `-credential` (default the standing minter credential), `-out` (default `<data-dir>/secrets/gemini-api-key`). Leaves an existing key alone |
| `repo [flags] [URL]` | `-branch` (default `main`), `-file`, `-token-file`, `-none`. Names, reports or clears the repository encrypted secrets are backed up to |

Secret files are written owner-only, with no temporary file left behind.

<!-- BEGIN GENERATED FLAGS: grain secrets -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain secrets list -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain secrets set -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-value-file` | string |  |  | file holding the exact bytes to write as this key's value; defaults to reading stdin |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain secrets delete -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain secrets mint-gemini-key -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-credential` | string | `"gcp-key-minter"` |  | name of the standing minter credential in this secrets store |
| `-out` | string |  |  | file to write the key to; defaults to <data-dir>/secrets/gemini-api-key |
| `-project` | string |  |  | GCP project to mint the key in (required) |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain secrets repo -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/secrets_test.go`: `TestMintGeminiKeyLeavesAnExistingKeyAlone`, `TestMintGeminiKeyRequiresAProject`, `TestWriteSecretFileIsOwnerOnlyAndExact`; `pkg/cli/secrets_repo_test.go`: `TestSecretsRepoCommandNamesAndClearsTheRepository`.

### 6.3 `grain connections -data-dir DIR import <file>` {#CLI-52}

This records the git and model connections a YAML seed file describes. It is idempotent, and a mistake is refused before anything is written. Relay settings are skipped with a note. The usage is hand-written. Flags:
- `-data-dir` (default `$GRAIN_DATA_DIR`);
- `-db-driver` (default `$GRAIN_DB_DRIVER`, then the data dir's record, then sqlite);
- `-db-dsn` (default `$GRAIN_DB_DSN`, then the data dir's record).

<!-- BEGIN GENERATED FLAGS: grain connections -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain connections import -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/connections_test.go`: `TestConnectionsImportRecordsConnectionsAndIsIdempotent`, `TestConnectionsImportRefusesAMistakeBeforeWritingAnything`, `TestConnectionsImportSkipsRelaySettingsWithNote`.

### 6.4 `grain state -data-dir DIR <command>` {#CLI-53}

This covers where the installation's state repository lives and what it holds. The usage is hand-written. Flags:

| Command | Behaviour / flags |
|---|---|
| `state` | `-data-dir` (string; default `$GRAIN_DATA_DIR`; required except for `check`) |
| `status` | inspect the config repository, including whether the secrets key is backed up |
| `repo [-branch B] [-token-file T] [-none] [URL]` | use an external config repository in read-only IaC mode, report the mode, or go back to the internal repository with `-none` |
| `sync` | export the database and commit now, or pull and apply in external mode |
| `resets clear` | print and forget the settings-reset notice |
| `reformat` | record that the schema changed, so the next start replaces the dump |
| `restore-snapshot DIR` | restore the settings and memory tables from a snapshot |
| `prepare-upgrade` | export a store this build cannot open, for the next start to migrate. Prints `prepared FROM TO DIR`, `current N` or `none` |
| `check [-migrate] [-base REV] DIR` | load a state repository into a throwaway database and report what breaks. Needs no `-data-dir` and has no team mode. `-migrate` (bool) rehearses a migration: it accepts a dump written at another schema and reports what would arrive. `-base` (string) names the git revision the change is against: a credential typed into a capability server rather than referenced as `$secret:<name>` fails the check when the change adds it and is a warning when the base already held it (without `-base`, or with `-migrate`, every one is a warning). The check grain posts on its own configuration repositories reads the base from the clone's `origin/HEAD` |
| `key show` / `key path` | print the secrets public key / where the private key is read from |
| `key import [-key-file F]` | install a private key from `-key-file` or stdin. The key it replaces is kept beside it as `secrets.key.replaced-<time>` |
| `key retired [list]` | list those replaced keys by name and public half (`status` lists them too) |
| `key retired delete (-all \| NAME...)` | delete replaced keys by exact name. Refuses the live key, anything that is not an archive beside it, and the archive the secrets file is still encrypted to when the live key does not open it |
| `key wrap -kms KEY` | wrap the private key with the Cloud KMS key `KEY` (`projects/P/locations/L/keyRings/R/cryptoKeys/K`), in the daemon when one answers and in this process otherwise. It wraps, unwraps the result and compares before replacing the file by a rename, then lists the plaintext copies of a private key it can still see (`secrets.key.replaced-*`, `/run/grain-secrets`) and notes the escrow ([secrets key protection §6.2](../../design/secrets-key-protection.design.md#SKP-6-2)) |
| `key unwrap` | put the private key back in plaintext form, verified against KMS and read back before it is done. Run it before downgrading to a build that cannot read the wrapped form |

<!-- BEGIN GENERATED FLAGS: grain state -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain state status -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain state repo -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain state sync -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain state resets clear -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain state reformat -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain state restore-snapshot -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain state prepare-upgrade -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain state check -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain state key show -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain state key path -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain state key retired -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain state key retired delete -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-all` | bool |  |  | delete every retired key |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain state key import -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-key-file` | string |  |  | file holding the private key; defaults to reading stdin |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain state key wrap -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-kms` | string |  |  | the Cloud KMS key to wrap with: projects/<p>/locations/<l>/keyRings/<r>/cryptoKeys/<k> (required) |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain state key unwrap -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/state_test.go`: `TestStateStatusReportsTheSecretsKeyAndSaysToBackItUp`, `TestStateRestoreSnapshotAndStatusReporting`, `TestStateRepoCommandNamesReportsAndClearsExternalConfigRepo`; `pkg/cli/state_check_test.go`; `pkg/cli/state_retired_test.go`; `pkg/cli/state_wrap_test.go`; `pkg/cli/state_reformat_test.go`; `pkg/cli/stateprepare_test.go`.

### 6.5 `grain backup`, `grain export`, `grain import` {#CLI-54}

- **`backup -data-dir DIR [flags]`** takes an online snapshot of the database and a restorable config-repository bundle, and uploads them to Google Drive or a GCS bucket. `-h` exits 0.
- **`export -data-dir DIR [flags]`** writes a verified migration bundle with the database, the state repository and the secrets.
- **`import -data-dir DIR -in FILE [flags]`** restores a bundle. It runs store migrations and verifies consistency. Existing data is refused without `-force`, and tampered or unlisted files are rejected.

`backup`'s usage is hand-written. Its flags:
- `-data-dir` (default `$GRAIN_DATA_DIR`; required);
- `-gcs-bucket` (string): upload to this GCS bucket instead of Drive;
- `-google-drive-folder` (string, default `root`);
- `-google-credentials-file` (string): omitted means Application Default Credentials.

Credentials and `data-dir/secrets` are never included.

The export and import usage is hand-written. Their flags:

| Flag | Commands | Meaning |
|---|---|---|
| `-data-dir` | both | default `$GRAIN_DATA_DIR` |
| `-out`, `-output`, `-file` | export | bundle path (aliases; default `grain-export-<timestamp>.tar.gz` or `.enc`) |
| `-in`, `-input`, `-file` | import | bundle path (aliases) |
| `-force` | import | overwrite an existing database, secrets or state |
| `-passphrase-file` | both | passphrase file, or `-` for stdin |
| `-key-file` | both | 64-hex-character key file, or `-` for stdin |
| `-passphrase`, `-password`, `-key` | both | deprecated literal forms |
| `-plaintext` | export | allow an unencrypted bundle that carries `secrets/secrets.key`; without a passphrase or key, export refuses one unless this is set |

<!-- BEGIN GENERATED FLAGS: grain backup -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain export -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain import -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/backup_test.go`: `TestBackupHelpExitsZero`; `pkg/cli/export_import_test.go`: `TestExportImportRoundtripEncrypted`, `TestBundleSecretFlags`, `TestImportIntegrityTamperRejection`, `TestImportRequiresForceForExistingData`, `TestImportRejectsFilesTheManifestDoesNotList`.

### 6.6 `grain repo-case [-data-dir DIR] [-default-target owner/name] [-apply]` {#CLI-55}

Reports every stored row whose repository is spelled in another case than the deployment uses. The usage is hand-written. Flags:
- `-data-dir` (default `$GRAIN_DATA_DIR`; required);
- `-default-target` (string): the daemon's own `-default-target-repo`;
- `-apply` (bool).

 `-apply` rewrites them and exports the rewrite to the state repository. It refuses while that repository holds an unloaded commit.

<!-- BEGIN GENERATED FLAGS: grain repo-case -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/repocase_test.go`, `pkg/cli/repocase_state_repo_test.go`.

### 6.7 `grain upgrade-postgres` (`upgrade-sqlite-to-postgres`, `db upgrade-postgres`) {#CLI-56}

Copies an existing SQLite store into PostgreSQL and optionally writes `db-config.json`, so later daemon starts use PostgreSQL. `grain db upgrade-postgres` and `grain db upgrade-sqlite-to-postgres` are the same command, and report errors as `grain upgrade-postgres: …`. Positional arguments are refused (`unexpected argument "<x>"`). A bare `grain db` prints `usage: grain db upgrade-postgres [flags]` and `grain db: a command is required …`; `grain db -h` prints that usage and `grain db: flag: help requested`; any other first word after `db` (for example the sandbox CLI's `query`) is refused with `grain db: unknown command "<x>" (grain db upgrade-postgres [flags], grain db check; db query is the in-sandbox CLI's)`. All exit 1. The usage is hand-written.

It runs only offline. While a daemon holds the data directory (the flock on `<data-dir>/daemon.lock`) or its lease on the target PostgreSQL store (a session advisory lock), it refuses before copying anything or writing `db-config.json`, with an error naming `sudo systemctl stop grain-daemon.service`. It never stops the daemon itself. `grain backup` refuses the same way.

`grain db check [-data-dir DIR]` only reads. It prints the store a daemon started on DIR would open, what that store holds (last task id, last run), DIR's high-water mark (`store-highwater.json`) and any other store under DIR. It exits 1, naming how far behind and how to recover, when the store is behind the mark. `setup.sh` runs it before restarting the daemon. Flags:
- `-data-dir` (default `$GRAIN_DATA_DIR`);
- `-sqlite-path` (string): the source SQLite file;
- `-dsn` (string): the target PostgreSQL DSN;
- `-no-data` (bool): skip copying rows;
- `-no-save-config` (bool): do not write `db-config.json`.

<!-- BEGIN GENERATED FLAGS: grain upgrade-postgres -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain upgrade-sqlite-to-postgres -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain db -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/upgrade_postgres_test.go`: `TestDBCmdRefusesUnknownSubcommand`, `TestUpgradePostgresRefusesStrayArguments`, `TestUpgradePostgresCLIValidation`, `TestUpgradePostgresCLIHelp`; `pkg/cli/store_offline_test.go`: `TestStoreToolsRefuseWhileADaemonHoldsTheStore`, `TestDBCheckSaysWhichStoreAndWhetherItIsBehind`.

### 6.8 `grain controller bootstrap-github-app` and `grain setup gcp` {#CLI-57}

- `controller bootstrap-github-app` is one-time interactive setup for the github-sandbox capability. It writes the App's credentials into `<data-dir>/secrets`. A bare `grain controller` prints `usage: grain controller bootstrap-github-app [flags]`. Any other first word, `-h` included, prints `grain controller: unknown command "<x>"`. Both exit 1.
- `setup gcp` bootstraps the GCP service accounts, IAM roles and keys the gcp-key and gemini-key capabilities need.

<!-- BEGIN GENERATED FLAGS: grain controller -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain controller bootstrap-github-app -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-data-dir` | value |  |  | root directory a colocated grain daemon was started with -- the App's credentials are written into <data-dir>/secrets, the same store 'grain daemon' and 'grain secrets' read and write (required) |
| `-git-credential` | string |  |  | also write <data-dir>/secrets/github/<name>.app.json as a standalone GitHub App bot git credential (uses least-privilege bot permissions unless -sandbox-permissions is set) |
| `-github-host` | string | `"github.com"` |  | GitHub host to run the manifest flow against |
| `-installation-id` | string |  |  | optional installation ID to pin in <git-credential>.app.json (empty resolves per repository automatically) |
| `-name` | string | `"grain-sandbox"` |  | name for the new GitHub App |
| `-org` | string |  |  | GitHub organization login to register the App under (empty registers under the signed-in account) |
| `-sandbox-permissions` | bool |  |  | include github#sandbox-repo repository-creation permissions even when -git-credential is set |
| `-url` | string | `"https://github.com/ai-on-gke/grain"` |  | homepage URL the App manifest requires |
| `-webhook-url` | string |  |  | public HTTPS URL where GitHub delivers webhook events (e.g. https://grain.example.com/webhooks/github) |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain setup -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain setup gcp -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-agent-account-count` | int | `1) (default 1` |  | number of agent service accounts to create for gcp-key |
| `-agent-account-id` | string | `"grain-agent"` |  | account_id of the service account gcp-key mints per-task keys for |
| `-credentials-file` | string |  |  | a GCP service-account key file to authenticate with; empty uses Application Default Credentials (gcloud auth application-default login, or GOOGLE_APPLICATION_CREDENTIALS) |
| `-enable-gemini-key` | bool |  |  | also grant the minter account roles/serviceusage.apiKeysAdmin and enable the Gemini/API Keys APIs, for the gcp server's gemini-key method (https://grain.dev/capabilities/gcp#gemini-key) |
| `-key-out` | string |  |  | path to write the minted minter key to (required with -mint-key); refused if the file already has content, the same never-overwrite rule scripts/setup.sh's own seed_secret follows |
| `-mint-key` | bool |  |  | mint a key for the minter account and write it to -key-out -- do this once, on a new installation; a later run leaves an existing key alone (see this file's own doc comment on why) |
| `-minter-account-id` | string | `"grain-gcp-key-minter"` |  | account_id of the service account that mints/revokes the agent account's keys |
| `-project` | string |  |  | GCP project to bootstrap (required) |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/controller_test.go`; `setup gcp` *untested* here.

### 6.9 `grain init [flags]` and `grain init workstation [flags]` {#CLI-58}

- **`init`** takes a host from bare Linux to a running deployment on one of four platforms (`-platform`: `linux-kvm` (the default), `linux-container`, `k8s-container`, `k8s-granule`).
  - It requires `-repo-root` and `-data-dir`.
  - It refuses an unknown platform, a wildcard GitHub repo, and a token passed as a literal argument.
  - Its exit code tells the three outcomes apart.
- **`init workstation`** writes a single-user workstation's policy and systemd user unit. It asks for what it was not told unless `-non-interactive`. `-data-dir` defaults to `$GRAIN_DATA_DIR`, else `~/.grain`.

<!-- BEGIN GENERATED FLAGS: grain init -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-admin-account` | string |  |  | deprecated and ignored: this build is single-tenant only, with no system admins to set up |
| `-admin-user` | string |  |  | deprecated and ignored: this build is single-tenant only, with no system admins to set up |
| `-claude-cli-sha256-amd64` | string | `"dabafec82ca1319fa43d8be36a10fc61b1d11821cb98ad5df04ad95e199b8eed"` |  | expected sha256 of the linux-x64 claude release |
| `-claude-cli-sha256-arm64` | string | `"a7e687fd1cb054d651c7588223de3786c99d4c682365e3c3ae1e59ce7f3a591f"` |  | expected sha256 of the linux-arm64 claude release |
| `-claude-cli-version` | string | `"2.1.319"` |  | claude CLI release to install |
| `-claude-home` | string |  |  | $HOME for the claude installer (default <data-dir>/claude) |
| `-daemon-arg` | value |  |  | extra argument appended to the daemon's own command line, repeatable |
| `-daemon-group` | string |  |  | run the daemon as this group (systemd Group=) instead of -daemon-user's own name; only meaningful alongside -daemon-user |
| `-daemon-user` | string |  |  | run the daemon as this user (systemd User=) instead of grain; grain init get-or-creates it (a system account, no login shell), get-or-adds it to the "docker" group, and chowns -data-dir (and -claude-home, if separate) to it, exactly as it does for the default. The one value that opts all the way back out -- no account created, no chown, no User=/Group= line at all -- is the literal "root" |
| `-data-dir` | string |  |  | the daemon's -data-dir (required); for -platform=k8s-container there is no shared filesystem with the daemon, so this instead names a directory on this machine for the preflight's own scratch manifests |
| `-dry-run` | bool |  |  | print the plan; change nothing |
| `-github-credential-name` | string |  |  | name the GitHub token is written under (secrets/github/<name>.token) |
| `-github-repo` | value |  |  | an "owner/repo" or "owner/*" pattern to point at -github-credential-name's token, repeatable (never "*" -- refused) |
| `-github-token` | string |  |  | deprecated: a GitHub token given directly on the command line -- ps shows it to every other user on this host for as long as this process runs, and shell history keeps a second copy; use -github-token-file instead. Ignored when -github-token-file is also given. |
| `-github-token-file` | string |  |  | path to a file holding a GitHub token to write and narrow to -github-repo; empty (with -github-token also empty) skips the credential-ladder step entirely |
| `-gke-cluster` | string | `"grain-sandboxes"` |  | gke-sandboxes only: GKE Standard cluster name |
| `-gke-disk-size-gb` | int | `100` |  | gke-sandboxes only: node boot disk size in GiB |
| `-gke-disk-type` | string |  |  | gke-sandboxes only: node boot disk type (defaults to hyperdisk-balanced for c4-* and pd-balanced for n2-*) |
| `-gke-git-proxy-url` | string | `"http://127.0.0.1:9728"` |  | gke-sandboxes only: git proxy URL verified from the throwaway sandbox |
| `-gke-image-type` | string | `"UBUNTU_CONTAINERD"` |  | gke-sandboxes only: GKE node image type (UBUNTU_CONTAINERD required for nested virtualization) |
| `-gke-kubeconfig-out` | string | `"/var/lib/grain/kubeconfig"` |  | gke-sandboxes only: path to write the least-privilege kubeconfig for the off-cluster controller |
| `-gke-location` | string | `"us-central1-a"` |  | gke-sandboxes only: GCP zone or region for the GKE Standard sandbox cluster |
| `-gke-machine-type` | string | `"n2-standard-8"` |  | gke-sandboxes only: GCE machine type for nested virtualization (e.g. c4-standard-96-lssd or n2-standard-8) |
| `-gke-max-nodes` | int |  |  | gke-sandboxes only: autoscaler maximum node count (0 disables autoscaling) |
| `-gke-min-nodes` | int |  |  | gke-sandboxes only: autoscaler minimum node count (when -gke-max-nodes > 0) |
| `-gke-node-pool` | string | `"kvm-pool"` |  | gke-sandboxes only: nested-virtualization node pool name |
| `-gke-num-nodes` | int | `1` |  | gke-sandboxes only: initial node count in the nested-virtualization node pool |
| `-gke-package-cache-url` | string | `"http://127.0.0.1:9729"` |  | gke-sandboxes only: package cache URL verified from the throwaway sandbox |
| `-gke-project` | string | `"grain-sandboxes"` |  | gke-sandboxes only: GCP project ID for the GKE Standard sandbox cluster |
| `-gke-service-account` | string | `"grain"` |  | gke-sandboxes only: least-privilege Kubernetes ServiceAccount name |
| `-grain-binary` | string |  |  | where to leave the built grain binary (default <repo-root>/bin/grain) |
| `-granule-binary` | string |  |  | where to build the granule CLI to (default <repo-root>/bin/granule) |
| `-granule-runtime-image` | string | `"granule:vendored"` |  | tag for the granule runtime image (scripts/granule/build-granule-image.sh) |
| `-granule-ssh-user` | string | `"debian"` |  | guest account granule authorizes its key for and the daemon execs as |
| `-granule-workspace` | string | `"/home/debian"` |  | working directory tools operate in on each granule VM |
| `-guest-base-image` | string | `"granule-guest-base:vendored"` |  | tag for Granulefile.guest-base |
| `-guest-image` | string | `"grain-guest:dev"` |  | tag for Granulefile -- the full sandbox guest |
| `-install-claude-cli` | bool | `true` |  | install the claude CLI |
| `-k8s-context` | string |  |  | k8s-container/k8s-granule: --context passed to every kubectl call this command makes (empty uses kubectl's own current context) |
| `-k8s-deployment` | string | `"grain"` |  | k8s-container/k8s-granule: the Deployment the manifest creates |
| `-k8s-granule-image` | string | `"docker.io/grainai/sandbox:latest"` |  | k8s-granule only: the packed guest image the preflight's throwaway pod boots and every real sandbox pod runs -- a self-contained image with its own guest disk and kernel already baked in (the shape Granulefile's own build produces), not a plain sandbox image and not "granule:vendored" either (-granule-runtime-image's own default, which has no guest disk and cannot boot at all). Defaults to defaultSandboxImage, the same per-commit guest CI publishes and -platform=linux-kvm's -sandbox-image pulls -- stamped in at build time the way -k8s-sandbox-image's own default is; override it for a guest of your own (a team layer, a different base) |
| `-k8s-granule-manifest` | string |  |  | k8s-granule only: path to grain-granule.yaml (default <repo-root>/deploy/kubernetes/grain-granule.yaml) |
| `-k8s-kubeconfig` | string |  |  | k8s-container/k8s-granule: --kubeconfig passed to every kubectl call this command makes (empty uses kubectl's own default) |
| `-k8s-manage-port-forward` | bool | `true` |  | k8s-container/k8s-granule: start a kubectl port-forward svc/<k8s-service> before provisioning and stop it after -- false if -ui-addr already reaches the Service some other way (an operator-managed port-forward, an Ingress, an internal route) |
| `-k8s-manifest` | string |  |  | k8s-container only: path to grain.yaml (default <repo-root>/deploy/kubernetes/grain.yaml) |
| `-k8s-namespace` | string | `"grain"` |  | k8s-container/k8s-granule: the Namespace the manifest's objects are created in (never created or labelled by this command itself -- see -k8s-granule-* below for why) |
| `-k8s-networkpolicy-manifest` | string |  |  | k8s-container only: path to networkpolicy.yaml (default <repo-root>/deploy/kubernetes/networkpolicy.yaml) |
| `-k8s-node-selector` | value |  |  | k8s-granule only: one "key=value" nodeSelector entry pinning every sandbox pod -- and the preflight's own nested-virt check -- to a node pool, repeatable. Leave unset only on a single-pool cluster where every node offers nested virtualization |
| `-k8s-require-network-policy` | bool |  |  | k8s-container only: this deployment will dispatch network:none tasks, so preflight must confirm the cluster's CNI actually enforces NetworkPolicy, and apply networkpolicy.yaml |
| `-k8s-run-host-rwx-class` | string |  |  | k8s-container only: the ReadWriteMany StorageClass of the run hosts' shared volume (deploy/kubernetes/runhost-rwx.yaml); a gcsfuse-backed class is refused. Empty keeps them on grain-data, pinned to the daemon's node |
| `-k8s-sandbox-image` | string |  |  | k8s-container only: the sandbox image the preflight's throwaway pod pulls -- must match -k8s-manifest's own -k8s-sandbox-image, which preflight refuses otherwise (default: the one -k8s-manifest names, else this build's own docker.io/grainai/sandbox-k8s:latest) |
| `-k8s-service` | string | `"grain"` |  | k8s-container/k8s-granule: the Service the manifest creates |
| `-k8s-storage-class` | string |  |  | k8s-container only: the StorageClass the preflight's scratch PVC requests (empty tests the cluster's own default class) |
| `-platform` | string | `"linux-kvm"` |  | deployment shape to bring up: linux-kvm (a Linux host with /dev/kvm, one VM per sandbox -- the default), linux-container (a Linux host with no /dev/kvm, one docker container per sandbox), k8s-container (a Kubernetes cluster, one plain pod per sandbox -- see the -k8s-* flags below), k8s-granule (a Kubernetes cluster, one privileged pod per sandbox booting a real VM over /dev/kvm -- see the -k8s-* and -k8s-granule-* flags below), or gke-sandboxes (an off-cluster GKE Standard sandbox cluster with nested virtualization and /var/lib/grain/kubeconfig -- see the -gke-* flags below); see docs/grain-init-platforms.md |
| `-repo-root` | string |  |  | the grain checkout to build from (required) |
| `-sandbox-network` | string | `"grain-sandboxes"` |  | docker network created for sandboxes to join -- granule's own VM containers under -platform=linux-kvm, or the sandbox containers themselves under -platform=linux-container -- whose gateway becomes -granule-git-proxy-host or -container-git-proxy-host respectively |
| `-start-daemon` | bool | `true` |  | start the daemon once everything it needs is ready |
| `-team-max-workers` | int |  |  | deprecated and ignored: a team has no worker count of its own |
| `-tenancy` | string |  |  | deprecated and ignored: this build is single-tenant only |
| `-ui-addr` | string | `"0.0.0.0:4242"` |  | host:port the daemon's UI/API listens on, written into a host platform's unit (the steps below reach it over loopback) -- unset keeps the installed unit's own on a re-run; must match -ui-addr if -daemon-arg overrides it |
| `-ui-trust-loopback` | bool | `true` |  | deprecated and ignored: every caller, loopback included, presents the access code |
| `-upgrade-allowed-branches` | string |  |  | comma-separated branches an upgrade of this deployment may install, written into the unit as the daemon's own -upgrade-allowed-branches: each a path.Match pattern ('*' never crosses a '/'; e.g. main,release/*), or ** for any branch. Unset keeps whatever the grain-daemon.service already on this host passes, and with none there writes no flag, leaving the daemon's default (main, candidate, latest and vX.Y.Z tags). Anyone who can push a matching branch can have it built and run as this daemon, so ** is an explicit opt-in |
| `-use-binary` | value |  |  | run this prebuilt grain binary (a release download, say) instead of building one: skips make build, so preflight demands neither make nor node/npm, and under -platform=linux-container no Go toolchain either |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain init workstation -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-capability` | value |  |  | a capability pattern tasks may be granted (capabilities.allow); repeatable. The Director's own and grain CI's are allowed without being named |
| `-container-git-proxy-host` | string |  |  | with container sandboxes, the address a sandbox container reaches this host at (default: the gateway of docker network grain-sandboxes, which this creates if it is missing) |
| `-data-dir` | string | `"$GRAIN_DATA_DIR"` | $GRAIN_DATA_DIR | the daemon's data directory ($GRAIN_DATA_DIR, else ~/.grain) |
| `-force` | bool |  |  | rewrite grain_policy.yml even if one exists |
| `-github-token-file` | string |  |  | a file holding a GitHub token, stored narrowed to -repo's repositories |
| `-install-service` | bool | `true` |  | write the grain.service systemd user unit |
| `-model-backend` | string |  |  | claude, codex or agy (default: the first on PATH) |
| `-network` | string |  |  | network a task's sandbox may have: open or none (default open; host sandboxes have the host's own network, so only open) |
| `-non-interactive` | bool |  |  | ask nothing: take the detected default for every choice not given as a flag |
| `-owner` | value |  |  | a requester whose tasks are this machine's own (local_requesters); repeatable. Defaults to git's user.email; this account's name, which the daemon files the local CLI's and UI's tasks as, is always added |
| `-repo` | value |  |  | a repository tasks may work on, as owner/name, a URL or a local checkout; repeatable. None refuses every task that names one |
| `-sandbox-backend` | string |  |  | container, granule or host (default: the strongest this machine runs) |
| `-start-service` | bool | `true` |  | enable and start grain.service (needs a systemd user session) |
| `-ui-addr` | string | `"127.0.0.1:4242"` |  | the address the UI and API listen on |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/init_test.go`: `TestInitExitCodeDistinguishesTheThreeOutcomes`, `TestCmdInitRequiresRepoRoot`, `TestCmdInitRequiresDataDir`, `TestCmdInitRejectsUnknownPlatform`, `TestCmdInitDefaultPlatformFlagIsLinuxKVM`, `TestCmdInitRefusesTokenShapedArgument`; `pkg/cli/init_workstation_test.go`.

### 6.10 `grain sync -config FILE [flags]` {#CLI-59}

Reconciles a live deployment's settings (`GET /api/settings`, then `PUT /api/settings`) and/or GCP infrastructure from a JSON file with `settings` and/or `gcp` sections.
- `-config` is required, and a file with neither section is refused.
- `-dry-run` plans through `POST /api/settings/plan` without writing, and explains a daemon with no plan route.
- It presents the local API token from cli.env's data dir.

<!-- BEGIN GENERATED FLAGS: grain sync -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-config` | string |  |  | path to a JSON file with "settings" and/or "gcp" sections to reconcile (required) |
| `-dry-run` | bool |  |  | report what the "settings" section would change and write nothing -- "gcp" has no plan mode and is skipped, so a dry run of a whole config file changes nothing at all |
| `-server` | string | `"http://127.0.0.1:4242"` |  | base URL of a running "grain daemon"'s UI/API -- only needed if -config has a "settings" section |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/sync_test.go`: `TestCmdSyncRequiresConfig`, `TestCmdSyncRejectsAConfigWithNeitherSection`, `TestCmdSyncDryRunPlansSettingsWithoutWriting`, `TestCmdSyncPresentsTheLocalAPITokenFromCLIEnvsDataDir`.

---

### 6.11 `grain access <list|allow|role|deny> [args]` {#CLI-63}

<a id="CLI-107"></a>This manages the allowed users and groups list (Settings -> Access): who may sign in when authenticated by Cloud IAP or another registered IdentityVerifier. Unlike the rest of this section it calls the daemon's API. The routes' scopes are `access:read` for `list` and `access:write` for `allow`, `role` and `deny` (`pkg/ui/route_scopes.go`). Every entry has a role, an API scope set (`docs/design/access-rbac.md`), and agents never change the list.

| Subcommand | Request |
|---|---|
| `list` (the default; also `ls`) | `GET /api/access/principals`. Prints each entry with its role, who added it and when, or `No allowed users or groups.`, and warns about `-ui-allowed-principals` entries that were dropped on import |
| `allow [--role <set>] [--reason <why>] [--label <name>] <user:<email>\|group:<email>\|serviceAccount:<email>\|delegate:<email>>` (also `add`) | `POST /api/access/principals {"entry","role","reason","label"}`. `--role` defaults to `grain/operator` (or `grain/delegated` for `delegate:`), whose dangerous scopes need a `--reason`, and from a signed-in person a role of their own that holds every scope of it. `--label` sets an optional short identifier (`[a-z][a-z0-9-]{0,31}`) on `delegate:` entries. Adding an existing entry is a no-op that keeps its role |
| `role [--reason <why>] [--confirm-self] <entry> <set>` | `PATCH /api/access/principals/{entry} {"role","reason","confirmSelf"}`. Prints `<entry>: role <old> -> <new>` |
| `deny [--reason <why>] [--confirm-self] <user:<email>\|group:<email>>` (also `remove`, `rm`) | `DELETE /api/access/principals/{entry}?reason=<why>&confirmSelf=1` |

A change involving a role that holds a dangerous scope or denies scopes needs `--reason`, which is recorded in the access history with the old and new role. With the access code anything may be changed. A signed-in person changes another entry only when their own role holds every scope of its current and new role; they may not widen, or add or lift a deny on, an entry that matches them (their `user:` entry or a `group:` they are in), narrowing one needs `--confirm-self` (refused with code `confirm_self` otherwise), and the last `user:`/`group:` entry holding a full admin role is never removed or narrowed (`last_admin`; the access code still can).

Errors: an unknown subcommand gives `unknown access subcommand "<s>"` followed by the usage, and a wrong argument count gives that subcommand's own usage line (`usage: grain access allow <user:<email>|group:<email>>`). This replaces the deprecated `-ui-allowed-principals` flag on `grain daemon` and `grain demo`, which is now only imported once into an empty database allowlist.

<!-- BEGIN GENERATED FLAGS: grain access -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/access_test.go`: `TestGrainAccessCLI`.

## 7. Build information modes

### 7.1 `grain version`, `schema-version`, `release-notes` {#CLI-60}

- `version [-json] [-track] [-data-dir D] [-unit-file F]` prints the version and build stamp, or only the detected install track (`google` or `oss`).
- `schema-version` prints `pkg/model.SchemaVersion`.
- `release-notes [-from vA] [-to vB] [-json]` prints the per-version notes this build carries. An empty range is an empty list, and stray arguments are refused.

<!-- BEGIN GENERATED FLAGS: grain version -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-data-dir` | string |  |  | optional data directory to inspect for install track signals |
| `-json` | bool |  |  | print version information as JSON |
| `-track` | bool |  |  | print only the detected install track ("google" or "oss") |
| `-unit-file` | string |  |  | optional systemd unit file to inspect for install track signals |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain schema-version -->
_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain release-notes -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-from` | string |  |  | only notes for versions newer than this one (the version being upgraded from) |
| `-json` | bool |  |  | print the notes as JSON, with the version of this binary |
| `-to` | string |  |  | only notes for versions up to and including this one |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/version_test.go`; `pkg/cli/schemaversion_test.go`: `TestSchemaVersionCmdPrintsModelSchemaVersion`; `pkg/cli/releasenotes_test.go`.

### 7.2 Image printers {#CLI-61}

- `sandbox-image [-base]` prints the Granule sandbox image this build expects.
- `sandbox-image check` probes it, and `sandbox-image contract` prints its contract (`-json`).
- `container-sandbox-image` and `k8s-sandbox-image` print the Docker and Kubernetes sandbox images. Today both print the same `k8s-sandbox` image (the local Docker sandbox runs it too). Both take `-base`, or a leading `base` word, to print the `k8s-sandbox-base` image instead.
- `image` prints the image this build is published as.

<!-- BEGIN GENERATED FLAGS: grain sandbox-image -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-base` | bool |  |  | print the minimal guest-base image reference instead of the full guest image |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain sandbox-image check -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-backend` | string | `"auto"` |  | sandbox backend to verify against: auto, granule, k8s, or container |
| `-docker` | string | `"docker"` |  | docker binary to inspect/probe the image with |
| `-json` | bool |  |  | print check result as JSON |
| `-probe` | bool |  |  | also run the in-container boundary verification script (VerifyScript) via docker run |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain sandbox-image contract -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-json` | bool |  |  | print contract details as JSON |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain container-sandbox-image -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-base` | bool |  |  | print the minimal k8s-sandbox-base image reference |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain k8s-sandbox-image -->
| Flag | Type | Default | Env | Description |
|---|---|---|---|---|
| `-base` | bool |  |  | print the minimal k8s-sandbox-base image reference |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain image -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/cli/sandboximage_test.go`: `TestSandboxImageCommandPrintsDefaultAndBase`, `TestSandboxImageContractSubcommand`, `TestSandboxImageCheckSubcommand`.
