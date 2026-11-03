#!/bin/sh
# Item 5: chat task $TASK's run pushes its grain-state checkout to main
# (refused, naming the proposal-branch prefix), then to that prefix
# (accepted), and to another task's prefix (refused). The stub backend
# runs each STUB-RUN line through the run's own run_command, so the push
# goes through the git proxy with the run's own credential.
TASK=${TASK:-3}
S=http://127.0.0.1:18702
T=$(cat /home/debian/live/.grain/local-api-token)
BODY=$(cat <<EOF
STUB-BEGIN
Try pushing grain/config directly.
STUB-RUN: cd grain-state && git fetch -q origin && git reset -q --hard origin/main && git commit -q --allow-empty -m 'direct push probe' && git push origin HEAD:main 2>&1; echo "exit=\$?"
STUB-RUN: cd grain-state && git push origin HEAD:refs/heads/grain/config/$TASK/direct-probe 2>&1; echo "exit=\$?"
STUB-RUN: cd grain-state && git push origin HEAD:refs/heads/grain/config/999/not-mine 2>&1; echo "exit=\$?"
STUB-RUN: cd grain-state && git push origin HEAD:refs/heads/grain/7e4e7587/task-$TASK-ops-chat 2>&1; echo "exit=\$?"
EOF
)
jq -n --arg b "$BODY" '{body:$b}' |
  curl -s -o /dev/null -w 'comment: %{http_code}\n' -H "Authorization: Bearer $T" \
    -H 'Content-Type: application/json' "$S/api/tasks/$TASK/comments" -d @-
