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

package gitproxy

// Forge traffic through the host's own tool: `<tool> api`.
//
// Some forges sit behind a front end that answers any client without the
// workstation's own login with a redirect to a login page, and every path
// on them -- /api/v3/... and a repository's smart-HTTP endpoints alike --
// is behind it. A tool built on gh that applies that login and transport
// itself is then the one client that reaches them. So for a credential
// whose source is such a tool (AmbientCommandSource.ForgeViaCommand),
// grain does not open a connection of its own: every forge request -- the
// REST calls, and the git proxy's upstream hop -- is one run of
// `<tool> api` on the host, as the daemon's own account, with the same
// environment `<tool> auth token` gets.
//
// CommandForgeTransport is that as an http.RoundTripper, so it slots in
// under github.RealTransport (rate-limit gates, ETag revalidation, retries
// and Link-header pagination unchanged) and under RealForwarder (git's own
// headers and streamed bodies unchanged). A REST call's Authorization
// header is dropped, and the tool authenticates the call with its own
// login -- the same login `<tool> auth token` hands the ambient
// credential, so it is the same identity either way. A git smart-HTTP
// request is the one exception when the source says how its host's git
// endpoints take a token (CommandForge.GitAuthorization): that header is
// handed to the tool (see args for why on its command line).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/textproto"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/authfix"
	"github.com/ai-on-gke/grain/pkg/procgroup"
)

// commandForgeTimeout bounds one REST call through the tool when the
// caller set no tighter bound of its own (the request's context,
// http.Client's Timeout).
const commandForgeTimeout = 60 * time.Second

// commandForgeMaxLoginPage is how much of an HTML answer is read to tell
// a login page from anything else.
const commandForgeMaxLoginPage = 1 << 20

// CommandForgeTransport sends each request as one `<command> api` run on
// this host. The zero value is not usable: newCommandForgeTransport
// builds one from a source.
type CommandForgeTransport struct {
	// Host is the host the tool is asked about (GH_HOST).
	Host string
	// APIBase is scheme://host/prefix of the host's REST API: a request
	// under it is given to the tool as the endpoint relative to it, which
	// the tool prefixes with the same base itself, and any other URL
	// (git smart HTTP) is given whole.
	APIBase string
	// Proxy, when set, is put in the tool's environment as HTTPS_PROXY:
	// an operator's proxy for the host, honoured but never required.
	Proxy string
	// Timeout bounds one run; zero is commandForgeTimeout. The request's
	// own context bounds it too.
	Timeout time.Duration

	// Source is the ambient source the tool is, by name: what a daemon
	// beside grain hostd asks the host's host-git server to run.
	Source string
	// Command is the tool, looked up on PATH; Env is added to the
	// account's environment for it ({host} already expanded).
	Command string
	Env     []string
	// LoginFix is the command that logs the account in to Host, for the
	// refusal of a tool that is not installed.
	LoginFix string
	// Forge is the source's own part: how its host's git endpoints take
	// a token, and what its login page looks like.
	Forge CommandForge
}

// newCommandForgeTransport is the transport for h through src, the
// source whose tool reaches it.
func newCommandForgeTransport(src AmbientCommandSource, h CredentialHost, timeout time.Duration) *CommandForgeTransport {
	host := hostName(h.Host)
	env := make([]string, 0, len(src.Env))
	for _, kv := range src.Env {
		env = append(env, expandHost(kv, host))
	}
	t := &CommandForgeTransport{Host: host, Proxy: h.Proxy, Timeout: timeout, Source: src.Name, Command: src.Command, Env: env,
		LoginFix: expandHost(src.LoginFix, host)}
	if src.ForgeViaCommand != nil {
		t.Forge = *src.ForgeViaCommand
	}
	if apiHost, prefix, ok := h.APIBase(); ok {
		scheme := "https"
		if !h.useTLS() {
			scheme = "http"
		}
		t.APIBase = scheme + "://" + apiHost + prefix
	}
	return t
}

// commandForgeClient is an http.Client sending every request through
// src's tool. timeout bounds each request whole, body included; zero
// leaves only the transport's own bound.
func commandForgeClient(src AmbientCommandSource, h CredentialHost, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: newCommandForgeTransport(src, h, timeout)}
}

// commandForgeSkippedHeaders are request headers never handed to the
// tool as they came: the credential (the tool authenticates a REST call
// with its own login, and args rebuilds a git request's in the one shape
// the source says its git endpoints take), and the hop-by-hop and
// encoding headers the tool's own HTTP client sets for itself -- left to
// it, it also undoes the compression it asked for, so the body that comes
// back is the one its headers describe.
var commandForgeSkippedHeaders = map[string]bool{
	"Authorization":       true,
	"Proxy-Authorization": true,
	"Cookie":              true,
	"Host":                true,
	"Content-Length":      true,
	"Transfer-Encoding":   true,
	"Connection":          true,
	"Accept-Encoding":     true,
}

// endpoint is u as the tool's api command takes it: relative to its REST
// API when u is under APIBase, and the whole URL otherwise. The host's
// GraphQL endpoint -- /api/graphql beside an Enterprise Server's
// /api/v3, where github.RealTransport sends GraphQL -- is the tool's own
// "graphql", which it posts to wherever its host serves GraphQL.
func (t *CommandForgeTransport) endpoint(u *url.URL) string {
	full := u.String()
	if t.APIBase != "" {
		base := strings.TrimSuffix(t.APIBase, "/")
		if root, ok := strings.CutSuffix(base, "/v3"); ok && full == root+"/graphql" {
			return "graphql"
		}
		if rest, ok := strings.CutPrefix(full, base+"/"); ok {
			return rest
		}
	}
	return full
}

// checkHost refuses u when it is on neither Host nor the host of APIBase.
// The tool is logged in to Host and sends its own login with whatever it
// is given, and a whole URL is sent where it points (endpoint): a request
// bound for some other forge -- a GraphQL call built for api.github.com,
// say -- would carry this host's login there, so it is never run.
func (t *CommandForgeTransport) checkHost(u *url.URL) error {
	got := strings.ToLower(u.Hostname())
	allowed := []string{strings.ToLower(hostName(t.Host))}
	if t.APIBase != "" {
		if base, err := url.Parse(t.APIBase); err == nil && base.Hostname() != "" {
			allowed = append(allowed, strings.ToLower(base.Hostname()))
		}
	}
	for _, h := range allowed {
		if got == h {
			return nil
		}
	}
	return fmt.Errorf("refusing to send %s through %s: it is bound for %s, and %s is logged in to %s",
		redactQuery(u.Path), t.Command, u.Hostname(), t.Command, t.Host)
}

// isGitSmartHTTP reports whether u is one of a repository's smart-HTTP
// endpoints (info/refs, git-upload-pack, git-receive-pack) rather than
// a call to the host's REST API.
func (t *CommandForgeTransport) isGitSmartHTTP(u *url.URL) bool {
	if t.endpoint(u) != u.String() {
		return false
	}
	for _, suffix := range []string{"/info/refs", "/git-upload-pack", "/git-receive-pack"} {
		if strings.HasSuffix(u.Path, suffix) {
			return true
		}
	}
	return false
}

// requestToken is the token req's Authorization header carries, in any
// of the shapes grain sends one: Basic with the token as the password
// (x-access-token:<token>) or as the username (<token>:), or
// "token <token>" / "Bearer <token>".
func requestToken(req *http.Request) string {
	auth := strings.TrimSpace(req.Header.Get("Authorization"))
	scheme, value, _ := strings.Cut(auth, " ")
	value = strings.TrimSpace(value)
	switch strings.ToLower(scheme) {
	case "basic":
		raw, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return ""
		}
		user, pass, _ := strings.Cut(string(raw), ":")
		if pass != "" {
			return pass
		}
		return user
	case "token", "bearer":
		return value
	}
	return ""
}

// args is the tool's command line for req. A REST call carries no
// credential: the tool authenticates it with its own login. A git
// smart-HTTP request carries one when the source names the header its
// git endpoints take (CommandForge.GitAuthorization), as -H, because
// such endpoints refuse the header the tool would send and gh offers no
// other way to set a header: -H is argv only, with no file or stdin form,
// and GH_TOKEN/GH_ENTERPRISE_TOKEN are sent as "token <token>" too. What
// that costs is bounded: the tool runs as the install user on a
// single-user workstation, the token is the short-lived one
// `<tool> auth token` already prints to the same account, and grain never
// logs or reports argv -- the log line and every error name the endpoint
// only (commandforge_test.go asserts that).
func (t *CommandForgeTransport) args(req *http.Request, hasBody bool) []string {
	args := []string{"api", "--include", "--method", req.Method}
	if t.Forge.GitAuthorization != nil && t.isGitSmartHTTP(req.URL) {
		if token := requestToken(req); token != "" && !strings.ContainsAny(token, "\r\n") {
			if auth := t.Forge.GitAuthorization(token); auth != "" && !strings.ContainsAny(auth, "\r\n") {
				args = append(args, "-H", "Authorization: "+auth)
			}
		}
	}
	keys := make([]string, 0, len(req.Header))
	for k := range req.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if commandForgeSkippedHeaders[textproto.CanonicalMIMEHeaderKey(k)] {
			continue
		}
		for _, v := range req.Header[k] {
			if strings.ContainsAny(k+v, "\r\n") {
				continue
			}
			args = append(args, "-H", k+": "+v)
		}
	}
	if hasBody {
		args = append(args, "--input", "-")
	}
	return append(args, t.endpoint(req.URL))
}

// env is the tool's environment: the account's own (ambientEnv), the
// source's, the operator's proxy when there is one, and nothing that
// would make the tool print for a terminal.
func (t *CommandForgeTransport) env() []string {
	var out []string
	for _, kv := range ambientEnv() {
		if strings.HasPrefix(kv, "GH_FORCE_TTY=") || strings.HasPrefix(kv, "CLICOLOR_FORCE=") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, t.Env...)
	out = append(out, "NO_COLOR=1", "GH_PAGER=")
	if t.Proxy != "" {
		out = append(out, "HTTPS_PROXY="+t.Proxy, "https_proxy="+t.Proxy)
	}
	return out
}

// commandForgeLoginRetries are the waits before each new try of an
// idempotent request the tool answered with a login page. A front end's
// refusal is often transient -- a bridge whose login lapses on and off --
// so a read is tried again a few times before the login is reported.
// Tests shorten it.
var commandForgeLoginRetries = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second}

// commandForgeMaxReplayBody is the largest request body kept in memory so
// an idempotent request can be sent again; a larger one is sent once.
const commandForgeMaxReplayBody = 8 << 20

// RoundTrip runs the tool for req and answers with what it printed: the
// status line and headers --include puts first, and the body after them,
// streamed. A request the tool could not send at all is an error naming
// the command and what it said on stderr (never its stdout, which is the
// response body); one answered with a login page is LoginRedirectError.
//
// An idempotent request (loginRetryable) the tool reports answering with
// a login page (CommandForge.LoginPage), one the source deems worth
// waiting out (CommandForge.RetryLogin), is tried again after each of
// commandForgeLoginRetries before the LoginRedirectError is returned. A
// front end's own redirect to its login (LoginRedirectError.Redirected)
// is returned at once: that login does not come back by itself. A push or a REST write is never sent twice here: whether it
// took effect is the caller's to find out.
func (t *CommandForgeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.loginRetryable(req) || len(commandForgeLoginRetries) == 0 {
		return t.roundTripOnce(req)
	}
	replay, err := replayableBody(req)
	if err != nil {
		return nil, err
	}
	if replay == nil {
		return t.roundTripOnce(req)
	}
	for attempt := 0; ; attempt++ {
		try := req
		if attempt > 0 {
			try = req.Clone(req.Context())
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					return nil, err
				}
				try.Body = body
			}
		}
		resp, err := t.roundTripOnce(try)
		login, ok := AsLoginRedirect(err)
		if !ok || login.Redirected || attempt >= len(commandForgeLoginRetries) || !t.Forge.retryLogin(login.Location) {
			return resp, err
		}
		wait := commandForgeLoginRetries[attempt]
		log.Printf("gitproxy: %s %s answered with a login page (%s); trying again in %s",
			req.Method, redactQuery(t.endpoint(req.URL)), login.Fix.Summary, wait)
		timer := time.NewTimer(wait)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, login
		case <-timer.C:
		}
	}
}

// loginRetryable reports whether req may be sent again when it was
// answered with a login page: a REST read (GET, HEAD), or a fetch's git
// smart-HTTP requests (info/refs, git-upload-pack). git-receive-pack and
// every other write are not.
func (t *CommandForgeTransport) loginRetryable(req *http.Request) bool {
	if t.isGitSmartHTTP(req.URL) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/git-receive-pack"):
			return false
		case strings.HasSuffix(req.URL.Path, "/info/refs"):
			// info/refs?service=git-receive-pack only advertises refs:
			// it changes nothing, so it is as safe to repeat as a fetch's.
			return req.Method == http.MethodGet || req.Method == http.MethodHead
		default:
			return req.Method == http.MethodGet || req.Method == http.MethodPost
		}
	}
	return req.Method == http.MethodGet || req.Method == http.MethodHead
}

// replayableBody makes req's body one that can be read again, setting
// req.GetBody when it has a body and none was set. It answers nil when
// the body is too large to keep, with req.Body still holding all of it,
// so req is sent once as it is.
func replayableBody(req *http.Request) (*http.Request, error) {
	if req.Body == nil || req.Body == http.NoBody || req.GetBody != nil {
		return req, nil
	}
	head, err := io.ReadAll(io.LimitReader(req.Body, commandForgeMaxReplayBody+1))
	if err != nil {
		req.Body.Close()
		return nil, err
	}
	if len(head) > commandForgeMaxReplayBody {
		req.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(head), req.Body), req.Body}
		return nil, nil
	}
	req.Body.Close()
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(head)), nil }
	req.Body, _ = req.GetBody()
	return req, nil
}

// roundTripOnce is RoundTrip's one try, through the host's host-git
// server beside grain hostd (which tries once too: the retries are this
// side's) or by running the tool here.
func (t *CommandForgeTransport) roundTripOnce(req *http.Request) (*http.Response, error) {
	if err := t.checkHost(req.URL); err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, err
	}
	if c := activeHostGit(); c != nil {
		return c.forge(t, req)
	}
	hasBody := req.Body != nil && req.Body != http.NoBody
	closeBody := func() {
		if req.Body != nil {
			req.Body.Close()
		}
	}
	path, err := exec.LookPath(t.Command)
	if err != nil {
		closeBody()
		return nil, fmt.Errorf("%s is not installed for %s: install it and run `%s`",
			t.Command, ambientAccount(), t.LoginFix)
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = commandForgeTimeout
	}
	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	args := t.args(req, hasBody)
	line := t.Command + " api --method " + req.Method + " " + redactQuery(t.endpoint(req.URL))
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = t.env()
	if hasBody {
		cmd.Stdin = req.Body
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		closeBody()
		return nil, err
	}
	stderr := &cappedBuffer{limit: 64 << 10}
	cmd.Stderr = stderr
	procgroup.Prepare(cmd)
	cmd.WaitDelay = ambientWaitDelay
	start := time.Now()
	if err := cmd.Start(); err != nil {
		cancel()
		closeBody()
		return nil, fmt.Errorf("running `%s`: %w", line, authfix.Wrap(err, "", path))
	}
	run := &commandForgeRun{cmd: cmd, cancel: cancel, line: line, host: req.URL.Host, start: start, ctx: ctx,
		timeout: timeout, stderr: stderr, forge: t.Forge, loginFix: t.LoginFix}
	br := bufio.NewReaderSize(stdout, 32<<10)
	resp, headErr := readCommandForgeHead(br)
	if headErr != nil {
		io.Copy(io.Discard, br)
		werr := run.wait()
		return nil, run.failure(werr, headErr)
	}
	resp.Request = req
	run.status = resp.StatusCode
	if commandForgeNoBody(req.Method, resp.StatusCode) {
		const maxNoBodyDrain = 64 << 10
		n, _ := io.Copy(io.Discard, io.LimitReader(br, maxNoBodyDrain+1))
		if n > maxNoBodyDrain {
			run.cancel()
		}
		run.wait()
		resp.Body = http.NoBody
		resp.ContentLength = 0
	} else {
		resp.Body = &commandForgeBody{r: br, run: run, status: resp.StatusCode}
	}

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if login, ok := NewLoginRedirect(req.URL.Host, resp.Header.Get("Location")); ok {
			resp.Body.Close()
			login.Redirected = true
			return nil, login
		}
	}
	if t.Forge.LoginPage != nil && strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		// The tool follows redirects itself, so a lapsed login can come
		// back as the login page, 200 and all.
		page, perr := io.ReadAll(io.LimitReader(resp.Body, commandForgeMaxLoginPage))
		resp.Body.Close()
		if login, ok := t.Forge.loginIn(req.URL.Host, string(page)); ok {
			return nil, login
		}
		if errors.Is(perr, ErrForgeBodyCut) {
			// Not the whole page: never handed on as if it were.
			return nil, perr
		}
		resp.Body = io.NopCloser(bytes.NewReader(page))
		resp.ContentLength = int64(len(page))
	}
	return resp, nil
}

// readCommandForgeHead reads the status line and headers --include prints ahead of
// the body.
func readCommandForgeHead(br *bufio.Reader) (*http.Response, error) {
	var status string
	for {
		line, err := br.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "" {
			status = line
			break
		}
		if err != nil {
			return nil, errors.New("it printed no response")
		}
	}
	proto, rest, _ := strings.Cut(status, " ")
	codeText, reason, _ := strings.Cut(rest, " ")
	code, err := strconv.Atoi(codeText)
	if !strings.HasPrefix(proto, "HTTP/") || err != nil || code < 100 || code > 999 {
		return nil, errors.New("its output did not start with an HTTP status line")
	}
	header, err := textproto.NewReader(br).ReadMIMEHeader()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("reading the headers it printed: %v", err)
	}
	h := http.Header(header)
	// The body is whatever follows, as the tool's client decoded it:
	// any length or encoding it printed described the wire, not this.
	h.Del("Content-Length")
	h.Del("Transfer-Encoding")
	h.Del("Content-Encoding")
	major, minor, ok := http.ParseHTTPVersion(proto)
	if !ok {
		major, minor = 1, 1
	}
	return &http.Response{
		Status:        strings.TrimSpace(codeText + " " + reason),
		StatusCode:    code,
		Proto:         proto,
		ProtoMajor:    major,
		ProtoMinor:    minor,
		Header:        h,
		ContentLength: -1,
	}, nil
}

// commandForgeRun is one running tool, waited for exactly once.
type commandForgeRun struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	ctx     context.Context
	line    string
	host    string
	start   time.Time
	timeout time.Duration
	stderr  *cappedBuffer
	forge   CommandForge
	// loginFix is the source's LoginFix, for a tool that says it is not
	// logged in.
	loginFix string
	// status is the status the tool printed, once its head has been read.
	status int

	once sync.Once
	err  error
	// ended is why ctx had ended by the time the tool exited, if it had:
	// wait cancels ctx itself afterwards, so ctx.Err() cannot tell a
	// run that was stopped from one that exited on its own.
	ended error
}

// wait reaps the tool and logs how the run ended -- never what it
// printed, which is a response body.
func (r *commandForgeRun) wait() error {
	r.once.Do(func() {
		err := r.cmd.Wait()
		if errors.Is(err, exec.ErrWaitDelay) {
			err = nil
		}
		took := time.Since(r.start).Round(time.Millisecond)
		if err != nil {
			r.ended = r.ctx.Err()
		}
		switch {
		case r.ended != nil:
			log.Printf("gitproxy: `%s` stopped after %s: %v", r.line, took, r.ended)
		case err != nil && r.status > 0 && r.status < 300:
			// A success it printed and then failed on: what it said is
			// the only account of what became of the body.
			log.Printf("gitproxy: `%s` printed HTTP %d, then exited %v in %s: stderr: %q",
				r.line, r.status, err, took, capLog(redactURLQueries(r.stderr.String())))
		case err != nil:
			// The tool exits non-zero for any status past 299, having
			// printed the response all the same: not a failure here.
			log.Printf("gitproxy: `%s` exited %v in %s", r.line, err, took)
		default:
			log.Printf("gitproxy: `%s` finished in %s", r.line, took)
		}
		r.err = err
		r.cancel()
	})
	return r.err
}

// failure is the error for a run that printed no response.
func (r *commandForgeRun) failure(werr, headErr error) error {
	said := strings.TrimSpace(r.stderr.String())
	if login, ok := r.forge.loginIn(r.host, said); ok {
		return login
	}
	if expired := authfix.Wrap(werr, said, r.cmd.Path); expired != werr {
		return fmt.Errorf("running `%s`: %w", r.line, expired)
	}
	if errors.Is(r.ended, context.DeadlineExceeded) {
		return fmt.Errorf("%w: `%s` did not answer within %s, so it was killed: run it by hand as %s "+
			"to see what it waits on", errHostToolTimeout, r.line, r.timeout, ambientAccount())
	}
	if r.ended != nil {
		return fmt.Errorf("`%s` was cancelled: %v", r.line, r.ended)
	}
	msg := shortMessage(said)
	if r.loginFix != "" && saysNotLoggedIn(said) {
		return fmt.Errorf("`%s` failed: %s: run `%s` as %s", r.line, msg, r.loginFix, ambientAccount())
	}
	if msg == "" && werr != nil {
		msg = werr.Error()
	}
	if msg == "" {
		msg = headErr.Error()
	}
	return fmt.Errorf("`%s` failed: %s", r.line, msg)
}

// saysNotLoggedIn reports whether a tool's stderr says it has no login
// for the host, the way gh-style tools do ("not logged in", "please run:
// ... auth login").
func saysNotLoggedIn(said string) bool {
	said = strings.ToLower(said)
	return strings.Contains(said, "not logged in") || strings.Contains(said, "auth login")
}

// commandForgeNoBody reports whether an HTTP response carries no entity body
// by RFC 9110 (1xx, 204 No Content, 304 Not Modified, or any response to HEAD),
// so RoundTrip can let the tool exit cleanly instead of cancelling it on Close.
func commandForgeNoBody(method string, status int) bool {
	return method == http.MethodHead ||
		status == http.StatusNotModified ||
		status == http.StatusNoContent ||
		(status >= 100 && status < 200)
}

// commandForgeBody is the response body still being printed; reading it to the
// end, or closing it, reaps the tool.
type commandForgeBody struct {
	r      io.Reader
	run    *commandForgeRun
	status int
	eof    bool
}

// Read ends with io.EOF only when the tool ended well. One that printed a
// success and then exited non-zero, or was stopped, did not finish the
// body: that end is an error naming the status, the exit and what the
// tool said, never a short answer passed off as the whole one. A status
// of 300 or more ends in io.EOF all the same, since the tool exits
// non-zero for those by design.
func (b *commandForgeBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if errors.Is(err, io.EOF) {
		b.eof = true
		werr := b.run.wait()
		if b.status < 300 && (werr != nil || b.run.ended != nil) {
			return n, b.run.cut(werr)
		}
	}
	return n, err
}

// cut is the error for a body the tool stopped printing early, werr
// being how it exited.
func (r *commandForgeRun) cut(werr error) error {
	how := "exited"
	var exit *exec.ExitError
	switch {
	case errors.Is(r.ended, context.DeadlineExceeded):
		how = fmt.Sprintf("was killed after %s", r.timeout)
	case r.ended != nil:
		how = fmt.Sprintf("was stopped (%v)", r.ended)
	case errors.As(werr, &exit) && exit.ExitCode() >= 0:
		how = fmt.Sprintf("exited %d", exit.ExitCode())
	case werr != nil:
		how = fmt.Sprintf("ended (%v)", werr)
	}
	msg := fmt.Sprintf("`%s` printed HTTP %d and its headers, then %s%s", r.line, r.status, how, bodyCutMarker)
	if said := shortMessage(redactURLQueries(r.stderr.String())); said != "" {
		msg += ": " + said
	} else {
		msg += ", saying nothing on stderr"
	}
	return &forgeBodyCutError{msg: msg}
}

// ErrForgeBodyCut is what a forge answer read through a command-line
// tool is, by errors.Is, when the tool printed a success and its headers
// and then failed or was stopped before the body ended -- directly or
// through the host's host-git server. The body read so far is not the
// whole answer.
var ErrForgeBodyCut = errors.New("the forge tool stopped before the body ended")

// bodyCutMarker is the phrase every ErrForgeBodyCut message carries, by
// which the error is known again after host-git has carried it as text.
const bodyCutMarker = " before the body ended"

type forgeBodyCutError struct{ msg string }

func (e *forgeBodyCutError) Error() string        { return e.msg }
func (e *forgeBodyCutError) Is(target error) bool { return target == ErrForgeBodyCut }

// capLog is a tool's stderr bounded for a log line.
func capLog(s string) string {
	s = strings.TrimSpace(s)
	if runes := []rune(s); len(runes) > 2000 {
		s = string(runes[:2000]) + "..."
	}
	return s
}

func (b *commandForgeBody) Close() error {
	if !b.eof {
		// Closed before the end: the rest is not wanted, so the tool is
		// stopped rather than left printing into a pipe nobody reads.
		b.run.cancel()
	}
	b.run.wait()
	return nil
}

// redactQuery is an endpoint without its query, for a log line.
func redactQuery(endpoint string) string {
	if i := strings.IndexByte(endpoint, '?'); i >= 0 {
		return endpoint[:i]
	}
	return endpoint
}

// urlQuery is a URL's query in free text, up to the quote or space that
// ends the URL.
var urlQuery = regexp.MustCompile(`(https?://[^\s"'?]+)\?[^\s"']*`)

// redactURLQueries is a tool's stderr with every URL's query cut off. A
// tool that followed a redirect itself names the URL it failed on, and a
// redirect to a log blob is signed in its query: what the tool said
// reaches the agent reading the log, the signature must not.
func redactURLQueries(s string) string {
	return urlQuery.ReplaceAllString(s, "$1?[redacted]")
}

// retryLogin reports whether a request answered with a login page at
// location is worth sending again: RetryLogin's verdict, or yes when the
// source says nothing.
func (f CommandForge) retryLogin(location string) bool {
	if f.RetryLogin == nil {
		return true
	}
	return f.RetryLogin(location)
}

// loginIn is the LoginRedirectError for text -- the tool's stderr, or an
// HTML page it answered with -- when the source's LoginPage finds its
// host's login page in it.
func (f CommandForge) loginIn(host, text string) (*LoginRedirectError, bool) {
	if f.LoginPage == nil {
		return nil, false
	}
	location, ok := f.LoginPage(text)
	if !ok {
		return nil, false
	}
	return NewLoginRedirect(host, location)
}
