#!/bin/sh
# Starts the walk daemon (log to $1, default ~/walk/daemon.log).
exec /home/debian/bin/grain daemon -data-dir /home/debian/walk/rd/d -ui-addr 127.0.0.1:7830 -sandbox-port-base=0 \
  -container-sandboxes -container-sandbox-image grain-sbx:walk -container-git-proxy-host 10.240.0.1 \
  -github-host 127.0.0.1:4250 -github-insecure-http -repo acme/widgets
