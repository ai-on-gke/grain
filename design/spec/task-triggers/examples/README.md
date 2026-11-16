# Trigger wire and acceptance examples

These are complete hypothetical messages, not live subscriptions, credentials or filed tasks. Bindings start paused. Digests are syntactically valid illustrative artifact/template pins; real admission must verify installed content. Descriptor schema digests and firing identity arithmetic are computed and checked. Only ASCII/integer identity fixtures use Python's sorted compact JSON as equivalent to RFC 8785; the implementation must use RFC 8785 generally.

Run from the repository root:

```sh
python3 design/spec/task-triggers/validate.py
```

The standalone checker needs `python3-jsonschema` (`sudo apt-get update && sudo apt-get install -y python3-jsonschema` on Debian). No runtime/library dependency was added. [manifest.json](manifest.json) indexes every fixture and its named definition in [wire.schema.json](../wire.schema.json) or [protocol.schema.json](../protocol.schema.json). Each transcript step validates separately; no omitted fields, patch-only messages or assumed callbacks. [TTS-24](../implementation.spec.md#TTS-24) is the behavioral acceptance oracle for the eventual implementation, including crashes and revocation races; these artifact checks do not implement or prove those behaviors.

| Fixture | Expected result |
|---|---|
| [time-binding.json](time-binding.json), [time-event.json](time-event.json), [time-sequence.json](time-sequence.json) | Due scan at 09:03 journals 09:00 tick with the same scheduled identity, then files one proposed task with an atomic receipt/outbox |
| [github-binding.json](github-binding.json), [descriptor.json](descriptor.json) | Approved installed provider/source schema and paused team binding; same pin used by calls. Actual setup/grants still required |
| [gateway-page.json](gateway-page.json), [github-event.json](github-event.json), [github-sequence.json](github-sequence.json) | Signed webhook append -> authenticated relay -> outbound MCP open/read -> durable journal -> matched pinned filing -> one proposed task -> exact receipt retry -> renewal -> cancellation. Unknown source occurrence time stays null |
| [cursor-expired.json](cursor-expired.json) | Typed error, no events/progress; persist explicit loss, hold dispatch and request bounded baseline/recovery decision |
| [filing-authorization.json](filing-authorization.json) | Verified person-issued scope for proposed task filing; source authenticity is a separate boundary. The GitHub transcript also includes the full durable match snapshot |
| [apply-binding.json](apply-binding.json), [replay-apply.json](replay-apply.json) | Closed conditional edit and finite preview-bound replay; no runtime/config mutation from reading these fixtures |
| [preview-result.json](preview-result.json), [firing-history.json](firing-history.json) | Redacted composition/authority/coverage and immutable receipt linkage; history gap remains visible, not hidden by successful filing |
| [update-binding.json](update-binding.json) | Explicit update action required; schema-valid configuration still needs never-started/revision checks at runtime |
| [malicious-payload.json](malicious-payload.json) | Untrusted instruction-like title is schema-valid data; deterministic bounded mapping must keep it literal. No payload-derived task authority |

The worked GitHub source uses private partition `partition-review-team77`, source lineage `source-uuid-github-17`, binding dedup lineage `binding-uuid-review`; time uses `partition-daily`, `source-uuid-daily`, `binding-uuid-daily`. The controller allocates/persists these values outside config. These constants permit checking every E/M/F key from [TTS-18](../implementation.spec.md#TTS-18). Duplicate receipt responses in github-sequence are the same receipt, not additional filings.

Each `invalid-*.json` contains a **full rejected message** plus the named definition; the manifest requires schema rejection. Cases cover floating release, callback injection, state mode on occurrence profile, undeclared head-change kind, update without action, standing approval without authorization, missing request epoch, unknown kind version, open without expiry, cursor progress on error, unconditional edit, replay flood, orphan created receipt, executable filter and enable without preview. Semantic holds such as revoked grants, stale heads, forged claims, byte limits, scope, out-of-order state, loops and storage crashes require the TTS-24 runtime tests even when the message itself validates.
