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

"""Validate proposed documentation fixtures, not a runtime protocol implementation.

Requires Debian python3-jsonschema. ASCII fixture digests use sorted compact JSON,
which equals RFC 8785 for these values; this is not a general JCS implementation.
Live authority, storage and concurrency checks remain future conformance tests.
"""
import hashlib
import json
import re
from datetime import datetime
from pathlib import Path

from jsonschema import Draft202012Validator, FormatChecker

BASE = Path(__file__).resolve().parent
ROOT = BASE.parents[2]
SCHEMA = json.loads((BASE / "wire.schema.json").read_text())
EXAMPLES = BASE / "examples"


def require(condition, message):
    if not condition:
        raise ValueError(message)


def load(name):
    return json.loads((EXAMPLES / name).read_text())


def canonical(value):
    encoded = json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":"))
    require(encoded.isascii(), "fixture digest helper only accepts ASCII")
    return encoded.encode()


def digest(value):
    return "sha256:" + hashlib.sha256(canonical(value)).hexdigest()


def validate_shape(definition, value):
    wrapper = dict(SCHEMA, **{"$ref": "#/$defs/" + definition})
    wrapper.pop("oneOf")
    Draft202012Validator(wrapper, format_checker=FormatChecker()).validate(value)


def depth(value):
    if isinstance(value, dict):
        return 1 + max((depth(v) for v in value.values()), default=0)
    if isinstance(value, list):
        return 1 + max((depth(v) for v in value), default=0)
    return 0


def moment(text):
    return datetime.fromisoformat(text.replace("Z", "+00:00"))


REG = load("registration.json")


def semantic(value, request=None):
    require(depth(value) <= 16, "JSON container depth exceeded")
    require(len(canonical(value)) <= 262144, "wire byte limit exceeded")
    if "min_poll_seconds" in value:
        require(value["min_poll_seconds"] <= value["max_poll_seconds"], "inverted poll bounds")
    if request:
        require(value["request_id"] == request["request_id"], "request identity mismatch")
        if "reference" in value:
            require(value["reference"] == request["reference"], "Observe redirected identity")
        if "observation_generation" in value:
            require(value["observation_generation"] == request["observation_generation"], "generation mismatch")
        for hint in value.get("events", []):
            require(hint["subscription_id"] == request["subscription_id"], "subscription mismatch")
            require(hint["generation"] == request["generation"], "hint generation mismatch")
            require(hint["reference_key"] == digest(request["reference"])[7:], "hint scope mismatch")
    if "observed_at" in value:
        require(moment(value["valid_until"]) >= moment(value["observed_at"]), "negative freshness")
        if value["availability"] != "available":
            require(value["valid_until"] == value["observed_at"], "unavailable cannot renew freshness")
        else:
            kind = next(k for k in REG["descriptor"]["kinds"] if k["kind"] == value["reference"]["resource_kind"])
            require(value["fact_version"] == kind["fact_version"], "unregistered fact version")
            Draft202012Validator(kind["identifier_schema"]).validate(value["reference"]["identifier"])
            Draft202012Validator(kind["details_schema"]).validate(value["details"])
        evidence_bytes = 0
        for evidence in value["evidence"]:
            registered = next(e for e in REG["evidence_schemas"] if e["kind"] == evidence["kind"])
            require(evidence["schema_digest"] == registered["schema_digest"], "evidence schema mismatch")
            require(evidence["version"] == registered["version"], "evidence version mismatch")
            Draft202012Validator(registered["schema"]).validate(evidence["data"])
            require(evidence["digest"] == digest(evidence["data"]), "evidence digest mismatch")
            evidence_bytes += len(canonical(evidence["data"]))
        require(evidence_bytes <= 32768, "evidence data bytes exceeded")
    if "reference" in value and "pin" in value:
        require(value["reference"]["provider"] == value["pin"]["identity"], "provider/pin mismatch")
    if "tracking" in value and isinstance(value.get("candidate"), dict):
        kind = REG["descriptor"]["kinds"][0]
        Draft202012Validator(kind["candidate_schema"]).validate(value["candidate"])


def check_registration():
    require(REG["identity"] == REG["pin"]["identity"], "registration identity mismatch")
    require(REG["descriptor"]["release"] == REG["pin"]["release"], "release mismatch")
    kinds = sorted(REG["descriptor"]["kinds"], key=lambda k: k["kind"])
    for evidence in REG["evidence_schemas"]:
        Draft202012Validator.check_schema(evidence["schema"])
        require(digest(evidence["schema"]) == evidence["schema_digest"], "registered schema digest mismatch")
    identifier_fields = ("kind", "identifier_schema", "candidate_schema", "selector_schema", "facets", "supports_pinned")
    expected = digest([{k: kind[k] for k in identifier_fields} for kind in kinds])
    require(expected == REG["pin"]["identifier_schema"], "identifier digest mismatch")
    expected = digest([{k: kind[k] for k in ("kind", "details_schema", "fact_version")} for kind in kinds])
    require(expected == REG["pin"]["details_schema"], "details digest mismatch")
    expected = digest({"facts": SCHEMA["$defs"]["facts"], "evidence_schemas": sorted(REG["evidence_schemas"], key=lambda e: e["kind"]), "access_scope_schema": REG["access_scope_schema"]})
    require(expected == REG["pin"]["fact_schema"], "fact digest mismatch")
    unpinned = {k: v for k, v in REG.items() if k not in ("pin", "enabled")}
    expected = digest({"registration": unpinned, "mcp_server": load("mcp-server.json")})
    require(expected == REG["pin"]["config_revision"], "configuration digest mismatch")


def check_docs():
    files = list(BASE.glob("*.md")) + [EXAMPLES / "README.md", ROOT / "design/design/tracked-resources.design.md"]
    link_count = fence_count = 0
    for path in files:
        content = path.read_text()
        for match in re.finditer(r"```json\n(.*?)\n```", content, re.S):
            json.loads(match.group(1))
            fence_count += 1
        for target in re.findall(r"\[[^\]\n]+\]\(([^)]+)\)", content):
            if target.startswith(("https:", "http:", "/")):
                continue
            filename, _, anchor = target.partition("#")
            destination = (path.parent / filename).resolve() if filename else path
            require(destination.exists(), f"{path}: missing link {target}")
            if anchor and destination.suffix == ".md":
                text = destination.read_text()
                anchors = set(re.findall(r"\{#([^}]+)\}", text))
                for heading in re.findall(r"^#+ (.+)$", text, re.M):
                    slug = re.sub(r"[^\w\- ]", "", heading.lower()).replace(" ", "-")
                    anchors.add(slug)
                require(anchor in anchors, f"{path}: missing anchor {target}")
            link_count += 1
    return link_count, fence_count


def main():
    Draft202012Validator.check_schema(SCHEMA)
    check_registration()
    entries = load("manifest.json")
    for entry in entries:
        value = load(entry["file"])
        if entry.get("semantic"):
            validate_shape(entry["definition"], value)
            try:
                semantic(value, load(entry["request"]) if entry.get("request") else None)
            except ValueError:
                pass
            else:
                raise ValueError("semantic invalid fixture accepted: " + entry["file"])
        else:
            errors = list(Draft202012Validator(
                {"$schema": SCHEMA["$schema"], "$defs": SCHEMA["$defs"], "$ref": "#/$defs/" + entry["definition"]},
                format_checker=FormatChecker(),
            ).iter_errors(value))
            require(bool(errors) != entry["valid"], "unexpected fixture verdict: " + entry["file"])
            if entry["valid"]:
                semantic(value)
    for result, request in [
        ("observe-result.json", "observe-request.json"),
        ("unauthorized-result.json", "observe-request.json"),
        ("watch-read-result.json", "watch-read.json"),
    ]:
        semantic(load(result), load(request))
    covered = {entry["file"] for entry in entries}
    actual = {p.name for p in EXAMPLES.glob("*.json")} - {"manifest.json", "mcp-server.json"}
    require(actual == covered, "fixture missing from manifest")
    links, fences = check_docs()
    print(f"Validated schema, registration digests, {len(entries)} fixtures, request/result pairs, {links} local links and {fences} JSON fences")


if __name__ == "__main__":
    main()
