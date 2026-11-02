#!/bin/sh
# The walkthrough daemon: GRAIN=path/to/grain DIR=workdir JWKS=host:port ./run-daemon.sh [extra flags, e.g. -ui-allowed-principals ...]
exec ${GRAIN:-grain} daemon -data-dir ${DIR:-.}/data -sandbox-port-base=0 -host-sandboxes -sandbox-dir ${DIR:-.}/sb \
  -ui-addr 127.0.0.1:4300 -as owner@example.com \
  -ui-iap-audience /projects/123/global/backendServices/456 \
  -ui-iap-issuer https://iap.test.local \
  -ui-iap-keys-url http://${JWKS:-127.0.0.1:18080}/jwks "$@"
