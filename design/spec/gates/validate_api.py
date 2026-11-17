#!/usr/bin/env python3
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Offline documentation contract checks; never authenticates or executes an API."""
import hashlib
import json
import re
from pathlib import Path

from jsonschema import Draft202012Validator, FormatChecker, RefResolver

ROOT = Path(__file__).resolve().parent
REPO = ROOT.parents[2]


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(value):
    # These schemas contain only integer/ASCII JSON; sorted compact JSON is JCS here.
    data = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    return "sha256:" + hashlib.sha256(data.encode()).hexdigest()


def no_remote(uri):
    raise ValueError("remote schema resolution forbidden: " + uri)


def semantic(operation, where, value, contracts, gate_semantic):
    if where == "request":
        if operation == "schema":
            require(1 <= int(value['version']) <= 4294967295, 'registry version exceeds uint32')
        body = value.get("body", {})
        mutation = body.get("mutation", body.get("gate_edit", {}).get("mutation"))
        if value.get('reason') is not None and mutation:
            require(value['reason'].strip() == mutation['reason'].strip(),
                    'tool and domain audit reasons must agree')
        if "action_request" in body and body["action_request"] is not None:
            action = body["action_request"]
            gate_semantic("action_request", action)
            expected = mutation["expected"]
            for field in ("head", "resource_version", "contract_revision",
                          "authority_epoch", "policy_digest", "context_digest",
                          "evidence_digest"):
                require(action["expected_" + field] == expected[field],
                        "action/mutation expectations disagree: " + field)
            require(action["idempotency_key"] == mutation["idempotency_key"],
                    "action/mutation idempotency differs")
            if body.get("intent") == "approve_and_queue":
                require(action["action"] == "change.merge", "CL submit requires execute intent")
        if operation == 'template_edit':
            gate_semantic('template_defaults', body['gate_defaults'])
        if operation == 'repo_edit':
            policy = body['gate_policy']
            repo = value['owner'] + '/' + value['name']
            require(policy['source'] == 'repository' and policy['source_id'] == repo,
                    'repository policy must match the source authority')
            for retirement in body['retire_guard_bindings']:
                target = retirement['target']
                require(target['kind'] == 'change' and target['repository'] == repo and
                        target['id'] not in {'task_changes', '*'}, 'retirement needs one owned resource')
        if operation == "issue":
            if body["kind"] == "action_override":
                action = body["action_request"]
                require(action is not None and body["operation_id"] == action["request_id"],
                        "override needs its reserved operation")
                require(body["head"] == action["expected_head"] and
                        body["target"] == action["target"], "override target/head mismatch")
            else:
                require(body["operation_id"] is None and body["action_request"] is None,
                        "completion waiver cannot reserve an effect")
                require(body["task_id"] is not None, "completion waiver needs a task")
        if operation == "file":
            materialize = body["gate_admission"]["materialize"]
            for field in ("repo", "no_repo", "template_id", "fields"):
                require(body[field] == materialize[field], "filing preview input mismatch")
        return

    if value.get("mode") == "observation_only":
        bindings = value["bindings"]
        ids = [v["origin"]["kind"] + "/" + v["origin"]["id"] + "/" + v["id"]
               for v in bindings]
        require(len(ids) == len(set(ids)), "duplicate observation binding")
        for binding in bindings:
            gate_semantic("attachment", binding)
        observed = [v["result"]["binding_id"] for v in value["results"]]
        require(len(observed) == len(set(observed)) and set(observed) <= set(ids),
                "observation result must identify exactly one visible monitor")
        require(value["dirty"] or set(observed) == set(ids),
                "missing observations require dirty=true and unknown display")
    if "gate_observations" in value:
        semantic("observation", "response", value["gate_observations"], contracts, gate_semantic)
    if operation in {"registry", "schema"}:
        descriptors = value["items"] if operation == "registry" else [value]
        names = []
        for descriptor in descriptors:
            names.append(descriptor["type"])
            config = contracts["$defs"][descriptor["type"] + "_config"]
            require(descriptor["config_schema"] == config, "discovery schema differs from catalogue")
            require(descriptor["schema_digest"] == digest(config), "discovery digest mismatch")
            Draft202012Validator.check_schema(descriptor["config_schema"])
        require(len(set(names)) == len(names), "duplicate registry ID/version")
        if operation == "registry":
            registered = {b["properties"]["type"]["const"]
                          for b in contracts["$defs"]["attachment"]["oneOf"]}
            require(set(names) == registered, "registry must discover every compiled type")
            require(value["registry_digest"] == digest(descriptors), "registry digest mismatch")
    if operation == "action_preview" and value["available"]:
        require(value["coverage"] in {"enforced", "explicit_unguarded"},
                "unknown/observation-only coverage cannot be available")
        require(value["permission"]["allowed"] and not value["blockers"],
                "availability requires authority and no blockers")
        require(all(s["disposition"] == "accepted" and s["result"]["status"] == "met"
                    for s in value["results"]), "availability needs accepted current met results")
    if "state" in value and "operation_id" in value:
        state = value["state"]
        if state in {"requested", "executing"}:
            require(value["receipt"] is None, "unsettled operation cannot claim receipt")
        require((value["submit_intent"] is not None) == (value["kind"] == "submit"),
                "submit intent must identify exactly submit operations")
        if value["receipt"] is not None:
            require(value["receipt"]["intent"] == value["submit_intent"],
                    "operation and receipt intent differ")
            require(value["receipt"]["operation_id"] == value["operation_id"],
                    "operation and receipt identity differ")
        if state == "completed" and value["kind"] == "submit":
            require(value["receipt"] is not None and
                    value["receipt"]["disposition"] in {"succeeded", "no_effect"},
                    "completed submit needs settled receipt")
        if state in {"failed", "blocked", "outcome_unknown"}:
            require(value["error"] is not None, "unsuccessful operation needs structured error")
    if "evaluation_id" in value and "disposition" in value:
        if value["disposition"] == "accepted":
            require(value["accepted_at"] is not None and value["invalidated_at"] is None,
                    "accepted state timing invalid")
        if value["disposition"] == "invalidated":
            require(value["invalidated_at"] is not None and value["invalidation_reason"],
                    "invalidation needs time/cause")

    # Apply the canonical predicate/shape-independent semantics to embedded records.
    def embedded(node):
        if isinstance(node, dict):
            if "evaluation_id" in node and "status" in node:
                gate_semantic("result", node)
            if "action" in node and "expected_head" in node:
                gate_semantic("action_request", node)
            if "issued_at" in node and "binding_ids" in node:
                gate_semantic("exception", node)
                require(node["revision"] >= 1, "exception revision starts at one")
            if "api_version" in node and "authority" in node and "attachments" in node:
                gate_semantic("contract", node)
            if "api_version" in node and "unguarded_actions" in node:
                gate_semantic("policy", node)
            if "name" in node and "args" in node:
                return
            for child in node.values():
                embedded(child)
        elif isinstance(node, list):
            for child in node:
                embedded(child)
    embedded(value)


def validate(gate_semantic):
    schema = json.loads((ROOT / "api.schema.json").read_text())
    contracts = json.loads((ROOT / "contracts.schema.json").read_text())
    operations = json.loads((ROOT / "operations.json").read_text())
    Draft202012Validator.check_schema(schema)
    store = {schema["$id"]: schema, contracts["$id"]: contracts}

    def local_refs(node):
        if isinstance(node, dict):
            if "$ref" in node:
                pointer = node["$ref"]
                if pointer.startswith("contracts.schema.json#"):
                    target = contracts
                    pointer = pointer.split("#", 1)[1]
                else:
                    require(pointer.startswith("#/"), "nonlocal API reference")
                    target = schema
                    pointer = pointer[1:]
                for part in pointer.lstrip("/").split("/"):
                    target = target[part.replace("~1", "/").replace("~0", "~")]
            for child in node.values():
                local_refs(child)
        elif isinstance(node, list):
            for child in node:
                local_refs(child)
    local_refs(schema)

    def check(definition, value):
        resolver = RefResolver.from_schema(
            schema, store=store, handlers={"http": no_remote, "https": no_remote})
        validator = Draft202012Validator(
            {"$ref": "#/$defs/" + definition, "$defs": schema["$defs"]},
            resolver=resolver, format_checker=FormatChecker())
        return list(validator.iter_errors(value))

    for operation in operations:
        require(not check("operation_contract", operation), "invalid operation index record")
    indexed = {op["id"]: op for op in operations}
    require(len(indexed) == len(operations), "duplicate operation identity")
    require(len({(op["method"], op["path"]) for op in operations}) == len(operations),
            "duplicate operation route")
    require(len({op["tool"] for op in operations}) == len(operations),
            "duplicate operation tool")
    server = (REPO / "pkg/ui/server.go").read_text()
    routes = dict(re.findall(r's\.route\("([^"]+)", s\.(\w+)\)', server))
    scopes = dict(re.findall(r'"((?:GET|POST|PATCH|PUT|DELETE) /[^"]+)":\s*"([^"]+)"',
                             (REPO / "pkg/ui/route_scopes.go").read_text()))
    for op in operations:
        pattern = op["method"] + " " + op["path"]
        require(op["request"] in schema["$defs"] and op["response"] in schema["$defs"],
                "operation schema missing")
        require(op["status"] in {"existing", "changed", "proposed"}, "bad route status")
        if op["status"] != "proposed":
            require(pattern in routes, "claimed current route missing: " + pattern)
            require(scopes[pattern] in op["scopes"], "shared route scope drift")
            handler = routes[pattern].removeprefix("handle")
            # Mirrors snake() for these handlers; runtime uniqueness check stays authoritative.
            name = re.sub(r"([A-Z]+)([A-Z][a-z])", r"\1_\2", handler)
            name = re.sub(r"([a-z0-9])([A-Z])", r"\1_\2", name).lower()
            require(op["tool"] == "api_" + name, "current tool alias drift: " + pattern)
        else:
            require(pattern not in routes, "proposed route already exists; review status")
        require(len(op["tool"]) <= 40, "proposed qualified tool name may exceed generator cap")
        for name in (op["request"], op["response"]):
            require(name in (ROOT / "http-api.spec.md").read_text(), "unlinked operation schema")

    context_set = json.loads((ROOT / "api-context-example.json").read_text())
    require(not check("context_set", context_set), "invalid aggregate context example")
    contexts = context_set["contexts"]
    ids = [v["binding_id"] for v in contexts]
    require(ids == sorted(set(ids)), "context IDs must be sorted and unique")
    bindings = context_set["bindings"]
    qualified = [v["origin"]["kind"] + "/" + v["origin"]["id"] + "/" + v["id"]
                 for v in bindings]
    require(qualified == ids, "context set must cover exact effective bindings")
    for context in contexts:
        gate_semantic("context", context)
    for binding in bindings:
        gate_semantic("attachment", binding)

    observation = json.loads((ROOT / "api-observation-example.json").read_text())
    require(not check("observation_view", observation), "invalid read-only observation example")
    semantic("observation", "response", observation, contracts, gate_semantic)
    observation_task = json.loads((ROOT / "api-observation-task-example.json").read_text())
    require(not check("task_response", observation_task), "invalid observation task projection")
    semantic("task", "response", observation_task, contracts, gate_semantic)

    exchanges = json.loads((ROOT / "api-examples.json").read_text())
    require({e["operation"] for e in exchanges} == set(indexed) and
            len(exchanges) == len(indexed), "one success/denial exchange required per operation")
    for exchange in exchanges:
        op = indexed[exchange["operation"]]
        for where in ("request", "response"):
            errors = check(op[where], exchange[where])
            require(not errors, op["id"] + " " + where + ": " +
                    (errors[0].message if errors else ""))
            semantic(op["id"], where, exchange[where], contracts, gate_semantic)
        require(exchange["response_status"] == op["success_status"], "success code drift")
        require(exchange["error_status"] == 403, "expected auth denial example")
        require(not check("failure", exchange["error"]), "invalid denial response")
        for field in ("tool_success", "tool_error"):
            require(not check(op["tool_result"], exchange[field]), "invalid tool result envelope")
        success = exchange["tool_success"]
        require(success["isError"] is False and
                json.loads(success["content"][0]["text"]) == exchange["response"],
                "tool success differs from HTTP response")
        denied = exchange["tool_error"]
        prefix = op["tool"] + " failed: HTTP " + str(exchange["error_status"]) + ": "
        require(denied["isError"] is True and denied["content"][0]["text"].startswith(prefix),
                "tool error lost HTTP status")
        require(json.loads(denied["content"][0]["text"][len(prefix):]) == exchange["error"],
                "tool failure differs from HTTP response")

    evidence = json.loads((ROOT / "api-evidence-examples.json").read_text())
    require({v["reference"]["kind"] for v in evidence} ==
            set(contracts["$defs"]["evidence"]["properties"]["kind"]["enum"]),
            "every evidence kind needs a concrete fact example")
    for value in evidence:
        errors = check("evidence_record", value)
        require(not errors, "invalid evidence example: " +
                (errors[0].message if errors else ""))
        semantic("evidence", "response", value, contracts, gate_semantic)
    flows = json.loads((ROOT / "api-flow-examples.json").read_text())
    for example in flows:
        op = indexed[example["operation"]]
        require(example["response_status"] == op["success_status"], "flow status mismatch")
        for where in ("request", "response"):
            errors = check(op[where], example[where])
            require(not errors, example["name"] + ": invalid " + where + ": " +
                    (errors[0].message if errors else ""))
            semantic(op["id"], where, example[where], contracts, gate_semantic)

    denials = json.loads((ROOT / "api-invalid-examples.json").read_text())
    for example in denials:
        op = indexed[example["operation"]]
        errors = check(op[example["where"]], example["value"])
        require((not errors) == example["schema_valid"], example["name"] + ": shape verdict")
        if not errors:
            try:
                semantic(op["id"], example["where"], example["value"], contracts, gate_semantic)
                accepted = True
            except ValueError:
                accepted = False
            require(accepted == example["semantic_valid"], example["name"] + ": semantic verdict")
    for example in json.loads((ROOT / "api-error-examples.json").read_text()):
        require(not check("failure", example["body"]), "invalid shared error response")
    print(f"Validated {len(operations)} API operations/tools, complete success/denial exchanges, "
          f"{len(evidence)} typed evidence records, {len(flows)} asynchronous/override flows, "
          f"{len(denials)} invalid/history cases, local references and current route/scope/tool mappings.")


if __name__ == "__main__":
    from validate import semantic as gate_semantic
    validate(gate_semantic)
