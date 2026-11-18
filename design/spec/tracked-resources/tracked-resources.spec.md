# Tracked resources and resource providers specification

**Status:** proposed, normative for the protocol below; not implemented. Approval authorizes no runtime, infrastructure, live external access or follow-up task filing.

**Specifies:** [Extensible tracked resources and capability-backed resource providers](../../design/tracked-resources.design.md).

MUST, MUST NOT, SHOULD, SHOULD NOT and MAY have their BCP 14 meanings. Requirement IDs are stable. The tables below and the [implementation contracts](implementation.spec.md) define closed-record schemas and cross-field invariants for protocol `1.0`; examples use illustrative IDs/digests. This specification does not claim real CITC or Buganizer API shapes. The unimplemented v1 proposal is completed by [wire.schema.json](wire.schema.json) and [validated examples](examples/README.md). Schema shape validation and the normative semantic/authority checks are both required; neither replaces the other.

## 1. Authority and gates compatibility {#TDS-1}

1. A tracked resource MUST identify an explicitly referenced observable entity. Grain MUST own persisted references, relations, authorization views, observations, journal, subscriptions, cursors and durable timers. Providers MUST report identity/state/change, never whether a tracked resource is “met”.
2. Merely attaching/watching a reference MUST NOT grant access, block dispatch or require completion. A required gate MUST explicitly bind the desired outcome; a dispatch prerequisite MUST separately declare its predicate and affected work. Advisory linkage MUST NOT prevent completion. New dispatch prerequisite predicates MUST be interpreted by explicitly attached gates (which need not be required for completion); only imported legacy dispatch predicates may retain their existing interpreter. A provider MUST NOT become a second outcome interpreter.
3. The canonical [gate specification](../gates/gates.spec.md) owns typed results/actions, scoped waivers and central lifecycle. Initial gates are compiled evaluators using focused internal reads; remote gate tools/state/paging are deferred. Resource providers keep their own pins/discovery, are not prerequisites for built-ins, and do not independently switch task completion authority. Concrete task goal APIs/predicates retain their names.
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
| TaskRelation extensions | Spawned task relations additionally carry `child_role: "required" \| "advisory" \| "independent"`; required MUST link a separately authorized required gate (default success; an explicitly approved different predicate follows MGS-7) by `gate_id: string`. Other relations MUST NOT carry these fields. |

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

1. Register only an operator-approved configured capability server, using its existing opaque capability ID. The exact registration, schema registry, controller grants and transport binding are [TDS-11](implementation.spec.md#TDS-11) and [TDS-12](implementation.spec.md#TDS-12). Discovery does not mint access. Existing attempt-scoped mint/release methods are unchanged.
2. MCP `tools/list` MUST advertise `resource_<method>_resolve_v1`, `resource_<method>_observe_v1`, and `resource_<method>_watch_v1`. Each MUST declare the matching request/result definition from wire.schema.json as inputSchema/outputSchema and the same `_meta["org.grain.resource/v1"]` descriptor. Resolve/Observe have readOnlyHint=true; Watch has readOnlyHint=false because it changes subscription state, idempotentHint=true and destructiveHint=false. No resource-write authority follows from these hints.
3. The descriptor MUST validate against `$defs.descriptor`: protocol, method, release, per-kind identifier/selector/details/candidate schemas, fact version, supported facets, pinning support, detection modes, poll bounds and cursor retention. Every provider schema is local, closed, bounded, validated with the capability supported argument subset; envelopes use JSON Schema 2020-12. No remote schema resolution. Digest computation and exact pin admission are TDS-11.
4. Unknown versions/kinds, missing trio, metadata/schema mismatch, unbounded schema or unsupported transport MUST refuse registration. Unknown provider-specific fact schemas MUST make dependent evidence unavailable; no generic positive interpretation is permitted. A resource provider may also implement trigger acquisition with independent discovery, pins and authority; initial built-in gate evaluation requires neither remote extension.
5. An explicitly named candidate MAY be stored unresolved without access; Resolve MUST NOT probe until the exact candidate scope has a current controller read grant. Unsupported/uninstalled providers retain a redacted unavailable reference. There is no automatic installation, grant, webhook route or reverse MCP call.
6. Controller leases are distinct durable records, never attempt passes. They last at most one hour, default 15 minutes, renew halfway through their term after current authorization checks, and become unusable immediately on revocation. No secret occurs in requests, descriptors, observations or evidence. TDS-12 defines the authenticated server principal and bounded access context; an authorization epoch by itself is not authentication.

## 4. Resolve and Observe schemas {#TDS-4}

| Message | Required fields |
|---|---|
| Common request envelope | `protocol_version:"1.0"`, `request_id:string`, `pin:ResourceProviderPin`, `authorization_epoch:integer >= 1`, `deadline:timestamp`, `access:AccessContext` (TDS-12) |
| Resolve request additions | `candidate:object` (provider-specific locator or canonical reference), `tracking:TrackingScope` |
| Resolve result | `protocol_version`, `request_id`, `status:"resolved" \| "invalid" \| "unavailable" \| "unauthorized" \| "error"`, `reference:TrackedResourceReference \| null`, `display:{summary:string,url:string\|null} \| null`, `error:Error \| null` |
| Observe request additions | `reference:TrackedResourceReference`, `observation_generation:integer >= 1`, `previous_revision:string \| null` (hint only, not permission to omit the current record) |
| Observe result | `protocol_version`, `request_id`, `reference:TrackedResourceReference`, `observation_generation`, `availability:"available" \| "unavailable" \| "deleted" \| "unauthorized" \| "error"`, `revision:string \| null`, `fact_version:string`, `facts:Facts \| null`, `details:object \| null`, `evidence:Evidence[]`, `display:{summary:string,url:string\|null}`, `observed_at:timestamp`, `valid_until:timestamp`, `error:Error \| null` |
| Error | `code:string`, `retryable:boolean`, `summary:string`, `retry_after_seconds:integer \| null` |
| Facts (v1) | `lifecycle:"active" \| "terminal" \| "unknown"`, `disposition:"merged" \| "submitted" \| "resolved" \| "closed" \| "succeeded" \| "failed" \| "canceled" \| "unknown"`, `subject_revision:string \| null` |
| Evidence | `evidence_id:string`, `version:string`, `kind:string`, `schema_digest:string`, `digest:string`, `data:object` (bounded inline immutable record) |

1. All results MUST echo the request ID and negotiated protocol version; Grain MUST match them to the outstanding pinned invocation and reject mismatched reference, scope or generation. A provider MUST NOT redirect an Observe response to another identity. Resolve MUST validate kinds/identifiers/scopes and canonicalize aliases only within authorized scope. Failed resolution MUST return null reference, generic redacted display/error as needed. “Unauthorized or unavailable” MUST be used when distinguishing nonexistent from forbidden would reveal private existence. Invalid syntax may be diagnosed without a remote lookup.
2. Available observations MUST contain nonempty revision, facts and validated bounded details (empty object permitted). Nonavailable observations MUST have null revision/facts/details and empty evidence; authorized confirmed deletion MAY return a Grain-stored tombstone through its observation availability, never infer success. Error MUST be non-null on error/unavailable/unauthorized, null on available; deleted MAY include a redacted explanation.
3. Facts MUST be sourced, not inferred from missing data. Absent/unreadable facts MUST be unknown/null. Available does not mean complete; a gate needing an unsupported fact MUST return unknown. `disposition` may be supplied only when authoritative provider evidence matches its declared fact-version mapping. Terminal alone MUST NOT imply success. Issue closed without confirmed resolution maps to closed, not resolved; unsuccessful task closure maps to its actual outcome, or unknown if unproven.
4. Provider-specific details MUST be namespaced by the registered kind/schema. A gate MUST declare support for the exact fact/details versions it consumes. It MUST NOT treat unknown fields/statuses as positive evidence. Human text, display colors, stored opaque provider state and event payloads MUST NOT substitute for facts. Providers MUST NOT manufacture Grain action receipts, human approvals or authoritative reviews. Gates requiring those MUST consume the existing authenticated Grain action/review collectors and their scope/policy records, not a provider's lifecycle claim.
5. Revision tokens MUST support equality only, scoped to tracked identity + provider pin + authorization partition. Changed relevant facts/details/evidence/subject MUST change the token; time/display-only changes need not. Tokens MUST NOT encode secrets. No consumer may sort tokens or compare across pins/scopes. Grain MUST maintain its own monotonic accepted observation sequence.
6. `observed_at` MUST be the time of actual source collection, not cache delivery; `valid_until >= observed_at`. Grain MUST record request start/receipt time, reject impossible future collection times beyond 30 seconds clock skew, and cap usable freshness at the minimum of provider validity and the gate's collection-age budget. A provider that cannot establish collection time MUST return unavailable for freshness-sensitive reads.
7. Grain MUST persist immutable observation records with reference/pin, authorization epoch, collector provenance, accepted sequence, received time, inline evidence and redacted payload digest. Evidence MUST be returned inline under $defs.evidence, validated against the registered kind/schema and canonical digest, and copied into immutable Grain-managed evidence before use by gates. Provider IDs are deduplication identities, never fetch handles. No URL or opaque external handle is dereferenced.
8. An available observation MAY lack evidence for advisory display; required predicates MUST have sufficient authenticated immutable evidence. Gate-consumed references bind canonical resource/subject, authorized task/controller scope, audience, version and expiry. Current read authorization is checked even for history; no remote snapshot handle is required.
9. Schema/pin changes MUST use an audited explicit migration: resolve identity equivalence, retain old observations, establish new watches and observe afresh. Old tokens/cursors MUST NOT be reused unless explicitly supported and validated. A fact meaning change requires a new fact version, invalidates dependent results and requires gate compatibility; no silent reinterpretation.

### Complete wire examples

The [fixture catalog](examples/README.md) contains complete Resolve requests/results, Observe requests/results with inline immutable evidence, redacted permission loss, all five Watch operations, poll fallback, cursor expiry, API edits and invalid messages. All real pins use full SHA-256 digests; canonical reference sketches above remain illustrative adapter outputs, not proof of service support.

An inaccessible resource has availability=unauthorized, null revision/facts/details, empty evidence, null display URL, validity equal to collection time and the generic access_unavailable error. A prior success MUST NOT fill those fields.

## 5. Watch and durable reconciliation {#TDS-5}

Watch is **pull only** in v1. All five operations use the same Grain-initiated MCP tool. Upstream webhooks may terminate at a separately configured provider and populate its queue; Grain does not expose a callback, delivery handle or webhook provisioning operation. Providers without durable history MUST advertise poll, not cursor. These hints never supply decision facts.

| Operation | Additions to common request (all include reference) |
|---|---|
| describe | `operation:"describe"`; no subscription created |
| ensure | `operation:"ensure", subscription_id, generation, expires_at, poll_seconds` |
| read | `operation:"read", subscription_id, generation, after_cursor, limit:1..100, wait_seconds:0..20` |
| renew | `operation:"renew", subscription_id, generation, expires_at` |
| cancel | `operation:"cancel", subscription_id, generation` |

Every result contains `protocol_version,request_id,status,modes,subscription_id,generation,cursor,replay,expires_at,min_poll_seconds,max_poll_seconds,events,has_more,error` per `$defs.watch_result`. Status is ready/unsupported/unauthorized/error; modes are cursor/poll. Replay is gap_free/gap_possible/expired. A hint is exactly `{subscription_id,generation,event_id,cursor,reference_key,emitted_at}`. It is at most 16 KiB, with no facts, URLs, task IDs or credentials.

1. Ensure MUST durably fence the provider queue before returning its initial cursor. Within a subscription/generation, cursor events have stable IDs, immutable content and exclusive replay after the supplied cursor. Cursor is opaque, partition/pin/generation scoped, and not sorted by Grain. Read returns a bounded ordered page and its checkpoint in the **reply**, including an empty-page high-water cursor; `has_more` reports more buffered events. Repeating a read cursor MUST NOT skip any event; newly appended events may appear after a previously returned empty page. Grain MUST NOT infer ordering from emitted_at. Providers MUST preserve replay through the advertised retention or report cursor_expired, never silently skip.
2. Ensure, renew and cancel MUST be idempotent by subscription ID/generation and request ID; identical request IDs require identical arguments. Initial ensure retry returns the same starting fence until expiry. Re-ensure of a live generation MUST NOT move that fence. Renew extends expiry (at most one hour from now) without changing fence/checkpoint; it MUST NOT revive an expired generation. Cancel replies ready, empty events, null cursor/expiry, even if already absent. Stale generations cannot affect a newer one. Describe has null subscription/generation/cursor/expiry and empty events. Poll ensure/renew returns null cursor, gap_possible and a durable expiry; poll read returns empty events and null cursor. Grain uses Observe timers instead of pretending poll has a lossless history.
3. Grain MUST persist intent and lease ownership before ensure. It then stores the returned fence, marks dirty, collects baseline Observe, drains read after the fence, and schedules another Observe if any hint raced that read. Receipt/checkpoint/dirty/outbox publication MUST commit atomically as [TDS-14](implementation.spec.md#TDS-14) specifies. If no hints race, the baseline is usable under its freshness budget. A dirty read never clears a later hint. New arrivals after a drained high-water cursor remain retrievable on the next read.
4. A lost/expired queue, provider restart without retained history or poll-only provider MUST establish a new subscription generation, record a coverage gap, Observe after ensure, then Observe once more after the first drain (for poll, after the next allowed poll). Before that second read required evidence remains unknown. Initial baseline evidence is also held behind the persisted initial-drain coverage barrier until TDS-14 marks it current. Recovery proves current state only: no claim that intermediate changes/occurrences or ABA transitions were recovered. Retain periodic reconciliation. An immutable snapshot that the service can no longer read is unavailable; do not substitute latest.
5. Events MUST match the outstanding authenticated pin/partition/subscription/generation/reference key. Reference key is lowercase SHA-256 of RFC 8785 canonical TrackedResourceReference, including tracking scope. IDs deduplicate within subscription/generation; conflicting same-ID content quarantines the provider. Old generations and unrequested responses are discarded. Errors MUST return empty events, null cursor and has_more=false; no error advances a checkpoint. Cursor expiry uses code cursor_expired and replay=expired.
6. Only one collector lease per authorized tracked view may commit. Observe CAS MUST check pin, accepted sequence, reference membership revision, authorization epoch, lease owner/fence and the dirty generation captured at start. If dirty changes during a read, the response may be retained diagnostically but MUST NOT clear dirty or authorize closure. Stale or revoked writes MUST be rejected. Current Observe, never event payload, establishes state. Same-token refresh advances freshness/accepted sequence but not material-change progress.
7. Read pages are immediately drained for at most 10 pages per worker turn, then yield durable dirty work. Watch call deadline is at most 30 seconds including at most 20 seconds long poll. stdio calls are currently serialized: queued calls count against deadline, and resource reads MUST yield after one long poll so other tools can run. Local cancel stops a call context; remote cancel is separately queued best-effort with its generation. Late replies cannot commit.
8. Observe reconciliation is default 60 seconds, bounded 30..3600 seconds by provider/service policy; even cursor watches reconcile at least hourly where permitted. Cursor reads run each 30 seconds or the provider minimum, whichever is larger, with immediate bounded draining when has_more. Retry backoff is exponential 60..3600 seconds with jitter; longer Retry-After wins and evidence becomes stale. Token buckets cap each provider and each grant at 60 requests/minute, burst 10, with at most 4 concurrent per provider. Three failures or 15 minutes unavailable raises one deduplicated blocker. Ten consecutive failures opens the durable circuit for 1 hour; one half-open probe is allowed, success resets the failure count, failure reopens for 1 hour. Unauthorized/invalid/unsupported responses hold pending grant/config repair, not automatic probes. Restart does not reset counters.
9. Renew halfway to expiry. On restart, reauthorize first, acquire a new local lease fence, then resume saved cursor if the remote generation is live; otherwise use gap recovery. Removing the last recipient immediately fences local use and persists cancel intent; cancel retries under bounded cleanup authority until acknowledged or recorded remote expiry. Removal never waits for remote success. Renewal/cancel must not acquire resource-write permission or retain an attempt credential.

## 6. Security, isolation and limits {#TDS-6}

1. Separate authorization MUST apply to attachment/edit, controller Resolve/Observe, controller Watch, person/agent evidence reads and every action. Grain MUST use current grants/RBAC/policy, not a resource URL or provider assertion, to authorize. An evaluator sees only its declared bounded snapshot scope. Typed actions MUST recheck present permission and evidence even if an earlier button was authorized.
2. Default cache/subscription partition MUST include tenant/installation, canonical tracked identity, provider pin/config revision, task grant lineage/principal, authorization epoch and field/redaction policy. Cross-task dedup MAY occur only after Grain proves equivalent effective access and visibility. Physical sharing MUST retain separate authorized recipient views and MUST NOT use unioned privileges. Cache hit timing/status, watcher counts and errors MUST NOT reveal private resources across partitions.
3. Revocation or policy/credential expiry MUST advance the authorization epoch and immediately fence future reads/writes/actions and evidence consumption; invalidate that view's current cache, pending fan-out, gate results and waits requiring access; unsubscribe that recipient and cancel/expire unneeded registrations. Raw historical records MAY remain for restricted audit retention, but MUST NOT remain accessible through normal caches or snapshot handles. Restoring access requires a fresh observation, not reuse of a prior epoch's “met”.
4. There is no inbound Grain resource webhook in v1. A provider receiving upstream webhooks MUST authenticate/deduplicate them under its separately reviewed adapter contract; it exposes only authenticated pull replies to Grain. Configuration credential rotation MUST fence affected connections, grants and in-flight calls; no inferred signature or delivery handle is supported.
5. Identifiers MUST validate against pinned schemas; reject ambiguous authority, oversized keys, traversal/control characters and malformed locators. Display URLs MUST use allowlisted HTTPS authority/path rules (or validated relative Grain routes), reject userinfo/credentials and executable schemes, and render as inert escaped links. Providers MUST NOT request arbitrary HTTP fetching by Grain. Evidence is inline and copied to registered Grain snapshot records; adapters performing remote requests MUST enforce configured service allowlists, DNS/IP/redirect checks, TLS and credential audience. Internal CITC/Buganizer routing requires explicit configured network policy, never trust of a user URL.
6. Summaries/details/identifiers/cursors MUST be classified/redacted before UI, logs, notifications, agent prompts or audit exports. Raw service responses, credentials, private bug text and unbounded transcripts MUST NOT be stored by default. Text MUST be treated as untrusted data, never executable instructions. Audit identity/pin/times and redacted errors without secrets.
7. Per call: deadline at most 30 seconds, request/response at most 256 KiB UTF-8 JSON, details at most 32 KiB, maximum JSON depth 16, 100 inline evidence records, summary 2 KiB, token/identifier scalar 2 KiB, event 16 KiB. Provider schemas MUST bound every string/array/object and reject over-limit input before remote work. No silent truncation may establish satisfaction. Oversized/incomplete evidence yields unknown with a limit error.
8. Per task defaults: at most 100 references and 200 gate-reference bindings; one logical subscription per tracked view, queued hint coalescing by subscription with a dirty bit after 100 pending receipts. These limits MAY be narrowed or explicitly revised under audited deployment policy, never silently exceeded. Providers MUST honor cancellation/timeouts; Grain MUST isolate/quarantine a persistently nonconforming provider.
9. Missing/uninstalled/unhealthy providers MUST produce unavailable with durable retry or installation blocker. Grain MUST NOT auto-install, broaden grants, substitute another provider or fall back to cached success.

## 7. Gates, freshness and atomic decisions {#TDS-7}

1. Gate dependencies MUST include TaskResourceReference revision, immutable observation ID/sequence, provider/fact pins, subject scope, freshness/availability, authorization and dirty/covered journal generations. Initial built-ins use focused authorized reads under BG-4, not a universal snapshot/page contract. Missing required evidence remains unknown and required.
2. Gate contracts MUST set an evidence-age budget (default 300 seconds, allowed 0–3600; zero requires a collection in that evaluation cycle). Grain MUST mark evidence stale at the earliest provider validity or budget expiry and schedule reevaluation then. Previously met results MUST cease to authorize closure/actions when stale. A cursor hint MUST NOT extend freshness without Observe.
3. Accepted relevant changes, dirty hints, grant changes, reference/contract/policy edits, provider migration, expiry, PR head or workspace subject changes MUST invalidate affected bundles and evidence-scoped waivers/actions. Waivers MUST name gate/contract, exact reference/subject/predicate, evidence and authority scope with expiry, following the gates waiver contract. No waiver of one snapshot automatically covers later content.
4. Grain MUST evaluate first on tracked-resource changes, coalescing duplicate wakes. It MUST dispatch an agent only for useful authorized actionable work identified under the gates work/action rules, not merely because status changed or a timer elapsed. Unrelated actionable work MUST remain dispatchable while another tracked resource is awaited. A whole-task dispatch hold requires explicit scope/reason; waiting alone is no such hold.
5. Atomic closure MUST recheck one consistent gate bundle, current task contract/policy/authority epoch, incoming/processed message watermarks, current authorized/fresh observations, no pending relevant dirty invalidation and settled/fenced live writers/operations. Observation and closure transactions MUST serialize on the relevant evidence revision/authorization fences. A competing change wins by invalidating the bundle or is recorded after the historical close under explicit regression policy.
6. Grain cannot atomically lock an external service with its DB. Closure MUST record observed cutoff/receipt and evidence digest; it MUST NOT claim zero remote-change risk. Changes discovered after closure MUST notify or reopen only under explicit lifecycle policy. Deadlines/no-progress limits MAY escalate/hold/recheck, MUST NOT auto-pass or silently waive.

## 8. Built-in tasks, graph rules and migration {#TDS-8}

1. Grain MUST provide a built-in `tasks` provider with the same protocol and immutable installation/task identity. Its v1 lifecycle/outcome facets MUST distinguish closure, failed/canceled outcome, trustworthy success and successor/rollover facts. Report delivery and merged integration remain separate authenticated gate-collector evidence under TDS-13, never inferred from task closure; unregistered facets return unsupported_selector. An unknown legacy outcome MUST NOT be synthesized into success.
2. Required children MUST have explicit required success gates (or a deliberately different approved predicate); advisory and independent spawned links MUST NOT block closure. A stopped/closed child is not proof of successful work. Failed required children require repair, authorized replacement or scoped waiver.
3. Import MUST preserve existing `depends_on`/`child-of` dispatch predicates, `wait_for_task` settled-vs-met semantics, `resume_later` conditions and one-shot park-owned link cleanup. Required completion gates MUST NOT be inferred. Existing `tasks-closed` concrete goals MUST preserve their exact current predicate/provenance; switching to success requires an authorized contract change.
4. Local cycle checking MUST transactionally inspect the union of explicit dispatch waits and required completion waits on edits, migration, reopening and retargeting. Reject self/cyclic blocking edges, including two concurrent edits that form a cycle. Permission-filtered diagnostics MUST NOT reveal hidden task titles/edges. Advisory edges MAY form cycles. Unknown/external graphs MUST be labeled unverifiable, retain bounded patience and escalate no progress; they MUST NOT be automatically assumed acyclic or have requirements dropped.
5. Reader-first additive migrations MUST preserve legacy fields/projections and keep Go/SQL task state parity. Each task MUST have one completion authority and epoch; shadow observations MUST NOT alter legacy decisions. Authoritative resource/gate rollout depends on central gate lifecycle ownership.
6. Rollback MUST fence workers, preserve records/pins/cursors/audit and explicitly reverse handoff only for contracts representable under legacy semantics. Unsupported readers MUST hold migrated tasks for a compatible version; they MUST NOT silently downgrade success to closure, remove gates or discard unknown provider records. Restart/backup restore MUST reauthorize/reconcile before using cached evidence.

## 9. Task UI, API and agent tools {#TDS-9}

The following routes/tools are proposed, not deployed:

| Agent tool | Proposed API | Contract |
|---|---|---|
| `resource_resolve` | `POST /api/tasks/{id}/resources/resolve` | Read-only candidate preview under an explicitly named current resolve grant; never attaches or grants access. |
| `resource_attach` | `POST /api/tasks/{id}/resources` | Input candidate, explicit grant_id, provider pin, tracking, relation and expected contract revision; returns a task-local reference or redacted unresolved blocker. Resolve preview is explicit and read-authorized. No auto-grant/gate. |
| `resource_list` | `GET /api/tasks/{id}/resources` | Authorized paginated references with relation, observation/freshness and linked gate IDs; at most 100 per page. |
| `resource_get` | `GET /api/tasks/{id}/resources/{reference_id}` | Current authorized bounded view, immutable evidence references and wait/error state. |
| `resource_edit` | `PATCH /api/tasks/{id}/resources/{reference_id}` | Full candidate/tracking/grant replacement under expected reference/contract revisions; explicit pin/tracking/retarget invalidation, no implicit grant or requirement weakening. |
| `resource_remove` | `DELETE /api/tasks/{id}/resources/{reference_id}` | Expected contract revision required; refuses live required bindings unless part of an authorized atomic gate edit. |
| `resource_watch_change` | `POST /api/tasks/{id}/resources/{reference_id}/watch-change` | Input after accepted observation sequence, max wait 30 seconds or explicit park; returns changed/unchanged/unavailable and new sequence. A park persists a cursor-fenced wait and stops the run under existing sandbox-preservation rules. |

1. All contract mutations MUST require expected contract revision, idempotency key and appropriate edit authority. Proposed API grants MUST be registered under the existing API capability system, with separate task-bounded resource read/edit/watch permissions; generic task metadata authority MUST NOT imply requirement relaxation or external read/watch. No new actual grant is created by this spec.
2. Watch-change MUST return immediately if a change already occurred after the supplied sequence and MUST register/replay transactionally otherwise. It watches revision, availability or freshness changes, not success, and MUST NOT mutate dispatch/completion contracts. A park MUST preserve independent declared links and report permission/access loss explicitly.
3. UI MUST show canonical identity/safe link, tracking facet/mode/selector, status/unknown/error, collection age and freshness, required outcome through gate bindings, dispatch holds and wait driver/deadline/escalation separately. UI MUST NOT equate green resource status with task completion. Actions MUST use typed gate buttons and current server authorization.
4. Add/remove and contract changes MUST have permission-aware previews and audit. Access-denied views MUST redact existence/private fields. User-requested access follows existing explicit permission flows; attach MUST NOT automatically request or grant it. New UI belongs in Angular; shared APIs MUST keep classic consumers working.

## 10. Acceptance scenarios {#TDS-10}

These are required future conformance tests, not runtime tests added by this documentation PR. [TDS-11–16](implementation.spec.md) specify registry/grants, transaction boundaries, retention, exact API inputs, migration mappings and shared contract ownership; [readiness](readiness.md) maps them to code entry points.

| Scenario | Required result |
|---|---|
| One PR, tasks A/B with different gates | Same canonical PR; A waits for integration, B can finish a head-scoped report; neither relation grants merge authority. |
| Shared PR, task C has narrower fields/grants | Separate authorized observation view; no private review content, existence/cache timing or watcher-count leakage; no use of A's credential. |
| Workspace S1 changes to S2 during evaluation | Follow-latest subject invalidates old review/waiver/action/result; close fails CAS. Pinned S1 remains S1 and needs exact S1 submission proof. |
| Inaccessible/nonexistent bug | Indistinguishable redacted access-unavailable view under unauthorized grant; required resolved gate unknown; no API/support claim. |
| Required child fails/cancels | Legacy settled wait can return failure; required success gate cannot pass. Advisory/independent children never hold closure. |
| Failed task has only legacy completion timestamp | New success gate unknown unless actual trustworthy outcome evidence exists; do not infer success from close/run exit. |
| Provider change hint missed during downtime | Durable reconciliation Observe discovers current state and fans out to evaluators; no agent polling loop. |
| Event races registration or baseline read | Persisted intent/fence, buffered receipts/replay and second read cover the gap; no lost dirty work. |
| Duplicate/out-of-order hint; ABA resource state | Coalesced hints invoke current Observe. Ordered Grain sequence never regresses; gates use current subject/evidence, not token ordering or stale payload. |
| Old Observe completes after newer commit/revocation | CAS/lease/reference/auth epoch fences reject write; no stale evidence resurrection. |
| Crash after remote watch ensure, before local ack | Reuse subscription key/generation, reconcile one logical registration and resume durable timers. |
| Crash after cursor receipt, before gate wake | Atomic dirty/outbox record survives; replay drives evaluation exactly once logically, duplicate delivery harmless. |
| Expired cursor or provider loses events | Mark gap, re-fence/full Observe, retain reconciliation; never infer unchanged from missing history. |
| Poll throttled, provider timeout/uninstalled | Stale/unknown with bounded retry and deduplicated escalation; no fallback met, install or authority expansion. |
| Permission revoked while views shared | Revoke affected recipient/cache/handles/results, reject late writes/buttons, keep independently authorized recipients; restored access requires fresh read. |
| Malicious URL, forged pull reply/evidence, oversized output | Reject/sanitize before fetch/store/fan-out; no code execution, remote credential leak or task lookup oracle. |
| Token same but freshness expires | Expiry invalidates usable met bundle and wakes evaluator; time-only refresh gives no false progress credit. |
| Two edits create local cycle | One transaction rejected; hidden graph details redacted. Unknown external cycle hits bounded escalation without removing obligations. |
| PR pending while report can advance | Evaluator dispatches useful report work with wait intact; no task-wide automatic hold. |
| Deadline reached; evidence stale | Recheck or escalate; never pass. |
| Tracked-resource change/message/live push races closure | Atomic evidence/message/operation fences reject stale close; later remote discoveries follow recorded regression policy. |
| Pin/fact schema upgrade or rollback | Explicit compatible migration and fresh Observe; no cross-pin token ordering or old-result reuse, one lifecycle owner. |
| Remove reference required by gate | Reject or perform authorized atomic contract edit with audit; no dangling ignored condition. |
| Restart after backup restored revoked grant | Ordinary restart reauthorizes and reconciles; backup restore holds every old grant until fresh person approval in the new recovery UUID, including revocations lost from the backup. No old cache or subscription restores authority. |
