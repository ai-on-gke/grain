#!/bin/sh
# Items 1 and 2: send chat task $TASK (a repo-less session holding
# writes: [grain/config]) a turn that the stub backend replays: retitle
# system:infra-review in its grain-state checkout, check it, and call
# propose_config_change. Then print the card grain recorded.
TASK=${TASK:-3}
S=http://127.0.0.1:18702
T=$(cat /home/debian/live/.grain/local-api-token)
BODY=$(cat <<'EOF'
STUB-BEGIN
Retitle the infrastructure review.
STUB-RUN: cd grain-state && sed -i '0,/title: Suggest what to tune for throughput and reliability/s//title: Nightly infrastructure review (renamed)/' settings/schedules.yml && git diff --stat
STUB-CALL: propose_config_change {"summary":"Rename the infrastructure review schedule","branch":"rename-infra-review"}
EOF
)
jq -n --arg b "$BODY" '{body:$b}' |
  curl -s -o /dev/null -w 'comment: %{http_code}\n' -H "Authorization: Bearer $T" \
    -H 'Content-Type: application/json' "$S/api/tasks/$TASK/comments" -d @-
