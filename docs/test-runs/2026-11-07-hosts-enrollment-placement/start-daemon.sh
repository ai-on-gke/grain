#!/bin/sh
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

# Starts (or restarts, keeping its data) the walkthrough's real daemon on
# 127.0.0.1:4280, with GitHub pointed at fake-github.py on 127.0.0.1:4291
# so the -join device flow completes on loopback. Re-running it is how the
# walkthrough restarts the daemon under connected hosts.
#
#   start-daemon.sh [extra daemon flags]   # e.g. -hostd-socket /tmp/host-a/hostd.sock
set -eu
here=$(cd "$(dirname "$0")" && pwd)
root=/tmp/walk
mkdir -p "$root/data" "$root/sandboxes" "$root/bin"
if ! curl -fs -o /dev/null http://127.0.0.1:4291/user 2>/dev/null && ! pgrep -f fake-github.py >/dev/null; then
	nohup python3 "$here/fake-github.py" 4291 >"$root/fake-github.log" 2>&1 &
fi
cat >"$root/bin/agy" <<'EOF'
#!/bin/sh
echo '{"event":"result","result":{"status":"SUCCESS","response":"walkthrough stub agy"}}'
EOF
chmod 755 "$root/bin/agy"
[ -f "$root/gemini-key" ] || echo walkthrough-not-a-key >"$root/gemini-key"
[ -f "$root/data/state-repo.json" ] || echo '{"directConfigWrites": true}' >"$root/data/state-repo.json"
if [ -f "$root/daemon.pid" ]; then
	kill "$(cat "$root/daemon.pid")" 2>/dev/null || true
	sleep 2
fi
cd "$root"
touch "$root/daemon.log"
start=$(wc -c <"$root/daemon.log")
PATH=$root/bin:$PATH nohup /home/debian/bin/grain daemon -data-dir "$root/data" \
	-sandbox-dir "$root/sandboxes" -host-sandboxes -ui-addr 127.0.0.1:4280 -sandbox-port-base=0 \
	-as walkthrough-owner -poll-interval 200ms -max-workers 1 -max-mergers 0 \
	-agy-path "$root/bin/agy" -gemini-api-key-file "$root/gemini-key" \
	-github-host 127.0.0.1:4291 -github-insecure-http "$@" \
	>>"$root/daemon.log" 2>&1 &
echo $! >"$root/daemon.pid"
for _ in $(seq 1 120); do
	if tail -c +"$((start + 1))" "$root/daemon.log" | grep -q 'serving the UI/API on'; then
		echo "daemon up (pid $(cat "$root/daemon.pid"))"
		exit 0
	fi
	kill -0 "$(cat "$root/daemon.pid")" 2>/dev/null || break
	sleep 0.5
done
tail -c +"$((start + 1))" "$root/daemon.log" | tail -5 >&2
echo "daemon did not come up; see $root/daemon.log" >&2
exit 1
