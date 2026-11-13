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

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// This file turns the route table into APIRoutes (docs/agent-api.md): one
// per route whose scope is in the vocabulary, named after the route's
// handler, described from routeDocs, and taking the route's path
// parameters, its query and its body as arguments. Their names,
// descriptions and input schemas are what grain_explore_scope advertises
// on POST /api/mcp (mcp_endpoint.go), where grain_invoke dispatches them
// in-process through s.mux, and what externalAgentAPITools serves as one
// tool per route on POST /mcp. Either way the route runs exactly as the
// UI's does -- same handler, same validation, same side effects -- and
// agentAPIGate holds the call to the caller's grants and records it.
//
// AgentAPITools, the per-route tools grain-sandbox used to serve a task
// over HTTP with ActingTaskHeader set, is kept as the legacy surface
// mcp_token_benchmark_test.go measures POST /api/mcp against; nothing
// in grain registers it any more.

// APIRoute is one route of the agent API, as a tool.
type APIRoute struct {
	Pattern     string   `json:"pattern"`
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Scope       APIScope `json:"scope"`
	Tool        string   `json:"tool"`
	Description string   `json:"description"`
	// Summary is Description's first line: what the route does, in the
	// sentence routeDocs gives it (or its handler's name), without the
	// boilerplate about the handler and scope that Description adds. It
	// is what the compact explore listing shows and what the keyword
	// filter searches.
	Summary string   `json:"summary,omitempty"`
	Params  []string `json:"params,omitempty"`
	// NeedsReason says a call must carry a reason: a dangerous scope,
	// or a DELETE.
	NeedsReason bool `json:"needsReason,omitempty"`
	// AliasOf names the whole-document route this one is a per-field
	// alias of -- api_set_repo_ci_mode is api_set_repo_settings for the
	// one field AliasField -- or is empty for a route that stands alone.
	// grain_explore_scope folds aliases out of a listing unless asked
	// for them (repoSettingsFieldRoutes).
	AliasOf    string `json:"aliasOf,omitempty"`
	AliasField string `json:"aliasField,omitempty"`
	body       any
	query      map[string]string
}

// APIToolPrefix begins every generated tool's name, so none can collide
// with a hand-written tool of the same verb (list_ci_runs, say).
const APIToolPrefix = "api_"

// routeDocs is the sentence a route's tool is described by. Every route
// that changes something has one; a read with none is described from
// its handler name, method and path.
var routeDocs = map[string]string{
	"PUT /api/settings":                                                    "Change deployment settings: only the fields given are changed, the rest keep their values.",
	"PUT /api/usage/settings":                                              "Replace the token usage settings: the concurrency mode (advisory, auto or off), the automatic cap's floor, per-run usage retention and the connection budgets.",
	"POST /api/settings":                                                   "Change deployment settings, as PUT /api/settings does: only the fields given are changed.",
	"POST /api/settings/plan":                                              "Preview a settings change without saving it: what the given fields would change, and what it would take to apply.",
	"PUT /api/settings/cli-run":                                            "Set the per-framework settings for agent CLI runs, keyed by framework.",
	"POST /api/registry/heartbeat":                                         "Trigger an immediate registration and heartbeat step against the configured Grain Hub, returning the updated hub snapshot.",
	"GET /api/tasks":                                                       "List tasks and their state, queue position and pull request. Narrow with repo, state, q, limit, cursor and fields=summary (default page " + fmt.Sprint(listPageSmall) + ", or " + fmt.Sprint(listPageMedium) + " for fields=summary, " + fmt.Sprint(listPageTiny) + " for fields=full, with truncated and nextCursor).",
	"GET /api/pipeline/changes":                                            "Read what changed in the pipeline since a cursor: task transitions, merge-queue states, stalls, base-branch verdicts, merge-batch results and the deployment's dispatch being blocked or unblocked (kind dispatch), oldest first, with the cursor to read on from.",
	"GET /api/sandboxes":                                                   "Read the deployment's health: dispatchBlocked first -- every condition under which nothing is dispatched (a git proxy that failed to bind, a reconcile loop that died), each with its error, since when and a remedy; absent while dispatching -- then the sandbox pool, host pressure, daemon restarts, state-repo sync and the other checks.",
	"POST /api/tasks":                                                      "File a new task: its title, description, repo and base branch, grants, dependencies and attachments.",
	"POST /api/tasks/from-template":                                        "File a task from a repository's own declared template (.grain/templates/<template>.yml), by its stem, with its fields filled; an agent's call is also held to POST /api/tasks's rules.",
	"POST /api/tasks/reorder":                                              "Move tasks in the queue: ids (in order) go after afterId and before beforeId; give only beforeId as the first queued task to move them to the front.",
	"GET /api/tasks/{id}":                                                  "Read one task in full: its fields, grants, conversation and runs.",
	"PATCH /api/tasks/{id}":                                                "Edit a task's title, description, priority, base branch, auto-merge or grants -- the same edit the task page makes.",
	"POST /api/tasks/{id}/capabilities":                                    "Attach a capability to a task or detach one (attach false), with its arguments; replace swaps a grant out for another. Under capabilities:detach alone, only attach false without replace, never on a standing session.",
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
	"POST /api/tasks/{id}/reply":                                           "Answer the question a task's run parked on (ask_question): refused with 409, posting nothing, when no question is parked or the task waits on a secret request, which is set on its page instead.",
	"GET /api/tasks/wait":                                                  "Block up to timeout seconds (at most 60) until the tasks in ids reach an until condition: closed, merged, awaiting-reply, ci-done or any; re-issue it to wait longer.",
	"GET /api/inbox":                                                       "List actionable Inbox items. Includes parked questions, secret requests, alerts, merge-held PRs and unapproved proposals; narrow with repo and kind. " + describeListQuery("task, title, repo, summary or subject", listPageLarge),
	"GET /api/refs/{ref}/summary":                                          "Roll up readable tasks carrying an externalRef. Reports counts by state, CI checks, open questions, pull requests and latest activity.",
	"GET /api/events":                                                      "List or stream ordered task, question, comment and CI events from cursor since. Supports wait long-poll (agent default limit 20, with truncated and nextCursor) and SSE (stream=1).",
	"GET /api/tasks/{id}/messages":                                         "List messages on a task's conversation. Pass agents=true for agent-to-agent messages and after for incremental reads. " + describeListQuery("author, summary or body", listPageSmall),
	"PUT /api/tasks/{id}/secret":                                           "Answer a task's pending secret request with its value, which goes to the secret store and is never shown back.",
	"POST /api/tasks/{id}/close":                                           "Close a task.",
	"POST /api/tasks/{id}/stop":                                            "Stop a task's running attempt.",
	"POST /api/tasks/{id}/pause":                                           "Pause a task in any open state, releasing everything it holds and keeping its place; immediate true skips a running task's grace period.",
	"POST /api/tasks/{id}/resume":                                          "Resume a paused task, back to exactly the state and dispatch standing it had.",
	"PUT /api/tasks/{id}/goal":                                             "Set a task's goal -- {kind, spec, on_met, on_limit}, as propose_task's goal -- replacing any it had; grain closes or rests the task when it is met.",
	"PUT /api/tasks/{id}/hostd":                                            "Refused: a task has no host. Which host host work runs on is set on the model connection or capability server, not the task.",
	"DELETE /api/tasks/{id}/goal":                                          "Remove a task's goal, making it an ordinary task again.",
	"POST /api/tasks/{id}/reopen":                                          "Reopen a closed task.",
	"POST /api/tasks/{id}/retry":                                           "Queue a task for another attempt.",
	"POST /api/tasks/{id}/retry-merge":                                     "Retry merging a task's pull request.",
	"POST /api/tasks/{id}/force-merge":                                     "Merge a task's pull request even though the merge queue will not, recording reason; head pins the commit to merge.",
	"POST /api/tasks/{id}/run-now":                                         "Dispatch a queued task now, ahead of the queue.",
	"POST /api/tasks/{id}/pull-request":                                    "Open the pull request for a task's pushed branch now, rather than when its run ends.",
	"POST /api/pull-requests/adopt":                                        "Adopt a pull request grain did not open (owner/name#n or its URL) as a shepherd task: review-only (the default) reviews and gates it without pushing; finish also pushes fixes, and upgrades a review-only shepherd in place. Needs merge:write and tasks:write on its repository.",
	"POST /api/pull-requests/review":                                       "File a grain review of a pull request (owner/name#n or its URL); design: true checks design-folder conventions and overlapping designs. Needs tasks:write and repos:read on its repository.",
	"POST /api/pull-requests/submit":                                       "Record a person's Submit approval on a pull request (owner/name#n or its URL); never skips or cancels required reviews. Overriding gates requires merge:force and an explicit reason.",
	"GET /api/pull-requests/status":                                        "Where a pull request stands: its head, shepherd task and mode, latest grain CI run and failing jobs, grain reviews and findings, and its merge-queue gate. Needs ci:read and tasks:read on its repository.",
	"POST /api/tasks/{id}/sandbox/recreate":                                "Destroy a task's sandbox and build a fresh one in its place; uncommitted work in it is lost.",
	"POST /api/sandbox-probe":                                              "Build a throwaway sandbox named probe-<id>, run argv in it over the deployment's guest-access channel (access: dialout or kubectl), return its output and exit code, and delete it.",
	"POST /api/tasks/{id}/messages":                                        "Say something to the human watching your own task and park it on their reply (the chat tool's write half); only your own task's id is accepted. Give summary too: one line of at most 100 characters naming the decision you need, which is all the inbox and task list show of it. To message another task's agent, use POST /api/tasks/{id}/agent-messages.",
	"POST /api/tasks/{id}/agent-messages":                                  "Send a message to another task's agent, signed as your task: its live run gets it on its next tool result, or its next run reads it; the task is never parked or re-queued by it.",
	"POST /api/tasks/{id}/human-review":                                    "From a review run, as your own task: hold the change you review for a person's decision (request_human_review) -- a product or design call, a risky migration, a security-sensitive behaviour change, an unclear brief. Turns its auto-merge off until a person submits or releases it. Code defects an agent can fix are findings, not this.",
	"POST /api/tasks/{id}/human-review/release":                            "Release a task's human-review hold as a person, recorded with who released it. Refused for any agent: an agent may raise the hold, never release it.",
	"GET /api/tasks/{id}/context":                                          "Read a task's brief, handover age and compaction cursor.",
	"POST /api/tasks/{id}/compact":                                         "Compact a task to a handover, or ask its next run to.",
	"POST /api/tasks/{id}/brief":                                           "Rewrite a task's current brief.",
	"POST /api/tasks/{id}/rollover":                                        "Roll a task over to a successor with a fresh record.",
	"POST /api/tasks/{id}/notes":                                           "Leave a note for your parent's or a child's agent (message_task): it never parks that task, reaches its live run, and wakes it if it is a resting durable task. Any other task needs https://grain.dev/capabilities/api#task-admin.",
	"POST /api/notifications":                                              "Raise an operator notification, or bump the open one with the same subject; it never parks anybody.",
	"POST /api/notifications/{id}/clear":                                   "Clear an operator notification.",
	"POST /api/tasks/{id}/wait-on":                                         "Make a task wait for the tasks in ids to close; park true ends its run and resumes it when they have.",
	"POST /api/tasks/{id}/park-until":                                      "Park a task until a time (until, RFC 3339), when it is queued again.",
	"POST /api/tasks/{id}/resume-later":                                    "Stop a task's run and queue it again when a condition is met (when: tasks, at, ci, message).",
	"PUT /api/memory":                                                      "Replace the memory grain keeps for the deployment, or for one repo when repo is given.",
	"PUT /api/memory/documents":                                            "Write one named memory document, for the deployment or for one repo.",
	"PUT /api/release-notes":                                               "Write the release notes for a week (period; the week just ended without one).",
	"POST /api/repos":                                                      "Add a repository to the deployment's targets.",
	"POST /api/repos/plan":                                                 "Preview a target repo list without saving it: the tasks, schedules and qualification plans it would strand in each repository it leaves out.",
	"DELETE /api/repos/{owner}/{name}":                                     "Remove a repository from the deployment's targets.",
	"PUT /api/repos/{owner}/{name}/ci-capabilities":                        "Set the capabilities a repository's grain CI jobs are granted. Under capabilities:detach alone, only a subset of the stored list.",
	"PUT /api/repos/{owner}/{name}/release":                                "Turn a repository's release workflow on or off, and set the jobs that judge its base branches and are dispatched after a merge.",
	"PUT /api/repos/{owner}/{name}/release-secrets/{secret}":               "Set the value of one of a repository's release secrets.",
	"DELETE /api/repos/{owner}/{name}/release-secrets/{secret}":            "Delete one of a repository's release secrets.",
	"POST /api/repos/{owner}/{name}/release-runs":                          "Start a release run of a repository, at a branch, ref or commit, optionally only some jobs.",
	"POST /api/repos/{owner}/{name}/pending-releases/{version}/approve":    "Approve a pending trunk release (vX.Y.Z), making it official; short_soak_ok accepts a soak under the minimum and allow_breaking breaking changes outside a RELEASE_MAJOR release, each otherwise refused. Only a person may; an agent is refused.",
	"GET /api/release-approval-key":                                        "The public key a GitHub Actions promote-release.yml verifies an Inbox approval with: the value for the repository variable GRAIN_RELEASE_APPROVAL_KEY.",
	"POST /api/repos/{owner}/{name}/pending-releases/{version}/reject":     "Reject a pending trunk release (vX.Y.Z), dropping its release candidate. Only a person may; an agent is refused.",
	"POST /api/releases/{version}/rollback":                                "Roll back a promoted trunk release (vX.Y.Z), tagging vX.Y.Z-retracted and re-pointing latest to an earlier release. Only a person may; an agent is refused.",
	"POST /api/repos/{owner}/{name}/base-branch-runs":                      "Start a grain CI run of one of a repository's base branches.",
	"PUT /api/repos/{owner}/{name}/work-ownership":                         "Set who owns a repository's work: its default, or one work item's override.",
	"PUT /api/repos/{owner}/{name}/capabilities":                           "Set the capabilities every task on a repository is granted by default. Under capabilities:detach alone, only a subset of the stored list.",
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
	"GET /api/repos/{owner}/{name}/releases":                               "List a repository's releases. " + describeListQuery("the release's name or branch", listPageLarge),
	"GET /api/deployment/reachable-repos":                                  "List the repositories the deployment's git connections reach, each with whether it can be pushed to -- what a task can be filed against. Narrow with push=true (only those the connection can push to), owner, host, q (a substring of owner/name), limit and cursor; an agent's call is paged " + fmt.Sprint(listPageDefault) + " at a time by default, and the answer says total, truncated and nextCursor when more pages exist.",
	"POST /api/repos/{owner}/{name}/releases":                              "Create a release of a repository, by name.",
	"GET /api/memory/documents":                                            "List the memory index: every memory document's name, scope (repo, or the deployment's own) and summary, without its text; narrow with repo. " + describeListQuery("a document's name, repo or summary", listPageLarge),
	"GET /api/repos/{owner}/{name}/branches":                               "List the branches grain has cut or adopted on a repository. " + describeListQuery("a branch's name", listPageMedium),
	"GET /api/repos/{owner}/{name}/github-branches":                        "List a repository's branches as its git host has them, by name. " + describeListQuery("a branch's name", listPageMedium),
	"GET /api/suite-runs":                                                  "List suite runs, with their suite, repository, base and progress; narrow with repo. " + describeListQuery("the suite's name or id", listPageSmall),
	"GET /api/notifications":                                               "List notifications, open or cleared; narrow with open, family and after. " + describeListQuery("a notification's subject or title", listPageLarge),
	"GET /api/ci/faults":                                                   "List the shared CI faults: a failure seen on more than one branch, with the branches, runs and files it was seen on and the task assigned to fix it; narrow with repo. " + describeListQuery("a fault's digest, kind, job, step, file or repository", listPageSmall),
	"DELETE /api/repos/{owner}/{name}/releases/{release}":                  "Delete a release.",
	"POST /api/repos/{owner}/{name}/releases/{release}/merge":              "Ask for a release's branch to be merged back into the repository's default branch.",
	"GET /api/repos/{owner}/{name}/releases/{release}/candidates":          "List a release's candidates. " + describeListQuery("a candidate's branch, status or release", listPageLarge),
	"POST /api/repos/{owner}/{name}/releases/{release}/candidates":         "Cut a release candidate of a release.",
	"POST /api/repos/{owner}/{name}/releases/{release}/candidates/promote": "Promote a release's current candidate.",
	"POST /api/repos/{owner}/{name}/branches":                              "Create a branch on a repository, named name, from the branch or commit in from.",
	"PUT /api/repos/{owner}/{name}/qualification-plan":                     "Set the plan a repository's release candidates are qualified by: its items, whether a run needs approval, and whether a qualified candidate is promoted.",
	"POST /api/repos/{owner}/{name}/candidates/{id}/qualification/approve": "Approve every task of a release candidate's qualification run in one action.",
	"GET /api/repos/{owner}/{name}/workflows":                              "List the grain CI workflows declared on the deployment for a repository. " + describeListQuery("a workflow's path or name", listPageTiny),
	"GET /api/merge-batches":                                               "List active merge-queue overlap re-check batches; narrow with repo. " + describeListQuery("a batch's repository, branch, ref or run id", listPageLarge),
	"GET /api/base-branch-gates":                                           "List watched base branches whose CI gate is unjudged or late; narrow with repo. " + describeListQuery("a gate's repository, branch or reason", listPageLarge),
	"GET /api/schedules":                                                   "List schedules; narrow with kind, sort, f and fields=full. " + describeListQuery("a schedule's name, id, repository, template, suite, source or declared path", listPageSmall),
	"POST /api/schedules":                                                  "Create a schedule, filing work from a template or suite on a recurrence.",
	"PATCH /api/schedules/{id}":                                            "Edit a schedule: only the fields given are changed; revision guards against a concurrent edit.",
	"POST /api/schedules/{id}/run":                                         "Run a schedule now (the schedules page's Run now), filing what it would file at its next firing.",
	"POST /api/schedules/composition":                                      "Draft a schedule without saving it: what it would file at its next firing.",
	"DELETE /api/schedules/{id}":                                           "Delete a schedule.",
	"GET /api/templates":                                                   "List task templates; narrow with repo, or pass fields=summary to omit prompt descriptions. " + describeListQuery("a template's id, name, title or repository", listPageTiny),
	"POST /api/templates":                                                  "Create a task template.",
	"PATCH /api/templates/{id}":                                            "Edit a task template: only the fields given are changed; revision guards against a concurrent edit.",
	"DELETE /api/templates/{id}":                                           "Delete a task template.",
	"GET /api/suites":                                                      "List suites of templates. " + describeListQuery("a suite's id or name", listPageLarge),
	"POST /api/suites":                                                     "Create a suite of templates, filed together.",
	"PATCH /api/suites/{id}":                                               "Edit a suite: only the fields given are changed; revision guards against a concurrent edit.",
	"POST /api/suites/composition":                                         "Draft a suite without saving it: the tasks it would file, for query repo and base.",
	"DELETE /api/suites/{id}":                                              "Delete a suite.",
	"POST /api/suite-runs":                                                 "Run a suite now against a repo and base branch, filing its tasks.",
	"GET /api/boards":                                                      "List boards. " + describeListQuery("a board's id, name or folder", listPageLarge),
	"GET /api/memory":                                                      "List standing memories across scopes; narrow with repo (empty for the deployment's own). " + describeListQuery("a memory's repository, author or text", listPageTiny),
	"GET /api/release-notes":                                               "List weekly release notes, without their full text. " + describeListQuery("a note's week period, title or author", listPageLarge),
	"GET /api/release-notes/versions":                                      "List released grain versions whose notes this build carries. " + describeListQuery("a version string", listPageMedium),
	"GET /api/state-repo/files":                                            "List files and directories of the exported configuration in the state repository. " + describeListQuery("a file's path", listPageDefault),
	"GET /api/deployment/git-connections":                                  "List the deployment's git connections; narrow with host. " + describeListQuery("a connection's credential, host, kind or scope", listPageLarge),
	"GET /api/deployment/audit-logs":                                       "List the deployment's credential and secret audit trail, newest first. " + describeListQuery("an event's kind, resource, credential, principal or task id", listPageLarge),
	"GET /api/deployment/secret-audit":                                     "List the deployment's newest secret resolutions. " + describeListQuery("an entry's secret name, key, principal or task id", listPageMedium),
	"GET /api/deployment/config-audit":                                     "List configuration changes recorded in the deployment's audit trail, newest first. " + describeListQuery("an entry's actor, actorLabel, surface or summary", listPageLarge),
	"GET /api/hostds":                                                      "List registered hosts and their live connection status. " + describeListQuery("a host's name or owner", listPageMedium),
	"GET /api/gcp-keys":                                                    "List configured GCP service-account keys. " + describeListQuery("a key's name, project id or service account email", listPageLarge),
	"GET /api/github-tokens":                                               "List the deployment's GitHub credentials and the connections naming them. " + describeListQuery("a credential's name, capability or connection scope", listPageLarge),
	"GET /api/secrets":                                                     "List the deployment's secret names, keys and owning capability servers (values are never shown). " + describeListQuery("a secret's name or key", listPageMedium),
	"GET /api/agent-models":                                                "List models available on an agent framework or model connection; narrow with framework and backend. " + describeListQuery("a model's id, label or family", listPageMedium),
	"GET /api/persistent-sandboxes":                                        "List supervised persistent sandboxes and their state. " + describeListQuery("a sandbox's name, repository or state", listPageMedium),
	"GET /api/console":                                                     "List live interactive console sessions. " + describeListQuery("a session's task id, title or repository", listPageLarge),
	"GET /api/console-sessions":                                            "List configured standing chat sessions. " + describeListQuery("a session's id, title, task id or team", listPageTiny),
	"GET /api/upgrade/targets":                                             "List upgrade channels and released versions offered by this deployment. " + describeListQuery("a target's name or kind", listPageMedium),
	"GET /api/capability-methods":                                          "List capability methods across configured capability servers; narrow with kind and repo to include policyRefusal. " + describeListQuery("a method's ref, capability, name or description", listPageSmall),
	"GET /api/capability-servers":                                          "List configured capability servers and their attach status. " + describeListQuery("a server's capability_id, command or URL", listPageSmall),
	"GET /api/capability-servers/usage":                                    "List open tasks, templates, schedules and workflow jobs holding each capability server. " + describeListQuery("a server's capability_id", listPageLarge),
	"GET /api/capability-servers/host-entries":                             "List host-command allowlist entries reported by the host agent. " + describeListQuery("an entry's name", listPageMedium),
	"GET /api/ci/workflows":                                                "List grain CI workflows declared for a repository; narrow with repo and ref. " + describeListQuery("a workflow's path or name", listPageLarge),
	"GET /api/ci/runs/{id}/artifacts":                                      "List artifacts collected from a CI run. " + describeListQuery("an artifact's job or name", listPageMedium),
	"POST /api/boards":                                                     "Create a board.",
	"PATCH /api/boards/{id}":                                               "Edit a board: only the fields given are changed.",
	"DELETE /api/boards/{id}":                                              "Delete a board.",
	"PUT /api/deployment":                                                  "Change the deployment's default model, per-model settings and target repositories.",
	"POST /api/deployment/git-connections":                                 "Add a git connection to the deployment: the credential grain reaches a git host with.",
	"DELETE /api/deployment/git-connections/{cred}":                        "Remove one of the deployment's git connections.",
	"POST /api/deployment/git-connections/check":                           "Check a git connection works, without saving it. To check an unsaved connection, leave credential empty and give what the editor would: type (pat with value, app with app, or ambient with nothing else) and host. To check a saved connection instead, name it in credential and give no value: credential is only ever a saved credential's name, never a type.",
	"PUT /api/deployment/agent-backends/{backendId}":                       "Create or replace one of the deployment's model connections.",
	"DELETE /api/deployment/agent-backends/{backendId}":                    "Delete one of the deployment's model connections.",
	"POST /api/deployment/agent-backends/{backendId}/discover":             "Ask a model connection's provider which models it serves now, and keep the answer as the connection's discovered list.",
	"PUT /api/deployment/repos/{owner}/{name}/config":                      "Set a repository's deployment-level configuration: only the fields given are changed.",
	"PUT /api/deployment/repos/{owner}/{name}/git-connection":              "Set which git connection a repository is reached through.",
	"POST /api/deployment/broadcast":                                       "Post a comment to every active task (running_only: only those with a run in flight).",
	"POST /api/hostds":                                                     "Register a remote host; its token is shown once, in the answer.",
	"PATCH /api/hostds/{name}":                                             "Set a host's owner, labels, or capacity.",
	"POST /api/hostds/{name}/rename":                                       "Rename a registered host.",
	"GET /api/hostds/{name}/install":                                       "Show how to install a registered host on its machine again; never its token.",
	"POST /api/hostds/{name}/rotate":                                       "Retire a host's token and mint a new one, shown once in the answer.",
	"POST /api/hostds/{name}/revoke":                                       "Revoke a host and every token it has.",
	"DELETE /api/hostds/{name}":                                            "Delete a revoked host from the registry, with its tokens; refused for a live host or one a model connection or capability server still names.",
	"POST /api/hostds/delete-revoked":                                      "Delete every revoked host nothing still names; lists the ones skipped.",
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
	"GET /api/capability-servers/gchat-usage":                              "Read each configured space's post counts on a gchat capability server; narrow with id. " + describeListQuery("a space's name or transport", listPageMedium),
	"POST /api/capability-servers/figma-test":                              "Verify one configured account on a figma capability server against Figma /v1/me.",
	"POST /api/capability-servers/figma-oauth-authorize":                   "Build a Figma OAuth 2.0 browser authorization URL for a configured oauth account.",
	"POST /api/capability-servers/figma-oauth-exchange":                    "Exchange a Figma OAuth 2.0 authorization code and store its refresh token in the encrypted secrets store.",
	"PUT /api/capability-policy":                                           "Set the deployment's capability policy.",
	"GET /api/api-scope-sets":                                              "List the agent API's scope sets, built-in and declared, with the scopes each resolves to. " + describeListQuery("a scope set's name or source", listPageSmall),
	"GET /api/api-scope-sets/usage":                                        "List every open task, template, schedule and workflow job holding each API scope set. " + describeListQuery("a scope set's name", listPageLarge),
	"PUT /api/api-scope-sets":                                              "Declare or replace one of the deployment's API scope sets (written to the config repository; grants it to nobody).",
	"POST /api/api-scope-sets/delete":                                      "Remove one of the deployment's API scope sets.",
	"POST /api/capability-policy/preview":                                  "Preview a capability policy without saving it: what it would allow and refuse.",
	"POST /api/gcp-keys":                                                   "Create a GCP key: a named service-account credential tasks can be granted.",
	"PUT /api/gcp-keys/{name}":                                             "Create or replace a named GCP key.",
	"DELETE /api/gcp-keys/{name}":                                          "Delete a named GCP key.",
	"PUT /api/github-tokens/{name}":                                        "Set a named GitHub token.",
	"DELETE /api/github-tokens/{name}":                                     "Delete a named GitHub token; refused while a git connection or capability grant still names it.",
	"PUT /api/secrets/{secret}/{key}":                                      "Set one key of a secret; the value is stored and never shown back.",
	"DELETE /api/secrets/{secret}/{key}":                                   "Delete one key of a secret.",
	"DELETE /api/secrets/{secret}":                                         "Delete a secret and every key in it.",
	"POST /api/state-repo/sync":                                            "Sync the deployment's state repository now.",
	"POST /api/state-repo/pull-requests":                                   "Open a validated, held grain/config pull request carrying the change a refused configuration write returned (a person only).",
	"POST /api/state-repo/remote":                                          "Name the external git repository the deployment reads its configuration from, or none: true to go back to the internal one. An agent needs https://grain.dev/capabilities/api#capabilities:admin as well as settings:write for it.",
	"POST /api/state-repo/secrets-key":                                     "Install the key the deployment's secrets file is sealed to.",
	"POST /api/state-repo/import-key":                                      "Install the key the deployment's secrets file is sealed to.",
	"POST /api/state-repo/secrets-key/wrap":                                "Wrap the deployment's secrets key with a Cloud KMS key, verified by a round trip before it is written.",
	"POST /api/state-repo/secrets-key/unwrap":                              "Put the deployment's secrets key back in plaintext form, the reversal of wrap.",
	"POST /api/state-repo/secrets-repo":                                    "Name the git repository the deployment's encrypted secrets file is backed up to, or none: true for none.",
	"POST /api/state-repo/reset-notice":                                    "Dismiss the notice of what a state-repository merge took away.",
	"POST /api/host/reboot":                                                "Reboot the machine grain runs on.",
	"POST /api/host/shutdown":                                              "Power off the machine grain runs on; it does not come back on its own.",
	"POST /api/host/restart":                                               "Restart the grain service, without touching the machine it runs on.",
	"POST /api/upgrade":                                                    "Start or schedule upgrading grain to a branch or image; set afterTasksFinish: true to drain running tasks and CI jobs first (your own run counts, so finish rather than polling), as false restarts grain immediately and discards in-flight work.",
	"DELETE /api/upgrade/current":                                          "Cancel an upgrade currently in progress before its point of no return (returns 409 Conflict once cut-over or restart has begun).",
	"DELETE /api/upgrade/scheduled":                                        "Cancel an upgrade waiting for running tasks to finish.",
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
	"GET /api/ci/runs":                                                     "List CI runs, newest first: grain CI's, and GitHub Actions runs (gha:<owner>:<name>:<id>) on Actions repos. Narrow with repo, ref, conclusion, job, workflow, since, until, provider, q, limit (default 10, or 20 for view=compact) and cursor; sets truncated and nextCursor.",
	"GET /api/ci/runs/{id}":                                                "Read one CI run in full: a grain CI run's jobs, steps and their log output, or a GitHub Actions run's (gha:...) jobs and steps with their conclusions.",
	"GET /api/ci/runs/{id}/log":                                            "Read one GitHub Actions job's log (gha:... runs): query job (name or id; default the first that failed) and tail (lines); capped at 256 KiB, keeping the end.",
	"DELETE /api/ci/runs/{id}":                                             "Cancel a grain CI run.",
	"POST /api/ci/runs/{id}/rerun":                                         "Re-run a GitHub Actions run (gha:...) as its next attempt, without a commit: its failed and cancelled jobs by default, every job with all, or the jobs named. Each run at one commit is re-run at most 3 times, grain's automatic re-runs included.",
	"DELETE /api/ci/cache":                                                 "Empty grain CI's job cache, so every job of the next runs does its work again.",
	"POST /api/tests/quarantine":                                           "Quarantine a flaky or failing test (returning the rendered .grain/quarantine.yml entry to commit in the workspace) and optionally file a held follow-up repair task.",
	"POST /api/package-cache/flush":                                        "Empty the package cache: query kind (apt, pypi, docker, npm or go) for one ecosystem, or all of them without.",
	"POST /api/actions-cache/flush":                                        "Empty the actions cache: query repo (owner/name) for one repository's entries, or every repository's without.",
	"POST /api/pipeline-pause":                                             "Pause the pipeline: nothing new is dispatched until it is resumed.",
	"DELETE /api/pipeline-pause":                                           "Resume the paused pipeline.",
	"GET /api/external-agents/tokens":                                      "List external agent credentials (metadata only; secret tokens are never shown after issue). " + describeListQuery("a credential's id, name, owner or scope set", listPageLarge),
	"POST /api/external-agents/tokens":                                     "Issue a scoped external agent credential for /mcp.",
	"POST /api/external-agents/tokens/{id}/rotate":                         "Rotate an external agent credential, returning a new secret token once and invalidating the previous token immediately.",
	"POST /api/external-agents/tokens/{id}/revoke":                         "Revoke an external agent credential immediately.",
	"DELETE /api/external-agents/tokens/{id}":                              "Delete an external agent credential record.",
	"POST /api/external-agents/tokens/revoke-all":                          "Emergency kill switch: revoke every active external agent credential immediately.",
	"GET /api/access/principals":                                           "List the allowed users and groups admitted through verified-identity sign-in. " + describeListQuery("a principal's entry, role, reason or addedBy", listPageMedium),
	"POST /api/access/principals":                                          "Add a user:<email> or group:<email> entry to the allowed users and groups list (idempotent when already present).",
	"PATCH /api/access/principals/{entry}":                                 "Give an entry on the allowed users and groups list another role (a scope set).",
	"DELETE /api/access/principals/{entry}":                                "Remove a user:<email> or group:<email> entry from the allowed users and groups list.",
	"GET /api/access/history":                                              "List recent changes to the allowed users and groups list, newest first: who made each, when, the old and new role and the reason.",
}

// routeBodies is the request type a route reads its body into, which
// its tool describes field by field: the type of the `var req X` its
// handler hands to readJSON. Every route that reads a body has one, and
// a route with none takes no body.
var routeBodies = map[string]any{
	"PUT /api/settings":             UpdateSettingsRequest{},
	"PUT /api/usage/settings":       model.UsageSettings{},
	"POST /api/settings":            UpdateSettingsRequest{},
	"POST /api/settings/plan":       UpdateSettingsRequest{},
	"PUT /api/settings/cli-run":     updateCLIRunSettingsRequest{},
	"POST /api/tasks":               CreateTaskRequest{},
	"POST /api/tasks/from-template": FromTemplateRequest{},
	"POST /api/repos/{owner}/{name}/pending-releases/{version}/approve": ReleaseApproval{},
	"POST /api/repos/{owner}/{name}/pending-releases/{version}/reject":  ReleaseApproval{},
	"POST /api/releases/{version}/rollback":                             ReleaseRollback{},
	"POST /api/tasks/reorder":                                           ReorderRequest{},
	"PATCH /api/tasks/{id}":                                             UpdateTaskRequest{},
	"POST /api/tasks/{id}/capabilities":                                 setCapabilityRequest{},
	"POST /api/tasks/{id}/depends-on":                                   setDependencyRequest{},
	"POST /api/tasks/{id}/defer":                                        deferRequest{},
	"POST /api/sandbox-probe":                                           SandboxProbeRequest{},
	"POST /api/tasks/{id}/pause":                                        pauseRequest{},
	"PUT /api/tasks/{id}/goal":                                          setGoalRequest{},
	"POST /api/tasks/{id}/run-now":                                      runNowRequest{},
	"POST /api/tasks/{id}/comments":                                     addCommentRequest{},
	"POST /api/tasks/{id}/reply":                                        addCommentRequest{},
	"POST /api/tasks/{id}/change-mail":                                  MailChangeRequest{},
	"PUT /api/tasks/{id}/secret":                                        setPendingSecretRequest{},
	"POST /api/tasks/{id}/close":                                        closeRequest{},
	"POST /api/tasks/{id}/reopen":                                       reopenRequest{},
	"POST /api/tasks/{id}/force-merge":                                  forceMergeRequest{},
	"POST /api/tasks/{id}/messages":                                     messageRequest{},
	"POST /api/tasks/{id}/agent-messages":                               agentMessageRequest{},
	"POST /api/tasks/{id}/notes":                                        NoteRequest{},
	"POST /api/tasks/{id}/compact":                                      CompactRequest{},
	"POST /api/tasks/{id}/brief":                                        BriefRequest{},
	"POST /api/tasks/{id}/human-review":                                 HumanReviewRequestBody{},
	"POST /api/notifications":                                           RaiseNotificationRequest{},
	"POST /api/tasks/{id}/wait-on":                                      waitOnRequest{},
	"POST /api/tasks/{id}/park-until":                                   parkUntilRequest{},
	"POST /api/tasks/{id}/resume-later":                                 resumeLaterRequest{},
	"PUT /api/memory":                                                   memoryRequest{},
	"PUT /api/memory/documents":                                         memoryDocumentRequest{},
	"PUT /api/release-notes":                                            releaseNoteRequest{},
	"POST /api/repos":                                                   AddRepoRequest{},
	"POST /api/repos/plan":                                              PlanTargetReposRequest{},
	"PUT /api/repos/{owner}/{name}/ci-capabilities":                     setRepoCICapabilitiesRequest{},
	"PUT /api/repos/{owner}/{name}/release":                             setRepoReleaseRequest{},
	"PUT /api/repos/{owner}/{name}/release-secrets/{secret}":            setRepoReleaseSecretRequest{},
	"POST /api/repos/{owner}/{name}/release-runs":                       RepoReleaseRunRequest{},
	"POST /api/repos/{owner}/{name}/base-branch-runs":                   BaseBranchRunRequest{},
	"PUT /api/repos/{owner}/{name}/work-ownership":                      SetRepoWorkOwnershipRequest{},
	"PUT /api/repos/{owner}/{name}/capabilities":                        SetRepoCapabilitiesRequest{},
	"PUT /api/repos/{owner}/{name}/prompt-extension":                    SetRepoPromptExtensionRequest{},
	"PUT /api/repos/{owner}/{name}/setup-command":                       SetRepoSetupCommandRequest{},
	"PUT /api/repos/{owner}/{name}/declarations":                        SetRepoDeclarationsRequest{},
	"PUT /api/repos/{owner}/{name}/require-branch-up-to-date":           SetRepoRequireBranchUpToDateRequest{},
	"PUT /api/repos/{owner}/{name}/ignore-github-ci":                    SetRepoIgnoreGitHubCIRequest{},
	"PUT /api/repos/{owner}/{name}/ci-mode":                             SetRepoCIModeRequest{},
	"PUT /api/repos/{owner}/{name}/settings":                            SetRepoSettingsRequest{},
	"PUT /api/repos/{owner}/{name}/watch-default-branch":                SetRepoWatchDefaultBranchRequest{},
	"PUT /api/repos/{owner}/{name}/merge-on-green":                      SetRepoMergeOnGreenRequest{},
	"PUT /api/repos/{owner}/{name}/base-repair-bypasses-human-gates":    SetRepoBaseRepairBypassesHumanGatesRequest{},
	"PUT /api/repos/{owner}/{name}/unfixable-checks":                    SetRepoUnfixableChecksRequest{},
	"PUT /api/repos/{owner}/{name}/workflows":                           SetRepoWorkflowsRequest{},
	"POST /api/repos/{owner}/{name}/releases":                           CreateReleaseRequest{},
	"POST /api/repos/{owner}/{name}/branches":                           CreateBranchRequest{},
	"PUT /api/repos/{owner}/{name}/qualification-plan":                  PutQualificationPlanRequest{},
	"POST /api/schedules":                                               CreateScheduleRequest{},
	"PATCH /api/schedules/{id}":                                         UpdateScheduleRequest{},
	"POST /api/schedules/composition":                                   CreateScheduleRequest{},
	"POST /api/templates":                                               CreateTemplateRequest{},
	"PATCH /api/templates/{id}":                                         UpdateTemplateRequest{},
	"POST /api/suites":                                                  CreateSuiteRequest{},
	"PATCH /api/suites/{id}":                                            UpdateSuiteRequest{},
	"POST /api/suites/composition":                                      CreateSuiteRequest{},
	"POST /api/suite-runs":                                              CreateSuiteRunRequest{},
	"POST /api/boards":                                                  CreateBoardRequest{},
	"PATCH /api/boards/{id}":                                            UpdateBoardRequest{},
	"PUT /api/deployment":                                               UpdateDeploymentRequest{},
	"POST /api/deployment/git-connections":                              AddTeamGitConnectionRequest{},
	"POST /api/deployment/git-connections/check":                        CheckTeamGitConnectionRequest{},
	"PUT /api/deployment/agent-backends/{backendId}":                    putDeploymentAgentBackendRequest{},
	"PUT /api/deployment/repos/{owner}/{name}/config":                   SetDeploymentRepoConfigRequest{},
	"PUT /api/deployment/repos/{owner}/{name}/git-connection":           SetTeamRepoGitConnectionRequest{},
	"POST /api/deployment/broadcast":                                    BroadcastTeamRequest{},
	"POST /api/hostds":                                                  CreateHostdRequest{},
	"PATCH /api/hostds/{name}":                                          UpdateHostdRequest{},
	"POST /api/hostds/{name}/rename":                                    RenameHostdRequest{},
	"PUT /api/agent-keys/{framework}":                                   setAgentKeyRequest{},
	"PUT /api/agent-backends/{id}/credential":                           setAgentKeyRequest{},
	"PUT /api/agent-backends/{id}/codex-auth":                           setAgentKeyRequest{},
	"PUT /api/codex-auth":                                               setAgentKeyRequest{},
	"POST /api/capability-grants/check":                                 checkGrantsRequest{},
	"PUT /api/capability-servers":                                       SetCapabilityServerRequest{},
	"POST /api/capability-servers/remove":                               CapabilityServerIDRequest{},
	"POST /api/capability-servers/enable":                               CapabilityServerIDRequest{},
	"POST /api/capability-servers/disable":                              CapabilityServerIDRequest{},
	"POST /api/capability-servers/dismiss-notice":                       CapabilityServerIDRequest{},
	"POST /api/capability-servers/rotate-managed":                       CapabilityServerIDRequest{},
	"POST /api/capability-servers/bind-secret":                          BindCapabilityServerSecretRequest{},
	"POST /api/capability-servers/test":                                 TestCapabilityServerRequest{},
	"POST /api/capability-servers/test-script":                          TestHostScriptRequest{},
	"POST /api/capability-servers/gchat-test":                           TestGChatPostRequest{},
	"POST /api/capability-servers/figma-test":                           TestFigmaAccountRequest{},
	"POST /api/capability-servers/figma-oauth-authorize":                FigmaOAuthAuthorizeRequest{},
	"POST /api/capability-servers/figma-oauth-exchange":                 FigmaOAuthExchangeRequest{},
	"PUT /api/capability-policy":                                        CapabilityPolicyBody{},
	"PUT /api/api-scope-sets":                                           PutAPIScopeSetRequest{},
	"POST /api/api-scope-sets/delete":                                   DeleteAPIScopeSetRequest{},
	"POST /api/capability-policy/preview":                               CapabilityPolicyBody{},
	"POST /api/gcp-keys":                                                SetGCPKeyRequest{},
	"PUT /api/gcp-keys/{name}":                                          SetGCPKeyRequest{},
	"PUT /api/github-tokens/{name}":                                     setGitHubTokenRequest{},
	"PUT /api/secrets/{secret}/{key}":                                   setSecretRequest{},
	"POST /api/state-repo/pull-requests":                                ConfigPullRequestRequest{},
	"POST /api/pull-requests/adopt":                                     AdoptPullRequestRequest{},
	"POST /api/pull-requests/review":                                    ReviewPullRequestRequest{},
	"POST /api/pull-requests/submit":                                    SubmitPullRequestRequest{},
	"POST /api/state-repo/remote":                                       stateRepoRemoteRequest{},
	"POST /api/state-repo/secrets-key":                                  secretsKeyRequest{},
	"POST /api/state-repo/import-key":                                   secretsKeyRequest{},
	"POST /api/state-repo/secrets-key/wrap":                             secretsKeyWrapRequest{},
	"POST /api/state-repo/secrets-repo":                                 secretsRepoRequest{},
	"POST /api/upgrade":                                                 startUpgradeRequest{},
	"DELETE /api/upgrade/current":                                       CancelUpgradeRequest{},
	"POST /api/console/{id}/input":                                      consoleInputRequest{},
	"POST /api/console/{id}/resize":                                     consoleResizeRequest{},
	"POST /api/console-sessions":                                        CreateConsoleSessionRequest{},
	"PUT /api/console-sessions/{id}":                                    UpdateConsoleSessionRequest{},
	"POST /api/console-sessions/{id}/restart":                           RestartConsoleSessionRequest{},
	"POST /api/ci/runs":                                                 CIRunRequest{},
	"DELETE /api/ci/runs/{id}":                                          CICancelRequest{},
	"POST /api/ci/runs/{id}/rerun":                                      ActionsRerunRequest{},
	"POST /api/tests/quarantine":                                        QuarantineTestRequest{},
	"POST /api/pipeline-pause":                                          pausePipelineRequest{},
	"POST /api/external-agents/tokens":                                  CreateExternalAgentTokenRequest{},
	"POST /api/access/principals":                                       AllowPrincipalRequest{},
	"PATCH /api/access/principals/{entry}":                              SetPrincipalRoleRequest{},
}

// routeQueries is the query parameters a route's handler reads, each
// with what it means: its tool describes them as properties of query.
// A route with none takes no query of its own, though the query object
// still carries any parameter a caller adds (such as a template
// composition's field.<name>).
var routeQueries = map[string]map[string]string{
	"GET /api/pull-requests/status": {
		"pr":      "The pull request: owner/name#n or its URL.",
		"threads": "1 to include every inline review thread's file, line and body.",
	},
	"GET /api/restart-watch": {
		"quick": "1 to answer at once with whether grain is restarting, rather than holding the request until it is or 25 s pass.",
	},
	"GET /api/pipeline/changes": {
		"after": "The cursor a previous read returned as next (<RFC 3339 time>~<seq>); omitted, the last 24 hours.",
		"limit": "At most this many entries (default 100, at most 500).",
		"repo":  "Only this repository's tasks, base branches and merge batches (owner/name); a repository-scoped tasks:read grant must name its own.",
	},
	"GET /api/tasks": {
		"repo":         "Only this repository's (owner/name) tasks; a repository-scoped tasks:read grant must name its own.",
		"state":        "Only tasks in these states, repeated or comma-separated (proposed, queued, running, awaiting_reply, failed, awaiting_submit, completed, closed).",
		"q":            "Only tasks whose title, description, branch, externalRef or id contains this text, case-insensitively.",
		"search":       "The same as q.",
		"limit":        "At most this many tasks (an agent's call: default 10, 50 for fields=summary, 5 for fields=full, at most 1000; with cursor for a person: default 100).",
		"fields":       "summary for id, title, state, repo, externalRef, createdAt, blockedBy and stall only -- no description; list for every field but the description; full for every field including descriptions.",
		"cursor":       "Page through the matching tasks newest-closed first, starting after this cursor (empty for the first page); the answer is {tasks, nextCursor, truncated, total, merged, sessions}.",
		"closedSince":  "Only tasks closed at or after this RFC 3339 time.",
		"closedBefore": "Only tasks closed before this RFC 3339 time.",
		"derived_from": "Only tasks whose provenance records being derived from this pull request (owner/name#number or its URL).",
		"filed_via":    "Only tasks whose filedVia (the X-Grain-Client label they were filed with) contains this text, case-insensitively.",
		"externalRef":  "Only tasks carrying this exact external work reference (for example CA-204).",
		"owner":        "Only tasks owned by this account (or me for the caller's own).",
	},
	"GET /api/tasks/wait": {
		"ids":     "The task ids to wait on, repeated or comma-separated; tasks:read must reach every one.",
		"until":   "closed (every task closed), merged (every task merged; answers failed at once if one closes unmerged), awaiting-reply (any task parked on a question or secret), ci-done (any task's latest CI concluded) or any (any change after since); comma-separated for the first of several.",
		"timeout": "How many seconds to hold the request, 1 to 60 (default 60).",
		"since":   "The revision a previous answer returned, for until=any: wake on the next change after it.",
	},
	"GET /api/inbox": listObjectQueryDocs("task, title, repo, summary, subject or pull request", listPageLarge, map[string]string{
		"repo": "Only this repository's (owner/name) items.",
		"kind": "Only items of this kind: question, secret, notification, merge-held or proposal.",
	}),
	"GET /api/refs/{ref}/summary": {
		"repo": "Only this repository's (owner/name) tasks in the rollup.",
	},
	"GET /api/events": {
		"since":  "Return events strictly after this event id cursor, or \"now\" to start from the current head.",
		"wait":   "Long-poll up to this many seconds (at most 25) for a matching event when none is already waiting.",
		"limit":  "At most this many events per JSON page, 1 to 500 (an agent's call: default 20; otherwise default 100).",
		"repo":   "Only events for tasks targeting this repository (owner/name).",
		"types":  "Comma-separated event types to include: task.state, task.question, task.comment, task.ci.",
		"stream": "1 or true to stream Server-Sent Events (text/event-stream) instead of a JSON page.",
	},
	"GET /api/tasks/{id}": {
		"comments_since":  "An RFC 3339 time: answer only the conversation items created strictly after it.",
		"comments_limit":  "Answer only the newest this many comments (1 to 500); olderComments says how many were left out.",
		"comments_before": "A comment id: answer only the comments before it, the cursor for the page before comments_limit's.",
		"attempts_limit":  "Answer only the newest this many runs (1 to 500); olderAttempts says how many were left out.",
		"attempts_before": "An attempt number: answer only the runs before it, the cursor for the page before attempts_limit's.",
	},
	"GET /api/tasks/states": {"limit": "At most this many states (an agent's call: default 250, at most 1000; a person's: default 1000).", "cursor": "The nextCursor of the previous page.", "since": "An older spelling of cursor."},
	"GET /api/states":       {"limit": "At most this many states (an agent's call: default 250, at most 1000; a person's: default 1000).", "cursor": "The nextCursor of the previous page.", "since": "An older spelling of cursor."},
	"GET /api/tasks/{id}/messages": listQueryDocs("author, summary or body", listPageSmall, map[string]string{
		"after":   "Only messages after this comment id.",
		"agents":  "true to include other tasks' runs' messages to this task's agent (POST /api/tasks/{id}/agent-messages).",
		"wait_ms": "Block up to this many milliseconds (at most 5000) for a new message to arrive when none is already waiting after the cursor.",
	}),
	"GET /api/capability-servers/gchat-usage": listQueryDocs("space or transport", listPageMedium, map[string]string{
		"id": "The gchat capability server's capability_id.",
	}),
	"GET /api/capability-methods": listQueryDocs("ref, capability, method or description", listPageSmall, map[string]string{
		"kind":   "The run kind the grants are for (task, process or ci): each method then carries policyRefusal, why the capability policy denies it that run.",
		"repo":   "With kind, the repository (owner/name) the run is on.",
		"fields": "full for whole rows: a paged row otherwise cuts description to 500 bytes and each array in args to 10 items (descriptionTruncated, argsTruncated and argItemCounts say so).",
	}),
	"GET /api/capability-servers": listQueryDocs("capability_id, command or URL", listPageSmall, map[string]string{
		"fields": "full for whole rows: a paged row otherwise leaves out schemas and caps status.methods at 40 and each array in config at 10 (methodCount, methodsTruncated, configTruncated and configItemCounts say so).",
	}),
	"GET /api/capability-servers/usage":        listQueryDocs("capability_id", listPageLarge, nil),
	"GET /api/capability-servers/host-entries": listObjectQueryDocs("allowlist entry name", listPageMedium, nil),
	"GET /api/api-scope-sets":                  listObjectQueryDocs("scope set name or source", listPageSmall, nil),
	"GET /api/api-scope-sets/usage":            listQueryDocs("scope set name", listPageLarge, nil),
	"GET /api/notifications":                   listQueryDocs("subject or title", listPageLarge, map[string]string{"open": "1 for only notifications not yet cleared.", "after": "Only notifications after this id.", "family": "Only this family (the subject before its first ':').", "fields": "list for each body cut to its first line, as the UI's lists draw it (a pending release's is kept whole)."}),
	"GET /api/memory": listQueryDocs("repository, author or text", listPageTiny, map[string]string{
		"repo": "Only this repository's (owner/name) standing memory; given empty, only the deployment's own.",
	}),
	"GET /api/memory/documents/{name}":                            {"repo": "The repository (owner/name) whose document to read; the deployment's without one."},
	"GET /api/release-notes":                                      listQueryDocs("week period, title or author", listPageLarge, nil),
	"GET /api/release-notes/versions":                             listObjectQueryDocs("version string", listPageMedium, nil),
	"GET /api/release-notes/stats":                                {"period": "The week whose figures to read; the week just ended without one."},
	"POST /api/repos":                                             {"team": "The team whose targets to add to; the default team without one."},
	"DELETE /api/repos/{owner}/{name}":                            {"team": "The team whose targets to remove from; the default team without one."},
	"GET /api/tasks/{id}/diff-preview":                            {"max": "How many hunks to return (default 6, at most 30)."},
	"GET /api/repos/{owner}/{name}/compare":                       {"base": "The branch or commit to compare from.", "head": "The branch or commit to compare to."},
	"GET /api/repos/{owner}/{name}/workflows":                     listObjectQueryDocs("workflow path or name", listPageTiny, nil),
	"GET /api/repos/{owner}/{name}/releases/{release}/candidates": listQueryDocs("branch, status or release", listPageLarge, nil),
	"GET /api/templates": listQueryDocs("id, name, title or repository", listPageTiny, map[string]string{
		"repo":   "Only templates bound to this repository (owner/name).",
		"fields": "summary to omit each template's prompt description (default limit 20).",
	}),
	"GET /api/schedules": listQueryDocs("name, id, repository, template, suite, source or declared path", listPageSmall, map[string]string{
		"kind":   "Only schedules of these kinds, comma-separated (schedule, workflow).",
		"sort":   "Sort keys, comma-separated (e.g. name, -next_run).",
		"f":      "Filter condition in the Processes page's encoding (field:op:value), repeatable.",
		"fields": "full to include each inline schedule's prompt description on paged calls.",
	}),
	"GET /api/suites":                     listQueryDocs("id or name", listPageLarge, nil),
	"GET /api/boards":                     listQueryDocs("id, name or folder", listPageLarge, nil),
	"GET /api/templates/{id}/composition": {"repo": "The repository (owner/name) the composition is for.", "base": "The base branch the composition is for.", "approved": "false to compose the tasks unapproved."},
	"GET /api/suites/{id}/composition":    {"repo": "The repository (owner/name) the composition is for.", "base": "The base branch the composition is for."},
	"POST /api/suites/composition":        {"repo": "The repository (owner/name) the composition is for.", "base": "The base branch the composition is for."},
	"GET /api/deployment/git-connections": listQueryDocs("credential, host, kind or scope", listPageLarge, map[string]string{
		"host": "Only connections for this git host (github.com when a connection names none).",
	}),
	"GET /api/deployment/secret-audit": listQueryDocs("secret name, key, principal or task id", listPageMedium, nil),
	"GET /api/deployment/audit-logs":   listQueryDocs("kind, resource, credential, principal or task id", listPageLarge, nil),
	"GET /api/deployment/reachable-repos": listQueryDocs("full name (owner/name, or host/owner/name on another git host)", listPageDefault, map[string]string{
		"push":  "true for only repositories the connection can push to -- those a task can be filed against; false for only those it cannot.",
		"owner": "Only this owner's (organization's or user's) repositories.",
		"host":  "Only repositories on this git host; the deployment's default host (github.com unless configured) for the ones a name does not spell a host on.",
	}),
	"GET /api/memory/documents": listQueryDocs("name, repo or summary", listPageLarge, map[string]string{
		"repo": "Only this repository's (owner/name) documents; given empty, only the deployment's own.",
	}),
	"GET /api/repos/{owner}/{name}/branches":        listQueryDocs("name", listPageMedium, nil),
	"GET /api/repos/{owner}/{name}/github-branches": listQueryDocs("name", listPageMedium, nil),
	"GET /api/repos/{owner}/{name}/releases":        listQueryDocs("name or branch", listPageLarge, nil),
	"GET /api/suite-runs": listQueryDocs("suite name or id", listPageSmall, map[string]string{
		"repo":   "Only this repository's (owner/name) runs.",
		"fields": "full for every task of each run: a paged row otherwise carries its first 3 (taskCount and tasksTruncated say so).",
	}),
	"GET /api/ci/faults": listObjectQueryDocs("fault's digest, kind, job, step, file or repository", listPageSmall, map[string]string{
		"repo":   "Only this repository's (owner/name) faults.",
		"fields": "full for every run, branch and file; a paged row otherwise keeps 3, 5 and 5 (runCount, refCount, pathCount).",
	}),
	"GET /api/deployment/config-audit": listQueryDocs("actor, actorLabel, surface or summary", listPageLarge, nil),
	"GET /api/access/history":          listQueryDocs("actor, actorLabel or summary", listPageLarge, nil),
	"GET /api/external-agents/tokens":  listQueryDocs("id, name, owner or scope set", listPageLarge, nil),
	"GET /api/access/principals":       listObjectQueryDocs("principal entry, role, reason or addedBy", listPageMedium, nil),
	"DELETE /api/access/principals/{entry}": {
		"reason":      "Why the entry is removed; required when its role holds a dangerous scope or denies scopes and a signed-in admin removes it.",
		"confirmSelf": "1 to confirm removing an entry that matches the caller itself.",
	},
	"GET /api/hostds":        listQueryDocs("host name or owner", listPageMedium, nil),
	"GET /api/gcp-keys":      listQueryDocs("key name, project id or service account email", listPageLarge, nil),
	"GET /api/github-tokens": listObjectQueryDocs("credential name, capability or connection scope", listPageLarge, nil),
	"GET /api/secrets":       listObjectQueryDocs("secret name or key", listPageMedium, nil),
	"GET /api/agent-models": listObjectQueryDocs("model id, label or family", listPageMedium, map[string]string{
		"framework": "Only this framework's models.",
		"backend":   "Only this model connection's models.",
	}),
	"GET /api/state-repo/files":      listObjectQueryDocs("file path", listPageDefault, nil),
	"GET /api/state-repo/file":       {"path": "The file's path in the state repository.", "limit": "At most this many bytes of it."},
	"GET /api/host/top":              {"lines": "At most this many processes."},
	"GET /api/upgrade/targets":       listObjectQueryDocs("target name or kind", listPageMedium, nil),
	"GET /api/upgrade/release-notes": {"branch": "The branch whose notes to read."},
	"DELETE /api/upgrade/current":    {"reason": "Why the in-progress upgrade is being cancelled (also accepted in the JSON body)."},
	"GET /api/logs/{source}": {
		"lines": "At most this many of the newest lines.",
		"unit": "For the daemon source on a host with both grain's system unit (grain-daemon.service) and its user unit (grain.service): which to read. " +
			"Default is the unit this daemon runs under; the response's unit says which that was, and units lists the choice when there is one.",
	},
	"GET /api/persistent-sandboxes":             listQueryDocs("sandbox name, repository or state", listPageMedium, nil),
	"GET /api/persistent-sandboxes/{name}/logs": {"format": "text for the logs as plain text rather than JSON."},
	"GET /api/console":                          listObjectQueryDocs("task id, title or repository", listPageLarge, nil),
	"GET /api/console-sessions":                 listObjectQueryDocs("session id, title, task id or team", listPageTiny, nil),
	"GET /api/metrics": {
		"window":  "How far back: a duration (36h) or a count of days or weeks (7d, 2w).",
		"since":   "The report's start: a date or an RFC 3339 time.",
		"until":   "The report's end: a date or an RFC 3339 time.",
		"buckets": "How many buckets to divide the report's time series into.",
		"section": "Only this section of the report.",
		"author":  "Only work by this author; comma-separated or repeated for several.",
		"repo":    "Only work on this repository (owner/name); comma-separated or repeated for several.",
	},
	"GET /api/sandboxes":           {"section": "counters for only the counters layers registered (ui.RegisterHealthCounters), or staleSchedules for only the schedules that stopped running, without the sandbox sweep the rest costs."},
	"GET /api/unmatched-failures":  {"window": "How far back: a duration (36h) or a count of days or weeks (7d, 2w).", "limit": "At most this many failures."},
	"DELETE /api/pause":            {"connection": "Lift this model connection's pauses.", "model": "With connection, only this model's pause on it.", "team": "Lift this team's pauses.", "framework": "With team, only this framework's pause."},
	"GET /api/console/{id}/output": {"offset": "Only output after this byte offset."},
	"GET /api/ci/workflows": listObjectQueryDocs("workflow path or name", listPageLarge, map[string]string{
		"repo": "The repository (owner/name) whose workflows to list.",
		"ref":  "The branch or commit to read them at (required: without one the route answers 400).",
	}),
	"GET /api/ci/runs": {
		"repo":       "Only runs of this repository (owner/name).",
		"ref":        "Only runs of a branch matching this, by any part of its name.",
		"conclusion": "Only runs ending so: failure, errored, cancelled, skipped, success, running or pending; comma-separated for several.",
		"job":        "Only runs with this job, by name or id.",
		"workflow":   "Only runs of this workflow, by file name (e.g. nightly.yml), stem (nightly), path, or display name.",
		"since":      "How far back: a span (24h, 7d) or a date.",
		"until":      "Only runs started before this: a span back from now (24h, 7d) or a date; with since, a window.",
		"view":       "compact for each run's identity, conclusion and failing job lines; full (default) for job summaries. Agent rows keep 5 jobs (10 compact lines), failures first (jobCount, jobsTruncated).",
		"fields":     "full for uncut jobs, errors and faults.",
		"sort":       "Order by started, finished or duration.",
		"order":      "asc or desc (the default).",
		"q":          "Substring of id, workflow, job, ref, commit, event, task id (requestedBy) or repository.",
		"limit":      "Max runs (default 10, or 20 for view=compact; max 1000).",
		"cursor":     "Previous nextCursor.",
		"provider":   "Only runs of this CI provider: grain or github-actions. Naming one also lists a GitHub Actions repo's workflow runs for a person's call, which otherwise sees grain's alone.",
	},
	"GET /api/ci/runs/{id}/artifacts": listObjectQueryDocs("job or artifact name", listPageMedium, nil),
	"GET /api/ci/runs/{id}/log": {
		"job":  "The job, by name or id; the first that failed when omitted.",
		"tail": "Only this many lines from the end.",
	},
	"GET /api/merge-batches": listObjectQueryDocs("repository, branch, ref or run id", listPageLarge, map[string]string{
		"repo": "Only this repository's (owner/name) overlap re-checks.",
	}),
	"GET /api/base-branch-gates": listObjectQueryDocs("repository, branch or reason", listPageLarge, map[string]string{
		"repo":   "Only this repository's (owner/name) gates.",
		"fields": "full to include each gate's detail.",
	}),
	"POST /api/package-cache/flush": {"kind": "Empty only this ecosystem's cache: apt, pypi, docker, npm or go."},
	"POST /api/actions-cache/flush": {"repo": "Empty only this repository's (owner/name) entries."},
}

var (
	apiRoutesOnce sync.Once
	apiRoutes     []APIRoute
)

// routeFieldDocs describes a body field whose meaning the field's name
// and type do not carry: keyed by route pattern, then by the field's
// JSON name, each sentence becomes the description of that property in
// the route's input schema. Most bodies need none; the ones here are
// where a caller working from the schema alone got it wrong.
var routeFieldDocs = map[string]map[string]string{
	"POST /api/deployment/git-connections/check": {
		"credential": "The name of a SAVED credential to check (as listed by the deployment's git connections). Leave it empty to check an unsaved connection described by type, value, app and host; it is never a type such as \"ambient\".",
		"type":       "The unsaved connection's kind: \"pat\" (a token in value), \"app\" (a GitHub App in app) or \"ambient\" (the host's own login -- nothing else needed; leave credential empty). Ignored when credential names a saved one.",
		"value":      "The unsaved token (PAT) to check; empty when checking a saved credential or an ambient login.",
		"app":        "The unsaved GitHub App to check: appId, installationId and privateKey.",
		"host":       "The git host to check against, github.com when empty; a saved credential can only be checked on the host it is recorded for.",
		"kind":       "The host's kind, \"github\" or \"git\" (a plain git host, checked by reading its repositories rather than its API).",
		"scope":      "What to check access to: an owner/name repository, an owner (its target repositories on this host), or empty for every target repository on the host.",
		"source":     "For an ambient login, which tool is asked for the token: \"gh\", \"git\" or \"auto\".",
	},
	"PUT /api/console-sessions/{id}": {
		"capabilities":         "The session's whole capability list, replacing what it holds. Send expectCapabilities with it, or a change somebody else made since you read the session is undone.",
		"expectCapabilities":   "The capabilities list you read the session holding (GET /api/console-sessions) and computed capabilities from. If the session no longer holds exactly that set, the call is refused with 409 code \"stale\" and changes nothing; read it again and redo the change.",
		"adoptShippedDefaults": "true adds the shipped defaults the session lacks (missingShippedDefaults) to what it holds now, keeping everything else. Send it instead of capabilities, never with it.",
	},
}

// repoSettingsFieldRoutes names the per-field repository configuration
// routes: GET and PUT /api/repos/{owner}/{name}/<suffix>, each reading
// or writing the one field of the repository's settings named here --
// the field GET /api/repos/{owner}/{name}/settings reports among the
// rest and PUT .../settings (api_set_repo_settings) writes beside the
// others. They stay for the UI, whose panes save one field at a time,
// and run as any route does; grain_explore_scope folds them out of a
// listing unless includeAliases is set, so a reader skimming the repos
// group sees the one route that covers them all rather than twenty
// that each cover a field.
var repoSettingsFieldRoutes = map[string]string{
	"ci-mode":                          "ciMode",
	"ignore-github-ci":                 "ignoreGitHubCI",
	"require-branch-up-to-date":        "requireBranchUpToDate",
	"watch-default-branch":             "watchDefaultBranch",
	"merge-on-green":                   "mergeOnGreen",
	"base-repair-bypasses-human-gates": "baseRepairBypassesHumanGates",
	"unfixable-checks":                 "unfixableChecks",
	"prompt-extension":                 "promptExtension",
	"setup-command":                    "setupCommand",
	"capabilities":                     "defaultCapabilities",
	"ci-capabilities":                  "ciCapabilities",
}

const repoSettingsPathPrefix = "/api/repos/{owner}/{name}/"

// repoSettingsAlias is the whole-document route and the field a
// per-field repository configuration route stands for, or "" for any
// other route.
func repoSettingsAlias(method, path string) (canonical, field string) {
	suffix, ok := strings.CutPrefix(path, repoSettingsPathPrefix)
	if !ok || strings.Contains(suffix, "/") {
		return "", ""
	}
	field, ok = repoSettingsFieldRoutes[suffix]
	if !ok {
		return "", ""
	}
	switch method {
	case http.MethodGet:
		return APIToolPrefix + "get_repo_settings", field
	case http.MethodPut:
		return APIToolPrefix + "set_repo_settings", field
	}
	return "", ""
}

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
			route.Summary = routeSummary(route, s.handlers[pattern])
			route.Description = routeDescription(route)
			route.AliasOf, route.AliasField = repoSettingsAlias(method, path)
			apiRoutes = append(apiRoutes, route)
		}
	})
	return slices.Clone(apiRoutes)
}

func toolNameFor(handler, method, path string) string {
	name := strings.TrimPrefix(handler, "handle")
	if name == "" {
		return APIToolPrefix + strings.ToLower(method) + "_" + pathWords(path)
	}
	return APIToolPrefix + snake(name)
}

// maxAPIToolName bounds a generated tool's bare name: POST /mcp serves
// it as a tool a CLI qualifies as mcp__<server>__<name>
// (mcp.QualifiedToolName), and a model API takes at most 64 characters
// for the whole of that.
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

// routeSummary is the sentence a route is described by: routeDocs' for
// the route, or, for a read with none, its handler's name as words.
func routeSummary(r APIRoute, handler string) string {
	doc := routeDocs[r.Pattern]
	if doc == "" {
		words := strings.ReplaceAll(snake(strings.TrimPrefix(handler, "handle")), "_", " ")
		if words == "" {
			words = strings.ToLower(r.Method) + " " + r.Path
		}
		doc = strings.ToUpper(words[:1]) + words[1:] + "."
	}
	return doc
}

// routeDescription is the route's tool's full description: its summary,
// the route it calls and the scope it needs, and whether it is destructive.
func routeDescription(r APIRoute) string {
	doc := r.Summary
	doc += fmt.Sprintf(" Calls %s through the same handler grain's UI uses (agent API scope %s).", r.Pattern, r.Scope)
	if r.NeedsReason {
		doc += " Destructive: reason is required and is recorded with the call."
	}
	return doc
}

// inputSchema is the route's tool's arguments: each path parameter, a
// reason, the query for a read and the body for a write.
func (r APIRoute) inputSchema() map[string]any {
	props := map[string]any{}
	if r.Method != http.MethodGet || r.NeedsReason {
		props["reason"] = map[string]any{"type": "string",
			"description": "Why this call is being made; recorded with it, and required for a destructive call."}
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
		if docs := routeFieldDocs[r.Pattern]; len(docs) > 0 {
			fields, _ := body["properties"].(map[string]any)
			for name, doc := range docs {
				if field, ok := fields[name].(map[string]any); ok {
					field["description"] = doc
				}
			}
		}
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
// (design/design/durable-tasks.design.md#DT-13).
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
		if !set.Has(route.Scope) && !tasksAdminAllowsTaskPatch(set, route.Pattern) && !capabilitiesDetachAllowsRoute(set, route.Pattern) {
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
// routeCall is one call of an APIRoute as a tool's arguments spelled it:
// the path with its parameters filled in and the query appended, the
// JSON body (nil for none), and the path parameters by name.
type routeCall struct {
	path   string
	body   []byte
	params map[string]string
}

// callArgKeys are the argument names every route's tool takes beside its
// path parameters; anything else given to a route with a body is the
// body's own fields, spelled flat.
var callArgKeys = []string{"reason", "query", "expect", "body"}

// buildCall turns a tool call's arguments into a request of r: each path
// parameter by name (a number where the schema said string is taken as
// its text), `query` as the query string, and on a method with a body
// either `body` as given or, for a route that declares one, every other
// field. The reason and expect are the caller's: a model-only tool reads
// them from more places than a generated one, and checks expect through
// its own channel.
func (r APIRoute) buildCall(args map[string]any) (routeCall, error) {
	call := routeCall{path: r.Path, params: make(map[string]string, len(r.Params))}
	for _, p := range r.Params {
		v, ok := stringArg(args, p)
		if !ok {
			return routeCall{}, fmt.Errorf("%s is required", p)
		}
		call.params[p] = v
		escaped := url.PathEscape(v)
		call.path = strings.Replace(call.path, "{"+p+"...}", escaped, 1)
		call.path = strings.Replace(call.path, "{"+p+"}", escaped, 1)
	}
	if q, ok := args["query"].(map[string]any); ok && len(q) > 0 {
		values := url.Values{}
		for k, v := range q {
			if v == nil {
				continue
			}
			values.Set(k, fmt.Sprint(v))
		}
		if encoded := values.Encode(); encoded != "" {
			call.path += "?" + encoded
		}
	}
	if r.Method == http.MethodGet {
		return call, nil
	}
	var body any
	if given, ok := args["body"]; ok && given != nil {
		body = given
	} else if r.body != nil {
		flat := map[string]any{}
		for k, v := range args {
			if slices.Contains(callArgKeys, k) || slices.Contains(r.Params, k) {
				continue
			}
			flat[k] = v
		}
		if len(flat) > 0 {
			body = flat
		}
	}
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return routeCall{}, fmt.Errorf("encoding body: %w", err)
		}
		call.body = encoded
	}
	return call, nil
}

// stringArg is args[key] as a trimmed, non-empty string: a string
// argument, or a number or other scalar a model sent where the schema
// said string (a task id as 42 rather than "42").
func stringArg(args map[string]any, key string) (string, bool) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return "", false
	}
	var v string
	switch x := raw.(type) {
	case string:
		v = x
	case map[string]any, []any:
		return "", false
	default:
		v = fmt.Sprint(x)
	}
	v = strings.TrimSpace(v)
	return v, v != ""
}

// routeError is the error a route answered a call with: its own message
// when the body is grain's {"error": ...} (and not a structured refusal
// such as config_managed_externally, whose whole body the caller needs),
// and otherwise the body, or the status text of an empty one.
func routeError(code int, body []byte) error {
	var errBody struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if json.Unmarshal(body, &errBody) == nil && errBody.Error != "" && errBody.Code == "" {
		return fmt.Errorf("HTTP %d: %s", code, errBody.Error)
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		trimmed = http.StatusText(code)
	}
	return fmt.Errorf("HTTP %d: %s", code, trimmed)
}

// emptyRouteAnswer is what a call that answered 2xx with no body returns.
func emptyRouteAnswer(method, path string, code int) string {
	return fmt.Sprintf("%s %s: %d %s", method, path, code, http.StatusText(code))
}

func (r APIRoute) call(ctx context.Context, client *HTTPClient, taskID string, args map[string]any) (string, error) {
	if client == nil {
		return "", fmt.Errorf("this run has no grain daemon to call")
	}
	rc, err := r.buildCall(args)
	if err != nil {
		return "", err
	}
	reason, _ := args["reason"].(string)
	reason = strings.TrimSpace(reason)
	if r.NeedsReason && reason == "" {
		return "", fmt.Errorf("reason is required: %s is destructive", r.Pattern)
	}
	if expect, ok := expectation(args); ok && r.takesExpect() {
		id := rc.params["id"]
		detail, err := getTaskAs(ctx, client, taskID, id)
		if err != nil {
			return "", fmt.Errorf("reading task %s to check expect: %w", id, err)
		}
		if err := expect.Check(detail.Task); err != nil {
			return "", err
		}
	}
	path, encoded := rc.path, rc.body
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
		return emptyRouteAnswer(r.Method, path, resp.StatusCode), nil
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
// credential tok, running through s.ServeHTTP, roleGate, and agentAPIGate so
// the same role, scope, repository, rail, and audit checks apply as on HTTP
// requests.
func (r APIRoute) externalCall(ctx context.Context, s *Server, tok *model.ExternalAgentToken, args map[string]any) (string, error) {
	if s == nil {
		return "", fmt.Errorf("this server is not initialized")
	}
	if tok == nil {
		return "", fmt.Errorf("missing external agent credential")
	}
	if err := s.authorizeMCPRoleScope(ctx, r.Pattern); err != nil {
		return "", err
	}
	rc, err := r.buildCall(args)
	if err != nil {
		return "", err
	}
	reason, _ := args["reason"].(string)
	reason = strings.TrimSpace(reason)
	if r.NeedsReason && reason == "" {
		return "", fmt.Errorf("reason is required: %s is destructive", r.Pattern)
	}
	if expect, ok := expectation(args); ok && r.takesExpect() {
		if err := s.authorizeMCPRoleCall(ctx, r.Pattern, rc); err != nil {
			return "", err
		}
		id := rc.params["id"]
		detail, err := getTaskAsExternal(ctx, s, tok, id)
		if err != nil {
			return "", fmt.Errorf("reading task %s to check expect: %w", id, err)
		}
		if err := expect.Check(detail.Task); err != nil {
			return "", err
		}
	}
	path, encoded := rc.path, rc.body
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
		return "", routeError(rec.Code, rec.Body.Bytes())
	}
	data := rec.Body.Bytes()
	if len(bytes.TrimSpace(data)) == 0 {
		return emptyRouteAnswer(r.Method, path, rec.Code), nil
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
