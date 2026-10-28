package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
)

// forgeRouter is the github.Transport under the daemon's one REST client:
// the -github-host transport for a repository on that host, and the
// repository's own host's API (gitproxy.CredentialSet.ForgeTransportOn)
// for any other.
//
// github.Client names a repository by owner and name, and the REST paths
// it builds carry nothing else, so before this every call -- the branch
// read a finishing run makes, the pull request it opens, the checks the
// reconcile loop polls, the merge -- went to -github-host whatever host
// the repository was on. For ghe.example.com/experimental/x that was
// api.github.com/repos/experimental/x: a 404, read as "never pushed".
//
// The host comes from one of two places. A caller holding a model.RepoRef
// passes its StoredOwner, "host/owner", which lands in the path as
// /repos/host/owner/name; the router takes the host back out. A caller
// passing the bare owner is resolved through hostOf, the repositories
// this deployment works in (storeRepoHosts): owner/name is routed to
// another host only when that is the one host this deployment knows it
// on, so a github.com repository is never sent elsewhere on a guess.
type forgeRouter struct {
	// def is the -github-host transport and defHost the git host it
	// reaches (restClientHost).
	def     github.Transport
	defHost string
	// transportFor builds the transport for owner/repo on host. The
	// daemon's is credentialTokenSource.forgeTransport.
	transportFor func(host, owner, repo string) (github.Transport, error)
	// hostOf is the host a bare owner/repo is on, or "" for defHost.
	hostOf func(owner, repo string) string
	// viaCommand, when set, is whether owner/repo on host is pushed with
	// a credential only its host's tool carries (ForgeViaCommand): a repository on
	// defHost that is goes through transportFor rather than def.
	viaCommand func(host, owner, repo string) bool

	mu    sync.Mutex
	cache map[string]github.Transport
	via   map[string]viaAnswer
}

// viaAnswer is one cached viaCommand answer.
type viaAnswer struct {
	yes bool
	at  time.Time
}

// viaCommandTTL is how long a viaCommand answer is trusted: the same
// half-minute repoTeamCredential trusts the team's credential for.
const viaCommandTTL = 30 * time.Second

// defViaCommand is viaCommand for owner/repo on defHost, cached.
func (r *forgeRouter) defViaCommand(owner, repo string) bool {
	if r.viaCommand == nil {
		return false
	}
	key := strings.ToLower(owner + "/" + repo)
	now := time.Now()
	r.mu.Lock()
	if a, ok := r.via[key]; ok && now.Sub(a.at) < viaCommandTTL {
		r.mu.Unlock()
		return a.yes
	}
	r.mu.Unlock()
	yes := r.viaCommand(r.defHost, owner, repo)
	r.mu.Lock()
	if r.via == nil {
		r.via = map[string]viaAnswer{}
	}
	r.via[key] = viaAnswer{yes: yes, at: now}
	r.mu.Unlock()
	return yes
}

// Request is github.Transport.
func (r *forgeRouter) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	host, owner, repo, rest, newBody, ok := r.route(path, body)
	if !ok {
		return r.def.Request(method, path, headers, body)
	}
	onDefault := host == "" || model.SameGitHost(host, r.defHost, model.DefaultGitHost)
	if onDefault && !r.defViaCommand(owner, repo) {
		return r.def.Request(method, rest, headers, newBody)
	}
	if onDefault {
		// -github-host is itself a host only its own tool reaches: the
		// plain transport for it would be redirected to a login page,
		// so it is asked through the tool as any other such host is.
		host = r.defHost
	}
	transport, err := r.transport(host, owner, repo, path == "/graphql")
	if err != nil {
		return github.ApiResponse{}, fmt.Errorf("reaching the forge API for %s/%s/%s: %w", host, owner, repo, err)
	}
	return transport.Request(method, rest, headers, newBody)
}

// route reads the repository a request is about out of its path (a
// /repos/{owner}/{repo} call) or its body (a GraphQL query's owner and
// repo variables): the host it is on, and the path and body with that
// host taken out of them. ok is false for a request about no one
// repository (/user, /search/...), which goes to def unchanged.
func (r *forgeRouter) route(path string, body []byte) (host, owner, repo, newPath string, newBody []byte, ok bool) {
	if path == "/graphql" {
		var doc map[string]json.RawMessage
		if json.Unmarshal(body, &doc) != nil {
			return "", "", "", "", nil, false
		}
		var vars map[string]any
		if json.Unmarshal(doc["variables"], &vars) != nil {
			return "", "", "", "", nil, false
		}
		o, _ := vars["owner"].(string)
		n, _ := vars["repo"].(string)
		if o == "" || n == "" {
			return "", "", "", "", nil, false
		}
		host, owner = r.split(o, n)
		if owner != o {
			vars["owner"] = owner
			raw, err := json.Marshal(vars)
			if err != nil {
				return "", "", "", "", nil, false
			}
			doc["variables"] = raw
			if body, err = json.Marshal(doc); err != nil {
				return "", "", "", "", nil, false
			}
		}
		return host, owner, n, path, body, true
	}
	tail, found := strings.CutPrefix(path, "/repos/")
	if !found {
		return "", "", "", "", nil, false
	}
	query := ""
	if i := strings.IndexByte(tail, '?'); i >= 0 {
		tail, query = tail[:i], tail[i:]
	}
	segs := strings.SplitN(tail, "/", 4)
	if len(segs) >= 3 && looksLikeHost(segs[0]) {
		rest := ""
		if len(segs) == 4 {
			rest = "/" + segs[3]
		}
		return segs[0], segs[1], segs[2], "/repos/" + segs[1] + "/" + segs[2] + rest + query, body, true
	}
	if len(segs) < 2 {
		return "", "", "", "", nil, false
	}
	return r.lookup(segs[0], segs[1]), segs[0], segs[1], path, body, true
}

// split is owner as a host and a bare owner: "host/owner" taken apart,
// and a bare one looked up.
func (r *forgeRouter) split(owner, repo string) (host, bare string) {
	if h, o, ok := strings.Cut(owner, "/"); ok {
		return h, o
	}
	return r.lookup(owner, repo), owner
}

func (r *forgeRouter) lookup(owner, repo string) string {
	if r.hostOf == nil {
		return ""
	}
	return r.hostOf(owner, repo)
}

// looksLikeHost is whether a path segment is a git host rather than an
// owner: a GitHub owner can hold neither "." nor ":", and a host name
// (with or without a port) always holds one -- the rule model.ParseRepo
// and the UI's repoHostOf read a host-qualified repository by.
func looksLikeHost(seg string) bool {
	return strings.ContainsAny(seg, ".:")
}

// transport is the cached transport for owner/repo on host. The
// credential owner/repo is pushed with decides which login a command-backed host is
// asked through, so the cache is keyed by what transportFor was given.
func (r *forgeRouter) transport(host, owner, repo string, graphql bool) (github.Transport, error) {
	key := strings.ToLower(host + "/" + owner + "/" + repo)
	if graphql {
		key += "#graphql"
	}
	r.mu.Lock()
	if t, ok := r.cache[key]; ok {
		r.mu.Unlock()
		return t, nil
	}
	r.mu.Unlock()
	t, err := r.transportFor(host, owner, repo)
	if err != nil {
		return nil, err
	}
	if graphql {
		t = graphqlTransport(t)
	}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]github.Transport{}
	}
	r.cache[key] = t
	r.mu.Unlock()
	return t, nil
}

// graphqlTransport is t aimed at its host's GraphQL endpoint: a GitHub
// Enterprise Server serves REST under /api/v3 and GraphQL at
// /api/graphql, so the /graphql path a RESTClient posts to goes under
// /api rather than /api/v3.
func graphqlTransport(t github.Transport) github.Transport {
	rt, ok := t.(*github.RealTransport)
	if !ok || !strings.HasSuffix(rt.PathPrefix, "/v3") {
		return t
	}
	return &github.RealTransport{Host: rt.Host, UseTLS: rt.UseTLS, Client: rt.Client,
		PathPrefix: strings.TrimSuffix(rt.PathPrefix, "/v3")}
}

// forgeTransport is forgeRouter.transportFor: the transport for
// owner/repo on host, through the credential owner/repo is cloned and
// pushed with -- the team's, or the ladder's -- so a command-backed host is asked
// through the same login its pushes went through.
func (c credentialTokenSource) forgeTransport(host, owner, repo string) (github.Transport, error) {
	if c.credentials == nil {
		return nil, fmt.Errorf("no credential store")
	}
	stored := model.RepoRef{Host: host, Owner: owner, Name: repo}.StoredOwner()
	name := ""
	if cred, ok := c.selectGitCredential(stored, repo); ok {
		name = cred.Name
	}
	return c.credentials.ForgeTransportOn(name, host)
}

// forgeViaCommand is forgeRouter.viaCommand: whether owner/repo on host
// is pushed with a credential whose forge traffic goes through its host's
// own tool (gitproxy.CredentialSet.ForgeViaCommand).
func (c credentialTokenSource) forgeViaCommand(host, owner, repo string) bool {
	if c.credentials == nil {
		return false
	}
	stored := model.RepoRef{Host: host, Owner: owner, Name: repo}.StoredOwner()
	cred, ok := c.selectGitCredential(stored, repo)
	return ok && c.credentials.ForgeViaCommand(cred.Name)
}

// storeRepoHosts is forgeRouter.hostOf over store: the host owner/repo is
// on when every repository by that owner and name this deployment works
// in (model.Store.WorkRepos: task targets, write targets and repo
// configs) is on that one other host, and "" otherwise -- a repository it
// knows on the default host, on two hosts, or not at all stays on the
// default host. Cached for a short while: the reconcile loop asks once
// per call, and a repository added a moment ago is not worth a read each.
func storeRepoHosts(store *model.Store) func(owner, repo string) string {
	if store == nil {
		return nil
	}
	const ttl = 30 * time.Second
	var (
		mu    sync.Mutex
		at    time.Time
		hosts map[string][]string
	)
	return func(owner, repo string) string {
		mu.Lock()
		defer mu.Unlock()
		if hosts == nil || time.Since(at) > ttl {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			repos, err := store.WorkRepos(ctx, 0)
			cancel()
			if err != nil {
				log.Printf("grain daemon: reading which hosts this deployment's repositories are on: %v", err)
				if hosts == nil {
					return ""
				}
			} else {
				hosts = map[string][]string{}
				for _, ref := range repos {
					ref = model.RepoFromStored(ref.StoredOwner(), ref.Name)
					key := strings.ToLower(ref.Owner + "/" + ref.Name)
					h := strings.ToLower(ref.OffDefaultHost())
					if !containsString(hosts[key], h) {
						hosts[key] = append(hosts[key], h)
					}
				}
				at = time.Now()
			}
		}
		if on := hosts[strings.ToLower(owner+"/"+repo)]; len(on) == 1 {
			return on[0]
		}
		return ""
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

var _ github.Transport = (*forgeRouter)(nil)

// newForgeClient is the daemon's REST client and the -github-host
// transport underneath it: the one every server-side forge call goes out
// over, routed per repository host by forgeRouter, and authenticated by
// credentialTokenSource with the same host resolution, so the token a
// call carries is the one for the host it is sent to.
func newForgeClient(cfg Config, store *model.Store, credentials *gitproxy.CredentialSet) (*github.RESTClient, *github.RealTransport, credentialTokenSource) {
	def := github.NewRealTransport(cfg.GithubHost)
	def.UseTLS = !cfg.GithubInsecureHTTP
	client, tokens := newForgeClientOver(def, restClientHost(cfg.GithubHost), store, credentials)
	return client, def, tokens
}

// newForgeClientOver is newForgeClient over def, the transport for
// defHost: the seam a test puts a transport that must never be reached
// for another host's repository in.
func newForgeClientOver(def github.Transport, defHost string, store *model.Store, credentials *gitproxy.CredentialSet) (*github.RESTClient, credentialTokenSource) {
	hostOf := storeRepoHosts(store)
	tokens := credentialTokenSource{credentials: credentials, teamCredential: repoTeamCredential(store),
		hostOf: hostOf}
	router := &forgeRouter{def: def, defHost: defHost, transportFor: tokens.forgeTransport, hostOf: hostOf,
		viaCommand: tokens.forgeViaCommand}
	return github.NewClient(router, tokens), tokens
}
