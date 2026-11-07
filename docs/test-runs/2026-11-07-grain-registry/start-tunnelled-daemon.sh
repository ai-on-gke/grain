#!/bin/bash
# Copyright 2026 Google LLC
# SPDX-License-Identifier: Apache-2.0
#
# Start the walkthrough's tunnelled `grain daemon` (entry conn-alice) with the
# registry verifier flags; $1 is the registry URL it first registers with.
# usage: start-d1.sh <registry-url>
mkdir -p /tmp/grwalk/d1/scratch
nohup /home/debian/bin/grain daemon -data-dir /tmp/grwalk/d1 -sandbox-dir /tmp/grwalk/d1/scratch -disable-native-sandboxes -sandbox-port-base=0 -ci=false \
  -deployment-owner=alice@alpha.test -ui-allowed-principals=user:alice@alpha.test,group:alpha-eng@alpha.test \
  -ui-addr=127.0.0.1:4270 -ui-allowed-hosts=conn-alice.grain-user.test,127.0.0.1,localhost \
  -registry-url "$1" -registry-entry conn-alice -registry-token-file /tmp/grwalk/conn-alice.token -registry-connect \
  -ui-require-verifier=registry -ui-registry-issuer=grain-registry -ui-registry-audience=https://conn-alice.grain-user.test \
  -ui-registry-keys-url=http://127.0.0.1:4253/.well-known/jwks.json >> /tmp/grwalk/logs/d1.log 2>&1 &
echo $! > /tmp/grwalk/d1.pid
