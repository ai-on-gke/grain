#!/bin/bash
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

# fence-modes.sh <grain>: starts a container-sandbox daemon three ways and
# prints what it says about the nftables fence:
#   auto, nft reachable through `sudo -n nft` (this sandbox's normal state);
#   auto, nft unreachable (PATH holds a sudo that refuses and no nft);
#   require, nft unreachable.
set -u
G=${1:-/home/debian/bin/grain-head}
root=$(mktemp -d /home/debian/fence.XXXX)
mkdir -p "$root/nonft"
printf '#!/bin/sh\necho "sudo: a password is required" >&2\nexit 1\n' >"$root/nonft/sudo"
chmod +x "$root/nonft/sudo"
run() { # run <name> <PATH> <fence mode>
  local d="$root/$1"
  mkdir -p "$d"
  PATH=$2 setsid "$G" daemon -data-dir "$d/data" -ui-addr 127.0.0.1:4240 -sandbox-port-base=0 \
    -container-sandboxes -container-sandbox-image debian:bookworm-slim -container-git-proxy-host 172.17.0.1 \
    -container-sandbox-fence "$3" >"$d/log" 2>&1 </dev/null &
  local pid=$!
  sleep 8
  kill "$pid" 2>/dev/null; sleep 2
  echo "== $1 (-container-sandbox-fence $3)"
  grep -iE 'fence|unfenced|nft' "$d/log" | cut -c21- | head -5
}
run auto-nft "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin" auto
run auto-nonft "$root/nonft:/usr/local/bin:/usr/bin:/bin" auto
run require-nonft "$root/nonft:/usr/local/bin:/usr/bin:/bin" require
sudo nft list tables 2>/dev/null | grep -i grain || echo "(no grain tables left behind)"
rm -rf "$root"
