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

# Self-contained end-to-end smoke test of the sandbox deployment topology with
# zero GCP credentials required:
#   1. Starts PostgreSQL (`postgres:16-alpine` in Docker)
#   2. Builds `grain-registry` and `grain`, validates the rendered `registry.yaml`
#   3. Starts `tests/iap/iapsim.go` in front of `grain-registry`
#   4. Mints a `connected` entry + `grc_...` token with the same SQL as `register-entry.sh`
#   5. Starts a real `grain daemon -registry-connect` with `-ui-registry-*` verifier
#      and `-ui-allowed-principals=user:alice@example.com`, registers it via
#      `grain registry register`, and verifies:
#        - Live heartbeats, version, health, and queue counts in `GET /api/directory`
#        - Repository route resolution in `GET /api/route`
#        - Routed HTTP reachability through `<entry>.<instance_domain>` (200 OK for
#          `alice@example.com`, 403 Forbidden for `bob@example.com`)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

tmp_dir="$(mktemp -d)"
pg_container="grain-reg-smoke-pg-$$"
pids=()

cleanup() {
  for pid in "${pids[@]:-}"; do
    kill "${pid}" >/dev/null 2>&1 || true
  done
  docker rm -f "${pg_container}" >/dev/null 2>&1 || true
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

pick_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

pg_port="$(pick_port)"
reg_main_port="$(pick_port)"
reg_pub_port="$(pick_port)"
reg_peer_port="$(pick_port)"
iap_issuer_port="$(pick_port)"
iap_proxy_port="$(pick_port)"
daemon_port="$(pick_port)"

# Pinned by digest (the same image files/startup.sh runs on the sandbox VM) so
# a retagged upstream image cannot change what this test executes.
POSTGRES_IMAGE="postgres:16-alpine@sha256:aa6cb33cf4c19c765cefcf48d96176258800870a9a1e930ad126200f9d825139"
pgpass="smokepass_$(openssl rand -hex 8)"
registry_host="reg.127-0-0-1.sslip.io"
instance_domain="i-reg.127-0-0-1.sslip.io"
entry_name="alice-dev"
entry_host="${entry_name}.${instance_domain}"
iap_aud="/projects/123456789/global/backendServices/987654321"

echo "==> [1/6] Starting PostgreSQL container (${pg_container}) on 127.0.0.1:${pg_port}..."
docker run -d \
  --name "${pg_container}" \
  -p "127.0.0.1:${pg_port}:5432" \
  -e POSTGRES_USER=grain \
  -e POSTGRES_PASSWORD="${pgpass}" \
  -e POSTGRES_DB=grain_registry \
  "${POSTGRES_IMAGE}" >/dev/null

for _ in $(seq 1 30); do
  if docker exec "${pg_container}" pg_isready -U grain -d grain_registry >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec "${pg_container}" pg_isready -U grain -d grain_registry >/dev/null

echo "==> [2/6] Building grain-registry and grain binaries..."
go -C "${REPO_ROOT}" build -o "${tmp_dir}/grain-registry" ./cmd/grain-registry
go -C "${REPO_ROOT}" build -o "${tmp_dir}/grain" ./cmd/grain

echo "==> [3/6] Starting IAP simulator + grain-registry and validating config..."
go -C "${REPO_ROOT}" run ./tests/iap/iapsim.go \
  -issuer "127.0.0.1:${iap_issuer_port}" \
  -proxy "127.0.0.1:${iap_proxy_port}" \
  -upstream "http://127.0.0.1:${reg_main_port}" \
  -aud "${iap_aud}" \
  -iss "https://cloud.google.com/iap" \
  >"${tmp_dir}/iapsim.log" 2>&1 &
pids+=("$!")

for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:${iap_issuer_port}/jwks" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

cat > "${tmp_dir}/registry.yaml" <<EOF
listen: "127.0.0.1:${reg_main_port}"
jwks_listen: "127.0.0.1:${reg_pub_port}"
host: "${registry_host}"
instance_domain: "${instance_domain}"
auth:
  provider: "iap"
  iap_audience: "${iap_aud}"
  iap_certs_url: "http://127.0.0.1:${iap_issuer_port}/jwks"
  allowed_domains:
    - "example.com"
  admins:
    - "user:alice@example.com"
  creators:
    - "user:alice@example.com"
  service_principals: []
groups:
  provider: "static"
signing:
  provider: "local"
tunnel:
  transport: "ws-mux"
  peer_listen: "127.0.0.1:${reg_peer_port}"
  self_addr: "127.0.0.1:${reg_peer_port}"
database:
  url: "postgres://grain:${pgpass}@127.0.0.1:${pg_port}/grain_registry?sslmode=disable"
compute:
  provider: "none"
config_repos:
  provider: "none"
EOF

"${tmp_dir}/grain-registry" validate -config "${tmp_dir}/registry.yaml"
"${tmp_dir}/grain-registry" serve -config "${tmp_dir}/registry.yaml" >"${tmp_dir}/registry.log" 2>&1 &
pids+=("$!")

reg_ready=0
for _ in $(seq 1 50); do
  if curl -fsS -H "Host: ${registry_host}" "http://127.0.0.1:${reg_main_port}/healthz" >/dev/null 2>&1 &&
     curl -fsS -H "Host: ${registry_host}" "http://127.0.0.1:${reg_pub_port}/.well-known/jwks.json" >/dev/null 2>&1; then
    reg_ready=1
    break
  fi
  sleep 0.2
done
if [[ "${reg_ready}" -ne 1 ]]; then
  echo "local-smoke-test.sh: grain-registry failed to start; logs:" >&2
  cat "${tmp_dir}/registry.log" >&2 || true
  exit 1
fi

# Create a tiny local proxy for JWKS with Host header rewritten to registry_host
# so grain daemon can fetch http://127.0.0.1:<port>/.well-known/jwks.json on loopback.
jwks_proxy_port="$(pick_port)"
python3 - "${jwks_proxy_port}" "${reg_pub_port}" "${registry_host}" >"${tmp_dir}/jwks-proxy.log" 2>&1 <<'PYEOF' &
import http.server, sys, urllib.request

listen_port = int(sys.argv[1])
pub_port = int(sys.argv[2])
reg_host = sys.argv[3]

class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        req = urllib.request.Request(
            f"http://127.0.0.1:{pub_port}{self.path}",
            headers={"Host": reg_host},
        )
        with urllib.request.urlopen(req, timeout=5) as resp:
            body = resp.read()
            self.send_response(resp.status)
            self.send_header("Content-Type", resp.headers.get("Content-Type", "application/json"))
            self.end_headers()
            self.wfile.write(body)
    def log_message(self, *args):
        pass

http.server.HTTPServer(("127.0.0.1", listen_port), H).serve_forever()
PYEOF
pids+=("$!")

echo "==> [4/6] Minting connected entry '${entry_name}' in PostgreSQL..."
token="grc_$(openssl rand -hex 32)"
token_sha256_hex="$(printf '%s' "${token}" | sha256sum | awk '{print $1}')"
entry_uuid="$(cat /proc/sys/kernel/random/uuid)"
cred_uuid="$(cat /proc/sys/kernel/random/uuid)"

docker exec -i "${pg_container}" psql -v ON_ERROR_STOP=1 -U grain -d grain_registry \
  -v entry_uuid="${entry_uuid}" \
  -v cred_uuid="${cred_uuid}" \
  -v entry_name="${entry_name}" \
  -v owner_email="alice@example.com" \
  -v token_sha256_hex="${token_sha256_hex}" >/dev/null <<'SQL'
BEGIN;
INSERT INTO registry.owners (email) VALUES (:'owner_email') ON CONFLICT (email) DO NOTHING;
INSERT INTO registry.entries (
  id, name, kind, owner_email, listed, visibility, access_list,
  display_name, purpose, tags, lifecycle_state, version, host_kind,
  created_at, updated_at
) VALUES (
  (:'entry_uuid')::uuid, :'entry_name', 'connected', :'owner_email',
  TRUE, 'owner', '[]'::jsonb,
  'Alice Workstation', 'Smoke test daemon', '[]'::jsonb, 'running', 1, 'workstation',
  now(), now()
);
INSERT INTO registry.entry_credentials (
  id, entry_id, token_sha256, label, created_by, created_at
) VALUES (
  (:'cred_uuid')::uuid, (:'entry_uuid')::uuid, decode(:'token_sha256_hex', 'hex'),
  'smoke-test', :'owner_email', now()
);
SELECT pg_notify('registry_entries', :'entry_name');
SELECT pg_notify('registry_catalog', :'entry_name');
COMMIT;
SQL

# Also declare a scope via PATCH/PUT through iapsim so /api/route resolves example/repo.
curl -fsS -X PUT \
  -H "Host: ${registry_host}" \
  -H "Origin: https://${registry_host}" \
  -H "X-Iapsim-Email: alice@example.com" \
  -H "Content-Type: application/json" \
  -d '{"scopes":[{"repo":"example/repo","role":"primary"}]}' \
  "http://127.0.0.1:${iap_proxy_port}/api/entries/${entry_name}/scopes" >/dev/null

# Host-header forwarder on 127.0.0.1:${pub_loop_port} -> PublicHandler (:reg_pub_port)
# so `grain registry register -registry-url http://127.0.0.1:<port>` works over
# loopback in the offline smoke test while exercising `PublicHandler` (`:8081`)
# for both heartbeats and WebSocket mux tunnel upgrades.
pub_loop_port="$(pick_port)"
cat > "${tmp_dir}/pub_fwd.go" <<'GOEOF'
package main

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
)

func main() {
	target, _ := url.Parse("http://127.0.0.1:" + os.Args[2])
	rp := httputil.NewSingleHostReverseProxy(target)
	orig := rp.Director
	rp.Director = func(r *http.Request) {
		orig(r)
		r.Host = os.Args[3]
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+os.Args[1])
	if err != nil {
		panic(err)
	}
	_ = http.Serve(ln, rp)
}
GOEOF
go -C "${REPO_ROOT}" build -o "${tmp_dir}/pub-fwd" "${tmp_dir}/pub_fwd.go"
"${tmp_dir}/pub-fwd" "${pub_loop_port}" "${reg_pub_port}" "${registry_host}" >"${tmp_dir}/pub-fwd.log" 2>&1 &
pids+=("$!")

for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:${pub_loop_port}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

echo "==> [5/6] Starting grain daemon (-registry-connect) and registering '${entry_name}'..."
mkdir -p "${tmp_dir}/daemon-data" "${tmp_dir}/daemon-sandbox"
"${tmp_dir}/grain" daemon \
  -data-dir "${tmp_dir}/daemon-data" \
  -sandbox-dir "${tmp_dir}/daemon-sandbox" \
  -disable-native-sandboxes \
  -ui-addr "127.0.0.1:${daemon_port}" \
  -registry-connect \
  -ui-registry-issuer "grain-registry" \
  -ui-registry-audience "https://${entry_host}" \
  -ui-registry-keys-url "http://127.0.0.1:${jwks_proxy_port}/.well-known/jwks.json" \
  -ui-allowed-principals "user:alice@example.com" \
  >"${tmp_dir}/daemon.log" 2>&1 &
pids+=("$!")

for _ in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:${daemon_port}/api/auth/access-code" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

GRAIN_DATA_DIR="${tmp_dir}/daemon-data" GRAIN_REGISTRY_TOKEN="${token}" "${tmp_dir}/grain" -server "http://127.0.0.1:${daemon_port}" registry register \
  -registry-url "http://127.0.0.1:${pub_loop_port}" \
  "${entry_name}"

echo "==> [6/6] Verifying /api/directory, /api/route, and tunnel routing + step-3 admission..."
online=0
for _ in $(seq 1 40); do
  dir_json="$(curl -fsS \
    -H "Host: ${registry_host}" \
    -H "X-Iapsim-Email: alice@example.com" \
    "http://127.0.0.1:${iap_proxy_port}/api/directory")"
  status="$(jq -r --arg n "${entry_name}" '.entries[] | select(.name == $n) | .status // empty' <<<"${dir_json}")"
  if [[ "${status}" == "online" ]]; then
    online=1
    break
  fi
  sleep 0.5
done

if [[ "${online}" -ne 1 ]]; then
  echo "local-smoke-test.sh: entry '${entry_name}' did not become online in /api/directory" >&2
  cat "${tmp_dir}/registry.log" >&2 || true
  cat "${tmp_dir}/daemon.log" >&2 || true
  exit 1
fi

jq --arg n "${entry_name}" '.entries[] | select(.name == $n)' <<<"${dir_json}"

route_json="$(curl -fsS \
  -H "Host: ${registry_host}" \
  -H "X-Iapsim-Email: alice@example.com" \
  "http://127.0.0.1:${iap_proxy_port}/api/route?repo=example/repo")"
route_primary="$(jq -r '.primary.name // empty' <<<"${route_json}")"
if [[ "${route_primary}" != "${entry_name}" ]]; then
  echo "local-smoke-test.sh: expected /api/route primary=${entry_name}, got '${route_primary}'" >&2
  jq . <<<"${route_json}" >&2
  exit 1
fi
echo "    PASS: /api/route?repo=example/repo -> primary=${route_primary}"

# Wait briefly for outbound mux tunnel session to establish, then route HTTP
# traffic to `<entry>.<instance_domain>` through iapsim -> grain-registry -> mux -> daemon.
routed_ok=0
for _ in $(seq 1 30); do
  code="$(curl -sS -o "${tmp_dir}/routed-tasks.json" -w '%{http_code}' \
    -H "Host: ${entry_host}" \
    -H "X-Iapsim-Email: alice@example.com" \
    "http://127.0.0.1:${iap_proxy_port}/api/tasks" || true)"
  if [[ "${code}" == "200" ]]; then
    routed_ok=1
    break
  fi
  sleep 0.5
done

if [[ "${routed_ok}" -ne 1 ]]; then
  echo "local-smoke-test.sh: routed GET /api/tasks for alice@example.com failed (last HTTP ${code})" >&2
  cat "${tmp_dir}/routed-tasks.json" >&2 || true
  cat "${tmp_dir}/registry.log" >&2 || true
  cat "${tmp_dir}/daemon.log" >&2 || true
  exit 1
fi

routed_user="$(curl -fsS \
  -H "Host: ${entry_host}" \
  -H "X-Iapsim-Email: alice@example.com" \
  "http://127.0.0.1:${iap_proxy_port}/api/auth/user")"
if [[ "$(jq -r '.authMethod' <<<"${routed_user}")" != "registry" || "$(jq -r '.user' <<<"${routed_user}")" != "alice@example.com" ]]; then
  echo "local-smoke-test.sh: unexpected routed /api/auth/user payload: ${routed_user}" >&2
  exit 1
fi
echo "    PASS: Routed GET /api/tasks and /api/auth/user succeeded for alice@example.com (authMethod=registry)"

# Non-allowlisted user must be rejected (403) at the registry / daemon admission gate.
forbidden_code="$(curl -sS -o /dev/null -w '%{http_code}' \
  -H "Host: ${entry_host}" \
  -H "X-Iapsim-Email: intruder@example.com" \
  "http://127.0.0.1:${iap_proxy_port}/api/tasks")"
if [[ "${forbidden_code}" != "403" ]]; then
  echo "local-smoke-test.sh: expected HTTP 403 for non-admitted intruder@example.com, got ${forbidden_code}" >&2
  exit 1
fi
echo "    PASS: Non-admitted user rejected with HTTP 403"

# Verify register-entry.sh's exact --revoke-previous SQL transaction revokes the
# prior credential, evicts the active tunnel session, and allows re-registration
# with the newly minted token.
register_sql="$(awk '/^docker exec -i grain-registry-postgres psql/,/^SQL$/ { if ($0 ~ /<<'\''SQL'\''/) { in_sql=1; next } if ($0 == "SQL") { exit } if (in_sql) print }' "${SCRIPT_DIR}/register-entry.sh")"
if [[ -z "${register_sql}" ]]; then
  echo "local-smoke-test.sh: failed to extract SQL block from register-entry.sh" >&2
  exit 1
fi
rot_token="grc_$(openssl rand -hex 32)"
rot_sha256_hex="$(printf '%s' "${rot_token}" | sha256sum | awk '{print $1}')"
rot_entry_uuid="$(cat /proc/sys/kernel/random/uuid)"
rot_cred_uuid="$(cat /proc/sys/kernel/random/uuid)"

docker exec -i "${pg_container}" psql -v ON_ERROR_STOP=1 -U grain -d grain_registry \
  -v entry_uuid="${rot_entry_uuid}" \
  -v cred_uuid="${rot_cred_uuid}" \
  -v entry_name="${entry_name}" \
  -v owner_email="alice@example.com" \
  -v display_name="Alice Workstation" \
  -v purpose="Smoke test daemon" \
  -v host_kind="workstation" \
  -v visibility="owner" \
  -v access_list="[]" \
  -v revoke_previous="1" \
  -v token_sha256_hex="${rot_sha256_hex}" >/dev/null <<<"${register_sql}"

old_revoked="$(docker exec "${pg_container}" psql -tAq -U grain -d grain_registry \
  -c "SELECT count(*) FROM registry.entry_credentials WHERE id = '${cred_uuid}' AND revoked_at IS NOT NULL")"
if [[ "${old_revoked}" != "1" ]]; then
  echo "local-smoke-test.sh: expected previous credential ${cred_uuid} to be revoked, got count=${old_revoked}" >&2
  exit 1
fi

old_hb_code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
  -H "X-Grain-Auth: token" \
  -H @<(printf 'Authorization: Bearer %s\n' "${token}") \
  -H "Content-Type: application/json" \
  -d '{"health":"ok"}' \
  "http://127.0.0.1:${pub_loop_port}/api/entries/${entry_name}/heartbeat")"
if [[ "${old_hb_code}" != "401" ]]; then
  echo "local-smoke-test.sh: expected revoked grc_ token heartbeat to fail with 401, got ${old_hb_code}" >&2
  exit 1
fi

GRAIN_DATA_DIR="${tmp_dir}/daemon-data" GRAIN_REGISTRY_TOKEN="${rot_token}" "${tmp_dir}/grain" -server "http://127.0.0.1:${daemon_port}" registry register \
  -registry-url "http://127.0.0.1:${pub_loop_port}" \
  "${entry_name}" >/dev/null

reconnected_ok=0
for _ in $(seq 1 30); do
  code="$(curl -sS -o /dev/null -w '%{http_code}' \
    -H "Host: ${entry_host}" \
    -H "X-Iapsim-Email: alice@example.com" \
    "http://127.0.0.1:${iap_proxy_port}/api/tasks" || true)"
  if [[ "${code}" == "200" ]]; then
    reconnected_ok=1
    break
  fi
  sleep 0.5
done
if [[ "${reconnected_ok}" -ne 1 ]]; then
  echo "local-smoke-test.sh: rotated token failed to re-establish tunnel routing" >&2
  exit 1
fi
echo "    PASS: --revoke-previous SQL revoked old token (401), evicted old session, and reconnected cleanly"

echo "Local registry sandbox smoke test PASSED."
