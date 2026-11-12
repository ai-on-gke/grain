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

# walk.sh <data-dir> <port> <task> <sql-cmd>: walks GET /api/tasks/<task>
# backwards from the head page (comments_limit=100&attempts_limit=50) to the
# start with comments_before= / attempts_before=, while a writer posts a
# person's comment through the API and inserts a finished run (with usage)
# through <sql-cmd> (e.g. "sqlite3 db" or "psql -q ...") every 0.2s. It then
# checks: no duplicate comment or attempt ids, and every id from the first up
# to the head page's newest is present (no gaps). Last, it checks the
# head-page cache: 30 rounds of "post a comment, GET the head with the
# previous ETag" must each answer 200 with the new comment on top.
dir=$1 port=$2 task=$3 sql=$4
tok=$(cat "$dir/local-api-token"); base="http://127.0.0.1:$port/api/tasks/$task"
get() { curl -s -H "Authorization: Bearer $tok" "$base$1"; }
post() { curl -s -o /dev/null -X POST -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' --data "$2" "$base$1"; }
out=$(mktemp -d)
( i=0; while [ ! -e "$out/stop" ]; do i=$((i+1))
    post /comments "{\"body\":\"concurrent comment $i\"}"
    n=$((10000 + i))
    echo "INSERT INTO task_run (id,task_id,sandbox,attempt,started_at,finished_at,outcome,detail) VALUES ('$task-$n','$task','sb',$n,'2026-11-01 00:00:00','2026-11-01 00:00:01','succeeded','');" | $sql
    sleep 0.2
  done; echo "$i" > "$out/writes" ) &
head=$(get '?comments_limit=100&attempts_limit=50')
echo "$head" | jq -r '.comments[].id' > "$out/c"; echo "$head" | jq -r '.attempts[].number' > "$out/a"
newestC=$(tail -n1 "$out/c"); newestA=$(tail -n1 "$out/a")
cb=$(head -n1 "$out/c"); ab=$(head -n1 "$out/a"); pages=1
while [ -n "$cb$ab" ]; do
  q="?comments_limit=100&attempts_limit=50"
  [ -n "$cb" ] && q="$q&comments_before=$cb"; [ -n "$ab" ] && q="$q&attempts_before=$ab"
  p=$(get "$q"); pages=$((pages+1))
  if [ -n "$cb" ]; then echo "$p" | jq -r '.comments[].id' > "$out/pc"; cat "$out/pc" >> "$out/c"
    [ "$(echo "$p" | jq '.olderComments // 0')" = 0 ] && cb= || cb=$(head -n1 "$out/pc"); fi
  if [ -n "$ab" ]; then echo "$p" | jq -r '.attempts[].number' > "$out/pa"; cat "$out/pa" >> "$out/a"
    [ "$(echo "$p" | jq '.olderAttempts // 0')" = 0 ] && ab= || ab=$(head -n1 "$out/pa"); fi
  sleep 0.1
done
touch "$out/stop"; wait
firstC=$(sort -n "$out/c" | head -n1)
echo "pages=$pages writes=$(cat "$out/writes") comments=$(wc -l < "$out/c") dupComments=$(sort "$out/c" | uniq -d | wc -l) attempts=$(wc -l < "$out/a") dupAttempts=$(sort "$out/a" | uniq -d | wc -l)"
echo "comment ids $firstC..$newestC: missing=$(comm -23 <(seq "$firstC" "$newestC") <(sort -n "$out/c" | uniq) | wc -l)"
echo "attempts 1..600 + head: missing=$(comm -23 <(seq 1 600 | sort) <(sort "$out/a" | uniq) | wc -l) newestHead=$newestA"
# Head-page cache invalidation.
etag=$(curl -s -D - -o /dev/null -H "Authorization: Bearer $tok" "$base?comments_limit=50&attempts_limit=20" | tr -d '\r' | sed -n 's/^[Ee][Tt]ag: //p')
ok=0
for i in $(seq 30); do
  post /comments "{\"body\":\"cache probe $i\"}"
  code=$(curl -s -o "$out/h" -D "$out/hd" -w '%{http_code}' -H "Authorization: Bearer $tok" -H "If-None-Match: $etag" "$base?comments_limit=50&attempts_limit=20")
  etag=$(tr -d '\r' < "$out/hd" | sed -n 's/^[Ee][Tt]ag: //p')
  [ "$code" = 200 ] && [ "$(jq -r '.comments[-1].body' "$out/h")" = "cache probe $i" ] && ok=$((ok+1))
done
echo "cache: $ok/30 polls answered 200 with the comment just posted"
rm -rf "$out"
