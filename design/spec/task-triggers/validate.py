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
"""Validate proposed trigger documentation contracts, without contacting a provider.

Run: python3 design/spec/task-triggers/validate.py
Dependency: Debian python3-jsonschema (no repository/runtime dependency added).
This is an artifact checker, not an implementation of matching or task filing.
"""
import hashlib
import json
from pathlib import Path
import re

from jsonschema import Draft202012Validator, FormatChecker

ROOT = Path(__file__).resolve().parent
WIRE = json.loads((ROOT / 'wire.schema.json').read_text())
PROTOCOL = json.loads((ROOT / 'protocol.schema.json').read_text())
STORE = {schema['$id']: schema for schema in (WIRE, PROTOCOL)}


def bundle(value, document):
    """Resolve only our two registered local schemas; never use network fallback.

    Flatten definition namespaces to support Debian jsonschema 4.10's URN
    fragment behavior as well as newer validators, without changing the wire.
    """
    if isinstance(value, list):
        return [bundle(item, document) for item in value]
    if not isinstance(value, dict):
        return value
    result = {}
    for key, item in value.items():
        if key == '$ref':
            uri, _, fragment = item.partition('#')
            target = STORE[uri] if uri else document
            assert fragment.startswith('/$defs/'), item
            prefix = 'wire_' if target is WIRE else 'protocol_'
            result[key] = '#/$defs/' + prefix + fragment.removeprefix('/$defs/')
        else:
            result[key] = bundle(item, document)
    return result


DEFINITIONS = {}
for document, prefix in ((WIRE, 'wire_'), (PROTOCOL, 'protocol_')):
    for name, definition in document['$defs'].items():
        DEFINITIONS[prefix + name] = bundle(definition, document)


def validator(name):
    schema = WIRE if name in WIRE['$defs'] else PROTOCOL
    assert name in schema['$defs'], name
    prefix = 'wire_' if schema is WIRE else 'protocol_'
    return Draft202012Validator(
        {'$ref': '#/$defs/' + prefix + name, '$defs': DEFINITIONS},
        format_checker=FormatChecker(),
    )


def check(name, value):
    errors = list(validator(name).iter_errors(value))
    assert not errors, '\n'.join(str(error) for error in errors[:3])


def digest(value):
    # Fixture identities use ASCII strings and interoperable integers only;
    # sorted compact JSON is RFC 8785 canonical for these fixture values.
    return 'sha256:' + hashlib.sha256(json.dumps(
        value, sort_keys=True, separators=(',', ':'), ensure_ascii=False,
    ).encode()).hexdigest()


for schema in (WIRE, PROTOCOL):
    Draft202012Validator.check_schema(schema)

manifest = json.loads((ROOT / 'examples/manifest.json').read_text())
for filename, definition in manifest['valid'].items():
    value = json.loads((ROOT / 'examples' / filename).read_text())
    check(definition, value)
    if definition == 'transcript':
        for step in value['steps']:
            check(step['definition'], step['message'])
        receipts = {step['message']['receipt_id']: step['message']
                    for step in value['steps'] if step['definition'] == 'receipt'}
        assert len(receipts) == value['expected_receipts'], filename
        assert len({v['task_id'] for v in receipts.values() if v['disposition'] == 'created'}) == value['expected_tasks'], filename
        binding = json.loads((ROOT / 'examples' / value['binding_fixture']).read_text())
        check('binding', binding)

for filename in manifest['invalid']:
    fixture = json.loads((ROOT / 'examples' / filename).read_text())
    assert list(validator(fixture['definition']).iter_errors(fixture['message'])), filename

# Every fixture is deliberately indexed, including all full invalid messages.
indexed = set(manifest['valid']) | set(manifest['invalid']) | {'manifest.json'}
assert indexed == {p.name for p in (ROOT / 'examples').glob('*.json')}

# Complete examples in the original spec must continue to match the shipped fixtures.
spec = (ROOT / 'task-triggers.spec.md').read_text()
blocks = re.findall(r'```json\n(.*?)\n```', spec, re.S)
for block, filename in zip(blocks, ('github-event.json', 'time-binding.json', 'github-binding.json')):
    assert json.loads(block) == json.loads((ROOT / 'examples' / filename).read_text()), filename

# Verify identity arithmetic and request/receipt consistency on the worked traces.
for filename, source_lineage, dedup_lineage in (
    ('github-sequence.json', 'source-uuid-github-17', 'binding-uuid-review'),
    ('time-sequence.json', 'source-uuid-daily', 'binding-uuid-daily'),
):
    trace = json.loads((ROOT / 'examples' / filename).read_text())
    event = next((step['message'] for step in trace['steps'] if step['definition'] == 'event'), None)
    if event is None:
        event = next(step['message']['events'][0] for step in trace['steps']
                     if step['definition'] == 'provider_result' and step['message']['events'])
    binding = json.loads((ROOT / 'examples' / trace['binding_fixture']).read_text())
    event_key = digest(['trigger-event-v1', source_lineage, event['event_id']])
    match_key = digest(['trigger-match-v1', binding['id'], binding['version'], event_key])
    firing_key = digest(['trigger-file-v1', binding['id'], dedup_lineage, event_key, 'primary', 'live'])
    for step in trace['steps']:
        message = step['message']
        if step['definition'] in ('receipt', 'file_request'):
            assert message['firing_key'] == firing_key, filename
            assert message['match_id'] == match_key, filename
        if step['definition'] == 'receipt':
            assert message['event_key'] == event_key, filename
            assert message['template_digest'] == binding['template']['revision'], filename
        if step['definition'] == 'provider_call':
            assert message['arguments']['provider_pin'] == binding['provider'], filename

descriptor = json.loads((ROOT / 'examples/descriptor.json').read_text())
binding = json.loads((ROOT / 'examples/github-binding.json').read_text())
source = descriptor['source_types'][0]
assert binding['provider']['schema_digest'] == digest(source)
assert source['schema_digest'] == digest(source['source_schema'])
for kind in source['kinds']:
    assert kind['schema_digest'] == digest(kind['extension_schema'])
    assert kind['payload_schema_digest'] == digest(kind['payload_schema'])

authority = json.loads((ROOT / 'examples/filing-authorization.json').read_text())
enabled_binding = json.loads((ROOT / 'examples/github-enabled-binding.json').read_text())
assert authority['binding_digest'] == digest(enabled_binding)
trace = json.loads((ROOT / 'examples/github-sequence.json').read_text())
for step in trace['steps']:
    if step['definition'] == 'receipt':
        assert step['message']['authority_digest'] == digest(authority)
    if step['definition'] == 'match_record':
        assert step['message']['input_digest'] == digest(step['message']['mapped_inputs'])
        assert step['message']['binding_digest'] == digest(enabled_binding)

# Local links and anchors, including repository-relative source evidence.
documents = [ROOT / 'task-triggers.spec.md', ROOT / 'implementation.spec.md',
             ROOT / 'examples/README.md', ROOT.parents[1] / 'design/task-triggers.design.md']
ids = set()
for path in documents:
    text = path.read_text()
    for requirement in re.findall(r'^#{1,6} .*?\{#(TTS-\d+)\}', text, re.M):
        assert requirement not in ids, requirement
        ids.add(requirement)
    for target in re.findall(r'\[[^\]\n]+\]\(([^\s()]+)\)', text):
        if re.match(r'^[a-z]+:', target):
            continue
        target_path, _, anchor = target.partition('#')
        resolved = (path.parent / target_path).resolve() if target_path else path
        assert resolved.exists(), (path.name, target)
        if anchor and resolved.suffix == '.md':
            dest = resolved.read_text()
            anchors = set(re.findall(r'\{#([^}]+)\}', dest))
            for heading in re.findall(r'^#{1,6}\s+(.+)', dest, re.M):
                slug = re.sub(r'[^\w\- ]', '', heading.lower()).replace(' ', '-')
                anchors.add(slug)
            assert anchor in anchors, (path.name, target)
assert ids == {'TTS-' + str(i) for i in range(1, 26)}, ids
print(f'PASS: 2 schemas; {len(manifest["valid"])} valid fixtures; '
      f'{len(manifest["invalid"])} schema rejections; full transcript messages and keys; '
      'inline examples; local links/anchors; 25 unique requirement IDs')
