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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/sandboximage"
)

func timePtr(t time.Time) *time.Time {
	return &t
}

func testJobRun(name string, conclusion ci.Conclusion, output string) ci.JobRun {
	started := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	finished := started.Add(time.Minute)
	return ci.JobRun{
		Workflow:     "build",
		WorkflowPath: ".grain/workflows/ci.yml",
		JobID:        name,
		Name:         name,
		Conclusion:   conclusion,
		StartedAt:    timePtr(started),
		FinishedAt:   timePtr(finished),
		Steps:        []ci.StepRun{{Name: "run", Conclusion: conclusion, Output: output}},
	}
}

func TestCIJobCacheKeysOnSandboxImageWithoutWipingOnUpgrade(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	w, err := ci.Parse(".grain/workflows/ci.yml", []byte("on: push\njobs:\n  test:\n    steps:\n      - run: make test\n"))
	if err != nil {
		t.Fatalf("parsing workflow: %v", err)
	}
	job := w.Jobs[0]
	baseReq := ci.Request{Repo: "o/r", Ref: "main", Commit: "abc123", Event: ci.EventPush}

	digest1 := "sha256:6b2003bc1c0cfbb39c62a68421ab9fa9d4fc82183ab8f3775c4892cf54e02203"
	digest2 := "sha256:1111111111111111111111111111111111111111111111111111111111111111"

	cfg1 := Config{
		DataDir:          dataDir,
		GranuleSandboxes: true,
		SandboxImage:     "ghcr.io/bwsalmon/grain/guest:sha-66c5a15@" + digest1,
	}
	cache1 := CiJobCache(cfg1, nil)
	if cache1 == nil {
		t.Fatal("CiJobCache returned nil")
	}

	req1 := baseReq
	req1.SandboxImage = activeSandboxImageFor(cfg1)
	key1 := ci.Fingerprint(req1, w, job)
	passRes := testJobRun("test", ci.ConclusionSuccess, "ok")
	if err := cache1.Save(ctx, key1, passRes); err != nil {
		t.Fatalf("saving result in cache1: %v", err)
	}

	// An upgrade to a new daemon build whose guest tag moved (sha-dc5b5a1)
	// while the guest digest is unchanged must hit the existing cache entry
	// and must not clear the cache directory.
	cfgSameDigest := Config{
		DataDir:          dataDir,
		GranuleSandboxes: true,
		SandboxImage:     "ghcr.io/bwsalmon/grain/guest:sha-dc5b5a1@" + digest1,
	}
	cacheSameDigest := CiJobCache(cfgSameDigest, nil)
	reqSameDigest := baseReq
	reqSameDigest.SandboxImage = activeSandboxImageFor(cfgSameDigest)
	keySameDigest := ci.Fingerprint(reqSameDigest, w, job)
	if keySameDigest != key1 {
		t.Fatalf("digest-identical guest tag changed fingerprint: %s vs %s", keySameDigest, key1)
	}
	if _, ok, err := cacheSameDigest.Lookup(ctx, keySameDigest); !ok || err != nil {
		t.Fatalf("expected cache hit after daemon upgrade with identical guest digest: ok=%v err=%v", ok, err)
	}

	// An upgrade that changes the guest image digest misses for the new
	// image without wiping entries cached under the previous one.
	cfgNewDigest := Config{
		DataDir:          dataDir,
		GranuleSandboxes: true,
		SandboxImage:     "ghcr.io/bwsalmon/grain/guest:sha-9999999@" + digest2,
	}
	cacheNewDigest := CiJobCache(cfgNewDigest, nil)
	reqNewDigest := baseReq
	reqNewDigest.SandboxImage = activeSandboxImageFor(cfgNewDigest)
	keyNewDigest := ci.Fingerprint(reqNewDigest, w, job)
	if keyNewDigest == key1 {
		t.Fatalf("expected different fingerprint when guest digest changes: %s", keyNewDigest)
	}
	if _, ok, err := cacheNewDigest.Lookup(ctx, keyNewDigest); ok || err != nil {
		t.Fatalf("expected cache miss under new guest digest: ok=%v err=%v", ok, err)
	}
	if _, ok, err := cacheNewDigest.Lookup(ctx, key1); !ok || err != nil {
		t.Fatalf("expected previous entry to remain in cache rather than wiping whole directory: ok=%v err=%v", ok, err)
	}
}

func TestApplySandboxImageUpdatesLiveCISandboxImage(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	cfg := Config{
		DataDir:          dataDir,
		GranuleSandboxes: true,
		SandboxImage:     "",
	}

	cache := CiJobCache(cfg, nil)
	engine := &ci.Engine{Cache: cache}
	svc := &ci.Service{Engine: engine}
	LiveCI.Store(svc)
	defer LiveCI.Store(nil)

	src := &sandboximage.Source{}
	stock := stockSandboxImageFor(cfg)
	src.Set(stock, "")
	live := (&LiveConfig{}).WithSandboxImages(src, stock, nil)
	live.applySandboxImage(ctx, cfg)

	w, err := ci.Parse(".grain/workflows/ci.yml", []byte("on: push\njobs:\n  test:\n    steps:\n      - run: make test\n"))
	if err != nil {
		t.Fatalf("parsing workflow: %v", err)
	}
	job := w.Jobs[0]
	stockKey := ci.Fingerprint(ci.Request{
		Repo: "o/r", Ref: "main", Commit: "abc", Event: ci.EventPush,
		SandboxImage: svc.SandboxImage(),
	}, w, job)
	if err := cache.Save(ctx, stockKey, testJobRun("test", ci.ConclusionSuccess, "ok")); err != nil {
		t.Fatalf("saving stock result: %v", err)
	}

	cfg.CustomSandboxImage = "registry.example/team/guest:v1"
	live.applySandboxImage(ctx, cfg)

	if got := src.Current(); got != cfg.CustomSandboxImage {
		t.Fatalf("the next sandbox is created from %q, want the custom image %q", got, cfg.CustomSandboxImage)
	}
	if got := svc.SandboxImage(); got != cfg.CustomSandboxImage {
		t.Fatalf("CI service sandbox image = %q, want %q", got, cfg.CustomSandboxImage)
	}
	customKey := ci.Fingerprint(ci.Request{
		Repo: "o/r", Ref: "main", Commit: "abc", Event: ci.EventPush,
		SandboxImage: svc.SandboxImage(),
	}, w, job)
	if customKey == stockKey {
		t.Fatalf("custom sandbox image shared key with stock image: %s", customKey)
	}
	if _, ok, err := cache.Lookup(ctx, customKey); ok || err != nil {
		t.Fatalf("expected cache miss under new custom sandbox image: ok=%v err=%v", ok, err)
	}

	cfg.CustomSandboxImage = ""
	live.applySandboxImage(ctx, cfg)
	if got := src.Current(); got != stock {
		t.Fatalf("clearing the custom image left %q, want the stock image %q", got, stock)
	}
	if _, ok, err := cache.Lookup(ctx, stockKey); !ok || err != nil {
		t.Fatalf("expected stock entry still present after clearing custom image: ok=%v err=%v", ok, err)
	}
}

// fakeImageBackend is a sandbox backend with a replaceable image
// (orchestrator.SandboxImageSetter), and nothing else.
type fakeImageBackend struct {
	orchestrator.Sandboxes
	image string
}

func (f *fakeImageBackend) SetImage(image string) { f.image = image }
func (f *fakeImageBackend) CurrentImage() string  { return f.image }

func TestApplySandboxImageReachesAPodBackend(t *testing.T) {
	ctx := context.Background()
	cfg := Config{K8sSandboxes: true, K8sSandboxImage: "ghcr.io/bwsalmon/grain/k8s-sandbox:sha-1"}
	backend := &fakeImageBackend{image: cfg.K8sSandboxImage}
	live := (&LiveConfig{sandboxes: backend}).WithSandboxImages(nil, stockSandboxImageFor(cfg), nil)

	cfg.CustomSandboxImage = "registry.example/team/sandbox:v2"
	live.applySandboxImage(ctx, cfg)
	if backend.image != cfg.CustomSandboxImage {
		t.Fatalf("pod image = %q, want the custom image", backend.image)
	}
	cfg.CustomSandboxImage = ""
	live.applySandboxImage(ctx, cfg)
	if backend.image != "ghcr.io/bwsalmon/grain/k8s-sandbox:sha-1" {
		t.Fatalf("pod image = %q after clearing, want the stock one back", backend.image)
	}
}

func TestAnExplicitSandboxImageFlagWinsOverTheCustomImage(t *testing.T) {
	cfg := Config{GranuleSandboxes: true, SandboxImage: "mirror.example/grain/guest:pinned", CustomSandboxImage: "registry.example/team/guest:v1"}
	if got := customSandboxImageFor(cfg); got != "" {
		t.Errorf("granule with -sandbox-image: custom image %q in force, want the flag to win", got)
	}
	cfg = Config{ContainerSandboxes: true, ContainerImage: "mirror.example/sandbox:pinned", CustomSandboxImage: "registry.example/team/sandbox:v1"}
	if got := customSandboxImageFor(cfg); got != "" {
		t.Errorf("container with -container-sandbox-image: custom image %q in force, want the flag to win", got)
	}
	cfg.ContainerImage = DefaultK8sSandboxImage
	if got := customSandboxImageFor(cfg); got != cfg.CustomSandboxImage {
		t.Errorf("container on the default image: custom image %q, want %q", got, cfg.CustomSandboxImage)
	}
	cfg = Config{K8sGranuleSandboxes: true, CustomSandboxImage: "registry.example/team/guest:v1"}
	if got := customSandboxImageFor(cfg); got != cfg.CustomSandboxImage {
		t.Errorf("kubernetes-granule: custom image %q, want %q", got, cfg.CustomSandboxImage)
	}
	cfg = Config{HostSandboxes: true, CustomSandboxImage: "registry.example/team/guest:v1"}
	if got := customSandboxImageFor(cfg); got != "" {
		t.Errorf("host sandboxes have no image, but %q is in force", got)
	}
}

// The state repository's import does not go through Settings' validation,
// so a value docker would read as one of its own flags is ignored here
// rather than handed to `docker run` as the image.
func TestACustomSandboxImageThatIsNotOneReferenceIsIgnored(t *testing.T) {
	for _, bad := range []string{"--privileged", "-v=/:/host", "registry.example/a:v1\nregistry.example/b:v1"} {
		cfg := Config{ContainerSandboxes: true, ContainerImage: DefaultK8sSandboxImage, CustomSandboxImage: bad}
		if got := customSandboxImageFor(cfg); got != "" {
			t.Errorf("custom image %q in force, want it ignored", got)
		}
	}
}

type testCISource struct {
	workflows []ci.Workflow
	commit    string
}

func (s testCISource) Workflows(_ context.Context, _, _ string) ([]ci.Workflow, string, error) {
	return s.workflows, s.commit, nil
}

func setupTestCIRepo(t *testing.T, workflowYAML string) (bareRepoRoot, commitSHA string, source ci.Source) {
	t.Helper()
	bareRepoRoot = t.TempDir()
	bareRepo := filepath.Join(bareRepoRoot, "acme", "widgets.git")
	workTree := filepath.Join(t.TempDir(), "seed")
	setupScript := `set -e
mkdir -p "` + filepath.Dir(bareRepo) + `"
git init -q --bare "` + bareRepo + `"
git init -q "` + workTree + `"
cd "` + workTree + `"
git config user.email test@example.com
git config user.name test
mkdir -p .grain/workflows
cat > .grain/workflows/tests.yml <<'EOF'
` + workflowYAML + `
EOF
git add .grain/workflows/tests.yml
git commit -q -m "add workflow"
git branch -M main
git push -q "` + bareRepo + `" main
git rev-parse HEAD
`
	out, err := exec.Command("bash", "-c", setupScript).CombinedOutput()
	if err != nil {
		t.Fatalf("setting up test git repo: %v\n%s", err, string(out))
	}
	commitSHA = strings.TrimSpace(string(out))
	w, err := ci.Parse(".grain/workflows/tests.yml", []byte(workflowYAML))
	if err != nil {
		t.Fatalf("parsing test workflow: %v", err)
	}
	return bareRepoRoot, commitSHA, testCISource{workflows: []ci.Workflow{w}, commit: commitSHA}
}

func openTestStore(t *testing.T, dataDir string) (*model.Store, func()) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(dataDir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		_ = db.Close()
		t.Fatalf("applying schema: %v", err)
	}
	return store, func() { _ = db.Close() }
}

func newTestCiSandboxes(sb orchestrator.Sandboxes, bareRepoRoot string) CiSandboxes {
	return CiSandboxes{
		Sandboxes:     sb,
		GitRemoteBase: "file://localhost" + bareRepoRoot,
		MintToken:     func(string) (string, error) { return "test-token", nil },
		Scopes:        gitproxy.NewReadScopes(),
		WriteScopes:   gitproxy.NewWriteScopes(),
		Teams:         gitproxy.NewSandboxTeams(),
	}
}

func TestCIRunAndInFlightJobSurviveDaemonRestart(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	sandboxRoot := t.TempDir()

	workflowYAML := `name: tests
on: push
jobs:
  fast:
    steps:
      - name: quick
        run: echo fast-done
  slow:
    steps:
      - name: wait-for-flag
        run: |
          echo slow-started
          while [ ! -f "$HOME/release-slow" ]; do sleep 0.05; done
          echo slow-finished
  after:
    needs: [fast, slow]
    steps:
      - name: final
        run: echo after-done
`
	bareRepoRoot, commitSHA, source := setupTestCIRepo(t, workflowYAML)

	store1, closeStore1 := openTestStore(t, dataDir)
	defer closeStore1()

	runStore1 := ci.NewStoredRunStore(store1, ci.StoredRunStoreOptions{})
	cache1 := CiJobCache(Config{DataDir: dataDir}, nil)
	sb1 := orchestrator.NewHostSandboxes(sandboxRoot)
	ciSb1 := newTestCiSandboxes(sb1, bareRepoRoot)
	svc1 := &ci.Service{
		Source:  source,
		Engine:  &ci.Engine{Sandboxes: ciSb1, Cache: cache1},
		Runs:    runStore1,
		PlanDir: filepath.Join(dataDir, "state", "ci-plans"),
	}

	started, err := svc1.Start(ctx, ci.Request{
		Repo:   "acme/widgets",
		Ref:    "main",
		Commit: commitSHA,
		Event:  ci.EventPush,
	})
	if err != nil {
		t.Fatalf("starting CI run: %v", err)
	}

	var slowHome string
	deadline := time.Now().Add(15 * time.Second)
	for {
		if time.Now().After(deadline) {
			r, _ := svc1.Get(started.ID)
			t.Fatalf("timed out waiting for fast job to succeed and slow job to start; run=%+v", r)
		}
		r, ok := svc1.Get(started.ID)
		if ok {
			var fastDone, slowRunning bool
			for _, j := range r.Jobs {
				if j.JobID == "fast" && j.Conclusion == ci.ConclusionSuccess {
					fastDone = true
				}
				if j.JobID == "slow" && !j.Conclusion.Done() && j.StartedAt != nil && j.Sandbox != "" {
					slowHome = filepath.Join(sandboxRoot, j.Sandbox)
					if matches, _ := filepath.Glob(filepath.Join(slowHome, ".grain-ci-step", "*", "out")); len(matches) > 0 {
						slowRunning = true
					}
				}
			}
			if fastDone && slowRunning {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Simulate daemon shutdown / upgrade flush: detach from the active CI run
	// without killing the detached step runner inside the sandbox.
	svc1.Detach()
	closeStore1()

	// Start daemon 2 on the same dataDir and sandboxRoot.
	store2, closeStore2 := openTestStore(t, dataDir)
	defer closeStore2()

	runStore2 := ci.NewStoredRunStore(store2, ci.StoredRunStoreOptions{})
	cache2 := CiJobCache(Config{DataDir: dataDir}, nil)
	sb2 := orchestrator.NewHostSandboxes(sandboxRoot)
	ciSb2 := newTestCiSandboxes(sb2, bareRepoRoot)
	svc2 := &ci.Service{
		Source:  source,
		Engine:  &ci.Engine{Sandboxes: ciSb2, Cache: cache2},
		Runs:    runStore2,
		PlanDir: filepath.Join(dataDir, "state", "ci-plans"),
	}

	adopted, lost := AdoptCIRuns(ctx, svc2)
	if len(adopted) != 1 || len(lost) != 0 {
		t.Fatalf("AdoptCIRuns = (%+v, %+v), want 1 adopted and 0 lost", adopted, lost)
	}
	if adopted[0].Run != started.ID || adopted[0].Job != "slow" {
		t.Fatalf("adopted = %+v, want run %s job slow", adopted[0], started.ID)
	}

	// Let the slow step finish while daemon 2 is attached.
	if err := os.WriteFile(filepath.Join(slowHome, "release-slow"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("releasing slow step: %v", err)
	}

	deadline = time.Now().Add(15 * time.Second)
	for {
		r, ok := svc2.Get(started.ID)
		if ok && r.Conclusion.Done() {
			if r.Conclusion != ci.ConclusionSuccess {
				t.Fatalf("adopted CI run finished with %s, want success: %+v", r.Conclusion, r)
			}
			for _, j := range r.Jobs {
				if j.Conclusion != ci.ConclusionSuccess {
					t.Fatalf("job %s finished with %s, want success: %+v", j.JobID, j.Conclusion, j)
				}
				if j.JobID == "slow" && (!strings.Contains(j.Steps[0].Output, "slow-started") || !strings.Contains(j.Steps[0].Output, "slow-finished")) {
					t.Fatalf("slow job output missing pre- or post-restart lines: %q", j.Steps[0].Output)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			r, _ := svc2.Get(started.ID)
			t.Fatalf("timed out waiting for adopted CI run to complete; run=%+v", r)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestCIRunWithLostSandboxMarksJobLostAndRecoversAsInterrupted(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	sandboxRoot := t.TempDir()

	workflowYAML := `name: tests
on: push
jobs:
  slow:
    steps:
      - name: wait-for-flag
        run: |
          echo slow-started
          while [ ! -f "$HOME/release-slow" ]; do sleep 0.05; done
`
	bareRepoRoot, commitSHA, source := setupTestCIRepo(t, workflowYAML)

	store1, closeStore1 := openTestStore(t, dataDir)
	defer closeStore1()

	runStore1 := ci.NewStoredRunStore(store1, ci.StoredRunStoreOptions{})
	sb1 := orchestrator.NewHostSandboxes(sandboxRoot)
	ciSb1 := newTestCiSandboxes(sb1, bareRepoRoot)
	svc1 := &ci.Service{
		Source:  source,
		Engine:  &ci.Engine{Sandboxes: ciSb1},
		Runs:    runStore1,
		PlanDir: filepath.Join(dataDir, "state", "ci-plans"),
	}

	started, err := svc1.Start(ctx, ci.Request{
		Repo:   "acme/widgets",
		Ref:    "main",
		Commit: commitSHA,
		Event:  ci.EventPush,
	})
	if err != nil {
		t.Fatalf("starting CI run: %v", err)
	}

	var slowDir string
	deadline := time.Now().Add(15 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for slow job to start")
		}
		r, ok := svc1.Get(started.ID)
		if ok {
			for _, j := range r.Jobs {
				if j.JobID == "slow" && j.Sandbox != "" {
					slowDir = filepath.Join(sandboxRoot, j.Sandbox)
				}
			}
		}
		if slowDir != "" {
			if matches, _ := filepath.Glob(filepath.Join(slowDir, ".grain-ci-step", "*", "out")); len(matches) > 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	svc1.Detach()
	closeStore1()

	// Destroy the sandbox while the daemon is down so readoption fails.
	_ = os.RemoveAll(slowDir)

	store2, closeStore2 := openTestStore(t, dataDir)
	defer closeStore2()

	runStore2 := ci.NewStoredRunStore(store2, ci.StoredRunStoreOptions{})
	sb2 := orchestrator.NewHostSandboxes(sandboxRoot)
	ciSb2 := newTestCiSandboxes(sb2, bareRepoRoot)
	svc2 := &ci.Service{
		Source:  source,
		Engine:  &ci.Engine{Sandboxes: ciSb2},
		Runs:    runStore2,
		PlanDir: filepath.Join(dataDir, "state", "ci-plans"),
	}

	adopted, lost := AdoptCIRuns(ctx, svc2)
	if len(adopted) != 0 || len(lost) != 1 {
		t.Fatalf("AdoptCIRuns = (%+v, %+v), want 0 adopted and 1 lost", adopted, lost)
	}
	if lost[0].Run != started.ID || lost[0].Job != "slow" {
		t.Fatalf("lost = %+v, want run %s job slow", lost[0], started.ID)
	}

	r, ok := svc2.Get(started.ID)
	if !ok || !r.Interrupted || r.Conclusion != ci.ConclusionCancelled {
		t.Fatalf("run after failed adoption = %+v, want Interrupted=true Conclusion=cancelled", r)
	}
}

func TestK8sGranuleCISandboxesSurviveDaemonRestartAndSparePodFromReap(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	fakeKubeDir := t.TempDir()

	// Write a fake kubectl binary that simulates pod creation, status, exec
	// (via sh -c inside a pod-specific HOME directory), list, and delete.
	// IMPORTANT: K8sGranuleConfig.Workspace is set to "work" (relative) and
	// this script runs in "$HOME", so ciCheckoutScript's `rm -rf 'work'` and
	// `cd 'work'` operate exclusively on "$d/pod-$pod/work" inside t.TempDir().
	kubectlPath := filepath.Join(fakeKubeDir, "kubectl")
	kubectlScript := `#!/usr/bin/env bash
set -e
d="` + fakeKubeDir + `"
args=("$@")
for ((i=0; i<${#args[@]}; i++)); do
  case "${args[i]}" in
    apply|create)
      name=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["metadata"]["name"])')
      mkdir -p "$d/pods" "$d/pod-$name"
      echo "$name" > "$d/pods/$name"
      exit 0
      ;;
    get)
      sub="${args[i+1]}"
      if [ "$sub" = "pod" ]; then
        name="${args[i+2]}"
        if [ ! -f "$d/pods/$name" ]; then
          echo "Error from server (NotFound): pods \"$name\" not found" >&2
          exit 1
        fi
        joined="${args[*]}"
        if [[ "$joined" == *"DisruptionTarget"* ]] || [[ "$joined" == *"lastState.terminated"* ]]; then
          exit 0
        fi
        printf 'Running|True|ghcr.io/bwsalmon/grain/guest@sha256:6b2003bc1c0cfbb39c62a68421ab9fa9d4fc82183ab8f3775c4892cf54e02203|\n'
        exit 0
      elif [ "$sub" = "pods" ]; then
        for f in "$d/pods"/*; do
          [ -f "$f" ] || continue
          basename "$f"
        done
        exit 0
      fi
      ;;
    delete)
      name="${args[i+2]}"
      rm -f "$d/pods/$name"
      echo "$name" >> "$d/deleted"
      exit 0
      ;;
    exec)
      pod=""
      for ((j=i+1; j<${#args[@]}; j++)); do
        if [ "${args[j]}" = "--" ]; then
          pod="${args[j-1]}"
          shift $((j+1))
          break
        fi
      done
      if [ "$1" = "granule" ] && [ "$2" = "exec" ] && [ "$3" = "--" ]; then
        shift 3
      fi
      export HOME="$d/pod-$pod"
      mkdir -p "$HOME"
      cd "$HOME"
      exec "$@"
      ;;
  esac
done
exit 0
`
	if err := os.WriteFile(kubectlPath, []byte(kubectlScript), 0o755); err != nil {
		t.Fatalf("writing fake kubectl: %v", err)
	}

	workflowYAML := `name: tests
on: push
jobs:
  fast:
    steps:
      - name: quick
        run: echo fast-done
  slow:
    steps:
      - name: wait-for-flag
        run: |
          echo slow-started
          while [ ! -f "$HOME/release-slow" ]; do sleep 0.05; done
          echo slow-finished
  after:
    needs: [fast, slow]
    steps:
      - name: final
        run: echo after-done
`
	bareRepoRoot, commitSHA, source := setupTestCIRepo(t, workflowYAML)

	newK8sGranule := func() *orchestrator.K8sGranuleSandboxes {
		return orchestrator.NewK8sGranuleSandboxes(orchestrator.K8sGranuleConfig{
			Namespace:    "default",
			Kubectl:      kubectlPath,
			Image:        "ghcr.io/bwsalmon/grain/guest:sha-111@sha256:6b2003bc1c0cfbb39c62a68421ab9fa9d4fc82183ab8f3775c4892cf54e02203",
			ReadyTimeout: 5 * time.Second,
			Workspace:    ".",
		})
	}

	// Plant an unclaimed orphan pod to verify ReapOrphans deletes it while
	// sparing the readopted CI pod.
	if err := os.MkdirAll(filepath.Join(fakeKubeDir, "pods"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeKubeDir, "pods", "grain-granule-orphan"), []byte("grain-granule-orphan\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store1, closeStore1 := openTestStore(t, dataDir)
	defer closeStore1()

	runStore1 := ci.NewStoredRunStore(store1, ci.StoredRunStoreOptions{})
	cache1 := CiJobCache(Config{DataDir: dataDir}, nil)
	k8sSb1 := newK8sGranule()
	ciSb1 := newTestCiSandboxes(k8sSb1, bareRepoRoot)
	svc1 := &ci.Service{
		Source:  source,
		Engine:  &ci.Engine{Sandboxes: ciSb1, Cache: cache1},
		Runs:    runStore1,
		PlanDir: filepath.Join(dataDir, "state", "ci-plans"),
		SandboxImage: func() string {
			return "ghcr.io/bwsalmon/grain/guest:sha-111@sha256:6b2003bc1c0cfbb39c62a68421ab9fa9d4fc82183ab8f3775c4892cf54e02203"
		},
	}

	started, err := svc1.Start(ctx, ci.Request{
		Repo:   "acme/widgets",
		Ref:    "main",
		Commit: commitSHA,
		Event:  ci.EventPush,
	})
	if err != nil {
		t.Fatalf("starting CI run: %v", err)
	}

	var slowPod, slowPodHome string
	deadline := time.Now().Add(15 * time.Second)
	for {
		if time.Now().After(deadline) {
			r, _ := svc1.Get(started.ID)
			t.Fatalf("timed out waiting for slow pod step runner to start; run=%+v", r)
		}
		r, ok := svc1.Get(started.ID)
		if ok {
			var fastDone bool
			for _, j := range r.Jobs {
				if j.JobID == "fast" && j.Conclusion == ci.ConclusionSuccess {
					fastDone = true
				}
				if j.JobID == "slow" && j.Sandbox != "" {
					slowPod = "grain-granule-" + j.Sandbox
					slowPodHome = filepath.Join(fakeKubeDir, "pod-"+slowPod)
				}
			}
			if fastDone && slowPodHome != "" {
				if matches, _ := filepath.Glob(filepath.Join(slowPodHome, ".grain-ci-step", "*", "out")); len(matches) > 0 {
					break
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	svc1.Detach()
	closeStore1()

	// Daemon 2 starts up: AdoptCIRuns claims the active pod BEFORE ReapOrphans.
	store2, closeStore2 := openTestStore(t, dataDir)
	defer closeStore2()

	runStore2 := ci.NewStoredRunStore(store2, ci.StoredRunStoreOptions{})
	cache2 := CiJobCache(Config{DataDir: dataDir}, nil)
	k8sSb2 := newK8sGranule()
	ciSb2 := newTestCiSandboxes(k8sSb2, bareRepoRoot)
	svc2 := &ci.Service{
		Source:  source,
		Engine:  &ci.Engine{Sandboxes: ciSb2, Cache: cache2},
		Runs:    runStore2,
		PlanDir: filepath.Join(dataDir, "state", "ci-plans"),
		SandboxImage: func() string {
			return "ghcr.io/bwsalmon/grain/guest:sha-222@sha256:6b2003bc1c0cfbb39c62a68421ab9fa9d4fc82183ab8f3775c4892cf54e02203"
		},
	}

	adopted, lost := AdoptCIRuns(ctx, svc2)
	if len(adopted) != 1 || len(lost) != 0 {
		t.Fatalf("AdoptCIRuns = (%+v, %+v), want 1 adopted and 0 lost", adopted, lost)
	}

	reaped, err := k8sSb2.ReapOrphans(ctx)
	if err != nil {
		t.Fatalf("ReapOrphans error: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("ReapOrphans = %d, want 1 (only grain-granule-orphan, sparing %s)", reaped, slowPod)
	}
	if _, err := os.Stat(filepath.Join(fakeKubeDir, "pods", slowPod)); err != nil {
		t.Fatalf("active CI pod %s was reaped instead of spared: %v", slowPod, err)
	}

	if err := os.WriteFile(filepath.Join(slowPodHome, "release-slow"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("releasing slow step: %v", err)
	}

	deadline = time.Now().Add(15 * time.Second)
	for {
		r, ok := svc2.Get(started.ID)
		if ok && r.Conclusion.Done() {
			if r.Conclusion != ci.ConclusionSuccess {
				t.Fatalf("adopted CI run finished with %s, want success: %+v", r.Conclusion, r)
			}
			break
		}
		if time.Now().After(deadline) {
			r, _ := svc2.Get(started.ID)
			t.Fatalf("timed out waiting for adopted CI run to complete; run=%+v", r)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify that a subsequent run under Daemon 2 (where the guest tag moved from
	// :sha-111 to :sha-222 on the same @sha256 digest) hits the CI job cache for
	// every job without re-running any of them.
	started2, err := svc2.Start(ctx, ci.Request{
		Repo:   "acme/widgets",
		Ref:    "main",
		Commit: commitSHA,
		Event:  ci.EventPush,
	})
	if err != nil {
		t.Fatalf("starting second CI run after upgrade: %v", err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		r2, ok := svc2.Get(started2.ID)
		if ok && r2.Conclusion.Done() {
			if r2.Conclusion != ci.ConclusionSuccess {
				t.Fatalf("second CI run finished with %s, want success: %+v", r2.Conclusion, r2)
			}
			for _, j := range r2.Jobs {
				if !j.Cached {
					t.Fatalf("job %s was not served from cache after upgrade (Cached=false): %+v", j.JobID, j)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			r2, _ := svc2.Get(started2.ID)
			t.Fatalf("timed out waiting for cached second CI run; run=%+v", r2)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
