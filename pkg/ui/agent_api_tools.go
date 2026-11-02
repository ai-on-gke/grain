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

package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// This file turns the route table into MCP tools (docs/agent-api.md): one
// tool per route whose scope is in the vocabulary, named after the
// route's handler, described from routeDocs, and taking the route's path
// parameters, its query and its body as arguments. A task granted a
// scope is served the tools of every route under it. The tools call the
// route over HTTP exactly as the UI does -- same handler, same
// validation, same side effects -- with ActingTaskHeader set, so
// agentAPIGate holds the call to the task's grants and records it.

// APIRoute is one route of the agent API, as a tool.
type APIRoute struct {
	Pattern     string   `json:"pattern"`
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Scope       APIScope `json:"scope"`
	Tool        string   `json:"tool"`
	Description string   `json:"description"`
	Params      []string `json:"params,omitempty"`
	// NeedsReason says a call must carry a reason: a dangerous scope,
	// or a DELETE.
	NeedsReason bool `json:"needsReason,omitempty"`
	body        any
	query       map[string]string
}

// APIToolPrefix begins every generated tool's name, so none can collide
// with a hand-written tool of the same verb (list_ci_runs, say).
const APIToolPrefix = "api_"

// routeDocs is the sentence a route's tool is described by. Every route
// that changes something has one; a read with none is described from
// its handler name, method and path.
var routeDocs = map[string]string{
	"PUT /api/settings":                                                    "Change deployment settings: only the fields given are changed, the rest keep their values.",
	"POST /api/settings":                                                   "Change deployment settings, as PUT /api/settings does: only the fields given are changed.",
	"POST /api/settings/plan":                                              "Preview a settings change without saving it: what the given fields would change, and what it would take to apply.",
	"PUT /api/settings/cli-run":                                            "Set the per-framework settings for agent CLI runs, keyed by framework.",
	"POST /api/capabilities/{id}/check":                                    "Check that a capability's credential works, by making one request to the service it is for; stores nothing.",
	"GET /api/tasks":                                                       "List tasks, with their state, position in the queue and pull request; narrow with state, q, limit and fields=summary rather than reading every task's full record.",
	"GET /api/pipeline/changes":                                            "Read what changed in the pipeline since a cursor: task transitions, merge-queue states, stalls, base-branch verdicts and merge-batch results, oldest first, with the cursor to read on from.",
	"POST /api/tasks":                                                      "File a new task: its title, description, repo and base branch, grants, dependencies and attachments.",
	"POST /api/tasks/from-template":                                        "File a task from a repository's own declared template (.grain/templates/<template>.yml), by its stem, with its fields filled; an agent's call is also held to POST /api/tasks's rules.",
	"POST /api/tasks/reorder":                                              "Move tasks in the queue: ids (in order) go after afterId and before beforeId; give only beforeId as the first queued task to move them to the front.",
	"GET /api/tasks/{id}":                                                  "Read one task in full: its fields, grants, conversation and runs.",
	"PATCH /api/tasks/{id}":                                                "Edit a task's title, description, priority, base branch, auto-merge or grants -- the same edit the task page makes.",
	"POST /api/tasks/{id}/capabilities":                                    "Attach a capability to a task or detach one (attach false), with its arguments; replace swaps a grant out for another.",
	"POST /api/tasks/{id}/depends-on":                                      "Make a task wait on another task (attach true), or stop waiting on it (attach false).",
	"POST /api/tasks/{id}/approve":                                         "Approve a proposed task, which puts it in the queue.",
	"POST /api/tasks/{id}/withdraw-approval":                               "Take a queued task back out of the queue, leaving it a proposal again without closing it.",
	"POST /api/tasks/{id}/defer":                                           "Put a task aside until deferredUntil (an RFC 3339 time), or indefinitely without one.",
	"POST /api/tasks/{id}/undefer":                                         "Pick a deferred task back up now.",
	"POST /api/tasks/{id}/submit":                                          "Submit a task's pull request to the merge queue, to be brought up to date and merged.",
	"POST /api/tasks/{id}/change-mail":                                     "Mail a task's CL to its reviewers. A person's call only: agents are refused.",
	"POST /api/tasks/{id}/change-sync":                                     "Sync a task's CL to head. A person's call only: agents are refused, and it is refused while the task has a run going.",
	"POST /api/tasks/{id}/change-publish":                                  "Publish the replies a task's runs left as drafts on its CLs. A person's call only: agents are refused.",
	"POST /api/tasks/{id}/comments":                                        "Comment on a task's conversation.",
	"PUT /api/tasks/{id}/secret":                                           "Answer a task's pending secret request with its value, which goes to the secret store and is never shown back.",
	"POST /api/tasks/{id}/close":                                           "Close a task.",
	"POST /api/tasks/{id}/stop":                                            "Stop a task's running attempt.",
	"POST /api/tasks/{id}/pause":                                           "Pause a task in any open state, releasing everything it holds and keeping its place; immediate true skips a running task's grace period.",
	"POST /api/tasks/{id}/resume":                                          "Resume a paused task, back to exactly the state and dispatch standing it had.",
	"PUT /api/tasks/{id}/goal":                                             "Set a task's goal -- {kind, spec, on_met, on_limit}, as propose_task's goal -- replacing any it had; grain closes or rests the task when it is met.",
	"DELETE /api/tasks/{id}/goal":                                          "Remove a task's goal, making it an ordinary task again.",
	"POST /api/tasks/{id}/reopen":                                          "Reopen a closed task.",
	"POST /api/tasks/{id}/retry":                                           "Queue a task for another attempt.",
	"POST /api/tasks/{id}/retry-merge":                                     "Retry merging a task's pull request.",
	"POST /api/tasks/{id}/force-merge":                                     "Merge a task's pull request even though the merge queue will not, recording reason; head pins the commit to merge.",
	"POST /api/tasks/{id}/run-now":                                         "Dispatch a queued task now, ahead of the queue.",
	"POST /api/tasks/{id}/pull-request":                                    "Open the pull request for a task's pushed branch now, rather than when its run ends.",
	"POST /api/tasks/{id}/sandbox/recreate":                                "Destroy a task's sandbox and build a fresh one in its place; uncommitted work in it is lost.",
	"POST /api/sandbox-probe":                                              "Build a throwaway sandbox named probe-<id>, run argv in it over the deployment's guest-access channel (access: dialout or kubectl), return its output and exit code, and delete it.",
	"POST /api/tasks/{id}/messages":                                        "Say something to the human watching your own task and park it on their reply (the chat tool's write half); only your own task's id is accepted. Give summary too: one line of at most 100 characters naming the decision you need, which is all the inbox and task list show of it. To message another task's agent, use POST /api/tasks/{id}/agent-messages.",
	"POST /api/tasks/{id}/agent-messages":                                  "Send a message to another task's agent, signed as your task: its live run gets it on its next tool result, or its next run reads it; the task is never parked or re-queued by it.",
	"POST /api/tasks/{id}/notes":                                           "Leave a note for your parent's or a child's agent (message_task): it never parks that task, reaches its live run, and wakes it if it is a resting durable task. Any other task needs api:task-admin.",
	"POST /api/notifications":                                              "Raise an operator notification, or bump the open one with the same subject; it never parks anybody.",
	"POST /api/notifications/{id}/clear":                                   "Clear an operator notification.",
	"POST /api/tasks/{id}/wait-on":                                         "Make a task wait for the tasks in ids to close; park true ends its run and resumes it when they have.",
	"POST /api/tasks/{id}/park-until":                                      "Park a task until a time (until, RFC 3339), when it is queued again.",
	"POST /api/tasks/{id}/resume-later":                                    "Stop a task's run and queue it again when a condition is met (when: tasks, at, ci, message).",
	"PUT /api/memory":                                                      "Replace the memory grain keeps for the deployment, or for one repo when repo is given.",
	"PUT /api/memory/documents":                                            "Write one named memory document, for the deployment or for one repo.",
	"PUT /api/release-notes":                                               "Write the release notes for a week (period; the week just ended without one).",
	"POST /api/repos":                                                      "Add a repository to the deployment's targets.",
	"DELETE /api/repos/{owner}/{name}":                                     "Remove a repository from the deployment's targets.",
	"PUT /api/repos/{owner}/{name}/ci-capabilities":                        "Set the capabilities a repository's grain CI jobs are granted.",
	"PUT /api/repos/{owner}/{name}/release":                                "Turn a repository's release workflow on or off, and set the jobs that judge its base branches and are dispatched after a merge.",
	"PUT /api/repos/{owner}/{name}/release-secrets/{secret}":               "Set the value of one of a repository's release secrets.",
	"DELETE /api/repos/{owner}/{name}/release-secrets/{secret}":            "Delete one of a repository's release secrets.",
	"POST /api/repos/{owner}/{name}/release-runs":                          "Start a release run of a repository, at a branch, ref or commit, optionally only some jobs.",
	"POST /api/repos/{owner}/{name}/pending-releases/{version}/approve":    "Approve a pending trunk release (vX.Y.0), making it official. Only a person may; an agent is refused.",
	"POST /api/repos/{owner}/{name}/pending-releases/{version}/reject":     "Reject a pending trunk release (vX.Y.0), dropping its release candidate. Only a person may; an agent is refused.",
	"POST /api/repos/{owner}/{name}/base-branch-runs":                      "Start a grain CI run of one of a repository's base branches.",
	"PUT /api/repos/{owner}/{name}/work-ownership":                         "Set who owns a repository's work: its default, or one work item's override.",
	"PUT /api/repos/{owner}/{name}/capabilities":                           "Set the capabilities every task on a repository is granted by default.",
	"PUT /api/repos/{owner}/{name}/prompt-extension":                       "Set the text added to every agent prompt for work on a repository.",
	"PUT /api/repos/{owner}/{name}/setup-command":                          "Set the command a sandbox runs to set up a repository's checkout.",
	"PUT /api/repos/{owner}/{name}/declarations":                           "Set whether grain reads a repository's own declarations from its tree.",
	"PUT /api/repos/{owner}/{name}/require-branch-up-to-date":              "Set whether a repository's pull requests must be up to date with their base before they merge.",
	"PUT /api/repos/{owner}/{name}/ignore-github-ci":                       "Set whether the merge queue ignores a repository's GitHub checks.",
	"PUT /api/repos/{owner}/{name}/ci-mode":                                "Set which CI gates a repository's merges: grain, github or none.",
	"PUT /api/repos/{owner}/{name}/settings":                               "Change several of a repository's settings at once: only the fields given are changed.",
	"PUT /api/repos/{owner}/{name}/watch-default-branch":                   "Set whether grain watches a repository's default branch and repairs it when its CI goes red.",
	"PUT /api/repos/{owner}/{name}/merge-on-green":                         "Set whether the merge queue merges a repository's green, clean pull request without waiting behind entries whose CI is still running.",
	"PUT /api/repos/{owner}/{name}/base-repair-bypasses-human-gates":       "Set whether a repository's base-branch repair pull requests merge without a human once their CI is green while the base branch is red.",
	"PUT /api/repos/{owner}/{name}/unfixable-checks":                       "Set the checks on a repository that an agent cannot fix, so a failure of one does not hold a merge on the agent.",
	"PUT /api/repos/{owner}/{name}/workflows":                              "Set a repository's grain CI workflows.",
	"POST /api/repos/{owner}/{name}/lifecycle/refresh":                     "Re-read a repository's release lifecycle from the repository now.",
	"POST /api/repos/{owner}/{name}/import-issues":                         "Import a repository's open GitHub issues as tasks now, as its issue importer would on its next poll: one task per issue not already imported, unapproved unless the repository's settings say approved.",
	"GET /api/repos/{owner}/{name}/releases":                               "List a repository's releases.",
	"POST /api/repos/{owner}/{name}/releases":                              "Create a release of a repository, by name.",
	"DELETE /api/repos/{owner}/{name}/releases/{release}":                  "Delete a release.",
	"POST /api/repos/{owner}/{name}/releases/{release}/merge":              "Ask for a release's branch to be merged back into the repository's default branch.",
	"GET /api/repos/{owner}/{name}/releases/{release}/candidates":          "List a release's candidates.",
	"POST /api/repos/{owner}/{name}/releases/{release}/candidates":         "Cut a release candidate of a release.",
	"POST /api/repos/{owner}/{name}/releases/{release}/candidates/promote": "Promote a release's current candidate.",
	"POST /api/repos/{owner}/{name}/branches":                              "Create a branch on a repository, named name, from the branch or commit in from.",
	"PUT /api/repos/{owner}/{name}/qualification-plan":                     "Set the plan a repository's release candidates are qualified by: its items, whether a run needs approval, and whether a qualified candidate is promoted.",
	"POST /api/repos/{owner}/{name}/candidates/{id}/qualification/approve": "Approve every task of a release candidate's qualification run in one action.",
	"GET /api/schedules":                                                   "List schedules.",
	"POST /api/schedules":                                                  "Create a schedule, filing work from a template or suite on a recurrence.",
	"PATCH /api/schedules/{id}":                                            "Edit a schedule: only the fields given are changed; revision guards against a concurrent edit.",
	"POST /api/schedules/{id}/run":                                         "Run a schedule now (the schedules page's Run now), filing what it would file at its next firing.",
	"POST /api/schedules/composition":                                      "Draft a schedule without saving it: what it would file at its next firing.",
	"DELETE /api/schedules/{id}":                                           "Delete a schedule.",
	"POST /api/templates":                                                  "Create a task template.",
	"PATCH /api/templates/{id}":                                            "Edit a task template: only the fields given are changed; revision guards against a concurrent edit.",
	"DELETE /api/templates/{id}":                                           "Delete a task template.",
	"POST /api/suites":                                                     "Create a suite of templates, filed together.",
	"PATCH /api/suites/{id}":                                               "Edit a suite: only the fields given are changed; revision guards against a concurrent edit.",
	"POST /api/suites/composition":                                         "Draft a suite without saving it: the tasks it would file, for query repo and base.",
	"DELETE /api/suites/{id}":                                              "Delete a suite.",
	"POST /api/suite-runs":                                                 "Run a suite now against a repo and base branch, filing its tasks.",
	"POST /api/boards":                                                     "Create a board.",
	"PATCH /api/boards/{id}":                                               "Edit a board: only the fields given are changed.",
	"DELETE /api/boards/{id}":                                              "Delete a board.",
	"PUT /api/deployment":                                                  "Change the deployment's default model, per-model settings and target repositories.",
	"POST /api/deployment/git-connections":                                 "Add a git connection to the deployment: the credential grain reaches a git host with.",
	"DELETE /api/deployment/git-connections/{cred}":                        "Remove one of the deployment's git connections.",
	"POST /api/deployment/git-connections/check":                           "Check a git connection works, without saving it.",
	"PUT /api/deployment/agent-backends/{backendId}":                       "Create or replace one of the deployment's model connections.",
	"DELETE /api/deployment/agent-backends/{backendId}":                    "Delete one of the deployment's model connections.",
	"PUT /api/deployment/repos/{owner}/{name}/config":                      "Set a repository's deployment-level configuration: only the fields given are changed.",
	"PUT /api/deployment/repos/{owner}/{name}/git-connection":              "Set which git connection a repository is reached through.",
	"POST /api/deployment/broadcast":                                       "Post a comment to every active task (running_only: only those with a run in flight).",
	"POST /api/executors/tokens":                                           "Create a token a remote executor registers with; the token is shown once, in the answer.",
	"POST /api/executors/tokens/{id}/revoke":                               "Revoke an executor token.",
	"DELETE /api/executors/tokens/{id}":                                    "Revoke an executor token.",
	"DELETE /api/executors/tokens/{id}/revoke":                             "Revoke an executor token.",
	"PUT /api/agent-keys/{framework}":                                      "Set an agent framework's credential.",
	"DELETE /api/agent-keys/{framework}":                                   "Clear an agent framework's credential.",
	"PUT /api/agent-backends/{id}/credential":                              "Set a model connection's credential.",
	"DELETE /api/agent-backends/{id}/credential":                           "Clear a model connection's credential.",
	"PUT /api/agent-backends/{id}/codex-auth":                              "Set a model connection's Codex login (the contents of its auth.json).",
	"DELETE /api/agent-backends/{id}/codex-auth":                           "Clear a model connection's Codex login.",
	"PUT /api/codex-auth":                                                  "Set the deployment-wide Codex login (the contents of its auth.json).",
	"DELETE /api/codex-auth":                                               "Clear the deployment-wide Codex login grain manages.",
	"POST /api/agent-keys/{framework}/check":                               "Check an agent framework's credential works, by making one request with it; stores nothing.",
	"POST /api/capability-grants/check":                                    "Check a list of grants: whether each names a capability this deployment has, and what it would need.",
	"PUT /api/capability-servers":                                          "Create or replace a capability server's configuration (renameFrom renames one).",
	"POST /api/capability-servers/remove":                                  "Remove a capability server.",
	"POST /api/capability-servers/enable":                                  "Enable a capability server.",
	"POST /api/capability-servers/disable":                                 "Disable a capability server.",
	"POST /api/capability-servers/dismiss-notice":                          "Clear a capability server's one-time notice.",
	"POST /api/capability-servers/rotate-managed":                          "Replace the secrets grain manages for a capability server (its data-plane token) and attach it again.",
	"POST /api/capability-servers/bind-secret":                             "Bind a secret to a capability server, storing its value when one is given.",
	"POST /api/capability-servers/test":                                    "Test a capability server configuration, without saving it.",
	"POST /api/capability-servers/test-script":                             "Run a capability server's host script once, with argv, to test it.",
	"POST /api/capability-servers/gchat-test":                              "Post a marked test message to one configured space of a gchat capability server.",
	"GET /api/capability-servers/gchat-usage":                              "Read each configured space's post counts on a gchat capability server.",
	"PUT /api/capability-policy":                                           "Set the deployment's capability policy.",
	"GET /api/api-scope-sets":                                              "List the agent API's scope sets, built-in and declared, with the scopes each resolves to.",
	"GET /api/api-scope-sets/usage":                                        "List every open task, template, schedule and workflow job holding each API scope set.",
	"PUT /api/api-scope-sets":                                              "Declare or replace one of the deployment's API scope sets (written to the config repository; grants it to nobody).",
	"POST /api/api-scope-sets/delete":                                      "Remove one of the deployment's API scope sets.",
	"POST /api/capability-policy/preview":                                  "Preview a capability policy without saving it: what it would allow and refuse.",
	"POST /api/gcp-keys":                                                   "Create a GCP key: a named service-account credential tasks can be granted.",
	"PUT /api/gcp-keys/{name}":                                             "Create or replace a named GCP key.",
	"DELETE /api/gcp-keys/{name}":                                          "Delete a named GCP key.",
	"PUT /api/github-tokens/{name}":                                        "Set a named GitHub token.",
	"DELETE /api/github-tokens/{name}":                                     "Delete a named GitHub token; refused while a git connection still names it.",
	"PUT /api/secrets/{secret}/{key}":                                      "Set one key of a secret; the value is stored and never shown back.",
	"DELETE /api/secrets/{secret}/{key}":                                   "Delete one key of a secret.",
	"DELETE /api/secrets/{secret}":                                         "Delete a secret and every key in it.",
	"POST /api/state-repo/sync":                                            "Sync the deployment's state repository now.",
	"POST /api/state-repo/pull-requests":                                   "Open a validated, held grain/config pull request carrying the change a refused configuration write returned (a person only).",
	"POST /api/state-repo/remote":                                          "Name the external git repository the deployment reads its configuration from, or none: true to go back to the internal one.",
	"POST /api/state-repo/secrets-key":                                     "Install the key the deployment's secrets file is sealed to.",
	"POST /api/state-repo/import-key":                                      "Install the key the deployment's secrets file is sealed to.",
	"POST /api/state-repo/secrets-repo":                                    "Name the git repository the deployment's encrypted secrets file is backed up to, or none: true for none.",
	"POST /api/state-repo/reset-notice":                                    "Dismiss the notice of what a state-repository merge took away.",
	"POST /api/host/reboot":                                                "Reboot the machine grain runs on.",
	"POST /api/host/shutdown":                                              "Power off the machine grain runs on; it does not come back on its own.",
	"POST /api/host/restart":                                               "Restart the grain service, without touching the machine it runs on.",
	"POST /api/upgrade":                                                    "Start or schedule upgrading grain to a branch or image; set afterTasksFinish: true to drain running tasks and CI jobs first (your own run counts, so finish rather than polling), as false restarts grain immediately and discards in-flight work.",
	"DELETE /api/upgrade/scheduled":                                        "Cancel an upgrade waiting for running tasks to finish.",
	"POST /api/database/test":                                              "Check grain can connect to a database DSN.",
	"POST /api/database/upgrade":                                           "Move grain's store to a Postgres DSN; the first call answers with a confirmation token, which a second identical call must carry.",
	"POST /api/upgrade/postgres":                                           "Move grain's store to a Postgres DSN, as POST /api/database/upgrade does.",
	"POST /api/settings/db/migrate-to-postgres":                            "Move grain's store to a Postgres DSN, as POST /api/database/upgrade does.",
	"POST /api/persistent-sandboxes/{name}/{action}":                       "Act on a persistent sandbox: action is stop, start, restart or rebuild.",
	"DELETE /api/pause":                                                    "Lift agent pauses: those of a connection (query connection, and model), of a team (team, and framework), or every pause the caller may lift.",
	"POST /api/console/{id}/input":                                         "Type input into an interactive console session.",
	"POST /api/console/{id}/resize":                                        "Tell an interactive console session the size of the window showing it.",
	"DELETE /api/console/{id}":                                             "Close an interactive console session, ending its run.",
	"POST /api/console-sessions":                                           "Start a standing chat session.",
	"PUT /api/console-sessions/{id}":                                       "Edit a standing chat session's title, model and grants.",
	"DELETE /api/console-sessions/{id}":                                    "Delete a standing chat session.",
	"POST /api/console-sessions/{id}/restart":                              "Restart a standing chat session; carryHandover carries its handover into the new one.",
	"POST /api/console-sessions/{id}/cancel":                               "Stop a standing chat session's in-flight or queued turn.",
	"POST /api/ci/runs":                                                    "Start a grain CI run: for a task (taskId), or a repo and ref; recheck with recheckFailed re-runs only the failed jobs, fresh.",
	"GET /api/ci/runs":                                                     "List grain CI runs, newest first; narrow with query repo, ref, conclusion, job, since, until, limit; view=compact for one short entry per run naming its failing jobs.",
	"GET /api/ci/runs/{id}":                                                "Read one grain CI run in full, every job's steps and their log output included.",
	"DELETE /api/ci/runs/{id}":                                             "Cancel a grain CI run.",
	"DELETE /api/ci/cache":                                                 "Empty grain CI's job cache, so every job of the next runs does its work again.",
	"POST /api/tests/quarantine":                                           "Quarantine a flaky or failing test (returning the rendered .grain/quarantine.yml entry to commit in the workspace) and optionally file a held follow-up repair task.",
	"POST /api/package-cache/flush":                                        "Empty the package cache: query kind (apt, pypi, docker, npm or go) for one ecosystem, or all of them without.",
	"POST /api/actions-cache/flush":                                        "Empty the actions cache: query repo (owner/name) for one repository's entries, or every repository's without.",
	"POST /api/pipeline-pause":                                             "Pause the pipeline: nothing new is dispatched until it is resumed.",
	"DELETE /api/pipeline-pause":                                           "Resume the paused pipeline.",
	"GET /api/external-agents/tokens":                                      "List external agent credentials (metadata only; secret tokens are never shown after issue).",
	"POST /api/external-agents/tokens":                                     "Issue a scoped external agent credential for /mcp.",
	"POST /api/external-agents/tokens/{id}/rotate":                         "Rotate an external agent credential, returning a new secret token once and invalidating the previous token immediately.",
	"POST /api/external-agents/tokens/{id}/revoke":                         "Revoke an external agent credential immediately.",
	"DELETE /api/external-agents/tokens/{id}":                              "Delete an external agent credential record.",
	"POST /api/external-agents/tokens/revoke-all":                          "Emergency kill switch: revoke every active external agent credential immediately.",
}

// routeBodies is the request type a route reads its body into, which
// its tool describes field by field: the type of the `var req X` its
// handler hands to readJSON. Every route that reads a body has one, and
// a route with none takes no body.
var routeBodies = map[string]any{
	"PUT /api/settings":                                              UpdateSettingsRequest{},
	"POST /api/settings":                                             UpdateSettingsRequest{},
	"POST /api/settings/plan":                                        UpdateSettingsRequest{},
	"PUT /api/settings/cli-run":                                      updateCLIRunSettingsRequest{},
	"POST /api/tasks":                                                CreateTaskRequest{},
	"POST /api/tasks/from-template":                                  FromTemplateRequest{},
	"POST /api/tasks/reorder":                                        ReorderRequest{},
	"PATCH /api/tasks/{id}":                                          UpdateTaskRequest{},
	"POST /api/tasks/{id}/capabilities":                              setCapabilityRequest{},
	"POST /api/tasks/{id}/depends-on":                                setDependencyRequest{},
	"POST /api/tasks/{id}/defer":                                     deferRequest{},
	"POST /api/sandbox-probe":                                        SandboxProbeRequest{},
	"POST /api/tasks/{id}/pause":                                     pauseRequest{},
	"PUT /api/tasks/{id}/goal":                                       setGoalRequest{},
	"POST /api/tasks/{id}/run-now":                                   runNowRequest{},
	"POST /api/tasks/{id}/comments":                                  addCommentRequest{},
	"POST /api/tasks/{id}/change-mail":                               MailChangeRequest{},
	"PUT /api/tasks/{id}/secret":                                     setPendingSecretRequest{},
	"POST /api/tasks/{id}/close":                                     closeRequest{},
	"POST /api/tasks/{id}/reopen":                                    reopenRequest{},
	"POST /api/tasks/{id}/force-merge":                               forceMergeRequest{},
	"POST /api/tasks/{id}/messages":                                  messageRequest{},
	"POST /api/tasks/{id}/agent-messages":                            agentMessageRequest{},
	"POST /api/tasks/{id}/notes":                                     NoteRequest{},
	"POST /api/notifications":                                        RaiseNotificationRequest{},
	"POST /api/tasks/{id}/wait-on":                                   waitOnRequest{},
	"POST /api/tasks/{id}/park-until":                                parkUntilRequest{},
	"POST /api/tasks/{id}/resume-later":                              resumeLaterRequest{},
	"PUT /api/memory":                                                memoryRequest{},
	"PUT /api/memory/documents":                                      memoryDocumentRequest{},
	"PUT /api/release-notes":                                         releaseNoteRequest{},
	"POST /api/repos":                                                AddRepoRequest{},
	"PUT /api/repos/{owner}/{name}/ci-capabilities":                  setRepoCICapabilitiesRequest{},
	"PUT /api/repos/{owner}/{name}/release":                          setRepoReleaseRequest{},
	"PUT /api/repos/{owner}/{name}/release-secrets/{secret}":         setRepoReleaseSecretRequest{},
	"POST /api/repos/{owner}/{name}/release-runs":                    RepoReleaseRunRequest{},
	"POST /api/repos/{owner}/{name}/base-branch-runs":                BaseBranchRunRequest{},
	"PUT /api/repos/{owner}/{name}/work-ownership":                   SetRepoWorkOwnershipRequest{},
	"PUT /api/repos/{owner}/{name}/capabilities":                     SetRepoCapabilitiesRequest{},
	"PUT /api/repos/{owner}/{name}/prompt-extension":                 SetRepoPromptExtensionRequest{},
	"PUT /api/repos/{owner}/{name}/setup-command":                    SetRepoSetupCommandRequest{},
	"PUT /api/repos/{owner}/{name}/declarations":                     SetRepoDeclarationsRequest{},
	"PUT /api/repos/{owner}/{name}/require-branch-up-to-date":        SetRepoRequireBranchUpToDateRequest{},
	"PUT /api/repos/{owner}/{name}/ignore-github-ci":                 SetRepoIgnoreGitHubCIRequest{},
	"PUT /api/repos/{owner}/{name}/ci-mode":                          SetRepoCIModeRequest{},
	"PUT /api/repos/{owner}/{name}/settings":                         SetRepoSettingsRequest{},
	"PUT /api/repos/{owner}/{name}/watch-default-branch":             SetRepoWatchDefaultBranchRequest{},
	"PUT /api/repos/{owner}/{name}/merge-on-green":                   SetRepoMergeOnGreenRequest{},
	"PUT /api/repos/{owner}/{name}/base-repair-bypasses-human-gates": SetRepoBaseRepairBypassesHumanGatesRequest{},
	"PUT /api/repos/{owner}/{name}/unfixable-checks":                 SetRepoUnfixableChecksRequest{},
	"PUT /api/repos/{owner}/{name}/workflows":                        SetRepoWorkflowsRequest{},
	"POST /api/repos/{owner}/{name}/releases":                        CreateReleaseRequest{},
	"POST /api/repos/{owner}/{name}/branches":                        CreateBranchRequest{},
	"PUT /api/repos/{owner}/{name}/qualification-plan":               PutQualificationPlanRequest{},
	"POST /api/schedules":                                            CreateScheduleRequest{},
	"PATCH /api/schedules/{id}":                                      UpdateScheduleRequest{},
	"POST /api/schedules/composition":                                CreateScheduleRequest{},
	"POST /api/templates":                                            CreateTemplateRequest{},
	"PATCH /api/templates/{id}":                                      UpdateTemplateRequest{},
	"POST /api/suites":                                               CreateSuiteRequest{},
	"PATCH /api/suites/{id}":                                         UpdateSuiteRequest{},
	"POST /api/suites/composition":                                   CreateSuiteRequest{},
	"POST /api/suite-runs":                                           CreateSuiteRunRequest{},
	"POST /api/boards":                                               CreateBoardRequest{},
	"PATCH /api/boards/{id}":                                         UpdateBoardRequest{},
	"PUT /api/deployment":                                            UpdateDeploymentRequest{},
	"POST /api/deployment/git-connections":                           AddTeamGitConnectionRequest{},
	"POST /api/deployment/git-connections/check":                     CheckTeamGitConnectionRequest{},
	"PUT /api/deployment/agent-backends/{backendId}":                 putDeploymentAgentBackendRequest{},
	"PUT /api/deployment/repos/{owner}/{name}/config":                SetDeploymentRepoConfigRequest{},
	"PUT /api/deployment/repos/{owner}/{name}/git-connection":        SetTeamRepoGitConnectionRequest{},
	"POST /api/deployment/broadcast":                                 BroadcastTeamRequest{},
	"POST /api/executors/tokens":                                     CreateExecutorTokenRequest{},
	"PUT /api/agent-keys/{framework}":                                setAgentKeyRequest{},
	"PUT /api/agent-backends/{id}/credential":                        setAgentKeyRequest{},
	"PUT /api/agent-backends/{id}/codex-auth":                        setAgentKeyRequest{},
	"PUT /api/codex-auth":                                            setAgentKeyRequest{},
	"POST /api/capability-grants/check":                              checkGrantsRequest{},
	"PUT /api/capability-servers":                                    SetCapabilityServerRequest{},
	"POST /api/capability-servers/remove":                            CapabilityServerIDRequest{},
	"POST /api/capability-servers/enable":                            CapabilityServerIDRequest{},
	"POST /api/capability-servers/disable":                           CapabilityServerIDRequest{},
	"POST /api/capability-servers/dismiss-notice":                    CapabilityServerIDRequest{},
	"POST /api/capability-servers/rotate-managed":                    CapabilityServerIDRequest{},
	"POST /api/capability-servers/bind-secret":                       BindCapabilityServerSecretRequest{},
	"POST /api/capability-servers/test":                              TestCapabilityServerRequest{},
	"POST /api/capability-servers/test-script":                       TestHostScriptRequest{},
	"POST /api/capability-servers/gchat-test":                        TestGChatPostRequest{},
	"PUT /api/capability-policy":                                     CapabilityPolicyBody{},
	"PUT /api/api-scope-sets":                                        PutAPIScopeSetRequest{},
	"POST /api/api-scope-sets/delete":                                DeleteAPIScopeSetRequest{},
	"POST /api/capability-policy/preview":                            CapabilityPolicyBody{},
	"POST /api/gcp-keys":                                             SetGCPKeyRequest{},
	"PUT /api/gcp-keys/{name}":                                       SetGCPKeyRequest{},
	"PUT /api/github-tokens/{name}":                                  setGitHubTokenRequest{},
	"PUT /api/secrets/{secret}/{key}":                                setSecretRequest{},
	"POST /api/state-repo/pull-requests":                             ConfigPullRequestRequest{},
	"POST /api/state-repo/remote":                                    stateRepoRemoteRequest{},
	"POST /api/state-repo/secrets-key":                               secretsKeyRequest{},
	"POST /api/state-repo/import-key":                                secretsKeyRequest{},
	"POST /api/state-repo/secrets-repo":                              secretsRepoRequest{},
	"POST /api/upgrade":                                              startUpgradeRequest{},
	"POST /api/database/test":                                        testDatabaseRequest{},
	"POST /api/database/upgrade":                                     upgradeDatabaseRequest{},
	"POST /api/upgrade/postgres":                                     upgradeDatabaseRequest{},
	"POST /api/settings/db/migrate-to-postgres":                      upgradeDatabaseRequest{},
	"POST /api/console/{id}/input":                                   consoleInputRequest{},
	"POST /api/console/{id}/resize":                                  consoleResizeRequest{},
	"POST /api/console-sessions":                                     CreateConsoleSessionRequest{},
	"PUT /api/console-sessions/{id}":                                 UpdateConsoleSessionRequest{},
	"POST /api/console-sessions/{id}/restart":                        RestartConsoleSessionRequest{},
	"POST /api/ci/runs":                                              CIRunRequest{},
	"DELETE /api/ci/runs/{id}":                                       CICancelRequest{},
	"POST /api/tests/quarantine":                                     QuarantineTestRequest{},
	"POST /api/pipeline-pause":                                       pausePipelineRequest{},
	"POST /api/external-agents/tokens":                               CreateExternalAgentTokenRequest{},
}

// routeQueries is the query parameters a route's handler reads, each
// with what it means: its tool describes them as properties of query.
// A route with none takes no query of its own, though the query object
// still carries any parameter a caller adds (a repo= the agent API's
// gate reads, a template composition's field.<name>).
var routeQueries = map[string]map[string]string{
	"GET /api/restart-watch": {
		"quick": "1 to answer at once with whether grain is restarting, rather than holding the request until it is or 25 s pass.",
	},
	"GET /api/pipeline/changes": {
		"after": "The cursor a previous read returned as next (<RFC 3339 time>~<seq>); omitted, the last 24 hours.",
		"limit": "At most this many entries (default 100, at most 500).",
		"repo":  "Only this repository's tasks, base branches and merge batches (owner/name); a repository-scoped tasks:read grant must name its own.",
	},
	"GET /api/tasks": {
		"repo":   "Only this repository's (owner/name) tasks; a repository-scoped tasks:read grant must name its own.",
		"state":  "Only tasks in these states, repeated or comma-separated (proposed, queued, running, awaiting_reply, failed, awaiting_submit, completed, closed).",
		"q":      "Only tasks whose title contains this text, case-insensitively.",
		"search": "The same as q.",
		"limit":  "At most this many tasks: the newest by filing time, listed oldest first.",
		"fields": "summary for id, title, state, repo, createdAt, blockedBy and stall only -- no description; use it when the full records are not needed.",
	},
	"GET /api/tasks/states": {"limit": "At most this many states (default 1000).", "cursor": "The nextCursor of the previous page.", "since": "An older spelling of cursor."},
	"GET /api/states":       {"limit": "At most this many states (default 1000).", "cursor": "The nextCursor of the previous page.", "since": "An older spelling of cursor."},
	"GET /api/tasks/{id}/messages": {
		"after":   "Only messages after this comment id.",
		"agents":  "true to include other tasks' runs' messages to this task's agent (POST /api/tasks/{id}/agent-messages).",
		"wait_ms": "Block up to this many milliseconds (at most 5000) for a new message to arrive when none is already waiting after the cursor.",
	},
	"GET /api/capability-servers/gchat-usage":   {"id": "The gchat capability server's capability_id."},
	"GET /api/capability-methods":               {"kind": "The run kind the grants are for (task, process or ci): each method then carries policyRefusal, why the capability policy denies it that run.", "repo": "With kind, the repository (owner/name) the run is on."},
	"GET /api/notifications":                    {"open": "1 for only notifications not yet cleared.", "after": "Only notifications after this id.", "family": "Only this family (the subject before its first ':')."},
	"GET /api/memory/documents/{name}":          {"repo": "The repository (owner/name) whose document to read; the deployment's without one."},
	"GET /api/release-notes/stats":              {"period": "The week whose figures to read; the week just ended without one."},
	"POST /api/repos":                           {"team": "The team whose targets to add to; the default team without one."},
	"DELETE /api/repos/{owner}/{name}":          {"team": "The team whose targets to remove from; the default team without one."},
	"GET /api/repos/{owner}/{name}/compare":     {"base": "The branch or commit to compare from.", "head": "The branch or commit to compare to."},
	"GET /api/templates/{id}/composition":       {"repo": "The repository (owner/name) the composition is for.", "base": "The base branch the composition is for.", "approved": "false to compose the tasks unapproved."},
	"GET /api/suites/{id}/composition":          {"repo": "The repository (owner/name) the composition is for.", "base": "The base branch the composition is for."},
	"POST /api/suites/composition":              {"repo": "The repository (owner/name) the composition is for.", "base": "The base branch the composition is for."},
	"GET /api/deployment/audit-logs":            {"limit": "At most this many entries."},
	"GET /api/agent-models":                     {"framework": "Only this framework's models.", "backend": "Only this model connection's models."},
	"GET /api/state-repo/file":                  {"path": "The file's path in the state repository.", "limit": "At most this many bytes of it."},
	"GET /api/host/top":                         {"lines": "At most this many processes."},
	"GET /api/upgrade/release-notes":            {"branch": "The branch whose notes to read."},
	"GET /api/logs/{source}":                    {"lines": "At most this many of the newest lines."},
	"GET /api/persistent-sandboxes/{name}/logs": {"format": "text for the logs as plain text rather than JSON."},
	"GET /api/metrics": {
		"window":  "How far back: a duration (36h) or a count of days or weeks (7d, 2w).",
		"since":   "The report's start: a date or an RFC 3339 time.",
		"until":   "The report's end: a date or an RFC 3339 time.",
		"buckets": "How many buckets to divide the report's time series into.",
		"section": "Only this section of the report.",
		"author":  "Only work by this author; comma-separated or repeated for several.",
		"repo":    "Only work on this repository (owner/name); comma-separated or repeated for several.",
	},
	"GET /api/unmatched-failures":  {"window": "How far back: a duration (36h) or a count of days or weeks (7d, 2w).", "limit": "At most this many failures."},
	"DELETE /api/pause":            {"connection": "Lift this model connection's pauses.", "model": "With connection, only this model's pause on it.", "team": "Lift this team's pauses.", "framework": "With team, only this framework's pause."},
	"GET /api/console/{id}/output": {"offset": "Only output after this byte offset."},
	"GET /api/ci/workflows":        {"repo": "The repository (owner/name) whose workflows to list.", "ref": "The branch or commit to read them at."},
	"GET /api/ci/runs": {
		"repo":       "Only runs of this repository (owner/name).",
		"ref":        "Only runs of a branch matching this, by any part of its name.",
		"conclusion": "Only runs ending so: failure, errored, cancelled, skipped, success, running or pending; comma-separated for several.",
		"job":        "Only runs with this job, by name or id.",
		"since":      "How far back: a span (24h, 7d) or a date.",
		"until":      "Only runs started before this: a span back from now (24h, 7d) or a date; with since, a window.",
		"view":       "compact for each run's identity, conclusion and a line per job that did not succeed -- no steps, faults named once by digest; full (the default) for the whole listing.",
		"sort":       "Order by started, finished or duration.",
		"order":      "asc or desc (the default).",
		"limit":      "At most this many runs.",
	},
	"GET /api/merge-batches":        {"repo": "Only this repository's (owner/name) overlap re-checks."},
	"POST /api/package-cache/flush": {"kind": "Empty only this ecosystem's cache: apt, pypi, docker, npm or go."},
	"POST /api/actions-cache/flush": {"repo": "Empty only this repository's (owner/name) entries."},
}

var (
	apiRoutesOnce sync.Once
	apiRoutes     []APIRoute
)

// AgentAPIRoutes is every route of the agent API, in registration order:
// all of Server's routes but grain's internal plumbing, each with the
// tool it is served as.
func AgentAPIRoutes() []APIRoute {
	apiRoutesOnce.Do(func() {
		s := NewServerWithClient(nil)
		taken := map[string]bool{}
		for _, pattern := range s.patterns {
			scope := routeScopes[pattern]
			if scope == "" || scope == scopeInternal {
				continue
			}
			method, path, ok := strings.Cut(pattern, " ")
			if !ok {
				continue
			}
			route := APIRoute{Pattern: pattern, Method: method, Path: path, Scope: scope,
				NeedsReason: IsDangerousAPIScope(scope) || method == http.MethodDelete,
				body:        routeBodies[pattern], query: routeQueries[pattern]}
			for _, m := range pathParam.FindAllStringSubmatch(path, -1) {
				route.Params = append(route.Params, m[1])
			}
			route.Tool = uniqueToolName(toolNameFor(s.handlers[pattern], method, path), method, path, taken)
			route.Description = routeDescription(route, s.handlers[pattern])
			apiRoutes = append(apiRoutes, route)
		}
	})
	return slices.Clone(apiRoutes)
}

// AgentAPIToolNames is every generated tool's name, for the frameworks'
// allowed-tool lists (agent.PublishedToolNames).
func AgentAPIToolNames() []string {
	var names []string
	for _, r := range AgentAPIRoutes() {
		names = append(names, r.Tool)
	}
	return names
}

func toolNameFor(handler, method, path string) string {
	name := strings.TrimPrefix(handler, "handle")
	if name == "" {
		return APIToolPrefix + strings.ToLower(method) + "_" + pathWords(path)
	}
	return APIToolPrefix + snake(name)
}

// maxAPIToolName bounds a generated tool's bare name: a CLI qualifies it
// as mcp__grain-sandbox__<name> (mcp.QualifiedToolName), and a model
// API takes at most 64 characters for the whole of that.
const maxAPIToolName = 64 - len("mcp__"+mcp.ToolNamespace+"__")

// uniqueToolName settles two routes served by one handler (GET, PUT and
// DELETE of codex-auth, say) by their method, then by a number, and
// keeps every name within maxAPIToolName.
func uniqueToolName(name, method, _ string, taken map[string]bool) string {
	clip := func(s string, room int) string {
		if len(s) > room {
			return strings.TrimRight(s[:room], "_")
		}
		return s
	}
	for _, candidate := range []string{clip(name, maxAPIToolName),
		clip(name, maxAPIToolName-len(method)-1) + "_" + strings.ToLower(method)} {
		if !taken[candidate] {
			taken[candidate] = true
			return candidate
		}
	}
	for i := 2; ; i++ {
		suffix := fmt.Sprintf("_%d", i)
		candidate := clip(name, maxAPIToolName-len(suffix)) + suffix
		if !taken[candidate] {
			taken[candidate] = true
			return candidate
		}
	}
}

func pathWords(path string) string {
	var words []string
	for _, part := range strings.Split(strings.TrimPrefix(path, "/api/"), "/") {
		part = strings.Trim(part, "{}.")
		part = strings.ReplaceAll(part, "-", "_")
		if part != "" {
			words = append(words, strings.ToLower(part))
		}
	}
	return strings.Join(words, "_")
}

// snake is a Go identifier in snake case, an initialism kept as one
// word: StartCIRun is start_ci_run.
func snake(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		if unicode.IsUpper(r) {
			prevLower := i > 0 && !unicode.IsUpper(runes[i-1])
			endsInitialism := i > 0 && unicode.IsUpper(runes[i-1]) && i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if prevLower || endsInitialism {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

func routeDescription(r APIRoute, handler string) string {
	doc := routeDocs[r.Pattern]
	if doc == "" {
		words := strings.ReplaceAll(snake(strings.TrimPrefix(handler, "handle")), "_", " ")
		if words == "" {
			words = strings.ToLower(r.Method) + " " + r.Path
		}
		doc = strings.ToUpper(words[:1]) + words[1:] + "."
	}
	doc += fmt.Sprintf(" Calls %s through the same handler grain's UI uses (agent API scope %s).", r.Pattern, r.Scope)
	if r.NeedsReason {
		doc += " Destructive: reason is required and is recorded with the call."
	}
	return doc
}

// inputSchema is the route's tool's arguments: each path parameter, a
// reason, the query for a read and the body for a write.
func (r APIRoute) inputSchema() map[string]any {
	props := map[string]any{
		"reason": map[string]any{"type": "string",
			"description": "Why this call is being made; recorded with it, and required for a destructive call."},
	}
	required := []string{}
	for _, p := range r.Params {
		props[p] = map[string]any{"type": "string", "description": "The {" + p + "} of " + r.Path}
		required = append(required, p)
	}
	query := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"},
		"description": "Query parameters, as the UI would send them."}
	if len(r.query) > 0 {
		params := map[string]any{}
		for name, doc := range r.query {
			params[name] = map[string]any{"type": "string", "description": doc}
		}
		query["properties"] = params
	}
	props["query"] = query
	// Every route that reads a body declares its type in routeBodies
	// (TestEveryScopedRouteIsDescribed), so one with none takes no body.
	if r.body != nil && r.Method != http.MethodGet {
		body := jsonSchemaOf(reflect.TypeOf(r.body), 0)
		body["description"] = "The JSON request body."
		props["body"] = body
	}
	if r.takesExpect() {
		props["expect"] = map[string]any{"type": "object", "additionalProperties": false,
			"description": "What you believe about task {id} before changing it. The call reads the task first and " +
				"refuses, changing nothing, if any field given here disagrees with the record.",
			"properties": map[string]any{
				"state":        map[string]any{"type": "string", "description": "The state it must be in (queued, running, completed, ...)."},
				"title_prefix": map[string]any{"type": "string", "description": "What its title must start with (case-insensitive)."},
				"kind": map[string]any{"type": "string", "description": "A kind tag it must carry: " +
					strings.Join(TaskKinds, ", ") + ", or none for a task with no tag."},
			}}
	}
	if r.NeedsReason {
		required = append(required, "reason")
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

// takesExpect reports whether the route changes one task by its id -- the
// task-admin writes a judgement agent acts through -- and so takes an
// expect argument checked against that task before the call is made
// (docs/design/durable-tasks.md#DT-13).
func (r APIRoute) takesExpect() bool {
	return r.Method != http.MethodGet && strings.HasPrefix(r.Path, "/api/tasks/{id}") && slices.Contains(r.Params, "id")
}

// expectation reads the expect argument, and reports whether one was given.
func expectation(args map[string]any) (TaskExpectation, bool) {
	raw, ok := args["expect"].(map[string]any)
	if !ok {
		return TaskExpectation{}, false
	}
	word := func(key string) string {
		v, _ := raw[key].(string)
		return strings.TrimSpace(v)
	}
	e := TaskExpectation{State: word("state"), TitlePrefix: word("title_prefix"), Kind: word("kind")}
	return e, !e.Empty()
}

// jsonSchemaOf is a JSON Schema for t as encoding/json would read it.
func jsonSchemaOf(t reflect.Type, depth int) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if depth > 4 {
		return map[string]any{}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": jsonSchemaOf(t.Elem(), depth+1)}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": jsonSchemaOf(t.Elem(), depth+1)}
	case reflect.Struct:
		if t.PkgPath() == "time" {
			return map[string]any{"type": "string"}
		}
		props := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				if f.Anonymous {
					for k, v := range jsonSchemaOf(f.Type, depth+1)["properties"].(map[string]any) {
						props[k] = v
					}
					continue
				}
				name = f.Name
			}
			props[name] = jsonSchemaOf(f.Type, depth+1)
		}
		return map[string]any{"type": "object", "properties": props}
	}
	return map[string]any{}
}

// AgentAPITools is the tools of every route the grants reach: for a run
// of taskID, calling the daemon through client. Nil when the grants hold
// no api scope at all.
func AgentAPITools(client *HTTPClient, taskID string, grants []string) []mcp.Tool {
	set := APIGrantsOf(grants)
	if set.Empty() {
		return nil
	}
	var tools []mcp.Tool
	for _, route := range AgentAPIRoutes() {
		if !set.Has(route.Scope) && !tasksAdminAllowsTaskPatch(set, route.Pattern) {
			continue
		}
		tools = append(tools, route.tool(client, taskID))
	}
	return tools
}

func (r APIRoute) tool(client *HTTPClient, taskID string) mcp.Tool {
	return mcp.Tool{
		Name:        r.Tool,
		Description: r.Description,
		InputSchema: r.inputSchema(),
		Handler: func(ctx context.Context, args map[string]any) mcp.Result {
			text, err := r.call(ctx, client, taskID, args)
			if err != nil {
				return mcp.Result{Text: r.Tool + " failed: " + err.Error(), IsError: true}
			}
			return mcp.Result{Text: text}
		},
		ElisionAdvice: "narrow it with query parameters, or read one item by its id",
	}
}

// call makes the route's request on taskID's behalf.
func (r APIRoute) call(ctx context.Context, client *HTTPClient, taskID string, args map[string]any) (string, error) {
	if client == nil {
		return "", fmt.Errorf("this run has no grain daemon to call")
	}
	path := r.Path
	for _, p := range r.Params {
		v, _ := args[p].(string)
		v = strings.TrimSpace(v)
		if v == "" {
			return "", fmt.Errorf("%s is required", p)
		}
		escaped := url.PathEscape(v)
		path = strings.Replace(path, "{"+p+"...}", escaped, 1)
		path = strings.Replace(path, "{"+p+"}", escaped, 1)
	}
	reason, _ := args["reason"].(string)
	reason = strings.TrimSpace(reason)
	if r.NeedsReason && reason == "" {
		return "", fmt.Errorf("reason is required: %s is destructive", r.Pattern)
	}
	if expect, ok := expectation(args); ok && r.takesExpect() {
		id, _ := args["id"].(string)
		detail, err := getTaskAs(ctx, client, taskID, strings.TrimSpace(id))
		if err != nil {
			return "", fmt.Errorf("reading task %s to check expect: %w", id, err)
		}
		if err := expect.Check(detail.Task); err != nil {
			return "", err
		}
	}
	if q, ok := args["query"].(map[string]any); ok && len(q) > 0 {
		values := url.Values{}
		for k, v := range q {
			values.Set(k, fmt.Sprint(v))
		}
		path += "?" + values.Encode()
	}
	var encoded []byte
	if body, ok := args["body"]; ok && body != nil && r.Method != http.MethodGet {
		var err error
		if encoded, err = json.Marshal(body); err != nil {
			return "", fmt.Errorf("encoding body: %w", err)
		}
	}
	resp, err := client.send(ctx, r.Method, func() (*http.Request, error) {
		var reader io.Reader
		if encoded != nil {
			reader = bytes.NewReader(encoded)
		}
		req, err := http.NewRequestWithContext(ctx, r.Method, client.BaseURL+path, reader)
		if err != nil {
			return nil, err
		}
		if encoded != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set(ActingTaskHeader, taskID)
		if reason != "" {
			req.Header.Set(ActingReasonHeader, reason)
		}
		return req, nil
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", client.httpError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("reading the response: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Sprintf("%s %s: %s", r.Method, path, resp.Status), nil
	}
	return string(data), nil
}

// getTaskAs reads task id as GET /api/tasks/{id} on taskID's behalf --
// through the agent API gate, as every call this file makes is -- so
// expect's read is authorized by the acting task's own tasks:read grant on
// that task's repository. A plain client.GetTask carries no acting header,
// which the daemon serves as a trusted read of any task; and a refusal
// echoes the title, state and kind it read, so a run holding only a write
// grant (or one for another repository) could read any task through it.
func getTaskAs(ctx context.Context, client *HTTPClient, taskID, id string) (TaskDetail, error) {
	resp, err := client.send(ctx, http.MethodGet, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, client.BaseURL+"/api/tasks/"+url.PathEscape(id), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set(ActingTaskHeader, taskID)
		return req, nil
	})
	if err != nil {
		return TaskDetail{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return TaskDetail{}, client.httpError(resp)
	}
	var detail TaskDetail
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&detail); err != nil {
		return TaskDetail{}, fmt.Errorf("reading task %s: %w", id, err)
	}
	return detail, nil
}

func (r APIRoute) externalTool(s *Server, tok *model.ExternalAgentToken) mcp.Tool {
	return mcp.Tool{
		Name:        r.Tool,
		Description: r.Description,
		InputSchema: r.inputSchema(),
		Handler: func(ctx context.Context, args map[string]any) mcp.Result {
			text, err := r.externalCall(ctx, s, tok, args)
			if err != nil {
				return mcp.Result{Text: r.Tool + " failed: " + err.Error(), IsError: true}
			}
			return mcp.Result{Text: text}
		},
		ElisionAdvice: "narrow it with query parameters, or read one item by its id",
	}
}

// externalCall dispatches r in-process on s on behalf of the external agent
// credential tok, running through s.ServeHTTP and agentAPIGate so the same
// scope, repository, rail, and audit checks apply as on HTTP requests.
func (r APIRoute) externalCall(ctx context.Context, s *Server, tok *model.ExternalAgentToken, args map[string]any) (string, error) {
	if s == nil {
		return "", fmt.Errorf("this server is not initialized")
	}
	if tok == nil {
		return "", fmt.Errorf("missing external agent credential")
	}
	path := r.Path
	for _, p := range r.Params {
		v, _ := args[p].(string)
		v = strings.TrimSpace(v)
		if v == "" {
			return "", fmt.Errorf("%s is required", p)
		}
		escaped := url.PathEscape(v)
		path = strings.Replace(path, "{"+p+"...}", escaped, 1)
		path = strings.Replace(path, "{"+p+"}", escaped, 1)
	}
	reason, _ := args["reason"].(string)
	reason = strings.TrimSpace(reason)
	if r.NeedsReason && reason == "" {
		return "", fmt.Errorf("reason is required: %s is destructive", r.Pattern)
	}
	if expect, ok := expectation(args); ok && r.takesExpect() {
		id, _ := args["id"].(string)
		detail, err := getTaskAsExternal(ctx, s, tok, strings.TrimSpace(id))
		if err != nil {
			return "", fmt.Errorf("reading task %s to check expect: %w", id, err)
		}
		if err := expect.Check(detail.Task); err != nil {
			return "", err
		}
	}
	if q, ok := args["query"].(map[string]any); ok && len(q) > 0 {
		values := url.Values{}
		for k, v := range q {
			values.Set(k, fmt.Sprint(v))
		}
		path += "?" + values.Encode()
	}
	var encoded []byte
	if r.Method != http.MethodGet {
		if body, ok := args["body"]; ok && body != nil {
			var err error
			if encoded, err = json.Marshal(body); err != nil {
				return "", fmt.Errorf("encoding body: %w", err)
			}
		} else if r.body != nil {
			fallback := map[string]any{}
			for k, v := range args {
				if k == "reason" || k == "query" || k == "expect" || k == "body" || slices.Contains(r.Params, k) {
					continue
				}
				fallback[k] = v
			}
			if len(fallback) > 0 {
				var err error
				if encoded, err = json.Marshal(fallback); err != nil {
					return "", fmt.Errorf("encoding body: %w", err)
				}
			}
		}
	}
	var reader io.Reader
	if encoded != nil {
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(r.Method, path, reader)
	req = req.WithContext(withExternalToken(ctx, tok))
	if encoded != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if reason != "" {
		req.Header.Set(ActingReasonHeader, reason)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code < 200 || rec.Code > 299 {
		var errBody struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &errBody) == nil && errBody.Error != "" && errBody.Code == "" {
			return "", fmt.Errorf("HTTP %d: %s", rec.Code, errBody.Error)
		}
		return "", fmt.Errorf("HTTP %d: %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	data := rec.Body.Bytes()
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Sprintf("%s %s: %d %s", r.Method, path, rec.Code, http.StatusText(rec.Code)), nil
	}
	return string(data), nil
}

func getTaskAsExternal(ctx context.Context, s *Server, tok *model.ExternalAgentToken, id string) (TaskDetail, error) {
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+url.PathEscape(id), nil)
	req = req.WithContext(withExternalToken(ctx, tok))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code < 200 || rec.Code > 299 {
		var errBody struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &errBody) == nil && errBody.Error != "" {
			return TaskDetail{}, fmt.Errorf("HTTP %d: %s", rec.Code, errBody.Error)
		}
		return TaskDetail{}, fmt.Errorf("HTTP %d: %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	var detail TaskDetail
	if err := json.NewDecoder(io.LimitReader(rec.Body, 4<<20)).Decode(&detail); err != nil {
		return TaskDetail{}, fmt.Errorf("reading task %s: %w", id, err)
	}
	return detail, nil
}

func init() {
	agent.RegisterPublishedToolNames(AgentAPIToolNames)
	agent.RegisterPublishedControlToolNames(ModelOnlyAgentAPIRouteToolNames)
}

// ModelOnlyAgentAPIRouteToolNames is every generated tool that stays on
// the model's control roster whatever the grants: the routes the sandbox
// channel refuses outright, and those it refuses for some bodies
// (ModelOnlyAgentAPIToolNames is the same for one run's grants).
func ModelOnlyAgentAPIRouteToolNames() []string {
	var names []string
	for _, r := range AgentAPIRoutes() {
		if IsSandboxDeniedScope(r.Scope) || sandboxChannelGuard(r.Pattern, nil) != nil || sandboxGuardReadsBody[r.Pattern] {
			names = append(names, r.Tool)
		}
	}
	return names
}
