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

// The host's own login, through grain hostd.
//
// An ambient credential borrows the login of the account grain was
// installed as: `gh auth token`, or a registered source's token and api
// commands (`forgecli auth token`, `forgecli api`), run as
// that account, with its home directory. A daemon on the bare machine
// runs them itself. A daemon in the release container has neither the
// tools nor the login, so it asks the host for them instead: through
// grain hostd, which starts `grain capability host-git` -- the in-tree
// capability server HostGitEntry names -- on the host, as the installing
// user, for one call at a time.
//
// The server is the narrow part. hostd's own argv checks treat any
// argument with a "/" as a path under the data directory, which every
// `<tool> api` endpoint fails, so the daemon never hands hostd an argv: the
// allowlist entry fixes the server's command line, and the server takes
// a request over its stdin that names a registered ambient source and a
// host, and runs only that source's own command for it -- its token
// command, its package-token command, or `<tool> api` for a source whose
// forge traffic goes through its tool (ForgeViaCommand), each exactly as
// a daemon on the host would run it. The git source (`git credential
// fill`) is not among them: it would hand the container every login the
// account's credential helpers hold, so it stays a host-mode source.
//
// docs/git-hosts.md ("Ambient credentials") and docs/design/
// cli-run-location.md ("Security") are the argument for letting the
// container have the token at all: the daemon is trusted as the
// installing user in both modes, and a host-mode daemon holds the same
// token.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

// HostGitEntry is the in-tree capability server that runs ambient
// sources' tools on the host (`grain capability host-git`), and the name
// of the capability entry in grain hostd's allowlist that starts it.
const HostGitEntry = "host-git"

// Tools the host-git server serves.
const (
	hostGitToolToken        = "ambient_token"
	hostGitToolPackageToken = "package_token"
	hostGitToolForge        = "forge_request"
	hostGitToolIdentity     = "git_identity"
	hostGitToolSign         = "git_sign"
)

// hostGitMaxBody bounds one forge response the server carries back over
// the socket, which it has to hold whole: a pack bigger than this is
// refused rather than taken into memory twice over.
const hostGitMaxBody = 512 << 20

// hostGitStartSlack is what a host-git call is allowed beyond the bound
// of the command it runs: a hostd connection and a grain process start.
const hostGitStartSlack = 15 * time.Second

// hostGitOffersTTL is how long one look at what hostd offers is trusted
// by AmbientAvailable, so a burst of loads asks hostd once.
const hostGitOffersTTL = 15 * time.Second

// HostGitClient is the daemon's side of host-git: every ambient source's
// tool runs through it once UseHostGit has installed one.
type HostGitClient struct {
	Hostd hostd.Client
	// Stderr receives the server's diagnostics; nil discards them.
	Stderr io.Writer

	mu      sync.Mutex
	checked time.Time
	offered error
}

var hostGit atomic.Pointer[HostGitClient]

// UseHostGit makes every ambient source's tool run on the host through
// c, for a daemon in the release container beside grain hostd; nil goes
// back to running them as this process's own children. It returns what
// puts the previous one back (for a test).
func UseHostGit(c *HostGitClient) (restore func()) {
	prev := hostGit.Swap(c)
	return func() { hostGit.Store(prev) }
}

// activeHostGit is the client UseHostGit installed, or nil.
func activeHostGit() *HostGitClient { return hostGit.Load() }

// available is nil when hostd answers and its allowlist offers the
// host-git entry.
func (c *HostGitClient) available() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.checked.IsZero() && time.Since(c.checked) < hostGitOffersTTL {
		return c.offered
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c.offered = nil
	offers, err := c.Hostd.Offers(ctx)
	if err != nil {
		c.offered = err
	} else {
		found := false
		for _, o := range offers {
			found = found || (o.Kind == hostd.KindCapability && o.Name == HostGitEntry)
		}
		if !found {
			c.offered = fmt.Errorf("grain hostd answers, but its allowlist (%s) offers no capability entry %q: "+
				"re-run setup.sh, or add the entry by hand (docs/git-hosts.md)",
				hostd.DefaultAllowlistPath, HostGitEntry)
		}
	}
	c.checked = time.Now()
	return c.offered
}

// call starts the host-git server through hostd, makes one call and
// stops it. One process per call: calls on one stdio session are
// serialized, and a cancelled one stops the server, so a slow push would
// otherwise hold up every token lookup behind it.
func (c *HostGitClient) call(ctx context.Context, tool string, args map[string]any, out any) error {
	opts := protocol.DialOptions{Hostd: &c.Hostd, Stderr: c.Stderr}
	client, err := protocol.Dial(ctx, protocol.ServerConfig{Command: HostGitEntry, Run: protocol.RunHost}, opts)
	if err != nil {
		return err
	}
	defer client.Close()
	raw, err := client.CallTool(ctx, tool, args)
	if err != nil {
		return fmt.Errorf("the host's %s server: %w", HostGitEntry, err)
	}
	var res struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("the host's %s server: %s: %w", HostGitEntry, tool, err)
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *HostGitClient) token(source, host string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ambientCommandTimeout+hostGitStartSlack)
	defer cancel()
	var res struct {
		Token string `json:"token"`
	}
	if err := c.call(ctx, hostGitToolToken, map[string]any{"source": source, "host": host}, &res); err != nil {
		return "", err
	}
	return res.Token, nil
}

func (c *HostGitClient) packageToken(source, host, registry string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ambientCommandTimeout+hostGitStartSlack)
	defer cancel()
	var res struct {
		Token string `json:"token"`
	}
	if err := c.call(ctx, hostGitToolPackageToken, map[string]any{"source": source, "host": host, "registry": registry}, &res); err != nil {
		return "", err
	}
	return res.Token, nil
}

func (c *HostGitClient) identity() (CommitIdentity, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*ambientCommandTimeout+hostGitStartSlack)
	defer cancel()
	var res struct {
		Name       string `json:"name"`
		Email      string `json:"email"`
		SigningKey string `json:"signing_key"`
	}
	if err := c.call(ctx, hostGitToolIdentity, map[string]any{}, &res); err != nil {
		return CommitIdentity{}, err
	}
	return CommitIdentity{Name: res.Name, Email: res.Email, SigningKey: res.SigningKey}, nil
}

func (c *HostGitClient) sign(ctx context.Context, namespace string, payload []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*ambientCommandTimeout+hostGitStartSlack)
	defer cancel()
	var res struct {
		Signature string `json:"signature"`
	}
	args := map[string]any{
		"namespace": namespace,
		"payload":   base64.StdEncoding.EncodeToString(payload),
	}
	if err := c.call(ctx, hostGitToolSign, args, &res); err != nil {
		return nil, err
	}
	return []byte(res.Signature), nil
}

// hostGitForgeResult is forge_request's answer: the response, or the
// login page the tool was sent to.
type hostGitForgeResult struct {
	Error         string              `json:"error,omitempty"`
	LoginHost     string              `json:"login_host,omitempty"`
	LoginLocation string              `json:"login_location,omitempty"`
	Status        int                 `json:"status,omitempty"`
	StatusText    string              `json:"status_text,omitempty"`
	Proto         string              `json:"proto,omitempty"`
	Header        map[string][]string `json:"header,omitempty"`
	Body          string              `json:"body,omitempty"`
}

// forge is t.RoundTrip(req) run on the host.
func (c *HostGitClient) forge(t *CommandForgeTransport, req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		b, err := io.ReadAll(io.LimitReader(req.Body, hostGitMaxBody+1))
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(b) > hostGitMaxBody {
			return nil, fmt.Errorf("a %s request body over %d MiB cannot be carried to the host's %s server", t.Command, hostGitMaxBody>>20, HostGitEntry)
		}
		body = b
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = commandForgeTimeout
	}
	ctx, cancel := context.WithTimeout(req.Context(), timeout+hostGitStartSlack)
	defer cancel()
	header := map[string]any{}
	for k, vs := range req.Header {
		list := make([]any, len(vs))
		for i, v := range vs {
			list[i] = v
		}
		header[k] = list
	}
	args := map[string]any{
		"source":     t.Source,
		"host":       t.Host,
		"api_base":   t.APIBase,
		"proxy":      t.Proxy,
		"timeout_ms": float64(timeout / time.Millisecond),
		"method":     req.Method,
		"url":        req.URL.String(),
		"header":     header,
		"body":       base64.StdEncoding.EncodeToString(body),
	}
	var res hostGitForgeResult
	if err := c.call(ctx, hostGitToolForge, args, &res); err != nil {
		return nil, err
	}
	if res.LoginLocation != "" {
		if login, ok := NewLoginRedirect(res.LoginHost, res.LoginLocation); ok {
			return nil, login
		}
		return nil, fmt.Errorf("%s answered with a login page at %s", res.LoginHost, res.LoginLocation)
	}
	data, err := base64.StdEncoding.DecodeString(res.Body)
	if err != nil {
		return nil, fmt.Errorf("the host's %s server: response body: %w", HostGitEntry, err)
	}
	major, minor, ok := http.ParseHTTPVersion(res.Proto)
	if !ok {
		res.Proto, major, minor = "HTTP/1.1", 1, 1
	}
	return &http.Response{
		Status:        res.StatusText,
		StatusCode:    res.Status,
		Proto:         res.Proto,
		ProtoMajor:    major,
		ProtoMinor:    minor,
		Header:        http.Header(res.Header),
		Body:          io.NopCloser(bytes.NewReader(data)),
		ContentLength: int64(len(data)),
		Request:       req,
	}, nil
}

// ServeHostGit adds host-git's tools to s: the server side, run on the
// host by `grain capability host-git`. Each tool answers a failure of the
// command it ran as {"error": "..."} -- the sentence the same failure is
// on a daemon on the host -- and keeps tool errors for requests it
// refuses.
func ServeHostGit(s *protocol.CapabilityServer) error {
	sourceHost := func(extra map[string]any) map[string]any {
		props := map[string]any{
			"source": map[string]any{"type": "string", "pattern": `^[a-z0-9][a-z0-9_.-]{0,31}$`},
			"host":   map[string]any{"type": "string", "pattern": `^[A-Za-z0-9.-]{1,253}(:[0-9]{1,5})?$`},
		}
		for k, v := range extra {
			props[k] = v
		}
		return map[string]any{"type": "object", "required": []any{"source", "host"}, "properties": props, "additionalProperties": false}
	}
	if err := s.HandleTool(hostGitToolToken, "Print the host account's token for host from an ambient source's own token command.",
		sourceHost(nil), func(ctx context.Context, args map[string]any) ([]byte, error) {
			src, host, err := hostGitSource(args)
			if err != nil {
				return nil, err
			}
			token, err := commandToken(src, host)
			return hostGitAnswer(map[string]any{"token": token}, err)
		}); err != nil {
		return err
	}
	if err := s.HandleTool(hostGitToolPackageToken, "Print the host account's package-registry token from an ambient source's package-credential command.",
		sourceHost(map[string]any{"registry": map[string]any{"type": "string", "pattern": `^[A-Za-z0-9.-]{0,253}$`}}),
		func(ctx context.Context, args map[string]any) ([]byte, error) {
			source, _ := args["source"].(string)
			registry, _ := args["registry"].(string)
			host := hostName(fmt.Sprint(args["host"]))
			pkg, ok := packageCredentialSourceFor(host, source)
			if !ok {
				return nil, fmt.Errorf("source %q has no package credential command for %s", source, host)
			}
			token, err := localPackageToken(pkg, host, registry)
			return hostGitAnswer(map[string]any{"token": token}, err)
		}); err != nil {
		return err
	}
	if err := s.HandleTool(hostGitToolIdentity, "Read the host account's global git user.name, user.email, and SSH signing public key.",
		map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		func(ctx context.Context, args map[string]any) ([]byte, error) {
			id := readHostGitIdentity()
			return hostGitAnswer(map[string]any{"name": id.Name, "email": id.Email, "signing_key": id.SigningKey}, nil)
		}); err != nil {
		return err
	}
	if err := s.HandleTool(hostGitToolSign, "Sign a git commit or tag payload with the host account's configured SSH signing key.",
		map[string]any{
			"type":     "object",
			"required": []any{"namespace", "payload"},
			"properties": map[string]any{
				"namespace": map[string]any{"type": "string", "pattern": `^git$`},
				"payload":   map[string]any{"type": "string", "maxLength": 1500000},
			},
			"additionalProperties": false,
		},
		func(ctx context.Context, args map[string]any) ([]byte, error) {
			ns, _ := args["namespace"].(string)
			raw, err := base64.StdEncoding.DecodeString(fmt.Sprint(args["payload"]))
			if err != nil {
				return nil, fmt.Errorf("git_sign: payload: %v", err)
			}
			sig, err := signHostGitPayload(ns, raw)
			return hostGitAnswer(map[string]any{"signature": string(sig)}, err)
		}); err != nil {
		return err
	}
	return s.HandleTool(hostGitToolForge, "Send one forge request through a ForgeViaCommand source's `<tool> api`, as the host account.",
		sourceHost(map[string]any{
			"api_base":   map[string]any{"type": "string", "maxLength": 2048},
			"proxy":      map[string]any{"type": "string", "maxLength": 2048},
			"timeout_ms": map[string]any{"type": "number", "minimum": 0, "maximum": 3600000},
			"method":     map[string]any{"type": "string", "pattern": `^[A-Z]{1,16}$`},
			"url":        map[string]any{"type": "string", "maxLength": 8192},
			"header": map[string]any{"type": "object",
				"additionalProperties": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
			"body": map[string]any{"type": "string"},
		}), hostGitServeForge)
}

// hostGitSource is a request's source and host: a registered
// command-backed source, asked only about a host it may be asked about.
func hostGitSource(args map[string]any) (AmbientCommandSource, string, error) {
	name, _ := args["source"].(string)
	host := hostName(fmt.Sprint(args["host"]))
	src, ok := ambientCommandSource(name)
	if !ok {
		return AmbientCommandSource{}, "", fmt.Errorf("%q is not an ambient source %s runs: it runs %s",
			name, HostGitEntry, strings.Join(hostGitSourceNames(), ", "))
	}
	if err := ambientSourceAllowed(host, name); err != nil {
		return AmbientCommandSource{}, "", err
	}
	return src, host, nil
}

func hostGitSourceNames() []string {
	var names []string
	for _, src := range ambientCommandSources() {
		names = append(names, src.Name)
	}
	return names
}

// hostGitServeForge is forge_request: the request rebuilt and sent
// through the source's own CommandForgeTransport, so it carries exactly
// the argv and environment a daemon on the host would give the tool.
func hostGitServeForge(ctx context.Context, args map[string]any) ([]byte, error) {
	src, host, err := hostGitSource(args)
	if err != nil {
		return nil, err
	}
	if src.ForgeViaCommand == nil {
		return nil, fmt.Errorf("source %q does not send forge requests through %s", src.Name, src.Command)
	}
	u, err := url.Parse(fmt.Sprint(args["url"]))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || hostName(u.Host) != host {
		return nil, fmt.Errorf("forge request: %q is not a URL on %s", fmt.Sprint(args["url"]), host)
	}
	apiBase, _ := args["api_base"].(string)
	if apiBase != "" {
		b, err := url.Parse(apiBase)
		if err != nil || hostName(b.Host) != host {
			return nil, fmt.Errorf("forge request: API base %q is not on %s", apiBase, host)
		}
	}
	body, err := base64.StdEncoding.DecodeString(fmt.Sprint(args["body"]))
	if err != nil {
		return nil, fmt.Errorf("forge request: body: %v", err)
	}
	proxy, _ := args["proxy"].(string)
	timeout := commandForgeTimeout
	if ms, ok := args["timeout_ms"].(float64); ok && ms > 0 {
		timeout = time.Duration(ms) * time.Millisecond
	}
	t := newCommandForgeTransport(src, CredentialHost{Host: host, Proxy: proxy}, timeout)
	t.APIBase = apiBase
	method, _ := args["method"].(string)
	var reqBody io.Reader
	if len(body) > 0 {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reqBody)
	if err != nil {
		return nil, err
	}
	if h, ok := args["header"].(map[string]any); ok {
		for k, vs := range h {
			list, _ := vs.([]any)
			for _, v := range list {
				req.Header.Add(k, fmt.Sprint(v))
			}
		}
	}
	resp, err := t.RoundTrip(req)
	if err != nil {
		if login, ok := AsLoginRedirect(err); ok {
			return json.Marshal(hostGitForgeResult{LoginHost: login.Host, LoginLocation: login.Location})
		}
		return json.Marshal(hostGitForgeResult{Error: err.Error()})
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, hostGitMaxBody+1))
	if err != nil {
		return json.Marshal(hostGitForgeResult{Error: fmt.Sprintf("reading %s's answer: %v", src.Command, err)})
	}
	if len(data) > hostGitMaxBody {
		return json.Marshal(hostGitForgeResult{Error: fmt.Sprintf("%s answered with more than %d MiB, more than the host's %s server carries",
			src.Command, hostGitMaxBody>>20, HostGitEntry)})
	}
	return json.Marshal(hostGitForgeResult{
		Status:     resp.StatusCode,
		StatusText: resp.Status,
		Proto:      resp.Proto,
		Header:     resp.Header,
		Body:       base64.StdEncoding.EncodeToString(data),
	})
}

// hostGitAnswer is a tool's result: out, or the command's failure as
// {"error": ...}.
func hostGitAnswer(out map[string]any, err error) ([]byte, error) {
	if err != nil {
		return json.Marshal(map[string]any{"error": err.Error()})
	}
	return json.Marshal(out)
}
