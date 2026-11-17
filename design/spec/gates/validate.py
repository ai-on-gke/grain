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

"""Validate documentation assets, not runtime gates or live authorization."""
import json
import re
from datetime import datetime
from pathlib import Path

from jsonschema import Draft202012Validator, FormatChecker, RefResolver

ROOT = Path(__file__).resolve().parent
REPO = ROOT.parents[2]
SCHEMA = json.loads((ROOT / "contracts.schema.json").read_text())


def require(condition, message):
    if not condition:
        raise ValueError(message)


def instant(value):
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def attachment(value):
    qualified = value['origin']['kind'] + '/' + value['origin']['id'] + '/' + value['id']
    require(len(qualified) <= 256, 'qualified binding ID exceeds 256 characters')
    kind = value["type"]
    subject = value["subject"]["kind"]
    expected = ("change" if kind in {"checks", "review", "approval", "integration", "qa_stage"}
                else "repository" if kind.endswith("_health") else "task")
    require(subject == expected, "gate has incompatible subject")
    if subject == "change":
        require(value["subject"]["id"] != "*" or value["origin"]["kind"] in
                {"deployment", "repository", "resource"}, "task/template cannot broaden resource scope")
        require(value["subject"]["id"] != "*" or value["purpose"] == "guard",
                "repository wildcard is only an action-policy selector")
    config = value["config"]
    legacy = any(isinstance(item, str) and item.startswith("legacy_")
                 for item in config.values())
    require(not legacy or value["origin"]["imported"], "legacy mode needs import provenance")
    require(not (kind == "review" and legacy and value["purpose"] == "guard"),
            "legacy review cannot enforce current-head guard")
    require(not (kind == "qa_stage" and config["assignment"] == "shadow" and value["required"]),
            "shadow cannot supply required authority")
    if kind == "checks" and subject == "change" and value["subject"]["change_kind"] == "cl":
        require(config["mode"] in {"effective", "provider"}, "CL needs its presubmit source")
    if kind == "checks" and config["mode"] == "none":
        require(config["empty"] == "explicit_none", "no-CI must be explicit")
    if kind == "pipeline_health":
        require(value["subject"]["repository"] in config["repos"], "pipeline subject outside scope")


def semantic(definition, value):
    if definition == "template_defaults":
        require(value["policy"]["source"] == "template", "template defaults need template provenance")
        semantic("policy", value["policy"])
    elif definition == "attachment":
        attachment(value)
    elif definition in {"contract", "policy"}:
        if definition == "policy" and value["unguarded_actions"]:
            require(value["source"] in {"deployment", "repository", "resource"},
                    "task/template cannot exempt resource guards")
        qualified = []
        for item in value["attachments"]:
            attachment(item)
            origin = item["origin"]
            qualified.append((origin["kind"], origin["id"], item["id"]))
        require(len(set(qualified)) == len(qualified), "duplicate qualified binding")
        if definition == "contract":
            require(any(item["purpose"] == "completion" for item in value["attachments"]),
                    "contract needs a completion obligation")
    elif definition == "result":
        start, end = instant(value["observed_at"]), instant(value["valid_until"])
        require(end >= start, "result validity precedes observation")
        keys = [item["key"] for item in value["dependencies"]]
        require(len(keys) == len(set(keys)), "duplicate dependency key")
        for item in value["dependencies"]:
            require(instant(item["valid_until"]) >= instant(item["observed_at"]),
                    "invalid dependency freshness interval")
            if value["status"] == "met":
                require(item["complete"], "met cannot use incomplete required evidence")
                require(item["dirty_generation"] <= item["covered_generation"], "dirty source")
                require(instant(item["valid_until"]) >= end, "result outlives its dependency")
        if value["status"] == "met" and value["type"] in {"checks", "review", "approval"}:
            heads = [item["head"] for item in value["dependencies"] + value["evidence"]]
            require(heads and all(head is not None for head in heads), "exact head missing")
            require(len(set(heads)) == 1, "mixed head evidence")
            require(value["evidence"], "positive current-head evidence missing")
    elif definition == "action_request":
        require(value["target"]["kind"] == "change", "action needs canonical change")
        expected = "pr" if value["action"] == "change.merge" else "cl"
        require(value["target"]["change_kind"] == expected, "wrong action adapter")
        require(value["target"]["id"] not in {"task_changes", "*"}, "action needs one resolved resource")
    elif definition == "action_receipt":
        semantic("action_request", value["request"])
        require(value['operation_id'] == value['request']['request_id'],
                'receipt must retain the request operation identity')
        if value['intent'] == 'approve_and_queue':
            require(value['disposition'] == 'no_effect', 'approval enrollment is not integration')
        if value["disposition"] == "succeeded":
            require(value["result_version"] is not None and value["evidence"],
                    "success needs authoritative receipt evidence")
        if value["disposition"] == "outcome_unknown":
            require(value["result_version"] is None, "uncertain is not known success")
    elif definition == "exception":
        seconds = (instant(value["expires_at"]) - instant(value["issued_at"])).total_seconds()
        ceiling = 3600 if value["kind"] == "action_override" else 7 * 86400
        require(0 < seconds <= ceiling, "exception expiry outside allowed bound")
        if value["kind"] == "action_override":
            require(value["target"]["kind"] == "change", "action exception needs resource")
    elif definition == "context":
        seconds = (instant(value["deadline"]) - instant(value["now"])).total_seconds()
        require(0 < seconds <= 10, "evaluation deadline out of bounds")


def no_remote(uri):
    raise ValueError("remote schema resolution forbidden: " + uri)


def check_local_refs(node):
    if isinstance(node, dict):
        if "$ref" in node:
            pointer = node["$ref"]
            require(pointer.startswith("#/"), "schema reference must be local")
            target = SCHEMA
            for part in pointer[2:].split("/"):
                target = target[part.replace("~1", "/").replace("~0", "~")]
        for value in node.values():
            check_local_refs(value)
    elif isinstance(node, list):
        for value in node:
            check_local_refs(value)


def main():
    Draft202012Validator.check_schema(SCHEMA)
    check_local_refs(SCHEMA)
    manifest = json.loads((ROOT / "examples/manifest.json").read_text())
    files = {entry["file"] for entry in manifest}
    require(len(files) == len(manifest), "duplicate example manifest entry")
    require(files == {p.name for p in (ROOT / "examples").glob("*.json")
                      if p.name != "manifest.json"}, "unlisted example or missing fixture")
    seen_types = set()
    structural = semantic_failures = 0
    for entry in manifest:
        value = json.loads((ROOT / "examples" / entry["file"]).read_text())
        require(len(json.dumps(value).encode()) <= 262144, "oversized example")
        resolver = RefResolver.from_schema(SCHEMA, handlers={"http": no_remote, "https": no_remote})
        validator = Draft202012Validator(
            {"$ref": "#/$defs/" + entry["definition"], "$defs": SCHEMA["$defs"]},
            resolver=resolver, format_checker=FormatChecker())
        errors = list(validator.iter_errors(value))
        require((not errors) == entry["schema_valid"],
                entry["file"] + ": unexpected schema verdict: " + (errors[0].message if errors else "accepted"))
        if errors:
            structural += 1
            continue
        try:
            semantic(entry["definition"], value)
            accepted = True
        except ValueError:
            accepted = False
        require(accepted == entry["semantic_valid"], entry["file"] + ": unexpected semantic verdict")
        if not accepted:
            semantic_failures += 1
        if accepted and entry["definition"] == "attachment":
            seen_types.add(value["type"])
    registered = {branch["properties"]["type"]["const"]
                  for branch in SCHEMA["$defs"]["attachment"]["oneOf"]}
    require(seen_types == registered, "catalogue type lacks a valid attachment example")
    for path in (ROOT / "history").rglob("*.schema.json"):
        Draft202012Validator.check_schema(json.loads(path.read_text()))
    # Check local file links on canonical documents. Historical prose retains
    # old requirement anchors; its wire assets are explicitly non-normative.
    docs = list(ROOT.glob("*.md")) + [ROOT / "examples/README.md", REPO / "design/design/gates.design.md"]
    for path in docs:
        for target in re.findall(r"\]\(([^)\s]+)\)", path.read_text()):
            if target.startswith(("http:", "https:", "#", "/", "mailto:")):
                continue
            require((path.parent / target.split("#")[0]).exists(),
                    str(path.relative_to(REPO)) + ": broken local link: " + target)
    from validate_api import validate as validate_api
    validate_api(semantic)
    print(f"Validated {len(manifest)} examples, {len(registered)} gate types, "
          f"{structural} structural and {semantic_failures} semantic denials; schemas and local links pass.")


if __name__ == "__main__":
    main()
