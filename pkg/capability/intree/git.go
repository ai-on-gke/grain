package intree

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/protocol"
	"github.com/bwsalmon/grain/pkg/gitproxy"
)

// GitTokenEnv is the environment variable the git server reads its root
// credential from: the token every proxied request is authenticated to the
// upstream git host with, bound in grain/config through $secret:<name>.
const GitTokenEnv = "GIT_REPO_TOKEN"

// DefaultGitPort is the git server's data-plane port (§6.3).
const DefaultGitPort = 9728

// Argument patterns for mint_git-repo. Every one is anchored, and none
// admits a quote, a space, a backslash or a leading dash, so each value
// is safe in a setup script and as a git argument.
var (
	gitRepoPattern = `^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`
	gitRefPattern  = `^[A-Za-z0-9][A-Za-z0-9_./-]{0,199}$`
	gitRefRE       = regexp.MustCompile(gitRefPattern)
)

// DefaultPushBranches is the branches a write grant may push unless
// -push-branch says otherwise: Grain's own task branches.
const DefaultPushBranches = `^grain/[A-Za-z0-9][A-Za-z0-9_./-]{0,190}$`

// gitRepoSchema is with: {repo}'s schema for the operator's -repo list:
// an enum when every entry is exact, and otherwise an anchored pattern of
// the exact entries and owner/* wildcards.
func gitRepoSchema(repos []string) (map[string]any, error) {
	if len(repos) == 0 {
		return nil, fmt.Errorf("git: at least one -repo is required")
	}
	repoRE := regexp.MustCompile(gitRepoPattern)
	ownerRE := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
	var alts []string
	enum := make([]any, 0, len(repos))
	wild := false
	for _, r := range repos {
		if owner, ok := strings.CutSuffix(r, "/*"); ok {
			if !ownerRE.MatchString(owner) {
				return nil, fmt.Errorf("git: -repo %q is not owner/*", r)
			}
			wild = true
			alts = append(alts, regexp.QuoteMeta(owner)+`/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}`)
			continue
		}
		if !repoRE.MatchString(r) {
			return nil, fmt.Errorf("git: -repo %q is not owner/repo", r)
		}
		alts = append(alts, regexp.QuoteMeta(r))
		enum = append(enum, r)
	}
	if !wild {
		return map[string]any{"type": "string", "enum": enum}, nil
	}
	return map[string]any{"type": "string", "pattern": "^(?:" + strings.Join(alts, "|") + ")$"}, nil
}

// validRefName is gitRefPattern plus the parts of git check-ref-format a
// pattern cannot say.
func validRefName(s string) bool {
	if !gitRefRE.MatchString(s) {
		return false
	}
	if strings.Contains(s, "..") || strings.Contains(s, "//") || strings.Contains(s, "/.") ||
		strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") || strings.HasSuffix(s, ".lock") {
		return false
	}
	return true
}

// GitConfig is the git server's configuration.
type GitConfig struct {
	// Listen is the data-plane listener's loopback address.
	Listen string
	// SandboxPort is the port the listener is forwarded to inside the
	// sandbox; zero is the listener's own port.
	SandboxPort int
	// Upstream is the git host's origin, e.g. https://github.com.
	Upstream string
	// Token is the upstream credential, from GitTokenEnv.
	Token string
	// Repos is the repositories with: {repo} may name, each owner/repo or
	// owner/* for every repository of owner. Required: the server's token
	// may reach far more than any run should be granted.
	Repos []string
	// PushBranches is the anchored pattern a write grant's branch must
	// match; empty is DefaultPushBranches. It keeps a grant's own with:
	// arguments, which are untrusted, from naming a default or release
	// branch.
	PushBranches string
	// Transport is the upstream round tripper; nil is
	// http.DefaultTransport.
	Transport http.RoundTripper
	Now       func() time.Time
	Log       *log.Logger
}

type gitGrant struct {
	attempt string
	owner   string
	repo    string
	write   bool
	branch  string
}

// gitConfigSchema is the git server's configure schema (§4.5): what
// -upstream, -repo, -push-branch and GIT_REPO_TOKEN say, as fields
// Settings draws a form for. Each one given overrides its flag or
// environment variable, which stay the fallback for a server configured
// before configure existed. -listen and -sandbox-port stay flags only:
// the listener is bound when the server starts.
var gitConfigSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"upstream": map[string]any{
			"type":        "string",
			"title":       "Upstream",
			"description": "Origin of the git host every proxied request goes to (https://github.com).",
			"minLength":   1,
		},
		"repos": map[string]any{
			"type":        "array",
			"title":       "Repositories",
			"description": "The repositories a grant's with: {repo} may name: owner/repo, or owner/* for every repository of owner.",
			"minItems":    1,
			"items":       map[string]any{"type": "string", "pattern": `^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/(?:\*|[A-Za-z0-9][A-Za-z0-9_.-]{0,99})$`},
		},
		"pushBranch": map[string]any{
			"type":        "string",
			"title":       "Push branches",
			"description": "Anchored pattern every branch a write grant pushes must match.",
			"pattern":     `^\^.*\$$`,
		},
		"token": map[string]any{
			"type":        "string",
			"title":       "Upstream token",
			"description": "The credential every proxied request is authenticated to the git host with.",
			"writeOnly":   true,
		},
	},
}

// withGitConfig is base with every field config sets put over it.
func withGitConfig(base GitConfig, config map[string]any) GitConfig {
	cfg := base
	if v, ok := config["upstream"].(string); ok {
		cfg.Upstream = v
	}
	if v, ok := config["pushBranch"].(string); ok {
		cfg.PushBranches = v
	}
	if v, ok := config["token"].(string); ok {
		cfg.Token = v
	}
	if list, ok := config["repos"].([]any); ok {
		cfg.Repos = nil
		for _, r := range list {
			if r, ok := r.(string); ok {
				cfg.Repos = append(cfg.Repos, r)
			}
		}
	}
	return cfg
}

// gitSettings is what cfg's upstream, repos and push pattern say, or why
// they say nothing a git-repo method can be built on.
type gitSettings struct {
	upstream     string
	repoSchema   map[string]any
	pushBranches *regexp.Regexp
}

func checkGit(cfg GitConfig) (gitSettings, error) {
	if cfg.Token == "" {
		return gitSettings{}, fmt.Errorf("git: %s is not set; bind it to the upstream credential's secret in grain/config", GitTokenEnv)
	}
	if cfg.Upstream == "" {
		cfg.Upstream = "https://github.com"
	}
	up, err := url.Parse(cfg.Upstream)
	if err != nil || (up.Scheme != "https" && up.Scheme != "http") || up.Host == "" || (up.Path != "" && up.Path != "/") {
		return gitSettings{}, fmt.Errorf("git: -upstream %q is not an http(s) origin", cfg.Upstream)
	}
	repoSchema, err := gitRepoSchema(cfg.Repos)
	if err != nil {
		return gitSettings{}, err
	}
	if cfg.PushBranches == "" {
		cfg.PushBranches = DefaultPushBranches
	}
	if !strings.HasPrefix(cfg.PushBranches, "^") || !strings.HasSuffix(cfg.PushBranches, "$") {
		return gitSettings{}, fmt.Errorf("git: -push-branch %q is not an anchored pattern", cfg.PushBranches)
	}
	pushBranches, err := regexp.Compile(cfg.PushBranches)
	if err != nil {
		return gitSettings{}, fmt.Errorf("git: -push-branch: %v", err)
	}
	return gitSettings{upstream: strings.TrimSuffix(cfg.Upstream, "/"), repoSchema: repoSchema, pushBranches: pushBranches}, nil
}

// NewGit builds the git server: method git-repo (§6.3), configured by
// cfg, which must say everything the method needs.
func NewGit(cfg GitConfig) (*Server, error) {
	if _, err := checkGit(cfg); err != nil {
		return nil, err
	}
	return newConfigurableGit(cfg)
}

// newConfigurableGit is the git server `grain capability git` serves: its
// proxy listening on fallback's -listen from the start, and git-repo
// configured by its flags and environment when they say enough, and by
// configure (gitConfigSchema) over them either way. Until one of the two
// configures it, it advertises configure and no method, and its attach
// fails with whatever the fallback lacks.
func newConfigurableGit(fallback GitConfig) (*Server, error) {
	p := &gitProxy{transport: fallback.Transport, grants: map[string]*gitGrant{}}
	if p.transport == nil {
		p.transport = http.DefaultTransport
	}
	s, err := newServer("git", fallback.Log)
	if err != nil {
		return nil, err
	}
	listen := fallback.Listen
	if listen == "" {
		listen = "127.0.0.1:" + strconv.Itoa(DefaultGitPort)
	}
	addr, err := s.listen(listen, p)
	if err != nil {
		return nil, fmt.Errorf("git: %w", err)
	}
	p.hostPort = addr.Port
	if fallback.SandboxPort == 0 {
		fallback.SandboxPort = addr.Port
	}
	if m, err := newGitMethod(fallback, p); err == nil {
		if err := s.SetMethods(m); err != nil {
			s.Close(context.Background())
			return nil, err
		}
		s.Methods = []*protocol.Method{m}
		s.Log.Printf("git: proxying %s on %s", p.origin(), addr)
	}
	err = s.configurable(gitConfigSchema, func(_ context.Context, config map[string]any) ([]*protocol.Method, error) {
		m, err := newGitMethod(withGitConfig(fallback, config), p)
		if err != nil {
			return nil, err
		}
		return []*protocol.Method{m}, nil
	})
	if err != nil {
		s.Close(context.Background())
		return nil, err
	}
	return s, nil
}

// newGitMethod is the git-repo method cfg configures, minting grants on
// p, which forwards upstream as cfg says from then on.
func newGitMethod(cfg GitConfig, p *gitProxy) (*protocol.Method, error) {
	set, err := checkGit(cfg)
	if err != nil {
		return nil, err
	}
	pushPattern := set.pushBranches.String()
	method, err := protocol.NewMethod(protocol.MethodSpec{
		Name:        "git-repo",
		Description: "Clone one repository into the sandbox through this server's git proxy, with a per-attempt token that can read it and, in write mode, push its one branch.",
		Args: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"repo"},
			"properties": map[string]any{
				"repo":   set.repoSchema,
				"mode":   map[string]any{"type": "string", "enum": []any{"read", "write"}, "default": "read"},
				"base":   map[string]any{"type": "string", "pattern": gitRefPattern},
				"branch": map[string]any{"type": "string", "pattern": gitRefPattern},
				"root":   map[string]any{"type": "boolean", "default": true},
			},
		},
		Now: cfg.Now,
		Mint: func(_ context.Context, attemptID string, _ time.Time, args map[string]any) (*protocol.MintResponse, error) {
			if !pathSafe(attemptID) {
				return refusedResponse("attempt_id " + attemptID + " cannot name a directory"), nil
			}
			repoArg, _ := args["repo"].(string)
			owner, repo, _ := strings.Cut(repoArg, "/")
			mode, _ := args["mode"].(string)
			base, _ := args["base"].(string)
			branch, _ := args["branch"].(string)
			root, _ := args["root"].(bool)
			for name, ref := range map[string]string{"base": base, "branch": branch} {
				if ref != "" && !validRefName(ref) {
					return refusedResponse(name + " " + ref + " is not a valid branch name"), nil
				}
			}
			if mode == "write" {
				switch {
				case branch == "":
					return refusedResponse("mode write needs the branch it may push"), nil
				case !set.pushBranches.MatchString(branch):
					return refusedResponse(fmt.Sprintf("branch %s is not one this server lets a grant push; it pushes only branches matching %s", branch, pushPattern)), nil
				case branch == base:
					return refusedResponse("branch " + branch + " is the grant's own base; a write grant pushes a branch of its own"), nil
				}
			}
			token, err := randomToken()
			if err != nil {
				return nil, err
			}
			g := &gitGrant{attempt: attemptID, owner: owner, repo: repo, write: mode == "write", branch: branch}
			p.authorize(token, g)
			return gitMint(attemptID, g, base, root, p.hostPort, cfg.SandboxPort, token), nil
		},
		Release: func(_ context.Context, attemptID string) error {
			p.revoke(attemptID)
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	p.setUpstream(set.upstream, cfg.Token)
	return method, nil
}

// gitMint is mint_git-repo's response for grant g: the port, the
// per-grant credentials file and clone script, and setup_root or setup.
func gitMint(attemptID string, g *gitGrant, base string, root bool, hostPort, sandboxPort int, token string) *protocol.MintResponse {
	dir := "/home/debian/.git-repo/" + attemptID
	creds := dir + "/credentials"
	script := dir + "/setup.sh"
	remote := fmt.Sprintf("http://127.0.0.1:%d/%s/%s.git", sandboxPort, g.owner, g.repo)
	target := "."
	where := "the root working directory"
	if !root {
		target = "/home/debian/repos/" + g.owner + "/" + g.repo
		where = target
	}
	var sh strings.Builder
	sh.WriteString("#!/bin/sh\nset -eu\n")
	fmt.Fprintf(&sh, "mkdir -p %s\n", shellQuote(target))
	fmt.Fprintf(&sh, "git clone -c credential.helper=%s %s %s\n",
		shellQuote("store --file="+creds), shellQuote(remote), shellQuote(target))
	fmt.Fprintf(&sh, "cd %s\n", shellQuote(target))
	switch {
	case g.branch != "":
		from := "origin/HEAD"
		if base != "" {
			from = "origin/" + base
		}
		fmt.Fprintf(&sh, "if git rev-parse -q --verify %s >/dev/null; then\n  git checkout -B %s %s\nelse\n  git checkout -B %s %s\nfi\n",
			shellQuote("refs/remotes/origin/"+g.branch), shellQuote(g.branch), shellQuote("origin/"+g.branch),
			shellQuote(g.branch), shellQuote(from))
	case base != "":
		fmt.Fprintf(&sh, "git checkout %s\n", shellQuote(base))
	}

	mode := "read it"
	if g.write {
		mode = "read it and push branch " + g.branch + " (and no other ref)"
	}
	resp := &protocol.MintResponse{
		Status: protocol.StatusOK,
		Ports:  []protocol.Port{{HostPort: hostPort, SandboxPort: sandboxPort}},
		Placements: []protocol.Placement{
			{Path: creds, Mode: "0600", Content: fmt.Sprintf("http://x-access-token:%s@127.0.0.1:%d\n", token, sandboxPort)},
			{Path: script, Mode: "0700", Content: sh.String()},
		},
		PromptSection: fmt.Sprintf("%s/%s is checked out in %s, through grain's git proxy at %s. This attempt may %s; its credential is in %s and stops working when the attempt ends.",
			g.owner, g.repo, where, remote, mode, creds),
	}
	if root {
		resp.SetupRoot = script
	} else {
		resp.Setup = script
	}
	return resp
}

// gitProxy is the git server's data plane: git smart HTTP for the
// repositories its live grants name, authenticated per attempt (§5.3.4)
// and forwarded upstream with the server's own credential.
type gitProxy struct {
	transport http.RoundTripper
	hostPort  int

	mu sync.Mutex
	// upstream and token are the git host and its credential, as the
	// configuration last applied says.
	upstream string
	token    string
	grants   map[string]*gitGrant // by token
}

func (p *gitProxy) setUpstream(upstream, token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.upstream, p.token = upstream, token
}

// origin is the git host requests are forwarded to.
func (p *gitProxy) origin() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.upstream
}

func (p *gitProxy) credential() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.token
}

func (p *gitProxy) authorize(token string, g *gitGrant) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.grants[token] = g
}

func (p *gitProxy) revoke(attemptID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for t, g := range p.grants {
		if g.attempt == attemptID {
			delete(p.grants, t)
		}
	}
}

func (p *gitProxy) grant(r *http.Request) *gitGrant {
	_, pass, ok := r.BasicAuth()
	if !ok || pass == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for t, g := range p.grants {
		if subtle.ConstantTimeCompare([]byte(t), []byte(pass)) == 1 {
			return g
		}
	}
	return nil
}

// maxPushCommands bounds the command list read before a push is forwarded.
const maxPushCommands = 1 << 20

func (p *gitProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g := p.grant(r)
	if g == nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="grain git-repo"`)
		http.Error(w, "this attempt's git-repo credential is missing, wrong, or already released", http.StatusUnauthorized)
		return
	}
	req, ok := gitproxy.ParsePath(r.URL.Path)
	if !ok || req.Host != "" || !strings.EqualFold(req.Owner, g.owner) || !strings.EqualFold(req.Repo, g.repo) {
		http.Error(w, fmt.Sprintf("this attempt may reach %s/%s only", g.owner, g.repo), http.StatusForbidden)
		return
	}
	query := ""
	push := false
	switch {
	case req.Action == "info/refs" && r.Method == http.MethodGet:
		service := r.URL.Query().Get("service")
		if service != "git-upload-pack" && service != "git-receive-pack" {
			http.Error(w, "only smart HTTP is served", http.StatusForbidden)
			return
		}
		push = service == "git-receive-pack"
		query = "service=" + service
	case req.Action == "git-upload-pack" && r.Method == http.MethodPost:
	case req.Action == "git-receive-pack" && r.Method == http.MethodPost:
		push = true
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if push && !g.write {
		http.Error(w, fmt.Sprintf("this attempt may read %s/%s but not push to it", g.owner, g.repo), http.StatusForbidden)
		return
	}
	var body io.Reader = r.Body
	if req.Action == "git-receive-pack" {
		if r.Header.Get("Content-Encoding") != "" {
			http.Error(w, "a compressed push is not accepted", http.StatusUnsupportedMediaType)
			return
		}
		br := bufio.NewReader(r.Body)
		cmds, err := readCommandList(br)
		if err != nil {
			http.Error(w, "reading the push: "+err.Error(), http.StatusBadRequest)
			return
		}
		updates, err := gitproxy.ParseReceivePackCommands(cmds)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, u := range updates {
			if u.Ref != "refs/heads/"+g.branch {
				http.Error(w, fmt.Sprintf("this attempt may push refs/heads/%s only, not %s", g.branch, u.Ref), http.StatusForbidden)
				return
			}
			if strings.Trim(u.New, "0") == "" {
				http.Error(w, fmt.Sprintf("this attempt may not delete %s", u.Ref), http.StatusForbidden)
				return
			}
		}
		spool, err := spoolPush(cmds, br)
		if err != nil {
			http.Error(w, "reading the push: "+err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		defer spool.Close()
		for _, u := range updates {
			if strings.Trim(u.Old, "0") == "" {
				continue
			}
			ok, err := spool.fastForward(u.New, u.Old)
			if err != nil {
				http.Error(w, "reading the push's pack: "+err.Error(), http.StatusBadRequest)
				return
			}
			if !ok {
				http.Error(w, fmt.Sprintf("this attempt may only fast-forward %s, and this push does not show %s descending from %s; fetch and merge or rebase instead of forcing it", u.Ref, u.New, u.Old), http.StatusForbidden)
				return
			}
		}
		body = spool.reader()
	}
	p.forward(w, r, fmt.Sprintf("%s/%s/%s.git/%s", p.origin(), g.owner, g.repo, req.Action), query, body)
}

// maxPushBytes bounds a push the proxy spools to check before forwarding.
const maxPushBytes = 2 << 30

// pushSpool is a push request body spooled to a temporary file, so its
// pack can be read to check fast-forwards and then forwarded whole.
type pushSpool struct{ f *os.File }

func spoolPush(cmds []byte, rest io.Reader) (*pushSpool, error) {
	f, err := os.CreateTemp("", "git-repo-push-*")
	if err != nil {
		return nil, err
	}
	os.Remove(f.Name())
	s := &pushSpool{f: f}
	n, err := io.Copy(f, io.LimitReader(io.MultiReader(bytes.NewReader(cmds), rest), maxPushBytes+1))
	if err == nil && n > maxPushBytes {
		err = fmt.Errorf("push is over %d bytes", int64(maxPushBytes))
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *pushSpool) fastForward(newTip, oldTip string) (bool, error) {
	return gitproxy.PushShowsFastForward(io.NewSectionReader(s.f, 0, 1<<62), newTip, oldTip)
}

func (s *pushSpool) reader() io.Reader { return io.NewSectionReader(s.f, 0, 1<<62) }

func (s *pushSpool) Close() error { return s.f.Close() }

// readCommandList reads a receive-pack request's pkt-lines up to and
// including the flush-pkt that ends its command list.
func readCommandList(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		head := make([]byte, 4)
		if _, err := io.ReadFull(r, head); err != nil {
			return nil, err
		}
		out = append(out, head...)
		n, err := strconv.ParseUint(string(head), 16, 16)
		if err != nil {
			return nil, fmt.Errorf("malformed pkt-line length %q", head)
		}
		if n == 0 {
			return out, nil
		}
		if n < 4 || len(out)+int(n) > maxPushCommands {
			return nil, fmt.Errorf("malformed or oversized command list")
		}
		line := make([]byte, n-4)
		if _, err := io.ReadFull(r, line); err != nil {
			return nil, err
		}
		out = append(out, line...)
	}
}

var forwardedHeaders = []string{"Content-Type", "Accept", "Accept-Encoding", "Content-Encoding", "Git-Protocol", "User-Agent"}

func (p *gitProxy) forward(w http.ResponseWriter, r *http.Request, target, query string, body io.Reader) {
	if query != "" {
		target += "?" + query
	}
	out, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
	if err != nil {
		http.Error(w, "building the upstream request", http.StatusInternalServerError)
		return
	}
	for _, h := range forwardedHeaders {
		if v := r.Header.Get(h); v != "" {
			out.Header.Set(h, v)
		}
	}
	out.SetBasicAuth("x-access-token", p.credential())
	resp, err := p.transport.RoundTrip(out)
	if err != nil {
		http.Error(w, "the git host could not be reached", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Content-Encoding", "Cache-Control", "Expires", "Pragma"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// The upstream credential is the server's, not the sandbox's:
		// never ask the sandbox's git for a different one.
		http.Error(w, "the git host refused this server's credential", http.StatusBadGateway)
		return
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func init() {
	register(Builder{
		Name:    "git",
		Summary: "repositories cloned through a per-attempt git proxy token (mint_git-repo)",
		Build: func(ctx context.Context, args []string, env Env, logger *log.Logger) (*Server, error) {
			fs := newFlags("git", logOutput(logger))
			listen := fs.String("listen", "127.0.0.1:"+strconv.Itoa(DefaultGitPort), "loopback address of the git proxy data-plane listener")
			sandboxPort := fs.Int("sandbox-port", 0, "port the proxy is forwarded to inside the sandbox (default: the listener's own)")
			upstream := fs.String("upstream", "https://github.com", "origin of the git host")
			pushBranches := fs.String("push-branch", DefaultPushBranches, "anchored pattern every branch a write grant pushes must match")
			var repos listFlag
			fs.Var(&repos, "repo", "an owner/repo, or owner/* for all of owner's, that with: {repo} may name; repeatable, at least one")
			if err := fs.Parse(args); err != nil {
				return nil, err
			}
			return newConfigurableGit(GitConfig{Listen: listenAddr(env, *listen), SandboxPort: *sandboxPort, Upstream: *upstream, Token: env(GitTokenEnv), Repos: repos, PushBranches: *pushBranches, Log: logger})
		},
	})
}
