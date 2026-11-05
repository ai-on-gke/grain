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

# Written by `grain init workstation`; a re-run rewrites it.
# The PATH is the one init ran under, so the daemon finds the same
# docker, granule and model CLI from under systemd as from a shell.
PATH='/var/tmp/grain-workstation-3070/home-open/bin:/usr/local/bin:/usr/bin:/bin'
export PATH
GRAIN_DATA_DIR='/var/tmp/grain-workstation-3070/home-open/.grain'
export GRAIN_DATA_DIR
exec '/home/debian/bin/grain' daemon \
  '-data-dir' '/var/tmp/grain-workstation-3070/home-open/.grain' \
  '-policy' '/var/tmp/grain-workstation-3070/home-open/.grain/grain_policy.yml' \
  '-ui-addr' '127.0.0.1:14320' \
  '-sandbox-backend' 'host' \
  '-model-backend' 'claude' \
  '-sandbox-dir' '/var/tmp/grain-workstation-3070/home-open/.grain/sandboxes' \
  '-repo' 'acme/app' \
  "$@"
