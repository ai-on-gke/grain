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

# seed_old.sh: §3 item 1's starting point. A 61668e58 daemon's data dir
# holding every credential file the upgrade imports: a GitHub PAT
# (connections import), an App key and an SSH key (secrets/github/*.json,
# the files 61668e58 reads), the secrets-repo token (written by the old
# daemon's POST /api/state-repo/secrets-repo), a state-repo token (in the
# format statemanager.writeRepoToken writes), the package-cache CA (old
# daemon first start) and the sandbox-access CA plus release-approval key
# (seedca.go, the old build's own code). Values are random and never printed.
set -euo pipefail
source "$(dirname "$0")/env.sh"
stop_all
rm -rf "$WORK"
start_sims
rnd() { head -c 24 /dev/urandom | base64 | tr -dc A-Za-z0-9 | sed 's/^/ghp_/'; }
rnd >"$WORK/values/pat"
printf 'git:\n  - credential: bot\n    token_file: %s\n' "$WORK/values/pat" >"$WORK/seed.yaml"
"$OLD_BIN" connections -data-dir "$WORK/data" import "$WORK/seed.yaml"
start_daemon "$OLD_BIN"
stop_daemon
(cd /var/tmp/ss/old && cp "$HERE/seedca.go" seedca_tmp.go && go run seedca_tmp.go "$WORK/data"; rm -f seedca_tmp.go)
openssl genrsa 2048 2>/dev/null >"$WORK/values/app.pem"
ssh-keygen -q -t ed25519 -N '' -f "$WORK/values/ssh_key" -C walk
jq -n --rawfile k "$WORK/values/app.pem" '{app_id:"4242",installation_id:"777",private_key:$k}' >"$WORK/data/secrets/github/ci-app.app.json"
jq -n --rawfile k "$WORK/values/ssh_key" '{host:"127.0.0.1",port:2222,user:"git",private_key:$k}' >"$WORK/data/secrets/github/deploy-ssh.ssh.json"
chmod 600 "$WORK/data/secrets/github/"* "$WORK/values/"*
rnd >"$WORK/values/secrets-repo-token"
rnd >"$WORK/values/state-repo-token"
git init -q --bare -b main "$WORK/git/acme/grain-secrets.git"
git -C "$WORK/git/acme/grain-secrets.git" config http.receivepack true
start_daemon "$OLD_BIN"
api POST /api/state-repo/secrets-repo "$(jq -n --rawfile t "$WORK/values/secrets-repo-token" --arg r "http://$GHSIM/acme/grain-secrets.git" '{remote:$r,token:($t|rtrimstr("\n"))}')" >/dev/null
(cat "$WORK/values/state-repo-token"; echo) >"$WORK/data/secrets/state-repo-token"
chmod 600 "$WORK/data/secrets/state-repo-token"
stop_daemon
for f in sandbox-access-ca.pem package-cache-ca.pem release-approval-key.pem; do echo "$f $(digest "$WORK/data/$f")"; done >"$WORK/pre-digests.txt"
cp -a "$WORK/data" "$WORK/data-131"
tree
