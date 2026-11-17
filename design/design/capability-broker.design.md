# Shared capability interactions and scoped Grain back-calls

**Owner:** @ai-on-gke/ai-factory-root-owners

**Status:** proposed architecture; documentation/schema only. No runtime, live permission, configuration, deployment, task migration or automatic follow-up filing is authorized.

**The core rule:** Grain drives a bounded interaction with a configured capability server. The server requests registered, typed Grain operations; Grain authorizes and executes each request and sends correlated results in its next ordinary MCP call. Server-wide availability does not imply server-wide authority.

The authoritative contract is the [shared broker specification](../spec/capability-broker/capability-broker.spec.md), [wire schema](../spec/capability-broker/wire.schema.json), [operation schemas](../spec/capability-broker/operations.schema.json), [registry](../spec/capability-broker/registry.json) and [executable documentation examples](../spec/capability-broker/examples/README.md). Gates, resources and triggers are profiles of this interaction, not competing back-call protocols. Existing resource/event acquisition remains a separate domain protocol.

## Direction and scope

An installed server advertises `grain_handle_v1` and a closed catalogue of named operations in its input-schema annotation `x-grain-broker`. Grain calls it with the named operation, typed arguments and an opaque invocation context. A response is either `requests` or `final`. A request round names typed registry operations, not arbitrary API routes or provider-selected executables. Grain executes the permitted requests, durably records their results, and calls the same pinned handle again. An SDK may present this as `grain.call(...)` without exposing the loop to application code.

Every network hop to the capability server is Grain initiated through existing stdio, HTTP or hostd placement. There is no provider-to-Grain network connection, callback endpoint, requirement to colocate, or claim that MCP natively implements reverse `tools/call`. Transport authentication and invocation authority are distinct. A connection credential cannot be used as an ambient back-call credential.

This generalizes [Task #56 / PR #1709](https://github.com/ai-on-gke/grain/pull/1709)'s brokered gate reads. Its nine typed logical read operations and dependency-validated observation model are carried forward, with provenance in the spec. The shared envelope replaces specialized gate page/read continuation envelopes for new admission. Historical schemas remain explicitly archived, so existing examples and concrete task goal names are preserved. There is no dependency on Task 1 and no task dependency added by this design.

## Authority and effects

Grain derives a server/invocation-bound context from the authenticated actor or standing binding, current policy and approved operation scope. The default is the acting task/attempt, when present. Gate contexts permit reads only, including when gates are evaluating prerequisites for a mutation. General operations may request explicitly delegated mutations. Trigger contexts bind an authenticated event and standing filing authorization; no synthetic task is created to hold a credential. Resource Observe/Watch controllers use separately approved renewable controller leases, never a released attempt pass.

Attachment is metadata plus an observable tracking relation. It neither grants upstream access nor adds a required outcome. An existing resource read grant, attachment authority and completion-contract authority remain separate. A PR creation result may be attached to the acting task only after Grain validates the canonical identity and current task/grant/contract preconditions.

General mutations can legitimately change task evidence before final. A successful mutation has a durable receipt independent of the provider's final answer. A failed final cannot roll back a created PR or task. Gate rounds have no such mutations: non-final rounds cannot write `gate_state`, lifecycle state, action intents, waits or work. Gate proposals still pass central arbitration and Task #50's action guards. Changing a tool name cannot evade a protected effect.

## Source-based reuse and changes

Inspected base `e253df886`, Task #56 `6cc040b91`, resource completeness Task #48 `b28827ee9`, and trigger completeness Task #49 `09813a840` on 2026-11-17. The latter three are branch revisions, not assertions that their PRs have merged. Task #47's gate transport and Task #50's guard bindings are in the base.

| Existing source | Reuse | Necessary change, not implemented here |
| --- | --- | --- |
| `pkg/capability/protocol/client.go`: `Dial`, `ListTools`, `CallTool` | MCP 2025-06-18, stdio/Streamable HTTP/hostd, structured JSON result or first text fallback, configured endpoint with redirects refused | Bound parsing/bytes, strict broker decoding, persistent application IDs separate from JSON-RPC IDs; no reverse RPC |
| `protocol/discovery.go`: `Discover`; `server.go`: `Refresh`, `CallTool`; `serve.go`: `HandleTool` | Mint/release discovery ignores other names; non-mint/config tools are control tools; plain handlers can coexist | Parse and retain broker discovery, validate catalogue and pin against installed artifact, generate typed per-operation envelope branches |
| `protocol/schema.go`, `configure.go`, `identity.go`; `servers/servers.go` | Capability argument defaults, local schema subset, configured canonical identity/secret references/reconfigure | Separate full protocol-envelope validation from capability argument validation; bind config, credential, process and recovery epochs |
| `orchestrator/capability_pass.go`, `capability_restart.go` | Attempt lifecycle, host placement audit, revocation/release notifications | Invocation ledger and standing controller ownership cannot live only in attempt-held passes; placement changes need fenced handoff |
| `pkg/ui/agent_api_gate.go`, `mcp_endpoint.go`, `messages.go` | Current acting-task/actor/delegation checks, repository boundaries, filing rails and authorized read semantics | Typed bounded projections, collection generations and adapter authority checks; copying headers or a broad API grant is insufficient |
| `pkg/mcp/open_pull_request_tool.go`, `pkg/ui/pull_requests.go` | Daemon derives task branch/base; open-or-find behavior; CL opening is distinct | Durable mutation intent/receipt and reconciliation; conditional head/policy checks, canonical returned resource |
| `pkg/change`, `orchestrator/mergegates.go`, `humanreviewhold.go`, `mergerules.go` | Authoritative changes/checks/reviews/policy collectors | Side-effect-free read projection: current checks/review handlers can update observations or perform actions, so cannot blindly forward a read request |
| Resource TDS-11–16 (#48), trigger TTS-17–25 (#49) | Canonical partitions, renewable controller grants, observation dirty fences, filing key/transaction/outbox | Broker profile adapters preserve these semantics and pins; no new catalogue/secret store or second filing ledger |

The [normative operation mapping](../spec/capability-broker/capability-broker.spec.md#CB-3) separates current handlers from proposed adapters. No new logical name is represented as a deployed API.

## Consistency and recovery

Automatically capture every authorized read's record versions, collection generation and coverage, including empty results. Before a decision, validate the union plus mandatory predicate inputs and current authorization in one Grain decision transaction. This supports bounded demand reads; it does not require materializing a universal full task snapshot. An adapter without stable collection generations may supply a bounded immutable capture. Independent external systems cannot offer a single atomic cut through this protocol: record real collection time, freshness and source gaps, and retain exact-head/resource conditional writes.

Durable invocation checkpoints contain canonical requests/replies, owner fences, read dependencies, counters and mutation receipts. A restart can replay the exact pending request while its pins/authority remain valid. It cannot reset budgets, reacquire revoked authority, or repeat an uncertain external mutation under a new key. Uncertain effects require reconciliation and may remain visibly unknown.

## Migration, owners and review gates

1. Ratify the shared schema, operation registry, authority matrix and adversarial examples. Reconcile PR #1709 against this shared contract; do not ship its completion 4.0/action 5.0 envelope as a competing broker.
2. Implement bounded discovery/transport decoding and a durable driver with read adapters and dependency validation in both stores. Keep legacy task authority and operation execution unchanged; shadow outputs have no lifecycle effects.
3. Add mutation intent/lock/idempotency/receipt/reconciliation support and route protected effects through the Task #50 executor. Qualify provider failure after commit before enabling any mutations.
4. Integrate resource controller leases and trigger event/binding filing adapters with their own controllers. Neither depends on migrating all tasks to gates.
5. Opt in pinned providers/profiles through reviewed configuration and explicit task/contract migration. Preserve old concrete goals (`pr-merged`, `cl-submitted`, `tasks-closed`, `base-green`, `person-closes`, etc.) and rollback fences.

These are implementation phases, not filed tasks. Controller engineers own authority, evidence and effect durability; provider authors own typed operation semantics/replay behavior; SDK authors own transparent correlation/cancellation without widening scope. Remaining enablement blockers are the registry adapters, stable collection versions, durable driver and receipt transactions, installed-artifact verification, controller lease integration, provider compatibility and conformance qualification. Recommend reads first, then narrowly scoped attachment and trigger filing; keep arbitrary dispatch and nested broker execution excluded. Approval of these documents alone enables none of them.
