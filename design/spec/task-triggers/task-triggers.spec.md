# Capability-backed task trigger specification

**Status:** proposed, normative for the design described here; not implemented.

**Specifies:** [Extensible capability-backed task triggers](../../design/task-triggers.design.md).

MUST, MUST NOT, SHOULD, SHOULD NOT and MAY have their BCP 14 meanings. `TTS-*` IDs are stable requirements. All extension methods, action IDs, records and management tools below are hypothetical contracts for future implementation. Existing APIs and behavior are identified explicitly in §12. Documentation approval authorizes no implementation, live subscription, configuration edit, deployment or follow-up task filing.

## TTS-1: Terms and invariants {#TTS-1}

A source adapter collects facts. A tracked resource is observed through a resource provider. A task dependency remains the existing prerequisite relationship. A gate interprets evidence for existing work. A trigger matches an occurrence or a recorded state transition to a standing authorization to create work. Grain MUST own journal admission, matching, filing, approval, quota accounting and task lifecycle. Providers MUST NOT set task status, grant capabilities or choose placement.

A source subscription is collection state, a binding is filing authority, and a firing is a durable disposition of a match. They MUST have separate identities and access controls. A tracked-resource attachment, event read permission, gate result or authenticated webhook MUST NOT imply binding-management, task-creation or automatic-approval authority. Transient occurrences MAY lack an enduring tracked resource; a PR request refers to a PR, but the request and PR are distinct identities.

## TTS-2: Discovery, pins and provider protocol {#TTS-2}

An installed capability server MAY advertise an `event_describe` MCP tool in `tools/list`. This is a proposed extension separate from existing `mint_*`, `release_*`, `configure` and proposed `gate_*` methods. Grain MUST NOT infer event support from a mint method, annotation, arbitrary tool name or provider prose. Installation and credential configuration require their existing authority; discovery has no subscription or filing side effects.

The closed descriptor contract is:

| Field | Required type and meaning |
|---|---|
| `protocol_version` | Literal `1.0` |
| `provider` | Canonical installation-qualified provider ID |
| `release` | Immutable artifact digest, never a floating tag |
| `config_revision` | Immutable approved installation configuration revision; no secret values |
| `source_types` | 1..32 descriptors, each with stable `id`, `version`, `source_schema`, `kinds`, `recovery`, `methods` |
| `source_schema` | Locally resolved JSON Schema and canonical digest for source arguments |
| `kinds` | 1..128 entries with `kind`, `version`, `extension_schema`, digest, identity rule, revision ordering rule, and `semantics` = `occurrence` or `state` |
| `recovery` | `history` = `complete`, `bounded`, `state_only` or `none`; retention seconds (nullable); stable identity boolean; cursor support boolean; reconciliation interval and documented loss limits |
| `methods` | Fixed registered MCP method names for open/read/reconcile/close, not executable URLs |

Descriptors and source args MUST validate against the supported capability schema subset, with defaults resolved before canonical JSON hashing. Schemas MUST be local, bounded to 64 KiB/depth 16, with no remote `$ref` resolution. Object records are closed except explicitly schema-validated `extensions`. Provider text is untrusted. No credentials may occur in arguments, descriptors, events or opaque cursor logs; use installation-owned secret handles.

The proposed adapter methods are `event_open`, `event_read`, `event_reconcile`, `event_close`. They use `tools/call` with this exact common request record: `{protocol_version, request_id, provider_pin, subscription_id, generation, source, after_cursor, limit, deadline}`. `provider_pin` is the binding provider record `{id, release, source_type, version, schema_digest, config_revision}`. `source` is validated source args; `after_cursor` is null or an opaque string at most 4096 bytes; `limit` is 1..500; `deadline` is an RFC3339 UTC instant. IDs are server assigned strings at most 512 bytes; generation is a positive integer. Open and close are idempotent by subscription/generation and request ID. Closing one consumer MUST NOT close a still-authorized shared source subscription.

Read/reconcile replies are `{protocol_version, request_id, subscription_id, generation, events, next_cursor, has_more, coverage, error}`. `events` is at most `limit` envelopes (§3); total reply is at most 1 MiB. `next_cursor` is null or bounded opaque string. `coverage` is `{mode, from, through, gap}`, with mode from the descriptor's history enum, UTC bounds nullable and gap null or `{code, from, through}`. `error` is null or `{code, retryable, retry_after_seconds, message}`; codes are `unsupported_version`, `unsupported_kind`, `invalid_config`, `unauthorized`, `not_found`, `cursor_expired`, `rate_limited`, `unavailable`, `invalid_event`, `limit_exceeded`. Error replies MUST NOT advance a cursor. Partial progress uses a successful bounded page and an explicit coverage gap, never an ambiguous error plus cursor. Open/close return the same record with an empty event list. Unknown fields/major versions/kinds MUST fail closed; optional metadata evolution requires a negotiated version. Read-only hints are not enforcement.

The shared terminology is **tracked resources** and **resource providers**; task dependencies remain prerequisite relationships. Task #45's draft inspected at `6d2ffe338` specifies `resource_<method>_resolve_v1`, `resource_<method>_observe_v1`, and `resource_<method>_watch_v1` (TDS-3). These replace the historical `dependency_*` spellings in its earlier draft. Implementations MUST pin/translate or reject protocol versions explicitly; a documentation rename is not runtime support. Watch uses describe/ensure/cancel and authenticated hints `{subscription_id,generation,event_id,cursor,reference_key,emitted_at}`; these are change hints, not authoritative facts or a complete occurrence record. Observe supplies a reference, generation, equality-only revision token, facts/evidence and observed/valid-until times (TDS-4). Grain MUST establish Watch's event/cursor fence before baseline Observe, buffer/drain hints and commit dirty generations/checkpoints as TDS-5 requires. Observe does not itself promise the collection watermark previously returned by Watch. A hint may prompt a fresh Observe; it MUST NOT be promoted into a review-request event with invented actor/head/request epoch. State-transition triggers MAY consume Grain-authenticated accepted observation transitions with independent binding authority. Gate waits SHOULD consume the journal with independent consumer cursors as proposed in #44 MGS-6. Providers MUST NOT move Grain consumer checkpoints.

The registry MUST bridge this proposal's installation-qualified `provider.id` to #45's `ResourceProviderIdentity={capability_id,method}`, approved `config_revision`, artifact release and authorization partition. `provider.id` is a registry key, not a replacement spelling for `capability_id`. The bridge MUST be immutable for the full provider pin, including `config_revision`; configuration changes require a new reviewed pin and source-generation handoff, never retarget an existing registry key silently. A source adapter may implement both protocols but MUST pin each protocol/schema separately. Resource references MUST map through a registered bijection from `(provider, source, resource.type, resource.id)` to the canonical provider/kind/identifier and explicit tracking scope; ambiguous or lossy mappings hold. The envelope's `resource.revision` maps only to its declared subject revision (for example a PR head); it MUST NOT be mistaken for #45's full-observation revision token. Transient events/time ticks need no TrackedResourceReference. Both protocols keep their native envelopes and bounded error vocabulary; adapters translate semantics explicitly rather than relabeling fields.

Shared source collection MUST enforce the stricter applicable service/read/watch partition limits. The trigger backoff defaults in §9 govern filing/occurrence adapters; resource-provider Observe/Watch retains TDS-5's 30-second-to-one-hour poll bounds, 60-second default, hourly reconciliation where allowed, token buckets and three-failure/15-minute escalation. Sharing MUST NOT weaken either consumer's liveness or privacy contract; incompatible requirements use separate subscriptions. Remaining table/package naming is an implementation coordination choice, not an unresolved authority contract.

Provider upgrades MUST produce a reviewed descriptor/pin migration with old/new schema, identity compatibility and cursor mapping. No new provider may interpret opaque old state silently. Incompatible migration holds the binding, retains old state and requires explicit baseline/replay selection. Removing an installation holds dependent bindings and preserves attribution, errors and history; it MUST NOT remove tasks or silently replace providers.

## TTS-3: Normalized event envelope {#TTS-3}

Every ingested event MUST have the following closed record. Required nullable fields are present as null rather than silently invented. Times are UTC RFC3339; identifiers are case-preserving canonical provider identifiers, not display titles.

```json
{
  "protocol_version": "1.0",
  "provider": "capability:github/install-17",
  "provider_version": "1",
  "source": "github.com/repository/12345",
  "event_id": "delivery:8c639:review-requested:team:77",
  "kind": "github.pull_request.review_requested",
  "kind_version": "1",
  "occurred_at": "2026-11-16T19:00:00Z",
  "received_at": "2026-11-16T19:00:02Z",
  "resource": {"type": "github.pull_request", "id": "github.com/repository/12345/pull/812", "revision": "head:abc123"},
  "provenance": {
    "transport": "webhook", "authentication_ref": "verification:901",
    "actor": "github:user:12", "actor_kind": "human",
    "installation": "install-17", "origin_task": null,
    "origin_action": null, "causation_id": null, "chain_id": null, "depth": 0
  },
  "extensions": {"repository_id": 12345, "number": 812, "head": "abc123", "recipient_kind": "team", "recipient_id": 77, "request_epoch": "delivery:8c639", "url": "https://github.com/ai-on-gke/grain/pull/812"},
  "payload": {"title": "Please review this change"}
}
```

Provider/source/event ID/kind/version strings are 1..512 UTF-8 bytes. Resource is null or `{type,id,revision}` with revision nullable and strings bounded to 512 bytes. `occurred_at` MAY be null if the source cannot authenticate occurrence time; receipt time MUST be stamped by Grain, and any source receipt time retained only as extension data. Authentication references and Grain causation MUST be generated/verified by Grain, never trusted from payload claims. Actor kind is `human`, `automation` or `unknown`; transport is `webhook`, `poll`, `stream`, `time` or `internal`. Unknown actor MUST NOT be promoted to human from display text. Depth is 0..16.

`extensions` MUST validate against the pinned event-kind schema, at most 16 KiB/depth 8. `payload` MUST be an object of untrusted data, at most 32 KiB/depth 8; whole encoded event is at most 64 KiB. All limits are encoded UTF-8 bytes. Provider-specific extension fields are namespaced by the provider/kind contract, not added at envelope top level. Oversized or invalid events MUST be quarantined with safe metadata and visible counters, not truncated into a potentially different predicate. External URLs MUST be parsed/allowlisted by host and resource; MUST NOT be arbitrary fetch destinations or authorization proof. Raw bodies, signatures and secrets MUST NOT be exposed through shared event APIs.

Time extensions include `scheduled_at`, `local_date`, `cadence_epoch`, `time_zone` and nullable `covered_through`; a normal tick has null coverage end, while a collapsed catch-up names its covered range. The scheduled instant, rather than worker execution time, determines tick identity. Review-request extensions in the example schema are all required; missing head or stable recipient/request identity holds normalization rather than guessing from a title.

Grain MUST retain the first accepted `received_at`, receipt sequence and verification provenance on the immutable event. Later delivery attempts have separate receipt/audit metadata; duplicate content comparison excludes those Grain-generated transport fields and compares canonical source facts.

Stable identity is `(provider, source, event_id)`; redelivery cannot change its canonical normalized content. A conflicting duplicate MUST be quarantined, with no second match. A delivery ID is sufficient only if provider redelivery preserves it; where aliases exist the adapter MUST resolve them to one occurrence identity. If stable occurrence identity cannot be supplied, the adapter MUST declare weaker delivery and MUST NOT advertise exactly-once filing. Clock ordering is not resource ordering. Grain MUST retain receipt sequence separately and MUST NOT regress state on older provider revisions. Opaque Git SHAs are equality tokens, not lexically ordered revisions.

## TTS-4: Binding schema and examples {#TTS-4}

[wire.schema.json](wire.schema.json) defines the closed binding and the two concrete source configurations below using JSON Schema 2020-12; `$defs.event`, `$defs.github_review_requested_extensions` and `$defs.time_occurrence_extensions` define the normalized envelope and the example event-kind extensions. Validate against the named definition as well as the cross-field/byte-limit rules here. The schema's root validates bindings. Other provider/source/state-edge contracts require registered, pinned schemas before admission; these two examples do not constitute support for every provider. These are exact proposed configuration examples, not currently accepted schedule/declaration syntax. Digests shown as `sha256:example-*` are documentation placeholders; admission MUST require actual computed digests.

```json
{
  "api_version": "grain.trigger/v1", "id": "daily-review", "version": 1,
  "provider": {"id": "builtin:time", "release": "sha256:example-time", "source_type": "calendar", "version": "1", "schema_digest": "sha256:example-calendar", "config_revision": "1"},
  "source": {"type": "calendar", "recurrence": {"kind": "daily", "time_of_day_minutes": 540}, "time_zone": "UTC", "dst": "skip_gap_first_fold", "calendar_version": "tzdb:2026b"},
  "event_kind": "time.occurrence", "kind_version": "1", "mode": "occurrence",
  "filter": {"all": []},
  "template": {"id": "repo:template:ai-on-gke:grain:daily-review", "revision": "sha256:example-template", "tracking": "pinned"},
  "parameters": {"date": {"from": "/extensions/local_date", "type": "string", "max_bytes": 10}},
  "target": {"repository": "ai-on-gke/grain", "base": "main"},
  "identity": {"owner": "user:review-owner", "service": "service:trigger-filer"},
  "permissions": {"read_repositories": ["ai-on-gke/grain"], "task_capabilities": [], "actions": ["task.create_from_trigger"]},
  "policy": {"admission": "standard", "approval": "proposed", "approval_authorization": null, "auto_merge": false, "auto_approve_proposals": false},
  "repeat": {"policy": "new", "key_fields": ["/source"], "on_head_change": "ignore", "active_limit": 1},
  "delivery": {"initial": "baseline", "enable": "future_only", "catch_up": "hold", "max_age_seconds": 86400, "debounce_seconds": 0, "batch_max": 1},
  "state": "paused",
  "quotas": {"matches_per_hour": 20, "tasks_per_day": 2, "queue_size": 20, "concurrent_tasks": 1, "fanout_per_event": 1, "cost_units_per_day": 100},
  "loops": {"automation": "allow_bounded", "max_depth": 3, "max_chain_tasks": 10}
}
```

```json
{
  "api_version": "grain.trigger/v1", "id": "requested-pr-review", "version": 1,
  "provider": {"id": "capability:github/install-17", "release": "sha256:example-github", "source_type": "github_review_requests", "version": "1", "schema_digest": "sha256:example-review", "config_revision": "7"},
  "source": {"type": "github_review_requests", "host": "github.com", "installation": "install-17", "repository_id": 12345, "repository": "ai-on-gke/grain", "recipient": {"kind": "team", "id": 77}},
  "event_kind": "github.pull_request.review_requested", "kind_version": "1", "mode": "occurrence",
  "filter": {"all": [{"path": "/extensions/recipient_kind", "op": "eq", "value": "team"}, {"path": "/extensions/recipient_id", "op": "eq", "value": 77}]},
  "template": {"id": "repo:template:ai-on-gke:grain:requested-review", "revision": "sha256:example-template", "tracking": "pinned"},
  "parameters": {"pr": {"from": "/extensions/number", "type": "integer", "max_bytes": 20}, "head": {"from": "/extensions/head", "type": "string", "max_bytes": 128}, "title": {"from": "/payload/title", "type": "string", "max_bytes": 1024}},
  "target": {"repository": "ai-on-gke/grain", "base": "main"},
  "identity": {"owner": "user:review-owner", "service": "service:trigger-filer"},
  "permissions": {"read_repositories": ["ai-on-gke/grain"], "task_capabilities": [], "actions": ["task.create_from_trigger", "task.coalesce_trigger"]},
  "policy": {"admission": "standard", "approval": "proposed", "approval_authorization": null, "auto_merge": false, "auto_approve_proposals": false},
  "repeat": {"policy": "coalesce", "key_fields": ["/resource/id", "/extensions/recipient_kind", "/extensions/recipient_id"], "on_head_change": "new_request_only", "active_limit": 1},
  "delivery": {"initial": "baseline", "enable": "future_only", "catch_up": "hold", "max_age_seconds": 86400, "debounce_seconds": 30, "batch_max": 20},
  "state": "paused",
  "quotas": {"matches_per_hour": 100, "tasks_per_day": 20, "queue_size": 100, "concurrent_tasks": 1, "fanout_per_event": 1, "cost_units_per_day": 1000},
  "loops": {"automation": "hold", "max_depth": 3, "max_chain_tasks": 10}
}
```

A user recipient uses `{"kind":"user","id":12}` and the corresponding filter. Stable recipient IDs MUST be resolved under installation scope; team slug/display name changes MUST NOT retarget authority. Team support requires a new normalized extension/schema: the current receiver only exposes requested reviewer login (§12). A team request is one trigger event, not one per member. Member fan-out requires separate explicit binding approval and fan-out quota.

Binding `version` is a monotonic configuration revision. Every edit MUST be audited and use expected-version CAS. Any source, filter, mapping, provider, template, permission, policy, target or repeat change MUST create a new immutable matching version. Operational pause/resume MAY retain matching version with a separate audited control revision. A binding ID MUST never be recycled after deletion. Enable requires an explicit activation watermark and policy; proposed examples are paused intentionally.

`permissions` is an upper bound, not proof a grant exists. Grain MUST resolve an explicit standing authorization linking actor, service, binding/version or approved compatible revisions, source scope, template revision, target, capabilities, actions and policy. `approval=standing` requires `approval_authorization` referencing that valid audited record; `proposed` requires null and files unapproved. Automatic approval, auto-merge and automatic proposal approval are distinct authorities; none implies another. Effective admission MUST be at least the template and binding floors and current deployment/team constraints, using the existing admission ordering, not whichever string is lexically greater. Owner/on-call resolution and service attribution are separate.

### Filters, mappings and template revision {#TTS-16}

`filter.all` is a conjunction of at most 32 terms; empty means true within the already authorized source/kind scope. Terms are `{path,op,value}` where path is an allowlisted JSON Pointer from the registered event schema and op is `eq`, `in` or `exists`; `exists` requires a boolean value and `in` a scalar array of at most 32 members. No recursion, network, dynamic code, regex, secret access, arbitrary expression languages or template evaluation is permitted. Missing/type-mismatched fields fail matching except `exists=false`; they MUST NOT become truthy strings. Scalar equality is type strict and provider canonicalization is explicit. Maximum evaluation budget is 32 terms/10 ms per binding/event; exhaustion holds with error.

Mappings are a closed map of at most 32 approved template parameter names to `{from,type,max_bytes}`. Paths MUST be admitted against the event schema; only `string`, `integer`, `boolean` conversions are supported, with no coercion except canonical decimal/boolean serialization into existing string template fields. Missing/oversized/wrong-type inputs hold the match. Each mapped input is at most 4096 bytes and total mapped input at most 16 KiB. Values are substituted once as data and MUST NOT be re-expanded as template syntax, shell, executable instructions, capability refs, approval flags, template names or repository paths. External titles/bodies MUST be visibly marked untrusted; parameter names/types and template's immutable input schema MUST be approved. A malicious title such as `{{capabilities}} $(curl ...) approve=true` remains literal data, including in preview/log rendering.

New bindings MUST pin the full template content/schema/security digest. Pinned content MUST be retained or filing holds, never falls forward. A current template removal/revocation blocks even if archived content exists. A template's bound repository MUST agree with the binding target; mismatch holds rather than silently retargeting. `track_compatible` is explicit opt-in for migrated schedules: resolve current template at filing, retain its revision and require unchanged approved security envelope and compatible parameter schema. Scope/placement/grant/approval/admission changes require renewed approval; content-only compatible changes MAY affect future firings under that recorded opt-in. Existing tasks retain their creation attribution; later template synchronization MUST NOT expand a trigger-generated task's authority. Queued matches use the matched version/template; current restrictions still apply. Binding edits never silently remap queued input into a new template.

## TTS-5: Occurrences versus state {#TTS-5}

`mode=occurrence` matches an authenticated event happened, not that a predicate is true during each poll. Initial observation MUST default to baseline/no task. `mode=state_edge` requires a registered state schema and ordered revisions or an authoritative Observe reconciliation. Grain MUST persist prior predicate value/resource revision atomically with transition matching. Only false-to-true creates an edge; unknown is neither false nor true. Initial true does not fire unless explicit `initial=emit_current` is previewed and authorized. Edge identity includes source/resource, predicate digest and stable transition revision. A changed predicate starts a new baseline, not an implied edge.

Replaying an event does not mean it happened again. Replay MUST preserve event identity and original occurrence time. Current state reads MUST NOT manufacture historical occurrences. An issue `labeled` event may match an occurrence binding; an issue that currently has the label matches a state binding only under its edge/initial policy. A tracked-resource state transition may be a versioned internal event only with separate read and trigger authority. A gate's repeated evaluation result/state churn MUST NOT produce new transitions.

Out-of-order occurrences MAY still match within admitted age policy; late state evidence MUST NOT regress the baseline or generate fabricated edges. Where ordering cannot be established, hold and reconcile; do not compare occurrence timestamps or SHA strings to invent order.

## TTS-6: Durable ingestion and subscriptions {#TTS-6}

Grain MUST persist a source registration/cleanup intent with its subscription ID and generation before calling event_open/event_close, then reconcile uncertain replies with the same idempotency key. A failed registration MUST NOT authorize collection or filing. Grain MUST persist normalized events before acknowledging ingestion success or advancing its provider checkpoint. A write transaction MUST atomically append accepted page events plus source cursor/generation, or neither. Failed page retries deduplicate unchanged events. Sources with per-event delivery acknowledgment MUST acknowledge only after durable commit. Webhooks MUST verify sender signatures over raw bytes with rotating installation secrets, enforce size/rate limits before parsing, and bind tenant/installation/host/repository to expected source scope. Relays MUST authenticate their own hop, use replay bounds and TLS outside a trusted local transport. Authentication proves sender origin, not task authority or human approval.

Shared subscriptions MUST be keyed by provider installation, credentials/read-security partition and normalized source scope; share only where collection authority permits. Consumers MUST retain separate cursor, generation, filters, retention requirements and read checks. A private resource/event, subscription existence, match count or payload MUST NOT leak through another task's cursor, cache, UI, error message or preview. Removing one consumer MUST not interrupt others. Provider credentials MUST remain outside task environments unless separately granted.

Cursor loss/expiry MUST produce a durable gap record. Reconcile from the last verified watermark where history exists, with overlapping poll windows and stable-ID dedup. When only current state is readable, reconcile current state and explicitly mark historical coverage incomplete. Review request remove/re-add, short-lived labels and other transient events may be unrecoverable from current GitHub state; Grain MUST report that it cannot recover those occurrences, never promise complete webhook recovery. Use history/timeline APIs only when the installation demonstrably supports the needed identity and retention. Time ticks can be regenerated from admitted recurrence/occurrence identity.

## TTS-7: Match and filing transactions {#TTS-7}

Grain MUST durably record a match with event key, binding ID/version, template/policy digests, mapped inputs/digest, provenance, repeat key, match time, status and explain trace. Unique match identity is `(binding_id, binding_version, provider, source, event_id)`; for a state edge the adapter supplies the durable edge ID described in §5. One match can have multiple explicitly bounded target slots only if authorized; slot ID is then part of filing identity. The default is one slot.

The filing operation MUST run in one database transaction that locks/fences binding policy/control revision and the repeat-key coordination row, checks the unique firing key, revalidates authority/current availability, reserves budgets, inserts/composes the task or records coalescing, writes immutable task attribution plus match disposition, and commits a filing receipt and dispatch outbox. Task visibility and receipt MUST become observable together. The same logical key always returns the same receipt. A uniqueness constraint, not an in-memory lock or title/tag search, MUST enforce this in both SQLite and Postgres. Dispatch consumes the outbox idempotently and still follows current dispatch/admission policy.

Before commit Grain MUST recheck enabled state, owner/service validity, revocations, source/read authorization, target repository allowed/standing-work settings, template availability/security envelope, current capability grants and approval/declaration acceptance. A concurrent disable/revoke MUST serialize or invalidate commit using policy epoch preconditions. Holds MUST NOT consume task IDs as proof of filing or grant stale approval. Proposed tasks also require authorized creation and scope; approval later rechecks authority.

A worker crash before match commit is recovered by journal replay; after match/before filing by leased pending-match scan; during transaction by rollback/retry; after commit/before acknowledgment by returning the stored receipt. Worker lease expiry MUST use fencing so a resumed old worker cannot file again. Provider duplicate/out-of-order deliveries MUST NOT duplicate logical filings. Exactly-once logical filing is promised only for stable identity plus retained durable unique keys and supported transactional storage; exactly-once external transport is never promised. External task mirrors/notifications require their own outbox/idempotency or reconciliation and MUST NOT be described as exactly once.

Routine config upgrades MUST NOT replay retained events into a new version automatically. Activation watermarks exclude already processed history by default. Compatible upgrades SHOULD retain a dedup lineage across versions; explicit replay into a new version needs preview and a budget, with `skip_prior_firings` default. A deliberate `allow_new_firings` replay uses an authorized replay epoch as an additional logical identity dimension and labels every new task. It is a new authorization, never a dedup workaround.

## TTS-8: Repeat, PR head, batching and concurrency {#TTS-8}

| Repeat policy | Required behavior |
|---|---|
| `new` | Each distinct eligible occurrence files a new task, subject to active/queue limits. Never enqueue unlimited overlap. |
| `coalesce` | Under a transactional repeat-key lock, attach each occurrence once to an active compatible task with immutable event receipt and bounded input batch; if none exists, create a task. Do not edit an executing prompt. Deliver a safe follow-up at an allowed run boundary. |
| `ignore` | While a compatible task is active, record `ignored_active` plus task ID; otherwise create. The event remains in history. |

Repeat key fields MUST be immutable allowlisted identifiers; keys are namespaced to binding/dedup lineage and security partition. An active task means any nonterminal task, including awaiting a person; terminal is completed/failed/closed under current lifecycle policy. Binding version/template/security changes MUST NOT coalesce into an incompatible task. Active/concurrency reservations MUST be transactionally shared with completion so simultaneous requests cannot exceed limits.

A PR review identity includes repository stable ID, PR number, recipient, request epoch and reviewed head. Duplicate delivery of the same request is not a re-request. A subsequent authenticated request after completion has a distinct epoch and creates work even on the same head. Head changes MUST invalidate old-head review evidence. The default `new_request_only` does not file on a synchronize/head event alone; a new request coalesced into an active task records the new head and explicit review obligation without claiming the earlier head was reviewed. If an old task is completing concurrently, the repeat-key transaction attaches before completion under message processing rules or creates a new task after it; no request disappears.

`on_head_change=new` is an explicit alternate subscription/policy: a verified head transition while a request remains outstanding can create a distinct bounded request-head occurrence. `ignore` is appropriate to time ticks, not permission to reuse stale approvals. Human removal of a request before filing SHOULD cancel/hold stale review work under a declared freshness precondition; if authoritative freshness cannot be read, hold rather than assume a current request. The historical event remains recorded.

Debounce delays filing for a bounded interval; batching groups eligible events up to `batch_max`. Both MUST retain per-event identities, disposition and individual receipt links. Batch overflow creates subsequent bounded batches or visible held entries, never drops silently. Debounce MUST have a maximum deadline (default 5 minutes), so a busy resource cannot postpone work forever. Coalescing/ignore/catch-up collapse MUST be distinguishable from data loss in UI and metrics.

## TTS-9: Enable, replay, quotas and recovery {#TTS-9}

New bindings default to baseline initial observation, future-only enable, explicit held catch-up and no automatic backfill. Eligibility starts after the committed Grain receipt-sequence activation watermark; source occurrence time additionally enforces maximum age where verified. A source backlog collected after activation with pre-activation occurrence time MUST be held for catch-up selection, not mistaken for new live work. If occurrence time is unavailable, the source MUST establish a verified baseline cursor or hold ambiguous backlog. Wall-clock time alone is not a checkpoint. Enabling after an outage/pause MUST display the gap/backlog summary and establish an activation watermark. Catch-up choices are `hold`, `skip` with recorded counts/ranges, or `one` collapsed summary; historical mass dispatch requires a separately authorized finite replay job. Legacy schedules retain their one-collapsed-firing behavior under §12 rather than silently adopting this default.

Replay jobs MUST specify binding version, source range/cursor/time bounds, event ceiling, task ceiling, expiry, cost reservation, skip-prior policy and actor/reason. Preview produces counts/sample dispositions with no writes to live cursors. Replay MUST NOT advance or rewind the live collection cursor, reset dedup keys, bypass pause/revocation or exceed quotas. Expired history reports incomplete coverage. Backfill from current state is labeled synthetic state observation, not a recovered request event.

Every binding MUST have finite matches/hour, tasks/day, queue size, concurrent tasks, event fan-out and cost/day budgets. Examples are illustrative, not deployment changes. Recommended deployment ceilings are 10000 queued matches, 100 bindings considered per event, 100 task filings/minute and 1000 cost units/day per owner; effective quota is the minimum of binding/owner/repository/deployment limits. A cost unit is the deployment's versioned admission reservation unit; actual run cost MUST settle reservations and feed normal run-budget enforcement. Unknown cost MUST use a configured conservative reservation or hold. Dispatch concurrency and cost control remain effective after filing.

Quota exhaustion MUST record `held_quota`, next eligible time and safe counts, never silently discard events. Once the bounded per-binding queue fills, collection SHOULD continue into the bounded shared journal; matching records a compact overflow range/count and stops automatic dispatch for that binding. Journal retention pressure MUST pause/negative-ack capable sources before deleting unread records; where collection cannot be stopped, persist an explicit loss/gap marker and alert. Bounded metadata MUST aggregate repeated overflow ranges without hiding first/last cursor or known count. Recovery requires a bounded replay/skip decision; quota refill alone MUST NOT unleash overflow backlog. Rate limits use persisted backoff with jitter (30 seconds to 30 minutes recommended), a provider circuit breaker and visible health. Provider failures MUST NOT spin agent runs.

Recommended retention: event bodies 7 days, redacted match/error traces 30 days, firing identity/attribution tombstones for the binding's lifetime plus maximum authorized replay horizon. Expired tombstones permanently fence the older replay range; administrative restore MUST not re-enable that range implicitly. Runtime backups MUST include journal/checkpoints/receipts and restore them coherently; stale backups require a held source reconciliation before dispatch, especially if tasks or external effects survived the restore.

## TTS-10: Security, loops and attribution {#TTS-10}

Binding create/edit/enable/replay/delete, installation management, event read and standing approval are separate typed permissions with repository/source/template limits. Management authority MUST NOT itself permit granting capabilities or automatic approval. Creation and authority expansion require an authorized actor and reasoned audit; agents MAY propose/manage only within explicit delegated ceilings, MUST NOT approve their own expansion, and MUST NOT bypass person-only grants, declaration acceptance or existing task/template admission. Webhook actors and payload fields cannot grant any of these rights.

Disable MUST fence filing immediately and hold queued matches. Remove MUST tombstone the binding, cancel unfiled matches and stop its source consumer; shared collection and already-created tasks remain. Re-enable requires explicit catch-up selection; changing policy does not resurrect cancelled matches. Existing tasks remain attributed and subject to current task/grant policy; binding deletion does not cancel tasks or legitimize revoked grants. Revocation/repository removal/provider removal holds queued work with a visible blocker; retry only after current authority validates.

Every task MUST carry immutable structured attribution linking binding ID/version, authorization actor/service and owner resolved at filing, event/source/resource identifiers, event occurrence/receipt times, template digest, match/firing/replay IDs and causation. UI MUST link to the binding's historical version and event/resource where access permits. Redacted payload expiry MUST not erase the minimal filing identity. Attribution MUST NOT be editable external title/body text; links MUST respect access checks and safe URL construction.

Grain actions MUST emit authenticated origin task/action IDs, chain ID, causation ID and depth where correlation is supported. An external payload's claimed Grain ID is untrusted. Clock bindings MUST explicitly authorize automated time occurrences (`allow_bounded` in the schedule example); migrated schedules map only their already-authorized clock source to this permission. Each scheduled tick starts a new root chain and MUST NOT inherit the previous firing task's causation. This does not exempt task actions from loop detection or any quota. Other automation events default to held; explicit automation opt-in still enforces per-chain task/depth/cost limits and repeated `(binding, resource, action-kind)` cycle detection. Unknown causation cannot be assumed safe: enforce owner/resource rate limits and hold suspicious repeated chains. Lack of provider actor/correlation support MUST be disclosed. Correlating outgoing action receipts to delivery is preferred over broad bot-name suppression. A later authenticated human request creates a new request epoch/chain root and MUST NOT be permanently suppressed merely because Grain once acted on that PR. Loop holds show the causation path and allow authorized bounded release, not a global bypass.

Secrets MUST be installation-owned references, encrypted under existing secret policy, rotated/revocable and absent from payloads, previews, task bodies, traces and error text. Quarantine stores only safe digest/size/source metadata by default. Event body access, explain traces and replay previews MUST enforce current caller source/task/repository rights; a provider's broad installation access MUST NOT broaden a consumer. Any external mutation/subscription installation MUST use separately authorized typed action execution and audit. MCP read-only annotations are insufficient.

## TTS-11: Human and agent surfaces {#TTS-11}

The future Angular management UI MUST show bindings, effective source/template pins, scope/grants/approval, enable/pause, preview, catch-up/replay confirmation, event coverage gaps, matches, coalesced tasks, held/ignored/error/overflow dispositions, provider health, quotas and explain traces. Enable/authority expansion MUST present the concrete effective composition and recovery limitations before recording approval. Deleting/pausing MUST show queued-work semantics. Existing historical tasks remain navigable through retained binding versions. No classic UI feature is required by this proposal.

Proposed scoped agent tool contracts are:

| Tool | Inputs | Result/authority |
|---|---|---|
| `trigger_list` / `trigger_get` | repository, binding ID, bounded page cursor | Authorized binding/history metadata only |
| `trigger_preview` | candidate binding, bounded caller-readable sample event IDs or synthetic fixtures | Match/exclusion reasons, typed mapping, template composition, permissions, quota/recovery warnings; no subscription, checkpoint, match or task writes |
| `trigger_explain` | binding version, event or match ID | Deterministic per-term trace, missing fields, admission/repeat/quota/loop disposition; private values redacted |
| `trigger_propose` | closed candidate binding, reason | Reviewable proposal, not standing authority |
| `trigger_apply` / `trigger_control` | approved candidate or binding/control change, expected version, authorization ref, reason | CAS audit receipt; only within explicit management/delegation scope |
| `trigger_replay_preview` / `trigger_replay_apply` | bounded replay contract from §9, expected version, reason | Preview then separately authorized replay receipt, cancellable and quota bound |
| `trigger_events` / `trigger_firings` | authorized binding/source, bounded time/cursor, limit 1..100 | Scoped redacted pages; never raw secrets or another consumer's cursor |

The same authorization/composition/matching functions MUST serve UI, tools and filing. Preview MUST not mint credentials or install webhooks. Errors MUST use stable typed codes plus safe human text; unsupported versions/configuration are blocking errors, not unmatched events. Explain MUST distinguish unmatched filter, held permission, ignored repeat, dropped catch-up, coalesced, filed and missing coverage. No management endpoint named here exists by virtue of this document.

## TTS-12: Current schedules and migration {#TTS-12}

Evidence inspected at base `d8ff32019`; source links below are repository-relative. Existing `docs/schedules.md` is historical and contains superseded sections. This inventory describes current code; preceding sections prescribe future behavior.

| Current surface/evidence | Observed semantics and required migration treatment |
|---|---|
| [Recurrence](../../../pkg/workflows/recurrence.go), [calendar tests](../../../pkg/model/schedule_test.go), [cron translation](../../../pkg/workflows/cron.go) | Every N hours, every N minutes (10-minute floor), daily/weekly/monthly; monthly day clamps to last day. Explicit schedule zone takes precedence over recurrence zone; empty uses UTC. Interval uses elapsed durations; calendar uses local wall time. Current DST gap/fold behavior delegates to Go `time.Date`, without an explicit skip/first/second policy. Preserve `legacy_go_time_date` plus runtime/tzdb fingerprint for migrated rows and flag differences on upgrade. New time bindings default `skip_gap_first_fold`: nonexistent local occurrence skipped visibly, ambiguous occurrence fires only the earlier instant. This is a deliberate changed default, not a claim about current behavior. |
| [Schedule model](../../../pkg/model/schedule.go), [UI CRUD](../../../pkg/ui/schedules.go) | Enabled state; NextRunAt/LastRunAt; creation waits for next occurrence; cadence edit retimes; unchanged cadence and pause/resume do not reset. Preserve instants and state. |
| [FireSchedule](../../../pkg/model/schedule_fire.go), [reconciler](../../../pkg/orchestrator/schedule.go), [skip model](../../../pkg/model/schedule_skip.go) | Overdue intervals fold into one firing and advance from scheduled cadence to future; paused due occurrence is recorded and held, resume fires once. Overlap `skip` actually holds NextRunAt until prior work finishes; `allow` permits overlap. Preserve held versus dropped distinctions and repo-scoped overlap groups; do not map `skip` to destructive ignore. Preserve publishing-held conditions, off-target/standing-work/CI-mode suppression, starvation escalation and failures visibly retried. Manual Run now/standing wake are separate firing reasons and do not consume the normal occurrence when advance=false. |
| [Composition](../../../pkg/model/compose.go), [template fields](../../../pkg/model/template_field.go), [schedule firing](../../../pkg/model/schedule_fire.go) | Template content resolved fresh at firing; unattended template fields use defaults and required fields without defaults are refused by unattended-template validation. There is no schedule-level per-firing parameter mapping/sequence expression in Schedule. Inline task content is also supported; schedule_sequence allocates schedule IDs, not template parameter values. Target-bound template wins placement today. Schedule's own admission/hosting floors and extra grants survive template cache refresh; schedule model override wins. Preserve effective composition, default substitution order and existing ID allocator state; suite pass/item ordering remains with the compatibility executor. A preview MUST NOT consume ID sequences. Any future parameter sequence feature requires its own admitted schema and transactional allocation with the filing receipt; it is not an existing API. |
| [Task creation](../../../pkg/model/compose.go), [grant holds](../../../pkg/model/template_grants.go), [schedule CRUD](../../../pkg/ui/schedules.go), [schedule team grants](../../../pkg/ui/agent_api_gate.go) | Schedules are standing approval: composed tasks land approved and carry schedule owner/team/current on-call attribution, reads/writes/grants/network/custom fields, auto-merge and proposal policy as configured. Declaration/person-only grant holds and admission are separate. Migration preserves only existing authorized scope; never upgrades an unaccepted declaration into a valid grant. New external trigger bindings default proposed. |
| [Filing identity](../../../pkg/model/schedule_fire.go), [task filing](../../../pkg/model/task.go) | `schedule:<id>` tags, `ReasonSchedule`, SchedulePrincipal, overlap-group tags, LastRunAt and skip records identify existing generated work. `putScheduleTask` calls PutTask before separate recordFiring/UpdateSchedule: current code is not the proposed atomic firing/checkpoint proof, particularly with overlap allowed. Keep old IDs/tags/reason and attribution links; add durable ledger keys rather than reinterpret tags as event IDs. |
| [Non-time recurrence](../../../pkg/workflows/recurrence.go), [GitHub ingestion](../../../pkg/orchestrator/githubevents.go), [path triggers](../../../pkg/model/path_trigger.go) | `github_workflow_run` and `git_ref` are already event-driven schedule kinds and excluded from due-time queries. Path triggers have separate merge/PR review, head tracking, human gates and template behavior. Migrate to source-specific adapters later, not the clock provider. Preserve baseline/no historical firing and existing review gate semantics. |
| [Suite/workflow firing](../../../pkg/model/schedule_fire.go), [scheduled workflow records](../../../pkg/model/scheduled_workflow_run.go) | Schedules can start suites or durable workflow-run intents with later review; overlap includes unfinished run/review and bounded abandoned-review hold. Standing templates wake existing tasks. Do not replace these with an ordinary task silently. Retain compatibility executor/action types until explicit equivalent contracts exist. |
| [State-repository tables](../../../pkg/staterepo/tables_internal_test.go), [runtime exclusions](../../../pkg/staterepo/bind.go), [external config](../../../docs/external-config-repo.md) | Schedule/template settings are config; integration events, webhook/git-ref/path cursors, scheduled workflow runs and skips are runtime history. New binding/pin/approved mapping config belongs in state-repo settings; journal/matches/receipts/cursors/activation/replay/leases belong in runtime tables, never live-applied settings. Sensitive standing approvals are separately audited authority records, not self-granting YAML. |

Migration MUST inventory every actual schedule row/source, owner and approval/declaration status, effective template/parameters/sequences, recurrence/zone, next/last instant, enabled state, overlap/group and in-flight tasks/suite/workflow runs before changing ownership. Produce a reviewed mapping report. Inline content becomes a private immutable synthetic template; floating templates use explicit `track_compatible` profile and archived approved security envelope. A schedule owner of `oncall` retains resolution at filing, recording both the configured selector and resolved person. No production inventory or migration is performed in this PR.

Handoff MUST fence old scheduler writers with a durable authority epoch per schedule. Shadow evaluation MUST NOT file, reserve sequences or advance live cursors. In a single migration transaction, assign binding ID, import current cadence/cursor/in-flight associations and dedup lineage, set activation occurrence, mark legacy authority transferred, and retain old configuration. A clock event key MUST include stable schedule/source ID, scheduled UTC instant and cadence epoch; missed-run collapsed windows retain the oldest due occurrence plus covered range. Cadence epoch changes only when recurrence semantics change, not on every poll. Preserve overlap reservations from old tags. Ambiguous legacy insertion/advance gaps MUST be reconciled against created tasks and held for review if identity cannot be proved; never guess and dispatch both.

Compatibility schedule CRUD MAY translate into binding edits while retaining schedule IDs, tags, reason, owner, NextRunAt/LastRunAt and held/drop display. Compatibility APIs MUST NOT expose new unapproved capabilities. Suite/workflow/standing executors remain under one recorded authority even while their conversion is deferred. Binding deletion does not delete old scheduled tasks.

Rollback MUST fence the new writer before restoring legacy ownership, map next due/last run and active reservations from committed receipts, and preserve the ledger as a dedup fence. If the old binary cannot read the epoch/fence or represent the policy, rollback MUST keep firing paused pending a compatible reader; it MUST NOT run both engines. Deploy compatible readers before writers. Restoring config alone MUST NOT rewind runtime cursors, reset sequences or reissue occurrences. Runtime field edits require a distinct audited recovery operation, never a state-repository PR. This deployment's settings are in `grain/config`, not this repository.

## TTS-13: Existing extension, event and authorization evidence {#TTS-13}

- [Capability specification](../capabilities/capabilities.spec.md), [discovery](../../../pkg/capability/protocol/discovery.go), [schema support](../../../pkg/capability/protocol/schema.go) and [configure](../../../pkg/capability/protocol/configure.go) define current mint/release/configuration conventions. The new `event_*` contract is not implemented there.
- [Waiting wire tests](../../../pkg/capability/protocol/waiting_test.go) exercise configure refusal with a `WaitingError`; this is credential/configuration waiting, not an existing durable trigger event subscription or permission to create a task.
- [Integration event design](../../design/integration-events.design.md), [model](../../../pkg/model/integration_event.go) and [reader authorization](../../../pkg/ui/events.go) provide a durable bounded outgoing event log (10000 retained records), repository-filtered replay/SSE. This is useful plumbing precedent, not already a complete inbound trigger journal or complete event recovery guarantee.
- [GitHub event normalization](../../../pkg/ghwebhook/event.go), [signature checks](../../../pkg/ghwebhook/sign.go), [receiver](../../../pkg/ghwebhook/server.go) and [consumer](../../../pkg/orchestrator/githubevents.go) provide authenticated relay, bounded fields and cursor consumption. Current requested-reviewer login is not a complete stable user/team request-epoch contract.
- [From-template API](../../../pkg/ui/tasks_from_template.go) delegates to ordinary task creation/gating; [agent gate](../../../pkg/ui/agent_api_gate.go), [template containment](../../../pkg/model/template_grants.go), [admission guard](../../../pkg/model/admission.go), [capability spec](../capabilities/capabilities.spec.md) and [RBAC spec](../rbac/rbac.spec.md) remain authority constraints. New filing MUST use the same typed authorization rules rather than copy raw JSON-body policy checks or bypass them.
- Task #39's evaluator proposal is now [mcp-gates.design.md](../../design/mcp-gates.design.md) and [mcp-gates.spec.md](../mcp-gates/mcp-gates.spec.md), following Task #44's documentation rename merged as `befe68b54` (PR #1699). The inspected evaluator revision `1ac7e412d` retains immutable pins, typed actions, operation ledgers and durable waits (MGS-1/4/6). Documentation is merged; runtime support remains proposed. Pin/translate protocols explicitly; existing concrete task goals retain their names and desired-outcome semantics.
- Task #45 draft at `6d2ffe338`, `origin/grain/10b43b32/task-45-design-extensible-dependency`, supplies `design/design/tracked-resources.design.md` and `design/spec/tracked-resources/tracked-resources.spec.md`. TDS-2/3/4/5 define ResourceProviderIdentity/Pin, TrackedResourceReference, `resource_*` discovery, read/watch authority, freshness and the fence protocol bridged in TTS-2. These branch-only paths are not links to nonexistent files on this base. Reading this draft does not assert it or its implementation has merged.

## TTS-14: Acceptance scenarios {#TTS-14}

These are requirements for future implementation validation, not tasks filed by this PR.

| Scenario | Required result |
|---|---|
| Duplicate webhook or poll overlap | One normalized event, match and logical filing per binding/version; same stored receipt on retry. Conflicting content quarantined. |
| Out-of-order requests and state changes | Eligible distinct requests retained; old state never regresses current head/edge baseline; ambiguity reconciles/holds. |
| Crash before/after match, during/after filing, two workers | Replay/lease scan recovers; transaction and unique keys yield one task/receipt/outbox; fenced loser has no effect. Sequence consumed once. |
| Missing webhook/cursor expiry | History reconciliation deduplicates where supported; state-only recovery records explicit gap and does not invent transient re-requests. |
| Initial state true; label removed/re-added | Baseline emits nothing by default; genuine later false-to-true emits once; replay of edge emits no new task. |
| Repeated review request after completion | New epoch creates new task even for same PR/head; duplicate old epoch does not. |
| Changed PR head during active review/filing | Old evidence invalid; new request coalesces safely with explicit head obligation or creates after terminal state; no new request means no default task. |
| Team renamed, membership changes | Stable team ID scope persists; no implicit per-member fan-out or retargeting. |
| Grant revoked/template removed/repository disabled between match and commit | Transaction detects new policy epoch, holds/refuses; no stale approval or task capability expansion. |
| Floating template gains writes or approval | Security-envelope mismatch holds for renewed authorization; pin never silently tracks it. |
| Event title contains shell/template/approval instructions | Literal bounded data only; no extra capabilities, repository, template or approval selected. |
| Grain action causes webhook chain; later human request | Automation loop bounded/held with causation; later authenticated human epoch remains eligible. |
| Resource-provider Watch hint delivered to trigger consumer | Hint only dirties Observe; no invented request event. Canonical identity/config/partition bridge verified; independent cursors and authority retained. |
| Private source shared with another consumer | No event/payload/existence/cursor leak; each reader and filing checks current scope. |
| Provider removed, credentials expire, outage | Binding held with safe typed error, persisted backoff and health; no implicit replacement or agent retry storm. |
| Pause/remove with queued matches | Pause fences/holds; remove tombstones/cancels unfiled; existing tasks remain; resume does not silently replay backlog. |
| Enable/replay after long outage; queue overflow | Preview finite range and budget; dedup preserved; visible gap/overflow; no automatic mass dispatch at quota refill. |
| Debounce with continuous requests | Bounded deadline and batches; every input accounted for by filed/coalesced/ignored/held disposition. |
| Daily DST gap/fold and monthly 31st | New explicit skip-gap/first-fold policy and month clamp; migrated legacy profile reproduces baseline or holds with drift report. |
| Migration with due occurrence/open legacy task/overlap allow | One authority epoch and imported reservations; no duplicate run; ambiguous legacy commit gap held. |
| Rollback/old binary/config restore | Durable fence prevents dual writers and repeated occurrence; incompatible rollback stays paused. |
| Preview/explain run by restricted agent | Same deterministic matcher, scoped redaction, no live side effect or authorization expansion. |

## TTS-15: Unresolved choices and implementation dependencies {#TTS-15}

Recommended defaults are normative until explicitly revised through design review. Journal package/table naming and shared collector implementation need coordination with #45; retain its pinned resource-provider method names behind a neutral Grain-owned journal and the TTS-2 adapter facade. Gates' action registry from #39/#44 may not exist when trigger implementation starts; use an equivalent typed authorization interface and ledger, not a fake dependency or a raw provider write path. Quota/cost units and retention need operator sizing before any binding is enabled; retain finite conservative defaults and report measured pressure. New DST semantics require explicit provider/calendar version and previews; retire the legacy profile only via reviewed opt-in migration. External GitHub event history completeness is installation-specific and must remain a declared limit, not an unresolved promise.

The [design's phases](../../design/task-triggers.design.md#phases-not-filed-tasks) specify sequencing. No phase, follow-up task, executable declaration or subscription is filed by these documents.
