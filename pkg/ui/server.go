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
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

//go:embed static
var staticFS embed.FS

// Server is the JSON API plus the static frontend it serves, a thin HTTP
// shim over a Client. The store behind that Client is the record: every
// answer here is read from it when it is asked for, so there is nowhere
// here for a stale value to hide.
//
// The taskList and taskDetails caches below hold polled task responses,
// because those routes are read every three seconds by open tabs and are
// expensive to rebuild on a real deployment's history. They are not so
// much exceptions to the sentence above as proof obligations for it: each
// entry is keyed on the store's own "has anything been committed?" token,
// so any write anywhere, from any process, invalidates it. The cache type
// comments give that argument in full.
type Server struct {
	tasks *Client
	mux   *http.ServeMux
	// paths carries the same API paths as mux with the method dropped,
	// so an unmatched /api/ request can say whether the path exists at
	// all -- see apiFallback.
	paths *http.ServeMux
	// patterns is every pattern registered on mux, in registration
	// order -- what Routes reports, so tests/security can hold each one
	// to an authorization level without a second copy of this list.
	patterns []string
	// handlers names the handler each pattern was registered with, the
	// name a generated agent API tool is called by (agent_api_tools.go).
	handlers map[string]string
	// Their zero values are empty caches, which is why neither constructor
	// mentions them.
	taskList    taskListCache
	taskDetails taskDetailCache
	dbConfirm   databaseConfirmations
	// access is the single-tenant access gate's rate limiter, and
	// accessCode the token file it checks codes against
	// (localAPITokenNow) -- both accesscode.go's.
	access          accessLimiter
	accessCode      atomic.Pointer[accessCodeFile]
	externalLimiter *externalAgentLimiter
}

// NewServer builds a Server over a store.
func NewServer(cfg Config, store *model.Store) *Server {
	if cfg.Secrets != nil && store != nil {
		cfg.Secrets.SetAuditLogger(store)
	}
	s := &Server{
		tasks:           NewClient(cfg, store),
		mux:             http.NewServeMux(),
		paths:           http.NewServeMux(),
		externalLimiter: newExternalAgentLimiter(),
	}
	s.routes()
	return s
}

// NewServerWithClient builds a Server over an already-configured Client
// -- for a caller that needs to set its clock, or share one Client with a
// non-HTTP path.
func NewServerWithClient(client *Client) *Server {
	if client != nil && client.Config.Secrets != nil && client.Store != nil {
		client.Config.Secrets.SetAuditLogger(client.Store)
	}
	s := &Server{
		tasks:           client,
		mux:             http.NewServeMux(),
		paths:           http.NewServeMux(),
		externalLimiter: newExternalAgentLimiter(),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	if !checkCrossOrigin(w, r) {
		return
	}
	serveRecovered(w, r, http.HandlerFunc(s.serveHTTP))
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if s.tasks != nil {
		r = r.WithContext(WithIAPConfig(r.Context(), IAPConfig{
			Audience:              s.tasks.Config.IAPAudience,
			Issuer:                s.tasks.Config.IAPIssuer,
			KeysURL:               s.tasks.Config.IAPKeysURL,
			Mode:                  s.tasks.Config.IAPMode,
			LocalAPIToken:         s.localAPITokenNow(),
			AccessSessionLifetime: s.tasks.Config.AccessSessionLifetime,
		}))
	}
	// Nobody gets further without the access code -- loopback included
	// (accesscode.go, grain/task-4343) -- unless the request presents a
	// scoped external agent bearer token (gma_...), which authenticates as
	// PrincipalAgent("external:<name>") and is held to its own scopes by
	// agentAPIGate and handleMCP.
	cfg, _ := IAPConfigFromContext(r.Context())
	if externalTokenFromContext(r.Context()) == nil && presentsExternalAgentToken(r) {
		tok, release, ok := s.authenticateExternalAgent(w, r)
		if !ok {
			return
		}
		defer release()
		agent := model.Principal{Kind: model.PrincipalAgent, ID: "external:" + tok.Name}
		r = r.WithContext(withExternalToken(withAuthMethod(WithPrincipal(r.Context(), agent), AuthMethodTrusted), tok))
	}
	if !s.singleTenantGate(w, r, cfg) {
		return
	}
	if singleTenantRefusesIdentity(r) && !s.presentsAccess(r) && !executorBootstrapRoute(r) && externalTokenFromContext(r.Context()) == nil {
		writeError(w, http.StatusUnauthorized, errors.New(singleTenantProxiedRefusal))
		return
	}
	if tok := externalTokenFromContext(r.Context()); tok != nil {
		agent := model.Principal{Kind: model.PrincipalAgent, ID: "external:" + tok.Name}
		r = r.WithContext(withAuthMethod(WithPrincipal(r.Context(), agent), AuthMethodTrusted))
	} else {
		p, method, ok := extractPrincipal(r)
		if ok {
			r = r.WithContext(withAuthMethod(WithPrincipal(r.Context(), p), method))
		}
		if requiresAccess(r) && !ok && !singleTenantTrusted(r) {
			// No verified principal at all. Only a caller this daemon
			// already trusts as itself -- the access code's, or an
			// in-process request -- runs as the deployment's own actor
			// (Client.actor). The gate above already refused anybody else
			// on every route but its own open ones (singleTenantOpenRoute).
			writeError(w, http.StatusUnauthorized, errors.New(singleTenantRefusal))
			return
		}
	}
	// Unarmed until a configuration write's guard finds the deployment
	// managed by an external config repository (checkSettingsWritable).
	rehearsal := &configWriteRehearsal{}
	r = r.WithContext(model.WithWriteRehearsal(r.Context(), rehearsal))
	rw := &configRefusalWriter{ResponseWriter: w, rehearsal: rehearsal}
	s.mux.ServeHTTP(rw, r)
	rw.finish()
}

// requiresAccess reports whether r is an API request that needs an
// allowed caller. The frontend's static files, /api/config (what the
// sign-in page reads) and /api/auth/* (signing in itself) are the only
// things served to a caller nobody has vouched for. Remote execution's
// routes under /api/executors/ authenticate in their own protocol
// (executorBootstrapRoute), as their twins outside /api/ always have.
//
// A persistent sandbox's staging environment under /staging/ is held to
// the same gate as the API: it is somebody's running service, not a page
// of the frontend.
func requiresAccess(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, model.PersistentSandboxSubpathPrefix) || r.URL.Path == "/mcp" {
		return true
	}
	return strings.HasPrefix(r.URL.Path, "/api/") &&
		r.URL.Path != "/api/config" &&
		!strings.HasPrefix(r.URL.Path, "/api/auth/") &&
		!executorBootstrapRoute(r)
}

func (s *Server) routes() {
	s.route("GET /api/config", s.handleConfig)
	s.route("GET /api/restart-watch", s.handleRestartWatch)
	s.route("GET /api/auth/access-code", s.handleAccessCodeStatus)
	s.route("POST /api/auth/access-code", s.handleAccessCodeLogin)
	s.route("GET /api/auth/access-code/login.js", s.handleAccessCodeScript)
	s.route("POST /api/auth/logout", s.handleAuthLogout)
	s.route("GET /api/auth/logout", s.handleAuthLogout)
	s.route("GET /api/auth/user", s.handleAuthUser)
	s.route("GET /api/auth/status", s.handleAuthUser)
	s.route("GET /api/settings", s.handleGetSettings)
	s.route("PUT /api/settings", s.handleUpdateSettings)
	s.route("POST /api/settings", s.handleUpdateSettings)
	s.route("POST /api/settings/plan", s.handlePlanSettings)
	s.route("GET /api/settings/cli-run", s.handleGetCLIRunSettings)
	s.route("PUT /api/settings/cli-run", s.handleUpdateCLIRunSettings)
	s.route("POST /api/capabilities/{id}/check", s.handleCheckCapability)
	s.route("GET /api/tasks", s.handleListTasks)
	s.route("GET /api/tasks/states", s.handleTaskStates)
	s.route("GET /api/states", s.handleTaskStates)
	s.route("POST /api/tasks", s.handleCreateTask)
	s.route("POST /api/tasks/from-template", s.handleCreateTaskFromTemplate)
	s.route("POST /api/tasks/reorder", s.handleReorder)
	s.route("GET /api/tasks/{id}", s.handleGetTask)
	s.route("PATCH /api/tasks/{id}", s.handleUpdateTask)
	s.route("POST /api/tasks/{id}/capabilities", s.handleSetCapability)
	s.route("POST /api/tasks/{id}/depends-on", s.handleSetDependency)
	s.route("POST /api/tasks/{id}/approve", s.handleApprove)
	s.route("POST /api/tasks/{id}/withdraw-approval", s.handleWithdrawApproval)
	s.route("POST /api/tasks/{id}/defer", s.handleDefer)
	s.route("POST /api/tasks/{id}/undefer", s.handleUndefer)
	s.route("POST /api/tasks/{id}/submit", s.handleSubmit)
	s.route("POST /api/tasks/{id}/comments", s.handleAddComment)
	// PUT, not POST: setting the one secret a parked run asked for is an
	// idempotent write of a value at a name the task already fixed --
	// the same shape (and the same verb) /api/secrets/{secret}/{key}
	// already uses for the write this one delegates to.
	s.route("PUT /api/tasks/{id}/secret", s.handleSetTaskSecret)
	s.route("GET /api/tasks/{id}/attachments/{attachmentId}", s.handleGetAttachment)
	s.route("POST /api/tasks/{id}/close", s.handleClose)
	s.route("POST /api/tasks/{id}/stop", s.handleStop)
	s.route("POST /api/tasks/{id}/pause", s.handlePause)
	s.route("POST /api/tasks/{id}/resume", s.handleResume)
	s.route("PUT /api/tasks/{id}/goal", s.handleSetGoal)
	s.route("DELETE /api/tasks/{id}/goal", s.handleClearGoal)
	s.route("POST /api/tasks/{id}/reopen", s.handleReopen)
	s.route("POST /api/tasks/{id}/retry", s.handleRetry)
	s.route("POST /api/tasks/{id}/retry-merge", s.handleRetryMerge)
	// An operator's own override of the merge predicate, reason and all
	// (ui.Client.ForceMerge, grain/task-479). A route rather than a tool:
	// pkg/mcp has nothing for this and must not, because a run that could
	// force its own merge is not an override at all.
	s.route("POST /api/tasks/{id}/force-merge", s.handleForceMerge)
	s.route("POST /api/tasks/{id}/run-now", s.handleRunNow)
	s.route("POST /api/tasks/{id}/pull-request", s.handleOpenPullRequest)
	s.route("POST /api/tasks/{id}/sandbox/recreate", s.handleRecreateSandbox)
	s.route("POST /api/tasks/{id}/activity", s.handleSetTaskActivity)
	// A standing session's own run retitling its chat (mcp's
	// rename_session): the title write PUT /api/console-sessions/{id}
	// makes, reached by the task the run serves rather than by session
	// id, so a run can only ever rename its own (session_title.go).
	s.route("PUT /api/tasks/{id}/session-title", s.handleRenameTaskSession)
	s.route("POST /api/tasks/{id}/staged-attachments", s.handleStageAttachment)
	s.route("POST /api/tasks/{id}/db/query", s.handleTaskDBQuery)
	s.route("POST /api/tasks/{id}/db/execute", s.handleTaskDBExec)
	// One chat turn, from inside the run having it: the run says
	// something and parks its task on the answer (POST), then reads what
	// has been said since (GET), until a person replies. See
	// messages.go, and mcp's chat tool at the other end of it.
	s.route("POST /api/tasks/{id}/messages", s.handlePostTaskMessage)
	s.route("POST /api/tasks/{id}/notes", s.handlePostTaskNote)
	s.route("POST /api/tasks/{id}/notify", s.handleAgentNotify)
	s.route("POST /api/notifications", s.handleRaiseNotification)
	s.route("GET /api/notifications", s.handleListNotifications)
	s.route("POST /api/notifications/{id}/ack", s.handleAckNotification)
	s.route("POST /api/notifications/{id}/clear", s.handleClearNotification)
	s.route("POST /api/notifications/{id}/investigate", s.handleInvestigateNotification)
	s.route("GET /api/tasks/{id}/messages", s.handleListTaskMessages)
	s.route("POST /api/tasks/{id}/agent-messages", s.handleMessageTaskAgent)
	// And what of it the run has been shown, so that the end of the run
	// can tell a message it answered from one it never saw
	// (Observation.DeliveredCommentID).
	s.route("POST /api/tasks/{id}/delivered", s.handleRecordDelivered)
	s.route("POST /api/tasks/{id}/wait-credit", s.handleCreditWait)
	s.route("POST /api/tasks/{id}/change-checks", s.handleChangeChecks)
	// A CL's review loop: a run's draft replies (internal), and a
	// person's Publish replies and Mail (change_review.go).
	s.route("POST /api/tasks/{id}/change-review", s.handleChangeReview)
	s.route("POST /api/tasks/{id}/change-publish", s.handlePublishReplies)
	s.route("POST /api/tasks/{id}/change-mail", s.handleMailChange)
	// A CL's Sync button (change_submit.go); its Submit is the task's
	// own Submit route.
	s.route("POST /api/tasks/{id}/change-sync", s.handleSyncChange)
	// The wait_for_task tool, from inside the run that is waiting: check
	// and describe the tasks it names, or park its own task on them as
	// depends-on links (wait_task.go).
	s.route("POST /api/tasks/{id}/wait-on", s.handleWaitOn)
	s.route("POST /api/tasks/{id}/park-until", s.handleParkUntil)
	s.route("POST /api/tasks/{id}/resume-later", s.handleResumeLater)
	s.route("POST /api/tasks/{id}/local-script-approvals", s.handleRequestLocalScriptApproval)
	s.route("POST /api/tasks/{id}/local-script-approvals/{approval}/wait", s.handleWaitLocalScriptApproval)
	s.route("POST /api/tasks/{id}/local-script-approvals/{approval}/expire", s.handleExpireLocalScriptApproval)
	s.route("POST /api/tasks/{id}/local-script-approvals/{approval}/answer", s.handleAnswerLocalScriptApproval)
	s.route("GET /api/local-script-approvals", s.handleListLocalScriptApprovals)
	// What grain has learned about working here (model.Memory): read by
	// anyone, written only by a run whose task holds the memory
	// capability -- the nightly memory agent through update_memory -- or
	// by a system admin editing one by hand (Server.memoryWriter), since
	// every run's prompt carries it.
	s.route("GET /api/memory", s.handleListMemories)
	s.route("PUT /api/memory", s.handlePutMemory)
	s.route("GET /api/memory/documents", s.handleListMemoryDocuments)
	s.route("PUT /api/memory/documents", s.handlePutMemoryDocument)
	s.route("GET /api/memory/documents/{name}", s.handleGetMemoryDocument)
	// The weekly account of this deployment's own work (releasenotes.go).
	// The stats route is registered before the {period} one it would
	// otherwise be read as a week by -- ServeMux prefers the more
	// specific pattern, but the two reading alike is worth saying out
	// loud beside them.
	s.route("GET /api/release-notes", s.handleListReleaseNotes)
	s.route("PUT /api/release-notes", s.handlePutReleaseNote)
	s.route("GET /api/release-notes/stats", s.handleClusterStats)
	s.route("GET /api/release-notes/{period}", s.handleGetReleaseNote)
	// The notes each grain release shipped with, out of this binary
	// (releasenotes_versions.go) -- per version rather than per week, so
	// under a literal segment no week is named, as stats is.
	s.route("GET /api/release-notes/versions", s.handleListReleaseVersions)
	s.route("GET /api/release-notes/versions/{version}", s.handleGetReleaseVersion)
	s.route("GET /api/tasks/{id}/attempts/{number}/transcript", s.handleGetAttemptTranscript)
	s.route("GET /api/tasks/{id}/attempts/{number}/capability-audit", s.handleGetAttemptCapabilityAudit)
	s.route("GET /api/tasks/{id}/prompt", s.handleGetTaskPrompt)
	s.route("GET /api/tasks/{id}/tree", s.handleGetTaskTree)

	s.route("POST /api/repos", s.handleAddTargetRepo)
	s.route("POST /api/repos/create", s.handleCreateRepo)
	s.route("GET /api/repo-hosts", s.handleRepoHosts)
	s.route("DELETE /api/repos/{owner}/{name}", s.handleRemoveTargetRepo)

	s.route("GET /api/repos/{owner}/{name}/ci-capabilities", s.handleGetRepoCapabilities)
	s.route("PUT /api/repos/{owner}/{name}/ci-capabilities", s.handleSetRepoCICapabilities)
	s.route("GET /api/repos/{owner}/{name}/release", s.handleGetRepoRelease)
	s.route("PUT /api/repos/{owner}/{name}/release", s.handleSetRepoRelease)
	s.route("PUT /api/repos/{owner}/{name}/release-secrets/{secret}", s.handleSetRepoReleaseSecret)
	s.route("DELETE /api/repos/{owner}/{name}/release-secrets/{secret}", s.handleDeleteRepoReleaseSecret)
	s.route("POST /api/repos/{owner}/{name}/release-runs", s.handleStartRepoRelease)
	// An operator asking for a base branch's own check to be run now,
	// without waiting for a merge to move the tip (base_branch_runs.go,
	// grain/task-480). It records a request on the branch's row; the
	// base-branch reconciler is what starts the run.
	s.route("POST /api/repos/{owner}/{name}/base-branch-runs", s.handleRequestBaseBranchRun)
	s.route("GET /api/repos/{owner}/{name}/work-ownership", s.handleGetRepoWorkOwnership)
	s.route("PUT /api/repos/{owner}/{name}/work-ownership", s.handleSetRepoWorkOwnership)
	s.route("GET /api/repos/{owner}/{name}/capabilities", s.handleGetRepoCapabilities)
	s.route("PUT /api/repos/{owner}/{name}/capabilities", s.handleSetRepoCapabilities)
	s.route("GET /api/repos/{owner}/{name}/prompt-extension", s.handleGetRepoPromptExtension)
	s.route("PUT /api/repos/{owner}/{name}/prompt-extension", s.handleSetRepoPromptExtension)
	s.route("GET /api/repos/{owner}/{name}/setup-command", s.handleGetRepoSetupCommand)
	s.route("PUT /api/repos/{owner}/{name}/setup-command", s.handleSetRepoSetupCommand)
	s.route("GET /api/repos/{owner}/{name}/declarations", s.handleGetRepoDeclarations)
	s.route("PUT /api/repos/{owner}/{name}/declarations", s.handleSetRepoDeclarations)
	s.route("GET /api/repos/{owner}/{name}/require-branch-up-to-date", s.handleGetRepoRequireBranchUpToDate)
	s.route("PUT /api/repos/{owner}/{name}/require-branch-up-to-date", s.handleSetRepoRequireBranchUpToDate)
	s.route("GET /api/repos/{owner}/{name}/ignore-github-ci", s.handleGetRepoIgnoreGitHubCI)
	s.route("PUT /api/repos/{owner}/{name}/ignore-github-ci", s.handleSetRepoIgnoreGitHubCI)
	s.route("GET /api/repos/{owner}/{name}/ci-mode", s.handleGetRepoCIMode)
	s.route("PUT /api/repos/{owner}/{name}/ci-mode", s.handleSetRepoCIMode)
	s.route("GET /api/repos/{owner}/{name}/settings", s.handleGetRepoSettings)
	s.route("PUT /api/repos/{owner}/{name}/settings", s.handleSetRepoSettings)
	s.route("GET /api/repos/{owner}/{name}/watch-default-branch", s.handleGetRepoWatchDefaultBranch)
	s.route("PUT /api/repos/{owner}/{name}/watch-default-branch", s.handleSetRepoWatchDefaultBranch)
	s.route("GET /api/repos/{owner}/{name}/merge-on-green", s.handleGetRepoMergeOnGreen)
	s.route("PUT /api/repos/{owner}/{name}/merge-on-green", s.handleSetRepoMergeOnGreen)
	s.route("GET /api/repos/{owner}/{name}/base-repair-bypasses-human-gates", s.handleGetRepoBaseRepairBypassesHumanGates)
	s.route("PUT /api/repos/{owner}/{name}/base-repair-bypasses-human-gates", s.handleSetRepoBaseRepairBypassesHumanGates)
	s.route("GET /api/repos/{owner}/{name}/unfixable-checks", s.handleGetRepoUnfixableChecks)
	s.route("PUT /api/repos/{owner}/{name}/unfixable-checks", s.handleSetRepoUnfixableChecks)
	s.route("GET /api/repos/{owner}/{name}/workflows", s.handleGetRepoWorkflows)
	s.route("PUT /api/repos/{owner}/{name}/workflows", s.handleSetRepoWorkflows)
	s.route("GET /api/repos/{owner}/{name}/lifecycle", s.handleGetRepoLifecycle)
	s.route("POST /api/repos/{owner}/{name}/lifecycle/refresh", s.handleRefreshRepoLifecycle)
	s.route("POST /api/repos/{owner}/{name}/import-issues", s.handleImportIssues)

	s.route("GET /api/repos/{owner}/{name}/releases", s.handleListReleases)
	s.route("POST /api/repos/{owner}/{name}/releases", s.handleCreateRelease)
	s.route("GET /api/repos/{owner}/{name}/release-creation", s.handleGetReleaseCreation)
	s.route("GET /api/repos/{owner}/{name}/releases/{release}", s.handleGetRelease)
	s.route("DELETE /api/repos/{owner}/{name}/releases/{release}", s.handleDeleteRelease)
	s.route("POST /api/repos/{owner}/{name}/releases/{release}/merge", s.handleRequestReleaseMerge)
	s.route("GET /api/repos/{owner}/{name}/releases/{release}/candidates", s.handleListCandidates)
	s.route("POST /api/repos/{owner}/{name}/releases/{release}/candidates", s.handleCutCandidate)
	s.route("POST /api/repos/{owner}/{name}/releases/{release}/candidates/promote", s.handlePromoteCandidate)
	s.route("POST /api/repos/{owner}/{name}/pending-releases/{version}/approve", s.handleApproveRelease)
	s.route("POST /api/repos/{owner}/{name}/pending-releases/{version}/reject", s.handleRejectRelease)

	s.route("GET /api/repos/{owner}/{name}/branches", s.handleListBranches)
	s.route("POST /api/repos/{owner}/{name}/branches", s.handleCreateBranch)
	s.route("GET /api/repos/{owner}/{name}/github-branches", s.handleListGitHubBranches)
	s.route("GET /api/repos/{owner}/{name}/compare", s.handleCompareBranches)

	s.route("GET /api/repos/{owner}/{name}/qualification-plan", s.handleGetQualificationPlan)
	s.route("PUT /api/repos/{owner}/{name}/qualification-plan", s.handlePutQualificationPlan)
	s.route("GET /api/repos/{owner}/{name}/candidates/{id}/qualification", s.handleGetCandidateQualification)
	s.route("POST /api/repos/{owner}/{name}/candidates/{id}/qualification/approve", s.handleApproveQualificationRun)

	s.route("GET /api/schedules", s.handleListSchedules)
	s.route("POST /api/schedules", s.handleCreateSchedule)
	s.route("PATCH /api/schedules/{id}", s.handleUpdateSchedule)
	s.route("POST /api/schedules/{id}/run", s.handleRunSchedule)
	s.route("GET /api/schedules/{id}/composition", s.handleScheduleComposition)
	// A POST that writes nothing: the schedule being composed is the
	// body of a form still being edited, which has no id to name it by.
	s.route("POST /api/schedules/composition", s.handleDraftScheduleComposition)
	s.route("DELETE /api/schedules/{id}", s.handleDeleteSchedule)

	s.route("GET /api/templates", s.handleListTemplates)
	s.route("POST /api/templates", s.handleCreateTemplate)
	s.route("GET /api/templates/{id}/composition", s.handleTemplateComposition)
	s.route("PATCH /api/templates/{id}", s.handleUpdateTemplate)
	s.route("DELETE /api/templates/{id}", s.handleDeleteTemplate)

	s.route("GET /api/suites", s.handleListSuites)
	s.route("POST /api/suites", s.handleCreateSuite)
	s.route("PATCH /api/suites/{id}", s.handleUpdateSuite)
	s.route("GET /api/suites/{id}/composition", s.handleSuiteComposition)
	s.route("POST /api/suites/composition", s.handleDraftSuiteComposition)
	s.route("DELETE /api/suites/{id}", s.handleDeleteSuite)
	s.route("GET /api/suite-runs", s.handleListSuiteRuns)
	s.route("POST /api/suite-runs", s.handleCreateSuiteRun)
	s.route("GET /api/suite-runs/{id}", s.handleGetSuiteRun)

	s.route("GET /api/boards", s.handleListBoards)
	s.route("POST /api/boards", s.handleCreateBoard)
	s.route("PATCH /api/boards/{id}", s.handleUpdateBoard)
	s.route("DELETE /api/boards/{id}", s.handleDeleteBoard)

	s.route("GET /api/git-ambient-sources", s.handleListGitAmbientSources)
	s.deploymentRoutes()

	s.route("GET /api/agent-models", s.handleListAgentModels)

	s.route("GET /api/executors/tokens", s.handleListExecutorTokens)
	s.route("POST /api/executors/tokens", s.handleCreateExecutorToken)
	s.route("POST /api/executors/tokens/{id}/revoke", s.handleRevokeExecutorToken)
	s.route("DELETE /api/executors/tokens/{id}", s.handleRevokeExecutorToken)
	s.route("DELETE /api/executors/tokens/{id}/revoke", s.handleRevokeExecutorToken)

	s.route("GET /api/external-agents/tokens", s.handleListExternalAgentTokens)
	s.route("POST /api/external-agents/tokens", s.handleCreateExternalAgentToken)
	s.route("POST /api/external-agents/tokens/{id}/rotate", s.handleRotateExternalAgentToken)
	s.route("POST /api/external-agents/tokens/{id}/revoke", s.handleRevokeExternalAgentToken)
	s.route("DELETE /api/external-agents/tokens/{id}", s.handleDeleteExternalAgentToken)
	s.route("POST /api/external-agents/tokens/revoke-all", s.handleRevokeAllExternalAgentTokens)
	s.route("POST /mcp", s.handleMCP)

	s.route("GET /api/agent-keys", s.handleListAgentKeys)
	s.route("PUT /api/agent-keys/{framework}", s.handleSetAgentKey)
	s.route("DELETE /api/agent-keys/{framework}", s.handleDeleteAgentKey)
	s.route("PUT /api/agent-backends/{id}/credential", s.handleSetAgentBackendCredential)
	s.route("DELETE /api/agent-backends/{id}/credential", s.handleDeleteAgentBackendCredential)
	s.route("GET /api/agent-backends/{id}/codex-auth", s.handleBackendCodexAuth)
	s.route("PUT /api/agent-backends/{id}/codex-auth", s.handleBackendCodexAuth)
	s.route("DELETE /api/agent-backends/{id}/codex-auth", s.handleBackendCodexAuth)
	s.route("PUT /api/codex-auth", s.handleSetCodexAuth)
	s.route("DELETE /api/codex-auth", s.handleDeleteCodexAuth)
	// The one route in this family that asks the vendor rather than the
	// store: whether the credential set above is still one that works.
	s.route("POST /api/agent-keys/{framework}/check", s.handleCheckAgentKey)

	// The deployment's capability servers (capability_servers.go). The
	// capability_id is a URL, so it travels in the body, not the path.
	s.route("GET /api/capability-servers", s.handleListCapabilityServers)
	s.route("GET /api/capability-methods", s.handleListCapabilityMethods)
	s.route("POST /api/capability-grants/check", s.handleCheckCapabilityGrants)
	s.route("PUT /api/capability-servers", s.handleSetCapabilityServer)
	s.route("POST /api/capability-servers/remove", s.handleCapabilityServerAction("remove"))
	s.route("POST /api/capability-servers/enable", s.handleCapabilityServerAction("enable"))
	s.route("POST /api/capability-servers/disable", s.handleCapabilityServerAction("disable"))
	s.route("POST /api/capability-servers/dismiss-notice", s.handleCapabilityServerAction("dismiss-notice"))
	s.route("POST /api/capability-servers/rotate-managed", s.handleCapabilityServerAction("rotate-managed"))
	s.route("POST /api/capability-servers/bind-secret", s.handleBindCapabilityServerSecret)
	s.route("POST /api/capability-servers/test", s.handleTestCapabilityServer)
	s.route("POST /api/capability-servers/test-script", s.handleTestHostScript)
	s.route("POST /api/capability-servers/gchat-test", s.handleTestGChatPost)
	s.route("GET /api/capability-servers/gchat-usage", s.handleGChatUsage)
	s.route("GET /api/capability-servers/host-entries", s.handleCapabilityServerHostEntries)
	s.route("GET /api/capability-servers/usage", s.handleCapabilityServerUsage)
	s.route("GET /api/capability-policy", s.handleGetCapabilityPolicy)
	s.route("PUT /api/capability-policy", s.handleSetCapabilityPolicy)
	s.route("GET /api/api-scope-sets", s.handleListAPIScopeSets)
	s.route("GET /api/api-scope-sets/usage", s.handleAPIScopeSetUsage)
	s.route("PUT /api/api-scope-sets", s.handlePutAPIScopeSet)
	s.route("POST /api/api-scope-sets/delete", s.handleDeleteAPIScopeSet)
	s.route("POST /api/capability-policy/preview", s.handlePreviewCapabilityPolicy)

	s.route("GET /api/gcp-keys", s.handleListGCPKeys)
	s.route("POST /api/gcp-keys", s.handleSetGCPKey)
	s.route("GET /api/gcp-keys/{name}", s.handleGetGCPKey)
	s.route("PUT /api/gcp-keys/{name}", s.handleSetGCPKey)
	s.route("DELETE /api/gcp-keys/{name}", s.handleDeleteGCPKey)
	s.route("GET /api/github-tokens", s.handleListGitHubTokens)
	s.route("PUT /api/github-tokens/{name}", s.handleSetGitHubToken)
	s.route("DELETE /api/github-tokens/{name}", s.handleDeleteGitHubToken)

	s.route("GET /api/secrets", s.handleListSecrets)
	s.route("PUT /api/secrets/{secret}/{key}", s.handleSetSecret)
	s.route("DELETE /api/secrets/{secret}/{key}", s.handleDeleteSecretKey)
	s.route("DELETE /api/secrets/{secret}", s.handleDeleteSecret)

	s.route("GET /api/state-repo", s.handleGetStateRepo)
	// Reading the exported configuration, which a person running this
	// deployment through a browser otherwise cannot do at all: the
	// repository is controller-local, so there is no forge page for it.
	// Read-only, and an allowlist of the paths grain writes rather than
	// a file server for the working tree -- see pkg/staterepo/browse.go.
	s.route("GET /api/state-repo/files", s.handleListStateRepoFiles)
	s.route("GET /api/state-repo/file", s.handleGetStateRepoFile)
	s.route("POST /api/state-repo/sync", s.handleSyncStateRepo)
	s.route("POST /api/state-repo/pull-requests", s.handleOpenConfigPullRequest)
	s.route("POST /api/state-repo/remote", s.handleSetStateRepoRemote)
	s.route("POST /api/state-repo/secrets-key", s.handleImportSecretsKey)
	s.route("POST /api/state-repo/import-key", s.handleImportSecretsKey)
	s.route("POST /api/state-repo/secrets-repo", s.handleSetSecretsRepo)
	s.route("POST /api/state-repo/reset-notice", s.handleDismissResetNotice)

	s.route("POST /api/host/reboot", s.handleRebootHost)
	s.route("POST /api/host/shutdown", s.handleShutdownHost)
	s.route("POST /api/host/restart", s.handleRestartDaemon)
	s.route("GET /api/host/top", s.handleGetHostTop)
	s.route("GET /api/upgrade", s.handleGetUpgradeStatus)
	s.route("POST /api/upgrade", s.handleStartUpgrade)
	s.route("DELETE /api/upgrade/scheduled", s.handleCancelScheduledUpgrade)
	s.route("GET /api/upgrade/targets", s.handleGetUpgradeTargets)
	s.route("GET /api/upgrade/release-notes", s.handleUpgradeReleaseNotes)
	s.route("GET /api/database", s.handleGetDatabase)
	s.route("POST /api/database/test", s.handleTestDatabaseConnection)
	s.route("POST /api/database/upgrade", s.handleUpgradeDatabase)
	s.route("POST /api/upgrade/postgres", s.handleUpgradeDatabase)
	s.route("POST /api/settings/db/migrate-to-postgres", s.handleUpgradeDatabase)

	s.route("GET /api/logs", s.handleListLogSources)
	s.route("GET /api/logs/{source}", s.handleGetLogLines)

	s.route("GET /api/sandboxes", s.handleGetSandboxHealth)
	s.route("GET /api/persistent-sandboxes", s.handleListPersistentSandboxes)
	s.route("GET /api/persistent-sandboxes/{name}/logs", s.handlePersistentSandboxLogs)
	s.route("POST /api/persistent-sandboxes/{name}/{action}", s.handlePersistentSandboxAction)
	// A persistent sandbox's HTTP service, under the UI (model.PersistentSandbox.UISubpath).
	s.route("/staging/{name}/{path...}", s.handleStagingProxy)

	s.route("GET /api/metrics", s.handleGetMetrics)

	// What failed runs said when grain could not name why -- the
	// instrument over its own refusal markers (pkg/ui/unmatched.go).
	s.route("GET /api/unmatched-failures", s.handleListUnmatchedFailures)

	s.route("GET /api/pause", s.handleGetAgentPause)
	s.route("DELETE /api/pause", s.handleLiftAgentPause)

	// The interactive consoles: which sessions are live, what one has
	// printed, and the two things a person does with one -- type into it
	// and close it. See pkg/console.
	s.route("GET /api/console", s.handleListConsoles)
	s.route("GET /api/console/{id}/output", s.handleConsoleOutput)
	s.route("POST /api/console/{id}/input", s.handleConsoleInput)
	s.route("POST /api/console/{id}/resize", s.handleConsoleResize)
	s.route("DELETE /api/console/{id}", s.handleCloseConsole)

	// The standing sessions this deployment keeps open -- the director
	// among them -- and what a person may change about one. A different
	// thing entirely from the live terminals above, which share the word
	// and nothing else: see console_sessions.go's own note.
	s.route("GET /api/console-sessions", s.handleListConsoleSessions)
	s.route("POST /api/console-sessions", s.handleCreateConsoleSession)
	s.route("PUT /api/console-sessions/{id}", s.handleUpdateConsoleSession)
	s.route("DELETE /api/console-sessions/{id}", s.handleDeleteConsoleSession)
	s.route("POST /api/console-sessions/{id}/restart", s.handleRestartConsoleSession)
	s.route("POST /api/console-sessions/{id}/cancel", s.handleCancelConsoleSession)
	s.route("POST /api/ci/runs", s.handleStartCIRun)
	s.route("GET /api/ci/workflows", s.handleListCIWorkflows)
	s.route("GET /api/ci/runs", s.handleListCIRuns)
	s.route("GET /api/ci/faults", s.handleListCIFaults)
	s.route("GET /api/ci/runs/{id}", s.handleGetCIRun)
	s.route("DELETE /api/ci/runs/{id}", s.handleCancelCIRun)
	s.route("GET /api/ci/runs/{id}/artifacts", s.handleListCIRunArtifacts)
	s.route("GET /api/ci/runs/{id}/artifacts/{job}/{name}", s.handleGetCIArtifact)
	// The job cache behind those runs: what it holds, and the one thing
	// an operator does to it.
	s.route("GET /api/ci/cache", s.handleGetCICache)
	s.route("DELETE /api/ci/cache", s.handleClearCICache)

	// The merge queue's speculative batches: which changes one suite is
	// deciding together (pkg/ui/mergebatches.go). Read-only, and beside
	// the CI routes because what a batch mostly is, to whoever is
	// watching it, is one grain CI run with several changes riding on it.
	s.route("GET /api/merge-batches", s.handleListMergeBatches)
	s.route("GET /api/pipeline/changes", s.handlePipelineChanges)

	// The base branches this deployment's merge queue is merging onto
	// that nothing is judging (pkg/ui/basebranchgate.go). The same
	// reading the health response carries, on a route of its own because
	// the inbox shows these and the rail counts them: that is a request
	// in every view, and GET /api/sandboxes -- where this otherwise
	// lives -- probes every live guest to answer. Read-only; every repair
	// for one of these is an operator's, elsewhere.
	s.route("GET /api/base-branch-gates", s.handleListBaseBranchGates)

	// The proxy cache: what it holds, and the button that empties it
	// (pkg/ui/packagecache.go). Whether sandboxes are pointed at it at
	// all is a setting rather than a route, on the Settings form with
	// the rest of what a sandbox is built with.
	s.route("GET /api/package-cache", s.handleGetPackageCache)
	s.route("POST /api/package-cache/flush", s.handleFlushPackageCache)
	s.route("GET /api/actions-cache", s.handleGetActionsCache)
	s.route("POST /api/actions-cache/flush", s.handleFlushActionsCache)

	s.route("GET /api/pipeline-pause", s.handleGetPipelinePause)
	s.route("POST /api/pipeline-pause", s.handlePausePipeline)
	s.route("DELETE /api/pipeline-pause", s.handleResumePipeline)

	// Remote executors / model servers API and bootstrap scripts.
	s.route("GET /api/executors", s.handleListExecutors)
	s.route("GET /api/model-servers/install.sh", s.handleExecutorInstallScript)
	s.route("GET /api/executors/install.sh", s.handleExecutorInstallScript)
	s.route("GET /install-executor.sh", s.handleExecutorInstallScript)
	s.route("GET /api/executors/bin/{binary}", s.handleExecutorBinary)
	s.route("GET /install/{os}/{arch}", s.handleExecutorBinaryByArch)

	// Remote executor inbound connections over HTTP Upgrade / Cloud Run reverse tunnel.
	s.route("GET /_grain/executor", s.handleExecutorConnect)
	s.route("GET /api/executors/connect", s.handleExecutorConnect)
	s.route("GET /api/executors/stream", s.handleExecutorConnect)
	// A grain hostd on another machine dialing in (hostd -server).
	s.route("GET /api/hostd/connect", s.handleHostdConnect)

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		// staticFS is embedded at build time from a directory this package
		// ships -- its absence is a build-time mistake, not a runtime
		// condition a caller can do anything about.
		panic("ui: embedding static/: " + err.Error())
	}
	s.mux.Handle("/", s.apiFallback(spaHandler(static)))
	s.patterns = append(s.patterns, "/")
}

// Routes is every pattern this server's mux serves, "METHOD /path" for
// the API and "/" for the frontend that answers everything else. It is
// read off the same calls that register them, so a new route is in it
// the moment it exists -- tests/security's route-authorization table is
// what holds each one to who may call it.
func (s *Server) Routes() []string {
	return append([]string(nil), s.patterns...)
}

// route registers one "METHOD /path" handler, and records the path on
// its own mux so apiFallback can tell a path that does not exist from
// one reached with the wrong method.
// timedRoutes are the routes logged when they take slowRequestThreshold
// or longer: saving a model connection and the reads the Team settings
// pane makes around it, so a report of a slow save says which of them
// the time went on (grain/task-3903).
var timedRoutes = map[string]bool{
	"GET /api/config":   true,
	"GET /api/settings": true,
	"PUT /api/settings": true,
	"PUT /api/deployment/agent-backends/{backendId}":    true,
	"DELETE /api/deployment/agent-backends/{backendId}": true,
	"GET /api/agent-models":                             true,
	"PUT /api/agent-backends/{id}/credential":           true,
	"DELETE /api/agent-backends/{id}/credential":        true,
}

const slowRequestThreshold = time.Second

func (s *Server) route(pattern string, handler http.HandlerFunc) {
	if s.handlers == nil {
		s.handlers = map[string]string{}
	}
	s.handlers[pattern] = handlerName(handler)
	// Every route answers an acting agent only within its scope
	// (route_scopes.go, agent_api_gate.go).
	handler = s.agentAPIGate(pattern, handler)
	if timedRoutes[pattern] {
		next := handler
		handler = func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			next(w, r)
			if d := time.Since(start); d >= slowRequestThreshold {
				log.Printf("grain ui: slow request: %s took %s", pattern, d.Round(time.Millisecond))
			}
		}
	}
	s.mux.HandleFunc(pattern, handler)
	s.patterns = append(s.patterns, pattern)
	_, path, ok := strings.Cut(pattern, " ")
	if !ok {
		return
	}
	if _, matched := s.paths.Handler(&http.Request{Method: http.MethodGet, URL: &url.URL{Path: path}}); matched == path {
		return // a second method on a path already recorded
	}
	s.paths.Handle(path, knownAPIPath{})
}

// knownAPIPath is the sentinel Server.paths is filled with: it is never
// served, only recognised, and being a named type rather than a closure
// is what lets apiFallback tell it apart from the redirect handler a
// ServeMux can return instead.
type knownAPIPath struct{}

func (knownAPIPath) ServeHTTP(http.ResponseWriter, *http.Request) {}

// apiFallback answers an /api/ request no route matched, instead of
// letting it fall through to next -- the SPA, which would answer any
// wrong path or wrong method with 200 and a page of HTML.
//
// That mattered beyond tidiness (found by hand, task 244): every caller
// of this API expects JSON. A CLI a version out of step with its daemon,
// an MCP tool hop like open_pull_request landing on a daemon that has no
// such route, or a typo in a curl -- each got "200 OK" and then failed
// somewhere further on parsing "<!doctype html>", naming a character
// rather than the endpoint.
func (s *Server) apiFallback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if h, _ := s.paths.Handler(r); h != nil {
			if _, known := h.(knownAPIPath); known {
				writeError(w, http.StatusMethodNotAllowed,
					fmt.Errorf("%s is not allowed on %s", r.Method, r.URL.Path))
				return
			}
		}
		writeError(w, http.StatusNotFound,
			fmt.Errorf("no such API endpoint: %s %s", r.Method, r.URL.Path))
	})
}

// spaHandler serves static assets straight out of fsys, the same as
// http.FileServerFS -- except a request for a path that isn't an actual
// file falls back to index.html instead of 404ing. App.jsx (bwsalmon/
// agents#548) now reads a sub-page like "/repos" or "/tasks/42" out of
// the URL itself, but that only works for a client-side navigation that
// starts from "/" already loaded; a direct hit on that URL -- a bookmark,
// a shared link, a hard refresh -- is a real request this server has to
// answer, and the only page it has to answer with is the one App.jsx's
// own routing already knows how to parse.
//
// A //go:embed'd static/ with no built frontend yet (a fresh checkout
// under `go test`, where static/ carries only placeholder.html) has no
// index.html to fall back to; that case is left to fall through to the
// normal file-not-found response rather than serving placeholder.html
// in its place.
func spaHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServerFS(fsys)
	index, indexErr := fs.ReadFile(fsys, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := strings.TrimPrefix(r.URL.Path, "/")
		if upath != "" {
			if _, err := fs.Stat(fsys, upath); err != nil && indexErr == nil {
				http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}
