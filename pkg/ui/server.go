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
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
)

//go:embed static
var staticFS embed.FS

// staticNextFS is the Angular UI (ui-ng/, design/design/angular-ui.design.md),
// built into static-next/ by `make frontend` and served under /next/ beside
// the React UI in static/. Same embedding, same placeholder arrangement.
//
//go:embed static-next
var staticNextFS embed.FS

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
	// hostdImage is the image /api/hostd/install.sh suggests
	// (hostdInstallImage), hostd_install.go's.
	hostdImage      hostdImageCache
	agentdRelease   agentdReleaseCache
	hostdJoinReplay hostd.NonceReplayCache
	// access is the single-tenant access gate's rate limiter, and
	// accessCode the token file it checks codes against
	// (localAPITokenNow) -- both accesscode.go's.
	access     accessLimiter
	accessCode atomic.Pointer[accessCodeFile]
	// accessCodeMode is the code's mode file and accessCodeNotices the
	// throttle on its recovery-use notifications (access_code_mode.go).
	accessCodeMode    atomic.Pointer[accessCodeFile]
	accessCodeNotices accessNoticeThrottle
	externalLimiter   *externalAgentLimiter
	defaultGroupsOnce sync.Once
	defaultGroups     GroupMemberChecker
	// controllerMCPHandler serves POST /api/mcp (mcp_endpoint.go), built
	// once on first use.
	controllerMCPOnce    sync.Once
	controllerMCPHandler http.Handler
	// fsNamespace is the namespace GET /api/fs/ serves (api_fs.go).
	fsNamespace func() *namespace.Server
	// eventsKeepalive overrides defaultEventsKeepalive in tests (events.go).
	eventsKeepalive time.Duration
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

const maxTunnelRequestBodyBytes = 64 << 20 // 64 MiB cap on non-upgraded tunnel request bodies

// HubTunnelHandler returns an http.Handler for serving accepted mux
// streams on an outbound registry tunnel (-hub-connect). Every tunneled
// request is gated unconditionally behind the "registry" IdentityVerifier
// (X-Grain-Registry-Assertion, Source == "registry")—including open routes like
// /api/config, /api/auth/*, and /api/hostd/connect—and loopback or access-code
// shortcuts (grain_access cookies, X-Grain-Access-Code headers, and bearer
// access codes) are unconditionally rejected.
func (s *Server) HubTunnelHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w.Header())
		r = r.Clone(withHubTunnelRequest(r.Context()))
		if strings.TrimSpace(r.RemoteAddr) == "" || isLoopbackRequest(r) {
			r.RemoteAddr = "tunnel-stream-remote"
		}
		if r.Body != nil && strings.TrimSpace(r.Header.Get("Upgrade")) == "" {
			r.Body = http.MaxBytesReader(w, r.Body, maxTunnelRequestBodyBytes)
		}
		if s.tasks != nil {
			r = r.WithContext(WithIAPConfig(r.Context(), IAPConfig{
				Audience:              s.tasks.Config.IAPAudience,
				Issuer:                s.tasks.Config.IAPIssuer,
				KeysURL:               s.tasks.Config.IAPKeysURL,
				HubIssuer:             s.tasks.Config.HubIssuer,
				HubAudience:           s.tasks.Config.HubAudience,
				HubKeysURL:            s.tasks.Config.HubKeysURL,
				RequireVerifier:       string(AuthMethodHub),
				RequireIAP:            false,
				LocalAPIToken:         "",
				AccessSessionLifetime: s.tasks.Config.AccessSessionLifetime,
			}))
		} else {
			cfg, _ := IAPConfigFromContext(r.Context())
			cfg.RequireVerifier = string(AuthMethodHub)
			cfg.RequireIAP = false
			cfg.LocalAPIToken = ""
			r = r.WithContext(WithIAPConfig(r.Context(), cfg))
		}
		if strings.TrimSpace(r.Header.Get(AccessCodeHeader)) != "" {
			writeError(w, http.StatusUnauthorized, errors.New("X-Grain-Access-Code is not accepted over the hub tunnel"))
			return
		}
		if c, err := r.Cookie(AccessCookieName); err == nil && c != nil && strings.TrimSpace(c.Value) != "" {
			writeError(w, http.StatusUnauthorized, errors.New("grain_access cookies are not accepted over the hub tunnel"))
			return
		}
		if presentsApprovalCallbackToken(r) {
			writeError(w, http.StatusUnauthorized, errors.New("local script approval tokens are not accepted over the hub tunnel"))
			return
		}
		if _, ok := bearerToken(r); ok && !presentsExternalAgentToken(r) {
			writeError(w, http.StatusUnauthorized, errors.New("bearer access codes are not accepted over the hub tunnel"))
			return
		}
		cv := verifyHubTunnelCaller(r)
		r = r.WithContext(withCallerVerification(r.Context(), &cv))
		if !cv.present || cv.err != nil {
			err := cv.err
			if err == nil {
				err = errors.New(errVerifierRequired(string(AuthMethodHub)))
			}
			writeAssertionRefused(w, r, err)
			return
		}
		s.ServeHTTP(w, r)
	})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	if !checkCrossOrigin(w, r) {
		return
	}
	withCompression(w, r, func(w http.ResponseWriter, r *http.Request) {
		serveRecovered(w, r, http.HandlerFunc(s.serveHTTP))
	})
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	tunnelReq := isHubTunnelRequest(r)
	if s.tasks != nil {
		iapCfg := IAPConfig{
			Audience:              s.tasks.Config.IAPAudience,
			Issuer:                s.tasks.Config.IAPIssuer,
			KeysURL:               s.tasks.Config.IAPKeysURL,
			HubIssuer:             s.tasks.Config.HubIssuer,
			HubAudience:           s.tasks.Config.HubAudience,
			HubKeysURL:            s.tasks.Config.HubKeysURL,
			RequireVerifier:       s.tasks.Config.RequireVerifier,
			RequireIAP:            s.tasks.Config.RequireIAP,
			LocalAPIToken:         s.localAPITokenNow(),
			AccessSessionLifetime: s.tasks.Config.AccessSessionLifetime,
		}
		if s.accessCodeModeNow() == AccessCodeOff && strings.TrimSpace(r.Header.Get(ActingTaskHeader)) == "" {
			// Off: no code is accepted, so every check of it -- the gate,
			// the sign-in, a session cookie -- refuses alike. A run's own
			// plumbing (ActingTaskHeader, held to its task's grants) still
			// reaches the daemon with it until runs carry a credential of
			// their own (docs/access-code.md).
			iapCfg.LocalAPIToken = ""
		}
		if tunnelReq {
			iapCfg.RequireVerifier = string(AuthMethodHub)
			iapCfg.RequireIAP = false
			iapCfg.LocalAPIToken = ""
		}
		r = r.WithContext(WithIAPConfig(r.Context(), iapCfg))
	}
	// Nobody gets further without the access code -- loopback included
	// (accesscode.go, grain/task-4343) -- unless the request presents a
	// scoped external agent bearer token (gma_...), which authenticates as
	// PrincipalAgent("external:<name>") and is held to its own scopes by
	// agentAPIGate and handleMCP.
	cfg, _ := IAPConfigFromContext(r.Context())
	var cv callerVerification
	if tunnelReq {
		cv = cachedOrVerifyCaller(r)
	} else {
		cv = verifyCallerNow(r)
		r = r.WithContext(withCallerVerification(r.Context(), &cv))
	}
	if cv.present && cv.err != nil {
		writeAssertionRefused(w, r, cv.err)
		return
	}
	if !tunnelReq && presentsApprovalCallbackToken(r) {
		taskID, ok := s.authenticateApprovalCallback(w, r)
		if !ok {
			return
		}
		r.Header.Del(ActingTaskHeader)
		ctx := r.Context()
		if s.tasks != nil {
			ctx = s.tasks.withOperatorActor(ctx)
		}
		agent := model.Principal{Kind: model.PrincipalAgent, ID: taskID, Source: model.SourceInternal}
		callCtx := withAuthMethod(WithPrincipal(ctx, agent), AuthMethodTrusted)
		r = r.WithContext(model.WithEventActor(callCtx, RequestActor(callCtx)))
		s.mux.ServeHTTP(w, r)
		return
	}
	if externalTokenFromContext(r.Context()) == nil && presentsExternalAgentToken(r) {
		tok, release, ok := s.authenticateExternalAgent(w, r)
		if !ok {
			return
		}
		defer release()
		agent := model.Principal{Kind: model.PrincipalAgent, ID: "external:" + tok.Name, Source: model.SourceExternalToken}
		r = r.WithContext(withExternalToken(withAuthMethod(WithPrincipal(r.Context(), agent), AuthMethodTrusted), tok))
	}
	roleSlot := &accessRoleSlot{}
	r = r.WithContext(context.WithValue(r.Context(), accessRoleSlotKey{}, roleSlot))
	if !s.singleTenantGate(w, r, cfg) {
		return
	}
	if roleSlot.role != nil {
		r = r.WithContext(withAccessRole(r.Context(), roleSlot.role))
	}
	if roleSlot.delegation != nil {
		r = r.WithContext(withDelegatedIdentity(r.Context(), roleSlot.delegation))
		log.Printf("grain ui: delegated request: %s %s actor=service:%s on_behalf_of=user:%s via=%s registry_rid=%q registry_jti=%q delegation_grant=%q",
			r.Method, r.URL.Path, roleSlot.delegation.AppEmail, roleSlot.delegation.PersonEmail, roleSlot.delegation.ViaLabel,
			roleSlot.delegation.RequestID, roleSlot.delegation.JTI, roleSlot.delegation.Grant)
	}
	if s.tasks != nil {
		r = r.WithContext(s.tasks.withOperatorActor(r.Context()))
	}
	if tok := externalTokenFromContext(r.Context()); tok != nil {
		agent := model.Principal{Kind: model.PrincipalAgent, ID: "external:" + tok.Name, Source: model.SourceExternalToken}
		r = r.WithContext(withAuthMethod(WithPrincipal(r.Context(), agent), AuthMethodTrusted))
	} else {
		p, method, ok := extractPrincipal(r)
		if ok {
			admitted := !singleTenantOpenRoute(r)
			if !admitted {
				if err := s.admitVerifiedPrincipal(r.Context(), cv.id); err == nil {
					admitted = true
					if roleSlot.role != nil {
						r = r.WithContext(withAccessRole(r.Context(), roleSlot.role))
					}
					if roleSlot.delegation != nil {
						r = r.WithContext(withDelegatedIdentity(r.Context(), roleSlot.delegation))
					}
				}
			}
			if admitted {
				r = r.WithContext(withAuthMethod(WithPrincipal(r.Context(), p), method))
			} else {
				ok = false
			}
		}
		if requiresAccess(r) && !ok && !singleTenantTrusted(r) {
			// No verified principal at all. Only a caller this daemon
			// already trusts as itself -- the access code's, or an
			// in-process request -- runs as the deployment's own actor
			// (RequestActor). The gate above already refused anybody else
			// on every route but its own open ones (singleTenantOpenRoute).
			writeError(w, http.StatusUnauthorized, errors.New(singleTenantRefusal))
			return
		}
		if !ok {
			src := trustedRequestSource(r)
			r = r.WithContext(withRequestSource(r.Context(), src))
			if src == model.SourceAccessCode {
				var admitted bool
				if r, admitted = s.admitAccessCodeRequest(w, r); !admitted {
					return
				}
			}
		}
	}
	r = r.WithContext(model.WithEventActor(r.Context(), RequestActor(r.Context())))
	// Unarmed until a configuration write's guard finds the deployment
	// managed by an external config repository (checkSettingsWritable).
	rehearsal := &configWriteRehearsal{}
	r = r.WithContext(model.WithWriteRehearsal(r.Context(), rehearsal))
	rw := &configRefusalWriter{
		ResponseWriter: w,
		rehearsal:      rehearsal,
		onSuccess: func() {
			// Who made the write is who it was guarded as made by: an
			// agent's principal is on the handler's context alone.
			by := rehearsal.writer(r.Context())
			s.tasks.recordConfigWrite(by, r)
			s.tasks.syncStateRepoAfterWrite(by)
		},
	}
	s.mux.ServeHTTP(rw, r)
	rw.finish()
}

// requiresAccess reports whether r is an API request that needs an
// allowed caller. The frontend's static files, /api/config (what the
// sign-in page reads) and /api/auth/* (signing in itself) are the only
// things served to a caller nobody has vouched for. A grain hostd dialing
// in to /api/hostd/connect authenticates in its own protocol
// (hostdConnectRoute), and the script that installs one
// (hostdInstallRoute) is grain's own, with nothing in it to keep.
//
// A persistent sandbox's staging environment under /staging/ is held to
// the same gate as the API: it is somebody's running service, not a page
// of the frontend.
func requiresAccess(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, model.PersistentSandboxSubpathPrefix) || r.URL.Path == "/mcp" {
		return true
	}
	return strings.HasPrefix(r.URL.Path, "/api/") &&
		!(r.URL.Path == "/api/config" && (r.Method == http.MethodGet || r.Method == http.MethodHead)) &&
		!strings.HasPrefix(r.URL.Path, "/api/auth/") &&
		!hostdConnectRoute(r) && !hostdInstallRoute(r) && !hostdEnrollOpenRoute(r)
}

func (s *Server) routes() {
	s.route("GET /api/config", s.handleConfig)
	s.mux.HandleFunc("PUT /api/config", s.roleGate("PUT /api/settings", s.agentAPIGate("PUT /api/settings", s.handleUpdateSettings)))
	s.route("GET /api/restart-watch", s.handleRestartWatch)
	s.route("GET /api/fs/ci/{path...}", s.handleReadCINamespace)
	s.route("GET /api/fs/task/{path...}", s.handleReadTaskNamespace)
	s.route("GET /api/fs/deploy/{path...}", s.handleReadDeployNamespace)
	s.route("GET /api/fs/config/{path...}", s.handleReadConfigNamespace)
	s.route("GET /api/fs/memory/{path...}", s.handleReadMemoryNamespace)
	s.route("GET /api/fs/src/{path...}", s.handleReadSrcNamespace)
	s.route("GET /api/auth/access-code", s.handleAccessCodeStatus)
	s.route("POST /api/auth/access-code", s.handleAccessCodeLogin)
	s.route("GET /api/auth/access-code/login.js", s.handleAccessCodeScript)
	s.route("POST /api/auth/logout", s.handleAuthLogout)
	s.route("GET /api/auth/logout", s.handleAuthLogout)
	s.route("GET /api/auth/user", s.handleAuthUser)
	s.route("GET /api/auth/status", s.handleAuthUser)
	s.route("GET /api/auth/whoami", s.handleAuthWhoami)
	s.route("GET /api/whoami", s.handleWhoami)
	s.route("GET /api/settings", s.handleGetSettings)
	s.route("PUT /api/settings", s.handleUpdateSettings)
	s.route("POST /api/settings", s.handleUpdateSettings)
	s.route("POST /api/settings/plan", s.handlePlanSettings)
	s.route("GET /api/settings/cli-run", s.handleGetCLIRunSettings)
	s.route("PUT /api/settings/cli-run", s.handleUpdateCLIRunSettings)
	s.route("GET /api/registry", s.handleGetHub)
	s.route("POST /api/registry/heartbeat", s.handleTriggerHubHeartbeat)
	s.route("GET /api/registry/status", s.handleGetHubInstanceStatus)
	s.route("GET /api/tasks", s.handleListTasks)
	s.route("GET /api/tasks/states", s.handleTaskStates)
	s.route("GET /api/tasks/wait", s.handleTasksWait)
	s.route("GET /api/inbox", s.handleInbox)
	s.route("GET /api/refs/{ref}/summary", s.handleRefSummary)
	s.route("GET /api/events", s.handleEvents)
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
	s.route("POST /api/tasks/{id}/reply", s.handleReplyTask)
	// PUT, not POST: setting the one secret a parked run asked for is an
	// idempotent write of a value at a name the task already fixed --
	// the same shape (and the same verb) /api/secrets/{secret}/{key}
	// already uses for the write this one delegates to.
	s.route("PUT /api/tasks/{id}/secret", s.handleSetTaskSecret)
	s.route("POST /api/tasks/{id}/config-change", s.handleProposeConfigChange)
	s.route("POST /api/tasks/{id}/config-change/approve", s.handleApproveConfigChange)
	s.route("POST /api/tasks/{id}/config-change/reject", s.handleRejectConfigChange)
	s.route("POST /api/tasks/{id}/access-request/check", s.handleCheckAccessRequest)
	s.route("POST /api/tasks/{id}/access-request/grant", s.handleGrantAccessRequest)
	s.route("POST /api/tasks/{id}/access-request/deny", s.handleDenyAccessRequest)
	s.route("GET /api/tasks/{id}/attachments/{attachmentId}", s.handleGetAttachment)
	s.route("POST /api/tasks/{id}/close", s.handleClose)
	s.route("POST /api/tasks/{id}/stop", s.handleStop)
	s.route("POST /api/tasks/{id}/pause", s.handlePause)
	s.route("POST /api/tasks/{id}/resume", s.handleResume)
	s.route("PUT /api/tasks/{id}/goal", s.handleSetGoal)
	s.route("PUT /api/tasks/{id}/hostd", s.handleSetTaskHostd)
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
	// A pull request grain did not open (pull_requests_api.go, #XC-28,
	// #XC-29): adopt it, review it, or report where it stands.
	s.route("POST /api/pull-requests/adopt", s.handleAdoptPullRequest)
	s.route("POST /api/pull-requests/review", s.handleReviewPullRequest)
	s.route("GET /api/pull-requests/status", s.handlePullRequestStatus)
	s.route("POST /api/pull-requests/submit", s.handleSubmitPullRequest)
	s.route("POST /api/tasks/{id}/sandbox/recreate", s.handleRecreateSandbox)
	s.route("POST /api/sandbox-probe", s.handleSandboxProbe)
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
	s.route("GET /api/tasks/{id}/context", s.handleGetTaskContext)
	s.route("POST /api/tasks/{id}/compact", s.handleCompactTask)
	s.route("POST /api/tasks/{id}/brief", s.handleSetTaskBrief)
	s.route("POST /api/tasks/{id}/rollover", s.handleRolloverTask)
	s.route("POST /api/tasks/{id}/human-review", s.handleRequestHumanReview)
	s.route("POST /api/tasks/{id}/human-review/release", s.handleReleaseHumanReview)
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
	s.route("GET /api/declarations", s.handleListDeclarationAcceptances)
	s.route("POST /api/declarations/accept", s.handleAcceptDeclaration)
	s.route("POST /api/declarations/revoke", s.handleRevokeDeclarationAcceptance)
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
	s.route("GET /api/tasks/{id}/diff-preview", s.handleTaskDiffPreview)
	s.route("GET /api/tasks/{id}/tree", s.handleGetTaskTree)

	s.route("POST /api/repos", s.handleAddTargetRepo)
	s.route("POST /api/repos/plan", s.handlePlanTargetRepos)
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
	s.route("POST /api/releases/{version}/rollback", s.handleRollbackRelease)
	s.route("GET /api/release-approval-key", s.handleReleaseApprovalKey)

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
	s.route("POST /api/capability-servers/figma-test", s.handleTestFigmaAccount)
	s.route("POST /api/capability-servers/figma-oauth-authorize", s.handleAuthorizeFigmaOAuth)
	s.route("POST /api/capability-servers/figma-oauth-exchange", s.handleExchangeFigmaOAuth)
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
	s.route("POST /api/secrets/redact", s.handleRedactSecrets)
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
	s.route("POST /api/state-repo/secrets-key/wrap", s.handleWrapSecretsKey)
	s.route("POST /api/state-repo/secrets-key/unwrap", s.handleUnwrapSecretsKey)
	s.route("POST /api/state-repo/import-key", s.handleImportSecretsKey)
	s.route("POST /api/state-repo/secrets-repo", s.handleSetSecretsRepo)
	s.route("POST /api/state-repo/reset-notice", s.handleDismissResetNotice)

	s.route("POST /api/host/reboot", s.handleRebootHost)
	s.route("POST /api/host/shutdown", s.handleShutdownHost)
	s.route("POST /api/host/restart", s.handleRestartDaemon)
	s.route("GET /api/host/top", s.handleGetHostTop)
	s.route("GET /api/upgrade", s.handleGetUpgradeStatus)
	s.route("POST /api/upgrade", s.handleStartUpgrade)
	s.route("DELETE /api/upgrade/current", s.handleCancelCurrentUpgrade)
	s.route("DELETE /api/upgrade/scheduled", s.handleCancelScheduledUpgrade)
	s.route("GET /api/upgrade/targets", s.handleGetUpgradeTargets)
	s.route("GET /api/upgrade/release-notes", s.handleUpgradeReleaseNotes)
	s.route("GET /api/database", s.handleGetDatabase)

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
	s.route("GET /api/usage", s.handleGetUsage)
	s.route("PUT /api/usage/settings", s.handlePutUsageSettings)

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
	s.route("POST /api/ci/runs/{id}/rerun", s.handleRerunCIRun)
	s.route("GET /api/ci/runs/{id}/log", s.handleGetCIRunLog)
	s.route("GET /api/ci/runs/{id}/artifacts", s.handleListCIRunArtifacts)
	s.route("GET /api/ci/runs/{id}/artifacts/{job}/{name}", s.handleGetCIArtifact)
	// The job cache behind those runs: what it holds, and the one thing
	// an operator does to it.
	s.route("GET /api/ci/cache", s.handleGetCICache)
	s.route("DELETE /api/ci/cache", s.handleClearCICache)
	s.route("POST /api/tests/quarantine", s.handleQuarantineTest)

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

	// A grain hostd on another machine dialing in (hostd -server).
	s.route("GET /api/hostd/connect", s.handleHostdConnect)
	s.route("POST /api/hostd/join", s.handleHostdJoin)
	s.route("POST /api/hostd/join/{id}/poll", s.handleHostdJoinPoll)
	s.route("POST /api/hostd/renew", s.handleHostdRenew)
	// The script that installs one there (Settings -> Hosts), and the
	// per-platform grain-agentd binary and signed manifest for hosts that
	// cannot reach the internet.
	s.route("GET "+hostdInstallPath, s.handleHostdInstallScript)
	s.route("GET "+hostdInstallShortPath, s.handleHostdInstallScript)
	s.route("GET "+hostdAgentdBinaryRoute, s.handleHostdAgentdBinary)
	s.route("GET "+hostdAgentdManifestPath, s.handleHostdAgentdManifest)
	s.route("GET "+cliInstallPath, s.handleCLIInstallScript)
	// The host registry and join handshake (Settings -> Hosts).
	s.route("GET /api/hostds", s.handleListHostds)
	s.route("POST /api/hostds", s.handleCreateHostd)
	s.route("GET /api/hostds/enrollment-codes", s.handleListHostEnrollmentCodes)
	s.route("POST /api/hostds/enrollment-codes", s.handleCreateHostEnrollmentCode)
	s.route("POST /api/hostds/enrollment-codes/{name}/rotate", s.handleRotateHostEnrollmentCode)
	s.route("POST /api/hostds/enrollment-codes/{name}/revoke", s.handleRevokeHostEnrollmentCode)
	s.route("GET /api/hostds/pending", s.handleListPendingHostJoins)
	s.route("POST /api/hostds/pending/{id}/accept", s.handleAcceptPendingHostJoin)
	s.route("POST /api/hostds/pending/{id}/reject", s.handleRejectPendingHostJoin)
	s.route("PATCH /api/hostds/{name}", s.handleUpdateHostd)
	s.route("GET /api/hostds/{name}/install", s.handleHostdInstall)
	s.route("POST /api/hostds/{name}/rename", s.handleRenameHostd)
	s.route("POST /api/hostds/{name}/rotate", s.handleRotateHostdToken)
	s.route("POST /api/hostds/{name}/revoke", s.handleRevokeHostd)
	s.route("DELETE /api/hostds/{name}", s.handleDeleteHostd)
	s.route("POST /api/hostds/delete-revoked", s.handleDeleteRevokedHostds)

	// Verified-identity sign-in allowlist (Settings -> Access).
	s.route("GET /api/access/principals", s.handleListAccessPrincipals)
	s.route("POST /api/access/principals", s.handleAllowPrincipal)
	s.route("PATCH /api/access/principals/{entry}", s.handleSetPrincipalRole)
	s.route("DELETE /api/access/principals/{entry}", s.handleDenyPrincipal)
	s.route("GET /api/access/history", s.handleAccessHistory)

	// Controller MCP server (mcp_endpoint.go) shared by external BYO
	// harnesses and built-in sessions/tasks holding api:* grants.
	s.route("POST /api/mcp", s.handleControllerMCP)

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		// staticFS is embedded at build time from a directory this package
		// ships -- its absence is a build-time mistake, not a runtime
		// condition a caller can do anything about.
		panic("ui: embedding static/: " + err.Error())
	}
	staticNext, err := fs.Sub(staticNextFS, "static-next")
	if err != nil {
		panic("ui: embedding static-next/: " + err.Error())
	}
	s.mux.Handle("/", s.apiFallback(frontendHandler(static, staticNext)))
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
	"PUT /api/deployment/agent-backends/{backendId}":           true,
	"DELETE /api/deployment/agent-backends/{backendId}":        true,
	"POST /api/deployment/agent-backends/{backendId}/discover": true,
	"GET /api/agent-models":                                    true,
	"PUT /api/agent-backends/{id}/credential":                  true,
	"DELETE /api/agent-backends/{id}/credential":               true,
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
	// And a verified person only within their role (access_roles.go),
	// ahead of the agent gate so an acting-task header never widens one.
	handler = s.roleGate(pattern, handler)
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
		if hq := hostQualifiedRepoRequest(r); hq != nil {
			if _, pattern := s.mux.Handler(hq); pattern != "" && pattern != "/" {
				s.mux.ServeHTTP(w, hq)
				return
			}
		}
		msg := fmt.Sprintf("no such API endpoint: %s %s", r.Method, r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/api/repos/") {
			msg += " (a repository off the default host is /api/repos/<host>%2F<owner>/<name>/...)"
		}
		writeError(w, http.StatusNotFound, errors.New(msg))
	})
}

// hostQualifiedRepoRequest is r with /api/repos/<host>/<owner>/<name>/...
// read as the repo route it means, /api/repos/<host>%2F<owner>/<name>/...
// -- the path an agent writes for a repository off the default host,
// which has one segment too many for {owner}/{name} -- or nil when r's
// path is not that. <host> is a host by model.ParseRepo's rule: it
// contains a dot or a colon, which no owner can.
func hostQualifiedRepoRequest(r *http.Request) *http.Request {
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/api/repos/")
	if !ok {
		return nil
	}
	parts := strings.SplitN(rest, "/", 4)
	if len(parts) < 3 || !strings.ContainsAny(parts[0], ".:") || parts[1] == "" || parts[2] == "" {
		return nil
	}
	raw := "/api/repos/" + parts[0] + "%2F" + strings.Join(parts[1:], "/")
	path, err := url.PathUnescape(raw)
	if err != nil {
		return nil
	}
	out := r.Clone(r.Context())
	out.URL.Path, out.URL.RawPath = path, raw
	out.RequestURI = out.URL.RequestURI()
	return out
}

// frontendHandler serves both embedded frontends from the one "/"
// catch-all: the Angular UI in staticNext under /next/ (a bare /next
// redirects to /next/), and the React UI in static everywhere else.
// Both sit behind the same ServeHTTP -- security headers, CSRF, the
// access-code gate and verified-identity admission -- so the new UI has
// no authentication path of its own: it is the same origin, the same
// session cookie and the same /api/ as the old one.
func frontendHandler(static, staticNext fs.FS) http.Handler {
	legacy := spaHandler(static)
	next := http.StripPrefix("/next", spaHandler(staticNext))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/next" {
			target := "/next/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/next/") {
			next.ServeHTTP(w, r)
			return
		}
		legacy.ServeHTTP(w, r)
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
