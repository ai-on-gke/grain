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

# start-daemon.sh: starts the walk daemon in the foreground; run it as
# nohup ./start-daemon.sh > daemon.log 2>&1 &.
exec /home/debian/bin/grain daemon -data-dir /home/debian/walk/rd/d -ui-addr 127.0.0.1:7830 -sandbox-port-base=0 \
  -container-sandboxes -container-sandbox-image grain-sbx:walk -container-git-proxy-host 10.240.0.1 \
  -github-host 127.0.0.1:4250 -github-insecure-http -repo acme/widgets
