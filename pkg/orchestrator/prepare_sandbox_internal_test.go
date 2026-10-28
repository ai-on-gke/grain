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

package orchestrator

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

// cacheRecordingSandboxes is a host backend whose sandboxes also take
// commands, the way a VM's do, and write down everything run in them in
// the order it ran -- package-cache commands and shell scripts alike.
type cacheRecordingSandboxes struct {
	inner *HostSandboxes

	mu       sync.Mutex
	acquired []*cacheRecordingSandbox
}

func (c *cacheRecordingSandboxes) Acquire(ctx context.Context, name string, shape Shape) (Sandbox, error) {
	sb, err := c.inner.Acquire(ctx, name, shape)
	if err != nil {
		return nil, err
	}
	rec := &cacheRecordingSandbox{hostSandbox: sb.(*hostSandbox)}
	c.mu.Lock()
	c.acquired = append(c.acquired, rec)
	c.mu.Unlock()
	return rec, nil
}

func (c *cacheRecordingSandboxes) sandboxes() []*cacheRecordingSandbox {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*cacheRecordingSandbox(nil), c.acquired...)
}

type cacheRecordingSandbox struct {
	*hostSandbox

	mu  sync.Mutex
	ran []string
}

func (s *cacheRecordingSandbox) RunInSandbox(_ context.Context, argv []string, stdin string) (string, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ran = append(s.ran, "command: "+strings.Join(argv, " ")+"\n"+stdin)
	return "", "", 0
}

func (s *cacheRecordingSandbox) RunShell(ctx context.Context, req ShellRequest) (ShellResult, error) {
	s.mu.Lock()
	s.ran = append(s.ran, "shell: "+req.Script)
	s.mu.Unlock()
	return s.hostSandbox.RunShell(ctx, req)
}

func (s *cacheRecordingSandbox) history() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ran...)
}

const (
	testCacheBase = "http://10.9.0.1:41234"
	testCacheDNS  = "10.9.0.53"
)

func testCacheConfig() Config {
	return Config{
		PackageCache: func(context.Context) string { return testCacheBase },
		PackageCacheCA: func(context.Context) []byte {
			return []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n")
		},
		PackageCacheDNS: func(context.Context) string { return testCacheDNS },
	}
}

// assertPreparedBeforeShell checks that sb was given the cache's CA, its
// resolver and its package-manager files, all before the first shell
// script -- the checkout -- ran in it.
func assertPreparedBeforeShell(t *testing.T, sb *cacheRecordingSandbox) {
	t.Helper()
	ran := sb.history()
	firstShell := len(ran)
	for i, r := range ran {
		if strings.HasPrefix(r, "shell: ") {
			firstShell = i
			break
		}
	}
	before := strings.Join(ran[:firstShell], "\n")
	resolv := pkgcache.SandboxResolvConf(testCacheDNS)
	for _, want := range []string{"update-ca-certificates", resolv.Path, testCacheDNS, testCacheBase} {
		if !strings.Contains(before, want) {
			t.Errorf("%s: %q was not configured before the first shell script; ran before it:\n%s", sb.Name(), want, before)
		}
	}
	if firstShell == len(ran) {
		t.Errorf("%s: no shell script ran at all", sb.Name())
	}
}

func TestPersistentSandboxIsPointedAtThePackageCache(t *testing.T) {
	fastPersistentBackoff(t)
	repo := persistentTestRepo(t, map[string]string{"run.sh": "#!/bin/sh\nexec sleep 60\n"})
	s := newTestPersistentSupervisor(repo)
	t.Cleanup(s.Close)
	backend := &cacheRecordingSandboxes{inner: NewHostSandboxes(t.TempDir())}
	def := model.PersistentSandbox{
		Name: "cached", Repo: "acme/cached", Ref: "main", Command: "./run.sh", Enabled: true,
		NetworkAccess: model.NetworkOpen,
	}

	s.Reconcile(context.Background(), Deps{Sandboxes: backend, Config: testCacheConfig()}, []model.PersistentSandbox{def})
	waitFor(t, "running", func() bool { return persistentStatus(s, def).State == PersistentRunning })

	st := persistentStatus(s, def)
	if !strings.Contains(st.PackageCache, "through this deployment's package cache at "+testCacheBase) {
		t.Errorf("PackageCache = %q, want it to say the sandbox goes through the cache", st.PackageCache)
	}
	var logged bool
	for _, ev := range st.Events {
		if ev.Kind == "package-cache" && strings.Contains(ev.Message, testCacheBase) {
			logged = true
		}
	}
	if !logged {
		t.Errorf("no package-cache event: %+v", st.Events)
	}
	if got := backend.sandboxes(); len(got) != 1 {
		t.Fatalf("acquired %d sandboxes, want 1", len(got))
	}
	assertPreparedBeforeShell(t, backend.sandboxes()[0])

	// A rebuild is a new machine with a stock /etc: it is prepared again.
	if err := s.Rebuild("cached"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the rebuilt sandbox running", func() bool {
		return len(backend.sandboxes()) == 2 && persistentStatus(s, def).State == PersistentRunning
	})
	assertPreparedBeforeShell(t, backend.sandboxes()[1])
}

func TestPersistentSandboxSaysWhenThereIsNoPackageCache(t *testing.T) {
	fastPersistentBackoff(t)
	repo := persistentTestRepo(t, map[string]string{"run.sh": "#!/bin/sh\nexec sleep 60\n"})
	s := newTestPersistentSupervisor(repo)
	t.Cleanup(s.Close)
	def := model.PersistentSandbox{Name: "uncached", Repo: "acme/uncached", Ref: "main", Command: "./run.sh", Enabled: true}

	s.Reconcile(context.Background(), Deps{Sandboxes: NewHostSandboxes(t.TempDir())}, []model.PersistentSandbox{def})
	waitFor(t, "running", func() bool { return persistentStatus(s, def).State == PersistentRunning })
	if st := persistentStatus(s, def); !strings.Contains(st.PackageCache, "from the internet") {
		t.Errorf("PackageCache = %q, want it to say the sandbox fetches from the internet", st.PackageCache)
	}
}

func TestPrepareSandboxDoesNothingWithNoCache(t *testing.T) {
	sb := &commandedSandbox{}
	if prep := PrepareSandbox(context.Background(), PackageCacheSource{}, sb); prep.Configured || prep.Err != nil {
		t.Errorf("PrepareSandbox with no cache = %+v, want nothing done", prep)
	}
	if len(sb.commands) != 0 {
		t.Errorf("ran %d commands in a sandbox on a deployment with no cache", len(sb.commands))
	}
}

// TestEverySandboxAcquireIsPrepared is the guard for the gap persistent
// sandboxes fell into: every function in grain that acquires a sandbox
// from a backend has to call PrepareSandbox (directly, or through
// applyPackageCache) itself, and nothing but PrepareSandbox configures
// the cache, so a new kind of sandbox cannot quietly go without it.
func TestEverySandboxAcquireIsPrepared(t *testing.T) {
	var dirs = []string{".", "../daemon"}
	type site struct{ file, fn string }
	var acquiring []site
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				var acquires, prepares, configures bool
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					var name string
					switch f := call.Fun.(type) {
					case *ast.SelectorExpr:
						name = f.Sel.Name
					case *ast.Ident:
						name = f.Name
					}
					switch name {
					case "Acquire":
						// A backend's Acquire(ctx, name, shape), or the
						// meter's (ctx, backend, kind, name, shape) --
						// not ci.Sandboxes' (ctx, request) or a gate's.
						if len(call.Args) >= 3 {
							acquires = true
						}
					case "PrepareSandbox", "applyPackageCache":
						prepares = true
					case "ConfigurePackageCacheTransparent":
						configures = true
					}
					return true
				})
				key := filepath.Base(path) + ":" + fn.Name.Name
				if configures && fn.Name.Name != "PrepareSandbox" {
					t.Errorf("%s configures the package cache itself; call PrepareSandbox instead", key)
				}
				if !acquires {
					continue
				}
				// A backend's Acquire, or the meter's, delegating to
				// another backend's is not where a sandbox is handed to
				// anything; its callers are. (CI's Acquire takes a request
				// rather than a shape, and is one of those callers.)
				if fn.Name.Name == "Acquire" && fn.Recv != nil && fn.Type.Params.NumFields() >= 3 {
					continue
				}
				acquiring = append(acquiring, site{key, fn.Name.Name})
				if !prepares {
					t.Errorf("%s acquires a sandbox but never calls PrepareSandbox -- its sandbox would get "+
						"neither this deployment's package cache nor its CA and resolver", key)
				}
			}
		}
	}
	// The scan has to be finding the paths it guards, or it guards nothing.
	for _, want := range []string{"cycle.go:runOneWithFramework", "persistent_sandboxes.go:build", "ci.go:Acquire"} {
		var found bool
		for _, s := range acquiring {
			if s.file == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the scan did not find %s acquiring a sandbox; found %v", want, acquiring)
		}
	}
}

// A standing sandbox is served rather than acquired, so the scan above
// does not see it: its spec has to carry PrepareSandbox, and Serve has to
// run it before the server starts.
func TestStandingSandboxIsPointedAtThePackageCache(t *testing.T) {
	var got ServeSpec
	sup := NewStandingSandboxSupervisor(func(_ context.Context, spec ServeSpec) (ServingSandbox, error) {
		got = spec
		return &fakeServingSandbox{spec: spec}, nil
	})
	deps := Deps{Config: testCacheConfig()}
	if _, _, _, err := sup.launch(context.Background(), deps, model.StandingSandbox{Name: "preview", Image: "img"}, nil); err != nil {
		t.Fatal(err)
	}
	if got.Prepare == nil {
		t.Fatal("a standing sandbox's spec has no Prepare: it would never be pointed at the package cache")
	}
	sb := &commandedSandbox{}
	got.Prepare(context.Background(), sb)
	var ran strings.Builder
	for i, argv := range sb.commands {
		ran.WriteString(strings.Join(argv, " ") + "\n" + sb.stdins[i] + "\n")
	}
	for _, want := range []string{"update-ca-certificates", testCacheDNS, testCacheBase} {
		if !strings.Contains(ran.String(), want) {
			t.Errorf("Prepare did not configure %q; ran:\n%s", want, ran.String())
		}
	}

	var order []string
	runner := &orderRunner{order: &order}
	served := &servingSandbox{sandbox: &granuleSandbox{name: "sb", runner: runner}, unit: "standing-preview"}
	spec := ServeSpec{Command: "serve", Prepare: func(context.Context, Sandbox) { order = append(order, "prepare") }}
	if err := served.start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "prepare" || order[1] != "start" {
		t.Errorf("start ran %v, want Prepare before the server", order)
	}
}

type orderRunner struct{ order *[]string }

func (r *orderRunner) Run(context.Context, []string, string) (string, string, int) {
	*r.order = append(*r.order, "start")
	return "", "", 0
}
