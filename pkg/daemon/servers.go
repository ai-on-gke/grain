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
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
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

type daemonNamespaceArtifacts struct{}

func (daemonNamespaceArtifacts) active() ci.ArtifactStore {
	if svc := LiveCI.embedded(); svc != nil && svc.Engine != nil {
		return svc.Engine.Artifacts
	}
	return nil
}

func (a daemonNamespaceArtifacts) Save(ctx context.Context, ref ci.ArtifactRef, body []byte) (ci.Artifact, error) {
	if s := a.active(); s != nil {
		return s.Save(ctx, ref, body)
	}
	return ci.Artifact{}, os.ErrNotExist
}

func (a daemonNamespaceArtifacts) List(ctx context.Context, runID string) ([]ci.Artifact, error) {
	if s := a.active(); s != nil {
		return s.List(ctx, runID)
	}
	return nil, nil
}

func (a daemonNamespaceArtifacts) Open(ctx context.Context, ref ci.ArtifactRef) (io.ReadCloser, ci.Artifact, error) {
	if s := a.active(); s != nil {
		return s.Open(ctx, ref)
	}
	return nil, ci.Artifact{}, os.ErrNotExist
}

func (a daemonNamespaceArtifacts) MaxArtifactBytes() int64 {
	if s := a.active(); s != nil {
		return s.MaxArtifactBytes()
	}
	return 0
}

// BuildNamespaceRedactor builds a Redactor loaded with known secret values and
// GitHub credentials from dataDir/secrets.
func BuildNamespaceRedactor(dataDir string) *redact.Redactor {
	return buildNamespaceRedactor(dataDir)
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
	srcDir := sourceDir("")
	coreSrcDir, coreModule := namespace.CoreSource(srcDir, defaultCoreSourceDir)
	nsServer := &namespace.Server{
		CIStore:     daemonNamespaceCIStore{fallback: fallbackCI},
		ExternalCI:  sandboxExternalCI{},
		ArtifactDir: ciArtifactDir(dataDir),
		Artifacts:   daemonNamespaceArtifacts{},
		SrcDir:      srcDir,
		CoreSrcDir:  coreSrcDir,
		CoreModule:  coreModule,
		// Rebuilt per request, as grainlog's redactor is per run
		// (capabilities.go), so a secret set after the daemon started
		// is redacted over /fs/ without waiting for a restart.
		NewRedactor: func() *redact.Redactor { return buildNamespaceRedactor(dataDir) },
	}
	// The same namespace answers GET /api/fs/ on grain's own API
	// (ui.Server.SetNamespace), for grain fs and external agents.
	liveNamespace.Store(nsServer)
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

// HostdRemoteFromFlags constructs the acceptor for a grain hostd dialing in
// with the token in tokenFile or $GRAIN_HOSTD_TOKEN, or nil when neither names
// a token.
func HostdRemoteFromFlags(tokenFile, name, owner string) (*hostd.Remote, error) {
	token := strings.TrimSpace(os.Getenv("GRAIN_HOSTD_TOKEN"))
	if tokenFile != "" {
		t, err := ReadTokenFile(tokenFile)
		if err != nil {
			return nil, fmt.Errorf("reading -hostd-token-file: %w", err)
		}
		token = t
	}
	if token == "" {
		return nil, nil
	}
	if name == "" {
		name = hostd.DefaultSharedName
	}
	if err := model.ValidateHostdName(name); err != nil {
		return nil, fmt.Errorf("-hostd-token-name: %w", err)
	}
	if name == model.LocalHostd {
		return nil, fmt.Errorf("-hostd-token-name: %q is the co-located hostd's name", name)
	}
	owner, err := model.NormalizeHostdOwner(owner)
	if err != nil {
		return nil, fmt.Errorf("-hostd-token-owner: %w", err)
	}
	log.Printf("grain daemon: -hostd-token-file is deprecated and goes in the next release: register hostd %s in the hostd registry (POST /api/hostds) and give it the token it answers with", name)
	return &hostd.Remote{Token: token, SharedName: name, SharedOwner: owner, Logf: log.Printf}, nil
}
