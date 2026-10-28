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

package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/modelexec"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/redact"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

type daemonNamespaceCIStore struct {
	fallback ci.RunStore
}

func (d daemonNamespaceCIStore) store() ci.RunStore {
	if svc := LiveCI.embedded(); svc != nil && svc.Runs != nil {
		return svc.Runs
	}
	return d.fallback
}

func (d daemonNamespaceCIStore) Put(run ci.Run) {
	if s := d.store(); s != nil {
		s.Put(run)
	}
}

func (d daemonNamespaceCIStore) Update(id string, fn func(*ci.Run)) {
	if s := d.store(); s != nil {
		s.Update(id, fn)
	}
}

func (d daemonNamespaceCIStore) Get(id string) (ci.Run, bool) {
	if s := d.store(); s != nil {
		return s.Get(id)
	}
	return ci.Run{}, false
}

func (d daemonNamespaceCIStore) List() []ci.Run {
	if s := d.store(); s != nil {
		return s.List()
	}
	return nil
}

func (d daemonNamespaceCIStore) Find(f ci.RunFilter, limit int) ci.RunPage {
	if s := d.store(); s != nil {
		return s.Find(f, limit)
	}
	return ci.RunPage{}
}

func (d daemonNamespaceCIStore) Live(key ci.Key) (ci.Run, bool) {
	if s := d.store(); s != nil {
		return s.Live(key)
	}
	return ci.Run{}, false
}

func buildNamespaceRedactor(dataDir string) *redact.Redactor {
	var known []redact.KnownValue
	if dataDir != "" {
		secretsDir := filepath.Join(dataDir, "secrets")
		if vals, err := KnownSecretValues(secretsDir); err == nil {
			known = append(known, vals...)
		}
		if set, err := gitproxy.LoadCredentialSet(filepath.Join(secretsDir, "github")); err == nil {
			for _, name := range set.Names() {
				if set.IsApp(name) {
					continue
				}
				cred, ok := set.Get(name)
				if !ok || cred.Token == nil {
					continue
				}
				known = append(known, redact.KnownValue{Label: "github credential " + name, Value: *cred.Token})
			}
		}
	}
	return redact.New(known)
}

// startGitProxy serves gitproxy.NewHandler on a random port, and returns
// the URL to point every slot's git credential helper at plus a shutdown
// func. Running it in-process rather than as a separate systemd unit (v1's
// shape, docs/design.md) is exactly bwsalmon/agents#254's "the MCP server
// just uses the local machine" simplification applied to the proxy too:
// one process, one machine, no unit to keep in sync with this one's own
// lifecycle.
//
// fabric is where the listener comes from and what decides the URL this
// hands back -- see sandboxnet.go. Under Direct that is the same two
// cases it always was: a backend sharing this process's own network
// namespace (HostSandboxes, and every existing deployment before
// bwsalmon/agents#567) binds loopback only and names that same loopback
// address, while a granule VM's guest -- in its own network namespace
// behind netshim's NAT (third_party/granule/internal/netshim), with its
// own unrelated 127.0.0.1 -- gets a URL naming the address
// -granule-git-proxy-host supplied, typically the docker bridge gateway
// its outbound NAT routes through to reach this host.
//
// Asking the fabric rather than taking an address is what keeps this
// service's answer the same as every other sandbox-facing service's:
// the four of them disagreeing was a real bug, and sandboxnet.go's own
// header says which one was wrong.
//
// forbidden is the live set of repos the proxy refuses to every sandbox
// (gitproxy.ForbiddenSet): this fills it with what this deployment's
// state repository makes of it right now, and whoever else holds it --
// stateManager, when an adopt points the installation at a different
// repository -- replaces its contents from then on, so the proxy answers
// for the repository grain's state is in now rather than the one it was
// in when the process started. A nil set is a caller with nothing that
// can change that answer, which gets one of its own.
func StartGitProxy(dataDir string, store *model.Store, githubHost string, insecureHTTP bool, fabric Fabric, forbidden *gitproxy.ForbiddenSet) (url string, handler http.Handler, stop func(context.Context) error, err error) {
	return startGitProxy(dataDir, store, githubHost, insecureHTTP, fabric, forbidden, nil)
}

func startGitProxy(dataDir string, store *model.Store, githubHost string, insecureHTTP bool, fabric Fabric, forbidden *gitproxy.ForbiddenSet, shutdownStarted <-chan struct{}) (url string, handler http.Handler, stop func(context.Context) error, err error) {
	repos, err := ForbiddenRepos(context.Background(), dataDir)
	if err != nil {
		return "", nil, nil, err
	}
	if forbidden == nil {
		forbidden = gitproxy.NewForbiddenSet()
	}
	forbidden.Set(repos)
	var fallbackCI ci.RunStore
	if store != nil {
		fallbackCI = ci.NewStoredRunStore(store, ci.StoredRunStoreOptions{})
	}
	nsServer := &namespace.Server{
		CIStore:     daemonNamespaceCIStore{fallback: fallbackCI},
		ArtifactDir: ciArtifactDir(dataDir),
		SrcDir:      sourceDir(""),
		// Rebuilt per request, as grainlog's redactor is per run
		// (capabilities.go), so a secret set after the daemon started
		// is redacted over /fs/ without waiting for a restart.
		NewRedactor: func() *redact.Redactor { return buildNamespaceRedactor(dataDir) },
	}
	// task://, memory://, deploy:// and config://, and the /cli/ issue
	// and database reads, answered by the backends of the MCP tools they
	// stand in for (sandbox_namespace.go).
	cliBackend := wireSandboxNamespace(nsServer, sandboxNamespaceConfig{
		DataDir: dataDir, Store: store, GithubHost: githubHost, InsecureHTTP: insecureHTTP, API: LiveAPI,
	})
	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir: dataDir, Store: store, ForwardHost: githubHost, ForwardTLS: !insecureHTTP,
		Forbidden: forbidden,
		Namespace: nsServer,
		CLI:       cliBackend,
		// The acting /cli/ routes (grain ci wait, grain pr open, ...),
		// through the UI/API server's own client once it is up, and the
		// credit their waits earn (sandbox_cli.go).
		CLIActions:      sandboxCLIGate{},
		WaitCredit:      waitCreditAdapter{runWaitCredits},
		API:             LiveAPI,
		ShutdownStarted: shutdownStarted,
		// External capability tools a run or CI job may call from its
		// sandbox under /cap/<server>/mcp (grain cap).
		CapServers: SandboxCapabilities,
		// The scopes that belong to a sandbox rather than to a task --
		// grain CI's job sandboxes, which have no task to be resolved
		// through (pkg/cli/ci.go's ciReadScopes and ciWriteScopes). Wired whether or not
		// this deployment runs CI: the registry is empty until a job
		// grants into it, and an empty one authorizes nothing.
		ReadScopes:   CiReadScopes,
		WriteScopes:  CiWriteScopes,
		SandboxTeams: CiSandboxTeams,
		// Single-tenant, as Run set it on the store: a team with no
		// credential of its own is the one tenant's work, and goes out on
		// this daemon's own ladder.
		SingleTenant: store != nil && store.SingleTenant,
		Local: &gitproxy.LocalRepository{
			Owner: model.ConfigRepo().Owner, Name: model.ConfigRepo().Name,
			Dir: StateRepoDir(dataDir),
			Enabled: func() bool {
				if dataDir == "" {
					return true
				}
				settings, err := staterepo.LoadSettings(dataDir)
				return err == nil && strings.TrimSpace(settings.Remote) == ""
			},
		},
	})
	if err != nil {
		return "", nil, nil, err
	}
	proxyHandler := gitproxy.NewHandler(proxy)
	endpoint, stopServer, err := fabric.Serve("git proxy", proxyHandler)
	if err != nil {
		return "", nil, nil, err
	}
	log.Printf("grain daemon: the git proxy is serving at %s (bound %s)", endpoint.URL, endpoint.Bound)
	return endpoint.URL, proxyHandler, stopServer, nil
}

func StartExecutorServer(cfg Config, store *model.Store) (*modelexec.Server, func(context.Context) error, error) {
	srv, _, stop, err := startExecutorServer(cfg, store)
	return srv, stop, err
}

// startExecutorServer is StartExecutorServer that also says where the
// listener bound: "" when -executor-addr is unset, since then nothing
// listens on its own TCP port for a worker to dial even when the server
// runs for HTTP-upgraded executors.
func startExecutorServer(cfg Config, store *model.Store) (*modelexec.Server, string, func(context.Context) error, error) {
	srv, ln, stop, err := startExecutorListener(cfg, store)
	if err != nil || ln == nil {
		return srv, "", stop, err
	}
	return srv, ln.Addr().String(), stop, nil
}

func startExecutorListener(cfg Config, store *model.Store) (*modelexec.Server, net.Listener, func(context.Context) error, error) {
	if cfg.ExecutorAddr == "" && cfg.ExecutorTokenFile == "" && store == nil {
		return nil, nil, func(context.Context) error { return nil }, nil
	}
	var token string
	if cfg.ExecutorTokenFile != "" {
		var err error
		token, err = ReadTokenFile(cfg.ExecutorTokenFile)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("reading -executor-token-file: %w", err)
		}
	} else if store == nil && cfg.ExecutorAddr != "" {
		return nil, nil, nil, errors.New("-executor-token-file (or -model-exec-token-file) is required with -executor-addr")
	}

	if token == "" && store == nil && cfg.ExecutorAddr == "" {
		return nil, nil, func(context.Context) error { return nil }, nil
	}

	var tlsCfg *tls.Config
	if cfg.ExecutorCertFile != "" || cfg.ExecutorKeyFile != "" {
		if cfg.ExecutorCertFile == "" || cfg.ExecutorKeyFile == "" {
			return nil, nil, nil, errors.New("both -executor-cert-file and -executor-key-file are required for TLS")
		}
		cert, err := tls.LoadX509KeyPair(cfg.ExecutorCertFile, cfg.ExecutorKeyFile)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("loading executor TLS keypair: %w", err)
		}
		tlsCfg = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	}

	srv, err := modelexec.NewServer(modelexec.Config{
		Token: token,
		Store: store,
		Wait:  cfg.ExecutorWait,
		Logf:  log.Printf,
		TLS:   tlsCfg,
		// The tunnel's args come from the executor, not from this daemon,
		// so the flags that decide where the GitHub credential goes are
		// held to this daemon's own config (mcpGitHubPins).
		ServeMCP: func(rwc io.ReadWriteCloser, args []string) error {
			return ServeMCPServerPinned(rwc, rwc, args, &mcpGitHubPins{
				dataDir:            cfg.DataDir,
				githubHost:         cfg.GithubHost,
				githubInsecureHTTP: cfg.GithubInsecureHTTP,
			})
		},
		GitProxy: cfg.gitProxyHandler,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	var ln net.Listener
	ctx, cancel := context.WithCancel(context.Background())
	if cfg.ExecutorAddr != "" {
		var err error
		ln, err = net.Listen("tcp", cfg.ExecutorAddr)
		if err != nil {
			cancel()
			return nil, nil, nil, fmt.Errorf("listening on -executor-addr %s: %w", cfg.ExecutorAddr, err)
		}
		log.Printf("grain daemon: the remote executor listener is serving at %s", ln.Addr().String())

		go func() {
			if err := srv.Serve(ctx, ln); err != nil && ctx.Err() == nil {
				log.Printf("grain daemon: executor server error: %v", err)
			}
		}()
	}

	stop := func(shutdownCtx context.Context) error {
		cancel()
		if ln != nil {
			return ln.Close()
		}
		return nil
	}
	return srv, ln, stop, nil
}
