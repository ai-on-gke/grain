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

package lifecycle

// The registry: what a repository's lifecycle resolves to, when a serving
// sandbox is started and released, and which failures hold rather than
// fall back.
//
// No docker, no registry and no VM here -- Images and Sandboxes are
// interfaces precisely so that the policy above them can be tested at the
// speed policy deserves.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/statemachine"
	_ "modernc.org/sqlite"
)

var repo = model.RepoRef{Owner: "acme", Name: "billing"}

func declaration() model.LifecycleDeclaration {
	return model.LifecycleDeclaration{
		Name:  "acme task lifecycle",
		Image: "ghcr.io/acme/billing/grain-lifecycle",
		Serve: model.LifecycleServe{
			Run: "python3 -m lifecycle.server", Listen: model.DefaultLifecycleListen,
			ReadyTimeout: 2 * time.Second, RequestTimeout: time.Second,
		},
	}
}

type fakeDeclarations struct {
	declared model.LifecycleDeclaration
	commit   string
	err      error
}

func (f *fakeDeclarations) Lifecycle(context.Context, model.RepoRef) (model.LifecycleDeclaration, string, error) {
	return f.declared, f.commit, f.err
}

type fakeImages struct {
	digest      string
	resolveErr  error
	prepareErr  error
	local       string
	resolvedTag string
	prepared    int32
}

func (f *fakeImages) Resolve(_ context.Context, _, tag string) (string, error) {
	f.resolvedTag = tag
	if f.resolveErr != nil {
		return "", f.resolveErr
	}
	return f.digest, nil
}

func (f *fakeImages) Prepare(context.Context, string, string) (string, error) {
	atomic.AddInt32(&f.prepared, 1)
	if f.prepareErr != nil {
		return "", f.prepareErr
	}
	return f.local, nil
}

// fakeSandboxes hands out connections to a real HTTP server, which is
// what a serving sandbox is from the registry's side: something to dial.
type fakeSandboxes struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []ServeRequest
	released int
	serveErr error
}

func (f *fakeSandboxes) Serve(_ context.Context, req ServeRequest) (Serving, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.serveErr != nil {
		return nil, f.serveErr
	}
	f.requests = append(f.requests, req)
	return &fakeServing{parent: f, addr: strings.TrimPrefix(f.server.URL, "http://")}, nil
}

func (f *fakeSandboxes) served() []ServeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ServeRequest(nil), f.requests...)
}

func (f *fakeSandboxes) releases() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.released
}

type fakeServing struct {
	parent *fakeSandboxes
	addr   string
	dials  atomic.Int32
}

// DialStream stands in for exec'ing grain-connect in the guest: the argv
// is checked, and what comes back is a connection to the definition.
func (s *fakeServing) DialStream(ctx context.Context, argv []string) (net.Conn, error) {
	if len(argv) != 2 || argv[0] != ForwarderCommand || argv[1] != model.DefaultLifecycleListen {
		return nil, fmt.Errorf("dialled %q, and a dial is grain's own forwarder against the declared socket", argv)
	}
	s.dials.Add(1)
	return (&net.Dialer{}).DialContext(ctx, "tcp", s.addr)
}

func (s *fakeServing) Release(context.Context) error {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()
	s.parent.released++
	return nil
}

// definition is a well-behaved server implementing the REST contract.
func definition(t *testing.T, token *string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if token != nil && req.Header.Get("Authorization") != "Bearer "+*token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch req.URL.Path {
		case statemachine.HealthPath:
			fmt.Fprint(w, `{"protocol":"1","ready":true,"definition":"acme v4"}`)
		case statemachine.ReducePath:
			var body struct {
				ID string `json:"id"`
			}
			_ = json.NewDecoder(req.Body).Decode(&body)
			fmt.Fprintf(w, `{"id":%q,"state":{"phase":"done"},"effects":[],"accepts":["task.observed"]}`, body.ID)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func registry(t *testing.T, decls *fakeDeclarations, images *fakeImages, sandboxes *fakeSandboxes) *Registry {
	t.Helper()
	r := &Registry{Declarations: decls, Images: images, Sandboxes: sandboxes, IdleTTL: time.Hour}
	t.Cleanup(func() { r.Close(context.Background()) })
	return r
}

func TestResolvePinsTheDigestTheReleaseJobPublished(t *testing.T) {
	decls := &fakeDeclarations{declared: declaration(), commit: "c0ffee"}
	images := &fakeImages{digest: testDigest, local: "grain-lifecycle-guest:abc"}
	r := registry(t, decls, images, &fakeSandboxes{})

	version, err := r.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if version.Digest != testDigest || version.Repo != repo {
		t.Fatalf("version = %+v", version)
	}
	// The tag is the commit the release job published at, so there is no
	// mutable tag in this path at all.
	if images.resolvedTag != "c0ffee" {
		t.Errorf("resolved tag %q", images.resolvedTag)
	}
	if got := version.String(); got != "acme/billing@"+testDigest {
		t.Errorf("stored form %q", got)
	}
	back, err := ParseVersion(version.String())
	if err != nil || back != version {
		t.Errorf("round trip: %+v, %v", back, err)
	}
	if status := r.Status(context.Background(), repo); status.State != model.LifecycleReady ||
		status.Version != version.String() || !status.Declared {
		t.Errorf("status = %+v", status)
	}
}

// A repository with no image is the zero-config path: the stock guest with
// its own checkout, no registry, no release job, no capability.
func TestResolveWithoutAnImagePinsTheCommit(t *testing.T) {
	declared := declaration()
	declared.Image = ""
	images := &fakeImages{}
	r := registry(t, &fakeDeclarations{declared: declared, commit: "abc123"}, images, &fakeSandboxes{})

	version, err := r.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !version.Stock() || version.Commit != "abc123" {
		t.Fatalf("version = %+v", version)
	}
	if version.String() != "acme/billing@commit:abc123" {
		t.Errorf("stored form %q", version)
	}
	if images.prepared != 0 {
		t.Error("the zero-config path pulled an image")
	}
}

// Every repository's state today: no declaration, the deployment default,
// and not an error.
func TestResolveWithNoDeclarationIsTheDeploymentDefault(t *testing.T) {
	r := registry(t, &fakeDeclarations{}, &fakeImages{}, &fakeSandboxes{})
	version, err := r.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if version != (Version{}) {
		t.Fatalf("version = %+v", version)
	}
	if status := r.Status(context.Background(), repo); status.State != model.LifecycleNone || status.Declared {
		t.Errorf("status = %+v", status)
	}
}

// Nothing falls back. A broken lifecycle build silently reverting a
// repository to the deployment default would mean a bad merge quietly
// changing what "approved" means there.
func TestResolveHoldsRatherThanFallingBack(t *testing.T) {
	for name, setup := range map[string]struct {
		images *fakeImages
		state  model.LifecycleState
	}{
		"the tag never resolved": {
			images: &fakeImages{resolveErr: errors.New("manifest unknown")},
			state:  model.LifecycleUnpublished,
		},
		"the pull failed": {
			images: &fakeImages{digest: testDigest, prepareErr: errors.New("registry unreachable")},
			state:  model.LifecycleFailed,
		},
		"the image is not a guest": {
			images: &fakeImages{digest: testDigest,
				prepareErr: &Refusal{Reason: "no /var/lib/granule/guest/disk.img in it"}},
			state: model.LifecycleRefused,
		},
	} {
		r := registry(t, &fakeDeclarations{declared: declaration(), commit: "c0ffee"}, setup.images, &fakeSandboxes{})
		version, err := r.Resolve(context.Background(), repo)
		if err == nil {
			t.Errorf("%s: no error, version %+v", name, version)
			continue
		}
		if version != (Version{}) {
			t.Errorf("%s: it handed back %+v rather than holding", name, version)
		}
		status := r.Status(context.Background(), repo)
		if status.State != setup.state {
			t.Errorf("%s: state = %q, want %q", name, status.State, setup.state)
		}
		if status.Error == "" {
			t.Errorf("%s: nothing on the lifecycle's error to act on", name)
		}
	}
}

// The namespace refusal is pkg/repodecl's, and it is repeated here
// because a DeclarationSource is an interface: this is the one refusal
// that must not be reachable by implementing it differently.
func TestResolveRefusesAnImageOutsideTheRepositorysNamespace(t *testing.T) {
	declared := declaration()
	declared.Image = "ghcr.io/attacker/evil/grain-lifecycle"
	images := &fakeImages{digest: testDigest}
	r := registry(t, &fakeDeclarations{declared: declared, commit: "c0ffee"}, images, &fakeSandboxes{})

	if _, err := r.Resolve(context.Background(), repo); err == nil {
		t.Fatal("another repository's namespace was accepted")
	}
	if images.prepared != 0 {
		t.Error("it pulled an image it was never going to serve")
	}
	if status := r.Status(context.Background(), repo); status.State != model.LifecycleRefused {
		t.Errorf("state = %q", status.State)
	}
}

// The whole of it end to end: a resolved version becomes a reducer whose
// dial is an exec and whose answer is the contract's.
func TestReducerServesOverTheSandboxesDial(t *testing.T) {
	var token string
	sandboxes := &fakeSandboxes{server: definition(t, &token)}
	decls := &fakeDeclarations{declared: declaration(), commit: "c0ffee"}
	r := registry(t, decls, &fakeImages{digest: testDigest, local: "grain-lifecycle-guest:abc"}, sandboxes)
	r.NewToken = func() (string, error) { token = "sealed-token"; return token, nil }

	version, err := r.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	reducer, err := r.Reducer(context.Background(), version.String())
	if err != nil {
		t.Fatalf("Reducer: %v", err)
	}

	ctx := statemachine.WithReduceContext(context.Background(),
		statemachine.ReduceContext{Instance: "123", Sequence: 11})
	result, err := reducer.Reduce(ctx, json.RawMessage(`{"phase":"waiting"}`),
		statemachine.Event{SpecVersion: "1.0", ID: "observation-12", Source: "grain://task-observer", Type: "task.observed"})
	if err != nil {
		t.Fatalf("Reduce: %v", err)
	}
	if string(result.State) != `{"phase":"done"}` {
		t.Errorf("state = %s", result.State)
	}

	served := sandboxes.served()
	if len(served) != 1 {
		t.Fatalf("%d sandboxes served", len(served))
	}
	if served[0].Token != "sealed-token" || served[0].Image != "grain-lifecycle-guest:abc" {
		t.Errorf("serve request = %+v", served[0])
	}
	if served[0].Version != version {
		t.Errorf("served %s for %s", served[0].Version, version)
	}
}

// One serving sandbox per (repo, version), shared by every task of that
// repository on that version -- so a burst of observations does not each
// start a VM.
func TestOneSandboxPerVersionEvenUnderABurst(t *testing.T) {
	sandboxes := &fakeSandboxes{server: definition(t, nil)}
	r := registry(t, &fakeDeclarations{declared: declaration(), commit: "c0ffee"},
		&fakeImages{digest: testDigest, local: "local:abc"}, sandboxes)
	version, err := r.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Reducer(context.Background(), version.String()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Reducer: %v", err)
	}
	if got := len(sandboxes.served()); got != 1 {
		t.Errorf("%d sandboxes started for one version", got)
	}
}

// A version this host has not prepared is a refusal rather than a
// silently re-resolved "whatever the repository declares now": an
// in-flight task keeps its digest across releases, and serving it the new
// definition would be changing a machine's rules underneath it.
func TestReducerRefusesAVersionThisHostHasNotPrepared(t *testing.T) {
	sandboxes := &fakeSandboxes{server: definition(t, nil)}
	r := registry(t, &fakeDeclarations{declared: declaration(), commit: "c0ffee"},
		&fakeImages{digest: testDigest, local: "local:abc"}, sandboxes)
	if _, err := r.Resolve(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	_, err := r.Reducer(context.Background(), "acme/billing@sha256:"+strings.Repeat("b", 64))
	if err == nil {
		t.Fatal("an unprepared version was served")
	}
	if !strings.Contains(err.Error(), "keeps its digest") {
		t.Errorf("%v", err)
	}
}

func TestReducerRefusesAVersionItCannotParse(t *testing.T) {
	r := registry(t, &fakeDeclarations{}, &fakeImages{}, &fakeSandboxes{})
	for _, version := range []string{"", "acme/billing", "acme/billing@", "@sha256:x", "acme/billing@latest"} {
		if _, err := r.Reducer(context.Background(), version); err == nil {
			t.Errorf("%q: accepted", version)
		}
	}
}

// A definition that never becomes ready is a lifecycle error naming the
// timeout, and the sandbox is given back rather than left holding a VM.
func TestAReadyTimeoutIsALifecycleFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"protocol":"1","ready":false,"definition":"starting"}`)
	}))
	t.Cleanup(server.Close)
	declared := declaration()
	declared.Serve.ReadyTimeout = 300 * time.Millisecond
	sandboxes := &fakeSandboxes{server: server}
	r := registry(t, &fakeDeclarations{declared: declared, commit: "c0ffee"},
		&fakeImages{digest: testDigest, local: "local:abc"}, sandboxes)
	version, err := r.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reducer(context.Background(), version.String()); err == nil {
		t.Fatal("a definition that never answered ready was served")
	} else if !strings.Contains(err.Error(), "ready-timeout-seconds") {
		t.Errorf("%v does not name the timeout", err)
	}
	if sandboxes.releases() != 1 {
		t.Errorf("%d releases -- a sandbox that never became ready was left holding a VM", sandboxes.releases())
	}
	if status := r.Status(context.Background(), repo); status.State != model.LifecycleFailed {
		t.Errorf("state = %q", status.State)
	}
}

// A server answering a contract version this grain does not speak will
// not start speaking it, so readiness fails at once rather than polling
// out the whole timeout.
func TestAProtocolMismatchFailsImmediately(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"protocol":"7","ready":true}`)
	}))
	t.Cleanup(server.Close)
	declared := declaration()
	declared.Serve.ReadyTimeout = 30 * time.Second
	r := registry(t, &fakeDeclarations{declared: declared, commit: "c0ffee"},
		&fakeImages{digest: testDigest, local: "local:abc"}, &fakeSandboxes{server: server})
	version, err := r.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := r.Reducer(context.Background(), version.String()); err == nil {
		t.Fatal("a definition speaking another protocol was served")
	} else if !strings.Contains(err.Error(), "protocol") {
		t.Errorf("%v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("it polled for %s against a mismatch that will never change", elapsed)
	}
}

// Released after an idle TTL, and started again on the next reduce.
func TestSweepReleasesAnIdleSandbox(t *testing.T) {
	sandboxes := &fakeSandboxes{server: definition(t, nil)}
	now := time.Now()
	r := registry(t, &fakeDeclarations{declared: declaration(), commit: "c0ffee"},
		&fakeImages{digest: testDigest, local: "local:abc"}, sandboxes)
	r.IdleTTL = 15 * time.Minute
	r.Now = func() time.Time { return now }

	version, err := r.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reducer(context.Background(), version.String()); err != nil {
		t.Fatal(err)
	}

	r.Sweep(context.Background())
	if sandboxes.releases() != 0 {
		t.Fatal("a sandbox in use was released")
	}
	now = now.Add(20 * time.Minute)
	r.Sweep(context.Background())
	if sandboxes.releases() != 1 {
		t.Fatalf("%d releases after the TTL", sandboxes.releases())
	}
	if _, err := r.Reducer(context.Background(), version.String()); err != nil {
		t.Fatalf("the next reduce did not start it again: %v", err)
	}
	if got := len(sandboxes.served()); got != 2 {
		t.Errorf("%d sandboxes started in total", got)
	}
}

type fakeCensus struct {
	pins Pins
	err  error
}

func (f *fakeCensus) Pinned(context.Context, model.RepoRef, string) (Pins, error) {
	return f.pins, f.err
}

// The hold: an instance that is active with attention set is precisely
// the state a person is about to act on, so it keeps its sandbox however
// idle the link has been.
func TestSweepHoldsASandboxSomebodyIsAboutToActOn(t *testing.T) {
	sandboxes := &fakeSandboxes{server: definition(t, nil)}
	now := time.Now()
	census := &fakeCensus{pins: Pins{Here: 3, Attention: 1}}
	r := registry(t, &fakeDeclarations{declared: declaration(), commit: "c0ffee"},
		&fakeImages{digest: testDigest, local: "local:abc"}, sandboxes)
	r.Census, r.IdleTTL, r.Now = census, time.Minute, func() time.Time { return now }

	version, err := r.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reducer(context.Background(), version.String()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)

	r.Sweep(context.Background())
	if sandboxes.releases() != 0 {
		t.Fatal("a sandbox somebody is about to act on was released")
	}
	// Not knowing is also a reason to keep it: minutes of one VM against
	// a cold start somebody watches.
	census.pins, census.err = Pins{}, errors.New("store is busy")
	r.Sweep(context.Background())
	if sandboxes.releases() != 0 {
		t.Fatal("a sandbox was released on a census that could not answer")
	}
	census.err = nil
	r.Sweep(context.Background())
	if sandboxes.releases() != 1 {
		t.Fatalf("%d releases once nothing held it", sandboxes.releases())
	}
}

// What an operator wants after a release: how much of the repository has
// moved to the version just published.
func TestStatusCountsWhatIsPinnedWhere(t *testing.T) {
	r := registry(t, &fakeDeclarations{declared: declaration(), commit: "c0ffee"},
		&fakeImages{digest: testDigest, local: "local:abc"}, &fakeSandboxes{server: definition(t, nil)})
	r.Census = &fakeCensus{pins: Pins{Here: 14, Older: 3}}
	if _, err := r.Resolve(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	status := r.Status(context.Background(), repo)
	if status.Instances.PinnedHere != 14 || status.Instances.OnOlderVersions != 3 {
		t.Errorf("instances = %+v", status.Instances)
	}
	if status.Sandbox != nil {
		t.Errorf("a sandbox nothing has started: %+v", status.Sandbox)
	}
}

// Prewarm is the answer to cold start falling on the first event after a
// quiet period -- very often a person clicking a button and watching.
func TestPrewarmStartsTheSandboxBeforeAnythingNeedsIt(t *testing.T) {
	sandboxes := &fakeSandboxes{server: definition(t, nil)}
	r := registry(t, &fakeDeclarations{declared: declaration(), commit: "c0ffee"},
		&fakeImages{digest: testDigest, local: "local:abc"}, sandboxes)
	if err := r.Prewarm(context.Background(), repo); err != nil {
		t.Fatalf("Prewarm: %v", err)
	}
	if len(sandboxes.served()) != 1 {
		t.Fatalf("%d sandboxes", len(sandboxes.served()))
	}
	status := r.Status(context.Background(), repo)
	if status.Sandbox == nil || status.Sandbox.State != "serving" {
		t.Errorf("sandbox = %+v", status.Sandbox)
	}
	// A repository with no declaration has nothing to warm, and that is
	// not a failure to dispatch over.
	r2 := registry(t, &fakeDeclarations{}, &fakeImages{}, &fakeSandboxes{})
	if err := r2.Prewarm(context.Background(), repo); err != nil {
		t.Errorf("Prewarm with no declaration: %v", err)
	}
}

func TestSQLCensusCountsPins(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "census.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := statemachine.NewSQLStore(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}

	census := NewSQLCensus(db)
	rRef := model.RepoRef{Owner: "acme", Name: "billing"}
	versionStr := "acme/billing@sha256:target"

	// Add instances:
	// 1: matching version, active, attention=true
	// 2: matching version, active, attention=false
	// 3: matching version, succeeded, attention=true (not active)
	// 4: older version of acme/billing, active, attention=true
	// 5: another repo's instance
	if err := store.Create(ctx, statemachine.Instance{
		ID: "1", Definition: versionStr, State: json.RawMessage(`{}`), Lifecycle: "active", Attention: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, statemachine.Instance{
		ID: "2", Definition: versionStr, State: json.RawMessage(`{}`), Lifecycle: "active", Attention: false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, statemachine.Instance{
		ID: "3", Definition: versionStr, State: json.RawMessage(`{}`), Lifecycle: "succeeded", Attention: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, statemachine.Instance{
		ID: "4", Definition: "acme/billing@sha256:older", State: json.RawMessage(`{}`), Lifecycle: "active", Attention: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, statemachine.Instance{
		ID: "5", Definition: "other/repo@sha256:target", State: json.RawMessage(`{}`), Lifecycle: "active", Attention: true,
	}); err != nil {
		t.Fatal(err)
	}

	pins, err := census.Pinned(ctx, rRef, versionStr)
	if err != nil {
		t.Fatalf("Pinned: %v", err)
	}
	if pins.Here != 3 {
		t.Errorf("pins.Here = %d, want 3", pins.Here)
	}
	if pins.Older != 1 {
		t.Errorf("pins.Older = %d, want 1", pins.Older)
	}
	if pins.Attention != 1 {
		t.Errorf("pins.Attention = %d, want 1", pins.Attention)
	}
}

// A version on a repository off the default git host reads back with its
// host, equal to the Version that wrote it.
func TestParseVersionHostQualifiedRoundTrip(t *testing.T) {
	r := model.RepoRef{Host: "git.example.com", Owner: "acme", Name: "app"}
	for _, v := range []Version{{Repo: r, Commit: "abc123"}, {Repo: r, Digest: testDigest}} {
		back, err := ParseVersion(v.String())
		if err != nil || back != v || !back.Repo.Same(r) {
			t.Errorf("ParseVersion(%q) = %+v, %v; want %+v", v.String(), back, err, v)
		}
	}
	for _, s := range []string{"acme@commit:abc", "a/b/c/d@commit:abc", "acme/app", "acme/app@"} {
		if v, err := ParseVersion(s); err == nil {
			t.Errorf("ParseVersion(%q) = %+v; want an error", s, v)
		}
	}
}
