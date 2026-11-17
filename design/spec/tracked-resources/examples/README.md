# Resource protocol fixtures

These are complete proposed wire records, not live integration requests. The provider under example.org is a hypothetical fixture. Its task outcome schema demonstrates inline evidence; it does not claim the built-in task provider is implemented. Digests are computed over the fixture's canonical ASCII JSON; actual admission verifies the registered configuration and artifact.

[manifest.json](manifest.json) selects the exact definition in [wire.schema.json](../wire.schema.json) and expected validity for each fixture. Run `python3 design/spec/tracked-resources/validate.py` from the checkout (requires python3-jsonschema). Schema validation is followed by the semantic/authority rules in [TDS-11–16](../implementation.spec.md); a valid shape never grants permission.

| Flow | Complete records |
|---|---|
| Registration | [local registration](registration.json) and [configured connection fixture](mcp-server.json) |
| Resolve preview | [API preview input](resolve-preview-request.json); returns the resolved result below under a named grant |
| Resolve | [request](resolve-request.json), [result](resolve-result.json) |
| Observe | [request](observe-request.json), [immutable inline result](observe-result.json), [permission loss](unauthorized-result.json) |
| Subscribe before Observe | [ensure](watch-ensure.json), [fence reply](watch-ensure-result.json), then Observe above |
| Pull hints after baseline | [read](watch-read.json), [bounded reply](watch-read-result.json), then fresh Observe at the next observation generation |
| Renew / stop | [renew](watch-renew.json), [renew reply](watch-renew-result.json), [cancel](watch-cancel.json), [cancel reply](watch-cancel-result.json) |
| Discovery / fallback | [describe](watch-describe.json), [describe reply](watch-describe-result.json), [poll-only fence](poll-ensure-result.json) |
| Lost history | [cursor expired](cursor-expired.json); retain dirty, increment generation, ensure a new fence and perform two current reads; no recovered occurrence claim |
| Task edits | [attach](attach-request.json), [unresolved attach without lookup](unresolved-attach-request.json), [edit](edit-request.json), [remove](remove-request.json), [wait](wait-request.json), [grant](grant-request.json), [revoke](revoke-request.json), [required child reference](task-reference.json) |
| API results | [projection](attach-result.json), [list query](list-request.json), [list page](list-result.json), [mutation receipt](remove-result.json), [wait reply](wait-result.json), [grant reply](grant-result.json), [conflict error](api-conflict.json) |
| Invalid closed records | [implicit grant selection](invalid-implicit-grant.json), [undefined callback](invalid-callback.json), [pinned without selector](invalid-pin.json), [denied facts](invalid-denied-facts.json), [hint facts](invalid-hint-facts.json), [oversized page](invalid-page-limit.json), [error checkpoint](invalid-error-checkpoint.json), [opaque evidence](invalid-opaque-evidence.json), [shared watcher count leak](invalid-shared-count.json) |
| Invalid semantic pairs | [wrong generation](invalid-generation.json), [wrong evidence digest](invalid-digest.json), [wrong identity](invalid-identity.json), [bad freshness](invalid-freshness.json); these pass envelope shape but fail the request/registry checks |

Renew response cursors are informational; only successful read commits advance the persisted checkpoint. The ensure/read examples illustrate a hint racing baseline: receiving read-result must invalidate the baseline's gate evidence and cause a new Observe, not install any hint as facts. All error responses preserve the previous local checkpoint. Permission-loss records contain no facts/evidence from old success. Poll fallback admits current-state reconciliation only.

Fixture normalization is case-sensitive installation identity plus positive decimal task ID; no aliases or remote task lookup. The fixture scope validator requires exact installation_id/task_id equality and projects only outcome. These semantics are illustrative and do not supply real service authority.
