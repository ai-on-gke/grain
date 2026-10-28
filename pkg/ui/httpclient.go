package ui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/modelexec"
)

// HTTPClient is Client's method surface (bwsalmon/agents#363) spoken over
// the wire instead of against a model.Store directly: cmd/grain's task
// CLI used to open the store itself -- embedded, or a Dolt SQL server via
// -store-addr -- and call Client's methods in-process, the same code path
// Server wraps in JSON for the frontend. That made the CLI a second
// direct writer alongside whatever daemon a deployment also runs, which
// is exactly the "single writer" caveat the store's own docs used to
// carry a whole section about. Now the daemon is the only thing that ever
// opens the store; the CLI is this instead, a plain REST client of the
// same JSON API the frontend already speaks, reaching it however an
// operator's network gets it there (a loopback port, an SSH tunnel,
// Cloud Run reverse proxy, Tailscale, IAP). When making requests to remote
// or Cloud Run endpoints, HTTPClient automatically resolves authentication
// tokens (via static token, ADC, or gcloud) and injects necessary proxy headers.
type HTTPClient struct {
	// BaseURL is the daemon's own address, e.g. "http://127.0.0.1:8420" --
	// no trailing slash required.
	BaseURL string
	// HTTP is the client requests are sent with. nil means
	// defaultHTTPClient, which gives up on a server that has not started
	// answering within defaultResponseHeaderTimeout.
	HTTP *http.Client
	// Token is a static Bearer token or tunnel secret (e.g. from -token or $GRAIN_TOKEN).
	Token string
	// Audience is the target audience for Google ID tokens (defaults to BaseURL if empty).
	Audience string
	// TokenProvider dynamically resolves an authentication token for each request.
	TokenProvider func(ctx context.Context) (string, error)
	// User is the optional identity to send in proxy identity headers (e.g. X-Forwarded-User).
	User string
	// RetryPolicy is how long a call waits out a daemon that is not
	// answering. nil means DefaultRetryPolicy; &NoRetry is one attempt.
	RetryPolicy *RetryPolicy

	tokenMu      sync.Mutex
	cachedToken  string
	cachedExpiry time.Time

	// adcIDToken and gcloud stand in for Application Default Credentials
	// and the gcloud binary in tests; nil means the real ones.
	adcIDToken func(ctx context.Context, audience string) (*oauth2.Token, error)
	gcloud     func(ctx context.Context, args ...string) (string, error)
}

// defaultResponseHeaderTimeout is how long defaultHTTPClient waits for a
// server to start answering a request before giving up on it, so a CLI
// call to a hung daemon fails instead of blocking until its caller's
// context ends. It is generous because some calls are synchronous on the
// server -- recreating a sandbox rebuilds a VM before it answers. It
// bounds only the wait for the headers: a body that is streaming, an
// artifact download, takes as long as it takes.
const defaultResponseHeaderTimeout = 10 * time.Minute

var defaultHTTPClient = func() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = defaultResponseHeaderTimeout
	return &http.Client{Transport: t}
}()

// NewHTTPClient builds an HTTPClient against baseURL.
func NewHTTPClient(baseURL string) *HTTPClient {
	return &HTTPClient{BaseURL: strings.TrimRight(baseURL, "/")}
}

func (c *HTTPClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTPClient
}

func (c *HTTPClient) prepareRequest(ctx context.Context, req *http.Request) error {
	tok, err := c.resolveToken(ctx)
	if err != nil {
		return err
	}
	if tok != "" {
		tok = strings.TrimSpace(strings.TrimPrefix(tok, "Bearer "))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("X-Grain-Tunnel-Token", tok)
	}
	if c.User != "" {
		req.Header.Set("X-Forwarded-User", c.User)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "grain-cli")
	}
	return nil
}

func (c *HTTPClient) resolveToken(ctx context.Context) (string, error) {
	if c.Token != "" {
		return c.Token, nil
	}
	if c.TokenProvider != nil {
		return c.TokenProvider(ctx)
	}
	for _, env := range []string{"GRAIN_TOKEN", "GRAIN_AUTH_TOKEN", "GRAIN_ID_TOKEN", "GRAIN_TUNNEL_TOKEN"} {
		if val := strings.TrimSpace(os.Getenv(env)); val != "" {
			return val, nil
		}
	}

	// A server on this very host is offered the daemon's local API
	// token, when this process can read it: what keeps the CLI working
	// against a daemon that wants the access code from every caller
	// (localtoken.go, grain/task-2078). A server elsewhere never is.
	if isLoopbackURL(c.BaseURL) {
		if tok := localAPIToken(); tok != "" {
			return tok, nil
		}
	}

	if c.shouldAttemptCloudRunAuth() {
		return c.getGoogleIDToken(ctx)
	}
	return "", nil
}

func (c *HTTPClient) shouldAttemptCloudRunAuth() bool {
	if c.Audience != "" {
		return true
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return false
	}
	return u.Scheme == "https" || isCloudRunHost(u.Hostname())
}

// isCloudRunHost reports whether host is a Cloud Run service's own
// address (*.run.app).
func isCloudRunHost(host string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSuffix(host, ".")), ".run.app")
}

func (c *HTTPClient) getGoogleIDToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.cachedToken != "" && time.Now().Before(c.cachedExpiry) {
		return c.cachedToken, nil
	}

	audience := c.Audience
	if audience == "" {
		audience = c.BaseURL
	}

	// 1. Google Application Default Credentials, minting a token for
	// this audience.
	adc := c.adcIDToken
	if adc == nil {
		adc = adcIDToken
	}
	if tok, err := adc(ctx, audience); err == nil && tok != nil && tok.AccessToken != "" {
		return c.cacheIDToken(tok.AccessToken, tok.Expiry), nil
	}

	gcloud := c.gcloud
	if gcloud == nil {
		if _, err := exec.LookPath("gcloud"); err != nil {
			return "", nil
		}
		gcloud = runGcloud
	}

	// 2. gcloud, for the same audience. That works for service-account
	// credentials, not for a user's own gcloud login.
	if token, err := gcloud(ctx, "auth", "print-identity-token", "--audiences="+audience); err == nil && token != "" {
		return c.cacheIDToken(token, time.Time{}), nil
	}

	// 3. A user's gcloud login can only mint a token with no audience of
	// this server's own: its audience is gcloud's client id, so anybody
	// who receives it can replay it to anything else that accepts those.
	// It is sent only to a Cloud Run service's own address, which is
	// what it exists for; any other server gets no token at all rather
	// than one it could reuse elsewhere.
	if u, err := url.Parse(c.BaseURL); err == nil && isCloudRunHost(u.Hostname()) {
		if token, err := gcloud(ctx, "auth", "print-identity-token"); err == nil && token != "" {
			return c.cacheIDToken(token, time.Time{}), nil
		}
	}

	return "", nil
}

// cacheIDToken keeps token until a minute before it expires -- at expiry
// if the caller knows it, else at the token's own exp claim -- and
// returns it. A token whose expiry cannot be read is used once and not
// kept, rather than kept for a guessed lifetime.
func (c *HTTPClient) cacheIDToken(token string, expiry time.Time) string {
	if expiry.IsZero() {
		expiry = jwtExpiry(token)
	}
	if expiry.IsZero() {
		c.cachedToken, c.cachedExpiry = "", time.Time{}
		return token
	}
	c.cachedToken, c.cachedExpiry = token, expiry.Add(-time.Minute)
	return token
}

// jwtExpiry reads the exp claim of a JWT without verifying it, the zero
// time if it has none or is not a JWT. It is only a cache lifetime: the
// server the token goes to is what checks it.
func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

func adcIDToken(ctx context.Context, audience string) (*oauth2.Token, error) {
	ts, err := idtoken.NewTokenSource(ctx, audience)
	if err != nil {
		return nil, err
	}
	return ts.Token()
}

func runGcloud(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "gcloud", args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RetryPolicy is how long HTTPClient keeps trying a daemon that is not
// answering, and how often, before it gives up and says so.
//
// It exists for the restart gap (docs/non-disruptive-upgrade.md, Stage
// 1): while an upgrade restarts the daemon, its API refuses connections
// -- or the reverse proxy in front of it answers 502/503 -- for as long
// as the new build takes to come up, and every `grain mcpserver` call an
// agent makes in that window would otherwise surface to the agent as a
// failed tool call. So a call that could not have reached the daemon is
// tried again, with backoff, until Budget is spent.
//
// What is retried is deliberately narrow (retryable): a failure that
// means the request never reached grain at all is safe to send again
// whatever it does, and one that might have been half-handled is sent
// again only for a method that is safe to repeat. Anything grain itself
// answered -- including its own 503s, which carry its JSON error body --
// is returned as it is.
type RetryPolicy struct {
	// Budget is how long to keep trying, from the first attempt. Zero
	// means a single attempt.
	Budget time.Duration
	// Initial is the first wait between attempts; it doubles, plus up to
	// half again of jitter, up to Max.
	Initial time.Duration
	Max     time.Duration
}

// DefaultRetryPolicy is the policy an HTTPClient with no RetryPolicy of its own
// uses: two minutes, which covers a host-binary restart and a container
// one with room to spare, and is short enough that an agent whose daemon
// really is gone hears so while it can still say so.
var DefaultRetryPolicy = RetryPolicy{Budget: 2 * time.Minute, Initial: 200 * time.Millisecond, Max: 5 * time.Second}

func (c *HTTPClient) retryPolicy() RetryPolicy {
	if c.RetryPolicy != nil {
		return *c.RetryPolicy
	}
	return DefaultRetryPolicy
}

// DaemonUnavailableError is what a call returns once its RetryPolicy is
// spent without the daemon answering: a sentence that says the daemon is
// down or restarting, rather than the bare dial error underneath it,
// which is still there for errors.Is and errors.As.
type DaemonUnavailableError struct {
	BaseURL  string
	Waited   time.Duration
	Attempts int
	Err      error
}

func (e *DaemonUnavailableError) Error() string {
	return fmt.Sprintf("grain's daemon at %s did not answer for %s (%d attempts): it is most likely restarting, "+
		"for an upgrade or after a crash; nothing was lost by this call failing, so try it again once grain is back (last error: %v)",
		e.BaseURL, e.Waited.Round(time.Second), e.Attempts, e.Err)
}

func (e *DaemonUnavailableError) Unwrap() error { return e.Err }

// idempotentMethod reports whether sending method twice does what
// sending it once does, so a request that may have been half-handled
// can be sent again.
func idempotentMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

// retryableError reports whether err, from sending a method request, is
// the daemon not being there rather than an answer from it.
//
// A refused or failed dial never reached grain, so it is retried for any
// method. A reset or an EOF part way through may have reached it and
// been acted on, so it is retried only for an idempotent method -- a
// POST that creates a task is not sent twice on a guess. A timeout
// waiting for a response is not a restart (a restarting daemon refuses;
// it does not hang) and is not retried at all.
func retryableError(method string, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "connection refused") {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	if !idempotentMethod(method) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		strings.Contains(err.Error(), "connection reset") ||
		strings.Contains(err.Error(), "EOF")
}

// retryableResponse reports whether resp is something standing in front
// of the daemon saying the daemon is not there -- a reverse proxy, an
// ingress, Cloud Run -- rather than grain's own answer.
//
// A 502, 503 or 504 from a gateway is retried for an idempotent method.
// None of them proves the request was never handled: proxies such as IAP's
// load balancer or Envoy answer 502 or 503 for an upstream connection that
// closed before it answered. So a non-idempotent method is never retried on
// a gateway error: a comment posted once must not land twice
// (docs/test-runs/2026-10-12-upgrade-drain-interrupted.md, finding 1).
//
// Grain's own handlers answer 502 and 503 too, for things that are not a
// restart (a database it cannot reach, a feature this daemon does not
// run), and those always carry its JSON error body: a response that does
// is grain answering, and is returned as it is.
func retryableResponse(method string, resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
	default:
		return false
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return false
	}
	return idempotentMethod(method)
}

// send sends the request newRequest builds, retrying it under the
// client's RetryPolicy (see RetryPolicy and retryable*) while the daemon
// is not there to answer it. newRequest is called once per attempt, so a
// body is rebuilt each time. The response it returns is the daemon's own,
// whatever its status, for the caller to read and close.
func (c *HTTPClient) send(ctx context.Context, method string, newRequest func() (*http.Request, error)) (*http.Response, error) {
	policy := c.retryPolicy()
	started := time.Now()
	wait := policy.Initial
	if wait <= 0 {
		wait = 100 * time.Millisecond
	}
	for attempt := 1; ; attempt++ {
		req, err := newRequest()
		if err != nil {
			return nil, err
		}
		if err := c.prepareRequest(ctx, req); err != nil {
			return nil, fmt.Errorf("authenticating request to %s: %w", c.BaseURL, err)
		}
		resp, err := c.httpClient().Do(req)
		var failure error
		switch {
		case err != nil && retryableError(method, err) && ctx.Err() == nil:
			failure = err
		case err != nil:
			return nil, fmt.Errorf("reaching grain server at %s: %w", c.BaseURL, err)
		case retryableResponse(method, resp):
			failure = fmt.Errorf("%s answered %s", c.BaseURL, resp.Status)
		default:
			return resp, nil
		}

		sleep := wait + time.Duration(rand.Int63n(int64(wait)/2+1))
		waited := time.Since(started)
		if waited+sleep > policy.Budget {
			if resp != nil {
				resp.Body.Close()
			}
			return nil, &DaemonUnavailableError{BaseURL: c.BaseURL, Waited: waited, Attempts: attempt, Err: failure}
		}
		if resp != nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
		}
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return nil, fmt.Errorf("reaching grain server at %s: %w (while waiting out: %v)", c.BaseURL, ctx.Err(), failure)
		}
		wait *= 2
		if policy.Max > 0 && wait > policy.Max {
			wait = policy.Max
		}
	}
}

// do sends a request with an optional JSON body and decodes a JSON
// response into out (nil to discard it), translating a non-2xx response
// into a NotFoundError, a ValidationError, a ForbiddenError, or a plain
// error carrying the server's own message -- the shapes writeClientError maps a
// direct Client error to, so a CLI caller sees identical behaviour either
// way.
//
// A daemon that is not answering -- restarting, in the ordinary case --
// is waited out under the client's RetryPolicy (send), and a call that
// outlasts it fails with a DaemonUnavailableError that says so.
func (c *HTTPClient) do(ctx context.Context, method, path string, body, out any) error {
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}
	}
	resp, err := c.send(ctx, method, func() (*http.Request, error) {
		var reader io.Reader
		if encoded != nil {
			reader = bytes.NewReader(encoded)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
		if err != nil {
			return nil, err
		}
		if encoded != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.httpError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response from %s %s: %w", method, path, err)
	}
	return nil
}

// httpError reads a {"error": "..."} body (writeError's own shape) and
// maps the status back to the same error type writeClientError derived
// it from -- 404 to NotFoundError, 400 to ValidationError, 403 to
// ForbiddenError, anything else to a plain error carrying the message and
// status.
func (c *HTTPClient) httpError(resp *http.Response) error {
	var payload struct {
		Error string `json:"error"`
	}
	message := resp.Status
	if err := json.NewDecoder(resp.Body).Decode(&payload); err == nil && payload.Error != "" {
		message = payload.Error
	}
	if resp.StatusCode == http.StatusUnauthorized && isLoopbackURL(c.BaseURL) {
		message += loopbackUnauthorizedHint()
	}
	switch resp.StatusCode {
	case http.StatusNotFound:
		return &NotFoundError{message: message}
	case http.StatusBadRequest:
		return validationErrorf("%s", message)
	case http.StatusForbidden:
		return forbiddenErrorf("%s", message)
	default:
		return fmt.Errorf("grain server: %s (%s)", message, resp.Status)
	}
}

// loopbackUnauthorizedHint returns an actionable hint for a 401 received
// from a loopback server, pointing the user to the local API token file
// or noting if an inspected token file was unreadable.
func loopbackUnauthorizedHint() string {
	path := strings.TrimSpace(os.Getenv(LocalAPITokenFileEnv))
	if path == "" {
		if dir := strings.TrimSpace(os.Getenv("GRAIN_DATA_DIR")); dir != "" {
			path = LocalAPITokenPath(dir)
		}
	}
	// The daemon's account is whoever owns its data: "grain" on a
	// scripts/setup.sh host, the installing user on a
	// google/scripts/cloudtop-install.sh one.
	dataDir := "/var/lib/grain/data"
	if path != "" {
		dataDir = filepath.Dir(path)
		if _, err := os.ReadFile(path); err != nil && os.IsPermission(err) {
			return fmt.Sprintf("; hint: local API token file %q is unreadable: %v (run as the daemon account or root, e.g. sudo -u %s grain <command>)", path, err, FileOwner(path))
		}
	}
	return fmt.Sprintf("; hint: to authenticate the grain CLI against this daemon, set GRAIN_LOCAL_API_TOKEN_FILE or run as the daemon account with GRAIN_DATA_DIR (e.g. sudo -u %s env GRAIN_DATA_DIR=%s grain <command>)", FileOwner(LocalAPITokenPath(dataDir)), dataDir)
}

// SandboxHealth reads the daemon's controller-side health snapshot. It is a
// read-only GET so capability tools gain no host command surface.
func (c *HTTPClient) SandboxHealth(ctx context.Context) (SandboxHealthResponse, error) {
	var health SandboxHealthResponse
	if err := c.do(ctx, http.MethodGet, "/api/sandboxes", nil, &health); err != nil {
		return SandboxHealthResponse{}, err
	}
	return health, nil
}

func (c *HTTPClient) ListTasks(ctx context.Context) ([]Task, error) {
	var tasks []Task
	if err := c.do(ctx, http.MethodGet, "/api/tasks", nil, &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

// TaskStatesPage fetches a page of task states from GET /api/tasks/states.
func (c *HTTPClient) TaskStatesPage(ctx context.Context, limit int, cursor string) (TaskStatesResponse, error) {
	path := "/api/tasks/states"
	v := url.Values{}
	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		v.Set("cursor", cursor)
	}
	if q := v.Encode(); q != "" {
		path += "?" + q
	}
	var resp TaskStatesResponse
	err := c.do(ctx, http.MethodGet, path, nil, &resp)
	return resp, err
}

// ReadStates reads all task states by iterating across paginated pages.
func (c *HTTPClient) ReadStates(ctx context.Context) (map[string]model.State, error) {
	out := make(map[string]model.State)
	var cursor string
	const limit = 2000
	for {
		page, err := c.TaskStatesPage(ctx, limit, cursor)
		if err != nil {
			return nil, err
		}
		for id, st := range page.States {
			out[id] = st
		}
		if page.Cursor == "" || !page.HasMore || len(page.States) == 0 {
			break
		}
		cursor = page.Cursor
	}
	return out, nil
}

// Task returns one task's list-shaped view, fetched the same way GetTask
// is -- there is no narrower endpoint, since the frontend never needed
// one either.
func (c *HTTPClient) Task(ctx context.Context, id string) (Task, error) {
	detail, err := c.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	return detail.Task, nil
}

func (c *HTTPClient) GetTask(ctx context.Context, id string) (TaskDetail, error) {
	var detail TaskDetail
	if err := c.do(ctx, http.MethodGet, "/api/tasks/"+id, nil, &detail); err != nil {
		return TaskDetail{}, err
	}
	return detail, nil
}

func (c *HTTPClient) CreateTask(ctx context.Context, req CreateTaskRequest) (Task, error) {
	var task Task
	if err := c.do(ctx, http.MethodPost, "/api/tasks", req, &task); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (c *HTTPClient) UpdateTask(ctx context.Context, id string, req UpdateTaskRequest) (Task, error) {
	var task Task
	if err := c.do(ctx, http.MethodPatch, "/api/tasks/"+id, req, &task); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (c *HTTPClient) SetCapability(ctx context.Context, id, capabilityID string, attach bool) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/capabilities",
		setCapabilityRequest{ID: capabilityID, Attach: &attach}, nil)
}

// SetDependency is Client.SetDependency over the wire: dependsOnID is
// sent as typed, so the server's own normalization (#N, task-N,
// grain/task-N, a task page's URL) and refusals (an unknown task, the
// task itself, a session that never ends) are the only copy of either.
func (c *HTTPClient) SetDependency(ctx context.Context, id, dependsOnID string, attach bool) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/depends-on",
		setDependencyRequest{ID: dependsOnID, Attach: attach}, nil)
}

func (c *HTTPClient) Approve(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/approve", nil, nil)
}

func (c *HTTPClient) WithdrawApproval(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/withdraw-approval", nil, nil)
}

// Defer is Client.Defer over the wire, wake time and all: the option
// travels as the request body's own deferredUntil, which the server
// reads back into the same DeferOptions. Sent as "" when there is none,
// for the reason Close sends its flag either way -- a body that says
// nothing about the field is the indefinite deferral, and saying so is
// no longer than leaving it out.
func (c *HTTPClient) Defer(ctx context.Context, id string, opts DeferOptions) error {
	var until string
	if opts.Until != nil {
		until = opts.Until.UTC().Format(time.RFC3339)
	}
	return c.do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/defer",
		deferRequest{DeferredUntil: until}, nil)
}

// Pause is Client.Pause over the API.
func (c *HTTPClient) Pause(ctx context.Context, id string, opts PauseOptions) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/pause",
		pauseRequest{Immediate: opts.Immediate}, nil)
}

// Resume is Client.Resume over the API.
func (c *HTTPClient) Resume(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/resume", nil, nil)
}

func (c *HTTPClient) Undefer(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/undefer", nil, nil)
}

func (c *HTTPClient) AddComment(ctx context.Context, id, body string, attachments []AttachmentUpload) error {
	return c.AddCommentWithAuthor(ctx, id, body, attachments, "")
}

func (c *HTTPClient) AddCommentWithAuthor(ctx context.Context, id, body string, attachments []AttachmentUpload, author string) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/comments",
		addCommentRequest{Body: body, Attachments: attachments, Author: author}, nil)
}

// Close is Client.Close over the wire, opts and all: the flag travels as
// the request body's own close_pull_request, which the server reads back
// into the same CloseOptions. Sent on every call rather than only when
// it is true, so that what a caller asked for is on the wire either way
// -- there is no shorter spelling of "and leave the pull request alone"
// than saying so.
func (c *HTTPClient) Close(ctx context.Context, id string, opts CloseOptions) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/close",
		closeRequest{ClosePullRequest: opts.ClosePullRequest}, nil)
}

func (c *HTTPClient) Reopen(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/reopen", nil, nil)
}

func (c *HTTPClient) Retry(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/retry", nil, nil)
}

// RetryMerge is Client.RetryMerge over the wire: put a task the merge
// queue gave up on back on that queue, with its refresh and its repair
// returned to it.
func (c *HTTPClient) RetryMerge(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/retry-merge", nil, nil)
}

// ForceMerge is Client.ForceMerge over the wire: record an operator's
// override of the merge queue, reason and all, for grain's next cycle to
// act on. The reason travels in the body and is required at the other
// end -- there is no spelling of this call without one. head is the
// commit reviewed, "" for the pull request's head as it is now.
func (c *HTTPClient) ForceMerge(ctx context.Context, id, reason, head string) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/force-merge",
		forceMergeRequest{Reason: reason, Head: head}, nil)
}

// RunNow is Client.RunNow over the wire: ask for an agent to be put on
// this task next, ahead of the queue and whatever its state says.
func (c *HTTPClient) RunNow(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/run-now", nil, nil)
}

// RunNowAs is RunNow asked for by the agent by names, which the approval
// and the note on the task are attributed to (dispatch-control's
// run_task_now). The zero Principal is RunNow.
func (c *HTTPClient) RunNowAs(ctx context.Context, id string, by model.Principal) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/run-now",
		runNowRequest{RequestedBy: by.ID, RequestedByKind: string(by.Kind)}, nil)
}

// OpenPullRequest is Client.OpenPullRequest over the wire: open (or find)
// the pull request for id's own branch and read its current checks.
//
// Its caller is not the CLI but `grain mcpserver` (pkg/cli/mcpserver.go),
// which serves a dispatched run's own tools and holds no GitHub
// credential -- this is the hop that lets a run open its pull request
// while it is still running without a credential ever reaching the
// process the agent drives.
func (c *HTTPClient) OpenPullRequest(ctx context.Context, id string) (PullRequestStatus, error) {
	var status PullRequestStatus
	if err := c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/pull-request", nil, &status); err != nil {
		return PullRequestStatus{}, err
	}
	return status, nil
}

// RecreateSandbox is Client.RecreateSandbox over the wire: destroy the
// sandbox of id's live run and build an empty one in its place.
//
// Its caller, like OpenPullRequest's above, is not the CLI but `grain
// mcpserver` (pkg/cli/mcpserver.go), which serves a dispatched run's
// own tools. Everything that process can do happens *inside* the
// sandbox, so throwing that sandbox away and building another is the one
// thing it has to ask the daemon for.
func (c *HTTPClient) RecreateSandbox(ctx context.Context, id string) (SandboxRecreation, error) {
	var recreation SandboxRecreation
	if err := c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/sandbox/recreate", nil, &recreation); err != nil {
		return SandboxRecreation{}, err
	}
	return recreation, nil
}

// SetTaskActivity is Client.SetTaskActivity over the wire: record what
// id's live run is doing right now, for the task list to show while it
// runs.
//
// Its caller, like the two above, is `grain mcpserver`
// (pkg/cli/mcpserver.go) rather than the CLI. The note travels this way
// for the plainest of the reasons any of them do: the run is in a
// sandbox, the task is a row in the daemon's store, and this is the only
// route between the two.
func (c *HTTPClient) SetTaskActivity(ctx context.Context, id, note string) (Activity, error) {
	var activity Activity
	if err := c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/activity",
		activityRequest{Note: note}, &activity); err != nil {
		return Activity{}, err
	}
	return activity, nil
}

// StageAttachment is Client.StageAttachment over the wire: hold a file
// id's live run attached by path until its finish relays it. Its caller,
// like SetTaskActivity's, is `grain mcpserver`, which read the file out
// of the run's sandbox.
func (c *HTTPClient) StageAttachment(ctx context.Context, id, path string, upload AttachmentUpload) (StagedAttachment, error) {
	var staged StagedAttachment
	if err := c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/staged-attachments",
		stagedAttachmentRequest{Path: path, Attachment: upload}, &staged); err != nil {
		return StagedAttachment{}, err
	}
	return staged, nil
}

// CreditWait is Client.CreditWait over the wire: credit id's live run for
// the time it has now spent blocked on CI, and read back where its
// deadline stands as a result.
//
// Its caller, like the three above, is `grain mcpserver`
// (pkg/cli/mcpserver.go) -- and unlike any of them it calls while a
// tool call is still in progress, once every poll of a wait_for_checks
// loop, because credit that arrives after the wait is credit that arrives
// after the clamp it exists to prevent (mcp's wait_credit.go).
func (c *HTTPClient) CreditWait(ctx context.Context, id string, blocked time.Duration) (WaitCredit, error) {
	var credit WaitCredit
	if err := c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/wait-credit",
		waitCreditRequest{BlockedMS: blocked.Milliseconds()}, &credit); err != nil {
		return WaitCredit{}, err
	}
	return credit, nil
}

// PostTaskMessage is Client.PostAgentMessage over the wire: say
// something to the human watching id's task, on behalf of the run working
// on it, and park the task on the answer.
//
// Its caller is `grain mcpserver` again (pkg/cli/mcpserver.go's
// daemonChat), and it is the one hop here whose whole purpose is to be
// followed by a wait: the tool that calls it blocks on the reply rather
// than ending the run and leaving it for the next dispatch.
func (c *HTTPClient) PostTaskMessage(ctx context.Context, id, body, summary string, attachments []AttachmentUpload) (Message, error) {
	var message Message
	if err := c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/messages",
		messageRequest{Body: body, Summary: summary, Attachments: attachments}, &message); err != nil {
		return Message{}, err
	}
	return message, nil
}

// TaskMessages is Client.MessagesSince over the wire: what a person has
// said on id's conversation since the comment named by after.
//
// Called in a loop by the same daemonChat, once per poll, for as long as
// its chat turn is waiting -- which is why it answers with the comments
// themselves rather than a "yes/no": the answer to a question is the
// words in it.
func (c *HTTPClient) TaskMessages(ctx context.Context, id string, after int64) ([]Message, error) {
	var messages []Message
	path := "/api/tasks/" + id + "/messages?after=" + strconv.FormatInt(after, 10)
	if err := c.do(ctx, http.MethodGet, path, nil, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

// TaskAddenda is Client.AddendaSince over the wire: what a person has
// said on id since after, plus what other tasks' runs have sent its
// agent (GET /api/tasks/{id}/messages?agents=true).
func (c *HTTPClient) TaskAddenda(ctx context.Context, id string, after int64) ([]Message, error) {
	var messages []Message
	path := "/api/tasks/" + id + "/messages?agents=true&after=" + strconv.FormatInt(after, 10)
	if err := c.do(ctx, http.MethodGet, path, nil, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

// RecordDelivered is Client.RecordDelivered over the wire: the run
// working on id has now been shown every comment up to through.
func (c *HTTPClient) RecordDelivered(ctx context.Context, id string, through int64) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/delivered",
		deliveredRequest{Through: through}, nil)
}

// Attachment returns one attachment's full content (GET /api/tasks/{id}/attachments/{attachmentId}).
func (c *HTTPClient) Attachment(ctx context.Context, taskID string, id int64) (model.Attachment, error) {
	resp, err := c.send(ctx, http.MethodGet, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet,
			fmt.Sprintf("%s/api/tasks/%s/attachments/%d", c.BaseURL, taskID, id), nil)
	})
	if err != nil {
		return model.Attachment{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return model.Attachment{}, c.httpError(resp)
	}
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return model.Attachment{}, err
	}
	return model.Attachment{
		ID:      id,
		TaskID:  taskID,
		Content: content,
	}, nil
}

// PutMemory is Client.PutMemory over the wire: replace what grain has
// learned about working here, deployment-wide (repo "") or about one repo
// ("owner/name").
//
// Its caller is `grain mcpserver` again (pkg/cli/mcpserver.go's
// daemonMemory, behind the memory capability), and for the same reason
// SetTaskActivity's is: the run that noticed the pattern is in a sandbox,
// the memory is a row in the daemon's store, and this is the route
// between the two. A run writing memory holds no credential of its own
// and reaches no database -- it asks grain, exactly as it does to open a
// pull request or to say what it is doing.
//
// by is who the write is recorded as -- the run's own task, as
// daemonMemory fills it in, since the daemon on the other end cannot
// tell this hop from the Settings pane's own PUT. The zero Principal
// sends no author and is recorded as the deployment's actor
// (Client.PutMemory).
// PutMemory is Client.PutMemory over the wire: replace what grain has
// learned about working in a scope (repo "" for the deployment-wide one,
// "owner/name" for a repo's).
//
// by is who the write is recorded as -- the run's own task, as
// daemonMemory fills it in, since the daemon on the other end cannot
// tell this hop from the Settings pane's own PUT. The zero Principal
// sends no author and is recorded as the deployment's actor
// (Client.PutMemory).
func (c *HTTPClient) PutMemory(ctx context.Context, repo, text string, by model.Principal) (Memory, error) {
	var memory Memory
	if err := c.do(ctx, http.MethodPut, "/api/memory",
		memoryRequest{Repo: repo, Text: text, Author: by.ID, AuthorKind: string(by.Kind)},
		&memory); err != nil {
		return Memory{}, err
	}
	return memory, nil
}

// PutReleaseNote is Client.PutReleaseNote over the wire: publish one
// week's release notes, replacing whatever was published for that week
// before, or withdraw the week by writing nothing.
//
// The same caller as PutMemory's, over the same hop, for the same reason
// (pkg/cli/mcpserver.go's daemonReleaseNotes, behind the release-notes
// capability): the row is the daemon's and the run holds no database. An
// empty period means the week that just ended, resolved on the
// deployment's own clock at the far end rather than guessed at here.
func (c *HTTPClient) PutReleaseNote(ctx context.Context, period, title, text string,
	by model.Principal) (ReleaseNote, error) {
	var note ReleaseNote
	if err := c.do(ctx, http.MethodPut, "/api/release-notes",
		releaseNoteRequest{
			Period: period, Title: title, Text: text,
			Author: by.ID, AuthorKind: string(by.Kind),
		}, &note); err != nil {
		return ReleaseNote{}, err
	}
	return note, nil
}

// ClusterStats is Client.ClusterStats over the wire: how much work this
// deployment did over one week, counted from its own rows. An empty
// period is the week that just ended, exactly as it is for the write
// above, so a run that names neither gets numbers and a note about the
// same week.
func (c *HTTPClient) ClusterStats(ctx context.Context, period string) (ClusterStats, error) {
	path := "/api/release-notes/stats"
	if strings.TrimSpace(period) != "" {
		path += "?period=" + url.QueryEscape(period)
	}
	var stats ClusterStats
	if err := c.do(ctx, http.MethodGet, path, nil, &stats); err != nil {
		return ClusterStats{}, err
	}
	return stats, nil
}

// ListMemoryDocuments is Client.ListMemoryDocuments over the wire: what
// notes grain keeps in full, in every scope, without their text.
//
// The same caller as PutMemory's, over the same hop, for the same reason
// (pkg/cli/mcpserver.go's daemonMemory, behind the memory capability):
// the rows are the daemon's and the run holds no database.
func (c *HTTPClient) ListMemoryDocuments(ctx context.Context) ([]MemoryDocument, error) {
	var docs []MemoryDocument
	if err := c.do(ctx, http.MethodGet, "/api/memory/documents", nil, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}

// GetMemoryDocument is Client.GetMemoryDocument over the wire: one named
// note, whole. The scope travels in the query rather than the path
// because the deployment's own has no repo to name (handleGetMemoryDocument).
func (c *HTTPClient) GetMemoryDocument(ctx context.Context, repo, name string) (MemoryDocument, error) {
	path := "/api/memory/documents/" + url.PathEscape(name)
	if strings.TrimSpace(repo) != "" {
		path += "?" + url.Values{"repo": {repo}}.Encode()
	}
	var doc MemoryDocument
	if err := c.do(ctx, http.MethodGet, path, nil, &doc); err != nil {
		return MemoryDocument{}, err
	}
	return doc, nil
}

// PutMemoryDocument is Client.PutMemoryDocument over the wire: replace
// one named note, or delete it by writing nothing. by is PutMemory's own
// author, meaning the same thing and defaulted the same way.
func (c *HTTPClient) PutMemoryDocument(ctx context.Context, repo, name, summary, text string,
	by model.Principal) (MemoryDocument, error) {
	var doc MemoryDocument
	if err := c.do(ctx, http.MethodPut, "/api/memory/documents",
		memoryDocumentRequest{
			Repo: repo, Name: name, Summary: summary, Text: text,
			Author: by.ID, AuthorKind: string(by.Kind),
		}, &doc); err != nil {
		return MemoryDocument{}, err
	}
	return doc, nil
}

func (c *HTTPClient) TaskPrompt(ctx context.Context, id string) (TaskPrompt, error) {
	var prompt TaskPrompt
	if err := c.do(ctx, http.MethodGet, "/api/tasks/"+id+"/prompt", nil, &prompt); err != nil {
		return TaskPrompt{}, err
	}
	return prompt, nil
}

func (c *HTTPClient) AttemptTranscript(ctx context.Context, id string, number int) (string, error) {
	var resp attemptTranscriptResponse
	path := fmt.Sprintf("/api/tasks/%s/attempts/%d/transcript", id, number)
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return "", err
	}
	return resp.Transcript, nil
}

// Config reads the deployment's fixed configuration -- who the daemon
// attributes every task and comment written through this API to, its
// default target repo, and the capabilities it offers. Unlike the
// store-backed Client, an HTTPClient has no -as flag of its own to
// override this with: attribution is a deployment-wide setting now,
// carried by whichever principal the daemon itself was started as,
// matching the "no auth, the network is the perimeter" model this
// deployment shape assumes rather than distinguishing callers.
func (c *HTTPClient) Config(ctx context.Context) (Config, error) {
	var resp configResponse
	if err := c.do(ctx, http.MethodGet, "/api/config", nil, &resp); err != nil {
		return Config{}, err
	}
	cfg := Config{
		Actor:        model.Principal{Kind: model.PrincipalKind(resp.ActorKind), ID: resp.Actor},
		Capabilities: resp.Capabilities,
	}
	if resp.DefaultTarget != "" {
		target, err := model.ParseRepo(resp.DefaultTarget)
		if err != nil {
			return Config{}, fmt.Errorf("server returned an unparseable default target %q: %w", resp.DefaultTarget, err)
		}
		cfg.DefaultTarget = &target
	}
	return cfg, nil
}

// TaskListConfig is the deployment-owned part of a task-list query. The
// pointer distinguishes an explicitly empty default (show every task) from an
// older daemon that did not report the field, while CustomFields lets the CLI
// parse deployment defaults naming declared attributes from the same config
// response the browser uses.
type TaskListConfig struct {
	DefaultFilters      *string
	ShowClosedByDefault bool
	CustomFields        []CustomFieldDeclaration
}

// TaskListConfig reads the config-to-query seam shared by the browser and
// `grain list`. It deliberately uses a pointer on the wire: empty is policy,
// absence is compatibility with a daemon from before deployment defaults.
func (c *HTTPClient) TaskListConfig(ctx context.Context) (TaskListConfig, error) {
	var resp struct {
		DefaultFilters      *string                  `json:"defaultFilters"`
		ShowClosedByDefault bool                     `json:"showClosedByDefault"`
		CustomFields        []CustomFieldDeclaration `json:"customFields"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/config", nil, &resp); err != nil {
		return TaskListConfig{}, err
	}
	return TaskListConfig{
		DefaultFilters:      resp.DefaultFilters,
		ShowClosedByDefault: resp.ShowClosedByDefault,
		CustomFields:        resp.CustomFields,
	}, nil
}

// CustomFields is the fields this deployment declares
// (model.CustomField) as GET /api/config sends them -- name, label,
// type, options and all, in the order every listing of them reads
// (model.SortCustomFields).
//
// Its own method rather than a field on the Config above, because the
// two answers are about different things: Config is the deployment's
// fixed configuration as this package's own server was handed it, and a
// declaration is a row in the store that only the HTTP side can see.
// The one route serves both, which is why this makes the same request.
//
// Its caller is `grain list`, which needs the declarations for the same
// reason the frontend's narrowing bar does: a declared field is an
// attribute a condition can name (pkg/cli/taskattributes.go), and
// nothing else says what its values are allowed to be.
func (c *HTTPClient) CustomFields(ctx context.Context) ([]CustomFieldDeclaration, error) {
	var resp configResponse
	if err := c.do(ctx, http.MethodGet, "/api/config", nil, &resp); err != nil {
		return nil, err
	}
	return resp.CustomFields, nil
}

func (c *HTTPClient) GetSettings(ctx context.Context) (Settings, error) {
	var settings Settings
	if err := c.do(ctx, http.MethodGet, "/api/settings", nil, &settings); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

func (c *HTTPClient) UpdateSettings(ctx context.Context, req UpdateSettingsRequest) (Settings, error) {
	var settings Settings
	if err := c.do(ctx, http.MethodPut, "/api/settings", req, &settings); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

// PlanSettings is Client.PlanSettings over the wire: the same request
// body UpdateSettings sends, to POST /api/settings/plan, which the
// daemon validates and normalizes exactly as a save would and answers
// with the Settings a save would leave behind -- having stored nothing.
//
// Its caller is `grain sync -dry-run` (pkg/cli/sync.go). It asks the
// daemon rather than working the answer out here for the reason
// Client.plannedConfig's own doc comment gives: what a value becomes is
// decided by the code that stores it, and a plan computed anywhere else
// is a guess that can differ from the save it is meant to describe.
//
// A daemon too old to have the route answers 404, which arrives here as
// a *NotFoundError -- see handlePlanSettings on why that failure is the
// one this had to have, and pkg/cli/sync.go for what a dry run says
// when it gets it.
func (c *HTTPClient) PlanSettings(ctx context.Context, req UpdateSettingsRequest) (Settings, error) {
	var settings Settings
	if err := c.do(ctx, http.MethodPost, "/api/settings/plan", req, &settings); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

// CheckCapability is Client.CheckCapability over the wire: authenticate
// as one capability's standing credential, make one cheap and harmless
// call with it, and report what came back.
//
// Its caller is `grain settings -check-capability` (cmd/grain/main.go).
// The question -- "is the credential this deployment holds still one the
// far end accepts?" -- is asked from a shell on the host at least as
// often as from a browser, and a host shell is where somebody reading a
// failed task's error is usually already standing.
func (c *HTTPClient) CheckCapability(ctx context.Context, id string) (CapabilityCheck, error) {
	var check CapabilityCheck
	if err := c.do(ctx, http.MethodPost, "/api/capabilities/"+url.PathEscape(id)+"/check", nil, &check); err != nil {
		return CapabilityCheck{}, err
	}
	return check, nil
}

// CheckAgentKey is Client.CheckAgentKey over the wire: authenticate as
// one framework's stored credential, list what it can see, and report
// what the vendor said.
//
// Its caller is `grain settings -check-agent-key` (cmd/grain/main.go),
// for the same reason -check-capability exists beside it: "why did every
// run today fail to authenticate" is asked from a shell on the host at
// least as often as from a browser, and a host shell is where whoever is
// reading the failed run's error is already standing.
func (c *HTTPClient) CheckAgentKey(ctx context.Context, framework string) (AgentKeyCheck, error) {
	var check AgentKeyCheck
	if err := c.do(ctx, http.MethodPost, "/api/agent-keys/"+url.PathEscape(framework)+"/check", nil, &check); err != nil {
		return AgentKeyCheck{}, err
	}
	return check, nil
}

// The repo family (grain/task-36): what a repo defaults on its own, and
// whether the deployment's allowlist names it. Four of the five mirror
// the Client method of the same name -- the ones the repos pane already
// calls in-process -- and ListRepos has no Client counterpart because it
// composes two endpoints that already exist rather than reaching a new
// one. The types they speak are repos.go's, next to the Client methods
// and HTTP handlers they cross the wire between; only the wire calls
// themselves live here, where every other HTTPClient method does.

// ListRepos reports one row per repo this deployment knows about --
// RepoSummary's own doc comment for why that is derived from GET
// /api/config and GET /api/tasks here rather than served whole by an
// endpoint of its own.
//
// Two requests, not one, and deliberately not concurrent: the second is
// a plain list of tasks, the ordering between them does not matter, and
// a CLI list command is not where saving one round trip is worth a
// goroutine and a second error path.
func (c *HTTPClient) ListRepos(ctx context.Context) ([]RepoSummary, error) {
	var cfg configResponse
	if err := c.do(ctx, http.MethodGet, "/api/config", nil, &cfg); err != nil {
		return nil, err
	}
	tasks, err := c.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	return repoSummaries(cfg.TargetRepos, tasks, cfg.RepoDefaultCapabilities,
		cfg.ReposWithPromptExtension, cfg.ReposWithSetupCommand, cfg.ReposWithWorkflows, cfg.ReposWithCICapabilities, cfg.ReposWithReleaseSettings), nil
}

// RepoDefaults reads repo's own default capability set, alongside the
// deployment-wide one it composes with and the effective set that
// composition produces -- Client.RepoDefaults over the wire.
func (c *HTTPClient) RepoDefaults(ctx context.Context, repo string) (RepoDefaults, error) {
	path, err := repoAPIPath(repo, "/capabilities")
	if err != nil {
		return RepoDefaults{}, err
	}
	var defaults RepoDefaults
	if err := c.do(ctx, http.MethodGet, path, nil, &defaults); err != nil {
		return RepoDefaults{}, err
	}
	return defaults, nil
}

// SetRepoCICapabilities replaces the capability set granted to this repo's CI jobs.
func (c *HTTPClient) SetRepoCICapabilities(ctx context.Context, repo string, ids []string) (RepoDefaults, error) {
	path, err := repoAPIPath(repo, "/ci-capabilities")
	if err != nil {
		return RepoDefaults{}, err
	}
	var defaults RepoDefaults
	body := struct {
		CICapabilities []string `json:"ciCapabilities"`
	}{ids}
	if err := c.do(ctx, http.MethodPut, path, body, &defaults); err != nil {
		return RepoDefaults{}, err
	}
	return defaults, nil
}

// SetRepoDefaultCapabilities replaces repo's own default capability set
// -- Client.SetRepoDefaultCapabilities over the wire, including its
// refusal of an id no capability answers to. A nil or empty ids is how a
// repo is returned to adding nothing, the same as sending an empty list
// from the repos pane.
func (c *HTTPClient) SetRepoDefaultCapabilities(ctx context.Context, repo string, ids []string) (RepoDefaults, error) {
	path, err := repoAPIPath(repo, "/capabilities")
	if err != nil {
		return RepoDefaults{}, err
	}
	var defaults RepoDefaults
	if err := c.do(ctx, http.MethodPut, path, SetRepoCapabilitiesRequest{DefaultCapabilities: ids}, &defaults); err != nil {
		return RepoDefaults{}, err
	}
	return defaults, nil
}

// SetRepoPromptExtension replaces repo's own standing instructions --
// Client.SetRepoPromptExtension over the wire. An empty text is how a
// repo goes back to adding nothing of its own, the same as clearing the
// box on the repos pane; everything else about the repo, its default
// capabilities included, is left exactly as it was.
func (c *HTTPClient) SetRepoPromptExtension(ctx context.Context, repo, text string) (RepoDefaults, error) {
	path, err := repoAPIPath(repo, "/prompt-extension")
	if err != nil {
		return RepoDefaults{}, err
	}
	var defaults RepoDefaults
	if err := c.do(ctx, http.MethodPut, path, SetRepoPromptExtensionRequest{PromptExtension: text}, &defaults); err != nil {
		return RepoDefaults{}, err
	}
	return defaults, nil
}

// SetRepoSetupCommand replaces the shell grain runs in this repo's fresh
// checkout before a run's first turn -- Client.SetRepoSetupCommand over
// the wire. An empty command is how a repo goes back to needing no setup
// at all; everything else about the repo, its default capabilities and
// its standing instructions included, is left exactly as it was.
func (c *HTTPClient) SetRepoSetupCommand(ctx context.Context, repo, command string) (RepoDefaults, error) {
	path, err := repoAPIPath(repo, "/setup-command")
	if err != nil {
		return RepoDefaults{}, err
	}
	var defaults RepoDefaults
	if err := c.do(ctx, http.MethodPut, path, SetRepoSetupCommandRequest{SetupCommand: command}, &defaults); err != nil {
		return RepoDefaults{}, err
	}
	return defaults, nil
}

// SetRepoUnfixableChecks replaces the whole list of checks that will
// never pass for a pull request grain's agents author in repo -- a CLA
// bot and its kind, Client.SetRepoUnfixableChecks over the wire,
// including its refusals: grain CI's own verdict and a name with a comma
// in it both come back as validation errors and nothing is written. A nil
// or empty checks is how a repository goes back to having no such check,
// the same as removing the last chip on its pane; everything else about
// the repo is left exactly as it was.
func (c *HTTPClient) SetRepoUnfixableChecks(ctx context.Context, repo string, checks []string) (RepoDefaults, error) {
	path, err := repoAPIPath(repo, "/unfixable-checks")
	if err != nil {
		return RepoDefaults{}, err
	}
	var defaults RepoDefaults
	if err := c.do(ctx, http.MethodPut, path, SetRepoUnfixableChecksRequest{UnfixableChecks: checks}, &defaults); err != nil {
		return RepoDefaults{}, err
	}
	return defaults, nil
}

// SetRepoCIMode replaces repo's configured CI mode -- Client.SetRepoCIMode over the wire.
func (c *HTTPClient) SetRepoCIMode(ctx context.Context, repo, mode string) (RepoDefaults, error) {
	path, err := repoAPIPath(repo, "/ci-mode")
	if err != nil {
		return RepoDefaults{}, err
	}
	var defaults RepoDefaults
	if err := c.do(ctx, http.MethodPut, path, SetRepoCIModeRequest{CIMode: mode}, &defaults); err != nil {
		return RepoDefaults{}, err
	}
	return defaults, nil
}

// RepoWorkOwnership reads what this deployment says about repo's
// standing work -- Client.RepoWorkOwnership over the wire: the
// repository default, and one row per work item with what has been said
// about it and what that resolves to.
func (c *HTTPClient) RepoWorkOwnership(ctx context.Context, repo string) (RepoWorkOwnership, error) {
	path, err := repoAPIPath(repo, "/work-ownership")
	if err != nil {
		return RepoWorkOwnership{}, err
	}
	var out RepoWorkOwnership
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return RepoWorkOwnership{}, err
	}
	return out, nil
}

// SetRepoWorkDefault writes repo's ownership default -- "on", "off", or
// "" for "nobody has said" -- and reads the whole record back, so a
// caller sees what the write did to every item that was following that
// default.
func (c *HTTPClient) SetRepoWorkDefault(ctx context.Context, repo, state string) (RepoWorkOwnership, error) {
	return c.setRepoWorkOwnership(ctx, repo, SetRepoWorkOwnershipRequest{Default: &state})
}

// SetRepoWorkItemOwnership writes one item's override, or clears it with
// an empty state so that item follows repo's default again.
func (c *HTTPClient) SetRepoWorkItemOwnership(ctx context.Context, repo, item, state string) (RepoWorkOwnership, error) {
	return c.setRepoWorkOwnership(ctx, repo, SetRepoWorkOwnershipRequest{Item: &item, State: &state})
}

func (c *HTTPClient) setRepoWorkOwnership(ctx context.Context, repo string, req SetRepoWorkOwnershipRequest) (RepoWorkOwnership, error) {
	path, err := repoAPIPath(repo, "/work-ownership")
	if err != nil {
		return RepoWorkOwnership{}, err
	}
	var out RepoWorkOwnership
	if err := c.do(ctx, http.MethodPut, path, req, &out); err != nil {
		return RepoWorkOwnership{}, err
	}
	return out, nil
}

// RepoWorkflows reads the grain CI workflows this deployment declares
// for repo -- Client.RepoWorkflows over the wire.
func (c *HTTPClient) RepoWorkflows(ctx context.Context, repo string) (RepoWorkflowsResponse, error) {
	path, err := repoAPIPath(repo, "/workflows")
	if err != nil {
		return RepoWorkflowsResponse{}, err
	}
	var out RepoWorkflowsResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return RepoWorkflowsResponse{}, err
	}
	return out, nil
}

// AddTargetRepo appends repo to the deployment's TargetRepos allowlist
// and returns the settings that result -- Client.AddTargetRepo over the
// wire, idempotent the same way.
//
// Note what an empty allowlist means everywhere else it appears
// (Config.TargetRepos): unrestricted. So the *first* repo added to a
// deployment that has never restricted itself does not widen anything --
// it narrows the deployment to that one repo. `grain repo add` says so
// when the list it prints back has exactly one entry.
func (c *HTTPClient) AddTargetRepo(ctx context.Context, repo string) (Settings, error) {
	return c.AddTargetRepoWithCloneURL(ctx, repo, "")
}

func (c *HTTPClient) AddTargetRepoWithCloneURL(ctx context.Context, repo, cloneURL string) (Settings, error) {
	var settings Settings
	if err := c.do(ctx, http.MethodPost, "/api/repos", AddRepoRequest{Repo: repo, CloneURL: cloneURL}, &settings); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

// RemoveTargetRepo removes repo from the deployment's TargetRepos
// allowlist -- Client.RemoveTargetRepo over the wire, a no-op rather
// than an error when repo isn't on it. It leaves everything else about
// the repo alone: tasks already targeting it keep doing so, and whatever
// it defaults on its own stays stored.
func (c *HTTPClient) RemoveTargetRepo(ctx context.Context, repo string) (Settings, error) {
	path, err := repoAPIPath(repo, "")
	if err != nil {
		return Settings{}, err
	}
	var settings Settings
	if err := c.do(ctx, http.MethodDelete, path, nil, &settings); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

// repoAPIPath builds /api/repos/{owner}/{name} + suffix, parsing repo
// first so a malformed one fails here, as the same ValidationError the
// Client method of the same name would return, rather than as whatever
// the resulting URL happened to hit: "widgets" with no owner would
// otherwise build /api/repos/widgets/capabilities and come back as a
// bare 404 from a route it could never have matched. A repo off the
// default host puts its host in the owner segment (RepoRef.StoredOwner,
// escaped as one segment), which is how the server reads it back.
func repoAPIPath(repo, suffix string) (string, error) {
	parsed, err := model.ParseRepo(repo)
	if err != nil {
		return "", validationErrorf("repo: %v", err)
	}
	return "/api/repos/" + url.PathEscape(parsed.StoredOwner()) + "/" + url.PathEscape(parsed.Name) + suffix, nil
}

// Metrics fetches a throughput and latency report over the span the
// request names, scoped to the repos and authors it names -- Client.Metrics
// over the wire, and the same MetricsRequest, so a caller that moves
// between the two changes nothing but which client it holds.
//
// Every field is optional and a zero one is simply not sent, which is
// what leaves each default where it is declared: an unset Window asks
// for the server's own DefaultMetricsWindow rather than for this
// binary's idea of it, and an unset Buckets takes pkg/metrics' own.
//
// The parameters go over in the spellings a hand-written request uses --
// ?window= as a duration string, the two moments as RFC 3339, and
// ?repo=/?author= once per value -- so a request made by hand (`curl
// '.../api/metrics?window=168h0m0s&repo=acme/widgets'`) and one made
// through here read the same.
func (c *HTTPClient) Metrics(ctx context.Context, req MetricsRequest) (MetricsReport, error) {
	q := url.Values{}
	if req.Window > 0 {
		q.Set("window", req.Window.String())
	}
	if !req.Since.IsZero() {
		q.Set("since", req.Since.UTC().Format(time.RFC3339))
	}
	if !req.Until.IsZero() {
		q.Set("until", req.Until.UTC().Format(time.RFC3339))
	}
	if req.Buckets > 0 {
		q.Set("buckets", strconv.Itoa(req.Buckets))
	}
	for _, repo := range req.Repos {
		q.Add("repo", repo)
	}
	for _, author := range req.Authors {
		q.Add("author", author)
	}
	if req.Section != "" {
		q.Set("section", req.Section)
	}
	path := "/api/metrics"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var report MetricsReport
	if err := c.do(ctx, http.MethodGet, path, nil, &report); err != nil {
		return MetricsReport{}, err
	}
	return report, nil
}

// UnmatchedFailures fetches what failed runs said when grain could not
// name why, over the window ending now -- Client.UnmatchedFailures over
// the wire. window is the same ?window= string Metrics takes, for the
// same reason; "" asks for the server's own default, and a limit of zero
// or less leaves the bound to the server too.
func (c *HTTPClient) UnmatchedFailures(ctx context.Context, window string, limit int) (UnmatchedFailureReport, error) {
	q := url.Values{}
	if window = strings.TrimSpace(window); window != "" {
		q.Set("window", window)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/api/unmatched-failures"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var report UnmatchedFailureReport
	if err := c.do(ctx, http.MethodGet, path, nil, &report); err != nil {
		return UnmatchedFailureReport{}, err
	}
	return report, nil
}

// AgentPause is Client.AgentPause over the wire: whether dispatch is
// currently gated by a usage limit on a credential of a team the caller
// can see, whose, and until when.
//
// enabled is false when the daemon has no gate to ask at all
// (Config.AgentPause nil -- a UI served without a reconcile loop behind
// it), which is a different answer from a deployment that simply is not
// paused, and the reason this returns the flag rather than folding both
// into a zero status. Its caller is `grain pause`
// (pkg/cli/pause.go): an operator on a terminal could otherwise only
// learn that dispatch was paused by reading the daemon's journal.
func (c *HTTPClient) AgentPause(ctx context.Context) (status AgentPauseStatus, enabled bool, err error) {
	var resp agentPauseResponse
	if err := c.do(ctx, http.MethodGet, "/api/pause", nil, &resp); err != nil {
		return AgentPauseStatus{}, false, err
	}
	if !resp.Enabled || resp.Pause == nil {
		return AgentPauseStatus{}, false, nil
	}
	return *resp.Pause, true, nil
}

// PipelinePause is Client.PipelinePause over the wire: whether this
// deployment is picking new work up at all, and if not, since when, why,
// who stopped it and which task -- if any -- it is waiting on.
//
// No enabled flag beside it, unlike AgentPause above: this gate is a row
// in the store every daemon has, so there is no deployment that cannot
// answer the question.
func (c *HTTPClient) PipelinePause(ctx context.Context) (PipelinePauseStatus, error) {
	var resp pipelinePauseResponse
	if err := c.do(ctx, http.MethodGet, "/api/pipeline-pause", nil, &resp); err != nil {
		return PipelinePauseStatus{}, err
	}
	return resp.Pause, nil
}

// PausePipeline is Client.PausePipeline over the wire -- `grain pause
// -set`: stop the deployment picking new tasks up, leaving whatever is
// already running to finish. paused is false when it was already paused,
// which is the answer whoever ran the command asked for.
func (c *HTTPClient) PausePipeline(ctx context.Context, reason string) (
	status PipelinePauseStatus, paused bool, err error) {
	return c.PausePipelineAs(ctx, reason, model.Principal{})
}

// PausePipelineAs is PausePipeline taken by the agent by names, which the
// pause row records as who paused (dispatch-control's pause_pipeline).
// The zero Principal is PausePipeline.
func (c *HTTPClient) PausePipelineAs(ctx context.Context, reason string, by model.Principal) (
	status PipelinePauseStatus, paused bool, err error) {
	var resp pipelinePauseResponse
	if err := c.do(ctx, http.MethodPost, "/api/pipeline-pause",
		pausePipelineRequest{Reason: reason, RequestedBy: by.ID, RequestedByKind: string(by.Kind)}, &resp); err != nil {
		return PipelinePauseStatus{}, false, err
	}
	return resp.Pause, resp.Paused != nil && *resp.Paused, nil
}

// ResumePipeline is Client.ResumePipeline over the wire -- `grain pause
// -resume`, and the banner's own Resume button: pick work up again,
// reporting whether there was a pause to lift.
func (c *HTTPClient) ResumePipeline(ctx context.Context) (
	status PipelinePauseStatus, resumed bool, err error) {
	var resp pipelinePauseResponse
	if err := c.do(ctx, http.MethodDelete, "/api/pipeline-pause", nil, &resp); err != nil {
		return PipelinePauseStatus{}, false, err
	}
	return resp.Pause, resp.Resumed != nil && *resp.Resumed, nil
}

// DecideRelease is Client.DecideRelease over the wire -- `grain release
// approve|reject vX.Y.0`: a person approving or rejecting a pending trunk
// release.
func (c *HTTPClient) DecideRelease(ctx context.Context, repo, version string, approve bool) (ReleaseDecision, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return ReleaseDecision{}, fmt.Errorf("repo %q is not owner/name", repo)
	}
	verb := "reject"
	if approve {
		verb = "approve"
	}
	var resp ReleaseDecision
	err := c.do(ctx, http.MethodPost, "/api/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+
		"/pending-releases/"+url.PathEscape(version)+"/"+verb, nil, &resp)
	return resp, err
}

// MergeBatches queries the merge batches currently in flight -- GET /api/merge-batches.
func (c *HTTPClient) MergeBatches(ctx context.Context, repo string) (MergeBatches, error) {
	var resp MergeBatches
	path := "/api/merge-batches"
	if repo != "" {
		path += "?repo=" + url.QueryEscape(repo)
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return MergeBatches{}, err
	}
	return resp, nil
}

// --- grain CI ------------------------------------------------------------
//
// The four calls behind `grain ci` (pkg/cli/cicmd.go), one per route
// pkg/ui/ci.go serves. They speak ci.Run rather than a shape of this
// package's for the reason Config.CI is: a run is served as it stands,
// and restating a dozen result fields here would be a second definition
// to keep in step for nobody's benefit.

// StartCIRun asks this deployment to run grain CI over one repository at
// one branch -- POST /api/ci/runs.
//
// It answers as soon as the run has been planned and begun, which is
// what the run it returns is: an id, the jobs it is about to run, and no
// conclusion yet. A run takes minutes and this call does not wait for
// one; CIRun below is how its caller finds out how it went.
//
// A repository grain CI is not enabled for is refused with the 403 that
// names the capability to attach, which arrives here as a plain error
// carrying that sentence.
func (c *HTTPClient) StartCIRun(ctx context.Context, req CIRunRequest) (ci.Run, error) {
	var run ci.Run
	if err := c.do(ctx, http.MethodPost, "/api/ci/runs", req, &run); err != nil {
		return ci.Run{}, err
	}
	return run, nil
}

// StartTaskCIRun starts grain CI using the target and branch fixed on a
// task, which is the form exposed to that task's agent.
func (c *HTTPClient) StartTaskCIRun(ctx context.Context, taskID string, jobs []string) (ci.Run, error) {
	return c.StartCIRun(ctx, CIRunRequest{TaskID: taskID, Jobs: jobs})
}

// RetriggerTaskCIRun starts a fresh, uncached grain CI run on a task's
// branch or commit, of the jobs named -- or, naming none, of the jobs
// whose latest result there did not pass, unless all asks for every one.
func (c *HTTPClient) RetriggerTaskCIRun(ctx context.Context, taskID string, workflow, commit string,
	jobs []string, all bool) (ci.Run, error) {
	return c.StartCIRun(ctx, CIRunRequest{
		TaskID:        taskID,
		Workflow:      workflow,
		Commit:        commit,
		Jobs:          jobs,
		Recheck:       true,
		RecheckFailed: len(jobs) == 0 && !all,
	})
}

// ListCIRuns returns the runs this deployment still remembers, newest
// first, narrowed to one repository when repo names one ("" for all of
// them).
//
// enabled is false when this deployment has no CI wired at all -- the
// same "there is nothing behind this question" answer AgentPause
// carries, and a different answer from a deployment that has simply run
// nothing yet.
//
// Each run arrives without its steps' output (ciListSummary); CIRun is
// what fetches one in full.
func (c *HTTPClient) ListCIRuns(ctx context.Context, repo string) (runs []ci.Run, enabled bool, err error) {
	path := "/api/ci/runs"
	if repo = strings.TrimSpace(repo); repo != "" {
		path += "?repo=" + url.QueryEscape(repo)
	}
	var resp CIRunsResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, false, err
	}
	return resp.Runs, resp.Enabled, nil
}

// CISharedFaults is what this deployment's CI is failing the same way on
// more than one branch right now -- GET /api/ci/faults.
//
// Asked on its own rather than read off a listing, because the caller
// that needs it most already holds the run it is asking about: a
// sandbox's wait_for_checks has just been handed a red verdict and wants
// the one sentence that says whose red it is (pkg/mcp, cmd/grain's
// daemonGrainCI).
func (c *HTTPClient) CISharedFaults(ctx context.Context) ([]CIFault, error) {
	var resp CIFaultsResponse
	if err := c.do(ctx, http.MethodGet, "/api/ci/faults", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Faults, nil
}

// FindCIRuns is ListCIRuns' narrowed form: the runs matching f, in the
// order f asks for and at most limit of them (zero for the route's own
// default), with the counts saying what the page leaves out.
//
// The filter travels as the query string rather than being applied to
// the answer, which is the whole point of it: the daemon asks its store,
// so "this job, failing, on any branch, in the last week" reaches every
// run the deployment keeps rather than the newest page of them
// (ci.RunStore.Find, grain/task-289).
func (c *HTTPClient) FindCIRuns(ctx context.Context, f ci.RunFilter, limit int) (CIRunsResponse, error) {
	var resp CIRunsResponse
	if err := c.do(ctx, http.MethodGet, ciRunsPath(f, limit, ""), nil, &resp); err != nil {
		return CIRunsResponse{}, err
	}
	return resp, nil
}

// FindCIRunsCompact is FindCIRuns asking for the compact view
// (CICompactRunsResponse): each run's identity and verdict and a line per
// job that did not succeed, which is what a page of a week's runs fits in.
func (c *HTTPClient) FindCIRunsCompact(ctx context.Context, f ci.RunFilter, limit int) (CICompactRunsResponse, error) {
	var resp CICompactRunsResponse
	if err := c.do(ctx, http.MethodGet, ciRunsPath(f, limit, "compact"), nil, &resp); err != nil {
		return CICompactRunsResponse{}, err
	}
	return resp, nil
}

// ciRunsPath is GET /api/ci/runs with a filter, a limit and a view as
// its query.
func ciRunsPath(f ci.RunFilter, limit int, view string) string {
	query := url.Values{}
	if repo := strings.TrimSpace(f.Repo); repo != "" {
		query.Set("repo", repo)
	}
	if ref := strings.TrimSpace(f.Ref); ref != "" {
		query.Set("ref", ref)
	}
	if job := strings.TrimSpace(f.Job); job != "" {
		query.Set("job", job)
	}
	for _, state := range f.States {
		query.Add("conclusion", state)
	}
	if !f.Since.IsZero() {
		// An instant rather than the span somebody may have typed: the
		// window is the one this caller already resolved, and re-reading
		// "24h" at the far end would measure it from a different now.
		query.Set("since", f.Since.UTC().Format(time.RFC3339))
	}
	if !f.Until.IsZero() {
		query.Set("until", f.Until.UTC().Format(time.RFC3339))
	}
	if f.Sort != "" {
		query.Set("sort", string(f.Sort))
	}
	if !f.Descending {
		query.Set("order", "asc")
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if view != "" {
		query.Set("view", view)
	}
	path := "/api/ci/runs"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return path
}

// CIRun returns one run in full: every job, every step, and every step's
// own output -- which is the whole reason anybody opens a run, and the
// one thing the listing leaves out.
//
// A run that has been forgotten (a deployment keeps a bounded history)
// is a NotFoundError carrying the route's own sentence about that, which
// is worth relaying rather than rewording: "no such run" and "that run
// has aged out" are different things to be told.
func (c *HTTPClient) CIRun(ctx context.Context, id string) (ci.Run, error) {
	var run ci.Run
	if err := c.do(ctx, http.MethodGet, "/api/ci/runs/"+url.PathEscape(id), nil, &run); err != nil {
		return ci.Run{}, err
	}
	return run, nil
}

// CIRunArtifacts is what one run left behind -- GET
// /api/ci/runs/{id}/artifacts.
//
// available is false for a deployment that keeps no artifacts at all,
// which is a different answer from a run that kept none: the first is a
// daemon with nowhere to put them (its log says why), the second is an
// ordinary green run.
func (c *HTTPClient) CIRunArtifacts(ctx context.Context, id string) (artifacts []ci.Artifact, available bool, err error) {
	var resp CIRunArtifactsResponse
	if err := c.do(ctx, http.MethodGet, "/api/ci/runs/"+url.PathEscape(id)+"/artifacts", nil, &resp); err != nil {
		return nil, false, err
	}
	return resp.Artifacts, resp.Available, nil
}

// DownloadCIArtifact writes one artifact's bytes to w and reports how
// many there were -- GET /api/ci/runs/{id}/artifacts/{job}/{name}.
//
// It does not go through do(), which decodes JSON into a value: an
// artifact is a gzipped tar of unbounded-ish size and the point of it is
// that it reaches a disk without being held in memory on the way. The
// error handling is do()'s own, so a 404 still arrives as a
// NotFoundError and reads the same as every other missing thing here.
func (c *HTTPClient) DownloadCIArtifact(ctx context.Context, ref ci.ArtifactRef, w io.Writer) (int64, error) {
	if err := ref.Validate(); err != nil {
		return 0, err
	}
	path := fmt.Sprintf("/api/ci/runs/%s/artifacts/%s/%s",
		url.PathEscape(ref.Run), url.PathEscape(ref.Job), url.PathEscape(ref.Name))
	resp, err := c.send(ctx, http.MethodGet, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, c.httpError(resp)
	}
	return io.Copy(w, resp.Body)
}

// CancelCIRun stops a run that is still going and returns it as it was
// left -- DELETE /api/ci/runs/{id}.
//
// Cancelling is asynchronous the way starting is: the jobs in flight are
// told to stop and the run reaches "cancelled" shortly afterwards, so
// the run this hands back has usually not got there yet. A run that has
// already finished, and one that never existed, are both the route's own
// 404 ("no CI run is still going"), since telling them apart would mean
// reporting success for a request nothing acted on.
//
// reason is the optional note about why, recorded on the run so that
// whoever reads it afterwards -- including the agent waiting on the
// branch it was judging -- is told rather than left to guess. Empty
// sends no body at all, which is the request this route has always
// answered.
func (c *HTTPClient) CancelCIRun(ctx context.Context, id, reason string) (ci.Run, error) {
	var body any
	if reason = strings.TrimSpace(reason); reason != "" {
		body = CICancelRequest{Reason: reason}
	}
	var run ci.Run
	if err := c.do(ctx, http.MethodDelete, "/api/ci/runs/"+url.PathEscape(id), body, &run); err != nil {
		return ci.Run{}, err
	}
	return run, nil
}

// CICache is what this deployment's job cache is holding -- GET
// /api/ci/cache, and `grain ci cache`.
//
// The response's own three states are passed through rather than
// flattened: a deployment with no CI (Enabled false), one running every
// job every time because it has no cache behind it (Available false),
// and one with a cache to describe. A caller that folded the first two
// into "nothing cached" would be telling an operator their cache is
// empty when the truth is that there is no cache.
func (c *HTTPClient) CICache(ctx context.Context) (CICacheResponse, error) {
	var resp CICacheResponse
	if err := c.do(ctx, http.MethodGet, "/api/ci/cache", nil, &resp); err != nil {
		return CICacheResponse{}, err
	}
	return resp, nil
}

// ClearCICache empties the job cache and returns what it left behind --
// DELETE /api/ci/cache, and `grain ci cache clear`.
//
// Removed says how many entries went. A deployment grain CI is not
// enabled anywhere on refuses this with the 403 that names the
// capability, which arrives here as a plain error carrying that
// sentence; a deployment with no cache to clear is a NotFoundError,
// since an action nothing acted on should not report success.
func (c *HTTPClient) ClearCICache(ctx context.Context) (CICacheResponse, error) {
	var resp CICacheResponse
	if err := c.do(ctx, http.MethodDelete, "/api/ci/cache", nil, &resp); err != nil {
		return CICacheResponse{}, err
	}
	return resp, nil
}

// LiftAgentPause is Client.LiftAgentPause over the wire -- `grain pause
// -lift`, the terminal's own half of the banner's "Resume now" button:
// open the gate now, reporting the state left behind and whether there
// was a pause to clear at all.
//
// A deployment with no gate wired answers 404, which surfaces here as
// the NotFoundError carrying the route's own message, rather than as the
// quiet enabled-false a read gets: an action that did nothing should say
// so. Lifting a pause that had already expired is not an error, and
// reports lifted false.
//
// lift names which pauses (AgentPauseLift); its zero value is every pause
// the caller may lift.
func (c *HTTPClient) LiftAgentPause(ctx context.Context, lift AgentPauseLift) (status AgentPauseStatus, lifted bool, err error) {
	var resp agentPauseResponse
	q := url.Values{}
	if lift.Connection != "" {
		q.Set("connection", lift.Connection)
	}
	if lift.Model != "" {
		q.Set("model", lift.Model)
	}
	if lift.Team != "" {
		q.Set("team", lift.Team)
	}
	if lift.Framework != "" {
		q.Set("framework", lift.Framework)
	}
	path := "/api/pause"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	if err := c.do(ctx, http.MethodDelete, path, nil, &resp); err != nil {
		return AgentPauseStatus{}, false, err
	}
	lifted = resp.Lifted != nil && *resp.Lifted
	if resp.Pause == nil {
		return AgentPauseStatus{}, lifted, nil
	}
	return *resp.Pause, lifted, nil
}

// ListGCPKeys is GET /api/gcp-keys.
func (c *HTTPClient) ListGCPKeys(ctx context.Context) ([]model.GCPKey, error) {
	var keys []model.GCPKey
	if err := c.do(ctx, http.MethodGet, "/api/gcp-keys", nil, &keys); err != nil {
		return nil, err
	}
	return keys, nil
}

// GetGCPKey is GET /api/gcp-keys/{name}.
func (c *HTTPClient) GetGCPKey(ctx context.Context, name string) (model.GCPKey, error) {
	var key model.GCPKey
	if err := c.do(ctx, http.MethodGet, "/api/gcp-keys/"+url.PathEscape(name), nil, &key); err != nil {
		return model.GCPKey{}, err
	}
	return key, nil
}

// SetGCPKey is PUT /api/gcp-keys/{name}.
func (c *HTTPClient) SetGCPKey(ctx context.Context, req SetGCPKeyRequest) (model.GCPKey, error) {
	var key model.GCPKey
	path := "/api/gcp-keys"
	if req.Name != "" {
		path += "/" + url.PathEscape(req.Name)
	}
	if err := c.do(ctx, http.MethodPut, path, req, &key); err != nil {
		return model.GCPKey{}, err
	}
	return key, nil
}

// DeleteGCPKey is DELETE /api/gcp-keys/{name}.
func (c *HTTPClient) DeleteGCPKey(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/api/gcp-keys/"+url.PathEscape(name), nil, nil)
}

// ListExecutors returns the connected remote executors snapshot from GET /api/executors.
func (c *HTTPClient) ListExecutors(ctx context.Context) ([]modelexec.Status, error) {
	var out []modelexec.Status
	if err := c.do(ctx, http.MethodGet, "/api/executors", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RequestLocalScriptApproval is the run's mcpserver asking for a person
// to approve one local-script call (POST
// /api/tasks/{id}/local-script-approvals); req is localexec.Request's
// JSON.
func (c *HTTPClient) RequestLocalScriptApproval(ctx context.Context, id string, req any) (LocalScriptApproval, error) {
	var out LocalScriptApproval
	err := c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/local-script-approvals", req, &out)
	return out, err
}

// WaitLocalScriptApproval long-polls approval until it is answered or the
// server's wait passes, and returns it as it then stands.
func (c *HTTPClient) WaitLocalScriptApproval(ctx context.Context, id, approval string) (LocalScriptApproval, error) {
	var out LocalScriptApproval
	err := c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/local-script-approvals/"+url.PathEscape(approval)+"/wait", struct{}{}, &out)
	return out, err
}

// ExpireLocalScriptApproval says the call behind approval stopped waiting.
func (c *HTTPClient) ExpireLocalScriptApproval(ctx context.Context, id, approval string) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/local-script-approvals/"+url.PathEscape(approval)+"/expire", struct{}{}, nil)
}

// AnswerLocalScriptApproval approves or denies approval, as a person.
func (c *HTTPClient) AnswerLocalScriptApproval(ctx context.Context, id, approval string, approve bool) (LocalScriptApproval, error) {
	var out LocalScriptApproval
	err := c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/local-script-approvals/"+url.PathEscape(approval)+"/answer",
		localScriptAnswerRequest{Approve: approve}, &out)
	return out, err
}
