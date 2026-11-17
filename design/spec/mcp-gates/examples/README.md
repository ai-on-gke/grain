# Brokered gate read transcripts

Documentation fixtures for completion **4.0**, action **5.0**, evidence context **3**, read operations **1.0**; not runtime endpoints or deployed policy. These supersede the former v2/3 snapshot-page-only transcripts. Historical filenames such as stale-snapshot/expired-pages identify the same failure scenario under the new dependency context; they do not imply full materialization.

| File | Complete sequence / expected handling |
|---|---|
| [success-paged.json](success-paged.json) | Two typed change.checks.list requests with stable collection generation/cursors, delivered pages, then final met/state candidate; final acceptance revalidates dependencies. |
| [action-review-brokered.json](action-review-brokered.json) | Taskless action-profile exact-head ordinary review: read request, authorized review result and final met, preserving action context/digest. No merge is performed. |
| [prefetch-single-round.json](prefetch-single-round.json) | Grain preexecutes declared check read in round 0; final without continuation, under the same read/dependency budgets. |
| [transient-read-retry.json](transient-read-retry.json) | Transient adapter failure before delivery; one exact logical-read retry charges another operation attempt; ordinary gate continuation/final then succeeds. |
| [repeated-request.json](repeated-request.json) | Later repeated operation/scope/cursor gets request_loop without data, then unknown. New read IDs cannot reset limits. |
| [denied-read.json](denied-read.json) | Well-formed registered operation targeting an out-of-scope private subject gets safe unauthorized with no records; final unknown. |
| [partial-batch.json](partial-batch.json) | Ordered successful page plus denied read; final unknown, required insufficiency latched. |
| [partial-required-input.json](partial-required-input.json) | Captured prefix plus explicit required-domain insufficiency cannot support met. |
| [permission-revoked.json](permission-revoked.json) | Permission epoch changes mid-call; no further delivery or accepted result/state, even if permission later restored. |
| [expired-pages.json](expired-pages.json) | Context/retained request expiry aborts recovery; no reconstruction from live facts. |
| [stale-snapshot.json](stale-snapshot.json) | Changed head/evidence invalidates context/dependency receipt; late final cannot commit or authorize an action/close. |
| [provider-timeout.json](provider-timeout.json) | Original deadline expires; diagnostic/backoff only. |
| [provider-crash-retry.json](provider-crash-retry.json) | Exact same gate round after provider crash/lost reply; no reexecution of already delivered reads or provider session. |
| [controller-crash-retry.json](controller-crash-retry.json) | Fenced lease/checkpoint takeover; final receipt uniqueness prevents second state application. |
| [cancellation.json](cancellation.json) | Local cancel/pause fences late replies, no provider cancel callback. |

Each scenario is independent. Repeated illustrative IDs do not share storage. Hashes are actual canonical fixture SHA-256 values; release/adapter pins and short subject/version IDs are illustrative, never deployed attestations. Production must use RFC 8785; fixture values use ASCII keys and integers so sorted compact JSON yields the same bytes.

Complete native MCP envelopes are in [checks.mcp.json](checks.mcp.json) and [reviews.mcp.json](reviews.mcp.json); [read-operations.json](read-operations.json) supplies exact request/result examples for all nine initial operations. A `call.message` is the application **arguments** of an ordinary MCP request:

```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"gate_checks","arguments":"REPLACE_WITH_THE_COMPLETE_call.message_OBJECT"}}
```

The string is a substitution marker, not a valid gate invocation. For action-review use gate_review and its complete action-profile object. The corresponding reply is an MCP result with `structuredContent` set to the complete `reply.message` object and `isError:false`; the existing client also permits a single text block carrying identical JSON. A non-final application's kind=read_requests is still an ordinary tools/call **result**, not a native server-to-client JSON-RPC request. Grain executes the named registered reads through an authorized adapter, never by giving the provider a callback connection or arbitrary tool caller.

A `read_result` acknowledges exact operation/version/args/read identity, carries typed records, source times, collection generation/completeness/cursor and trusted automatically tracked dependencies. Only a final response can propose durable gate_state. `event` steps describe controller/adapter/store/clock events, not wire messages, source events or triggers. `controller_error` is a diagnostic projection with unchanged state/no accepted result. Full context/dependency/permission/freshness/coverage checks and state CAS remain necessary beyond shape validation.

[tools-list.json](tools-list.json) provides complete resolved completion discovery/input schema and declaration. [args.schema.json](args.schema.json) is the fixture's fixed gate configuration. [evidence.schema.json](evidence.schema.json) defines the bounded inline evaluation_context/check fixture kinds; read operations additionally use the [shared exact record/args/result schemas](../read-operations.schema.json) and [registry](../read-operations.registry.json). The check predicate requires every suite named in the context on exact head a1, with actual check observation age ≤60 seconds at acceptance and complete required-set coverage. Unknown suites/missing/denied/stale facts mean unknown; observed failure means unmet. A final citation cannot prove an undelivered page or omitted prerequisite.

The nine initial operation schemas are proposed logical read contracts, not claims that those MCP names or safe endpoint equivalents exist. Registry adapter releases in fixtures are illustrative. [BR-1](../brokered-reads.spec.md#BR-1) maps current source and explicit adapter gaps; BR-3 specifies the versioned projection/dependency transactions needed for a consistent recorded cut.

From repository root, run:

```sh
/usr/bin/python3 design/spec/mcp-gates/validate.py
```

Requires Python 3 and jsonschema (`sudo apt-get update && sudo apt-get install -y python3-jsonschema` on Debian). The documentation validator checks schemas with remote resolution forbidden; initial registry/declaration/record/argument/request/context hash chains; all completion/action transcript messages; typed read records/citations and page invariants; and action binding fixtures. It implements no driver or runtime endpoint. [MGS-11](../mcp-gates.spec.md#MGS-11)/[BR-4](../brokered-reads.spec.md#BR-4) separately require live race, revocation, byte/allocation, retry/crash and action/closure conformance testing before implementation admission.
