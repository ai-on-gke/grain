# Gate v2 wire transcripts

These are documentation fixtures, not a runtime implementation. Each transcript is an independent scenario; repeated example IDs across files do not share storage. All call/reply/controller-error messages are complete envelopes. Hashes are real canonical SHA-256 values; the small readable record, snapshot and cursor IDs are illustrative.

| File | Expected behavior |
|---|---|
| [success-paged.json](success-paged.json) | Two needs_data replies, two retained pages, then one final met and state CAS. |
| [prefetch-single-round.json](prefetch-single-round.json) | Declared required data is prefetched in round 0; final met without continuation. This variant has its own release/declaration pin. |
| [repeated-request.json](repeated-request.json) | A later round requests the same selection with a new read ID; one unavailable request_loop delivery, then final unknown. |
| [denied-read.json](denied-read.json) | An undeclared/inaccessible reference returns safe unauthorized without records, then final unknown. |
| [partial-batch.json](partial-batch.json) | One ordered batch contains a successful page and a safe unauthorized delivery; final unknown. |
| [permission-revoked.json](permission-revoked.json) | Permission epoch changes mid-call; no further delivery or accepted result/state, even if access is later restored. |
| [partial-required-input.json](partial-required-input.json) | A complete captured prefix with explicit required-domain unavailability cannot yield met. |
| [expired-pages.json](expired-pages.json) | Recovery after retention expires aborts the old evaluation without another outbound call or live-page reconstruction. |
| [stale-snapshot.json](stale-snapshot.json) | New evidence fences the whole bundle; a late reply cannot update state or authorize an action/close. |
| [provider-timeout.json](provider-timeout.json) | The original deadline expires during round 1; only a synthetic diagnostic, then bounded backoff. |
| [provider-crash-retry.json](provider-crash-retry.json) | Exact stored round retransmission after provider restart; request-carried continuation makes it replayable without a session. |
| [controller-crash-retry.json](controller-crash-retry.json) | New owner lease resumes a persisted checkpoint; crash after final commit reuses the unique receipt. |
| [cancellation.json](cancellation.json) | Pause cancels the driver context; no provider cancel callback or accepted gate result. |

A `call` step is the `arguments` of MCP `tools/call` for the pinned tool; a `reply` step is its JSON `structuredContent`. An `event` step describes a controller/storage/clock event, not a new MCP message or trigger. A `controller_error` is a local error projection that never commits candidate state or becomes met. Only validated non-error final candidates enter the bundle transaction.

[tools-list.json](tools-list.json) is a complete MCP `tools/list` result for the paged provider. Its tool's `inputSchema` embeds all resolved request-schema dependencies from [wire.schema.json](../wire.schema.json) and constrains args with [args.schema.json](args.schema.json). `inputSchema["x-grain-gate"]` contains the declaration, including supported record-schema digests. It is a schema annotation, not a call argument. The annotations are hints and supply no authority. [evidence.schema.json](evidence.schema.json) is the local example registry for the delivered `check` and `evaluation_context` record data; real adapters register their own bounded schemas/validators under immutable pins. The example predicate requires every suite in the context record’s required_suites to have a matching check on head a1 and a success conclusion, with observed_at no older than 60 seconds at acceptance. Coverage/count and delivered-page completeness must agree. A missing or unavailable required suite is unknown; an observed failure is unmet. This is a fixture contract, not an implicitly attached built-in gate.

From the repository root, this checks Draft 2020-12 schemas, every complete message, resolved tool input, registry data and the request/response digest chain. It requires Python 3 and `jsonschema` (Debian package `python3-jsonschema`). The fixtures contain no floating-point or unusual Unicode object keys, so sorted compact JSON produces their RFC 8785 canonical bytes. Production MUST use RFC 8785 for all supported JSON values.

```sh
python3 - <<'PY'
import hashlib, json
from pathlib import Path
from jsonschema import Draft202012Validator, FormatChecker
root = Path("design/spec/mcp-gates")
schema = json.loads((root / "wire.schema.json").read_text())
Draft202012Validator.check_schema(schema)
def validator(name):
    return Draft202012Validator(
        {**schema, "$ref": "#/$defs/" + name},
        format_checker=FormatChecker())
def digest(value):
    raw = json.dumps(value, sort_keys=True, separators=(",", ":"),
                     ensure_ascii=False).encode()
    return "sha256:" + hashlib.sha256(raw).hexdigest()
registry = json.loads((root / "examples/evidence.schema.json").read_text())
tool = json.loads((root / "examples/tools-list.json").read_text())["tools"][0]
Draft202012Validator.check_schema(registry)
Draft202012Validator.check_schema(tool["inputSchema"])
validator("discovery").validate(tool["inputSchema"]["x-grain-gate"])
for path in sorted((root / "examples").glob("*.json")):
    if path.name.endswith(".schema.json") or path.name == "tools-list.json":
        continue
    transcript = json.loads(path.read_text())
    validator("transcript").validate(transcript)
    previous, calls = None, {}
    for step in transcript["steps"]:
        if step["type"] == "event":
            continue
        message = step["message"]
        if step["type"] == "call":
            body = {k: v for k, v in message.items() if k != "request_digest"}
            assert message["request_digest"] == digest(body)
            assert message["pin"]["input_declaration"] == digest(transcript["declaration"])
            if message["round"]:
                assert message["previous_response_digest"] == digest(previous)
            Draft202012Validator(tool["inputSchema"]).validate(message)
            calls[message["round"]] = message
            records = list(message["snapshot"]["records"])
            for delivery in message["deliveries"]:
                if delivery["status"] == "ok":
                    records += delivery["page"]["records"]
            for record in records:
                Draft202012Validator(
                    {**registry, "$ref": "#/$defs/" + record["kind"]},
                    format_checker=FormatChecker()).validate(record["data"])
        else:
            assert message["request_digest"] == calls[message["round"]]["request_digest"]
            if message["kind"] == "needs_data":
                previous = message
    print(path.name + ": valid")
PY
```

Schema validation does not replace the normative cross-field rules. Implementation conformance must additionally exercise live authorization/vector/lease/clock fences, page selection and aggregate accounting, registry predicate checks, TTL storage guarantees, mode restrictions and transaction crash boundaries in [MGS-10/11](../mcp-gates.spec.md#MGS-10).
