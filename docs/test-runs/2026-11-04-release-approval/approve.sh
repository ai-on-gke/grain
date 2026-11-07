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

# approve.sh RUN_DIR AGENT_TASK VERSION: §2 item 1 after inbox-approve.mjs
# pressed VERSION's Approve in the Inbox -- a second press from the CLI
# and the API, an agent's Approve (CLI through agent-proxy.py, and the
# API with the acting-task header), a fresh CLI approve of v2.6.0, then
# every promote-release.yml dispatch the sim holds, each record checked
# against the deployment's key with openssl.
set -uo pipefail
run="${1:?run dir}"
agent_task="${2:?agent task id}"
version="${3:?version approved in the Inbox}"
here="$(cd "$(dirname "$0")" && pwd)"
grain="${GRAIN:-/home/debian/bin/grain}"
GRAIN_SERVER="http://127.0.0.1:${PORT:-18080}"
GRAIN_TOKEN="$(cat "$run/code")"
export GRAIN_SERVER GRAIN_TOKEN
bare="$(cat "$run/bare")"
auth=(-H "Authorization: Bearer $GRAIN_TOKEN" -H 'Content-Type: application/json')
step() { printf '\n$ %s\n' "$*"; "$@"; printf '[exit %d]\n' "$?"; }
python3 "$here/agent-proxy.py" "${PROXY_PORT:-18081}" "$GRAIN_SERVER" "$agent_task" &
proxy=$!
trap 'kill $proxy 2>/dev/null' EXIT
sleep 1

echo "== second press on $version =="
step "$grain" release approve -repo acme/widgets "$version"
step curl -s -w '\n[http %{http_code}]\n' "${auth[@]}" -X POST -d '{}' \
  "$GRAIN_SERVER/api/repos/acme/widgets/pending-releases/$version/approve"
echo "== an agent ($agent_task, api:release-manager) =="
step env GRAIN_SERVER="http://127.0.0.1:${PROXY_PORT:-18081}" "$grain" release approve -repo acme/widgets "${AGENT_VERSION:-v2.5.0}"
step curl -s -w '\n[http %{http_code}]\n' "${auth[@]}" -H "X-Grain-Acting-Task: $agent_task" -X POST -d '{}' \
  "$GRAIN_SERVER/api/repos/acme/widgets/pending-releases/${AGENT_VERSION:-v2.5.0}/approve"
echo "== a person's CLI approve of v2.6.0 (-allow-breaking passes through) =="
git --git-dir "$bare" -c user.name=train -c user.email=train@example.invalid tag -f -a v2.6.0-pending -m "v2.6.0 staged" main >/dev/null
step "$grain" release approve -repo acme/widgets -allow-breaking v2.6.0

echo
echo "== what the sim was sent =="
curl -s "$GRAIN_SERVER/api/e2e/github-sim/repos/acme/widgets/actions/workflows/promote-release.yml/dispatches" \
  >"$run/dispatches.json"
"$grain" release approval-key >"$run/approval-key.pem"
python3 - "$run/dispatches.json" "$run/approval-key.pem" <<'EOF'
import base64, json, subprocess, sys, tempfile, os
ds = json.load(open(sys.argv[1]))
print(f"{len(ds)} dispatch(es) of promote-release.yml")
for i, d in enumerate(ds):
    inp = d["inputs"]
    rec = base64.b64decode(inp["approval"])
    with tempfile.TemporaryDirectory() as t:
        open(os.path.join(t, "r"), "wb").write(rec)
        open(os.path.join(t, "s"), "wb").write(base64.b64decode(inp["approval_signature"]))
        ok = subprocess.run(["openssl", "dgst", "-sha256", "-verify", sys.argv[2], "-signature",
                             os.path.join(t, "s"), os.path.join(t, "r")], capture_output=True, text=True)
    shown = {k: (v if k not in ("approval", "approval_signature") else v[:12] + "...") for k, v in inp.items()}
    print(f"[{i}] ref={d['ref']} inputs={json.dumps(shown, sort_keys=True)}")
    print("    signature:", ok.stdout.strip() or ok.stderr.strip())
    print("    " + rec.decode().replace("\n", "\n    ").rstrip())
EOF
