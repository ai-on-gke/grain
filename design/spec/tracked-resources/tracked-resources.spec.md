# Tracked resources and resource providers specification

**Status:** proposed, normative for the protocol below; not implemented. Approval authorizes no runtime, infrastructure, live external access or follow-up task filing.

**Specifies:** [Extensible tracked resources and capability-backed resource providers](../../design/tracked-resources.design.md).

MUST, MUST NOT, SHOULD, SHOULD NOT and MAY have their BCP 14 meanings. Requirement IDs are stable. The tables below define concrete closed-record schemas and cross-field invariants for protocol `1.0`; examples use illustrative IDs/digests. This specification does not claim real CITC or Buganizer API shapes.

## 1. Authority and gates compatibility {#TDS-1}

1. A tracked resource MUST identify an explicitly referenced observable entity. Grain MUST own persisted references, relations, authorization views, observations, journal, subscriptions, cursors and durable timers. Providers MUST report identity/state/change, never whether a tracked resource is “met”.
2. Merely attaching/watching a reference MUST NOT grant access, block dispatch or require completion. A required gate MUST explicitly bind the desired outcome; a dispatch prerequisite MUST separately declare its predicate and affected work. Advisory linkage MUST NOT prevent completion. New dispatch prerequisite predicates MUST be interpreted by explicitly attached gates (which need not be required for completion); only imported legacy dispatch predicates may retain their existing interpreter. A provider MUST NOT become a second outcome interpreter.
3. The Task #39 [evaluator specification](../mcp-gates/mcp-gates.spec.md) governs snapshots, typed actions, waivers and atomic lifecycle ownership, using Task #44's merged documentation spelling `gate_*`/`gate_state`. Implementations MUST negotiate/pin the actual gate protocol and migrate explicitly; they MUST NOT infer runtime protocol support from a documentation merge. Existing concrete task goals/APIs MUST retain their names and predicates. This protocol adds tracked-resource evidence; it does not independently switch completion authority.
4. Resolve/Observe MUST be read-only. Watch MAY create/renew/remove authenticated delivery subscriptions using a distinct watch permission; it MUST NOT comment, submit, close, merge or otherwise modify the observed resource. Those operations MUST use independently authorized registered typed actions with current preconditions.
5. Required reference/gate edits MUST audit actor, authority epoch, before/after contract revision, reason and affected evidence. Removal/relaxation, retargeting, follow-latest/pin changes, provider replacement and success-to-closure changes MUST require authorized person approval unless that exact transformation was preauthorized. Agents MAY propose edits. Ordinary reference removal MUST fail if it would leave an attached gate dangling.

6. The new observable entity MUST be called a tracked resource and its capability-backed integration a resource provider. Task dependency MUST retain its existing task-prerequisite meaning and semantics, including `depends_on`. Watch MUST denote the subscription, not the resource. Gates interpret blocking requirements; triggers create work from events under separate policy/authorization. Resource observation/Watch MUST NOT itself create tasks or acquire trigger authority.

## 2. Identity, references and relations {#TDS-2}

All objects are UTF-8 JSON; timestamps are RFC 3339 UTC. IDs/tokens are opaque strings unless a schema states otherwise. Closed records reject unknown keys; extensible `identifier`, `selector`, `details` and configured `args` validate against pinned local provider schemas. No remote schema resolution is permitted.

| Record | Required fields and types |
|---|---|
| ResourceProviderIdentity | `capability_id: string` canonical configured capability ID (HTTPS identity, no query/fragment), `method: string` matching `^[a-z0-9][a-z0-9_]{0,47}$` |
| ResourceProviderPin | `identity: ResourceProviderIdentity`, `release: string` immutable artifact digest, `protocol: "1.0"`, `identifier_schema: string`, `fact_schema: string`, `details_schema: string`, `config_revision: string` (no secrets) |
| TrackingScope | `facet: string`, `mode: "follow_latest" \| "pinned"`, `selector: object \| null`; pinned requires non-null selector identifying an immutable subject |
| TrackedResourceReference | `version: "1"`, `provider: ResourceProviderIdentity`, `resource_kind: string`, `identifier: object`, `tracking: TrackingScope` |
| TaskResourceReference | `task_id: string`, `reference_id: string`, `reference: TrackedResourceReference`, `pin: ResourceProviderPin`, `relation: "watches" \| "uses" \| "produced_by" \| "spawned_task"`, `view_id: string`, `reference_revision: integer >= 1` |
| TaskRelation extensions | Spawned task relations additionally carry `child_role: "required" \| "advisory" \| "independent"`; required MUST link a separately authorized required success gate by `gate_id: string`. Other relations MUST NOT carry these fields. |

1. Canonical resource identity MUST include provider identity, kind and the identifier's authority/installation plus stable resource key. Core MUST treat kind and identifier as registry-schema data, not a fixed external-provider enum. A human URL, workspace name, repository name or bug title MUST NOT alone identify a resource if it can be reused/renamed.
2. Resolve MUST define canonicalization, including locator-to-stable-ID mapping, exact string case rules and identifier schema version. Grain MUST use canonical JSON (RFC 8785) to compare/hash validated records. Resource identity excludes mutable display fields, credentials and release pin; tracked identity additionally includes TrackingScope. Different scopes MUST NOT share decision evidence without explicit validated mapping.
3. Task-local reference IDs MUST be stable and unique within a task, never recycled after removal. Retargeting MUST increment reference and contract revisions, retain old provenance and invalidate affected evidence. Provider upgrades MUST NOT silently change identity.
4. Follow-latest MUST observe the current subject within the facet. Pinned scope MUST continue observing only the selected immutable subject and facts explicitly bound to it. Providers MUST reject unsupported selectors; they MUST NOT silently convert pinning into follow-latest. Revision equality tokens are not necessarily immutable snapshot IDs and MUST NOT be used as selectors unless the provider explicitly declares that property.
5. A workspace adapter MUST distinguish workspace identity, content snapshot/revision and submission identity. Pinned snapshot submission facts MUST bind the exact content digest/snapshot to the submission. A mutable changelist number or “workspace submitted” flag alone is insufficient.
6. Reference identity MUST NOT contain secret credentials. Sensitive identifiers MUST be protected as private task data. Public display MUST be separately authorized.

### Canonical reference examples

These are hypothetical resolved adapter outputs, not external API requests. Provider capability identities under `example.org` are illustrative configuration keys, not fetch endpoints. Grain task is the proposed built-in registration. Stable node IDs, workspace/bug IDs and selectors are illustrative, not assertions of real service fields.

```json
{
  "version": "1",
  "provider": {"capability_id": "https://example.org/capabilities/github", "method": "forge"},
  "resource_kind": "github.pull_request",
  "identifier": {"authority": "github.com", "repository_id": "repo-81", "resource_id": "pr-1666"},
  "tracking": {"facet": "change", "mode": "follow_latest", "selector": null}
}
```

```json
{
  "version": "1",
  "provider": {"capability_id": "https://example.org/capabilities/github", "method": "forge"},
  "resource_kind": "github.issue",
  "identifier": {"authority": "github.com", "repository_id": "repo-81", "resource_id": "issue-42"},
  "tracking": {"facet": "lifecycle", "mode": "follow_latest", "selector": null}
}
```

```json
{
  "version": "1",
  "provider": {"capability_id": "https://grain.dev/capabilities/grain", "method": "tasks"},
  "resource_kind": "grain.task",
  "identifier": {"installation_id": "instance-a1", "task_id": "72"},
  "tracking": {"facet": "outcome", "mode": "follow_latest", "selector": null}
}
```

```json
{
  "version": "1",
  "provider": {"capability_id": "https://example.org/capabilities/workspaces", "method": "workspaces"},
  "resource_kind": "example.workspace",
  "identifier": {"authority": "workspace-installation-a", "workspace_id": "ws-17"},
  "tracking": {"facet": "content_submission", "mode": "pinned", "selector": {"snapshot_id": "S1", "content_digest": "sha256:111"}}
}
```

The CITC workspace example deliberately uses the neutral illustrative kind `example.workspace` and capability key `workspaces`; these are adapter fixture names, not actual service identifiers. A follow-latest workspace uses the same identifier with `{"facet":"content_submission","mode":"follow_latest","selector":null}`. Its observation identifies S1 today and may identify S2 tomorrow.

```json
{
  "version": "1",
  "provider": {"capability_id": "https://example.org/capabilities/buganizer", "method": "bugs"},
  "resource_kind": "buganizer.bug",
  "identifier": {"authority": "bug-installation-a", "bug_id": "9001"},
  "tracking": {"facet": "lifecycle", "mode": "follow_latest", "selector": null}
}
```

CITC/Buganizer adapters require documented identifiers and fact mappings, permitted read/watch credentials, service/network access, tested snapshot semantics where relevant and separately reviewed implementation/configuration. This spec MUST NOT be read as granting access or claiming support.

## 3. Discovery, configuration and leases {#TDS-3}

1. A provider MUST be explicitly registered from a configured capability server. MCP `tools/list` MUST advertise a trio `resource_<method>_resolve_v1`, `resource_<method>_observe_v1`, `resource_<method>_watch_v1` for each method. These MUST remain distinct from `mint_*`, `release_*` and `gate_*`; discovery MUST NOT attach references or mint access.
2. Each tool MUST have `inputSchema` for its request, `outputSchema` for its result, `annotations.readOnlyHint=true`, and an identical proposed `_meta["org.grain.resource/v1"]` descriptor. Grain MUST independently enforce read-only authority; annotations alone are insufficient.
3. Descriptor schema: `{protocol:"1.0", method:string, release:string, kinds:[{kind:string, identifier_schema:object, selector_schema:object, details_schema:object, fact_version:string}], detection:["event"|"cursor"|"poll"], watch_scope:string, min_poll_seconds:integer, max_poll_seconds:integer}`. Schema digests MUST cover resolved local definitions. Unknown required metadata or missing trio MUST prevent registration. Unknown major protocol/fact versions MUST be rejected; minor extensions require explicit advertised support. Tools MUST NOT silently change schemas or implementation at the same pin.
4. Provider-specific schemas MUST use the capability protocol's supported argument subset for configuration/identifiers/selectors/details; envelope schemas MAY use JSON Schema 2020-12. Configuration/secrets/transports MUST follow the [capability specification](../capabilities/capabilities.spec.md). Capability ID is an opaque registry key, never the address to fetch.
5. Grain MUST authorize controller read and watch scope separately from task edit/evidence read and action scope. An explicitly named candidate MAY be persisted unresolved without access; Resolve MUST NOT probe a private service until authorized. Unsupported/uninstalled providers MUST retain the reference with an unavailable reason.
6. Durable watchers MUST use controller leases tied to task grant lineage, provider pin, authorized fields/resource scope, expiry and revocation epoch. They MUST NOT retain an attempt-scoped pass after that attempt releases it. Lease renewal MUST recheck current policy; failed renewal makes the view unauthorized/unknown. Scoped transport context MUST carry credentials, never payload args/tokens returned to agents.

## 4. Resolve and Observe schemas {#TDS-4}

| Message | Required fields |
|---|---|
| Common request envelope | `protocol_version:"1.0"`, `request_id:string`, `pin:ResourceProviderPin`, `authorization_epoch:integer >= 1`, `deadline:timestamp` |
| Resolve request additions | `candidate:object` (provider-specific locator or canonical reference), `tracking:TrackingScope` |
| Resolve result | `protocol_version`, `request_id`, `status:"resolved" \| "invalid" \| "unavailable" \| "unauthorized" \| "error"`, `reference:TrackedResourceReference \| null`, `display:{summary:string,url:string\|null} \| null`, `error:Error \| null` |
| Observe request additions | `reference:TrackedResourceReference`, `observation_generation:integer >= 1`, `previous_revision:string \| null` (hint only, not permission to omit the current record) |
| Observe result | `protocol_version`, `request_id`, `reference:TrackedResourceReference`, `observation_generation`, `availability:"available" \| "unavailable" \| "deleted" \| "unauthorized" \| "error"`, `revision:string \| null`, `fact_version:string`, `facts:Facts \| null`, `details:object \| null`, `evidence:Evidence[]`, `display:{summary:string,url:string\|null}`, `observed_at:timestamp`, `valid_until:timestamp`, `error:Error \| null` |
| Error | `code:string`, `retryable:boolean`, `summary:string`, `retry_after_seconds:integer \| null` |
| Facts (v1) | `lifecycle:"active" \| "terminal" \| "unknown"`, `disposition:"merged" \| "submitted" \| "resolved" \| "closed" \| "succeeded" \| "failed" \| "canceled" \| "unknown"`, `subject_revision:string \| null` |
| Evidence | `handle:string` provider opaque evidence ID, `version:string`, `kind:string`, `digest:string \| null` |

1. All results MUST echo the request ID and negotiated protocol version; Grain MUST match them to the outstanding pinned invocation and reject mismatched reference, scope or generation. A provider MUST NOT redirect an Observe response to another identity. Resolve MUST validate kinds/identifiers/scopes and canonicalize aliases only within authorized scope. Failed resolution MUST return null reference, generic redacted display/error as needed. “Unauthorized or unavailable” MUST be used when distinguishing nonexistent from forbidden would reveal private existence. Invalid syntax may be diagnosed without a remote lookup.
2. Available observations MUST contain nonempty revision, facts and validated bounded details (empty object permitted). Nonavailable observations MUST have null revision/facts/details and empty evidence; authorized confirmed deletion MAY return a Grain-stored tombstone through its observation availability, never infer success. Error MUST be non-null on error/unavailable/unauthorized, null on available; deleted MAY include a redacted explanation.
3. Facts MUST be sourced, not inferred from missing data. Absent/unreadable facts MUST be unknown/null. Available does not mean complete; a gate needing an unsupported fact MUST return unknown. `disposition` may be supplied only when authoritative provider evidence matches its declared fact-version mapping. Terminal alone MUST NOT imply success. Issue closed without confirmed resolution maps to closed, not resolved; unsuccessful task closure maps to its actual outcome, or unknown if unproven.
4. Provider-specific details MUST be namespaced by the registered kind/schema. A gate MUST declare support for the exact fact/details versions it consumes. It MUST NOT treat unknown fields/statuses as positive evidence. Human text, display colors, stored opaque provider state and event payloads MUST NOT substitute for facts. Providers MUST NOT manufacture Grain action receipts, human approvals or authoritative reviews. Gates requiring those MUST consume the existing authenticated Grain action/review collectors and their scope/policy records, not a provider's lifecycle claim.
5. Revision tokens MUST support equality only, scoped to tracked identity + provider pin + authorization partition. Changed relevant facts/details/evidence/subject MUST change the token; time/display-only changes need not. Tokens MUST NOT encode secrets. No consumer may sort tokens or compare across pins/scopes. Grain MUST maintain its own monotonic accepted observation sequence.
6. `observed_at` MUST be the time of actual source collection, not cache delivery; `valid_until >= observed_at`. Grain MUST record request start/receipt time, reject impossible future collection times beyond 30 seconds clock skew, and cap usable freshness at the minimum of provider validity and the gate's collection-age budget. A provider that cannot establish collection time MUST return unavailable for freshness-sensitive reads.
7. Grain MUST persist immutable observation records with reference/pin, authorization epoch, collector provenance, accepted sequence, received time, evidence handles and redacted payload digest. Evidence handles MUST be materialized into immutable Grain-managed evidence or verified immutable authorized artifacts before use by gates. Unsupported/mutable/expired handles mean unknown, not an implicit fetch.
8. An available observation MAY have no evidence for advisory display; a required gate MUST NOT be met without sufficient authenticated immutable evidence. Grain handles served to gates MUST be bound to snapshot, task, audience and expiry. Current authorization MUST be checked at every read even for historical evidence.
9. Schema/pin changes MUST use an audited explicit migration: resolve identity equivalence, retain old observations, establish new watches and observe afresh. Old tokens/cursors MUST NOT be reused unless explicitly supported and validated. A fact meaning change requires a new fact version, invalidates dependent results and requires gate compatibility; no silent reinterpretation.

### Resolve example

The following is a complete `resource_forge_resolve_v1` request for an illustrative configured GitHub adapter. A real adapter must authorize the repository lookup and document locator resolution; this does not define a GitHub API.

```json
{
  "protocol_version": "1.0", "request_id": "resolve-18",
  "pin": {
    "identity": {"capability_id": "https://example.org/capabilities/github", "method": "forge"},
    "release": "sha256:aaa", "protocol": "1.0",
    "identifier_schema": "sha256:bbb", "fact_schema": "sha256:ccc",
    "details_schema": "sha256:ddd", "config_revision": "7"
  },
  "authorization_epoch": 4, "deadline": "2026-11-16T20:00:30Z",
  "candidate": {"authority": "github.com", "repository": "ai-on-gke/grain", "kind": "github.pull_request", "number": 1666},
  "tracking": {"facet": "change", "mode": "follow_latest", "selector": null}
}
```

```json
{
  "protocol_version": "1.0", "request_id": "resolve-18", "status": "resolved",
  "reference": {
    "version": "1",
    "provider": {"capability_id": "https://example.org/capabilities/github", "method": "forge"},
    "resource_kind": "github.pull_request",
    "identifier": {"authority": "github.com", "repository_id": "repo-81", "resource_id": "pr-1666"},
    "tracking": {"facet": "change", "mode": "follow_latest", "selector": null}
  },
  "display": {"summary": "PR #1666", "url": "https://github.com/ai-on-gke/grain/pull/1666"},
  "error": null
}
```

### Observe example

The request below is the `tools/call.arguments` for `resource_forge_observe_v1`. It illustrates one full pin and the equality token from a previous observation. Authorization context is transport-bound, not a credential in JSON.

```json
{
  "protocol_version": "1.0", "request_id": "read-19",
  "pin": {
    "identity": {"capability_id": "https://example.org/capabilities/github", "method": "forge"},
    "release": "sha256:aaa", "protocol": "1.0",
    "identifier_schema": "sha256:bbb", "fact_schema": "sha256:ccc",
    "details_schema": "sha256:ddd", "config_revision": "7"
  },
  "authorization_epoch": 4, "deadline": "2026-11-16T20:00:30Z",
  "reference": {
    "version": "1",
    "provider": {"capability_id": "https://example.org/capabilities/github", "method": "forge"},
    "resource_kind": "github.pull_request",
    "identifier": {"authority": "github.com", "repository_id": "repo-81", "resource_id": "pr-1666"},
    "tracking": {"facet": "change", "mode": "follow_latest", "selector": null}
  },
  "observation_generation": 19, "previous_revision": "opaque:before"
}
```

```json
{
  "protocol_version": "1.0", "request_id": "read-19",
  "reference": {
    "version": "1",
    "provider": {"capability_id": "https://example.org/capabilities/github", "method": "forge"},
    "resource_kind": "github.pull_request",
    "identifier": {"authority": "github.com", "repository_id": "repo-81", "resource_id": "pr-1666"},
    "tracking": {"facet": "change", "mode": "follow_latest", "selector": null}
  },
  "observation_generation": 19, "availability": "available",
  "revision": "opaque:now", "fact_version": "1",
  "facts": {"lifecycle": "active", "disposition": "unknown", "subject_revision": "head:H1"},
  "details": {"head_oid": "H1", "change_state": "open"},
  "evidence": [{"handle": "ev-pr-19", "version": "1", "kind": "github.pull_request", "digest": "sha256:eee"}],
  "display": {"summary": "PR #1666 is open at H1", "url": "https://github.com/ai-on-gke/grain/pull/1666"},
  "observed_at": "2026-11-16T20:00:02Z", "valid_until": "2026-11-16T20:05:02Z", "error": null
}
```

An inaccessible bug response uses the same envelope/reference/generation with `availability:"unauthorized"`, `revision:null`, `facts:null`, `details:null`, `evidence:[]`, null display URL and generic summary, validity equal to observation time, and `error:{"code":"access_unavailable","retryable":false,"summary":"Resource unavailable under this grant","retry_after_seconds":null}`. It MUST NOT confirm the bug exists. A stale previous success MUST NOT fill those nulls.

## 5. Watch and durable reconciliation {#TDS-5}

| Watch request | Fields in addition to common envelope |
|---|---|
| Declare | `operation:"describe"`, `reference:TrackedResourceReference` |
| Establish/renew | `operation:"ensure"`, `reference`, `subscription_id:string` Grain-generated stable idempotency key, `generation:integer`, `after_cursor:string \| null`, `delivery_handle:string \| null` Grain-issued authenticated destination handle, `expires_at:timestamp`, `poll_seconds:integer` |
| Stop | `operation:"cancel"`, `reference`, `subscription_id`, `generation` |
| Watch result (all operations) | `protocol_version`, `request_id`, `status:"ready" \| "unsupported" \| "unauthorized" \| "error"`, `modes:("event" \| "cursor" \| "poll")[]`, `subscription_id:string\|null`, `generation:integer\|null`, `cursor:string\|null`, `replay:"gap_free" \| "gap_possible" \| "expired"`, `expires_at:timestamp\|null`, `min_poll_seconds:integer`, `max_poll_seconds:integer`, `error:Error\|null` |

1. Describe MUST declare actual supported detection and bounds without establishing a subscription. Ensure/cancel MUST be idempotent by subscription ID + generation, and MUST reject stale generations. Poll-only providers MAY answer ready without remote registration; Grain still persists the durable timer. Cursor-mode retrieval MUST use ensure with the prior cursor and return change hints through the authenticated delivery channel; it MUST declare retention/gap behavior. Cancel MUST be safe after expiry/restart and MUST NOT require resource-write permission.
2. Change hints MUST carry `{subscription_id,generation,event_id,cursor,reference_key,emitted_at}` (strings except generation/timestamp), be authenticated to the exact registered provider and partition, and be bounded. They MUST NOT carry authoritative decision facts. Cursor may be null for event-only delivery; only provider-declared cursor semantics may determine replay order.
3. Grain MUST persist registration intent before remote ensure and reconcile a crash between ensure and local acknowledgement using the same key. It MUST establish an event/cursor fence **before** baseline Observe, buffer receipts, then Observe and drain/replay after that fence. Advancing the durable cursor/receipt MUST atomically record resulting dirty work or accepted observation and fan-out outbox, so crash cannot lose reevaluation.
4. Where registration cannot supply gap-free replay, Grain MUST record gap_possible, Observe after registration and again after draining buffered hints, then retain bounded periodic reconciliation. Cursor expiry requires full Observe/new fence, not assumption of no change. An event arriving during or after Observe MUST leave a dirty generation until a subsequent current Observe covers it.
5. Each Observe write MUST CAS the expected accepted sequence, lease owner/generation, provider pin, reference revision and authorization epoch. Only one collector lease may commit per partition. An older/delayed response MUST NOT overwrite a newer accepted observation. If dirty hints arrived during a read, Grain MUST invalidate affected gate bundles immediately and reconcile again; it MUST NOT declare that read covers later hints.
6. Tokens are equality-only: out-of-order event payloads MUST NOT install old state; they trigger current Observe. Grain MUST coalesce duplicate event IDs and outstanding dirty work with bounded dedup retention; duplicates outside retention may cause redundant reads, never repeated actions. Every accepted material change MUST durably fan out only to authorized dependent task views/gates. Same-token refresh MAY advance freshness; it MUST NOT count as resource progress or trigger an unbounded evaluator feedback loop.
7. Grain MUST persist reconciliation/expiry/retry/escalation timers and restore them after restart under fenced ownership. Polling MUST be bounded between 30 seconds and 1 hour, default 60 seconds where service policy permits; adapter/service minimums and rate limits override faster requests. Jitter retries and exponentially back off from 60 seconds to at most 1 hour; honor longer Retry-After by exposing staleness/escalation. A per-provider and per-grant token bucket MUST cap requests (default 60/minute, burst 10, at most 4 concurrent per provider; deployments may narrow).
8. Every subscription MUST have reconciliation at least hourly when access/service limits permit, even with webhooks. If limits prevent the configured freshness, expose unknown/stale and the next allowed read, not fictitious freshness. Three failed attempts or 15 minutes of unavailability (whichever first) MUST raise one deduplicated blocker/escalation; subsequent retries remain bounded. Deadline escalation MUST NOT satisfy a gate.
9. Removing the last authorized recipient MUST cancel remote delivery best-effort and retain a cleanup intent until confirmed or expired. Leases MUST expire within one hour without renewal; use hourly-or-shorter registration expiry/renewal. Undeliverable cleanup MUST NOT preserve access.

### Watch wire example

An ensure request adds the following fields to the common envelope (with the full reference from §4); these are contract fields, not a claimed webhook service API:

```json
{
  "operation": "ensure",
  "subscription_id": "sub-81", "generation": 3,
  "after_cursor": null, "delivery_handle": "delivery:scoped-81",
  "expires_at": "2026-11-16T21:00:00Z", "poll_seconds": 60
}
```

```json
{
  "protocol_version": "1.0", "request_id": "watch-81",
  "status": "ready", "modes": ["event", "cursor", "poll"],
  "subscription_id": "sub-81", "generation": 3,
  "cursor": "opaque:fence-7", "replay": "gap_free",
  "expires_at": "2026-11-16T21:00:00Z",
  "min_poll_seconds": 60, "max_poll_seconds": 3600, "error": null
}
```

## 6. Security, isolation and limits {#TDS-6}

1. Separate authorization MUST apply to attachment/edit, controller Resolve/Observe, controller Watch, person/agent evidence reads and every action. Grain MUST use current grants/RBAC/policy, not a resource URL or provider assertion, to authorize. An evaluator sees only its declared bounded snapshot scope. Typed actions MUST recheck present permission and evidence even if an earlier button was authorized.
2. Default cache/subscription partition MUST include tenant/installation, canonical tracked identity, provider pin/config revision, task grant lineage/principal, authorization epoch and field/redaction policy. Cross-task dedup MAY occur only after Grain proves equivalent effective access and visibility. Physical sharing MUST retain separate authorized recipient views and MUST NOT use unioned privileges. Cache hit timing/status, watcher counts and errors MUST NOT reveal private resources across partitions.
3. Revocation or policy/credential expiry MUST advance the authorization epoch and immediately fence future reads/writes/actions and snapshot consumption; invalidate that view's current cache, pending fan-out, gate results and waits requiring access; unsubscribe that recipient and cancel/expire unneeded registrations. Raw historical records MAY remain for restricted audit retention, but MUST NOT remain accessible through normal caches or snapshot handles. Restoring access requires a fresh observation, not reuse of a prior epoch's “met”.
4. Webhooks MUST verify issuer/signature or mutually authenticated channel, timestamp/replay window, delivery identity, subscription generation, resource scope and payload size before queuing. Credential rotation MUST support a bounded explicit overlap window. Invalid events MUST NOT enumerate task/resource associations or create arbitrary subscriptions. Event credentials authorize delivery only, not Observe or resource actions.
5. Identifiers MUST validate against pinned schemas; reject ambiguous authority, oversized keys, traversal/control characters and malformed locators. Display URLs MUST use allowlisted HTTPS authority/path rules (or validated relative Grain routes), reject userinfo/credentials and executable schemes, and render as inert escaped links. Providers MUST NOT request arbitrary HTTP fetching by Grain. Evidence uses scoped registered handles; adapters performing remote requests MUST enforce configured service allowlists, DNS/IP/redirect checks, TLS and credential audience. Internal CITC/Buganizer routing requires explicit configured network policy, never trust of a user URL.
6. Summaries/details/identifiers/cursors MUST be classified/redacted before UI, logs, notifications, agent prompts or audit exports. Raw service responses, credentials, private bug text and unbounded transcripts MUST NOT be stored by default. Text MUST be treated as untrusted data, never executable instructions. Audit identity/pin/times and redacted errors without secrets.
7. Per call: deadline at most 30 seconds, request/response at most 256 KiB UTF-8 JSON, details at most 32 KiB, maximum JSON depth 16, 100 evidence handles, summary 2 KiB, token/identifier scalar 2 KiB, event 16 KiB. Provider schemas MUST bound every string/array/object and reject over-limit input before remote work. No silent truncation may establish satisfaction. Oversized/incomplete evidence yields unknown with a limit error.
8. Per task defaults: at most 100 references and 200 gate-reference bindings; one logical subscription per tracked view, queued hint coalescing by subscription with a dirty bit after 100 pending receipts. These limits MAY be narrowed or explicitly revised under audited deployment policy, never silently exceeded. Providers MUST honor cancellation/timeouts; Grain MUST isolate/quarantine a persistently nonconforming provider.
9. Missing/uninstalled/unhealthy providers MUST produce unavailable with durable retry or installation blocker. Grain MUST NOT auto-install, broaden grants, substitute another provider or fall back to cached success.

## 7. Gates, freshness and atomic decisions {#TDS-7}

1. Grain snapshots MUST include TaskResourceReference revision, immutable observation ID/accepted sequence, provider/fact pins, subject scope, freshness/availability, authorization epoch and resource observation journal cursor. A gate MUST name consumed reference IDs and fact versions and cite those immutable records. A missing required reference/fact or provider error MUST be unknown, never satisfied.
2. Gate contracts MUST set an evidence-age budget (default 300 seconds, allowed 0–3600; zero requires a collection in that evaluation cycle). Grain MUST mark evidence stale at the earliest provider validity or budget expiry and schedule reevaluation then. Previously met results MUST cease to authorize closure/actions when stale. An event-only watch MUST NOT extend freshness without Observe.
3. Accepted relevant changes, dirty hints, grant changes, reference/contract/policy edits, provider migration, expiry, PR head or workspace subject changes MUST invalidate affected bundles and evidence-scoped waivers/actions. Waivers MUST name gate/contract, exact reference/subject/predicate, evidence and authority scope with expiry, following the gates waiver contract. No waiver of one snapshot automatically covers later content.
4. Grain MUST evaluate first on tracked-resource changes, coalescing duplicate wakes. It MUST dispatch an agent only for useful authorized actionable work identified under the gates work/action rules, not merely because status changed or a timer elapsed. Unrelated actionable work MUST remain dispatchable while another tracked resource is awaited. A whole-task dispatch hold requires explicit scope/reason; waiting alone is no such hold.
5. Atomic closure MUST recheck one consistent gate bundle, current task contract/policy/authority epoch, incoming/processed message watermarks, current authorized/fresh observations, no pending relevant dirty invalidation and settled/fenced live writers/operations. Observation and closure transactions MUST serialize on the relevant evidence revision/authorization fences. A competing change wins by invalidating the bundle or is recorded after the historical close under explicit regression policy.
6. Grain cannot atomically lock an external service with its DB. Closure MUST record observed cutoff/receipt and evidence digest; it MUST NOT claim zero remote-change risk. Changes discovered after closure MUST notify or reopen only under explicit lifecycle policy. Deadlines/no-progress limits MAY escalate/hold/recheck, MUST NOT auto-pass or silently waive.

## 8. Built-in tasks, graph rules and migration {#TDS-8}

1. Grain MUST provide a built-in `tasks` provider with the same protocol and immutable installation/task identity. It MUST distinguish lifecycle closure, failed/canceled outcome, report delivery, merged integration, trustworthy success and successor/rollover facts. An unknown legacy outcome MUST NOT be synthesized into success.
2. Required children MUST have explicit required success gates (or a deliberately different approved predicate); advisory and independent spawned links MUST NOT block closure. A stopped/closed child is not proof of successful work. Failed required children require repair, authorized replacement or scoped waiver.
3. Import MUST preserve existing `depends_on`/`child-of` dispatch predicates, `wait_for_task` settled-vs-met semantics, `resume_later` conditions and one-shot park-owned link cleanup. Required completion gates MUST NOT be inferred. Existing `tasks-closed` concrete goals MUST preserve their exact current predicate/provenance; switching to success requires an authorized contract change.
4. Local cycle checking MUST transactionally inspect the union of explicit dispatch waits and required completion waits on edits, migration, reopening and retargeting. Reject self/cyclic blocking edges, including two concurrent edits that form a cycle. Permission-filtered diagnostics MUST NOT reveal hidden task titles/edges. Advisory edges MAY form cycles. Unknown/external graphs MUST be labeled unverifiable, retain bounded patience and escalate no progress; they MUST NOT be automatically assumed acyclic or have requirements dropped.
5. Reader-first additive migrations MUST preserve legacy fields/projections and keep Go/SQL task state parity. Each task MUST have one completion authority and epoch; shadow observations MUST NOT alter legacy decisions. Authoritative resource/gate rollout depends on central gate lifecycle ownership.
6. Rollback MUST fence workers, preserve records/pins/cursors/audit and explicitly reverse handoff only for contracts representable under legacy semantics. Unsupported readers MUST hold migrated tasks for a compatible version; they MUST NOT silently downgrade success to closure, remove gates or discard unknown provider records. Restart/backup restore MUST reauthorize/reconcile before using cached evidence.

## 9. Task UI, API and agent tools {#TDS-9}

The following routes/tools are proposed, not deployed:

| Agent tool | Proposed API | Contract |
|---|---|---|
| `resource_attach` | `POST /api/tasks/{id}/resources` | Input candidate, provider pin, tracking, relation and expected contract revision; returns a task-local reference or redacted unresolved blocker. Resolve preview is explicit and read-authorized. No auto-grant/gate. |
| `resource_list` | `GET /api/tasks/{id}/resources` | Authorized paginated references with relation, observation/freshness and linked gate IDs; at most 100 per page. |
| `resource_get` | `GET /api/tasks/{id}/resources/{reference_id}` | Current authorized bounded view, immutable evidence references and wait/error state. |
| `resource_remove` | `DELETE /api/tasks/{id}/resources/{reference_id}` | Expected contract revision required; refuses live required bindings unless part of an authorized atomic gate edit. |
| `resource_watch_change` | `POST /api/tasks/{id}/resources/{reference_id}/watch-change` | Input after accepted observation sequence, max wait 30 seconds or explicit park; returns changed/unchanged/unavailable and new sequence. A park persists a cursor-fenced wait and stops the run under existing sandbox-preservation rules. |

1. All mutations MUST require expected contract revision, idempotency key and appropriate edit authority. Proposed API grants MUST be registered under the existing API capability system, with separate task-bounded resource read/edit/watch permissions; generic task metadata authority MUST NOT imply requirement relaxation or external read/watch. No new actual grant is created by this spec.
2. Watch-change MUST return immediately if a change already occurred after the supplied sequence and MUST register/replay transactionally otherwise. It watches revision, availability or freshness changes, not success, and MUST NOT mutate dispatch/completion contracts. A park MUST preserve independent declared links and report permission/access loss explicitly.
3. UI MUST show canonical identity/safe link, tracking facet/mode/selector, status/unknown/error, collection age and freshness, required outcome through gate bindings, dispatch holds and wait driver/deadline/escalation separately. UI MUST NOT equate green resource status with task completion. Actions MUST use typed gate buttons and current server authorization.
4. Add/remove and contract changes MUST have permission-aware previews and audit. Access-denied views MUST redact existence/private fields. User-requested access follows existing explicit permission flows; attach MUST NOT automatically request or grant it. New UI belongs in Angular; shared APIs MUST keep classic consumers working.

## 10. Acceptance scenarios {#TDS-10}

These are required future conformance tests, not runtime tests added by this documentation PR.

| Scenario | Required result |
|---|---|
| One PR, tasks A/B with different gates | Same canonical PR; A waits for integration, B can finish a head-scoped report; neither relation grants merge authority. |
| Shared PR, task C has narrower fields/grants | Separate authorized observation view; no private review content, existence/cache timing or watcher-count leakage; no use of A's credential. |
| Workspace S1 changes to S2 during evaluation | Follow-latest subject invalidates old review/waiver/action/result; close fails CAS. Pinned S1 remains S1 and needs exact S1 submission proof. |
| Inaccessible/nonexistent bug | Indistinguishable redacted access-unavailable view under unauthorized grant; required resolved gate unknown; no API/support claim. |
| Required child fails/cancels | Legacy settled wait can return failure; required success gate cannot pass. Advisory/independent children never hold closure. |
| Failed task has only legacy completion timestamp | New success gate unknown unless actual trustworthy outcome evidence exists; do not infer success from close/run exit. |
| Webhook missed during downtime | Durable reconciliation Observe discovers current state and fans out to evaluators; no agent polling loop. |
| Event races registration or baseline read | Persisted intent/fence, buffered receipts/replay and second read cover the gap; no lost dirty work. |
| Duplicate/out-of-order webhook; ABA resource state | Coalesced hints invoke current Observe. Ordered Grain sequence never regresses; gates use current subject/evidence, not token ordering or stale payload. |
| Old Observe completes after newer commit/revocation | CAS/lease/reference/auth epoch fences reject write; no stale evidence resurrection. |
| Crash after remote watch ensure, before local ack | Reuse subscription key/generation, reconcile one logical registration and resume durable timers. |
| Crash after cursor receipt, before gate wake | Atomic dirty/outbox record survives; replay drives evaluation exactly once logically, duplicate delivery harmless. |
| Expired cursor or provider loses events | Mark gap, re-fence/full Observe, retain reconciliation; never infer unchanged from missing history. |
| Poll throttled, provider timeout/uninstalled | Stale/unknown with bounded retry and deduplicated escalation; no fallback met, install or authority expansion. |
| Permission revoked while views shared | Revoke affected recipient/cache/handles/results, reject late writes/buttons, keep independently authorized recipients; restored access requires fresh read. |
| Malicious URL, forged webhook/evidence, oversized output | Reject/sanitize before fetch/store/fan-out; no code execution, remote credential leak or task lookup oracle. |
| Token same but freshness expires | Expiry invalidates usable met bundle and wakes evaluator; time-only refresh gives no false progress credit. |
| Two edits create local cycle | One transaction rejected; hidden graph details redacted. Unknown external cycle hits bounded escalation without removing obligations. |
| PR pending while report can advance | Evaluator dispatches useful report work with wait intact; no task-wide automatic hold. |
| Deadline reached; evidence stale | Recheck or escalate; never pass. |
| Tracked-resource change/message/live push races closure | Atomic evidence/message/operation fences reject stale close; later remote discoveries follow recorded regression policy. |
| Pin/fact schema upgrade or rollback | Explicit compatible migration and fresh Observe; no cross-pin token ordering or old-result reuse, one lifecycle owner. |
| Remove reference required by gate | Reject or perform authorized atomic contract edit with audit; no dangling ignored condition. |
| Restart after backup restored revoked grant | Reauthorize and reconcile; stale cached views and old remote subscriptions cannot restore authority. |
