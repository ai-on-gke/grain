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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

// goCacheRecordingSandbox is a job's machine that records every command
// run in it and runs none: enough to see what GOCACHEPROG a job is given.
type goCacheRecordingSandbox struct {
	mu   sync.Mutex
	cmds [][]string
}

func (s *goCacheRecordingSandbox) Name() string { return "ci-gocache-job" }
func (s *goCacheRecordingSandbox) Tools(context.Context) ([]mcp.Tool, error) {
	return nil, nil
}
func (s *goCacheRecordingSandbox) ConfigureGitCredentials(context.Context, string, string, mcp.GitIdentity) error {
	return nil
}
func (s *goCacheRecordingSandbox) Release(context.Context) error { return nil }
func (s *goCacheRecordingSandbox) RunInSandbox(_ context.Context, argv []string, _ string) (string, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cmds = append(s.cmds, append([]string(nil), argv...))
	return "", "", 0
}

// goCacheProgURL is the URL the last `go env -w GOCACHEPROG=...` run in
// the sandbox points its helper at, and "" when none was run.
func (s *goCacheRecordingSandbox) goCacheProgURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := ""
	for _, argv := range s.cmds {
		for _, arg := range argv {
			if v, ok := strings.CutPrefix(arg, "GOCACHEPROG="); ok {
				if fields := strings.Fields(v); len(fields) == 2 {
					found = fields[1]
				}
			}
		}
	}
	return found
}

// defaultBranchClient is a GitHub client that knows one thing: the
// repository's default branch.
type defaultBranchClient struct {
	github.Client
	branch string
}

func (c defaultBranchClient) DefaultBranch(string, string) (string, error) { return c.branch, nil }

// goCacheProgSession runs one GOCACHEPROG session against baseURL, as the
// helper inside a sandbox would, and returns its responses by request ID.
func goCacheProgSession(t *testing.T, baseURL string, reqs ...any) map[int64]pkgcache.GoCacheProgResponse {
	t.Helper()
	var in, out bytes.Buffer
	enc := json.NewEncoder(&in)
	for _, r := range reqs {
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := pkgcache.RunGoCacheProg(context.Background(), &in, &out, baseURL, t.TempDir()); err != nil {
		t.Fatalf("RunGoCacheProg: %v", err)
	}
	resps := map[int64]pkgcache.GoCacheProgResponse{}
	dec := json.NewDecoder(&out)
	for {
		var resp pkgcache.GoCacheProgResponse
		if err := dec.Decode(&resp); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decoding GOCACHEPROG response: %v", err)
		}
		resps[resp.ID] = resp
	}
	return resps
}

// putGoCacheAction PUTs one action entry straight at the cache, as a
// sandbox's own code could, with basic-auth token when it is not "".
func putGoCacheAction(t *testing.T, base, token, actionID string) int {
	t.Helper()
	body, _ := json.Marshal(pkgcache.GoCacheAction{OutputID: strings.Repeat("ab", 32), Size: 1})
	req, err := http.NewRequest(http.MethodPut, strings.TrimSuffix(base, "/")+"/gocache/action/"+actionID, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.SetBasicAuth(pkgcache.GoCacheJobUser, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// newGoCacheCI is a running package cache, as the daemon starts it, and a
// CI sandbox adapter wired to it the way run.go wires the daemon's: its
// writer registry, and the reviewed-branch rule trusted releases use, over
// a store whose repository's default branch is main.
func newGoCacheCI(t *testing.T) (*PackageCache, CiSandboxes) {
	t.Helper()
	dataDir := t.TempDir()
	pc, stop, err := startPackageCacheFrom(Config{DataDir: dataDir, PackageCacheGB: 1, PackageCacheEnabled: true},
		NewDirect(""), packageCacheUpstreams{}, nil)
	if err != nil {
		t.Fatalf("startPackageCacheFrom: %v", err)
	}
	t.Cleanup(func() { _ = stop(context.Background()) })
	if pc.GoCacheJobs == nil || pc.Url == "" {
		t.Fatalf("package cache = %+v, want a URL and a Go build-cache writer registry", pc)
	}
	store, closeStore := openTestStore(t, dataDir)
	t.Cleanup(closeStore)

	service := &ci.Service{Engine: &ci.Engine{Sandboxes: StateCheckSandboxes{Sandboxes: CiSandboxes{
		PackageCache: func(context.Context) string { return pc.Url },
	}}}}
	WithCIGoCacheWriters(service, pc.GoCacheJobs, CiReleaseRefRefusal(store, defaultBranchClient{branch: "main"}))
	jobs := service.Engine.Sandboxes.(StateCheckSandboxes).Sandboxes.(CiSandboxes)
	if jobs.GoCacheJobs != pc.GoCacheJobs || jobs.GoCacheRefRefusal == nil {
		t.Fatal("WithCIGoCacheWriters did not reach the CI job sandboxes")
	}
	return pc, jobs
}

// TestATrustedCIJobsGoBuildsReachALaterTaskSandbox is the point of
// registering trusted jobs: a push to the default branch writes the Go
// build cache through its GOCACHEPROG, and a task sandbox that comes
// later -- presenting no token, as every task sandbox does -- is served
// what it compiled, while still being unable to write anything itself.
// And the job's token is worth nothing once the job is over.
func TestATrustedCIJobsGoBuildsReachALaterTaskSandbox(t *testing.T) {
	ctx := context.Background()
	pc, jobs := newGoCacheCI(t)

	backing := &goCacheRecordingSandbox{}
	revoke := jobs.grantGoCacheWrites(ctx, ci.SandboxRequest{
		Name: "ci-tests-build", Repo: "acme/widgets", Ref: "main", Commit: strings.Repeat("1", 40), Event: ci.EventPush,
	}, backing)
	if revoke == nil {
		t.Fatal("a push to the default branch was not made a Go build-cache writer")
	}
	jobURL := backing.goCacheProgURL()
	u, err := url.Parse(jobURL)
	if err != nil || u.User == nil {
		t.Fatalf("the job's GOCACHEPROG URL = %q, want one carrying its token as basic auth", jobURL)
	}
	token, _ := u.User.Password()
	if u.User.Username() != pkgcache.GoCacheJobUser || token == "" {
		t.Fatalf("the job's GOCACHEPROG URL = %q, want http://%s:<token>@host:port", jobURL, pkgcache.GoCacheJobUser)
	}
	if access, ok := pc.GoCacheJobs.Lookup(&http.Request{Header: http.Header{"Grit-Job-Token": {token}}, URL: &url.URL{Path: "/"}}); !ok || !access.Trusted {
		t.Fatalf("the job's token is registered as %+v (found %v), want trusted", access, ok)
	}

	// The job compiles something; its helper puts it.
	actionID := sha256.Sum256([]byte("acme/widgets compile"))
	body := []byte("compiled widgets.a")
	outputID := sha256.Sum256(body)
	put := goCacheProgSession(t, jobURL,
		pkgcache.GoCacheProgRequest{ID: 1, Command: pkgcache.GoCacheProgCmdPut, ActionID: actionID[:], OutputID: outputID[:], BodySize: int64(len(body))},
		body,
		pkgcache.GoCacheProgRequest{ID: 2, Command: pkgcache.GoCacheProgCmdClose},
	)
	if put[1].Err != "" {
		t.Fatalf("the trusted job's put = %+v", put[1])
	}

	// A later task sandbox, with the plain URL every sandbox is given.
	got := goCacheProgSession(t, pc.Url,
		pkgcache.GoCacheProgRequest{ID: 1, Command: pkgcache.GoCacheProgCmdGet, ActionID: actionID[:]},
		pkgcache.GoCacheProgRequest{ID: 2, Command: pkgcache.GoCacheProgCmdClose},
	)
	if got[1].Miss || got[1].Err != "" || hex.EncodeToString(got[1].OutputID) != hex.EncodeToString(outputID[:]) {
		t.Fatalf("a task sandbox's get of what the trusted job compiled = %+v, want a hit on output %x", got[1], outputID)
	}

	// Which does not make the task sandbox a writer.
	if code := putGoCacheAction(t, pc.Url, "", strings.Repeat("cd", 32)); code != http.StatusForbidden {
		t.Errorf("a task sandbox's PUT of an action = %d, want 403", code)
	}
	// The job's own token writes while the job lives...
	if code := putGoCacheAction(t, pc.Url, token, strings.Repeat("ef", 32)); code != http.StatusOK {
		t.Errorf("the trusted job's PUT of an action = %d, want 200", code)
	}
	// ...and not once it has ended.
	revoke()
	if code := putGoCacheAction(t, pc.Url, token, strings.Repeat("01", 32)); code == http.StatusOK {
		t.Errorf("the ended job's token still writes the Go build cache (PUT = %d)", code)
	}
}

// TestATaskBranchCIJobIsNotATrustedGoCacheWriter: CI on anything its
// author controls -- a task's branch, a pull request, a commit nothing
// vouches for, a manual run -- keeps the token-less GOCACHEPROG every
// sandbox gets, and nothing is registered for it.
func TestATaskBranchCIJobIsNotATrustedGoCacheWriter(t *testing.T) {
	ctx := context.Background()
	pc, jobs := newGoCacheCI(t)
	const taskBranch = "grain/0a1b2c3d/task-4527-register-trusted"
	commit := strings.Repeat("2", 40)

	for _, tc := range []struct {
		name string
		req  ci.SandboxRequest
		want bool
	}{
		{"push to a task's branch", ci.SandboxRequest{Ref: taskBranch, Event: ci.EventPush}, false},
		{"push to a task's branch, spelled as a ref", ci.SandboxRequest{Ref: "refs/heads/" + taskBranch, Event: ci.EventPush}, false},
		{"pull request into the default branch", ci.SandboxRequest{Ref: "main", Event: ci.EventPullRequest}, false},
		{"push to some other branch", ci.SandboxRequest{Ref: "feature", Event: ci.EventPush}, false},
		{"unvouched commit on the default branch", ci.SandboxRequest{Ref: "main", Event: ci.EventPush, Untrusted: true}, false},
		{"manual run of the default branch", ci.SandboxRequest{Ref: "main", Event: ci.EventManual}, false},
		{"schedule a caller fired", ci.SandboxRequest{Ref: "main", Event: ci.EventSchedule}, false},
		{"no ref", ci.SandboxRequest{Event: ci.EventPush}, false},
		{"schedule grain fired on the default branch", ci.SandboxRequest{Ref: "main", Event: ci.EventSchedule, TrustedSchedule: true}, true},
		{"push to the default branch", ci.SandboxRequest{Ref: "refs/heads/main", Event: ci.EventPush}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.Name, tc.req.Repo, tc.req.Commit = "ci-job", "acme/widgets", commit
			backing := &goCacheRecordingSandbox{}
			revoke := jobs.grantGoCacheWrites(ctx, tc.req, backing)
			if revoke != nil {
				defer revoke()
			}
			if got := revoke != nil; got != tc.want {
				t.Fatalf("granted = %v, want %v (refusal %q)", got, tc.want, jobs.ciGoCacheWriteRefusal(ctx, tc.req))
			}
			if !tc.want {
				if got := backing.goCacheProgURL(); got != "" {
					t.Errorf("an untrusted job's GOCACHEPROG was repointed at %q", got)
				}
				if len(backing.cmds) != 0 {
					t.Errorf("commands were run in an untrusted job's sandbox: %q", backing.cmds)
				}
			}
		})
	}

	// And with nothing registered, nothing but a trusted job writes.
	if code := putGoCacheAction(t, pc.Url, "", strings.Repeat("23", 32)); code != http.StatusForbidden {
		t.Errorf("an unregistered PUT of an action = %d, want 403", code)
	}
}

// TestBuildCIServiceWiresBeforeAdoptingInFlightRuns guards against the
// startup race where BuildCIService resumed unconcluded CI runs before
// WithCIGoCacheWriters and the capability hooks finished wiring
// service.Engine.Sandboxes and Admission.
func TestBuildCIServiceWiresBeforeAdoptingInFlightRuns(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	sandboxDir := t.TempDir()
	gateDir := t.TempDir()
	release := filepath.Join(gateDir, "release")
	t.Cleanup(func() { _ = os.WriteFile(release, []byte("ok\n"), 0o644) })
	workflowYAML := `name: tests
on: push
jobs:
  slow:
    steps:
      - run: |
          echo step-started
          until [ -e "` + release + `" ] || [ ! -d "` + gateDir + `" ]; do sleep 0.02; done
  after:
    needs: [slow]
    steps:
      - run: echo after-done
`
	bareRepoRoot, commitSHA, source := setupTestCIRepo(t, workflowYAML)

	store1, closeStore1 := openTestStore(t, dataDir)
	sb1 := orchestrator.NewHostSandboxes(sandboxDir)
	svc1 := &ci.Service{
		Source:  source,
		Engine:  &ci.Engine{Sandboxes: newTestCiSandboxes(sb1, bareRepoRoot)},
		Runs:    ci.NewStoredRunStore(store1, ci.StoredRunStoreOptions{}),
		PlanDir: filepath.Join(dataDir, "state", "ci-plans"),
	}
	started, err := svc1.Start(ctx, ci.Request{Repo: "acme/widgets", Ref: "main", Commit: commitSHA, Event: ci.EventPush})
	if err != nil {
		t.Fatalf("starting CI run: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		r, _ := svc1.Get(started.ID)
		var slowStarted bool
		for _, j := range r.Jobs {
			if j.JobID == "slow" && !j.Conclusion.Done() && j.StartedAt != nil && j.Sandbox != "" {
				if stepRunnerStarted(filepath.Join(sandboxDir, j.Sandbox), "step-started") {
					slowStarted = true
				}
			}
		}
		if slowStarted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for job step runner to start; run=%+v", r)
		}
		time.Sleep(20 * time.Millisecond)
	}
	svc1.Detach()
	closeStore1()

	store2, closeStore2 := openTestStore(t, dataDir)
	defer closeStore2()
	store2.SingleTenant = true
	sb2 := orchestrator.NewHostSandboxes(sandboxDir)
	tokens := gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "sandbox-tokens.json"))
	jobs := pkgcache.NewJobTokenSet(false)
	cfg := Config{DataDir: dataDir, SandboxDir: sandboxDir, HostSandboxes: true, Ci: true}
	svc2 := BuildCIService(ctx, cfg, func() model.Limits { return model.Limits{} }, nil, nil, store2, sb2,
		&gitproxy.CredentialSet{}, "file://localhost"+bareRepoRoot, tokens, nil, nil, nil, nil,
		func(context.Context) string { return "http://127.0.0.1:9" }, nil, nil, nil,
		func(svc *ci.Service) { svc.Source = source },
		WithDaemonCIWiring(func() string { return "" },
			CiReleaseRefRefusal(store2, defaultBranchClient{branch: "main"}),
			&PackageCache{GoCacheJobs: jobs},
			func() orchestrator.CapabilityServers { return nil }, nil, ""))
	defer svc2.Detach()

	ciSb := svc2.Engine.Sandboxes.(StateCheckSandboxes).Sandboxes.(CiSandboxes)
	if ciSb.GoCacheJobs != jobs || ciSb.GoCacheRefRefusal == nil {
		t.Fatalf("CiSandboxes GoCache wiring = (jobs=%v, refusalNil=%v), want (%v, false)", ciSb.GoCacheJobs, ciSb.GoCacheRefRefusal == nil, jobs)
	}
	if adm, ok := svc2.Engine.Admission.(*CiGrantReservations); !ok || adm.Servers == nil {
		t.Fatalf("Engine.Admission capability servers not wired before adopt: %+v", svc2.Engine.Admission)
	}
	if err := os.WriteFile(release, []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := waitConcluded(t, svc2, started.ID)
	if run.Conclusion != ci.ConclusionSuccess || run.Interrupted {
		t.Fatalf("adopted run = %s (interrupted %v), want success: %+v", run.Conclusion, run.Interrupted, run)
	}
}
