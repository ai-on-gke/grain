# The controller MCP server

**Owner:** @Dzianis-Harbatsenka
**Status:** implemented (PR #511) and in use; this design describes the model as built so it can be iterated on. The tool reference is in [`docs/agent-api.md`](../../docs/agent-api.md) ("The controller MCP server").
**Related:** [`sandbox-first-agent-surface.design.md`](sandbox-first-agent-surface.design.md) (the sandbox `grain` CLI; superseded in part by this), [`external-cli.design.md`](external-cli.design.md) (workstation agents and `grain mcp`), [`unified-grants.design.md`](unified-grants.design.md) (`api:*` grants).

**The core rule:** an agent that reaches the controller API through MCP gets one server, `grain-controller`, serving the same six tools to every caller. What a call may reach is decided when the call is made, from the caller's grants, never from the tool list.

A grain run, a session, and an engineer's workstation agent all need to ask the controller to do things: file a task, read one, retry CI, comment on a sibling, change a setting. The controller's HTTP API has a few hundred routes for that, each under a scope, each behind the same gate. The question this design answers is what a *model* should see of that API.

## What existed before

Each route had a tool. A run holding `api:*` grants got one MCP tool per route it could reach, served by `grain mcpserver` alongside the sandbox tools. That was faithful — every route, its own schema — and it had three problems:

- **Context.** A caller with a broad scope set loaded a few hundred tool schemas before it had done anything. The before/after measurement for PR #511 is in `docs/test-runs/2026-11-02-controller-mcp-token-cost.md`.
- **Churn.** Every new route meant a new tool, a new allow-list entry in each harness, and a new name for the census to learn.
- **Two surfaces.** External harnesses had `POST /mcp`, with the same one-tool-per-route shape, served separately from what runs and sessions got, with the authorization logic repeated.

`sandbox-first-agent-surface.design.md` moved most of a run's everyday calls into the sandbox `grain` CLI and proposed shrinking the model-only MCP roster to a small control set. That left open what the model-only channel should look like, and what sessions and external harnesses should get. This design is the answer that shipped.

## The model

**One endpoint.** The daemon serves one stateless MCP server at `POST /api/mcp`. It is stateless on purpose: one request, one answer, no session to keep alive, so a harness, a proxy, or a test can call it like any other API route, and a daemon restart loses nothing.

**Six tools, the same for everyone.** Four are the everyday task operations, shaped for a model rather than for a REST client: list tasks (compact rows), read one task (a one-kilobyte summary by default, the conversation paged, or the full record), read what you need before filing (repositories, templates, boards, fields, capabilities), and file a task. The other two are how everything else is reached: *explore* answers "what may I do here?" — the scope groups you hold, then the routes under a group or matching a word, with their input schemas — and *invoke* calls one of those routes. A caller pays for a route's schema only when it asks for it.

**Authority per call, not per connection.** The server does not care how a caller got in. Once the daemon's ordinary admission has passed (identity, access code, or an external agent token), each tool call works out who is calling and what they hold — from the store, every time — and then does for them exactly what a direct HTTP call would. Three kinds of caller come out of that:

- a **run or session** acting for its task, whose authority is the task's `api:*` grants;
- an **external agent** with a `gma_…` token, whose authority is the token's scopes and repository limits;
- a **person**, who may call any route.

The same server is reached three ways. A run with any `api:*` grant has `grain-controller` attached as a capability server beside `grain-sandbox`; the harness talks to a loopback proxy on the host that strips whatever headers the harness sent, stamps the task, and adds the daemon's local credential, so the model never holds one. A process inside the sandbox that reaches `/api/mcp` through the per-sandbox proxy arrives stamped with the task and the sandbox channel, and gets the sandbox's narrower ceiling. A workstation harness connects directly with its token or the person's identity.

**The gate is the gate.** Every call that touches data is an in-process subrequest to the route's own handler, through the route's own `agentAPIGate`: scopes, repository limits, body checks, the no-self-escalation rule for grants, response redaction, audit. The controller adds a cheap pre-check so that *explore* lists only routes the caller can use and *invoke* refuses early with a message naming the missing scope, but nothing is reachable through the controller with less than a direct call needs. Two things ride on top of the gate because they only make sense for a model: a `reason`, required on every delete and every dangerous-scope route and carried into the audit log and comments; and `expect`, a precondition on a task mutation (`state`, `title_prefix`, `kind`) that makes the call read the task first and refuse, changing nothing, if the record disagrees.

**Failures are answers.** A refusal, a validation error, or a 4xx from the route comes back as a tool *result* flagged `isError`, with text the model can act on (ask for `api:<scope>`, narrow the call, read the task again), not as a protocol error that some harnesses swallow. Oversized answers are cut in the middle with a note saying how much was dropped and which narrower call to make.

## How it is used

**A run reruns CI.** A task with `api:ci:read` and `api:ci:write` is asked to look at a failing check and run the suite again. Its harness has `grain-controller` in its server list. It calls `grain_explore_scope(scope="ci", tool="start_ci")`, gets one route and its schema, and calls `grain_invoke(tool="api_start_ci_run", arguments={body: {...}}, reason="rerun after flaky TestQuotaOffByOne")`. The gate runs the call as `{agent, <task>}`, the census records it under `api_start_ci_run`, and the task's activity line reads *calling api_start_ci_run* while it runs. If the task held only `api:ci:read`, the explore call would not have listed the route, and the invoke would have said so and named the scope.

**A session files follow-up work.** A session is asked to file three tasks. It calls `grain_filing_context()` once, sees the target repositories and the template it wants, and calls `grain_submit_task` three times with `template_id` and `fields`. `approve` defaults to false, so the tasks land as proposals unless the session holds `tasks:admin` and says otherwise.

**An engineer's agent checks on a task.** Jetski, with a `gma_…` token scoped to one repository, calls `grain_get_task(id="1840")` and gets the kilobyte summary; then `grain_get_task(id="1840", view="conversation")` to read the thread. Nothing in its token lets it see tasks in other repositories, and the gate, not the tool, enforces that.

**A grant changes mid-run.** An operator adds `api:merge:write` to a running task. Because authority is read per call, the next call sees it; the run is told by a comment and nothing restarts. The one exception is a run that started with no `api:*` grant at all — it has no `grain-controller` attached — given a grant that only the controller can reach; that run restarts so the server can be attached.

## Trade-offs

**Six fixed tools versus one per route.** Fixed tools cost the same context whatever the caller holds, never need a harness allow-list edit, and let a new route be used the moment it is scoped. The cost is a two-step dance (explore, then invoke) for anything outside the four everyday tools, and a model that has to learn that `grain_invoke` exists rather than seeing a tool named for what it wants. The `initialize` instructions teach the dance; the four everyday tools are there so the common cases never need it.

**Route names as the vocabulary.** `grain_invoke` addresses a route by `api_<handler>` — the HTTP handler's name, with method and path recovered from the route table. This was the cheapest thing that worked: the names already existed, the census already knew them, and *explore* makes them discoverable. But it means the agent's vocabulary is the controller's internal route table. A handler rename changes what a model has to say; two routes that do similar things for different resources have unrelated names; and nothing in `api_update_task` tells a model it is the same action the sandbox `grain` CLI performs with a verb. The alternatives are a vocabulary of verbs on resources (`task.update`, `ci.start`, the shape the sandbox CLI already has), with the route table as the implementation behind each verb; or a small set of per-area tools (`grain_task`, `grain_ci`, `grain_repo`) each taking an action. Both keep the fixed-roster property; both cost a mapping table that has to be maintained. This is the first open question below, and the one the owner of this design most wants to iterate on.

**Authority from the store on every call.** Simple and always right: no cached token to invalidate, no restart to pick up a grant, and the sandbox ceiling is applied where the request arrives, not where the model runs. The cost is a store read per call, which at the rate a model calls tools is nothing.

**`grain_filing_context` reads the store directly.** It is the one tool that does not compose subrequests; it applies the gate's scopes by hand to the parts that are gated, so a caller sees roughly what the matching routes would show. That was done for one round trip and one JSON object. It is also a second place that has to agree with the gate.

**Stateless.** No streaming, no progress, no server-initiated messages. For a controller API whose calls are short that has cost nothing so far; a long-running call (a wait for CI) would want the sandbox CLI's long-poll pattern instead.

## Security notes

- The controller adds no power. Every call lands on an existing scoped route through its own gate; the route-scope totality test covers it. `POST /api/mcp` itself is scope `internal` and carries no authority of its own.
- A run never holds a credential for the controller; the host-side proxy supplies the daemon's local token and strips anything the harness sent in `Authorization` or `X-Grain-*`.
- The sandbox channel's ceiling (no dangerous scopes, no deployment writes, none of the model-only scopes such as dispatch, repository and session writes, no person-only scope sets) is applied by the proxy stamping the channel and by the gate reading it, not by anything the model can influence.
- `reason` is audit text, not authorization; `expect` is a convenience for the model, not a lock. Both are recorded.

## Open questions

1. **The vocabulary.** Should `grain_invoke` keep addressing routes by `api_<handler>`, or should the controller expose verbs on resources (or per-area tools) with the route table behind them? See *Trade-offs*.
2. **`grain_filing_context`.** Keep the direct store read, or compose it from the templates, boards and config routes through the gate? And should unauthorized parts be absent (as capabilities are today) or `null` (as templates and boards are)?
3. **`grain#ci` alone.** A task holding only the CI capability may read CI through the controller's pre-check, but has no `grain-controller` attached because it holds no `api:*` grant. Should the CI capability count as one?
4. **People.** Person calls on `POST /api/mcp` are not logged per call the way agent calls are. Should they be?
5. **Hints.** The `expect` mismatch message still tells the caller to re-read with `read_grain_task`, a `grain-sandbox` tool; a `grain-controller` caller has `grain_get_task`.

## Follow-up work

### Retire the per-route MCP roster at `POST /mcp`

The controller server replaced the per-route `api_*` roster for runs and sessions, but the older external endpoint `POST /mcp` still serves that roster to `gma_…` tokens beside it. That leaves two external MCP URLs with different tool lists, which is the "two surfaces" problem this design set out to remove. The decision is to finish it: **one public MCP entry, serving the six-tool server to every caller kind, and the per-route roster gone.**

The public URL is `/mcp`, because it is the path harness documentation assumes and the one people will type into a client config; `/api/mcp` stays as an alias, because the host-side proxy and the sandbox git proxy already reach it under `/api/` and need not change. Concretely:

1. **Serve the controller server at `POST /mcp`.** Point the route at the same handler as `POST /api/mcp`. Admission is unchanged — `requiresAccess` already gates `/mcp`, and the `externalAgentApi` switch keeps gating `gma_` tokens — so the endpoint admits every principal the auth layer produces and resolves it to external, acting task or person exactly as `/api/mcp` does. The handler's current refusal of anything that is not a token goes away with the handler.
2. **Delete the per-route roster.** `ExternalAgentAPITools` and the per-request registry in `handleMCP` are removed; `APIRoute.externalTool` / `externalCall` go with them, since `grain_invoke` dispatches the same routes through the same gate.
3. **Keep `/api/mcp` as an alias** of the same handler for the proxied channels and for anything already configured against it. Nothing advertises it.
4. **Deprecation window: one release.** `externalAgentApi` is off by default and no CLI, page or skill file advertises `/mcp` today, so the window is short. During it, a `gma_` token that sends `X-Grain-MCP-Roster: routes` gets the old per-route roster with a deprecation line in the daemon's log; without the header it gets the six tools. After the release the header is ignored.
5. **Advertise `/mcp`** as the MCP URL when `grain mcp`, Settings → CLI and the skill files land (`external-cli.design.md`, *Follow-up work* 8–9), and in `docs/agent-api.md`'s *External agents* section, which today describes both endpoints.
6. **Tests.** The external-agent tests that call `api_*` tools over `/mcp` move to `grain_invoke` (same assertions); the route-authorization table keeps `POST /mcp` at scope `internal`; a test asserts `/mcp` and `/api/mcp` return the same `tools/list` to the same caller.

What this does not touch: `grain-sandbox` (`grain mcpserver`), whose tools execute inside a run's sandbox and stay a per-run stdio server; the in-sandbox `grain` CLI, which is a REST client of the same gate on the sandbox channel; and the REST API itself, of which the controller server is a projection.

## Where the code is today

Everything in *The model* and *How it is used* is implemented: the endpoint and six tools in `pkg/ui/mcp_endpoint.go`; the gate in `pkg/ui/agent_api_gate.go`; attachment to runs in `pkg/orchestrator/capability_endpoints.go` and the host proxy in `pkg/daemon/ui_server.go`; grant changes in `pkg/orchestrator/capability_restart.go`; the census and activity line in `pkg/orchestrator/census.go` and `pkg/mcp`. `grain mcpserver` no longer serves any `api_*` tool. `POST /mcp` still serves the per-route roster to external tokens (`pkg/ui/external_agents.go`, `handleMCP`) until the follow-up above lands. The open questions are the rest of the gap between this design and `main`.
