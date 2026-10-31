#!/bin/sh
# start-sd.sh <image>: the d1 daemon as the transient unit grain-daemon.service,
# so deploy://grain.log has a journal to read.
sudo systemctl stop grain-daemon.service 2>/dev/null; sudo systemctl reset-failed grain-daemon.service 2>/dev/null
sudo systemd-run --unit grain-daemon --uid debian --gid debian -p WorkingDirectory=/home/debian/wt -E HOME=/home/debian -E PATH=/home/debian/bin:/usr/local/bin:/usr/bin:/bin \
  /home/debian/bin/grain daemon -data-dir /home/debian/wt/d1 -sandbox-dir /home/debian/wt/sbx-d1 -ui-addr 127.0.0.1:7811 -sandbox-port-base=0 \
  -sandbox-backend container -container-sandbox-image "$1" -github-host 127.0.0.1:4250 -github-insecure-http -container-git-proxy-host 10.241.0.1
