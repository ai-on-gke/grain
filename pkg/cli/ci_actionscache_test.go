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

package cli

// What a CI job is told about this deployment's Actions cache, and what
// it is deliberately not told when the cache cannot be reached.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/actionscache"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/grit/actions"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// commandingSandbox is a fakeSandbox that also answers the direct
// transport ConfigurePackageCache and the cache probe use -- the real
// backends both have one, and without it neither can write a file.
type commandingSandbox struct {
	fakeSandbox
	mu sync.Mutex
	// written is the last file installed, by path.
	written map[string]string
	// probeExit is what the reachability check exits with.
	probeExit int
}

func (s *commandingSandbox) RunInSandbox(_ context.Context, argv []string, stdin string) (string, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	script := ""
	if len(argv) > 2 {
		script = argv[2]
	}
	switch {
	case strings.Contains(script, "artifactcache/cache?keys=grain-ci-probe"):
		if s.probeExit != 0 {
			return "", "could not connect", s.probeExit
		}
		return "", "", 0
	case strings.Contains(script, "install -m"):
		if s.written == nil {
			s.written = map[string]string{}
		}
		// writeAsRootScript is run as `sh -c <script> sh <path> <mode>`.
		if len(argv) > 4 {
			s.written[argv[4]] = stdin
		}
		return "", "", 0
	}
	return "", "", 0
}

func (s *commandingSandbox) file(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written[path]
}

// A job's sandbox is told where its own repository's cache is, and the
// token in that URL is given back when the sandbox is released.
func TestACIJobIsToldWhereTheActionsCacheIs(t *testing.T) {
	t.Parallel()
	sandbox := &commandingSandbox{}
	adapter, _, _ := newCIAdapter(&sandbox.fakeSandbox)
	adapter.Sandboxes = commandingBackend{sandbox}

	var granted actionscache.Scope
	var revoked string
	adapter.ActionsCache = func(scope actionscache.Scope) (string, func(), error) {
		granted = scope
		return "http://10.0.0.1:9000/token-for-this-job/", func() { revoked = scope.Repo }, nil
	}

	job, err := adapter.Acquire(context.Background(), ci.SandboxRequest{
		Name: "ci-run1-ci-image", Repo: "acme/widgets", Ref: "main", Commit: "deadbeef", Event: ci.EventPush,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if want := (actionscache.Scope{Repo: "acme/widgets", Ref: "main"}); granted != want {
		t.Errorf("the cache was asked for %+v, want the repository and branch under test, untrusted", granted)
	}
	if got := strings.TrimSpace(sandbox.file(actions.CacheURLFile)); got != "http://10.0.0.1:9000/token-for-this-job/" {
		t.Errorf("%s holds %q, want the job's own cache URL", actions.CacheURLFile, got)
	}
	if err := job.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if revoked != "acme/widgets" {
		t.Error("releasing the sandbox left its cache token live")
	}
}

// Only a release run is trusted with the cache, because only a release
// is an event no caller can start: everything else -- a push to the
// very branch being released included -- writes entries a release never
// reads (issue #2392).
func TestACIJobsCacheScopeIsTrustedOnlyForARelease(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		event ci.Event
		want  bool
	}{
		{ci.EventRelease, true},
		{ci.EventPush, false},
		{ci.EventPullRequest, false},
		{ci.EventManual, false},
		{ci.EventSchedule, false},
	} {
		got := daemon.CiActionsCacheScope(ci.SandboxRequest{Repo: "acme/widgets", Ref: "main", Event: tc.event})
		if got.Trusted != tc.want || got.Repo != "acme/widgets" || got.Ref != "main" {
			t.Errorf("%s: scope %+v, want trusted=%v on acme/widgets@main", tc.event, got, tc.want)
		}
	}
}

// A job with no branch to name has no corner of the cache to be scoped
// to, so it is told about no cache at all -- and the real grant mints
// no token for it.
func TestAnActionsCacheGrantNeedsARef(t *testing.T) {
	t.Parallel()
	a := &daemon.ActionsCache{Scopes: actionscache.NewScopes(), Url: "http://10.0.0.1:9000/"}
	url, revoke, err := a.Grant(nil, actionscache.Scope{Repo: "acme/widgets"})
	revoke()
	if err != nil || url != "" || a.Scopes.Granted() != 0 {
		t.Errorf("a grant with no ref answered %q, %v with %d token(s) live; want no cache", url, err, a.Scopes.Granted())
	}
	url, revoke, err = a.Grant(nil, actionscache.Scope{Repo: "acme/widgets", Ref: "grain/task-1"})
	if err != nil || url == "" || a.Scopes.Granted() != 1 {
		t.Fatalf("a grant for a branch answered %q, %v with %d token(s) live", url, err, a.Scopes.Granted())
	}
	revoke()
	if a.Scopes.Granted() != 0 {
		t.Error("revoking left the token live")
	}
}

// A cache the sandbox cannot reach is worse than no cache at all:
// buildkit fails the build when its cache exporter cannot answer. So a
// failed probe leaves act running its own server, exactly as before.
func TestACIJobKeepsItsOwnCacheServerWhenOursIsUnreachable(t *testing.T) {
	t.Parallel()
	sandbox := &commandingSandbox{probeExit: 7}
	adapter, _, _ := newCIAdapter(&sandbox.fakeSandbox)
	adapter.Sandboxes = commandingBackend{sandbox}
	adapter.ActionsCache = func(actionscache.Scope) (string, func(), error) {
		return "http://10.0.0.1:9000/token-for-this-job/", func() {}, nil
	}

	if _, err := adapter.Acquire(context.Background(), ci.SandboxRequest{
		Name: "ci-run1-ci-image", Repo: "acme/widgets", Ref: "main", Commit: "deadbeef",
	}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if got := sandbox.file(actions.CacheURLFile); got != "" {
		t.Errorf("%s holds %q, want nothing at all", actions.CacheURLFile, got)
	}
}

// A deployment keeping no cache tells a job nothing, and that is not a
// failure of anything.
func TestACIJobWithNoActionsCache(t *testing.T) {
	t.Parallel()
	sandbox := &commandingSandbox{}
	adapter, _, _ := newCIAdapter(&sandbox.fakeSandbox)
	adapter.Sandboxes = commandingBackend{sandbox}
	adapter.ActionsCache = func(actionscache.Scope) (string, func(), error) { return "", func() {}, nil }

	job, err := adapter.Acquire(context.Background(), ci.SandboxRequest{
		Name: "ci-run1-ci-image", Repo: "acme/widgets", Ref: "main",
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if got := sandbox.file(actions.CacheURLFile); got != "" {
		t.Errorf("%s holds %q on a deployment with no cache", actions.CacheURLFile, got)
	}
	if err := job.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// commandingBackend hands out the one sandbox above, which the fake
// backend beside it cannot do without losing the extra transport.
type commandingBackend struct{ sandbox *commandingSandbox }

func (b commandingBackend) Acquire(_ context.Context, name string, _ orchestrator.Shape) (orchestrator.Sandbox, error) {
	b.sandbox.name = name
	return b.sandbox, nil
}
