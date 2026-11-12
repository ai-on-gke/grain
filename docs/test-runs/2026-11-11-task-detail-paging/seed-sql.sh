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

# seed-sql.sh <task-id> <comments> <runs> [question-at]: prints SQL (portable
# across SQLite and PostgreSQL) that gives an existing task <comments>
# comments and <runs> finished runs, each with a task_run_usage row. Odd
# comments are a person's, even ones an agent's. Runs 1..runs-7 succeed and
# the last seven fail, so the failure streak is 7. With question-at, that
# comment is an agent's question and the task is parked on it
# (pending_question_comment_id), so the task is awaiting_reply on a question
# far older than any head page.
task=$1 nc=$2 nr=$3 q=${4:-0}
awk -v task="$task" -v nc="$nc" -v nr="$nr" -v q="$q" 'BEGIN {
  print "BEGIN;"
  for (r = 1; r <= nr; r++) {
    s = sprintf("2026-10-01 %02d:%02d:%02d", int(r/60/60)%24, int(r/60)%60, r%60)
    f = sprintf("2026-10-01 %02d:%02d:%02d", int((r)/60/60)%24, int(r/60)%60, r%60)
    out = (r > nr - 7) ? "failed" : "succeeded"
    printf "INSERT INTO task_run (id,task_id,sandbox,attempt,started_at,finished_at,outcome,detail,model,agent_framework) VALUES (%c%s-%d%c,%c%s%c,%csb%c,%d,%c%s%c,%c%s%c,%c%s%c,%cdetail-%d%c,%cfake-model%c,%cantigravity%c);\n", 39,task,r,39, 39,task,39, 39,39, r, 39,s,39, 39,f,39, 39,out,39, 39,r,39, 39,39, 39,39
    printf "INSERT INTO task_run_usage (run_id,task_id,model,framework,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,cost_micro_usd,turns,duration_ms,started_at,finished_at) VALUES (%c%s-%d%c,%c%s%c,%cfake-model%c,%cantigravity%c,%d,%d,%d,0,%d,1,1000,%c%s%c,%c%s%c);\n", 39,task,r,39, 39,task,39, 39,39, 39,39, 1000+r, 10+r, 100, r*7, 39,s,39, 39,f,39
  }
  for (c = 1; c <= nc; c++) {
    at = sprintf("2026-10-02 %02d:%02d:%02d", int(c/3600)%24, int(c/60)%60, c%60)
    kind = (c % 2) ? "human" : "agent"; who = (c % 2) ? "alice@example.com" : sprintf("%s-%d", task, 1 + (c % nr))
    body = sprintf("walk comment %d of %d", c, nc); summ = ""
    if (c == q) { kind = "agent"; who = sprintf("%s-%d", task, nr); body = "Which branch should this land on? (walk question)"; summ = "Which branch should this land on?" }
    printf "INSERT INTO task_comment (task_id,author_kind,author_id,body,created_at,summary) VALUES (%c%s%c,%c%s%c,%c%s%c,%c%s%c,%c%s%c,%c%s%c);\n", 39,task,39, 39,kind,39, 39,who,39, 39,body,39, 39,at,39, 39,summ,39
  }
  print "COMMIT;"
}'
