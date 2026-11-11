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

package sandboxcli

const sandboxHelpText = `grain -- sandbox CLI for this grain deployment (proxy: %s)

NAMESPACE & FILES (no size cap; pipe to grep, jq, head, or redirect to files):
  grain ls <uri> [--glob <pat>]          List a directory in ci://, src://, task://, config://, memory://, deploy://
  grain read <uri> [--tail N] [--offset N --limit N] [--lines N]
                                         Read a file from the namespace to stdout
                                         (--tail, --offset, --limit count bytes; --lines/-n N is the last N lines)
  grain grep <pattern> <uri> [-i] [-C N] [--glob <pat>] [--max N]
                                         Search a file or subtree with an RE2 regex

CI & PULL REQUEST (scoped to this task's repository and branch):
  grain ci list [--task <id> | --ref <b>] [--repo <r>] [--provider grain|github-actions] [--conclusion <c>] [--job <j>] [--since <d>] [--limit N] [--format github|github-jobs]
                                         List recent CI runs: grain CI's, and a GitHub Actions repo's workflow runs
  grain ci show <run-id>                 A run's jobs and steps with their conclusions (grain CI or gha:... Actions runs)
  grain ci checks <sha> [--repo <r>] [--name <check>] [--format github]
                                         A commit's check runs on the forge, every attempt of each
  grain ci log [<run-id> | --task <id>] [--repo <r>] [--job <j>] [--step <s>] [--attempt K] [--tail N | --lines N]
                                         List a run's jobs and steps, or print one step's log
  grain ci test-history <test-name> [--limit N] [--provider grain|github-actions]
                                         Pass/fail/flake history across recent CI runs
  grain ci artifact <job> <name> [-o <dest>] [--run <run-id> | --task <id>] [--repo <r>]
                                         Download a CI artifact
  grain ci wait [--jobs j1,j2] [--timeout 9m] [--resume <run-id>]
                                         Wait for this branch's CI verdict (GitHub Actions on ci_mode: github, grain CI on ci_mode: grain)
                                         (time blocked is credited back to this run's deadline)
  grain ci run-job <job> [--timeout 9m] [--resume <run-id>]
                                         Run a single named CI job against the current branch commit and wait
  grain ci retrigger [--jobs j1,j2 | --all] [--commit <sha>] [--workflow <file>]
                                         Rerun failed (or named) jobs fresh and uncached
  grain pr status [--task <id> | --ref <branch> [--repo <r>]]
                                         Latest CI verdict on this task's branch (grain CI, or every GitHub Actions workflow), or another task's or branch's
  grain pr base-status                   Latest CI verdict on the base branch
  grain pr open                          Open or update the pull request for this branch from its commit messages
  grain pr reply <body|--file f> [--comment-id <id>]
                                         Reply to an inline review comment, or post a top-level PR comment
  grain pr resolve (--thread-id <id> | --comment-id <id>)
                                         Mark a review conversation on this branch's pull request resolved
  grain pr threads [--unresolved]        List the review threads on this branch's pull request with their IDs
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
  grain task list [--state s] [--search t] [--limit N]
                                         Tasks this run may read, one line each
  grain task read <id>                   A task's record
  grain task prompt <id> [--attempt N] [--tail N | --lines N]
                                         A task's prompt
  grain task transcript <id> [--attempt N] [--tail N | --lines N]
                                         A task's transcript (its size goes to stderr)
  grain api routes [--scope <s>]         List agent API routes granted to this attempt
  grain api <METHOD> <path> [--body '<json>' | --field k=v] [--reason <why>]
                                         Call a granted non-dangerous agent API route
  grain memory (list | read <path> | write [--repo o/n] <text|--file f> | doc-write <name> <body|--file f>)
                                         Deployment memory and standing docs (api:memory / api:memory:write)
  grain release-notes (stats | publish --version <v> <body|--file f>)
                                         Release notes stats and publishing (api:release-notes)
  grain cap list                         Capability tools this run's grants let the sandbox call
  grain cap <server> <tool> [flags | --json '<args>']
                                         Call one (--help lists its flags, generated from its input schema)
  grain screenshot --url <app-url> [--pages /,/x] [--widths desktop,phone] [--reference design.png]
                                         Screenshot the app you are working on (light and dark, desktop and phone)
                                         plus a contact sheet to attach to the closing note, where grain shows it
  grain quarantine <pkg> <test> --reason <why> [--no-follow-up]
                                         Quarantine a flaky test in .grain/quarantine.yml and file a follow-up (api:test-quarantine)
  grain metrics [<section>] [--window 7d | --since <d> --until <d>]
                                         grain's own measurements (api:self-debug)
  grain health                           This deployment's health (deployment-health)
  grain log [--source daemon|dmesg] [--since <t>] [--until <t>] [--priority <p>] [--contains <s>] [--lines N]
                                         grain's own log, redacted (grain-log)
  grain playbooks [<name>]               Bootstrap playbooks (config-map bootstrap)
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
    regardless. A proposal that sets keep_running, or a priority above both your
    task's own and the default (5), always waits for a person: only a person may
    give a task either. Read what the call returns to see whether it queued or held.

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
                               that must close before this task can start. A proposal that answers
                               with a closing note and no pull request closes itself (see
                               --nothing-needed-action). Standing chat sessions never complete and
                               are dropped with a note.
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
  --priority <N>               Task priority, 1 or more (default 5; higher numbers mean higher
                               priority). 0 is refused. Not inherited from your task: left out,
                               the proposal files at 5 (or its template's priority). Above your
                               task's own and the default, it waits for a person.
  --no-repo                    Standalone task with no git repository or branch.
  --no-port                    Change belongs only on the target branch; do not port to paired branches.
  --keep-running               Immediately restart the task when its run exits instead of completing or parking.
                               Only a person may ask this, so the proposal waits for one.
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
  --design <[owner/name:]path[@rev]>
                               Committed-context spec path (repeatable; 'designs' in propose_task).
                               Omitted, the proposal inherits your task's committed context.
  --write <owner/name[:base]>  Extra repository this task may push to (repeatable).
  --custom-field <k=v>         Deployment custom field value (repeatable).
  --review-template-id <id>    Pre-merge code review template ID.
  --template-id <id>           Instantiate from an existing task template.
  --template-field <k=v>       Template input field value (repeatable).
  --nothing-needed-action <close|ack>
                               Whether nothing_needed closes the task (default) or leaves it for ack.
                               For a task your run files it also governs a closing note with no pull
                               request: close (the default) makes it a report-only sub-task that
                               closes when it answers, so tasks depending on it start and your next
                               attempt reads its note; ack leaves its report for a person to close.
  --goal <json>                Goal spec JSON: {"kind":"base-green|pr-merged|tasks-closed|person-closes|pipeline-healthy|cl-submitted","spec":{...},"on_met":"close|rest","on_limit":"hold|notify"}.
`

// sandboxCommandHelp is each command's --help.
var sandboxCommandHelp = map[string]string{
	"cap": "grain cap list [--json]\ngrain cap <server> [--help]\ngrain cap <server> <tool> --help\ngrain cap <server> <tool> [flags | --json '<args>']\n\n" +
		"Call a tool of an external capability server granted to this run, from the sandbox. Only tools\n" +
		"whose capability marks them sandbox-callable are here; a model-only tool is the agent's own MCP\n" +
		"tool and the proxy refuses it. Each tool's flags are generated from its input schema.\n\n" +
		"  grain cap list\n  grain cap widgets ping --help\n  grain cap widgets ping --host example.com --count 3\n  grain cap widgets ping --json '{\"host\":\"example.com\"}'\n",
	"screenshot": "grain screenshot --url <app-url> [--pages /,/settings] [flags]\n\n" +
		"Screenshot a web app -- the dev server this run started, a preview deployment -- in a real\n" +
		"browser (Playwright's Chromium, which the sandbox image carries), at desktop and phone widths\n" +
		"in light and dark, and lay every shot out on one contact sheet. Attach the contact sheet to the\n" +
		"closing note (comment_on_issue attachments: [{\"path\": \"screenshots/contact-sheet.png\"}]) and\n" +
		"grain shows it inline in the task's conversation and in the pull request's description.\n" +
		"Playwright is the app's own if it has one, else playwright-core is installed once into\n" +
		"~/.cache/grain/screenshot ($GRAIN_PLAYWRIGHT_VERSION picks the version).\n\n" +
		"  --url URL          the app's base URL, e.g. http://127.0.0.1:5173 (required)\n" +
		"  --pages P          comma-separated paths under --url (\"/\" if unset); a path's #anchor is\n" +
		"                     scrolled to the top before the shot\n" +
		"  --out DIR          directory for the PNGs (screenshots if unset)\n" +
		"  --widths W         any of desktop, tablet, phone (\"desktop,phone\")\n" +
		"  --themes T         any of light, dark (\"light,dark\"); sets prefers-color-scheme\n" +
		"  --theme-key KEY    also set localStorage[KEY] to the theme's name\n" +
		"  --storage K=V      a localStorage entry set before the page loads (repeat)\n" +
		"  --cookie N=V       a cookie for --url's host (repeat)\n" +
		"  --header \"N: v\"    a header sent on every request (repeat)\n" +
		"  --click SEL        a Playwright selector clicked after load (repeat, in order)\n" +
		"  --type TEXT        text typed after the clicks\n" +
		"  --wait-for SEL     wait for this selector to be visible before the shot\n" +
		"  --full-page        capture the whole scrollable page, not the viewport\n" +
		"  --reference FILE   a design image (a Figma export) put first on the contact sheet (repeat)\n" +
		"  --allow-errors     do not fail on uncaught page errors\n\n" +
		"Files are <page>-<width>-<theme>.png, so a before and an after run line up file for file.\n\n" +
		"  grain screenshot --url http://127.0.0.1:5173 --pages /,/checkout --out screenshots/after\n" +
		"  grain screenshot --url http://127.0.0.1:3000 --pages /pricing --widths phone --reference figma/pricing.png\n",
	"ls": "grain ls <uri> [--glob <pat>]\n\nList a namespace directory. --glob matches the directory's own entries; a\npattern with / or ** walks the subtree instead, and ends with a [truncated: ...]\nline if the walk was cut short. A trailing / matches directories only.\n\n  grain ls ci://<owner>/<repo>\n  grain ls task:// --glob '19*'\n  grain ls src://cmd --glob '**/*.go'\n\nsrc:// is the source this daemon was built from. A daemon whose own module builds on grain core (github.com/ai-on-gke/grain) serves core's source under src://core/, and for any src:// path its own module has no copy of.\n",
	"read": "grain read <uri> [--tail N] [--offset N --limit N] [--lines N]\n" +
		"grain read deploy://pipeline-changes [--cursor <c>] [--page N]\n\nPrint a namespace file to stdout, whole: there is no size cap.\n\n" +
		byteRangeHelp +
		"  --cursor <c>        deploy://pipeline-changes only: the changes after the cursor a page ended\n" +
		"                      with (its last line is the command that reads the next page), or after\n" +
		"                      an RFC 3339 time; --since is the same flag. Without it, the last 24 hours\n" +
		"  --page N            deploy://pipeline-changes only: at most N changes (default 100, at most 500)\n\n" +
		"  grain read task://1436/record.md\n  grain read ci://<owner>/<repo>/<run>/go/gotest.log --tail 4000\n" +
		"  grain read deploy://pipeline-changes --page 20\n  grain read deploy://pipeline-changes --cursor 2026-10-29T11:50:43.079363246Z~1\n",
	"grep": "grain grep <pattern> <uri> [-i] [-C N] [--glob <pat>] [--max N]\n\nSearch a namespace file or subtree with an RE2 regular expression.\n\n" +
		"  -i           match case-insensitively\n" +
		"  -C N         N lines of context around each match\n" +
		"  --glob <pat> only files matching pat, in a subtree\n" +
		"  --max N      stop after N matches (default 100)\n\n" +
		"A subtree walk stops after 2000 entries (20000 under src://, which skips node_modules/);\n" +
		"the last line then names where it stopped, and with no match the exit status is 1.\n\n" +
		"  grain grep -- '--- FAIL' ci://<owner>/<repo>/<run>/go/gotest.log\n  grain grep -i -C 2 TODO src://cmd --glob '**/*.go'\n",
	"ci list":   "grain ci list [--task <id> | --ref <branch>] [--conclusion failure,errored] [--job <j>] [--workflow <file>] [--since 24h] [--limit N] [--repo owner/name] [--provider grain|github-actions] [--format github|github-jobs]\n\nList recent CI runs, newest first: grain CI's, and for a repository whose CI is GitHub Actions\n(ci_mode github) its workflow runs too, read through grain's own forge connection. The second\ncolumn is the provider, and for an Actions run its workflow file; an Actions run's id is\ngha:<owner>:<name>:<run id>. --workflow keeps one workflow's runs (e.g. nightly.yml). --provider\nkeeps one provider's runs. --ref @branch is this task's own branch; --task <id> is that task's\nbranch, in any repository this task may read CI for.\n\n--format github writes the GitHub Actions runs as JSON in GitHub's own shape, GET\n/repos/{owner}/{repo}/actions/runs' answer ({\"workflow_runs\": [...]}, up to --limit 500), and\n--format github-jobs every attempt of every job of each of them (at most 50 runs), GET\n.../actions/runs/{id}/jobs?filter=all's answer ({\"jobs\": [...]}): what scripts/candidate-gate.sh\nreads without a GitHub token. Runs grain knows only from its record of their jobs are left out,\nand grain_note says how many.\n",
	"ci checks": "grain ci checks <sha> [--repo owner/name] [--name <check>] [--format github]\n\nEvery check run on a commit (every attempt of each, GitHub's filter=all), or of one --name, read\nthrough grain's own forge connection, with the App that posted each; \"(grain)\" marks grain's own.\n--format github writes GitHub's GET /repos/{owner}/{repo}/commits/{sha}/check-runs answer\n({\"check_runs\": [...]}) plus grain_app, the slug of the App grain posts its check runs as: what\nscripts/candidate-gate.sh check-qualification reads as --checks-json, and its\nCHECK_QUALIFICATION_APP.\n",
	"ci show":   "grain ci show <run-id>\n\nOne run: its line from grain ci list, then every job and its steps with their conclusions.\nWorks for grain CI runs and GitHub Actions runs (gha:<owner>:<name>:<run id>) alike.\n",
	"ci log": "grain ci log [<run-id> | --task <id>] [--repo owner/name] [--job <j>] [--step <s>] [--attempt K] [--tail N | --lines N]\n\nWith no run, the latest run on this task's branch (or --task's). A run is found in whichever\nrepository it belongs to that this task may read CI for; --repo names it outright. With no job, list the run's jobs;\nwith a job and no step, list its step logs; with both, print that step's log. A GitHub Actions run\n(gha:..., or its bare GitHub run id) keeps one log per job: --job prints it, and --step the lines that step\nwrote, found by the step's start and end times (--tail and --lines work on either as on any log).\n\n" +
		byteRangeHelp + "\n" +
		"  grain ci log --job go\n  grain ci log --job go --step Test --tail 8000\n  grain ci log --job go --step Test -n 50\n",
	"ci test-history": "grain ci test-history <test-name> [--limit N] [--repo owner/name] [--provider grain|github-actions]\n\nPass, fail and flake history of one test across recent CI runs. For a repository whose CI is\nGitHub Actions (ci_mode github) the runs are its workflow runs, with what grain recorded of\ntheir failed jobs' logs, and grain CI's older runs of it are left out (--provider grain reads\nthem). Grain keeps no log of an Actions job that passed, so there a test's failures are counted\nand its passes are not: the report names each source's runs and the window they span.\n",
	"ci artifact":     "grain ci artifact <job> <name> [-o <dest>] [--run <run-id> | --task <id>] [--repo owner/name]\n\nDownload a stored CI artifact, to <dest> or stdout. With no run, the latest run on this task's branch (or --task's).\n",
	"pr status":       "grain pr status [--task <id> | --ref <branch> [--repo owner/name]]\n\nThe latest CI on this task's branch and each job's verdict -- or on another task's branch (--task),\nor any branch (--ref, in --repo or this task's repository), where this task may read CI. For a\ngrain CI branch that is its newest run; for a GitHub Actions branch, every workflow's newest run at\nthe branch's newest commit.\n",
	"pr base-status":  "grain pr base-status\n\nThe latest CI on this task's base branch and each job's verdict, as grain pr status reports it.\n",
	"ci wait": "grain ci wait [--jobs j1,j2] [--timeout 9m] [--resume <run-id>]\n\n" +
		"Wait for this task's branch CI verdict: GitHub Actions / check runs on a ci_mode: github\n" +
		"repository, or grain CI on a ci_mode: grain repository (starting or attaching to the run in\n" +
		"flight): exit 0 green, 1 red. One call waits at most 9m (and less when\n" +
		"$GRAIN_COMMAND_TIMEOUT_MS says the command has less), so give run_command timeout 600000.\n" +
		"If CI is still running when the slice ends it says so, exits 0, and names the target to resume:\n\n" +
		"  grain ci wait --resume <run-id|my-branch>\n\n" +
		"or, to free the sandbox while CI finishes, call resume_later with when: {\"ci\": \"my-branch\"}\n" +
		"(or a grain CI / gha:<owner>:<name>:<run-id> run id).\n" +
		"Time blocked here is credited back to this run's deadline (up to 45m over the run).\n" +
		"--jobs (grain CI only) runs only those jobs (and their dependencies): fast, but never the full-suite gate.\n\n" +
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
		"Mark a review conversation on this branch's own pull request resolved.\n" +
		"grain pr threads --unresolved lists the IDs of those still open.\n",
	"pr threads": "grain pr threads [--unresolved]\n\n" +
		"List the review threads on this branch's own pull request, one per conversation: its\n" +
		"thread ID (for grain pr resolve --thread-id), the ID of the comment that opened it (for\n" +
		"grain pr reply --comment-id), its author, path:line, whether it is resolved or outdated,\n" +
		"and the comment with its replies. --unresolved lists only the open ones, outdated ones\n" +
		"included -- the set a branch requiring conversation resolution will not merge past.\n" +
		"Needs no CI grant.\n",
	"pr review-comment": "grain pr review-comment <body|--file f> [--path <file> --line <N>]\n\n" +
		"Record one review finding, as add_review_comment does: on a line with --path and --line,\n" +
		"or a general remark without. They are relayed together when this run finishes.\n",
	"git revert": "grain git revert (--task <id> | --commit <sha>) --reason <why> [-C <dir>]\n\n" +
		"Commit a git revert in this sandbox's working tree (the current directory, or -C <dir>) of\n" +
		"a task's merge -- found by the branch grain recorded for it -- or of one commit. A merge is\n" +
		"reverted against its first parent. Nothing is pushed.\n\n" +
		"  grain git revert --task 1234 --reason 'broke the go job on main'\n",
	"git":            "grain git revert (--task <id> | --commit <sha>) --reason <why>\n",
	"merge-queue":    "grain merge-queue\n\nThe merge queue: in-flight batches, queued pull requests and held gates (deploy://merge-queue).\n",
	"status":         "grain status <one-line-note>\n\nSet this run's live one-line status synopsis in grain's UI (at most 120 chars, no newlines).\n",
	"handover":       "grain handover write (<text> | --file <f>)\n\nWrite or replace this task's standing handover summary for future runs on this task.\n",
	"handover write": "grain handover write (<text> | --file <f>)\n\nWrite or replace this task's standing handover summary for future runs on this task.\nUse '--file -' to read from stdin.\n",
	"notify":         "grain notify --subject <key> --title <one-line> [--body <md> | --file <f>] [--severity info|warning|critical] [--task <id>] [--clears-when <json>]\n\nRaise an operator notification without parking this task.\n",
	"task list":      sandboxTaskListHelp,
	"task read":      "grain task read <id>\n\nA task's record (task://<id>/record.md). grain ls task://<id> shows its other files.\n",
	"task prompt":    "grain task prompt <id> [--attempt N] [--tail N] [--offset N --limit N] [--lines N]\n\nA task's latest prompt, or attempt N's.\n\n" + byteRangeHelp,
	"task transcript": "grain task transcript <id> [--attempt N] [--tail N] [--offset N --limit N] [--lines N]\n\n" +
		"A task's latest transcript, or attempt N's. How big the whole file is goes to stderr: a\n" +
		"transcript can run to hundreds of KB, so read part of it.\n\n" + byteRangeHelp + "\n" +
		"  grain task transcript 1933 --lines 40\n  grain task transcript 1933 --tail 20000 | grep -n error\n",
	"task wait": "grain task wait <id>... [--until closed|merged] [--timeout 9m]\n\n" +
		"Block until every named task has closed (or merged), up to a 9m slice with wait credit.\n" +
		"To park and free the sandbox VM across a long wait, call the wait_for_task (park: true)\n" +
		"or resume_later MCP control tool instead.\n",
	"task message":          "grain task message <id> (<body> | --body <b> | --file <f>)\n\nLeave a note on a parent or child task's conversation (or any task with api:tasks:admin).\n",
	"task propose":          sandboxProposeHelp,
	"propose":               sandboxProposeHelp,
	"api":                   "grain api routes [--scope <s>]\ngrain api <METHOD> <path> [--body '<json>' | --file <f> | --field k=v] [--reason <why>]\n\nList or call non-dangerous agent API routes granted to this run.\n\nA repository off the default git host goes in a /api/repos/{owner}/{name}/... path\nwith its host and owner escaped into one segment:\n  grain api GET '/api/repos/git.forge.example%2Fexperimental/grain/releases'\n",
	"api routes":            "grain api routes [--scope <s>]\n\nList the non-dangerous agent API routes this run's capabilities unlock.\n",
	"memory":                "grain memory (list | read <path> | write [--repo o/n] <text|--file f> | doc-write <name> <body|--file f>)\n",
	"memory list":           "grain memory list\n\nList entries in deployment memory (memory://).\n",
	"memory read":           "grain memory read <path>\n\nRead a file from deployment memory (memory://<path>; grain memory list names them).\n",
	"memory write":          "grain memory write [--repo owner/name] (<text> | --file <f>) [--reason <why>]\n\nReplace the deployment's memory text (or a repository's, with --repo) with <text>. It is one text, not keyed entries: a named entry is grain memory doc-write. Needs api:memory or api:memory:write.\n",
	"memory doc-write":      "grain memory doc-write <path> (<content> | --file <f>) [--summary <s>] [--repo <owner/name>] [--reason <why>]\n\nWrite a standing document in the memory repository. Needs api:memory or api:memory:write.\n",
	"memory doc":            "grain memory doc write <path> (<content> | --file <f>)\n\nWrite a standing document in the memory repository. Needs api:memory or api:memory:write.\n",
	"release-notes":         "grain release-notes (stats | publish --version <v> <body|--file f>)\n",
	"release-notes stats":   "grain release-notes stats [<period> | --period <p>]\n\nCompute task and PR statistics for a release window. Needs api:release-notes or api:release-notes:read.\n",
	"release-notes publish": "grain release-notes publish --version <v> (<markdown> | --file <f>) [--period <p>] [--title <t>] [--reason <why>]\n\nPublish release notes for a version. Needs api:release-notes or api:release-notes:write.\n",
	"quarantine":            "grain quarantine <package> <test-name> --reason <why> [--ttl 30d] [--no-follow-up] [-C <dir>]\n\nAdd a flaky test entry to .grain/quarantine.yml in the workspace and file a follow-up task.\nNeeds api:test-quarantine or api:tests:write.\n",
	"metrics": "grain metrics [<section>] [--window 7d | --since <date> [--until <date>]] [--repo owner/name]... [--author <a>]...\n\n" +
		"grain's own measurements (deploy://metrics). Needs api:self-debug or api:deployment:read.\n\n" +
		"  --window   how far back to measure: 36h, 7d, 2w (default: the daemon's own)\n" +
		"  --since, --until  an absolute span instead: a date or an RFC 3339 timestamp\n" +
		"  --repo, --author  only tasks on these repositories / by these authors (repeat or comma-separate)\n",
	"health": "grain health\n\nThis deployment's health (deploy://health). Needs deployment-health.\n",
	"log": "grain log [<source> | --source daemon|dmesg] [--unit <unit>] [--since <rfc3339>] [--until <rfc3339>] [--priority err|warning|...] [--contains <text>] [--lines N] [--tail N]\n\n" +
		"grain's own log, redacted (deploy://grain.log). Needs grain-log.\n\n" +
		"  --since, --until  the window, as RFC 3339 timestamps (default: the latest entries)\n" +
		"  --source          which journal: daemon (the default) or dmesg (the kernel's)\n" +
		"  --unit            for daemon when both grain-daemon.service and grain.service exist: which unit to read\n" +
		"  --priority        least severe journald priority to return (err, warning, info, ...; alias --min-priority)\n" +
		"  --contains        only entries containing this text (plain, case-insensitive)\n" +
		"  --lines N, -n N   the latest N entries, one a line (alias --max-lines; default 200, at most 1000)\n" +
		"  --tail N          the last N BYTES of what that returns\n",
	"playbooks":  "grain playbooks [<name>]\n\nList the bootstrap playbooks by name, one per line, or print one (src://playbooks; the name\nis taken with or without its .md). Needs https://grain.dev/capabilities/config-map#config-map={\"config_maps\":[\"bootstrap\"]}.\n",
	"issue list": "grain issue list [--repo owner/name] [--label <l>] [--limit N]\n\nGitHub issues. Needs https://grain.dev/capabilities/github#read-issues (formerly read-issues).\n",
	"issue read": "grain issue read <number> [--repo owner/name] [--comments]\n\nOne GitHub issue. Needs https://grain.dev/capabilities/github#read-issues (formerly read-issues).\n",
	"db query":   "grain db query <sql>\n\nA read-only SQL query on grain's database. Needs db-read.\n",
	"ci":         "grain ci (list | show | checks | log | test-history | artifact | wait | run-job | retrigger) ...\n\nRun 'grain ci <command> --help' for each.\n",
	"pr":         "grain pr (status | base-status | open | reply | resolve | threads | review-comment)\n",
	"task":       "grain task (list | read | prompt | transcript | wait | message | propose) ...\n",
	"issue":      "grain issue (list | read) ...\n",
	"db":         "grain db query <sql>\n",
}

// sandboxTaskListHelp is grain task list --help.
const sandboxTaskListHelp = "grain task list [--state <s>[,<s>...]] [--repo <owner/name>] [--search <title text>]\n" +
	"                [--kind <k>] [--changed-since <30m|24h|7d|time>] [--stalled] [--limit N] [--json]\n\n" +
	"The tasks this run may read (its own, or every task with api:self-debug or api:tasks:read), one line each:\n" +
	"id, state, repo, title, then \"(stopped by operator)\" or \"(stalled)\" and \"blocked on <ids>\" where they apply.\n" +
	"Oldest first by filing; --limit (default 50) keeps the newest. The count, and how many\n" +
	"were left out, go to stderr. --state may be repeated or comma-separated (proposed, queued,\n" +
	"running, awaiting_reply, failed, awaiting_submit, completed, closed); --kind is one of\n" +
	"port, repair, session, proposal, durable, imported. Example, before filing a fix:\n" +
	"  grain task list --state proposed --search 'go-long'\n"

// byteRangeHelp is how read, ci log and task prompt/transcript take a part
// of a file.
const byteRangeHelp = "  --tail N            the last N BYTES (not lines: --tail 2000 is about 2 KB)\n" +
	"  --offset A --limit B  B bytes starting at byte A\n" +
	"  --lines N, -n N     the last N lines (of the byte range, when one is given too)\n"
