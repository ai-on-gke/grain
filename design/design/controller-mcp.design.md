# The controller MCP server: one endpoint, six tools

**Owner:** @Dzianis-Harbatsenka
**Status:** implemented (PR #511); this document describes what is on `main` and lists what is open in §8.
**Related:** [`docs/agent-api.md`](../../docs/agent-api.md) (scope vocabulary, route rules, tool reference), [`sandbox-first-agent-surface.design.md`](sandbox-first-agent-surface.design.md) (the sandbox `grain` CLI this complements; superseded in part by this design), [`external-cli.design.md`](external-cli.design.md) (external callers and credentials), [`unified-grants.design.md`](unified-grants.design.md) (`api:*` grants), [`capabilities.spec.md`](../spec/capabilities/capabilities.spec.md) (capability MCP servers).

**The core rule:** an agent that reaches the controller API through MCP gets one server, `grain-controller` at `POST /api/mcp`, which serves the same six tools to every caller. What a call may reach is decided per call from the caller's grants, never from the tool list.

## 1. What it is {#CM-1}

`grain-controller` is a stateless Streamable HTTP MCP server mounted on the daemon's `ui.Server` at `POST /api/mcp` (`pkg/ui/mcp_endpoint.go`). It has no session state: one JSON-RPC 2.0 message per `POST`, one JSON answer, no event stream. It answers `initialize` (with a workflow guide as `instructions`), `ping`, `tools/list` and `tools/call`; notifications get `202`.

It does not have a tool per route. The agent API has a few hundred scoped routes (`ui.AgentAPIRoutes()`, every entry of `pkg/ui/route_scopes.go` whose scope is not `internal`), and serving each as its own tool costs a caller holding many scopes a few hundred schemas of context. Instead the server has **six fixed tools** (§3): four compact tools for the everyday task operations, and two for progressive discovery and invocation of every other route. `tools/list` is identical for every caller; authorization happens when a tool runs.

Everything a tool returns passes through `pkg/mcp`'s result cap: output over 20 KiB is elided in the middle with a notice saying how much was dropped and what narrower call to make instead.

## 2. Who calls it {#CM-2}

The endpoint sits behind the daemon's ordinary `/api/` admission (IAP/OIDC identity, the deployment access code in `Authorization: Bearer` or `X-Grain-Access-Code`, an access-session cookie, or an external agent token `gma_…` when the deployment has enabled them). Once admitted, each call resolves to one of three callers; the decision is made again on every call, from the store, so a grant change is seen by the next call.

| Caller | Recognised by | Authority |
| --- | --- | --- |
| **External agent** (a BYO harness on a workstation) | verified `gma_…` token on the request | the token's scope set, scopes and repository limits (`model.ResolveExternalAgentGrants`); `X-Grain-Acting-Task` is ignored |
| **Acting task** (a run or session) | `X-Grain-Acting-Task: <task id>` | the task's `api:*` grants (`model.APIGrantsOf`); on `X-Grain-Channel: sandbox-cli`, the narrower sandbox set (`model.SandboxAPIGrantsOf`) |
| **Person** | neither of the above | full access to every route in `AgentAPIRoutes()` |

Three paths deliver those callers to the endpoint:

- **A run holding any `api:*` grant** gets `grain-controller` attached as a capability server beside `grain-sandbox` (§5). The harness talks to a loopback proxy minted on the host for that run (`daemon.controllerMCPProxy`, keyed by task ID), which strips whatever `Authorization` or `X-Grain-*` headers the harness sent, injects `X-Grain-Acting-Task`, adds the daemon's local access token, and delivers the request in-process. The model never holds a credential.
- **A process inside the sandbox** reaching `/api/mcp` through the per-sandbox HTTP proxy (`gitproxy.forwardToAPI`) arrives with `X-Grain-Acting-Task`, `X-Grain-Acting-Run` and `X-Grain-Channel: sandbox-cli` injected and its own `Authorization`/`X-Grain-*` headers stripped, so it is the acting task on the sandbox channel, with the sandbox's scope ceiling.
- **An external harness** connects directly with a `gma_…` token or, for a person at a workstation, their own identity or the access code.

## 3. The six tools {#CM-3}

`tools/list` advertises them in this order, each flagged `_meta["antigravity/alwaysLoad"]: true` so harnesses that lazy-load tools keep these resident.

| Tool | Arguments | Does | Returns |
| --- | --- | --- | --- |
| `grain_explore_scope` | `scope?` (group, exact scope, preset or declared scope set), `tool?` (route tool name or substring of a tool name or path) | With no arguments, the scope groups the caller is authorized for and how many routes each holds; with `scope` and/or `tool`, the matching authorized routes with method, path, scope, whether a `reason` is needed, and their JSON input schemas | JSON; when nothing authorized matches, an `isError` text that names the scopes required if the routes exist but the caller lacks them, or says the scope or tool is unknown |
| `grain_invoke` | `tool` (route tool name `api_<handler>`; `api_` is added if missing), `arguments?` (path params by name, `query`, `body` or flat body fields, `expect`), `reason?` | Dispatches one route of `AgentAPIRoutes()` as an in-process subrequest (§4) | the route's response body verbatim on 2xx (a one-line `<METHOD> <path>: <status>` when the body is empty); `<api_tool> failed: HTTP <code>: <message>` as `isError` otherwise |
| `grain_filing_context` | `repo?` | Reads, straight from the store, what a caller needs before filing: target repositories, custom fields, templates, boards, grantable capabilities and `api:` presets, default capabilities/model/models/network. The tool as a whole needs at least one task, template, repo, board, settings or deployment read/write scope; within it, capabilities and the settings fields appear only with `capabilities:read` and `settings:read`, and templates and boards are filled only with their read scopes (templates with `templates:read` or `tasks:from-template`, boards with `boards:read`) | one JSON object |
| `grain_submit_task` | `title`, `description`, `repo` / `no_repo`, `base`, `approve` (default `false`), `template_id`, `fields`, `board_id`, `priority`, `interactive`, `auto_merge`, `capabilities`, `depends_on`, `reads`, `writes`, `custom_fields`, `network_access`, `model`, `reason` | `POST /api/tasks` with the arguments mapped to the route's body; `approved` is always sent so an unapproved call files a `proposed` task | compact `{id, url, title, state, approved, repo, base, interactive, heldBy?, approvalNote?}` |
| `grain_list_tasks` | `repo?`, `state?` (comma-separated), `q?` (title substring), `limit?` (default 50), `summary?` (default `true`) | `GET /api/tasks?fields=summary` (or `full`) | the route's JSON array: with `summary`, `{id, title, state, repo?, createdAt?, blockedBy?, stall?}` rows, newest `limit` by creation time, oldest first |
| `grain_get_task` | `id` (`42`, `#42`, `task-42`, a task branch or a full task URL all normalise), `view?` (`summary` default, `conversation`, `full`), `after?` | `GET /api/tasks/{id}`, then renders the chosen view | `summary`: ~1 KB plain text (state, priority, kind, repo, dependencies, merge queue, PR, last three events, handover head); `conversation`: up to 20 comments after `after`, bodies clipped at 2 KiB; `full`: the `TaskDetail` JSON verbatim |

A tool failure is a JSON-RPC *result* with `isError: true` — for the five tools that dispatch or read, text of the form `<tool> failed: <why>`; for `grain_explore_scope`, the sentence saying what did not match — not a JSON-RPC error, so the model sees it as a tool answer it can act on. Unknown tool names and malformed `tools/call` params are the only `-32602`s.

## 4. Authorization {#CM-4}

Authorization has two layers, and the second is the one that counts.

**Pre-check (`allowsRoute`).** `grain_explore_scope` and `grain_invoke` first ask whether the caller may see or call a route at all, from scope alone. An external agent needs the route's scope in its token, or `tasks:admin` for `PATCH /api/tasks/{id}`, and is kept off a task's own chat and console endpoints. An acting task needs the scope in its grants, or one of three narrow carve-outs (`tasks:admin` for `PATCH /api/tasks/{id}`, `releases:read` for release-notes reads, `grain#ci` for the CI reads it already opens); on the sandbox channel its grants are first narrowed to the sandbox set (scope sets that require a person are dropped), and it is also refused every sandbox-denied scope (every dangerous scope, `dispatch:write`, `repos:write`, `sessions:write`, `teams:broadcast`, and `deployment:*` other than reads) and the handful of model-only routes (posting to a task's own chat, parking and resuming). A person passes. This is what makes `grain_explore_scope` list only routes the caller can use.

**The gate (`agentAPIGate`).** Every subrequest — from `grain_invoke`, `grain_submit_task`, `grain_list_tasks` and `grain_get_task` alike — then enters the target route's own gate exactly as a direct HTTP call would, where repository limits, request-body fields, self-protection rails and the no-escalation rule for grants are enforced, the call runs under the agent principal, and the answer is redacted where the caller may mutate a task it may not read. `POST /api/mcp` itself has scope `internal` and passes through the gate untouched; the gate is on the routes behind it. Nothing reachable through the controller is reachable with less than a direct call needs.

Two conventions ride on top:

- **`reason`.** Every `DELETE` route and every route under a dangerous scope needs a non-empty reason before dispatch (`grain_invoke` takes it from `reason`, `arguments.reason` or `arguments.body.reason`); it travels as `X-Grain-Acting-Reason` and lands in the audit log and comments.
- **`expect`.** A mutation of `/api/tasks/{id}` may carry `arguments.expect` with any of `state`, `title_prefix`, `kind`. When set, `grain_invoke` reads the task first — as a gated `GET /api/tasks/{id}`, so the caller needs `tasks:read` on that task's repository — and refuses, changing nothing, if the record disagrees, reporting each field's recorded and expected values. Unknown keys are refused rather than ignored.

## 5. Attachment to runs {#CM-5}

At dispatch (`orchestrator.wireControllerMCP`), a task whose grants contain any `api:*` grant gets a loopback proxy for the run, served on `127.0.0.1:0`, handshaken with a 30 s timeout (a failed handshake fails dispatch), and appended to the run's capability servers as `grain-controller` with the six tool names. Harnesses then see it under their usual naming: `mcp__grain-controller__<tool>` (Claude), `mcp_grain-controller_<tool>` (Antigravity/Jetski), or an MCP server entry `grain-controller` (Codex, custom backends). A task with no `api:*` grant gets nothing attached; `grain mcpserver` (`grain-sandbox`) never serves these tools.

Because authority is re-read on every call, changing a running task's `api:*` grants needs no restart when the server is already attached: the next call sees the new set, and the run is told by a comment. The one case that restarts the run is a grant that only the controller can reach (a scope outside the sandbox set) added to a run that started with no `api:*` grant at all, since that run has no `grain-controller` to call.

## 6. Audit and telemetry {#CM-6}

Every agent subrequest is logged by the gate as `grain api: {agent, <principal>} <pattern> scope=… repo=… [channel=…] reason=… -> <status>`; a mutation of another task posts a comment on that task signed `grain`, with the reason; on the sandbox channel it is also recorded per run in the store. Person calls run as the person and are not logged by the gate. Configuration writes are attributed to the principal the handler ran as, not to the transport credential.

Run telemetry (`orchestrator.runTelemetryAt`) strips the harness prefixes and records a `grain_invoke` call under the route tool it invoked (`api_<handler>`), with the route arguments as the tool input, so the census sees routes, not the wrapper; the other five are recorded under their own names, and `grain_get_task`/`grain_list_tasks` count as reads. While a call is in flight the task's live activity line says what it is doing (*calling `api_…`*, *reading task 42*, *filing task "…"*).

## 7. Why this shape {#CM-7}

- **One endpoint, not one per caller kind.** Sessions, runs, external harnesses and people reach the same server and the same gate, so there is one place where authority is decided and one audit trail.
- **Six tools, not one per route.** A fixed roster costs the same context whatever the caller holds and never needs updating when a route is added; a new route is reachable the moment it is scoped. The price is a two-step discovery (`grain_explore_scope`, then `grain_invoke`) for anything outside the four everyday tools.
- **Authority per call, from the store.** No cached token to invalidate, no restart to pick up a grant, and the sandbox channel's ceiling is applied where the request arrives rather than where the model runs.
- **Tool failures as results.** A refusal or a 4xx is something the model should read and act on (ask for a grant, narrow the call), so it arrives as text, not as a protocol error that some harnesses hide.

## 8. Open questions {#CM-8}

These are the points of the surface still being iterated on; none changes §1–§6 as implemented.

1. **The route tool names.** `grain_invoke` addresses routes as `api_<handler>` — the HTTP route's handler name with method and path recovered from `AgentAPIRoutes()`. That exposes the controller's route table as the agent's vocabulary. The alternative is a vocabulary of verbs on resources (what the sandbox `grain` CLI already does) with the route table as an implementation detail behind it.
2. **`grain_filing_context` reads the store directly** rather than composing `GET /api/templates`, `GET /api/boards` and `GET /api/config` subrequests through the gate. It applies the same scopes by hand; whether that should stay a special case is open.
3. **Absent vs. `null`.** `capabilities`/`apiPresets` and the settings fields are omitted from `grain_filing_context`'s answer when the caller lacks the scope, while `templates` and `boards` are always present and `null` both when empty and when unauthorized.
4. **Two external endpoints.** An external `gma_…` token can call both `POST /api/mcp` and the older `POST /mcp`; which one `grain mcp` and Settings → CLI advertise, and whether `/mcp` stays, is open.
5. **`grain#ci` without an `api:*` grant.** The pre-check admits a task holding `grain#ci` to the CI reads, but a run with only that grant has no `grain-controller` attached, so it can reach them only by `grain api` from the sandbox.
6. **The `expect` mismatch hint** still tells the caller to re-read with `read_grain_task`, which is a `grain-sandbox` tool; a `grain-controller` caller has `grain_get_task`.
7. **No per-call log for people.** Person calls on `POST /api/mcp` emit neither a `grain api:` line nor a `tools/call` line.
