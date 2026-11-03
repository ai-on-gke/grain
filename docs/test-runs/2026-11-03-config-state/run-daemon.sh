#!/bin/sh
# (Re)start the walkthrough's real daemon: the one `grain init workstation
# -non-interactive -repo acme/widgets -sandbox-backend host -model-backend
# claude -ui-addr 127.0.0.1:18702 -start-service=false` wrote under
# HOME=/home/debian/live. Logs go to $LIVE/daemon.log.
LIVE=${LIVE:-/home/debian/live}
for pid in $(pgrep -f '^/home/debian/bin/grain daemon'); do kill "$pid"; done
sleep 3
cd "$LIVE" || exit 1
HOME=$LIVE setsid nohup "$LIVE/.grain/daemon.sh" -sandbox-port-base=0 \
  -github-host 127.0.0.1:1 -github-insecure-http >>"$LIVE/daemon.log" 2>&1 </dev/null &
for _ in $(seq 1 60); do
  curl -s -o /dev/null http://127.0.0.1:18702/ && exit 0
  sleep 1
done
echo "daemon did not answer" >&2
exit 1
