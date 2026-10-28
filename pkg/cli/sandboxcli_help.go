// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cli

const sandboxHelpText = `grain -- sandbox CLI for this grain deployment (proxy: %s)

NAMESPACE & FILES (no size cap; pipe to grep, jq, head, or redirect to files):
  grain ls <uri> [--glob <pat>]          List a directory in ci://, src://, task://, config://, memory://, deploy://
  grain read <uri> [--tail N] [--offset N --limit N]
                                         Read a file from the namespace to stdout
  grain grep <pattern> <uri> [-i] [-C N] [--glob <pat>]
                                         Search a file or subtree with an RE2 regex

CI & PULL REQUEST (scoped to this task's repository and branch):
  grain ci list [--ref <b>] [--conclusion <c>] [--job <j>] [--since <d>] [--limit N]
                                         List recent grain CI runs
  grain ci log [<run-id>] [--job <j>] [--step <s>] [--attempt K] [--tail N]
                                         List a run's jobs and steps, or print one step's log
  grain ci test-history <test-name> [--limit N]
                                         Pass/fail/flake history across recent CI runs
  grain ci artifact <job> <name> [-o <dest>] [--run <run-id>]
                                         Download a CI artifact
  grain ci wait [--jobs j1,j2] [--timeout 9m] [--resume <run-id>]
                                         Start or attach to grain CI for this branch and wait for its verdict
                                         (time blocked is credited back to this run's deadline)
  grain ci run-job <job> [--timeout 9m] [--resume <run-id>]
                                         Run a single named CI job against the current branch commit and wait
  grain ci retrigger [--jobs j1,j2 | --all] [--commit <sha>] [--workflow <file>]
                                         Rerun failed (or named) jobs fresh and uncached
  grain pr status                        Latest grain CI verdict on this task's branch
  grain pr base-status                   Latest grain CI verdict on the base branch
  grain pr open                          Open or update the pull request for this branch from its commit messages
  grain pr reply <body|--file f> [--comment-id <id>]
                                         Reply to an inline review comment, or post a top-level PR comment
  grain pr resolve (--thread-id <id> | --comment-id <id>)
                                         Mark a review conversation on this branch's pull request resolved
  grain pr review-comment <body|--file f> [--path <file> --line <N>]
                                         Record a review finding (relayed when the run finishes)
  grain merge-queue                      Merge queue batches, queued PRs and held gates

GIT, TASK & COLLABORATION:
  grain status <one-line-note>           Update this run's live activity status in the UI (<120 chars)
  grain handover write (<text> | --file <f>)
                                         Write or replace the standing handover summary for this task
  grain task wait <id>... [--until closed|merged] [--timeout 9m]
                                         Block until other task(s) close or merge (with wait credit)
  grain task message <id> (<body|--file f>)
                                         Leave a note on a parent or child task's conversation
  grain task propose --title <t> (--body <b> | --file <f>) [flags]
                                         File an unapproved follow-up task proposal (run 'grain help propose' for full reference)
  grain notify --subject <s> --title <t> [--body <b>] [--severity info|warning] [--clears-when <json>]
                                         Raise an operator notification without parking this task
  grain git revert (--task <id> | --commit <sha>) --reason <why> [-C <dir>]
                                         Commit a git revert of a task's merge or of one commit

TASKS, CAPABILITIES & AGENT API (shown to every run; the proxy refuses what this run's grants do not cover):
  grain task list                        Tasks this run may read
  grain task read <id>                   A task's record
  grain task prompt <id> [--attempt N]   A task's prompt
  grain task transcript <id> [--attempt N]
                                         A task's transcript
  grain api routes [--scope <s>]         List agent API routes granted to this attempt
  grain api <METHOD> <path> [--body '<json>' | --field k=v] [--reason <why>]
                                         Call a granted non-dangerous agent API route
  grain memory (list | read <key> | write <key> <body|--file f> | doc-write <path> <body|--file f>)
                                         Deployment memory and standing docs (memory / api:memory:write)
  grain release-notes (stats | publish --version <v> <body|--file f>)
                                         Release notes stats and publishing (release-notes)
  grain quarantine <pkg> <test> --reason <why> [--no-follow-up]
                                         Quarantine a flaky test in .grain/quarantine.yml and file a follow-up (quarantine-test)
  grain metrics [<section>]              grain's own measurements (self-debug)
  grain health                           This deployment's health (deployment-health)
  grain log [<source>] [--tail N]        grain's own log, redacted (grain-log)
  grain playbooks [<name>]               Bootstrap playbooks (bootstrap-playbooks)
  grain issue list | grain issue read <n>
                                         GitHub issues (read-issues)
  grain db query <sql>                   Read-only query on grain's database (db-read)

Run 'grain <command> --help' for flags and examples.
`

const sandboxProposeHelp = `grain task propose --title <t> (--body <b> | --file <f>) [flags]
propose_task (MCP control tool)

Propose a new task for the task queue -- for example, splitting follow-up work
out of the task you were given, or flagging work you noticed is needed but is
out of scope for this one.

SURFACE & APPROVAL:
  - Over 'grain task propose' (sandbox credential), every proposal is forced to
    hold_for_approval: true and auto_merge: false, and 'supersedes' is refused.
    A sandbox process can at most file an unapproved draft (capped at 10 per
    attempt).
  - Over the model-only 'propose_task' MCP tool, a proposal is normally filed
    unapproved unless your own task's permission already covers it: if your task
    is an auto-merge job, a proposal that lands on the same branch with no
    capability your task lacks is filed approved and dispatched in its turn with
    nobody looking at it, and the same is true of a standing chat session whose
    deployment allows that session to queue its own work. Set hold_for_approval
    on any proposal a human should look at first and it is filed unapproved
    regardless. Read what the call returns to see whether it queued or held.

ARGUMENTS & FLAGS:
  --title <string>             Task title (required unless --template-id is set).
  --body <string> | --file <f> Task description in markdown (required unless --template-id is set).
  --id <string>                Short local name used only so a later propose_task call in the
                               same run can list this one in its own depends_on.
  --repo <owner/name>          Repository the work belongs in. Defaults to this task's repository.
                               Accepted: a repository your task declares (filed on its declared
                               branch) or any repository this deployment works in (filed on its
                               default branch). Unknown repos are filed against yours with a note.
  --branch <name>              Base branch to target (defaults to the repo's default/declared branch).
  --depends-on <id[,id...]>    Task IDs (including your current task or an earlier proposal's --id)
                               that must close before this task can start. Standing chat sessions
                               never complete and are dropped with a note.
  --supersedes <task-id>       (propose_task MCP tool only) Replace an existing task that has never
                               been dispatched and was filed in the same repository. Closes the old
                               task as superseded and moves its open dependents to this proposal;
                               a task with an attempt, live run, or open PR is left open with a note.
  --auto-merge                 (propose_task MCP tool only) Inherit auto-merge from your task when
                               landing on the same/declared branch. Pass auto_merge: false for
                               separate work that deserves its own human review.
  --hold-for-approval          File unapproved for a human to review even if your task's permission
                               would have queued it.
  --at-front                   Place at the front of the backlog ahead of already queued work.
  --no-repo                    Standalone task with no git repository or branch.
  --no-port                    Change belongs only on the target branch; do not port to paired branches.
  --fixes-base-branch          Marks this task as repairing a red base branch (gives it a base-green
                               goal and lets it run while the branch is red).
  --origin <ref>               GitHub PR or issue origin (owner/repo#N, #N, or URL).
  --not-before <date|rfc3339>  Defer dispatch until this UTC date (2026-10-14) or RFC 3339 timestamp.
  --model <name>               Pin model/backend (e.g. claude-sonnet-5, gemini-3.1-pro).
  --agent-framework <name>     Pin agent framework (antigravity, claude, codex).
  --network-access <open|none> Outbound internet policy for the task's sandbox.
  --sandbox-cpus <N>           vCPUs for the task's sandbox VM.
  --sandbox-memory-mb <N>      Memory in MiB for the task's sandbox VM.
  --sandbox-disk-gb <N>        Disk in GiB for the task's sandbox VM.
  --capability <cap>           Request a capability (repeatable). Naming a capability your own task
                               does not hold always files the proposal unapproved.
  --read <owner/name>          Extra repository this task may read (repeatable).
  --write <owner/name[:base]>  Extra repository this task may push to (repeatable).
  --custom-field <k=v>         Deployment custom field value (repeatable).
  --review-template-id <id>    Pre-merge code review template ID.
  --template-id <id>           Instantiate from an existing task template.
  --template-field <k=v>       Template input field value (repeatable).
  --nothing-needed-action <close|ack>
                               Whether nothing_needed closes the task (default) or leaves it for ack.
  --goal <json>                Goal spec JSON: {"kind":"base-green|pr-merged|tasks-closed|person-closes|pipeline-healthy|cl-submitted","spec":{...},"on_met":"close|rest","on_limit":"hold|notify"}.
`

// sandboxCommandHelp is each command's --help.
var sandboxCommandHelp = map[string]string{
	"ls":              "grain ls <uri> [--glob <pat>]\n\nList a namespace directory.\n\n  grain ls ci://bwsalmon/grain\n  grain ls task://\n  grain ls src://pkg --glob '*_test.go'\n",
	"read":            "grain read <uri> [--tail N] [--offset N --limit N]\n\nPrint a namespace file to stdout, whole: there is no size cap.\n\n  grain read task://1436/record.md\n  grain read ci://bwsalmon/grain/<run>/go/gotest.log --tail 4000\n",
	"grep":            "grain grep <pattern> <uri> [-i] [-C N] [--glob <pat>]\n\nSearch a namespace file or subtree with an RE2 regular expression.\n\n  grain grep -- '--- FAIL' ci://bwsalmon/grain/<run>/go/gotest.log\n  grain grep -i -C 2 TODO src://pkg/ci --glob '*.go'\n",
	"ci list":         "grain ci list [--ref <branch>] [--conclusion failure,errored] [--job <j>] [--since 24h] [--limit N] [--repo owner/name]\n\nList recent grain CI runs, newest first. --ref @branch is this task's own branch.\n",
	"ci log":          "grain ci log [<run-id>] [--job <j>] [--step <s>] [--attempt K] [--tail N]\n\nWith no run, the latest run on this task's branch. With no job, list the run's jobs;\nwith a job and no step, list its step logs; with both, print that step's log.\n\n  grain ci log --job go\n  grain ci log --job go --step Test --tail 8000\n",
	"ci test-history": "grain ci test-history <test-name> [--limit N]\n\nPass, fail and flake history of one test across recent CI runs.\n",
	"ci artifact":     "grain ci artifact <job> <name> [-o <dest>] [--run <run-id>]\n\nDownload a stored CI artifact, to <dest> or stdout. With no run, the latest run on this task's branch.\n",
	"pr status":       "grain pr status\n\nThe latest grain CI run on this task's branch and each job's verdict.\n",
	"pr base-status":  "grain pr base-status\n\nThe latest grain CI run on this task's base branch and each job's verdict.\n",
	"ci wait": "grain ci wait [--jobs j1,j2] [--timeout 9m] [--resume <run-id>]\n\n" +
		"Start grain CI on this task's branch (or attach to the identical run in flight) and wait for\n" +
		"its verdict: exit 0 green, 1 red. One call waits at most 9m (and less when\n" +
		"$GRAIN_COMMAND_TIMEOUT_MS says the command has less), so give run_command timeout 600000.\n" +
		"If CI is still running when the slice ends it says so, exits 0, and names the run to resume:\n\n" +
		"  grain ci wait --resume <run-id>\n\n" +
		"or, to free the sandbox while CI finishes, call resume_later with when: {\"ci\": \"<run-id>\"}.\n" +
		"Time blocked here is credited back to this run's deadline (up to 45m over the run).\n" +
		"--jobs runs only those jobs (and their dependencies): fast, but never the full-suite gate.\n\n" +
		"  git push origin HEAD && grain ci wait\n  grain ci wait --jobs go,lint\n",
	"ci run-job": "grain ci run-job <job> [--timeout 9m] [--resume <run-id>]\n\n" +
		"Run one named CI job (and its dependencies) against this branch's commit and wait for it,\n" +
		"with the same 9m slice, resume and wait credit as grain ci wait.\n",
	"ci retrigger": "grain ci retrigger [--jobs j1,j2 | --all] [--commit <sha>] [--workflow <file>]\n\n" +
		"Rerun this branch's failed jobs fresh and uncached (or the named ones, or --all of them).\n" +
		"Then wait on it with grain ci wait --resume <run-id>.\n",
	"pr open": "grain pr open\n\nOpen the pull request for this task's branch (or find the one already open) and\n" +
		"report its checks. Its title and description come from the branch's commit messages.\n",
	"pr reply": "grain pr reply <body|--file f> [--comment-id <id>]\n\n" +
		"Post on GitHub on this branch's own pull request: in an inline review comment's thread\n" +
		"with --comment-id (grain pr status lists them), or as a top-level comment without.\n",
	"pr resolve": "grain pr resolve (--thread-id <id> | --comment-id <id>)\n\n" +
		"Mark a review conversation on this branch's own pull request resolved.\n",
	"pr review-comment": "grain pr review-comment <body|--file f> [--path <file> --line <N>]\n\n" +
		"Record one review finding, as add_review_comment does: on a line with --path and --line,\n" +
		"or a general remark without. They are relayed together when this run finishes.\n",
	"git revert": "grain git revert (--task <id> | --commit <sha>) --reason <why> [-C <dir>]\n\n" +
		"Commit a git revert in this sandbox's working tree (the current directory, or -C <dir>) of\n" +
		"a task's merge -- found by the branch grain recorded for it -- or of one commit. A merge is\n" +
		"reverted against its first parent. Nothing is pushed.\n\n" +
		"  grain git revert --task 1234 --reason 'broke the go job on main'\n",
	"git":             "grain git revert (--task <id> | --commit <sha>) --reason <why>\n",
	"merge-queue":     "grain merge-queue\n\nThe merge queue: in-flight batches, queued pull requests and held gates (deploy://merge-queue).\n",
	"status":          "grain status <one-line-note>\n\nSet this run's live one-line status synopsis in grain's UI (at most 120 chars, no newlines).\n",
	"handover":        "grain handover write (<text> | --file <f>)\n\nWrite or replace this task's standing handover summary for future runs on this task.\n",
	"handover write":  "grain handover write (<text> | --file <f>)\n\nWrite or replace this task's standing handover summary for future runs on this task.\nUse '--file -' to read from stdin.\n",
	"notify":          "grain notify --subject <key> --title <one-line> [--body <md> | --file <f>] [--severity info|warning|critical] [--task <id>] [--clears-when <json>]\n\nRaise an operator notification without parking this task.\n",
	"task list":       "grain task list\n\nThe tasks this run may read: its own, or every task with self-debug.\n",
	"task read":       "grain task read <id>\n\nA task's record (task://<id>/record.md). grain ls task://<id> shows its other files.\n",
	"task prompt":     "grain task prompt <id> [--attempt N]\n\nA task's latest prompt, or attempt N's.\n",
	"task transcript": "grain task transcript <id> [--attempt N]\n\nA task's latest transcript, or attempt N's.\n",
	"task wait": "grain task wait <id>... [--until closed|merged] [--timeout 9m]\n\n" +
		"Block until every named task has closed (or merged), up to a 9m slice with wait credit.\n" +
		"To park and free the sandbox VM across a long wait, call the wait_for_task (park: true)\n" +
		"or resume_later MCP control tool instead.\n",
	"task message":          "grain task message <id> (<body|--file f>)\n\nLeave a note on a parent or child task's conversation (or any task with api:tasks:admin).\n",
	"task propose":          sandboxProposeHelp,
	"propose":               sandboxProposeHelp,
	"api":                   "grain api routes [--scope <s>]\ngrain api <METHOD> <path> [--body '<json>' | --file <f> | --field k=v] [--reason <why>]\n\nList or call non-dangerous agent API routes granted to this run.\n",
	"api routes":            "grain api routes [--scope <s>]\n\nList the non-dangerous agent API routes this run's capabilities unlock.\n",
	"memory":                "grain memory (list | read <key> | write <key> <body|--file f> | doc-write <path> <body|--file f>)\n",
	"memory list":           "grain memory list\n\nList entries in deployment memory (memory://).\n",
	"memory read":           "grain memory read <key>\n\nRead a file from deployment memory (memory://<key>).\n",
	"memory write":          "grain memory write <key> (<content> | --file <f>)\n\nWrite an entry under memory/entries/<key>. Needs memory or api:memory:write.\n",
	"memory doc-write":      "grain memory doc-write <path> (<content> | --file <f>)\n\nWrite a standing document in the memory repository. Needs memory or api:memory:write.\n",
	"memory doc":            "grain memory doc write <path> (<content> | --file <f>)\n\nWrite a standing document in the memory repository. Needs memory or api:memory:write.\n",
	"release-notes":         "grain release-notes (stats | publish --version <v> <body|--file f>)\n",
	"release-notes stats":   "grain release-notes stats [--since <rfc3339>] [--until <rfc3339>]\n\nCompute task and PR statistics for a release window. Needs release-notes.\n",
	"release-notes publish": "grain release-notes publish --version <v> (<markdown> | --file <f>) [--since <rfc3339>] [--until <rfc3339>]\n\nPublish release notes for a version. Needs release-notes.\n",
	"quarantine":            "grain quarantine <package> <test-name> --reason <why> [--expires <YYYY-MM-DD>] [--Follow-up-title <t>] [--no-follow-up] [-C <dir>]\n\nAdd a flaky test entry to .grain/quarantine.yml in the workspace and file a follow-up task.\nNeeds quarantine-test.\n",
	"metrics":               "grain metrics [<section>]\n\ngrain's own measurements (deploy://metrics). Needs self-debug.\n",
	"health":                "grain health\n\nThis deployment's health (deploy://health). Needs deployment-health.\n",
	"log":                   "grain log [<source>] [--tail N]\n\ngrain's own log, redacted (deploy://grain.log). Needs grain-log.\n",
	"playbooks":             "grain playbooks [<name>]\n\nList the bootstrap playbooks, or print one (src://playbooks). Needs bootstrap-playbooks.\n",
	"issue list":            "grain issue list [--repo owner/name]\n\nGitHub issues. Needs read-issues.\n",
	"issue read":            "grain issue read <number> [--repo owner/name]\n\nOne GitHub issue. Needs read-issues.\n",
	"db query":              "grain db query <sql>\n\nA read-only SQL query on grain's database. Needs db-read.\n",
	"ci":                    "grain ci (list | log | test-history | artifact | wait | run-job | retrigger) ...\n\nRun 'grain ci <command> --help' for each.\n",
	"pr":                    "grain pr (status | base-status | open | reply | resolve | review-comment)\n",
	"task":                  "grain task (list | read | prompt | transcript | wait | message | propose) ...\n",
	"issue":                 "grain issue (list | read) ...\n",
	"db":                    "grain db query <sql>\n",
}
