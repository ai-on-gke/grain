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

# step-gcp-token.sh (host side, run by fake-agy while the run is live):
# items 1 and 2. Looks at what the gcp-key grant placed in the run's
# container, exchanges the per-attempt token at the forwarded listener
# from inside it (with curl and with the Go client library, adc-probe),
# then moves the gcp server's clock (the -fake-cloud fixture's clock_file)
# to 30 s before the grant's deadline and to 5 s after it. It saves the
# per-attempt token and token_url to $W/saved-<attempt> for release-check
# after the run has ended (item 3). Everything it prints cuts token values
# to their first characters.
W=/home/debian/walk
# Once per walk (token-walk.sh arms it): the run's second turn runs it too.
[ -f $W/propose-armed ] || { echo "== step-gcp-token: not armed, nothing probed"; exit 0; }
rm -f $W/propose-armed
c=$(docker ps --latest --format '{{.Names}}')
echo "== sandbox container: $c ($(docker inspect -f '{{.Config.Image}}' "$c"))"
docker cp $W/adc-probe "$c":/tmp/adc-probe >/dev/null
ex() { docker exec "$c" sh -c "$1" 2>&1; }
ex '
cd $HOME
echo "-- ls -l the grant'"'"'s files"
ls -l .gcp-service-account.json .gcp-service-account.about; ls -l .gcp-token/*/
echo "-- ~/.gcp-service-account.json"; cat .gcp-service-account.json
echo "-- ~/.gcp-service-account.about"; cat .gcp-service-account.about
ls .gcp-key-* 2>/dev/null; echo "-- any private_key anywhere under ~: $(grep -rl private_key $HOME 2>/dev/null | wc -l) file(s)"
'
url=$(ex 'grep -o "\"token_url\": *\"[^\"]*\"" $HOME/.gcp-service-account.json' | sed 's/.*: *"//; s/"$//')
subj=$(ex 'grep -o "\"file\": *\"[^\"]*\"" $HOME/.gcp-service-account.json' | sed 's/.*: *"//; s/"$//')
attempt=$(echo "$url" | sed 's|.*/runs/||; s|/token||')
echo "token_url: $url"; echo "subject token file: $subj"
# exchange posts the RFC 8693 form the external_account config implies.
exchange() {
  docker exec "$c" sh -c "curl -sS -m 10 -w ' HTTP %{http_code}' -X POST '$url' \
    --data-urlencode grant_type=urn:ietf:params:oauth:grant-type:token-exchange \
    --data-urlencode requested_token_type=urn:ietf:params:oauth:token-type:access_token \
    --data-urlencode subject_token_type=urn:ietf:params:oauth:token-type:jwt \
    --data-urlencode audience=//grain.dev/capabilities/gcp/permission-sets/ext-deploy \
    --data-urlencode subject_token=$1" 2>&1 | sed -E 's/("access_token":"[^@"]*)@[^"]*"/\1@<cut>"/'
  echo
}
echo "== $(date -u +%T) exchange 1 (curl, inside the container)"; exchange '$(cat '"$subj"')'
echo "== $(date -u +%T) exchange 2"; exchange '$(cat '"$subj"')'
echo "== $(date -u +%T) adc-probe (golang.org/x/oauth2/google, inside the container)"; ex '/tmp/adc-probe'
echo "== $(date -u +%T) ID token (the placed id-token helper)"
idscript=$(ex 'ls $HOME/.gcp-token/*/*-id-token')
ex "$idscript https://walk.example/aud; echo" | sed 's/@[^ ]*/@<cut>/'
echo "== $(date -u +%T) a wrong subject token"; exchange 'not-the-grant'
mkdir -p $W/saved; ex "cat $subj" > $W/saved/$attempt.token; echo "$url" > $W/saved/$attempt.url
echo "saved the per-attempt token ($(head -c 6 $W/saved/$attempt.token)<cut>) for after release"
# The grant's deadline, from the gcp server's own audit line.
deadline=$(grep "audit: attempt $attempt: permission set ext-deploy: granted access tokens" $W/daemon.log | tail -1 | sed 's/.* until //')
echo "== the grant's deadline (gcp server audit): $deadline"
now=$(date -u +%s); dl=$(date -u -d "$deadline" +%s)
echo "$((dl - now - 30))s" > $W/gcp-clock
echo "== $(date -u +%T) server clock moved to $(date -u -d @$((dl - 30)) +%T) (deadline - 30s)"; exchange '$(cat '"$subj"')'
ex '/tmp/adc-probe'
echo "$((dl - now + 5))s" > $W/gcp-clock
echo "== $(date -u +%T) server clock moved to $(date -u -d @$((dl + 5)) +%T) (deadline + 5s)"; exchange '$(cat '"$subj"')'
ex '/tmp/adc-probe'
ex "$idscript https://walk.example/aud; echo"
rm -f $W/gcp-clock
echo "== $(date -u +%T) server clock back to now"; exchange '$(cat '"$subj"')'
