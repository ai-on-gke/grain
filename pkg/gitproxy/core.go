package gitproxy

// The proxy's decision logic -- independent of the HTTP transport.
// Order matters and mirrors docs/design.md's own spec:
//
//	match the four legal smart-HTTP paths,
//	authenticate the caller by per-sandbox token,
//	check the caller's live task scope against (owner, repo), default-deny,
//	select the credential -- for a sandbox running a task, the task's
//	  grant, its team's repo override or its team's own credential and
//	  nothing past them (grain/task-1592); the owner/repo ladder only for
//	  a sandbox with no live task -- and set Authorization,
//	stream the body through, and log the tuple.
//
// Authentication is checked before authorization so an unauthenticated
// caller learns nothing about what any sandbox is scoped to -- a 401 with
// nothing else leaked, same as a real git server's own behavior toward an
// unauthenticated client.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/namespace"
)

// The default request-body limits, per action. A push carries a packfile
// and can be large; anything else is a fetch negotiation (wants, haves,
// and the odd filter line), which stays small even for a deep history,
// or a GET with no body at all. Both are generous on purpose: the point
// is to bound what one request can make the controller hold, not to
// second-guess git.
const (
	DefaultMaxPushBodyBytes  int64 = 2 << 30  // 2 GiB, git-receive-pack
	DefaultMaxFetchBodyBytes int64 = 64 << 20 // 64 MiB, git-upload-pack and info/refs
)

// ProxyResponse is what Handle decided to answer with. Body and Stream
// are as on UpstreamResponse: a response carrying a Stream is passing an
// upstream body through unread, and whoever receives it must Close it
// (WriteResponse does).
type ProxyResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
	Stream  io.ReadCloser
}

// GitProxy is the proxy's whole decision logic, wired to its
// dependencies. Now is injected rather than read from time.Now() so
// core_test.go can assert on audit timestamps deterministically.
type GitProxy struct {
	Authorizer  Authorizer
	Credentials *CredentialSet
	Tokens      *SandboxTokens
	Forwarder   Forwarder
	Audit       AuditLog
	Now         func() time.Time
	// CredentialOverrides resolves bwsalmon/agents#52's per-task named
	// credential override, if any. nil (every existing caller/test)
	// leaves every request on the ordinary owner/repo ladder, the same
	// as an override lookup that always answered false.
	CredentialOverrides CredentialOverrideLookup
	// SandboxTeams names the team a CI job's sandbox runs as, for a job
	// whose repository access is its team's to give (readscope.go's
	// SandboxTeams). A registered sandbox's requests go out with that
	// team's credential, resolved through TeamCredentials, and never with
	// the deployment's own ladder. nil registers nothing.
	SandboxTeams *SandboxTeams
	// TeamCredentials resolves a registered sandbox's team to its
	// credential. nil with a registered sandbox refuses the request
	// rather than falling back to the deployment's ladder.
	TeamCredentials TeamCredentialLookup
	// SingleTenant is the single-tenant engine, which the daemon always
	// runs (false is left only for the team tests until team RBAC goes,
	// docs/single-tenant-reset.md 3e-iii-d): one tenant, the
	// machine's owner, so a task or CI job whose team has no credential
	// is not refused (grain/task-1592's isolation is between tenants, and
	// there is one) but goes out on the deployment's own ladder. A CI
	// job's sandbox is resolved as the default team's, the same team a
	// single-tenant store resolves every task as (model.Store.SingleTenant).
	SingleTenant bool
	// Local, when set, is the controller-owned repository served under
	// its stable alias. It is authorized like every other repository but
	// never forwarded to GitHub or made to depend on a GitHub credential.
	Local *LocalRepository
	// ForwarderFor is where a request for a git host other than the
	// default is forwarded: the forwarder for the credential's own host.
	// nil sends it to that host's address (CredentialHost.hostPort,
	// base_url) with a RealForwarder; a test supplies fakes per host.
	ForwarderFor func(CredentialHost) (Forwarder, error)
	// PushSpoolThresholdBytes is the most of a push body held in memory
	// before spooling to disk. Zero means DefaultSpoolThreshold (16 MiB).
	PushSpoolThresholdBytes int64
	// MaxPushBodyBytes and MaxFetchBodyBytes cap the request body the
	// proxy will read for git-receive-pack and for everything else. Zero
	// means DefaultMaxPushBodyBytes / DefaultMaxFetchBodyBytes.
	MaxPushBodyBytes  int64
	MaxFetchBodyBytes int64
	// Namespace serves /fs/<scheme>/<path> over the same per-sandbox HTTP
	// listener and token authentication.
	Namespace       *namespace.Server
	NamespaceScopes NamespaceScopeLookup
	// CLI answers the /cli/ reads that are neither files nor grain CI's
	// records -- GitHub issues and grain's database (cli.go). nil answers
	// them 501 to a run holding the grant.
	CLI                     CLIBackend
	MaxNamespaceConcurrency int
	nsLimiter               namespaceLimiter
	// Activity, when set, narrates each /fs/ request on the live run's
	// activity row and records it in task_run_tool (activity.go).
	Activity ProxyActivityRecorder
	activity proxyActivity
	// CLIActions, when set, serves the acting /cli/ routes -- grain ci
	// wait / run-job / retrigger, grain pr open / reply / resolve /
	// review-comment, and grain git revert's revert-info
	// (cli_actions.go). Nil answers each of them 501.
	CLIActions CLIActions
	// WaitCredit, when set, credits the time /cli/ci/wait and
	// /cli/ci/run-job block back to the run's wall-clock deadline.
	WaitCredit CLIWaitCreditor
	cliWaits   cliWaitLedger
}

// pushSpoolThreshold returns the threshold above which push bodies spool to disk.
func (p *GitProxy) pushSpoolThreshold() int64 {
	if p.PushSpoolThresholdBytes > 0 {
		return p.PushSpoolThresholdBytes
	}
	return DefaultSpoolThreshold
}

// maxBodyBytes is the most of a request body the proxy will read for
// action.
func (p *GitProxy) maxBodyBytes(action string) int64 {
	if action == "git-receive-pack" {
		if p.MaxPushBodyBytes > 0 {
			return p.MaxPushBodyBytes
		}
		return DefaultMaxPushBodyBytes
	}
	if p.MaxFetchBodyBytes > 0 {
		return p.MaxFetchBodyBytes
	}
	return DefaultMaxFetchBodyBytes
}

// errBodyTooLarge is readBody's answer to a body over the action's limit.
var errBodyTooLarge = errors.New("request body too large")

// readBody reads body into memory, refusing one longer than limit. A nil
// body is no body at all, and stays nil.
func readBody(body io.Reader, limit int64) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errBodyTooLarge
	}
	return data, nil
}

func (p *GitProxy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *GitProxy) audit() AuditLog {
	if p.Audit != nil {
		return p.Audit
	}
	return NullAuditLog{}
}

// Handle is HandleRequest for a body already in memory; nil is no body.
func (p *GitProxy) Handle(ctx context.Context, method, path, query string, headers map[string]string, body []byte) ProxyResponse {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	return p.HandleRequest(ctx, method, path, query, headers, reader)
}

// HandleRequest decides how to answer one request and, if it is allowed,
// forwards it -- the entire proxy, as one call.
//
// body is not touched until the request has been decided from its path
// and headers alone: a caller that is not running git, has no valid
// token, or is not scoped to the repository is answered without a byte
// of it being read, and an allowed one is read only up to maxBodyBytes
// for its action (413 past that). Anything that can reach the proxy can
// send a body, so reading it first would let any of them make the
// controller hold as much as they cared to send. nil is no body.
func (p *GitProxy) HandleRequest(ctx context.Context, method, path, query string, headers map[string]string, body io.Reader) ProxyResponse {
	if isNamespacePath(path) || isCLIPath(path) {
		return p.handleNamespaceRequest(ctx, method, path, query, headers, body)
	}
	req, ok := ParsePath(path)
	if !ok {
		return ProxyResponse{Status: 404, Body: []byte("not found")}
	}

	if !IsValidGitRequest(headers["User-Agent"], headers["Accept"], req.Action) {
		return ProxyResponse{Status: 404, Body: []byte("not found")}
	}
	// One spelling of the host from here on: normalized, and empty for
	// the default host, so /owner/repo and /<default host>/owner/repo are
	// one repository and every check below compares like with like.
	host, err := p.requestHost(req.Host)
	if err != nil {
		return ProxyResponse{Status: 404, Body: []byte("not found")}
	}
	req.Host = host
	// What the authorizers compare against a sandbox's scope:
	// owner, or host/owner off the default host (GitRequest.ScopeOwner).
	scopeOwner := req.ScopeOwner()

	token, hasToken := ExtractBasicAuthToken(headers["Authorization"])
	var sandbox string
	if hasToken {
		sandbox, ok = p.Tokens.Authenticate(token)
	}
	if !hasToken || !ok {
		unauthenticated := Denial(401, "authentication required")
		unauthenticated.Headers["WWW-Authenticate"] = `Basic realm="grain-git-proxy"`
		return unauthenticated
	}

	// A per-team configuration repository's name (grain/config-<team>)
	// names nothing: there is one configuration repository, grain/config.
	// Answered not found ahead of the scope, so that a scope naming one
	// (a task filed against grain/config-default) is not forwarded to a
	// forge's grain/ owner with this deployment's credential.
	if RetiredConfigRepo(req.Host, req.Owner, req.Repo) {
		p.audit().Record(AuditEntry{
			Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
			Action: req.Action, Outcome: "denied: no such configuration repository",
		})
		return Denial(404, RetiredConfigRepoReason(req.Owner, req.Repo))
	}
	if p.Local != nil && req.Host == "" && p.Local.DisabledFor(req.Owner, req.Repo) {
		p.audit().Record(AuditEntry{
			Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
			Action: req.Action, Outcome: "denied: local configuration repository disabled",
		})
		return Denial(404, "local config repository is disabled when an external config repository is configured")
	}

	// A repo that is refused to every sandbox, checked ahead of the
	// scope: this is not "you are not scoped to it" but "nobody is", and
	// the two deserve different answers. The only one today is grain's
	// own state repository when it carries the encrypted secrets file --
	// see ModelAuthorizer.Forbidden for why that cannot be enforced any
	// more finely than per repository.
	if refuser, ok := p.Authorizer.(Refuser); ok {
		if reason, refused := refuser.Refusal(scopeOwner, req.Repo); refused {
			p.audit().Record(AuditEntry{
				Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
				Action: req.Action, Outcome: "denied: forbidden repository",
			})
			return Denial(403, fmt.Sprintf("%s/%s: %s", scopeOwner, req.Repo, reason))
		}
	}

	// A push's info/refs advertisement is authorized as the push it opens,
	// not as the fetch its action looks like (isPush). git prints the body
	// of a refused advertisement but not of a refused git-receive-pack
	// POST, so this is the one place a read-only sandbox's push can be
	// refused in a sentence the person running git actually sees.
	push := isPush(req.Action, query)
	authAction := req.Action
	if push {
		authAction = "git-receive-pack"
	}
	allowed, err := p.Authorizer.Authorize(ctx, sandbox, scopeOwner, req.Repo, authAction)
	if err != nil {
		p.audit().Record(AuditEntry{
			Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
			Action: req.Action, Outcome: fmt.Sprintf("error: %v", err),
		})
		return Denial(500, "authorization check failed")
	}
	if !allowed && push {
		// Refused as a push: say so when the repository is one this
		// sandbox may fetch, rather than calling it out of scope.
		if readable, err := p.Authorizer.Authorize(ctx, sandbox, scopeOwner, req.Repo, "info/refs"); err == nil && readable {
			p.audit().Record(AuditEntry{
				Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
				Action: req.Action, Outcome: "denied: push to read-only repository",
			})
			return Denial(403, ReadOnlyPushReason(scopeOwner, req.Repo))
		}
	}
	if !allowed {
		p.audit().Record(AuditEntry{
			Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
			Action: req.Action, Outcome: "denied: not in scope",
		})
		return Denial(403, fmt.Sprintf("%s/%s is not in scope for this sandbox", scopeOwner, req.Repo))
	}

	limit := p.maxBodyBytes(req.Action)
	var data []byte
	var spooled *spooledBody
	if req.Action == "git-receive-pack" {
		spooled, err = spoolBody(body, limit, p.pushSpoolThreshold())
		if spooled != nil {
			defer spooled.Close()
		}
	} else {
		data, err = readBody(body, limit)
	}
	if errors.Is(err, errBodyTooLarge) {
		p.audit().Record(AuditEntry{
			Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
			Action: req.Action, Outcome: fmt.Sprintf("denied: request body over %d bytes", limit),
		})
		return Denial(413, fmt.Sprintf("request body exceeds the git proxy's %d-byte limit for %s", limit, req.Action))
	}
	if err != nil {
		return Denial(400, "reading request body")
	}

	// A push held in memory is forwarded from there. One spooled to disk
	// is read from its file -- by the ref check below as a stream, and on
	// to the local repository or a RealForwarder as one -- and is loaded
	// into memory whole only for the transports that need the bytes
	// (loadSpooled).
	if spooled != nil && spooled.file == nil {
		data = spooled.mem
	}
	loadSpooled := func() *ProxyResponse {
		if spooled == nil || spooled.file == nil || data != nil {
			return nil
		}
		if data, err = spooled.Bytes(); err != nil {
			denial := Denial(500, "reading spooled push body")
			return &denial
		}
		return nil
	}

	// A push may update only the run's own branch (refupdates.go). Read
	// off the command list before anything is forwarded or served -- the
	// local repository below included: git http-backend has no branch
	// protection behind it, and its receive.denyCurrentBranch keeps a
	// sandbox off only the one branch the controller has checked out.
	if req.Action == "git-receive-pack" {
		if refusal, refused := p.refuseRefUpdates(ctx, sandbox, req, headers["Content-Encoding"], spooled.reader); refused {
			return refusal
		}
	}

	if p.Local != nil && req.Host == "" && p.Local.Matches(req.Owner, req.Repo) {
		var upstream UpstreamResponse
		if spooled != nil && spooled.file != nil {
			r, rErr := spooled.NewReader()
			if rErr != nil {
				return Denial(500, "reading spooled push body")
			}
			defer func() {
				if c, ok := r.(io.Closer); ok {
					c.Close()
				}
			}()
			upstream, err = p.Local.ForwardRepoStream(req.Owner, req.Repo, method, req.Action, query, headers, r, spooled.Len())
		} else {
			upstream, err = p.Local.ForwardRepo(req.Owner, req.Repo, method, req.Action, query, headers, data)
		}
		if err != nil {
			p.audit().Record(AuditEntry{Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
				Action: req.Action, Outcome: fmt.Sprintf("error: local repository failed: %v", err)})
			return Denial(502, "local repository request failed")
		}
		p.audit().Record(AuditEntry{Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
			Action: req.Action, Credential: "grain controller", Outcome: fmt.Sprintf("served locally: %d", upstream.Status)})
		return ProxyResponse(upstream)
	}

	credential, notConfigured, err := p.selectCredential(ctx, sandbox, req.Host, req.Owner, req.Repo)
	if err != nil {
		p.audit().Record(AuditEntry{
			Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
			Action: req.Action, Outcome: fmt.Sprintf("error: %v", err),
		})
		return Denial(500, "resolving credential override failed")
	}
	if notConfigured != "" {
		p.audit().Record(AuditEntry{
			Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
			Action: req.Action, Outcome: "error: " + notConfigured,
		})
		return Denial(500, notConfigured)
	}

	if credential.SSH != nil {
		// Everything above -- authorization, read scope, the body limit
		// and the ref policy -- has already applied; only the transport
		// differs (sshforward.go).
		if denial := loadSpooled(); denial != nil {
			return *denial
		}
		return p.forwardSSH(ctx, sandbox, req, method, query, headers, data, credential)
	}

	// The request goes to the credential's own host, which selectCredential
	// has already checked is the host the request named.
	forwarder, err := p.forwarderFor(credential)
	if err != nil {
		p.audit().Record(AuditEntry{
			Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
			Action: req.Action, Credential: credential.Name,
			Outcome: fmt.Sprintf("error: %v", err),
		})
		return upstreamFailedDenial(err, credential)
	}
	var upstream UpstreamResponse
	if real, ok := forwarder.(*RealForwarder); ok && spooled != nil && spooled.file != nil {
		r, rErr := spooled.NewReader()
		if rErr != nil {
			return Denial(500, "reading spooled push body")
		}
		defer func() {
			if c, ok := r.(io.Closer); ok {
				c.Close()
			}
		}()
		upstream, err = real.ForwardStream(
			method, fmt.Sprintf("/%s/%s.git/%s", req.Owner, req.Repo, req.Action),
			query, headers, r, spooled.Len(), credential.Token,
		)
	} else {
		if denial := loadSpooled(); denial != nil {
			return *denial
		}
		upstream, err = forwarder.Forward(
			method, fmt.Sprintf("/%s/%s.git/%s", req.Owner, req.Repo, req.Action),
			query, headers, data, credential.Token,
		)
		if err == nil && upstream.Status == 401 {
			if fresh, ok := p.refetchAmbient(ctx, sandbox, req, credential); ok {
				if upstream.Stream != nil {
					upstream.Stream.Close()
				}
				p.audit().Record(AuditEntry{
					Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
					Action: req.Action, Credential: credential.Name,
					Outcome: forwardedOutcome(credential, upstream.Status) + "; asked the host's login again and retried",
				})
				credential = fresh
				upstream, err = forwarder.Forward(
					method, fmt.Sprintf("/%s/%s.git/%s", req.Owner, req.Repo, req.Action),
					query, headers, data, credential.Token,
				)
			}
		}
	}
	if err != nil {
		p.audit().Record(AuditEntry{
			Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
			Action: req.Action, Credential: credential.Name,
			Outcome: fmt.Sprintf("error: forwarding failed: %v", err),
		})
		return upstreamFailedDenial(err, credential)
	}
	p.audit().Record(AuditEntry{
		Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
		Action: req.Action, Credential: credential.Name,
		Outcome: forwardedOutcome(credential, upstream.Status),
	})
	if upstream.Status == 401 {
		// An ambient credential's token came from the host's login a
		// few minutes ago at most; a 401 means that login moved on, and
		// the next request asks it again (ambient.go).
		p.Credentials.Refused(credential)
	}
	// GitHub's own refusals are rewritten to name the credential the
	// request went out under -- the one fact the sandbox cannot discover
	// and the operator needs. Everything else, success included, streams
	// through untouched. See refusals.go. Only a refusal's body is
	// read here, and only so much of it (bufferRefusalBody); a success
	// is a packfile, and goes on as a stream.
	if isRefusalStatus(upstream.Status) {
		upstream = bufferRefusalBody(upstream)
	}
	var check *loginCheck
	if upstream.Status == 401 && credential.Ambient != nil && credential.MintFailure == nil &&
		p.Credentials.ForgeViaCommand(credential.Name) {
		// A git endpoint behind a host tool can refuse a login its REST
		// API accepts, so the login is asked before it is blamed.
		id, err := p.Credentials.checkAmbientLogin(credential.Name)
		check = &loginCheck{Login: id.Login, Err: err}
	}
	if refusal, refused := upstreamRefusal(req, query, credential, upstream, check); refused {
		if team, ok := p.SandboxTeams.Team(sandbox); ok {
			refusal.Body = append(refusal.Body, []byte(TeamCredentialRefusedNote(team, scopeOwner+"/"+req.Repo)+"\n")...)
		}
		return refusal
	}
	return ProxyResponse(upstream)
}

// refetchAmbient is the credential to retry a request GitHub answered 401
// with, when that request went out under an ambient credential's cached
// token and the host's login now hands out a different one: the login
// rotated (a re-login, `gh auth refresh`) within the cache's lifetime, or
// another part of the daemon already heard the old token refused. Without
// it the first clone or push after a rotation fails with "that login is
// no longer valid" although the login is fine. ok is false -- and the 401
// stands -- when the credential is not ambient, the fetch gives no token,
// or it gives the same one again.
func (p *GitProxy) refetchAmbient(ctx context.Context, sandbox string, req GitRequest, refused Credential) (Credential, bool) {
	if refused.Ambient == nil || refused.Token == nil {
		return Credential{}, false
	}
	p.Credentials.Refused(refused)
	fresh, notConfigured, err := p.selectCredential(ctx, sandbox, req.Host, req.Owner, req.Repo)
	if err != nil || notConfigured != "" || fresh.Name != refused.Name || fresh.Ambient == nil ||
		fresh.Token == nil || *fresh.Token == *refused.Token {
		return Credential{}, false
	}
	return fresh, true
}

// refuseRefUpdates is the push's ref check: the refusal to answer with,
// and true, when the Authorizer is a RefAuthorizer and the command list in
// body names a ref this sandbox may not update -- or cannot be read at
// all, which is refused rather than forwarded on trust. A refused ref is
// answered as receive-pack would answer it (rejectPush), so the client
// prints why; an unreadable push gets a plain 403.
//
// body opens the request body, as the client encoded it, from its start;
// it is read only as far as the command list, and past it to the
// packfile only when a ref's check needs the pack (pushCheck).
func (p *GitProxy) refuseRefUpdates(ctx context.Context, sandbox string, req GitRequest, contentEncoding string, body func() io.Reader) (ProxyResponse, bool) {
	refs, ok := p.Authorizer.(RefAuthorizer)
	if !ok {
		return ProxyResponse{}, false
	}
	deny := func(status int, outcome, message string) (ProxyResponse, bool) {
		p.audit().Record(AuditEntry{
			Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
			Action: req.Action, Outcome: outcome,
		})
		return Denial(status, message), true
	}
	commands, err := commandList(body(), contentEncoding)
	if err == nil {
		var updates []RefUpdate
		var capabilities []string
		if updates, capabilities, err = parseReceivePack(commands); err == nil {
			check := &pushCheck{
				body: func() (io.Reader, error) { return decodedPush(body(), contentEncoding) },
				upstream: func(wants []string, have string, hashLen int) (packCommits, error) {
					return p.upstreamCommits(ctx, sandbox, req, wants, have, hashLen)
				},
			}
			reason, err := refs.AuthorizeRefUpdates(withPushCheck(ctx, check), sandbox, req.ScopeOwner(), req.Repo, updates)
			if err != nil {
				return deny(500, fmt.Sprintf("error: %v", err), "authorization check failed")
			}
			if reason == "" {
				// Every update of a ref other than the run's own branch is
				// logged on its own, with what allowed it, before the push
				// goes anywhere.
				for _, g := range check.grants {
					p.audit().Record(AuditEntry{
						Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
						Action: req.Action, Outcome: "allowed: " + g.Via + " (" + g.Kind + ")",
						Ref: g.Ref, Task: g.TaskID, Template: g.TemplateID, Declaration: g.Declaration,
					})
				}
				return ProxyResponse{}, false
			}
			refusal, refused := deny(403, "denied: ref not in scope", reason)
			if rejected, ok := rejectPush(updates, capabilities, reason); ok {
				refusal = rejected
			}
			return refusal, refused
		}
	}
	return deny(403, fmt.Sprintf("denied: unreadable push: %v", err),
		fmt.Sprintf("refusing a push to %s/%s whose ref updates could not be read: %v", req.ScopeOwner(), req.Repo, err))
}

// forwardedOutcome is what the audit log records for a request that was
// actually sent: the status it came back with, and -- for the one case
// where the credential named beside it is not what went out -- that it
// travelled unauthenticated.
//
// This is the failure that leaves no other trace at all (grain/task-158).
// A refused request at least reaches the sandbox with an explanation on
// it, but an App credential that cannot mint reads a *public* repo
// perfectly well anonymously, so the deployment goes on working, this
// log goes on saying "forwarded: 200" beside the credential's own name,
// and the first thing anybody hears about it is a push failing days
// later.
func forwardedOutcome(credential Credential, status int) string {
	if credential.Ambient != nil {
		return fmt.Sprintf("forwarded: %d (ambient: this host's %s login)", status, credential.Ambient.From)
	}
	if credential.MintFailure == nil {
		return fmt.Sprintf("forwarded: %d", status)
	}
	return fmt.Sprintf("forwarded: %d (sent unauthenticated -- no installation token could be minted "+
		"for the %q GitHub App credential)", status, credential.Name)
}

// selectCredential is which credential a request should forward with.
// notConfigured is a non-empty, request-safe message (never err) for the
// two "nothing here" business outcomes -- an override naming a credential
// this deployment has no `<name>.token` for, or no ladder entry covering
// (owner, repo) -- both of which get logged and returned to the caller
// exactly as CredentialSet's own docstring already promises. err is
// reserved for a genuine failure to resolve the override itself.
func (p *GitProxy) selectCredential(ctx context.Context, sandbox, host, owner, repo string) (credential Credential, notConfigured string, err error) {
	credential, notConfigured, err = p.resolveCredential(ctx, sandbox, host, owner, repo)
	if err == nil && notConfigured == "" && credential.WrongHost != "" {
		// Whichever rung chose it, a credential recorded for a git host
		// other than the one this request is for is refused rather than
		// sent (docs/git-hosts.md).
		return Credential{}, credential.WrongHost, nil
	}
	if err == nil && notConfigured == "" && credential.Unusable != "" {
		return Credential{}, credential.Unusable, nil
	}
	return credential, notConfigured, err
}

// repoName is owner/repo as a refusal names it: host/owner/repo off the
// default host.
func repoName(host, owner, repo string) string {
	if host != "" {
		return host + "/" + owner + "/" + repo
	}
	return owner + "/" + repo
}

// resolveCredential is selectCredential before its host check: the
// override, or the deployment's git connections. host is
// the request's, normalized and empty for the default host.
func (p *GitProxy) resolveCredential(ctx context.Context, sandbox, host, owner, repo string) (credential Credential, notConfigured string, err error) {
	if p.CredentialOverrides != nil {
		var name string
		var overridden bool
		if onHost, ok := p.CredentialOverrides.(HostCredentialOverrideLookup); ok {
			name, overridden, err = onHost.GitCredentialOverrideOn(ctx, sandbox, host, owner, repo)
		} else if host == "" {
			name, overridden, err = p.CredentialOverrides.GitCredentialOverride(ctx, sandbox, owner, repo)
		} else {
			return Credential{}, fmt.Sprintf("this proxy cannot resolve a credential for git host %s", host), nil
		}
		switch {
		case errors.Is(err, model.ErrNoTeamGitCredential):
			// The deployment's git connections hold nothing for this
			// repository. There is no ladder behind them any more
			// (docs/single-tenant-reset.md, stage 1): the message names
			// the host and where a connection is added.
			return Credential{}, err.Error(), nil
		case err != nil:
			return Credential{}, "", fmt.Errorf("resolving credential override for %s: %w", sandbox, err)
		}
		if overridden {
			// The task's own grant, or the deployment's connection for
			// this repository: whichever it is replaces the owner/repo
			// ladder entirely rather than narrowing it.
			cred, ok := p.Credentials.GetForRepoOn(name, host, owner, repo)
			if !ok {
				return Credential{}, fmt.Sprintf("no credential named %q configured", name), nil
			}
			return cred, "", nil
		}
	}
	// Everything else -- a CI job's sandbox, grain's own use of the
	// proxy, a sandbox with no live run -- resolves through the
	// deployment's git connections, whichever team the sandbox's work
	// belongs to. The secrets/github/credentials.json ladder is read by
	// nothing here (docs/single-tenant-reset.md, stage 1).
	return p.selectTeamCredential(ctx, model.DefaultTeamID, host, owner, repo)
}

// selectTeamCredential is selectCredential for a CI job's sandbox that
// runs as team (SandboxTeams): the team's own credential for owner/repo
// on host, or a message naming the team, the repository and where the
// team sets one. There is no fallback to the deployment's ladder -- the
// team's credential is the authority on what the job may reach
// (grain/task-1654).
//
// team is always the default team now: its row holds the deployment's
// git connections (model.Store.GitConnectionOn).
func (p *GitProxy) selectTeamCredential(ctx context.Context, team, host, owner, repo string) (Credential, string, error) {
	team = model.DefaultTeamID
	full := repoName(host, owner, repo)
	if p.TeamCredentials == nil {
		return Credential{}, fmt.Sprintf("this proxy cannot look up the deployment's "+
			"GitHub credential, so this CI job cannot reach %s", full), nil
	}
	var name string
	var err error
	if onHost, ok := p.TeamCredentials.(HostTeamCredentialLookup); ok {
		name, err = onHost.TeamGitCredentialOn(ctx, team, host, owner, repo)
	} else if host == "" {
		name, err = p.TeamCredentials.TeamGitCredential(ctx, team, owner, repo)
	} else {
		return Credential{}, fmt.Sprintf("this proxy cannot look up the deployment's "+
			"credential for git host %s, so this CI job cannot reach %s", host, full), nil
	}
	if errors.Is(err, model.ErrNoTeamGitCredential) {
		return Credential{}, NoTeamCredentialMessage(team, full), nil
	}
	if err != nil {
		return Credential{}, "", fmt.Errorf("resolving team %s's credential for %s: %w", team, full, err)
	}
	if p.Credentials == nil {
		return Credential{}, fmt.Sprintf("no credential named %q configured", name), nil
	}
	cred, ok := p.Credentials.GetForRepoOn(name, host, owner, repo)
	if !ok {
		return Credential{}, fmt.Sprintf("the deployment's GitHub credential %q is not configured, "+
			"so this CI job cannot reach %s -- choose another in Settings → Git (/settings/git)", name, full), nil
	}
	return cred, "", nil
}

// requestHost is a request path's host as the proxy compares it:
// normalized, and empty for the default host (the credentials' own
// default, which is the ForwardHost every unrecorded credential goes to).
func (p *GitProxy) requestHost(host string) (string, error) {
	n, err := model.NormalizeGitHost(host)
	if err != nil || n == "" {
		return "", err
	}
	if n == p.defaultHost() {
		return "", nil
	}
	return n, nil
}

// defaultHost is the host a request naming none goes to.
func (p *GitProxy) defaultHost() string {
	if p.Credentials != nil {
		return p.Credentials.DefaultHost()
	}
	return model.DefaultGitHost
}

// upstreamFailedDenial is the 502 for a request that never got an answer
// from its git host. The reason goes in the body -- git prints a
// text/plain error body as "remote: ...", so it lands in the run's log
// beside git's own "returned error: 502" -- with the credential's token
// cut out of it in case the error quotes something that carried it.
func upstreamFailedDenial(err error, credential Credential) ProxyResponse {
	reason := err.Error()
	if credential.Token != nil && *credential.Token != "" {
		reason = strings.ReplaceAll(reason, *credential.Token, "[token]")
	}
	return Denial(502, "upstream request failed: "+reason)
}

// forwarderFor is where a request going out with credential is sent: the
// credential's own host (CredentialSet.HostOf). The default host is
// Forwarder, the proxy's one configured forward target; any other host
// is ForwarderFor's answer, or a RealForwarder to that host's own
// host[:port] and scheme (base_url).
func (p *GitProxy) forwarderFor(credential Credential) (Forwarder, error) {
	if p.Credentials == nil {
		return p.Forwarder, nil
	}
	h := p.Credentials.HostOf(credential.Name)
	if h.Host == p.defaultHost() {
		if rf, ok := p.Forwarder.(*RealForwarder); ok && h.Proxy == "" && rf.Client == nil {
			if src, gh, ok := p.Credentials.forgeCommand(credential.Name); ok {
				// The default host is one only its own tool reaches:
				// the same target, through it (hostForwarder's case).
				return &RealForwarder{Host: rf.Host, UseTLS: rf.UseTLS, Username: rf.Username,
					Client: commandForgeClient(src, gh, forwardTimeout)}, nil
			}
		}
		if rf, ok := p.Forwarder.(*RealForwarder); ok && h.Proxy != "" && rf.Client == nil {
			// The default host behind a proxy of its own: the same
			// target, through that proxy.
			return &RealForwarder{Host: rf.Host, UseTLS: rf.UseTLS, Client: h.forwardClient()}, nil
		}
		return p.Forwarder, nil
	}
	if p.ForwarderFor != nil {
		return p.ForwarderFor(h)
	}
	forwarder := hostForwarder(h, credential)
	if src, gh, ok := p.Credentials.forgeCommand(credential.Name); ok && h.Proxy == "" {
		// Behind the front end only the host's own tool gets through,
		// and git's smart-HTTP paths are behind it as much as the API
		// is: each upstream request is one `<tool> api` run with
		// the whole URL, bounded as any forwarded request is. A proxy
		// an operator recorded for the host is their way round that,
		// and is used as it always was.
		forwarder.Client = commandForgeClient(src, gh, forwardTimeout)
	}
	return forwarder, nil
}

// hostForwarder is the forwarder for a request going out to h, a host
// other than the proxy's default one, with credential.
func hostForwarder(h CredentialHost, credential Credential) *RealForwarder {
	forwarder := &RealForwarder{Host: h.hostPort(), UseTLS: h.useTLS(), Client: h.forwardClient()}
	if h.Kind == model.GitHostGit && credential.Ambient != nil {
		// A plain git host's login is git's credential helper's, and
		// its username is half of it: sent the way git itself sends it.
		forwarder.Username = credential.Ambient.User
	}
	return forwarder
}

// NoTeamCredentialMessage is what a CI job that declared access to repo
// is told when team has no GitHub credential to give it: the team, the
// repository, and where the team sets one.
//
// team is always the default team now, whose row holds the deployment's
// git connections (docs/single-tenant-reset.md, stage 1).
func NoTeamCredentialMessage(team, repo string) string {
	return fmt.Sprintf("this deployment has no git connection covering %s, so this CI job cannot reach it -- "+
		"add one in Settings → Git (/settings/git)", repo)
}

// TeamCredentialRefusedNote is appended to GitHub's own refusal of a
// request a CI job made as team: the credential that was refused is the
// team's, so the team's settings are where its access is changed.
func TeamCredentialRefusedNote(team, repo string) string {
	return fmt.Sprintf("this CI job went out with the deployment's git connection, which cannot do this on %s -- "+
		"give that credential access on GitHub, or choose another in Settings → Git (/settings/git)", repo)
}
