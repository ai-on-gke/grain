# Controller MCP server specification

**Owner:** @Dzianis-Harbatsenka
**Status:** draft for review. Normative.
**Specifies:** [`sandbox-first-agent-surface.design.md`](../../design/sandbox-first-agent-surface.design.md), [`external-cli.design.md`](../../design/external-cli.design.md), [`unified-grants.design.md`](../../design/unified-grants.design.md)
**Describes:** the `grain-controller` MCP server at `POST /api/mcp` (`pkg/ui/mcp_endpoint.go`), its authentication and authorization rules, the six tools it exposes, subrequest dispatch and audit (`pkg/ui/agent_api_gate.go`), run attachment (`pkg/orchestrator/capability_endpoints.go`, `pkg/daemon/ui_server.go`), and run telemetry (`pkg/orchestrator/census.go`).
**Related:** [`docs/agent-api.md`](../../../docs/agent-api.md) (scope vocabulary and route rules), [`capabilities.spec.md`](../capabilities/capabilities.spec.md) (capability MCP servers and `protocol.Pass`), [`external-cli.spec.md`](../external-cli/external-cli.spec.md) (external callers and credentials), [`docs/access-code.md`](../../../docs/access-code.md).

The key words **MUST**, **MUST NOT**, **SHOULD**, **SHOULD NOT** and **MAY** are to be interpreted as described in BCP 14 (RFC 2119, RFC 8174) when, and only when, they appear in all capitals.

Where this document and `main` differ or leave a surface choice open, §9 records the item.

## Contents

1. Terms
2. Endpoint and transport
3. Callers and authentication
4. Authorization model
5. Tools
6. Subrequest dispatch, attribution and audit
7. Attachment to runs and sessions
8. Telemetry and live activity
9. Open questions

---

## 1. Terms {#CM-1}

- **Controller MCP server**, **`grain-controller`**: the stateless MCP server specified by this document. `grain-controller` (`mcp.ControllerToolNamespace`) is both its MCP `serverInfo.name` and the capability server name attached to runs.
- **Route**: an entry in `ui.AgentAPIRoutes()` (`pkg/ui/agent_api_tools.go`), covering every route in `pkg/ui/route_scopes.go` with a non-empty scope other than `internal`. Each route has:
  - a **route tool name** `api_<handler>` (`APIRoute.Tool`);
  - an HTTP `method` and `path` (with `{param}` or `{param...}` placeholders) forming `pattern` (`<METHOD> <path>`);
  - an `APIScope` (`scope`);
  - a `description` and JSON Schema `inputSchema`;
  - `needsReason` (`true` for every `DELETE` route and every route under a dangerous scope).
- **Scope**, **scope group**, **preset**, **scope set**: an `APIScope` is `<area>:<level>` (for example `tasks:read`, `merge:force`); its scope group is `<area>`. Built-in presets (`task-admin`, `ci-read`, …) and declared scope sets (`tables/api_scope_set.json`) expand to sets of scopes as defined in `pkg/model/agent_api.go`.
- **Caller**: the principal resolved for an individual request to `POST /api/mcp` (§3.2): an **external agent**, an **acting task**, or a **person**.
- **Subrequest**: an in-process HTTP request dispatched through `ui.Server`'s route mux on the caller's behalf (`dispatchSubrequest`, §6.1).
- **Control headers**:
  - `X-Grain-Access-Code` (`ui.AccessCodeHeader`): deployment access code credential when `Authorization` is unavailable or owned by an upstream proxy.
  - `X-Grain-Acting-Task` (`ui.ActingTaskHeader`): task ID whose grants bound an agent caller's authority.
  - `X-Grain-Acting-Run` (`ui.ActingRunHeader`): run ID set by the per-sandbox HTTP proxy.
  - `X-Grain-Channel` (`ui.ChannelHeader`): request channel; `sandbox-cli` (`ui.SandboxCLIChannel`) identifies calls forwarded from inside a sandbox.
  - `X-Grain-Acting-Reason` (`ui.ActingReasonHeader`): audit reason attached to a subrequest.

## 2. Endpoint and transport

### 2.1 Endpoint {#CM-2}

1. The server **MUST** be mounted at `POST /api/mcp` on `ui.Server` with route scope `internal`.
2. Any HTTP method other than `POST` on `/api/mcp` is answered by `ui.Server.apiFallback` with `405 Method Not Allowed` and JSON body `{"error": "<METHOD> is not allowed on /api/mcp"}`.
3. `grain mcpserver` (`grain-sandbox`) **MUST NOT** register any of the six tools defined in §5.

### 2.2 Wire protocol {#CM-3}

1. `POST /api/mcp` speaks MCP over stateless Streamable HTTP (`mcp.StatelessHTTPHandler`): one JSON-RPC 2.0 message per HTTP `POST`, synchronous `application/json` response, no `Mcp-Session-Id` state, and no server-sent event stream. Every response **MUST** carry `Cache-Control: no-store`.
2. `Content-Type` **MUST** have media type `application/json` (parameters after `;` ignored, case-insensitive); any other media type answers `415 Unsupported Media Type`.
3. The request body **MUST NOT** exceed `4 MiB` (`4,194,304` bytes); a larger body answers `413 Request Entity Too Large`.
4. A body that fails JSON decoding, lacks `"jsonrpc": "2.0"`, or has an empty `method` answers `400 Bad Request`.
5. A JSON-RPC notification (absent `id` or `id: null`) **MUST** be answered `202 Accepted` with an empty body.
6. Supported JSON-RPC request methods:
   - `initialize`: returns:
     ```json
     {
       "protocolVersion": "2025-03-26",
       "capabilities": {"tools": {}},
       "serverInfo": {"name": "grain-controller", "version": "0.1.0"},
       "instructions": "<controllerMCPInstructions>"
     }
     ```
     `protocolVersion` **MUST** echo `"2024-11-05"` when `params.protocolVersion` is `"2024-11-05"` and `"2025-03-26"` otherwise. `instructions` names the four dedicated task tools, the `grain_explore_scope` → `grain_invoke` discovery sequence, `reason` requirements, `arguments.expect`, and scope refusal handling.
   - `notifications/initialized`: handled as a notification (`202 Accepted`).
   - `ping`: returns `{}`.
   - `tools/list`: returns `{"tools": [...]}` containing the six tools in the exact order of §5, each with `name`, `description`, `inputSchema`, and `_meta: {"antigravity/alwaysLoad": true}`. The returned list **MUST** be identical for all callers.
   - `tools/call`: invokes one of the six tools. Unknown `params.name` or malformed `params` returns JSON-RPC error `-32602`.
   - Any other `method` with an `id` returns JSON-RPC error `-32601` (`unknown method <method>`).
7. Tool-level failures (authorization refusals, validation errors, non-2xx subrequests) **MUST** return a JSON-RPC `result` (HTTP `200`) with `isError: true`:
   ```json
   {
     "content": [{"type": "text", "text": "<tool> failed: <message>"}],
     "isError": true
   }
   ```
   `<tool>` is the controller tool name, except in `grain_invoke` once `params.arguments.tool` resolves to a known `APIRoute`, where `<tool>` is that route's `api_<handler>` name.
8. Every tool's text output passes through `pkg/mcp`'s result-size cap (`maxResultTextBytes()`, `20 KiB` / `20,480` bytes at the default `16 KiB` `maxToolResultBytes`). Output exceeding `20 KiB` is elided in the middle—snapping each cut to a line boundary when a newline exists within that half, or cutting at the raw half-budget byte offsets (`limit/2` and `len(text)-(limit-limit/2)`) when a half contains no newline (retaining both head and tail)—and inserting (with `<dropped>` and `<total>` formatted via `humanBytes`):
   ```text
   [grain] <dropped> of <total> elided here by grain, not by the command -- a tool result is capped so one answer cannot eat the run's context. To see it: <advice>.
   ```

## 3. Callers and authentication

### 3.1 Request admission {#CM-4}

1. `POST /api/mcp` sits behind `ui.Server`'s standard `/api/` admission gates (`requiresAccess`). A request is admitted by any of:
   - a verified IAP / OIDC user identity;
   - the deployment access code in `Authorization: Bearer <code>` (when the bearer token matches access-code syntax);
   - the deployment access code in `X-Grain-Access-Code: <code>`;
   - a valid access-session cookie issued by access-code login;
   - an external agent bearer token (`Authorization: Bearer gma_<hex>`) when `externalAgentApi` is enabled on the deployment.
2. When `-ui-require-iap` is set, access codes (via `Authorization: Bearer` or `X-Grain-Access-Code`) admit only un-relayed loopback requests or requests for which the identity verifier sets `accessCodeWins`.
3. When an access code is configured and a candidate code is presented (`X-Grain-Access-Code` or access-code-shaped `Authorization: Bearer`), non-loopback (or relayed) requests **MUST** first check the per-source rate-limit bucket (`sourceLimited`) before comparison, refusing exhausted sources with `429 Too Many Requests` regardless of whether the code is valid; un-relayed loopback requests are exempt from this pre-check (`accessLimitExempt`) so valid local calls are never throttled. Wrong codes on any request (including loopback) deduct from both the per-source and global buckets (applying a 1-minute per-source penalty when the global bucket has `< 1` token) and answer `429 Too Many Requests` once the source bucket drops below `1`.
4. `X-Grain-Access-Code` **MUST** be redacted by `pkg/redact` and stripped on outbound hops alongside `Authorization` (`/staging/` reverse proxy; `protocol.Proxy` header allow-list).

### 3.2 Caller resolution {#CM-5}

`grain_explore_scope`, `grain_invoke`, and `grain_filing_context` resolve caller access per call (`resolveMCPCallerAccess`) with no caching across calls. `grain_submit_task`, `grain_list_tasks`, and `grain_get_task` dispatch directly through `dispatchSubrequest`, where `agentAPIGate` resolves the same three caller kinds in the same precedence order:

1. **External agent**: the request carries a verified `ExternalAgentToken` in context. Authority is `model.ResolveExternalAgentGrants(tok.ScopeSet, tok.Scopes, tok.Repos)` with repository limits `tok.Repos`. Any `X-Grain-Acting-Task` header on the request **MUST** be ignored.
2. **Acting task**: `X-Grain-Acting-Task` is non-empty. The server loads the task from the store on each call (`Store.GetTask`); if the task is missing or unreadable, the call fails (`acting task <id> could not be read: …`). Authority is derived from `Task.Grants`:
   - when `X-Grain-Channel: sandbox-cli` is present: `model.SandboxAPIGrantsOf(grants)` (excluding `RequiresPerson` scope sets and sandbox-denied scopes);
   - otherwise: `model.APIGrantsOf(grants)` (including `api:*` grants and legacy API alias capabilities).
3. **Person**: neither an external agent token nor `X-Grain-Acting-Task` is present. The caller has `fullAccess: true`.

## 4. Authorization model

### 4.1 Route pre-check (`allowsRoute`) {#CM-6}

`grain_explore_scope` and `grain_invoke` evaluate `allowsRoute(route)` before listing or dispatching `route`:

1. **External agent**: `route.Pattern` is not in `noTaskContextRoutes` (a task's own chat, session title, live console endpoints, and standing console session endpoints), and either `grantSet.Has(route.Scope)` or (`grantSet.Has("tasks:admin")` and `route.Pattern == "PATCH /api/tasks/{id}"`).
2. **Acting task**:
   - If `channel == "sandbox-cli"`, `route.Scope` **MUST NOT** be in `model.IsSandboxDeniedAPIScope` (every dangerous scope, `dispatch:write`, `repos:write`, `sessions:write`, `teams:broadcast`, and `deployment:*` except `deployment:read`), and `sandboxChannelGuard(route.Pattern, nil)` **MUST** succeed.
   - On any channel, at least one of the following **MUST** hold:
     - `grantSet.Has(route.Scope)`;
     - `grantSet.Has("tasks:admin")` and `route.Pattern == "PATCH /api/tasks/{id}"`;
     - `grantSet.Has("releases:read")` and `route` is a `release-notes:read` route under `/api/release-notes` (`agentCapabilityAllowsRoute`);
     - `model.HasGrainCI(heldGrants)` holds (matching `https://grain.dev/capabilities/grain#ci` with or without `={...}` arguments, or the retired bare alias `grain-ci`) and `route.Pattern` is in `grainCIReadRoutes`.
3. **Person**: `true` for every route in `AgentAPIRoutes()` (`route.Scope != ""` and `route.Scope != "internal"`).

`allowsRoute` is a body- and repository-independent pre-check. Every subrequest dispatched by `grain_invoke`, `grain_submit_task`, `grain_list_tasks`, or `grain_get_task` is evaluated in full—including repository limits, request body fields, self-protection rails, and filing-escalation rules—by `agentAPIGate` (§6.2).

### 4.2 Audit reason {#CM-7}

1. A route with `needsReason == true` **MUST** be refused before dispatch if no non-empty reason is supplied.
2. `grain_invoke` resolves `reason` from the first non-empty trimmed string among: top-level `reason`, `arguments.reason`, `arguments.body.reason`.
3. `dispatchSubrequest` sets `X-Grain-Acting-Reason` to the trimmed reason when non-empty, and deletes `X-Grain-Acting-Reason` from the inner request when empty.

### 4.3 Task state expectation (`expect`) {#CM-8}

1. Any route with `method != "GET"`, `path` prefixed `/api/tasks/{id}`, and path parameter `id` (`APIRoute.takesExpect()`) accepts `arguments.expect`.
2. `arguments.expect`, when non-null, **MUST** be a JSON object whose keys are drawn only from `state`, `title_prefix`, and `kind`; any other type or unknown key fails before dispatch.
3. Each provided non-empty string field specifies a precondition (`TaskExpectation`, `pkg/ui/task_kinds.go`):
   - `state`: case-insensitive match against `Task.State`;
   - `title_prefix`: case-insensitive prefix match against `Task.Title`;
   - `kind`: case-insensitive membership in `TaskKindTags(task)` (`port`, `repair`, `session`, `proposal`, `durable`, `imported`), or `"none"` requiring zero kind tags.
4. When at least one field is non-empty, `grain_invoke` **MUST** first dispatch `GET /api/tasks/{id}` as a subrequest (requiring `tasks:read` on that task's repository for agent callers) and run `expect.Check`. On mismatch, `grain_invoke` fails without dispatching the mutation, reporting every disagreeing field with both recorded and expected values.

### 4.4 `grain_filing_context` authorization {#CM-9}

`grain_filing_context` reads from `ui.Server`'s store rather than dispatching subrequests:

1. **Entry gate**: the caller **MUST** have `fullAccess` or hold at least one of `tasks:write`, `tasks:from-template`, `tasks:read`, `tasks:admin`, `repos:read`, `templates:read`, `boards:read`, `settings:read`, `deployment:read`. Otherwise the tool fails with `grain_filing_context failed: HTTP 403: caller holds no task, template, repo, or deployment read/write scope`.
2. **Per-field visibility**:
   - `targetRepos`, `customFields`, `submitDefaults`: always present (`targetRepos` is `null` when empty).
   - `templates`: populated when `reads("templates:read") || reads("tasks:from-template")`; `null` when empty or when the caller holds neither scope.
   - `boards`: populated when `reads("boards:read")`; `null` when empty or when the caller lacks `boards:read`.
   - `capabilities`, `apiPresets`: keys included only when `reads("capabilities:read")`; omitted otherwise.
   - `defaultCapabilities`, `defaultModel`, `models`, `defaultNetwork`: keys included only when `reads("settings:read")` (and config is present); omitted otherwise.
3. **Repository filtering**: for an external agent with `repoLimits`, `targetRepos` and repo-bound `templates` are filtered case-insensitively to `repoLimits` (global templates with empty `repo` remain visible).

## 5. Tools

`tools/list` advertises the following six tools in order. All `inputSchema` definitions have `"type": "object"`; properties are optional unless listed in `required`. JSON tool outputs are serialized compactly without indentation.

### 5.1 `grain_explore_scope` {#CM-10}

Lists authorized scope groups or returns route descriptions and JSON input schemas for matching authorized routes.

**Arguments:**

| Property | Type | Required | Description |
|---|---|---|---|
| `scope` | `string` | no | Scope group (`"tasks"`), exact scope (`"ci:read"`), built-in preset (`"task-admin"`), or declared scope set (`"ops/triage"`). Case-insensitive; leading `"api:"` is stripped. |
| `tool` | `string` | no | Route tool name (`"api_add_comment"` or `"add_comment"`) or case-insensitive substring matched against route tool names and paths (`"comment"`). |

**Behavior and output:**

1. **Overview mode** (`scope` and `tool` both omitted or whitespace-only):
   - If the caller has zero authorized routes, returns plain text (not `isError`): `"No Grain controller API scopes are currently authorized for this caller."` (plus an acting-task grant hint when `actingTaskID != ""`).
   - Otherwise returns JSON:
     ```json
     {
       "totalAuthorizedRoutes": 42,
       "hint": "Call grain_explore_scope(scope='<group>', tool='<word>') to view matching route tool names and JSON input schemas (scope alone lists a whole group, which is much larger), then grain_invoke(tool, arguments, reason?) to execute.",
       "groups": [
         {
           "group": "tasks",
           "guidance": "Task lifecycle & mutations. ...",
           "totalRoutes": 31,
           "scopes": [
             {
               "scope": "tasks:read",
               "description": "read tasks, conversations, transcripts, trees",
               "dangerous": false,
               "routeCount": 12
             }
           ]
         }
       ]
     }
     ```
     (`guidance`, `description`, and `dangerous` carry `omitempty` and are omitted when empty/`false`. Groups and scopes are sorted lexicographically.)
2. **Detail mode** (`scope` and/or `tool` non-empty):
   - Filters `authorizedRoutes()` to routes matching `scope` (when given: exact scope, scope group, or scope in `ExpandAPIPreset(scope)`) and `tool` (when given: exact tool, `"api_" + tool`, or substring of `rt.Tool` or `rt.Path`).
   - When at least one authorized route matches, returns JSON:
     ```json
     {
       "scope": "tasks",
       "tool": "comment",
       "guidance": "Task lifecycle & mutations. ...",
       "routeCount": 1,
       "routes": [
         {
           "tool": "api_add_comment",
           "method": "POST",
           "path": "/api/tasks/{id}/comments",
           "scope": "tasks:write",
           "needsReason": false,
           "description": "...",
           "inputSchema": {
             "type": "object",
             "properties": {
               "id": {"type": "string", "description": "The {id} of /api/tasks/{id}/comments"},
               "reason": {"type": "string", "description": "..."},
               "query": {"type": "object", "...": "..."},
               "body": {"type": "object", "properties": {"...": "..."}},
               "expect": {"type": "object", "properties": {"state": {}, "title_prefix": {}, "kind": {}}}
             },
             "required": ["id"]
           }
         }
       ]
     }
     ```
     (`scope` is `""` when only `tool` was passed; `tool`, `guidance`, and `needsReason` are omitted when empty/`false`; `body` is present only on non-`GET` routes with a declared body struct; `expect` is present only when `takesExpect()` is true; `reason` is in `required` when `needsReason` is true.)
   - When zero authorized routes match, returns `isError: true`:
     - if `AgentAPIRoutes()` has matching routes whose scopes the caller lacks: `<What> is not authorized for this caller (requires grant for <scopes>).`;
     - else if `tool` was given: `No authorized route matches tool "<tool>". Call grain_explore_scope with a scope and no tool to list a group's routes.`;
     - else: `Unknown scope "<scope>". Call grain_explore_scope with no arguments to list available scope groups.`

### 5.2 `grain_invoke` {#CM-11}

Executes one route from `AgentAPIRoutes()`.

**Arguments:**

| Property | Type | Required | Description |
|---|---|---|---|
| `tool` | `string` | **yes** | Route tool name from `grain_explore_scope` (leading `"api_"` added if omitted). |
| `arguments` | `object` | no | Route call inputs: required `{param}` path values by name; `query` (`object`, `null` entries skipped, scalars formatted with `fmt.Sprint`); `body` (`object`) or flat top-level body fields when the route declares a request body; `expect` (§4.3). |
| `reason` | `string` | conditional | Audit reason; required when `route.NeedsReason` is true (§4.2). |

**Behavior and output:**

1. Validation order:
   1. `tool` non-empty and matches an `APIRoute` in `AgentAPIRoutes()` (otherwise fails naming `grain_explore_scope`);
   2. `resolveMCPCallerAccess` and `allowsRoute` (§4.1; otherwise fails with `<api_tool> failed: HTTP 403: caller holds no grant for scope <scope> (<pattern>)`);
   3. `reason` non-empty when `route.NeedsReason` is true (§4.2);
   4. `route.buildCall(arguments)`: every path parameter present and non-empty after trimming (non-collection scalars accepted as strings) and `url.PathEscape`d into `path`; `query` encoded; for non-`GET` routes, `arguments.body` (or non-reserved fields when the route declares a body type) JSON-encoded; `GET` routes never send a body;
   5. `checkExpectFields(arguments)` and, if non-empty on a `takesExpect()` route, `GET /api/tasks/{id}` subrequest + `expect.Check` (§4.3);
   6. `dispatchSubrequest` (§6.1).
2. On 2xx:
   - non-empty body: returned verbatim as text (after any `agentAPIGate` response redaction, such as `redactAgentTaskResponse` when an acting task mutates a task without `tasks:read`);
   - whitespace-only/empty body: `"<METHOD> <path>: <code> <status text>"`.
3. On non-2xx: returns `isError: true` with `"<api_tool> failed: HTTP <code>: <message>"`. When the response body decodes as `{"error": "<msg>"}` with an empty `code` field, `<message>` is `<msg>`; otherwise `<message>` is the trimmed response body (preserving structured refusals such as `{"error": "...", "code": "config_managed_externally", "patch": "..."}`), or `http.StatusText(code)` when empty.
4. Elision advice: `"narrow the route call with query parameters, or read one item by its id"`.

### 5.3 `grain_filing_context` {#CM-12}

Returns repository, template, board, custom-field, capability, and model metadata used to construct a `grain_submit_task` call.

**Arguments:**

| Property | Type | Required | Description |
|---|---|---|---|
| `repo` | `string` | no | Case-insensitive `owner/name` filter applied to `targetRepos` and repo-scoped `templates` (templates with empty `repo` are retained). |

**Output shape** (subject to §4.4 visibility rules):

```json
{
  "targetRepos": ["ai-on-gke/grain"],
  "customFields": [{"name": "...", "type": "...", "...": "..."}],
  "templates": [
    {
      "id": "bugfix",
      "name": "Bug fix",
      "description": "...",
      "repo": "ai-on-gke/grain",
      "fields": [
        {"name": "symptom", "label": "Symptom", "required": true, "options": ["..."]}
      ]
    }
  ],
  "boards": [{"id": "default", "name": "Main"}],
  "capabilities": ["gemini-key", "self-debug"],
  "apiPresets": ["api:ci-read", "api:task-admin"],
  "defaultCapabilities": ["gemini-key"],
  "defaultModel": "claude-sonnet-4-6",
  "models": ["claude-sonnet-4-6", "gemini-2.5-pro"],
  "defaultNetwork": "none",
  "submitDefaults": {
    "approve": false,
    "note": "grain_submit_task defaults approve=false so tasks land in 'proposed' state awaiting human approval unless approve=true is explicitly passed."
  }
}
```

(Template properties `description`, `repo`, `interactive`, and `fields`, and field properties `label`, `required`, `default`, and `options`, carry `omitempty` and are omitted when empty/`false`.)

### 5.4 `grain_submit_task` {#CM-13}

Files a task by dispatching `POST /api/tasks`.

**Arguments and request-body mapping:**

| Property | Type | `POST /api/tasks` mapping |
|---|---|---|
| `title` | `string` | `title` (sent when string) |
| `description` | `string` | `description` (sent when string) |
| `repo` | `string` | `repo` (trimmed; omitted when empty) |
| `no_repo` | `boolean` | `noRepo: true` when `true`; omitted otherwise |
| `base` | `string` | `base` (trimmed; omitted when empty) |
| `approve` | `boolean` | `approved` (**always sent**; defaults to `false`) |
| `template_id` | `string` | `templateId` (trimmed; omitted when empty) |
| `fields` | `object` | `fields` (`map[string]string`; `null` values dropped, others formatted via `fmt.Sprint`; omitted when empty) |
| `board_id` | `string` | `boardId` (trimmed; omitted when empty) |
| `priority` | `integer` | `priority` (accepts integer or integer string) |
| `interactive` | `boolean` | `interactive: true` when `true`; omitted otherwise |
| `auto_merge` | `boolean` | `autoMerge: true` when `true`; `declineAutoMerge: true` when `false`; both omitted when unset |
| `capabilities` | `string[]` | `capabilities` |
| `depends_on` | `string[]` | `dependsOn` |
| `reads` | `string[]` | `reads` |
| `writes` | `object[]` | `writes` (`{repo, base}` entries passed through) |
| `custom_fields` | `object` | `customFields` (omitted when empty) |
| `network_access` | `string` | `networkAccess` (trimmed; omitted when empty) |
| `model` | `string` | `model` (trimmed; omitted when empty) |
| `reason` | `string` | `X-Grain-Acting-Reason` header on the subrequest |

**Output:**

When `POST /api/tasks` returns a JSON task record with a non-empty `id`, `grain_submit_task` returns the compact projection:

```json
{
  "id": "42",
  "url": "/tasks/42",
  "title": "Fix flaky proxy test",
  "state": "proposed",
  "approved": false,
  "repo": "ai-on-gke/grain",
  "base": "main",
  "interactive": false,
  "heldBy": "...",
  "approvalNote": "..."
}
```

(`heldBy` and `approvalNote` are included only when non-empty. If the response does not decode to a task with an `id`, the subrequest body is returned verbatim.)

### 5.5 `grain_list_tasks` {#CM-14}

Lists tasks by dispatching `GET /api/tasks`.

**Arguments:**

| Property | Type | Default | Query parameter on `GET /api/tasks` |
|---|---|---|---|
| `repo` | `string` | omitted | `repo` (trimmed, when non-empty) |
| `state` | `string` | omitted | `state` (comma-separated states, when non-empty) |
| `q` | `string` | omitted | `q` (case-insensitive title substring, when non-empty) |
| `limit` | `integer` | `50` | `limit` (`50` when omitted or `<= 0`) |
| `summary` | `boolean` | `true` | `fields=summary` when `true`; `fields=full` when `false` |

**Output:**

Returns the `GET /api/tasks` JSON array verbatim. With `summary: true`, each element has the `taskListSummary` shape `{"id", "title", "state", "repo"?, "createdAt"?, "blockedBy"?, "stall"?}`, selecting the newest `limit` matching tasks by creation time, ordered oldest-first.

Elision advice: `"filter with state, repo, q, or a smaller limit, then inspect one task with grain_get_task"`.

### 5.6 `grain_get_task` {#CM-15}

Reads one task via `GET /api/tasks/{id}` and formats it according to `view`.

**Arguments:**

| Property | Type | Required | Default | Description |
|---|---|---|---|---|
| `id` | `string` | **yes** | — | Task identifier (string or scalar; `#42`, `/tasks/42`, task URLs, and task branch names are normalized via `model.NormalizeTaskID`). |
| `view` | `string` | no | `"summary"` | One of `"summary"`, `"conversation"`, `"full"` (case-insensitive). Any other value fails with `unknown view`. |
| `after` | `integer` | no | `-1` | Used when `view == "conversation"`: returns comments with `ID > after`. |

**Output by `view`:**

1. `"summary"` (`mcp.RenderTaskSummary`, plain text):
   - `Task <id>: <title>`
   - `State` (plus indented `Stalled: <stall>` when non-empty), `Priority` (`5` when `<= 0`), `Kind` (comma-joined kind tags or `none`), `Repo` (with base branch, when `repo` is non-empty), `Depends on` (adding `(still open: <blockedBy>)` when `blockedBy` is non-empty), `Merge queue` (when `mergeQueue.state` is non-empty on a non-`closed` task), `Pull request` (when non-empty), `Last events:` (up to the three latest entries merged across attempts, `closed`/`reopened` transitions with non-empty `by`/`reason`/`detail`, and comments/handovers, each with an RFC 3339 UTC timestamp), `Handover (head):` (up to four lines / 300 bytes of the latest handover comment when present), and a closing hint naming `view "full"` and `"conversation"`.
2. `"conversation"` (`mcp.RenderTaskConversation`, plain text):
   - Header: `Task <id> conversation[ after comment #<after>] (<totalAfter> comment(s)):`, where `<totalAfter>` is the count of comments with `ID > after` before paging.
   - Up to 20 comments formatted as `#<id> [<RFC 3339>] <author> (<authorKind>[, handover]):` with each comment body clipped at `2 KiB`.
   - Footer: `More follow: read again with after <lastShownID>.` when more than 20 matched, or `That is the end of the thread.` otherwise.
3. `"full"`: raw `TaskDetail` JSON body from `GET /api/tasks/{id}` verbatim.

Elision advice: `"use view='summary' or page comments with view='conversation' and after"`.

## 6. Subrequest dispatch, attribution and audit

### 6.1 `dispatchSubrequest` {#CM-16}

Every REST call performed by `grain_invoke`, `grain_submit_task`, `grain_list_tasks`, or `grain_get_task` runs in-process against `ui.Server`'s route mux (`s.mux.ServeHTTP`):

1. The inner `http.Request` **MUST** inherit from the outer `POST /api/mcp` request:
   - `Context()` (preserving verified principal, authentication method, external token, and caller verification), wrapped in a fresh `configWriteRehearsal`;
   - `RemoteAddr`, `Host` (when non-empty), and `TLS` (never synthesizing an empty `RemoteAddr` for a caller that had one);
   - all HTTP headers except `Content-Length`, `Content-Type`, `Accept`, `If-None-Match`, `If-Modified-Since`, `Mcp-Session-Id`, `Mcp-Protocol-Version`, and `Last-Event-Id` (preserving `Authorization`, `X-Grain-Access-Code`, `Cookie`, `X-Grain-Acting-Task`, `X-Grain-Acting-Run`, `X-Grain-Channel`, and identity-proxy headers).
2. The inner request sets `Accept: application/json`, sets `Content-Type: application/json` when a body is present, and sets or deletes `X-Grain-Acting-Reason` per §4.2.

### 6.2 Route gating {#CM-17}

1. `POST /api/mcp` has route scope `internal` and passes through `agentAPIGate` as an ungated transport endpoint.
2. Every inner subrequest enters the target route's `agentAPIGate`, which enforces:
   - external agent token scope, `noTaskContextRoutes`, and repository limits;
   - acting-task scope, repository resolution (path, target task, body, query), `sandbox-cli` channel restrictions, self-protection rails, and filing/grant non-escalation rules;
   - acting-task task-mutation response redaction (`redactAgentTaskResponse` when the task lacks `tasks:read` on the target repository);
   - unrestricted route access for person callers.

### 6.3 Attribution and audit {#CM-18}

1. **Principal**: handlers execute under `{agent, <task id>}` for an acting task, `{agent, external:<name>}` for an external agent token, or the authenticated person principal otherwise.
2. **Agent audit (`agentAPIGate` / `recordAgentCall`, `pkg/ui/agent_api_gate.go`)**: for acting-task and external-agent subrequests, `agentAPIGate`:
   - logs `grain api: refused {agent, <principal>} <pattern>: <err>` when authorization fails before handler execution, or `grain api: {agent, <principal>} <pattern> scope=<scope> repo="<repo>" [channel="<channel>" ]reason="<reason>" -> <status>` when the handler runs;
   - for acting-task calls on `X-Grain-Channel: sandbox-cli` where a run ID is resolved (`X-Grain-Acting-Run` or `Store.ActiveRunID(ctx, taskID)` fallback), records a `task_run_tool_call` row (`RecordProxyToolCall`) on all statuses and a `task_run_api_call` row (`RecordProxyAPICall`) when `status < 400`; host-proxied `grain-controller` calls omit `X-Grain-Channel: sandbox-cli`, and their per-call store rows are recorded through run telemetry (§8.1);
   - on a 2xx (`status < 300`) non-`GET` call targeting another task (other than `POST /api/tasks/{id}/agent-messages`), posts an audit comment on each distinct target task signed `grain`.
3. **Person calls**: subrequests with neither an external token nor `X-Grain-Acting-Task` bypass `recordAgentCall` and run directly under the person's principal.
4. **Configuration writes**: `dispatchSubrequest` wraps its `ResponseWriter` in `configRefusalWriter`; on a successful configuration mutation, `recordConfigWrite` (`config_audit` table) and `syncStateRepoAfterWrite` record the guarded handler principal (`rehearsal.writer`), not the outer transport credential.
5. **Non-subrequest calls**: `POST /api/mcp` itself, `grain_explore_scope`, and `grain_filing_context` emit no `agentAPIGate` log line or `config_audit` entry.

## 7. Attachment to runs and sessions

### 7.1 Run attachment (`wireControllerMCP`) {#CM-19}

1. During `RunDispatch`, when `orchestrator.Config.ControllerMCP` is non-nil, `model.APIGrantsOf(task.Grants)` is non-empty, and no capability server named `grain-controller` is already present, the orchestrator **MUST**:
   - build a per-task proxy via `cfg.ControllerMCP(task.ID)` and serve it on `127.0.0.1:0` (`protocol.ServeEndpoint`);
   - run an MCP handshake (`protocol.Handshake`, `30s` timeout), failing dispatch (`unprepared`) if the handshake fails;
   - append `agent.CapabilityMCPServer{Name: "grain-controller", URL: ep.URL, Tools: roster.Tools}` alongside `grain-sandbox` and any external capability servers.
2. When `model.APIGrantsOf(task.Grants)` is empty, `grain-controller` **MUST NOT** be attached.
3. Harnesses expose the attached server under their standard MCP naming: `mcp__grain-controller__<tool>` (Claude), `mcp_grain-controller_<tool>` (Antigravity/Jetski, eagerly loaded via `_meta["antigravity/alwaysLoad"]`), or MCP server entry `grain-controller` (Codex and custom backends).

### 7.2 Host proxy (`controllerMCPProxy`) {#CM-20}

1. `daemon.controllerMCPProxy` **MUST** reject an empty `taskID`.
2. `protocol.NewEndpointProxy("http://127.0.0.1/api/mcp", {"X-Grain-Acting-Task": taskID}, client)` accepts `POST`, `GET`, and `DELETE` (`405` otherwise, `32 MiB` body cap, `502` on upstream transport error or redirect), strips any `Authorization` or `X-Grain-*` header sent by the harness (forwarding only MCP headers `Accept`, `Content-Type`, `Mcp-Session-Id`, `Mcp-Protocol-Version`, `Last-Event-Id`), and injects `X-Grain-Acting-Task: <taskID>`.
3. `controllerMCPTransport` reads the daemon's local access token (`ui.LocalAPITokenPath(dataDir)`) on each request into `X-Grain-Access-Code` and delivers the request in-process through `LiveAPI.LoopbackRoundTrip` (`RemoteAddr: "127.0.0.1:0"`), without opening a network socket.

### 7.3 Live grant changes {#CM-21}

1. Because `resolveMCPCallerAccess` and `agentAPIGate` load `Task.Grants` from the store on every call, adding or removing `api:*` grants on a running task that already has `grain-controller` attached takes effect on the next call without restarting the run (`noteAPIChange`). Adding sandbox-reachable `api:*` grants to a live run that started without `grain-controller` attached likewise skips restart (`noteAPIChange` advises using `grain api` from the sandbox immediately, while `grain-controller` attaches on the next run).
2. Adding a controller-only scope (`controllerOnlyScopesAdded`—scopes in `APIGrantsOf` absent from `SandboxAPIGrantsOf`) to a live run that started with empty `model.APIGrantsOf(task.Grants)`—and therefore has no `grain-controller` attached—restarts the run so `wireControllerMCP` attaches the server.

### 7.4 Sandbox HTTP proxy channel {#CM-22}

A process inside a sandbox reaching `/api/mcp` via the per-sandbox HTTP proxy (`gitproxy.forwardToAPI`) has client `Authorization` and `X-Grain-*` headers stripped (preserving client `X-Grain-Acting-Reason` or `X-Grain-Reason` as `X-Grain-Acting-Reason`), `RemoteAddr` cleared, and `X-Grain-Acting-Task`, `X-Grain-Acting-Run` (when non-empty), and `X-Grain-Channel: sandbox-cli` injected. Such calls resolve as an acting task on `sandbox-cli` (§3.2.2, §4.1.2).

## 8. Telemetry and live activity {#CM-23}

1. **Run tool telemetry (`orchestrator.runTelemetryAt`, `pkg/orchestrator/census.go`)**:
   - Strips harness server prefixes (`mcp__grain-controller__`, `mcp_grain-controller_`, `grain-controller/`, `grain-controller__`) and unwraps `call_mcp_tool` envelopes.
   - Rewrites `grain_invoke` calls to the target route tool name (`api_<handler>`), unwrapping `arguments` as the recorded tool input (flattening keys from `arguments.body` and `arguments.query` when absent at top level, and carrying top-level `reason` when absent) and, on non-error calls, extracting referenced task IDs and CI run IDs (`extractAPICallTargetIDs`).
   - Records `grain_explore_scope`, `grain_filing_context`, `grain_submit_task`, `grain_list_tasks`, and `grain_get_task` under their bare `grain_*` names.
2. **Read-call classification**: `grain_get_task` and `grain_list_tasks` are classified as read calls alongside `api_get_task` and `api_list_tasks` in `metrics.isReadToolForAgents` and `orchestrator.IsReadAPICall`.
3. **Live activity narration (`mcp.capabilityActivityNotes["grain-controller"]`)**: while a proxied call is in flight, `proxy.ObserveToolCalls` updates the task's live status line to:
   - `grain_explore_scope`: *exploring `<scope>` API scope* (or *listing API scopes* when `scope` is omitted);
   - `grain_invoke`: *calling `<tool>`* (or *calling controller API* when `tool` is omitted);
   - `grain_filing_context`: *reading task filing context*;
   - `grain_submit_task`: *filing task "`<title>`"* (or *filing a task*);
   - `grain_list_tasks`: *listing tasks*;
   - `grain_get_task`: *reading task `<id>`* (or *reading a task*).

## 9. Open questions {#CM-24}

Items open for iteration on the API contract:

1. **`grain_filing_context` omitted vs. `null` keys (§4.4.2, §5.3).** Unauthorized `capabilities`/`apiPresets` and `settings:read` fields are omitted from the JSON object, whereas `templates` and `boards` are always present and serialize as `null` both when empty and when the caller lacks `templates:read` or `boards:read` (`targetRepos` is also `null` when empty). Should unauthorized `templates` and `boards` be omitted like `capabilities`, and authorized empty lists serialize as `[]`?
2. **`grain_filing_context` direct store reads vs. subrequests (§4.4).** `grain_filing_context` is the only data-returning tool that queries `Store` directly under `filingContextScopes` rather than composing subrequests through `agentAPIGate` (`GET /api/templates`, `GET /api/boards`, `GET /api/config`). Should it remain a direct store read or compose subrequests?
3. **`expect` mismatch recovery hint (§4.3.4).** `TaskExpectation.Check` (`pkg/ui/task_kinds.go`) ends its error message with `re-read it with read_grain_task before deciding what to do`. Callers of `grain-controller` have `grain_get_task` rather than `read_grain_task`.
4. **External MCP endpoint consolidation (`POST /api/mcp` vs. `POST /mcp`).** External agent tokens (`gma_…`) can call both `POST /api/mcp` and `POST /mcp`. Whether `POST /mcp` remains supported alongside `POST /api/mcp`, and which URL `grain mcp` and Settings → CLI advertise by default, is open.
5. **Per-call logging for person callers (§6.3.3).** Agent subrequests emit `grain api:` audit log lines in `recordAgentCall`, whereas person calls on `POST /api/mcp` emit neither a `grain api:` line nor an MCP `tools/call` log line. Should `POST /api/mcp` log `tools/call` invocations for person callers?
6. **`grain#ci` without an `api:*` grant (§4.1.2, §7.1.1).** `allowsRoute` (`grainCIReadsRoute`) admits a task satisfying `model.HasGrainCI` (`grain#ci` or `grain-ci`) to `grainCIReadRoutes`, while `wireControllerMCP` attaches `grain-controller` only when `model.APIGrantsOf(task.Grants)` is non-empty (which excludes `grain#ci` / `grain-ci`).
