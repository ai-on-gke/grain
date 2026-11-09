#!/usr/bin/env bash
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

# rollback.sh RUN_DIR AGENT_TASK: §2 item 3 against the demo start-demo.sh
# left in RUN_DIR -- `grain release rollback` and POST
# /api/releases/{v}/rollback as a person and as an agent (through
# agent-proxy.py), on acme/widgets (GitHub Actions: a signed rollback
# record dispatched to the sim) and on acme/gadgets (grain CI: a
# promote-release.yml:rollback run on -fake-ci). Prints every command,
# its exit code and output, the dispatched inputs and the reason hash.
set -uo pipefail
run="${1:?run dir}"
agent_task="${2:?agent task id}"
here="$(cd "$(dirname "$0")" && pwd)"
grain="${GRAIN:-/home/debian/bin/grain}"
GRAIN_SERVER="http://127.0.0.1:${PORT:-18080}"
GRAIN_TOKEN="$(cat "$run/code")"
export GRAIN_SERVER GRAIN_TOKEN
bare="$(cat "$run/bare")"
auth=(-H "Authorization: Bearer $GRAIN_TOKEN" -H 'Content-Type: application/json')
step() { printf '\n$ %s\n' "$*"; "$@"; printf '[exit %d]\n' "$?"; }

# The releases a rollback names: v2.3.0 and v2.4.0 on acme/widgets' sim.
git --git-dir "$bare" -c user.name=train -c user.email=train@example.invalid tag -f -a v2.3.0 -m v2.3.0 release/1.0 >/dev/null
git --git-dir "$bare" -c user.name=train -c user.email=train@example.invalid tag -f -a v2.4.0 -m v2.4.0 main >/dev/null
# acme/gadgets: a grain CI repository (no sim behind it; -fake-ci runs it).
curl -s "${auth[@]}" -X POST -d '{"repo":"acme/gadgets"}' "$GRAIN_SERVER/api/repos" >/dev/null
curl -s "${auth[@]}" -X PUT -d '{"ciMode":"grain"}' "$GRAIN_SERVER/api/repos/acme/gadgets/ci-mode" >/dev/null

python3 "$here/agent-proxy.py" "${PROXY_PORT:-18081}" "$GRAIN_SERVER" "$agent_task" &
proxy=$!
trap 'kill $proxy 2>/dev/null' EXIT
sleep 1
reason='v2.4.0 drops queued widgets on restart (#4242)'

echo "== Actions repo acme/widgets =="
step "$grain" release rollback -repo acme/widgets -to v2.3.0 -reason "$reason" v2.4.0
step "$grain" release rollback -repo acme/widgets -to v2.3.0 -reason "$reason" v2.4.0
echo "-- agent ($agent_task, api:release-manager) through the sandbox proxy stub:"
step env GRAIN_SERVER="http://127.0.0.1:${PROXY_PORT:-18081}" "$grain" release rollback -repo acme/widgets -reason "agent says so" v2.4.0
step curl -s -w '\n[http %{http_code}]\n' "${auth[@]}" -H "X-Grain-Acting-Task: $agent_task" -X POST \
  -d '{"repo":"acme/widgets","to":"v2.3.0","reason":"agent says so"}' "$GRAIN_SERVER/api/releases/v2.4.0/rollback"
echo "-- what the sim was sent:"
curl -s "$GRAIN_SERVER/api/e2e/github-sim/repos/acme/widgets/actions/workflows/promote-release.yml/dispatches" \
  >"$run/dispatches-rollback.json"
python3 - "$run/dispatches-rollback.json" "$reason" <<'EOF'
import base64, hashlib, json, sys
ds = [d for d in json.load(open(sys.argv[1])) if d["inputs"].get("decision") == "rollback"]
print(f"{len(ds)} rollback dispatch(es)")
for d in ds:
    rec = base64.b64decode(d["inputs"]["approval"]).decode()
    shown = {k: (v if k not in ("approval", "approval_signature") else v[:16] + "...") for k, v in d["inputs"].items()}
    print("ref:", d["ref"], "inputs:", json.dumps(shown, sort_keys=True))
    print(rec, end="")
    want = hashlib.sha256(d["inputs"].get("reason", "").encode()).hexdigest()
    got = [l.split(": ", 1)[1] for l in rec.splitlines() if l.startswith("reason_sha256: ")][0]
    print("sha256(reason input) =", want, "matches record:", want == got,
          "| equals sha256(the reason given):", want == hashlib.sha256(sys.argv[2].encode()).hexdigest())
EOF

echo
echo "== grain CI repo acme/gadgets =="
step "$grain" release rollback -repo acme/gadgets -to v1.0.0 -reason "$reason" v1.1.0
step curl -s -w '\n[http %{http_code}]\n' "${auth[@]}" -X POST \
  -d "{\"repo\":\"acme/gadgets\",\"reason\":\"UI path: $reason\"}" "$GRAIN_SERVER/api/releases/v1.1.0/rollback"
echo "-- agent ($agent_task) through the sandbox proxy stub:"
step env GRAIN_SERVER="http://127.0.0.1:${PROXY_PORT:-18081}" "$grain" release rollback -repo acme/gadgets -reason "agent says so" v1.1.0
step curl -s -w '\n[http %{http_code}]\n' "${auth[@]}" -H "X-Grain-Acting-Task: $agent_task" -X POST \
  -d '{"repo":"acme/gadgets","reason":"agent says so"}' "$GRAIN_SERVER/api/releases/v1.1.0/rollback"
