#!/usr/bin/env python3
"""Validate documentation schemas, wire transcripts and locally resolved digests."""
import hashlib
import json
from pathlib import Path
from jsonschema import Draft202012Validator, FormatChecker, RefResolver

ROOT = Path(__file__).resolve().parent
DOCS = {}
for filename in ("wire.schema.json", "action-bindings.schema.json",
                 "read-operations.schema.json", "examples/evidence.schema.json"):
    path = ROOT / filename
    doc = json.loads(path.read_text())
    Draft202012Validator.check_schema(doc)
    DOCS[path.as_uri()] = doc
    DOCS[doc["$id"]] = doc
    DOCS["https://grain.dev/spec/mcp-gates/" + filename] = doc

def canonical(value):
    # Fixtures use only integer/ASCII values; production requires RFC 8785.
    return json.dumps(value, sort_keys=True, separators=(",", ":"),
                      ensure_ascii=False).encode()

def digest(value):
    return "sha256:" + hashlib.sha256(canonical(value)).hexdigest()

def no_remote(uri):
    raise ValueError("Remote schema resolution forbidden: " + uri)

def validate(filename, name, value):
    doc = DOCS[(ROOT / filename).as_uri()]
    resolver = RefResolver(base_uri=(ROOT / filename).as_uri(), referrer=doc,
                           store=DOCS, handlers={"http": no_remote, "https": no_remote})
    Draft202012Validator({**doc, "$ref": "#/$defs/" + name},
                         resolver=resolver, format_checker=FormatChecker()).validate(value)

def resolved(value, doc):
    if isinstance(value, dict):
        if "$ref" in value:
            filename, fragment = value["$ref"].split("#")
            target = doc if not filename else DOCS["https://grain.dev/spec/mcp-gates/" + filename]
            item = target
            for part in fragment.strip("/").split("/"):
                item = item[part]
            return resolved(item, target)
        return {key: resolved(item, doc) for key, item in value.items()}
    if isinstance(value, list):
        return [resolved(item, doc) for item in value]
    return value

registry = json.loads((ROOT / "read-operations.registry.json").read_text())
ops = DOCS[(ROOT / "read-operations.schema.json").as_uri()]
assert registry["registry_digest"] == digest(registry["operations"])
entries = {}
for entry in registry["operations"]:
    validate("read-operations.schema.json", "registry_entry", entry)
    assert entry["operation"] not in entries
    entries[entry["operation"]] = entry
    args_name = entry["operation"].replace(".", "_") + "_args"
    assert entry["args_schema_digest"] == digest(resolved(ops["$defs"][args_name], ops))
    assert entry["result_schema_digest"] == digest(resolved(ops["$defs"]["operation_result"], ops))
    assert entry["record_schema_digest"] == digest(resolved(ops["$defs"][entry["record_kind"] + "_data"], ops))

def declaration(value, action=False):
    validate("action-bindings.schema.json" if action else "wire.schema.json",
             "guard_discovery" if action else "discovery", value)
    assert value["read_registry"] == registry["registry_digest"]
    for entry in value["read_operations"]:
        assert entry == entries[entry["operation"]]
    kinds = [entry["kind"] for entry in value["record_schemas"]]
    assert len(kinds) == len(set(kinds))
    for domain in value["inputs"]:
        assert set(domain["operations"]) <= {entry["operation"] for entry in value["read_operations"]}

def page(delivery, context):
    validate("read-operations.schema.json", "operation_result", delivery)
    if delivery["status"] != "ok":
        return []
    request, value = delivery["request"], delivery["page"]
    assert value["evidence_context_id"] == context["id"]
    assert value["read_id"] == request["read_id"]
    assert value["operation"] == request["operation"]
    assert value["args_digest"] == digest(request["args"])
    assert value["subject"] == request["args"].get("resource", value["subject"])
    assert len(canonical(value)) <= 131072
    if value["coverage"]["empty"]:
        assert value["coverage"]["complete"] and not value["records"] and value["next_cursor"] is None
    if value["coverage"]["complete"]:
        assert value["next_cursor"] is None
    for record in value["records"]:
        entry = entries[request["operation"]]
        assert record["kind"] == entry["record_kind"]
        validate("read-operations.schema.json", record["kind"] + "_data", record["data"])
    return value["records"]

tool = json.loads((ROOT / "examples/tools-list.json").read_text())["tools"][0]
Draft202012Validator.check_schema(tool["inputSchema"])
declaration(tool["inputSchema"]["x-grain-gate"])
for path in sorted((ROOT / "examples").glob("*.json")):
    if path.name.endswith((".schema.json", ".mcp.json")) or path.name in ("tools-list.json", "read-operations.json"):
        continue
    transcript = json.loads(path.read_text())
    action = path.name == 'action-review-brokered.json'
    validate('action-bindings.schema.json' if action else 'wire.schema.json',
             'guard_transcript' if action else 'transcript', transcript)
    declaration(transcript['declaration'], action=action)
    previous, calls, delivered, complete_reads = None, {}, set(), set()
    for step in transcript["steps"]:
        if step["type"] == "event":
            continue
        message = step["message"]
        if step["type"] == "call":
            assert message["request_digest"] == digest({k: v for k, v in message.items() if k != "request_digest"})
            assert message["pin"]["input_declaration"] == digest(transcript["declaration"])
            assert message["pin"]["read_registry"] == registry["registry_digest"]
            if message["round"]:
                assert message["previous_response_digest"] == digest(previous)
                assert message["continuation_state"] == previous["continuation_state"]
            if not action:
                Draft202012Validator(tool["inputSchema"]).validate(message)
            calls[message["round"]] = message
            records = list(message["evidence_context"]["records"])
            for result in message["read_results"]:
                records += page(result, message["evidence_context"])
                if result['status'] == 'ok' and result['page']['coverage']['complete']:
                    selection = result['request']
                    scope = {k: v for k, v in selection['args'].items() if k not in ('cursor', 'limit')}
                    complete_reads.add((selection['operation'], selection['operation_version'], digest(scope)))
            for record in records:
                if record["kind"] == "action_guard_context":
                    validate("action-bindings.schema.json", "context", record["data"])
                    assert record["data"] == message["action_context"]
                    assert record["version"] == digest(record["data"])
                elif record["kind"] == "evaluation_context":
                    validate("examples/evidence.schema.json", record["kind"], record["data"])
                else:
                    validate("read-operations.schema.json", record["kind"] + "_data", record["data"])
                delivered.add((record["id"], record["kind"], record["version"]))
        else:
            assert message["request_digest"] == calls[message["round"]]["request_digest"]
            if message["kind"] == "read_requests":
                previous = message
            elif step["type"] == "reply":
                if message['status'] == 'met':
                    context = calls[message['round']]['evidence_context']
                    for requirement in context['requirements']:
                        assert (requirement['operation'], requirement['operation_version'], requirement['args_digest']) in complete_reads
                        assert requirement['domain'] not in {v['domain'] for v in context['unavailable']}
                for ref in message["evidence"]:
                    assert (ref["id"], ref["kind"], ref["version"]) in delivered
    print(path.name + ": valid")

actions = json.loads((ROOT / "action-bindings.examples.json").read_text())
validate("action-bindings.schema.json", "examples", actions)
declaration(actions["evaluator_declaration"], action=True)
for value in actions['evaluator_declarations'].values():
    declaration(value, action=True)
for binding in actions['bindings']:
    for attachment in binding['attachments']:
        assert attachment['pin']['input_declaration'] == digest(actions['evaluator_declarations'][attachment['pin']['tool']])
for name in ("guard_request", "guard_paged_request"):
    request = actions[name]
    assert request["request_digest"] == digest({k: v for k, v in request.items() if k != "request_digest"})
    assert request["pin"]["input_declaration"] == digest(actions["evaluator_declaration"])
    context = request["action_context"]
    assert context["context_digest"] == digest({k: v for k, v in context.items() if k != "context_digest"})
    records = list(request['evidence_context']['records'])
    for result in request['read_results']:
        records += page(result, request['evidence_context'])
    response = actions["guard_response" if name == "guard_request" else "guard_paged_response"]
    if response['kind'] == 'final':
        supplied = {(v['id'], v['kind'], v['version']) for v in records}
        for reference in response['evidence']:
            assert (reference['id'], reference['kind'], reference['version']) in supplied
    assert response["request_digest"] == request["request_digest"]
    assert response["action_context_digest"] == context["context_digest"]
print("Action profiles, read registry and local schema/digest chains: valid")

operation_examples = json.loads((ROOT / 'examples/read-operations.json').read_text())
assert len(operation_examples['examples']) == len(entries)
for example in operation_examples['examples']:
    validate('read-operations.schema.json', 'operation_request', example['request'])
    assert example['result']['request'] == example['request']
    page(example['result'], {'id': 'context:operation-examples'})
print('Nine exact operation request/result examples: valid')
for filename, source, tool_name in [('checks.mcp.json', 'success-paged.json', 'gate_checks'),
                                    ('reviews.mcp.json', 'action-review-brokered.json', 'gate_review')]:
    messages = json.loads((ROOT / 'examples' / filename).read_text())
    transcript = json.loads((ROOT / 'examples' / source).read_text())
    expected, call_id = [], 0
    for step in transcript['steps']:
        if step['type'] == 'call':
            call_id += 1
            expected.append({'jsonrpc':'2.0','id':call_id,'method':'tools/call',
                             'params':{'name':tool_name,'arguments':step['message']}})
        elif step['type'] == 'reply':
            expected.append({'jsonrpc':'2.0','id':call_id,'result':{'content':[],
                             'structuredContent':step['message'],'isError':False}})
    assert messages == expected
    print(filename + ': complete MCP envelope/application identity valid')
