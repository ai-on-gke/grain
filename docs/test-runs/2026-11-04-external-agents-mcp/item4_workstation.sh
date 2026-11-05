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

# Item 4 walkthrough: `grain init workstation` on a clean scratch HOME, starter
# grain_policy.yml inspection, live Director chat run with stub `claude` CLI,
# and `-network none` Director warning verification.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GRAIN_BIN="${GRAIN_BIN:-/home/debian/bin/grain}"
WS_ROOT="/var/tmp/grain-workstation-3070"
WS_UI="127.0.0.1:14320"

cleanup() {
  if [[ -f "$WS_ROOT/daemon.pid" ]]; then
    local pid
    pid="$(cat "$WS_ROOT/daemon.pid" || true)"
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      for _ in $(seq 1 40); do
        kill -0 "$pid" 2>/dev/null || break
        sleep 0.1
      done
    fi
    rm -f "$WS_ROOT/daemon.pid"
  fi
}
trap cleanup EXIT

cleanup
rm -rf "$WS_ROOT"
mkdir -p "$WS_ROOT/home-open/bin" "$WS_ROOT/home-open/src" "$WS_ROOT/home-none"

# 1. Stub `claude` CLI emitting valid Claude stream-json NDJSON.
cat >"$WS_ROOT/home-open/bin/claude" <<'EOF'
#!/usr/bin/env bash
cat >/dev/null 2>&1 || true
printf '%s\n' '{"type":"system","subtype":"init","session_id":"director-stub-1"}'
printf '%s\n' '{"type":"assistant","session_id":"director-stub-1","message":{"content":[{"type":"text","text":"Checking workstation policy and state repo..."}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"Director stub ready: local workstation policy allowed this run cleanly.","session_id":"director-stub-1"}'
EOF
chmod +x "$WS_ROOT/home-open/bin/claude"

cat >"$WS_ROOT/home-open/.gitconfig" <<'EOF'
[user]
	name = Alice Workstation
	email = alice@example.com
EOF
cp "$WS_ROOT/home-open/.gitconfig" "$WS_ROOT/home-none/.gitconfig"

# Local checkout with an origin remote so `grain init workstation -repo <dir>` resolves acme/app.
git init -b main "$WS_ROOT/home-open/src/app" >/dev/null
(
  cd "$WS_ROOT/home-open/src/app"
  git remote add origin "https://github.com/acme/app.git"
  echo "# acme/app" > README.md
  git add README.md
  git commit -m "chore: init" >/dev/null
)

# 2. Run `grain init workstation` (network=open, sandbox-backend=host) on clean HOME.
INIT_OPEN_OUT="$(
  env -i \
    HOME="$WS_ROOT/home-open" \
    USER="debian" \
    PATH="$WS_ROOT/home-open/bin:/usr/local/bin:/usr/bin:/bin" \
    "$GRAIN_BIN" init workstation \
      -non-interactive \
      -repo "$WS_ROOT/home-open/src/app" \
      -sandbox-backend host \
      -ui-addr "$WS_UI" \
      -install-service=false \
      -start-service=false 2>&1
)"
printf '%s\n' "$INIT_OPEN_OUT" >"$DIR/item4-init-open-stdout.txt"
cp "$WS_ROOT/home-open/.grain/grain_policy.yml" "$DIR/item4-grain_policy-open.yml"
cp "$WS_ROOT/home-open/.grain/daemon.sh" "$DIR/item4-daemon.sh"

echo "=== grain init workstation (open) stdout ==="
printf '%s\n' "$INIT_OPEN_OUT"

# 3. Start the configured workstation daemon with the stub claude token and open Director chat.
printf 'stub-claude-oauth-token\n' >"$WS_ROOT/stub-claude-token"
chmod 600 "$WS_ROOT/stub-claude-token"

env -i \
  HOME="$WS_ROOT/home-open" \
  USER="debian" \
  PATH="$WS_ROOT/home-open/bin:/usr/local/bin:/usr/bin:/bin" \
  nohup "$WS_ROOT/home-open/.grain/daemon.sh" \
    -sandbox-port-base=0 \
    -ci=false \
    -poll-interval 1s \
    -github-host "127.0.0.1:1" \
    -github-insecure-http \
    -claude-oauth-token-file "$WS_ROOT/stub-claude-token" \
    >"$WS_ROOT/daemon.log" 2>&1 &
echo $! >"$WS_ROOT/daemon.pid"

for _ in $(seq 1 80); do
  if curl -fsS "http://${WS_UI}/api/auth/access-code" >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done
curl -fsS "http://${WS_UI}/api/auth/access-code" >/dev/null

WS_CODE="$(tr -d '\r\n' < "$WS_ROOT/home-open/.grain/local-api-token")"

# Register the stub claude model connection via Settings -> Models API (as init prompts).
curl -fsS -X PUT "http://${WS_UI}/api/settings" \
  -H "X-Grain-Access-Code: ${WS_CODE}" \
  -H "Content-Type: application/json" \
  -d '{"agentKeyChecksEnabled":false}' >/dev/null

curl -fsS -X PUT "http://${WS_UI}/api/deployment/agent-backends/claude" \
  -H "X-Grain-Access-Code: ${WS_CODE}" \
  -H "Content-Type: application/json" \
  -d '{"framework":"claude","models":["claude-sonnet-4-6"]}' >/dev/null

curl -fsS -X PUT "http://${WS_UI}/api/agent-backends/claude/credential" \
  -H "X-Grain-Access-Code: ${WS_CODE}" \
  -H "Content-Type: application/json" \
  -d '{"value":"sk-ant-oat01-stub-workstation"}' >/dev/null

# Check initial Director task (task 1) state and send a message to wake/run it.
DIRECTOR_BEFORE="$(curl -fsS "http://${WS_UI}/api/tasks/1" -H "X-Grain-Access-Code: ${WS_CODE}")"
printf 'Director task 1 initial state=%s capabilities=%s\n' \
  "$(printf '%s' "$DIRECTOR_BEFORE" | jq -r '.state')" \
  "$(printf '%s' "$DIRECTOR_BEFORE" | jq -c '.capabilities')"

curl -fsS -X POST "http://${WS_UI}/api/tasks/1/comments" \
  -H "X-Grain-Access-Code: ${WS_CODE}" \
  -H "Content-Type: application/json" \
  -d '{"body":"Hello Director, verify local workstation policy allows you to run."}' >/dev/null

# Wait up to 25s for Director task 1 attempt #1 to run and reply via stub claude.
DIRECTOR_AFTER=""
for _ in $(seq 1 50); do
  DIRECTOR_AFTER="$(curl -fsS "http://${WS_UI}/api/tasks/1" -H "X-Grain-Access-Code: ${WS_CODE}")"
  ATTEMPTS_LEN="$(printf '%s' "$DIRECTOR_AFTER" | jq '.attempts | length')"
  HAS_STUB_REPLY="$(printf '%s' "$DIRECTOR_AFTER" | jq '[.comments[]? | select(.body | contains("Director stub ready"))] | length')"
  if [[ "$ATTEMPTS_LEN" -ge 1 && "$HAS_STUB_REPLY" -ge 1 ]]; then
    break
  fi
  sleep 0.5
done

printf 'Director task 1 after message: state=%s attempts=%s lastFailureReason=%q\n' \
  "$(printf '%s' "$DIRECTOR_AFTER" | jq -r '.state')" \
  "$(printf '%s' "$DIRECTOR_AFTER" | jq -c '.attempts')" \
  "$(printf '%s' "$DIRECTOR_AFTER" | jq -r '.lastFailureReason // ""')"

if grep -qi "local policy" <<<"$(printf '%s' "$DIRECTOR_AFTER" | jq -r '.lastFailureReason // ""')"; then
  echo "FAIL: local policy held Director chat!" >&2
  exit 1
fi
if [[ "$(printf '%s' "$DIRECTOR_AFTER" | jq '[.comments[]? | select(.body | contains("Director stub ready"))] | length')" -lt 1 ]]; then
  echo "Director did not finish stub turn yet; daemon log tail:" >&2
  tail -n 50 "$WS_ROOT/daemon.log" >&2
  exit 1
fi

# 4. Run `grain init workstation -network none` (container backend) and verify warning;
#    also check `-sandbox-backend host -network none` refusal.
INIT_NONE_OUT="$(
  env -i \
    HOME="$WS_ROOT/home-none" \
    USER="debian" \
    PATH="$WS_ROOT/home-open/bin:/usr/local/bin:/usr/bin:/bin" \
    "$GRAIN_BIN" init workstation \
      -non-interactive \
      -repo "acme/app" \
      -sandbox-backend container \
      -container-git-proxy-host "127.0.0.1" \
      -network none \
      -install-service=false \
      -start-service=false 2>&1
)"
printf '%s\n' "$INIT_NONE_OUT" >"$DIR/item4-init-none-stdout.txt"
cp "$WS_ROOT/home-none/.grain/grain_policy.yml" "$DIR/item4-grain_policy-none.yml"

echo "=== grain init workstation (-network none) stdout ==="
printf '%s\n' "$INIT_NONE_OUT"

if ! grep -Fq "The Director chat asks for open network, so this policy holds it" <<<"$INIT_NONE_OUT"; then
  echo "FAIL: expected Director -network none warning in init output!" >&2
  exit 1
fi

set +e
HOST_NONE_ERR="$(
  env -i \
    HOME="$WS_ROOT/home-host-none" \
    USER="debian" \
    PATH="$WS_ROOT/home-open/bin:/usr/local/bin:/usr/bin:/bin" \
    "$GRAIN_BIN" init workstation \
      -non-interactive \
      -repo "acme/app" \
      -sandbox-backend host \
      -network none \
      -install-service=false \
      -start-service=false 2>&1
)"
HOST_NONE_RC=$?
set -e
printf 'host + -network none (rc=%d): %s\n' "$HOST_NONE_RC" "$HOST_NONE_ERR"
[[ "$HOST_NONE_RC" -ne 0 ]]

jq -n \
  --arg initOpenStdout "$INIT_OPEN_OUT" \
  --arg initNoneStdout "$INIT_NONE_OUT" \
  --arg hostNoneRefusal "$HOST_NONE_ERR" \
  --argjson directorBefore "$DIRECTOR_BEFORE" \
  --argjson directorAfter "$DIRECTOR_AFTER" \
  '{
    initOpenStdout: $initOpenStdout,
    initNoneStdout: $initNoneStdout,
    hostSandboxNetworkNoneRefusal: $hostNoneRefusal,
    directorBefore: $directorBefore,
    directorAfter: $directorAfter
  }' >"$DIR/item4-workstation.json"

cleanup
rm -rf "$WS_ROOT"
echo "Item 4 completed cleanly!"
