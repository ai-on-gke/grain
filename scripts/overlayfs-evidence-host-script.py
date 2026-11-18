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

"""Print a reviewable capability-server entry. Does not contact/change Grain."""
import json
import sys
from pathlib import Path

if len(sys.argv) != 1:
    sys.exit("usage: overlayfs-evidence-host-script.py (no arguments)")
collector = Path(__file__).with_name("overlayfs-evidence.py").read_text()
print(json.dumps({
    "id": "https://grain.dev/capabilities/host-script",
    "entry": {
        "builtin": "host-script",
        "run": "host",
        "command": "grain-host-script",
        "config": {"scripts": [{
            "name": "overlayfs-evidence",
            "description": "Collect bounded read-only host evidence for the 2026-11-18 OverlayFS restart warnings.",
            "usage": "grain host-script overlayfs-evidence (no arguments; emits JSONL, including unavailable/incomplete evidence)",
            "script": "#!/usr/bin/env bash\nset -euo pipefail\n[ \"$#\" -eq 0 ] || exit 2\nexec /usr/bin/python3 -I - <<'GRAIN_OVERLAYFS_EVIDENCE_PY'\n" + collector + "\nGRAIN_OVERLAYFS_EVIDENCE_PY\n",
            "timeout": 600,
            "approve": True,
            "max_output": 3 * 1024 * 1024,
            "max_stdin": 0,
            "max_calls": 3,
            "max_args": 0,
            "max_arg_len": 1
        }]}
    },
    "grant": {
        "capability": "https://grain.dev/capabilities/host-script#host-script",
        "with": {"script": "overlayfs-evidence"}
    }
}, indent=2))
