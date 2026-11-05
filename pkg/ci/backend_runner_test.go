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

package ci

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/attest"
	"github.com/ai-on-gke/grain/pkg/grit"
	"github.com/ai-on-gke/grain/pkg/gritapi"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
	"github.com/ai-on-gke/grain/pkg/treeid"
)

func buildTestTree(t *testing.T, files map[string]string) (treeid.ID, []byte) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
			Format:   tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	sum := sha256.Sum256(raw)
	gitSum := sha256.Sum256(append([]byte("git-tree:"), raw...))
	return treeid.ID{
		GitTree: hex.EncodeToString(gitSum[:20]),
		SHA256:  hex.EncodeToString(sum[:]),
	}, raw
}

type backendTestEnv struct {
	local   *grit.Local
	srv     *httptest.Server
	backend gritapi.Backend
	token   string
}

func (e *backendTestEnv) close() {
	if e.srv != nil {
		e.srv.Close()
	}
	if e.local != nil {
		_ = e.local.Close()
	}
}

func newBackendTestEnv(t *testing.T, mode, gritDir string, signer attest.Signer, token string, jobRunner grit.JobRunner) *backendTestEnv {
	t.Helper()
	pkgAdmin, err := pkgcache.OpenAdmin(filepath.Join(gritDir, "pkgcache"), 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	loc, err := grit.NewLocal(grit.LocalConfig{
		DataDir:       gritDir,
		Executor:      "local",
		Concurrency:   4,
		Signer:        signer,
		PkgCacheAdmin: pkgAdmin,
		JobRunner:     jobRunner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		var hash string
		token, hash, err = grit.GenerateClientToken("grain-ci")
		if err != nil {
			t.Fatal(err)
		}
		if err := loc.Store().CreateOrUpdateClient(grit.ClientRecord{
			ID:           "grain-ci",
			SecretSHA256: hash,
			Scopes: []grit.ClientScope{
				{Scope: gritapi.ScopeSubmit, Repo: "*"},
				{Scope: gritapi.ScopeRead, Repo: "*"},
				{Scope: gritapi.ScopeCancel, Repo: "*"},
				{Scope: gritapi.ScopeCacheAdmin, Repo: "*"},
				{Scope: gritapi.ScopeRelease, Repo: "*"},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	env := &backendTestEnv{
		local: loc,
		token: token,
	}
	if mode == "local" {
		env.backend = loc
	} else {
		srv := httptest.NewServer(grit.NewServer(grit.ServerConfig{Local: loc}))
		env.srv = srv
		env.backend = gritapi.NewClient(gritapi.ClientConfig{
			BaseURL:     srv.URL,
			ClientToken: token,
		})
	}
	return env
}

func waitRunnerRunDone(t *testing.T, r *BackendRunner, runID string) Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_ = r.SyncOnce(ctx, 10*time.Millisecond, 100)
		if run, ok := r.Get(runID); ok && run.Conclusion.Done() {
			return run
		}
		select {
		case <-ctx.Done():
			run, _ := r.Get(runID)
			t.Fatalf("timed out waiting for runner run %s to conclude (last=%+v)", runID, run)
		case <-time.After(15 * time.Millisecond):
		}
	}
}

func TestBackendRunnerContract(t *testing.T) {
	for _, mode := range []string{"local", "remote"} {
		t.Run(mode, func(t *testing.T) {
			t.Run("SubmitWithTreeSecretsAndVerifyAttestation", func(t *testing.T) {
				signer, err := attest.LoadOrCreateFileSigner(filepath.Join(t.TempDir(), "attest.key"))
				if err != nil {
					t.Fatal(err)
				}
				gritDir := t.TempDir()
				env := newBackendTestEnv(t, mode, gritDir, signer, "", func(ctx context.Context, jenv grit.JobExecEnv) (grit.JobExecResult, error) {
					msg, _ := os.ReadFile(filepath.Join(jenv.WorkDir, "msg.txt"))
					return grit.JobExecResult{
						Conclusion: "success",
						Log:        []byte("msg=" + string(msg) + " env=" + strings.Join(jenv.Env, ",")),
					}, nil
				})
				defer env.close()

				treeID, archive := buildTestTree(t, map[string]string{"msg.txt": "hello-grit"})
				stateDir := t.TempDir()
				var verifiedRuns []Run
				runner := NewBackendRunner(BackendRunnerConfig{
					Backend:  env.backend,
					StateDir: stateDir,
					TreeResolver: func(ctx context.Context, req Request) (treeid.ID, []byte, error) {
						return treeID, archive, nil
					},
					SecretResolver: func(ctx context.Context, req Request) (gritapi.SecretPayload, error) {
						raw := []byte("cap-secret-token-xyz")
						sum := sha256.Sum256(raw)
						h := hex.EncodeToString(sum[:])
						return gritapi.SecretPayload{
							Manifest: []gritapi.SecretManifestEntry{
								{Capability: "test-cap", Kind: "env", Target: "CAP_TOKEN", SHA256: h},
							},
							Blobs: map[string][]byte{h: raw},
						}, nil
					},
					OnVerdictVerified: func(run Run) {
						verifiedRuns = append(verifiedRuns, run)
					},
				})

				ctx := context.Background()
				run, err := runner.Start(ctx, Request{
					Repo:   "ai-on-gke/grain",
					Ref:    "grain/task-1496",
					Commit: "1111111111111111111111111111111111111111",
					Event:  EventPush,
					Jobs:   []string{"go"},
				})
				if err != nil {
					t.Fatalf("runner.Start: %v", err)
				}

				done := waitRunnerRunDone(t, runner, run.ID)
				if done.Conclusion != ConclusionSuccess {
					t.Fatalf("done.Conclusion = %q (err=%q), want success", done.Conclusion, done.Error)
				}
				if len(verifiedRuns) != 1 || verifiedRuns[0].ID != run.ID {
					t.Fatalf("OnVerdictVerified = %+v, want 1 verified run %s", verifiedRuns, run.ID)
				}
				if len(done.Jobs) != 1 || len(done.Jobs[0].Steps) != 1 {
					t.Fatalf("done.Jobs = %+v, want 1 job with 1 step", done.Jobs)
				}
				out := done.Jobs[0].Steps[0].Output
				if !strings.Contains(out, "hello-grit") || strings.Contains(out, "cap-secret-token-xyz") || !strings.Contains(out, "[REDACTED]") {
					t.Fatalf("unexpected step output: %q", out)
				}
			})

			t.Run("AttestationForAnotherTreeFailsVerificationAndNeverRecordsGreen", func(t *testing.T) {
				signer, err := attest.LoadOrCreateFileSigner(filepath.Join(t.TempDir(), "attest.key"))
				if err != nil {
					t.Fatal(err)
				}
				gritDir := t.TempDir()
				env := newBackendTestEnv(t, mode, gritDir, signer, "", func(ctx context.Context, jenv grit.JobExecEnv) (grit.JobExecResult, error) {
					return grit.JobExecResult{
						Conclusion: "success",
						Log:        []byte("ok"),
					}, nil
				})
				defer env.close()

				treeA, archiveA := buildTestTree(t, map[string]string{"a.txt": "tree-a"})
				treeB, _ := buildTestTree(t, map[string]string{"b.txt": "tree-b"})

				var verifiedRuns []Run
				runner := NewBackendRunner(BackendRunnerConfig{
					Backend:  env.backend,
					StateDir: t.TempDir(),
					TreeResolver: func(ctx context.Context, req Request) (treeid.ID, []byte, error) {
						return treeA, archiveA, nil
					},
					OnVerdictVerified: func(run Run) {
						verifiedRuns = append(verifiedRuns, run)
					},
				})

				ctx := context.Background()
				run, err := runner.Start(ctx, Request{
					Repo:   "ai-on-gke/grain",
					Ref:    "grain/task-1496",
					Commit: "2222222222222222222222222222222222222222",
					Event:  EventPush,
					Jobs:   []string{"go"},
				})
				if err != nil {
					t.Fatalf("runner.Start: %v", err)
				}

				// Override the expected tree on the runner to treeB so that the
				// attestation signed by grit for treeA does not match treeB.
				runner.OverrideExpectedTree(run.ID, treeB)

				done := waitRunnerRunDone(t, runner, run.ID)
				if done.Conclusion != ConclusionErrored || !strings.Contains(done.Error, "attestation verification failed") {
					t.Fatalf("done = (conclusion=%q, error=%q), want errored with attestation verification failed", done.Conclusion, done.Error)
				}
				if len(verifiedRuns) != 0 {
					t.Fatalf("OnVerdictVerified was called for mismatched tree attestation: %+v", verifiedRuns)
				}
			})

			t.Run("KillAndRestartEitherSideLosesNoVerdict", func(t *testing.T) {
				signer, err := attest.LoadOrCreateFileSigner(filepath.Join(t.TempDir(), "attest.key"))
				if err != nil {
					t.Fatal(err)
				}
				gritDir := t.TempDir()
				stateDir := t.TempDir()

				jobStarted := make(chan struct{})
				var once sync.Once
				blockFirst := make(chan struct{})
				defer close(blockFirst)

				env1 := newBackendTestEnv(t, mode, gritDir, signer, "", func(ctx context.Context, jenv grit.JobExecEnv) (grit.JobExecResult, error) {
					once.Do(func() { close(jobStarted) })
					select {
					case <-ctx.Done():
						return grit.JobExecResult{}, ctx.Err()
					case <-blockFirst:
						return grit.JobExecResult{Conclusion: "success"}, nil
					}
				})

				treeID, archive := buildTestTree(t, map[string]string{"f.txt": "v1"})
				runner1 := NewBackendRunner(BackendRunnerConfig{
					Backend:  env1.backend,
					StateDir: stateDir,
					TreeResolver: func(ctx context.Context, req Request) (treeid.ID, []byte, error) {
						return treeID, archive, nil
					},
				})

				ctx := context.Background()
				run, err := runner1.Start(ctx, Request{
					Repo:   "ai-on-gke/grain",
					Ref:    "main",
					Commit: "3333333333333333333333333333333333333333",
					Event:  EventPush,
					Jobs:   []string{"go"},
				})
				if err != nil {
					t.Fatalf("runner1.Start: %v", err)
				}

				<-jobStarted
				// Sync runner1 once while the run is in flight, then kill both grit and runner1.
				_ = runner1.SyncOnce(ctx, 0, 50)
				savedToken := env1.token
				env1.close()

				// Restart grit over the same gritDir and a new BackendRunner over the same stateDir.
				env2 := newBackendTestEnv(t, mode, gritDir, signer, savedToken, func(ctx context.Context, jenv grit.JobExecEnv) (grit.JobExecResult, error) {
					return grit.JobExecResult{
						Conclusion: "success",
						Log:        []byte("resumed-after-restart"),
					}, nil
				})
				defer env2.close()

				runner2 := NewBackendRunner(BackendRunnerConfig{
					Backend:  env2.backend,
					StateDir: stateDir,
				})

				done := waitRunnerRunDone(t, runner2, run.ID)
				if done.Conclusion != ConclusionSuccess {
					t.Fatalf("done after restart = (conclusion=%q, error=%q), want success", done.Conclusion, done.Error)
				}
			})

			t.Run("CursorGone410ResyncReconcilesAndVerifiesAttestation", func(t *testing.T) {
				signer, err := attest.LoadOrCreateFileSigner(filepath.Join(t.TempDir(), "attest.key"))
				if err != nil {
					t.Fatal(err)
				}
				gritDir := t.TempDir()
				env := newBackendTestEnv(t, mode, gritDir, signer, "", func(ctx context.Context, jenv grit.JobExecEnv) (grit.JobExecResult, error) {
					return grit.JobExecResult{
						Conclusion: "success",
						Log:        []byte("ok-410"),
					}, nil
				})
				defer env.close()

				treeID, archive := buildTestTree(t, map[string]string{"f.txt": "410"})
				stateDir := t.TempDir()
				runner := NewBackendRunner(BackendRunnerConfig{
					Backend:  env.backend,
					StateDir: stateDir,
					TreeResolver: func(ctx context.Context, req Request) (treeid.ID, []byte, error) {
						return treeID, archive, nil
					},
				})

				ctx := context.Background()
				// Seed the cursor at seq=1, then run a job to completion and prune events <= 3
				// so the next SyncOnce encounters 410 cursor_gone.
				run, err := runner.Start(ctx, Request{
					Repo:   "ai-on-gke/grain",
					Ref:    "main",
					Commit: "4444444444444444444444444444444444444444",
					Event:  EventPush,
					Jobs:   []string{"go"},
				})
				if err != nil {
					t.Fatalf("runner.Start: %v", err)
				}

				// Wait for grit to finish the run before runner consumes events.
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					rs, gErr := env.backend.GetRun(ctx, run.ID)
					if gErr == nil && rs.Status == "completed" {
						break
					}
					time.Sleep(15 * time.Millisecond)
				}

				if err := runner.cursor.SaveCursor(env.local.Store().Epoch(), 1); err != nil {
					t.Fatal(err)
				}
				if err := env.local.Store().PruneEventsUpTo(3); err != nil {
					t.Fatal(err)
				}

				if err := runner.SyncOnce(ctx, 0, 50); err != nil {
					t.Fatalf("runner.SyncOnce on 410 cursor_gone: %v", err)
				}
				done, ok := runner.Get(run.ID)
				if !ok || done.Conclusion != ConclusionSuccess {
					t.Fatalf("run after 410 resync = (%+v, %v), want ConclusionSuccess", done, ok)
				}
			})
		})
	}
}

// greenWashingBackend reports every completed job and run as a success in
// its (unsigned) run status, leaving the signed attestation alone: what a
// buggy grit or anything between grain and grit could hand the runner.
type greenWashingBackend struct {
	gritapi.Backend
}

func (b greenWashingBackend) GetRun(ctx context.Context, id string) (gritapi.RunStatus, error) {
	rs, err := b.Backend.GetRun(ctx, id)
	if rs.Status == "completed" {
		rs.Conclusion = "success"
		for i := range rs.Jobs {
			rs.Jobs[i].Conclusion = "success"
		}
	}
	return rs, err
}

// TestBackendRunnerRecordsOnlyTheAttestedVerdict is the regression for the
// 2026-10-29 walkthrough's F2: the runner verified the attestation's
// signature and subject but recorded the conclusions from the unsigned run
// status, so a status saying "success" over an attestation signing
// "failure" was recorded green.
func TestBackendRunnerRecordsOnlyTheAttestedVerdict(t *testing.T) {
	for _, mode := range []string{"local", "remote"} {
		t.Run(mode, func(t *testing.T) {
			signer, err := attest.LoadOrCreateFileSigner(filepath.Join(t.TempDir(), "attest.key"))
			if err != nil {
				t.Fatal(err)
			}
			env := newBackendTestEnv(t, mode, t.TempDir(), signer, "", func(ctx context.Context, jenv grit.JobExecEnv) (grit.JobExecResult, error) {
				return grit.JobExecResult{Conclusion: "failure", Log: []byte("exit 1")}, nil
			})
			defer env.close()

			tree, archive := buildTestTree(t, map[string]string{"a.txt": "a"})
			var verified []Run
			runner := NewBackendRunner(BackendRunnerConfig{
				Backend:  greenWashingBackend{env.backend},
				StateDir: t.TempDir(),
				TreeResolver: func(ctx context.Context, req Request) (treeid.ID, []byte, error) {
					return tree, archive, nil
				},
				OnVerdictVerified: func(run Run) { verified = append(verified, run) },
			})
			run, err := runner.Start(context.Background(), Request{
				Repo:   "acme/widgets",
				Ref:    "main",
				Commit: "3333333333333333333333333333333333333333",
				Event:  EventPush,
				Jobs:   []string{"test"},
			})
			if err != nil {
				t.Fatal(err)
			}
			done := waitRunnerRunDone(t, runner, run.ID)
			if done.Conclusion == ConclusionSuccess || !strings.Contains(done.Error, "attestation signs") {
				t.Fatalf("done = (conclusion=%q, error=%q), want a refusal naming the attested conclusion", done.Conclusion, done.Error)
			}
			if len(verified) != 0 {
				t.Fatalf("OnVerdictVerified called for a status the attestation contradicts: %+v", verified)
			}
			for _, j := range done.Jobs {
				if j.Conclusion == ConclusionSuccess {
					t.Errorf("job %s recorded success from an unverified status", j.JobID)
				}
			}
		})
	}
}

// TestCheckAttestedVerdict covers the binding's other refusals: a status
// for another run, and a job set the attestation does not cover.
func TestCheckAttestedVerdict(t *testing.T) {
	stmt := &attest.Statement{Predicate: attest.RunPredicate{
		Request: attest.RequestInfo{Run: "r-1"},
		Jobs:    []attest.Job{{ID: "a", Result: attest.Result{Conclusion: "success"}}},
	}}
	ok := gritapi.RunStatus{ID: "r-1", Jobs: []gritapi.JobStatus{{ID: "a", Conclusion: "success"}}}
	if err := checkAttestedVerdict(stmt, ok); err != nil {
		t.Fatalf("matching status refused: %v", err)
	}
	for name, rs := range map[string]gritapi.RunStatus{
		"other run":   {ID: "r-2", Jobs: ok.Jobs},
		"extra job":   {ID: "r-1", Jobs: []gritapi.JobStatus{{ID: "a", Conclusion: "success"}, {ID: "b", Conclusion: "success"}}},
		"renamed job": {ID: "r-1", Jobs: []gritapi.JobStatus{{ID: "b", Conclusion: "success"}}},
		"conclusion":  {ID: "r-1", Jobs: []gritapi.JobStatus{{ID: "a", Conclusion: "failure"}}},
	} {
		if err := checkAttestedVerdict(stmt, rs); err == nil {
			t.Errorf("%s: accepted %+v", name, rs)
		}
	}
}

// flakyAttestationBackend fails the first `fails` attestation fetches with
// a 503, as grit does while it restarts behind a proxy.
type flakyAttestationBackend struct {
	gritapi.Backend
	mu    sync.Mutex
	fails int
}

func (b *flakyAttestationBackend) GetAttestation(ctx context.Context, id string) (*attest.Envelope, error) {
	b.mu.Lock()
	fail := b.fails > 0
	if fail {
		b.fails--
	}
	b.mu.Unlock()
	if fail {
		return nil, &gritapi.APIError{StatusCode: http.StatusServiceUnavailable, Code: "http_error"}
	}
	return b.Backend.GetAttestation(ctx, id)
}

// TestBackendRunnerRetriesATransientAttestationFetch is the regression for
// the 2026-10-29 walkthrough's F3: a 503 fetching the attestation was
// recorded as the run's verdict ("errored") for good.
func TestBackendRunnerRetriesATransientAttestationFetch(t *testing.T) {
	signer, err := attest.LoadOrCreateFileSigner(filepath.Join(t.TempDir(), "attest.key"))
	if err != nil {
		t.Fatal(err)
	}
	env := newBackendTestEnv(t, "remote", t.TempDir(), signer, "", func(ctx context.Context, jenv grit.JobExecEnv) (grit.JobExecResult, error) {
		return grit.JobExecResult{Conclusion: "success", Log: []byte("ok")}, nil
	})
	defer env.close()

	tree, archive := buildTestTree(t, map[string]string{"a.txt": "a"})
	backend := &flakyAttestationBackend{Backend: env.backend, fails: 3}
	runner := NewBackendRunner(BackendRunnerConfig{
		Backend:  backend,
		StateDir: t.TempDir(),
		TreeResolver: func(ctx context.Context, req Request) (treeid.ID, []byte, error) {
			return tree, archive, nil
		},
	})
	run, err := runner.Start(context.Background(), Request{
		Repo: "acme/widgets", Ref: "main", Commit: "4444444444444444444444444444444444444444",
		Event: EventPush, Jobs: []string{"go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitRunnerRunDone(t, runner, run.ID)
	if done.Conclusion != ConclusionSuccess || done.Error != "" {
		t.Fatalf("done = (conclusion=%q, error=%q), want success once the attestation could be fetched", done.Conclusion, done.Error)
	}
}

// swappableBackend lets a test replace the grit behind a runner, as an
// operator does by wiping grit's data directory and restarting it.
type swappableBackend struct {
	gritapi.Backend
}

// TestBackendRunnerConcludesARunGritLost is the regression for the
// 2026-10-29 walkthrough's F4: after grit's state was reset (new epoch,
// 404 for the old run id) the resync skipped the run, which stayed
// "running" in grain for ever.
func TestBackendRunnerConcludesARunGritLost(t *testing.T) {
	signer, err := attest.LoadOrCreateFileSigner(filepath.Join(t.TempDir(), "attest.key"))
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	env1 := newBackendTestEnv(t, "remote", t.TempDir(), signer, "", func(ctx context.Context, jenv grit.JobExecEnv) (grit.JobExecResult, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return grit.JobExecResult{Conclusion: "success"}, nil
	})
	defer env1.close()
	env2 := newBackendTestEnv(t, "remote", t.TempDir(), signer, "", nil)
	defer env2.close()

	tree, archive := buildTestTree(t, map[string]string{"a.txt": "a"})
	backend := &swappableBackend{Backend: env1.backend}
	runner := NewBackendRunner(BackendRunnerConfig{
		Backend:  backend,
		StateDir: t.TempDir(),
		TreeResolver: func(ctx context.Context, req Request) (treeid.ID, []byte, error) {
			return tree, archive, nil
		},
	})
	ctx := context.Background()
	run, err := runner.Start(ctx, Request{
		Repo: "acme/widgets", Ref: "main", Commit: "5555555555555555555555555555555555555555",
		Event: EventPush, Jobs: []string{"go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.SyncOnce(ctx, 10*time.Millisecond, 100); err != nil {
		t.Fatal(err)
	}
	if got, _ := runner.Get(run.ID); got.Conclusion.Done() {
		t.Fatalf("run concluded before grit was reset: %+v", got)
	}

	backend.Backend = env2.backend
	done := waitRunnerRunDone(t, runner, run.ID)
	if done.Conclusion != ConclusionErrored || !strings.Contains(done.Error, "no longer has run") {
		t.Fatalf("done = (conclusion=%q, error=%q), want errored naming the lost run", done.Conclusion, done.Error)
	}
}

// TestBackendRunnerConcludesARunLostWhileNoDaemonWatched covers F4's other
// half: grit was reset while grain was down, so the next daemon starts with
// a run in flight that no event or epoch change will ever name again.
func TestBackendRunnerConcludesARunLostWhileNoDaemonWatched(t *testing.T) {
	signer, err := attest.LoadOrCreateFileSigner(filepath.Join(t.TempDir(), "attest.key"))
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	env1 := newBackendTestEnv(t, "remote", t.TempDir(), signer, "", func(ctx context.Context, jenv grit.JobExecEnv) (grit.JobExecResult, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return grit.JobExecResult{Conclusion: "success"}, nil
	})
	defer env1.close()
	env2 := newBackendTestEnv(t, "remote", t.TempDir(), signer, "", nil)
	defer env2.close()

	tree, archive := buildTestTree(t, map[string]string{"a.txt": "a"})
	stateDir := t.TempDir()
	resolver := func(ctx context.Context, req Request) (treeid.ID, []byte, error) { return tree, archive, nil }
	runner1 := NewBackendRunner(BackendRunnerConfig{Backend: env1.backend, StateDir: stateDir, TreeResolver: resolver})
	run, err := runner1.Start(context.Background(), Request{
		Repo: "acme/widgets", Ref: "main", Commit: "6666666666666666666666666666666666666666",
		Event: EventPush, Jobs: []string{"go"},
	})
	if err != nil {
		t.Fatal(err)
	}

	runner2 := NewBackendRunner(BackendRunnerConfig{Backend: env2.backend, StateDir: stateDir, TreeResolver: resolver})
	done := waitRunnerRunDone(t, runner2, run.ID)
	if done.Conclusion != ConclusionErrored || !strings.Contains(done.Error, "no longer has run") {
		t.Fatalf("done = (conclusion=%q, error=%q), want errored naming the lost run", done.Conclusion, done.Error)
	}
}

// unreachableBackend fails every call that would reach the grit server
// while down is set, the way a gritapi.Client does when grit is not
// listening, and counts the runs actually started.
type unreachableBackend struct {
	gritapi.Backend
	mu      sync.Mutex
	down    bool
	reject  error
	started int
}

func (b *unreachableBackend) fault() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reject != nil {
		return b.reject
	}
	if b.down {
		return errors.New("Post http://grit/v1/trees:negotiate: EOF")
	}
	return nil
}

func (b *unreachableBackend) NegotiateTrees(ctx context.Context, sha256s []string) ([]string, error) {
	if err := b.fault(); err != nil {
		return nil, err
	}
	return b.Backend.NegotiateTrees(ctx, sha256s)
}

func (b *unreachableBackend) StartRun(ctx context.Context, req gritapi.StartRun) (gritapi.RunStatus, error) {
	if err := b.fault(); err != nil {
		return gritapi.RunStatus{}, err
	}
	rs, err := b.Backend.StartRun(ctx, req)
	if err == nil {
		b.mu.Lock()
		b.started++
		b.mu.Unlock()
	}
	return rs, err
}

// TestBackendRunnerQueuesAStartGritCannotTakeYet is the regression for the
// 2026-10-29 walkthrough's U4: with grit unreachable, Start answered the
// bare transport error (served as a 400) while the start stayed in the
// outbox and ran once grit was back, so a caller that retried a recheck got
// two runs. It now says the start is queued, and asking again joins it.
func TestBackendRunnerQueuesAStartGritCannotTakeYet(t *testing.T) {
	signer, err := attest.LoadOrCreateFileSigner(filepath.Join(t.TempDir(), "attest.key"))
	if err != nil {
		t.Fatal(err)
	}
	env := newBackendTestEnv(t, "remote", t.TempDir(), signer, "", func(ctx context.Context, jenv grit.JobExecEnv) (grit.JobExecResult, error) {
		return grit.JobExecResult{Conclusion: "success"}, nil
	})
	defer env.close()
	tree, archive := buildTestTree(t, map[string]string{"a.txt": "a"})
	backend := &unreachableBackend{Backend: env.backend, down: true}
	runner := NewBackendRunner(BackendRunnerConfig{
		Backend:  backend,
		StateDir: t.TempDir(),
		TreeResolver: func(ctx context.Context, req Request) (treeid.ID, []byte, error) {
			return tree, archive, nil
		},
	})
	ctx := context.Background()
	req := Request{
		Repo: "acme/widgets", Ref: "main", Commit: "6666666666666666666666666666666666666666",
		Event: EventPush, Jobs: []string{"go"}, Recheck: true,
	}

	_, err = runner.Start(ctx, req)
	var queued *StartQueuedError
	if !errors.As(err, &queued) || !errors.Is(err, ErrStartQueued) {
		t.Fatalf("Start with grit down = %v, want a StartQueuedError", err)
	}
	first := queued.RequestID
	if got := runner.PendingOutbox(); len(got) != 1 || got[0].RequestID != first {
		t.Fatalf("outbox = %+v, want the one queued start %s", got, first)
	}

	// The caller retries the recheck, which draws a fresh attempt.
	_, err = runner.Start(ctx, req)
	if !errors.As(err, &queued) || queued.RequestID != first {
		t.Fatalf("retried Start = %v, want the same queued request %s", err, first)
	}
	if got := runner.PendingOutbox(); len(got) != 1 {
		t.Fatalf("outbox after retry holds %d starts, want 1: %+v", len(got), got)
	}

	backend.mu.Lock()
	backend.down = false
	backend.mu.Unlock()
	run, err := runner.Start(ctx, req)
	if err != nil {
		t.Fatalf("Start once grit is back: %v", err)
	}
	if run.ID == "" {
		t.Fatal("Start once grit is back returned no run")
	}
	if got := runner.PendingOutbox(); len(got) != 0 {
		t.Fatalf("outbox once grit is back = %+v, want empty", got)
	}
	if backend.started != 1 {
		t.Fatalf("grit started %d runs, want 1", backend.started)
	}
	waitRunnerRunDone(t, runner, run.ID)
}

// TestBackendRunnerDropsAStartGritRefuses: a start grit answers with a 4xx
// it will give however often it is asked is reported as that refusal and
// not kept in the outbox, where it would be resent for ever and called
// "queued".
func TestBackendRunnerDropsAStartGritRefuses(t *testing.T) {
	env := newBackendTestEnv(t, "remote", t.TempDir(), nil, "", nil)
	defer env.close()
	tree, archive := buildTestTree(t, map[string]string{"a.txt": "a"})
	refusal := &gritapi.APIError{StatusCode: http.StatusBadRequest, Code: "bad_request", Message: "no such job"}
	backend := &unreachableBackend{Backend: env.backend, reject: refusal}
	runner := NewBackendRunner(BackendRunnerConfig{
		Backend:  backend,
		StateDir: t.TempDir(),
		TreeResolver: func(ctx context.Context, req Request) (treeid.ID, []byte, error) {
			return tree, archive, nil
		},
	})
	_, err := runner.Start(context.Background(), Request{
		Repo: "acme/widgets", Ref: "main", Commit: "7777777777777777777777777777777777777777",
		Event: EventPush, Jobs: []string{"go"},
	})
	if err == nil || errors.Is(err, ErrStartQueued) || !strings.Contains(err.Error(), "no such job") {
		t.Fatalf("Start refused by grit = %v, want the refusal itself", err)
	}
	if got := runner.PendingOutbox(); len(got) != 0 {
		t.Fatalf("outbox = %+v, want the refused start dropped", got)
	}
}

// TestBackendRunnerFlushKeepsEachQueuedEntryOnce: a failed delivery used to
// append the entries behind it to what stays queued twice over, so every
// flush against an unreachable grit doubled the outbox.
func TestBackendRunnerFlushKeepsEachQueuedEntryOnce(t *testing.T) {
	env := newBackendTestEnv(t, "remote", t.TempDir(), nil, "", nil)
	defer env.close()
	backend := &unreachableBackend{Backend: env.backend, down: true}
	runner := NewBackendRunner(BackendRunnerConfig{Backend: backend, StateDir: t.TempDir()})
	ctx := context.Background()
	for _, commit := range []string{"8888888888888888888888888888888888888888", "9999999999999999999999999999999999999999", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if _, err := runner.Start(ctx, Request{Repo: "acme/widgets", Ref: "main", Commit: commit, Event: EventPush, Jobs: []string{"go"}}); !errors.Is(err, ErrStartQueued) {
			t.Fatalf("Start %s = %v, want queued", commit, err)
		}
	}
	for i := 0; i < 3; i++ {
		_ = runner.FlushOutbox(ctx)
	}
	if got := runner.PendingOutbox(); len(got) != 3 {
		t.Fatalf("outbox holds %d entries after repeated flushes, want 3", len(got))
	}
}
