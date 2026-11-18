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

# Real-daemon environment for docs/test-plans/2026-11-16.md §1:
# - ghsim on 127.0.0.1:4294
# - grain-slowverifier daemon on 127.0.0.1:4291 (-sandbox-port-base=0, -ui-require-verifier=slow-proxy)
# - HTTPS identity-aware reverse proxy on 127.0.0.1:4292 (and secondary host 127.0.0.1:4295)
# - verifier control server on 127.0.0.1:4293
# - persistent staging sandbox "preview" on sandbox_port/host_port 4281 mounted at /staging/preview

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GHSIM_SRC="$ROOT/docs/test-runs/2026-11-13-grain-health-exit-codes/ghsim.go"
FAKE_AGY="$ROOT/docs/test-runs/2026-11-13-grain-health-exit-codes/fake-agy.py"

DAEMON_PORT=${DAEMON_PORT:-4291}
PROXY_PORT=${PROXY_PORT:-4292}
CONTROL_PORT=${CONTROL_PORT:-4293}
GHSIM_PORT=${GHSIM_PORT:-4294}
PROXY_ALT_PORT=${PROXY_ALT_PORT:-4295}
STAGING_PORT=${STAGING_PORT:-4281}

DAEMON_HTTP="http://127.0.0.1:${DAEMON_PORT}"
PROXY_HTTPS="https://127.0.0.1:${PROXY_PORT}"
PROXY_ALT_HTTPS="https://127.0.0.1:${PROXY_ALT_PORT}"
CONTROL_HTTP="http://127.0.0.1:${CONTROL_PORT}"
GHSIM_ADDR="127.0.0.1:${GHSIM_PORT}"
PG_DSN=${PG_DSN:-postgres://grain:grain@127.0.0.1:5432/grain_walk_id?sslmode=disable}

export PYTHONDONTWRITEBYTECODE=1

stop_daemon_only() {
	local work=${1:-${WORK_DIR:-/tmp/walk-id-sessions}}
	if [[ -f "$work/daemon.pid" ]]; then
		local pid
		pid=$(cat "$work/daemon.pid")
		kill "$pid" 2>/dev/null || true
		for _ in $(seq 80); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.1
		done
		rm -f "$work/daemon.pid"
	fi
}

stop_all() {
	local work=${1:-${WORK_DIR:-/tmp/walk-id-sessions}}
	stop_daemon_only "$work"
	if [[ -f "$work/ghsim.pid" ]]; then
		local pid
		pid=$(cat "$work/ghsim.pid")
		kill "$pid" 2>/dev/null || true
		for _ in $(seq 50); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.1
		done
		rm -f "$work/ghsim.pid"
	fi
}

init_walk_env() {
	WORK_DIR=${1:-/tmp/walk-id-sessions}
	stop_all "$WORK_DIR"
	rm -rf "$WORK_DIR"
	mkdir -p "$WORK_DIR/data" "$WORK_DIR/sandboxes" "$WORK_DIR/bin" "$WORK_DIR/home" "$WORK_DIR/captures"

	if [[ ${STORE:-sqlite} == postgres ]]; then
		sudo -u postgres psql -q -c "DROP DATABASE IF EXISTS grain_walk_id WITH (FORCE);"
		sudo -u postgres psql -q -c "CREATE DATABASE grain_walk_id OWNER grain;"
	fi

	(cd "$ROOT" && go build -o "$WORK_DIR/bin/ghsim" "$GHSIM_SRC")
	(cd "$ROOT" && go build -o "$WORK_DIR/bin/grain-slowverifier" "$HERE/grain_slowverifier.go")

	git init -q --bare -b main "$WORK_DIR/widgets.git"
	git -C "$WORK_DIR/widgets.git" config http.receivepack true
	local tmp
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b main
	cat >"$tmp/README.md" <<'EOF'
# acme/widgets
EOF
	cat >"$tmp/staging_echo.py" <<EOF
#!/usr/bin/env python3
import http.server
import json

PORT = ${STAGING_PORT}

class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass
    def do_GET(self):
        if self.path.startswith("/plant"):
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Set-Cookie", "__Host-grain_verified=evil; Path=/; Secure; HttpOnly")
            self.send_header("Set-Cookie", "__host-grain_verified=evil2; Path=/; Secure")
            self.send_header("Set-Cookie", "grain_access=evil3; Path=/")
            self.send_header("Set-Cookie", "staging_app=ok; Path=/; HttpOnly")
            self.send_header("Clear-Site-Data", '"*"')
            self.end_headers()
            self.wfile.write(b'{"planted":true}\n')
            return
        headers = {k.lower(): v for k, v in self.headers.items()}
        body = json.dumps({
            "path": self.path,
            "cookie": self.headers.get("Cookie", ""),
            "headers": headers,
        }).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

http.server.ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
EOF
	chmod +x "$tmp/staging_echo.py"
	git -C "$tmp" add .
	git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed widgets and staging echo"
	git -C "$tmp" push -q "$WORK_DIR/widgets.git" main
	rm -rf "$tmp"

	echo "ghp_walkthrough" >"$WORK_DIR/bot.token"
	echo "walkthrough-gemini-key" >"$WORK_DIR/gemini.key"
	echo '{"directConfigWrites": true}' >"$WORK_DIR/data/state-repo.json"
	cat >"$WORK_DIR/seed-git.yaml" <<EOF
git:
  - credential: bot
    token_file: $WORK_DIR/bot.token
EOF
	cat >"$WORK_DIR/seed-models.yaml" <<EOF
git:
  - credential: bot
    token_file: $WORK_DIR/bot.token
models:
  - id: gemini
    framework: antigravity
    models: [gemini-3.1-pro]
    credential_file: $WORK_DIR/gemini.key
default_model: gemini-3.1-pro
EOF
	if [[ ${STORE:-sqlite} == sqlite ]]; then
		"$WORK_DIR/bin/grain-slowverifier" connections -data-dir "$WORK_DIR/data" import "$WORK_DIR/seed-git.yaml" >"$WORK_DIR/connections.log" 2>&1
	else
		"$WORK_DIR/bin/grain-slowverifier" connections -data-dir "$WORK_DIR/data" -db-driver postgres -db-dsn "$PG_DSN" import "$WORK_DIR/seed-git.yaml" >"$WORK_DIR/connections.log" 2>&1
	fi
}

start_walk_ghsim() {
	local work=${1:-$WORK_DIR}
	"$work/bin/ghsim" -addr "$GHSIM_ADDR" -bare "$work/widgets.git" -default main >"$work/ghsim.log" 2>&1 &
	echo $! >"$work/ghsim.pid"
	for _ in $(seq 50); do
		curl -sf "http://$GHSIM_ADDR/repos/acme/widgets" >/dev/null && return 0
		sleep 0.1
	done
	echo "ghsim failed to start; see $work/ghsim.log" >&2
	return 1
}

start_walk_daemon() {
	local work=${1:-$WORK_DIR}
	shift || true
	stop_daemon_only "$work"
	touch "$work/daemon.log"
	local store_flags=()
	if [[ ${STORE:-sqlite} == postgres ]]; then
		store_flags=(-postgres-url "$PG_DSN")
	fi
	env -i \
		PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
		HOME="$work/home" \
		PYTHONDONTWRITEBYTECODE=1 \
		GCE_METADATA_HOST="127.0.0.1:9" \
		GRAIN_SLOW_DELAY_MS="${SLOW_DELAY_MS:-1500}" \
		GRAIN_SLOW_TTL_MS="${SLOW_TTL_MS:-60000}" \
		GRAIN_SLOW_MODE="${SLOW_MODE:-ok}" \
		GRAIN_SLOW_CONTROL_ADDR="127.0.0.1:${CONTROL_PORT}" \
		GRAIN_SLOW_UPSTREAM="${DAEMON_HTTP}" \
		GRAIN_SLOW_PROXY_ADDRS="127.0.0.1:${PROXY_PORT},127.0.0.1:${PROXY_ALT_PORT}" \
		GRAIN_SLOW_TLS_CERT="$work/proxy-cert.pem" \
		GRAIN_SLOW_TLS_KEY="$work/proxy-key.pem" \
		setsid "$work/bin/grain-slowverifier" daemon \
		-data-dir "$work/data" \
		-sandbox-dir "$work/sandboxes" \
		-host-sandboxes \
		-ui-addr "127.0.0.1:${DAEMON_PORT}" \
		-sandbox-port-base=0 \
		-ui-require-verifier=slow-proxy \
		-deployment-owner=alice@example.com \
		-ui-allowed-principals="user:alice@example.com,user:bob@example.com" \
		-as alice@example.com \
		-repo acme/widgets \
		-default-target-repo acme/widgets \
		-poll-interval 1s \
		-max-workers 1 \
		-max-mergers 0 \
		-agy-path "$FAKE_AGY" \
		-github-host "$GHSIM_ADDR" \
		-github-insecure-http \
		"${store_flags[@]}" \
		"$@" >>"$work/daemon.log" 2>&1 </dev/null &
	echo $! >"$work/daemon.pid"

	for _ in $(seq 120); do
		if [[ -s "$work/data/local-api-token" ]]; then
			CODE=$(tr -d '[:space:]' <"$work/data/local-api-token")
			if curl -sf -H "Authorization: Bearer $CODE" "$DAEMON_HTTP/api/config" >/dev/null 2>&1; then
				if [[ -f "$work/seed-models.yaml" && ! -f "$work/.models-seeded" ]]; then
					if [[ ${STORE:-sqlite} == sqlite ]]; then
						"$work/bin/grain-slowverifier" connections -data-dir "$work/data" import "$work/seed-models.yaml" >>"$work/connections.log" 2>&1
					else
						"$work/bin/grain-slowverifier" connections -data-dir "$work/data" -db-driver postgres -db-dsn "$PG_DSN" import "$work/seed-models.yaml" >>"$work/connections.log" 2>&1
					fi
					touch "$work/.models-seeded"
				fi
				export WORK_DIR="$work" CODE DAEMON_HTTP PROXY_HTTPS PROXY_ALT_HTTPS CONTROL_HTTP GHSIM_ADDR PG_DSN
				return 0
			fi
		fi
		if ! kill -0 "$(cat "$work/daemon.pid")" 2>/dev/null; then
			echo "daemon exited early; tail of $work/daemon.log:" >&2
			tail -n 50 "$work/daemon.log" >&2
			return 1
		fi
		sleep 0.2
	done
	echo "daemon failed to become ready; tail of $work/daemon.log:" >&2
	tail -n 50 "$work/daemon.log" >&2
	return 1
}

seed_walk_data() {
	local work=${1:-$WORK_DIR}
	# File two proposed/queued tasks so /next/inbox, /next/tasks and /next/task/<id> render populated views.
	curl -sf -X POST "$DAEMON_HTTP/api/tasks" \
		-H "Authorization: Bearer $CODE" \
		-H "Content-Type: application/json" \
		-d '{"title":"Verify session fast-path on /next/","description":"Walkthrough task #1 with **Markdown** details for `/next/task/1`.\n\n- Check session cookie issuance\n- Check ETag revalidation","repo":"acme/widgets","approve":false}' \
		>"$work/task1.json"

	curl -sf -X POST "$DAEMON_HTTP/api/tasks" \
		-H "Authorization: Bearer $CODE" \
		-H "Content-Type: application/json" \
		-d '{"title":"Audit bundle exemption headers","description":"Walkthrough task #2 proposed on acme/widgets.","repo":"acme/widgets","approve":false}' \
		>"$work/task2.json"

	# Seed persistent staging sandbox "preview" at /staging/preview -> port 4281.
	local sql="INSERT INTO persistent_sandbox (name,repo,ref,auto_update,command,dir,env,capabilities,cpus,memory_mb,disk_gb,network_access,sandbox_port,host_port,ui_subpath,strip_prefix,enabled,refresh,rollback_on_crash) VALUES ('preview','acme/widgets','main',FALSE,'python3 staging_echo.py','','','',0,0,0,'',${STAGING_PORT},0,'/staging/preview',TRUE,TRUE,'manual',FALSE);"
	if [[ ${STORE:-sqlite} == sqlite ]]; then
		sqlite3 "$work/data/store/grain.db" "$sql"
	else
		psql "$PG_DSN" -q -c "$sql"
	fi
}
