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

# exchange.sh (in the sandbox): one STS token exchange the way a
# google-auth client makes it from the placed external_account config --
# POST the config's token_url with the subject token read from its
# credential_source.file. SUBJECT overrides the subject token. With
# `field NAME` only, prints one string field of the config instead. The
# walk image's python3-minimal has no json module, so the config is read
# with sed: grain writes each field on a line of its own.
cfg=${CFG:-$HOME/.gcp-service-account.json}
field() { sed -n "s/.*\"$1\": \"\([^\"]*\)\".*/\1/p" "$cfg"; }
if [ "${1:-}" = field ]; then
  field "$2"
  exit
fi
curl -sS -m 10 -w " HTTP %{http_code}" -X POST "$(field token_url)" \
  --data-urlencode grant_type=urn:ietf:params:oauth:grant-type:token-exchange \
  --data-urlencode "audience=$(field audience)" \
  --data-urlencode requested_token_type=urn:ietf:params:oauth:token-type:access_token \
  --data-urlencode scope=https://www.googleapis.com/auth/cloud-platform \
  --data-urlencode subject_token_type=urn:ietf:params:oauth:token-type:jwt \
  --data-urlencode "subject_token=${SUBJECT:-$(cat "$(field file)")}"
echo
