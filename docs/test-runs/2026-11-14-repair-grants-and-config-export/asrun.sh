#!/bin/bash
# asrun.sh DATADIR TASK [driver dsn] -- then commands on stdin, one per line
dd=$1; task=$2; driver=${3:-sqlite}; dsn=${4:-}
rm -rf /home/debian/wt/px$task; mkdir -p /home/debian/wt/h$task
/home/debian/bin/runproxy -db-driver $driver -db-dsn "$dsn" -data-dir $dd -scratch /home/debian/wt/px$task -task $task > /home/debian/wt/px$task.log 2>&1 &
pid=$!
for _ in $(seq 20); do grep -q "proxy http" /home/debian/wt/px$task.log && break; sleep 1; done
url=$(grep -o "proxy http[^ ]*" /home/debian/wt/px$task.log | cut -d' ' -f2)
[ -z "$url" ] && { cat /home/debian/wt/px$task.log; kill $pid; exit 1; }
while read -r cmd; do echo "--- [task $task] grain $cmd"; HOME=/home/debian/wt/h$task GRAIN_SANDBOX_PROXY=$url GRAIN_SANDBOX_TOKEN=walk-token eval "timeout 20 /home/debian/bin/grain $cmd" 2>&1 | head -14; done
kill -INT $pid; wait $pid
