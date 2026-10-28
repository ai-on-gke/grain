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
// API when u is under APIBase, and the whole URL otherwise.
func (t *CommandForgeTransport) endpoint(u *url.URL) string {
	full := u.String()
	if t.APIBase != "" {
		base := strings.TrimSuffix(t.APIBase, "/")
		if rest, ok := strings.CutPrefix(full, base+"/"); ok {
			return rest
		}
	}
	return full
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

// RoundTrip runs the tool for req and answers with what it printed: the
// status line and headers --include puts first, and the body after them,
// streamed. A request the tool could not send at all is an error naming
// the command and what it said on stderr (never its stdout, which is the
// response body); one answered with a login page is LoginRedirectError.
func (t *CommandForgeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
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
		timeout: timeout, stderr: stderr, forge: t.Forge}
	br := bufio.NewReaderSize(stdout, 32<<10)
	resp, headErr := readCommandForgeHead(br)
	if headErr != nil {
		io.Copy(io.Discard, br)
		werr := run.wait()
		return nil, run.failure(werr, headErr)
	}
	resp.Request = req
	resp.Body = &commandForgeBody{r: br, run: run}

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if login, ok := NewLoginRedirect(req.URL.Host, resp.Header.Get("Location")); ok {
			resp.Body.Close()
			return nil, login
		}
	}
	if t.Forge.LoginPage != nil && strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		// The tool follows redirects itself, so a lapsed login can come
		// back as the login page, 200 and all.
		page, _ := io.ReadAll(io.LimitReader(resp.Body, commandForgeMaxLoginPage))
		resp.Body.Close()
		if login, ok := t.Forge.loginIn(req.URL.Host, string(page)); ok {
			return nil, login
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

	once sync.Once
	err  error
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
		switch {
		case r.ctx.Err() != nil && err != nil:
			log.Printf("gitproxy: `%s` stopped after %s: %v", r.line, took, r.ctx.Err())
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
	if errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: `%s` did not answer within %s, so it was killed: run it by hand as %s "+
			"to see what it waits on", errHostToolTimeout, r.line, r.timeout, ambientAccount())
	}
	if r.ctx.Err() != nil {
		return fmt.Errorf("`%s` was cancelled: %v", r.line, r.ctx.Err())
	}
	msg := shortMessage(said)
	if msg == "" && werr != nil {
		msg = werr.Error()
	}
	if msg == "" {
		msg = headErr.Error()
	}
	return fmt.Errorf("`%s` failed: %s", r.line, msg)
}

// commandForgeBody is the response body still being printed; reading it to the
// end, or closing it, reaps the tool.
type commandForgeBody struct {
	r   io.Reader
	run *commandForgeRun
}

func (b *commandForgeBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if errors.Is(err, io.EOF) {
		b.run.wait()
	}
	return n, err
}

func (b *commandForgeBody) Close() error {
	// Closed before the end: the rest is not wanted, so the tool is
	// stopped rather than left printing into a pipe nobody reads.
	b.run.cancel()
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
