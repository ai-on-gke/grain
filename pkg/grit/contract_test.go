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

package grit

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/attest"
	"github.com/ai-on-gke/grain/pkg/gritapi"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
	"github.com/ai-on-gke/grain/pkg/treeid"
)

type memCursor struct {
	mu    sync.Mutex
	epoch string
	seq   int64
}

func (m *memCursor) LoadCursor() (string, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.epoch, m.seq, nil
}

func (m *memCursor) SaveCursor(epoch string, seq int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.epoch = epoch
	m.seq = seq
	return nil
}

type harness struct {
	name    string
	dataDir string
	signer  attest.Signer
	keys    attest.KeySet
	local   *Local
	srv     *httptest.Server
	backend gritapi.Backend
	client  *gritapi.Client
	cursor  *memCursor
	token   string
}

func (h *harness) close() {
	if h.srv != nil {
		h.srv.Close()
	}
	if h.local != nil {
		_ = h.local.Close()
	}
}

func newTestSigner(t *testing.T) (attest.Signer, attest.KeySet) {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), "attest.key")
	signer, err := attest.LoadOrCreateFileSigner(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	ks, err := attest.NewKeySet(map[string]crypto.PublicKey{
		signer.KeyID(): signer.Public(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return signer, ks
}

func makeSampleTree(t *testing.T, files map[string]string) (treeid.ID, []byte) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o755,
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

func startHarness(t *testing.T, mode string, dataDir string, signer attest.Signer, ks attest.KeySet, runner JobRunner, cursor *memCursor, token string) *harness {
	t.Helper()
	pkgAdmin, err := pkgcache.OpenAdmin(filepath.Join(dataDir, "pkgcache"), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	loc, err := NewLocal(LocalConfig{
		DataDir:       dataDir,
		Executor:      "local",
		Concurrency:   4,
		Signer:        signer,
		PkgCacheAdmin: pkgAdmin,
		JobRunner:     runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		var secretHash string
		token, secretHash, err = GenerateClientToken("grain-main")
		if err != nil {
			t.Fatal(err)
		}
		err = loc.Store().CreateOrUpdateClient(ClientRecord{
			ID:           "grain-main",
			SecretSHA256: secretHash,
			Scopes: []ClientScope{
				{Scope: gritapi.ScopeSubmit, Repo: "*"},
				{Scope: gritapi.ScopeRead, Repo: "*"},
				{Scope: gritapi.ScopeCancel, Repo: "*"},
				{Scope: gritapi.ScopeCacheAdmin, Repo: "*"},
				{Scope: gritapi.ScopeRelease, Repo: "*"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if cursor == nil {
		cursor = &memCursor{}
	}
	h := &harness{
		name:    mode,
		dataDir: dataDir,
		signer:  signer,
		keys:    ks,
		local:   loc,
		cursor:  cursor,
		token:   token,
	}
	if mode == "local" {
		h.backend = loc
	} else {
		srv := httptest.NewServer(NewServer(ServerConfig{Local: loc}))
		h.srv = srv
		h.client = gritapi.NewClient(gritapi.ClientConfig{
			BaseURL:     srv.URL,
			ClientToken: token,
			Cursor:      cursor,
		})
		h.backend = h.client
	}
	return h
}

func waitForRunCompleted(t *testing.T, b gritapi.Backend, runID string) gritapi.RunStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		rs, err := b.GetRun(ctx, runID)
		if err != nil {
			t.Fatalf("GetRun(%s): %v", runID, err)
		}
		if rs.Status == "completed" {
			return rs
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for run %s to complete (last status=%s)", runID, rs.Status)
		case <-time.After(15 * time.Millisecond):
		}
	}
}

// TestBackendContract runs the full GS-2 contract suite against both
// in-process `grit.Local` and `gritapi.Client` talking to `grit.Server`.
func TestBackendContract(t *testing.T) {
	for _, mode := range []string{"local", "remote"} {
		t.Run(mode, func(t *testing.T) {
			t.Run("TreeNegotiateUploadDedupeAndAttestation", func(t *testing.T) {
				signer, ks := newTestSigner(t)
				dataDir := t.TempDir()
				h := startHarness(t, mode, dataDir, signer, ks, func(ctx context.Context, env JobExecEnv) (JobExecResult, error) {
					greeting, _ := os.ReadFile(filepath.Join(env.WorkDir, "hello.txt"))
					return JobExecResult{
						Conclusion: "success",
						Log:        []byte("ran " + env.JobID + ": " + string(greeting) + " secret=" + strings.Join(env.Env, ",")),
						Tests: &attest.Tests{
							Passed: 1,
						},
						Artifacts: map[string][]byte{
							"report": []byte("artifact-bytes"),
						},
					}, nil
				}, nil, "")
				defer h.close()

				ctx := context.Background()
				info, err := h.backend.Info(ctx)
				if err != nil {
					t.Fatalf("Info: %v", err)
				}
				if info.Epoch == "" || info.Executor != "local" || !info.PackageCache || len(info.AttestationPublicKeys) != 1 {
					t.Fatalf("unexpected Info: %+v", info)
				}

				treeID, archive := makeSampleTree(t, map[string]string{"hello.txt": "world"})
				missing, err := h.backend.NegotiateTrees(ctx, []string{treeID.SHA256})
				if err != nil {
					t.Fatalf("NegotiateTrees: %v", err)
				}
				if len(missing) != 1 || missing[0] != treeID.SHA256 {
					t.Fatalf("NegotiateTrees missing = %v, want [%s]", missing, treeID.SHA256)
				}

				// Starting a run before uploading its tree must fail with 428 tree_missing.
				commitSHA := "0123456789abcdef0123456789abcdef01234567"
				startReq := gritapi.StartRun{
					Repo:   "bwsalmon/grain",
					Ref:    "grain/task-1330",
					Commit: commitSHA,
					Tree:   treeID,
					Event:  "push",
					Jobs:   []string{"go", "lint"},
				}
				startReq.RequestID = gritapi.ComputeRequestID(startReq)
				_, err = h.backend.StartRun(ctx, startReq)
				if missingSHA, ok := gritapi.IsTreeMissing(err); !ok || missingSHA != treeID.SHA256 {
					t.Fatalf("StartRun before tree upload = %v, want 428 tree_missing for %s", err, treeID.SHA256)
				}

				// Uploading tampered bytes must fail with 400 tree_digest_mismatch.
				if err := h.backend.UploadTree(ctx, treeID.SHA256, []byte("wrong")); err == nil {
					t.Fatal("UploadTree with mismatched digest succeeded, want error")
				}

				if err := h.backend.UploadTree(ctx, treeID.SHA256, archive); err != nil {
					t.Fatalf("UploadTree: %v", err)
				}
				missingAfter, err := h.backend.NegotiateTrees(ctx, []string{treeID.SHA256})
				if err != nil || len(missingAfter) != 0 {
					t.Fatalf("NegotiateTrees after upload = (%v, %v), want empty", missingAfter, err)
				}

				// Attach an env secret and verify it is redacted from the job log.
				secBytes := []byte("super-secret-value-123")
				secSum := sha256.Sum256(secBytes)
				secHash := hex.EncodeToString(secSum[:])
				startReq.Secrets = &gritapi.SecretPayload{
					Manifest: []gritapi.SecretManifestEntry{
						{Capability: "test-cap", Kind: "env", Target: "MY_SECRET", SHA256: secHash},
					},
					Blobs: map[string][]byte{secHash: secBytes},
				}

				run1, err := h.backend.StartRun(ctx, startReq)
				if err != nil {
					t.Fatalf("StartRun: %v", err)
				}
				// Duplicate submission with the same requestId returns the same run ID.
				run2, err := h.backend.StartRun(ctx, startReq)
				if err != nil {
					t.Fatalf("duplicate StartRun: %v", err)
				}
				if run1.ID != run2.ID {
					t.Fatalf("duplicate StartRun returned ID %s, want %s", run2.ID, run1.ID)
				}

				done := waitForRunCompleted(t, h.backend, run1.ID)
				if done.Conclusion != "success" || done.AttestationSHA256 == "" {
					t.Fatalf("completed run = %+v, want success with attestation", done)
				}

				// Log must contain the tree file content and have redacted the secret.
				logBytes, _, complete, err := h.backend.GetLog(ctx, run1.ID, "go", 0, 4096)
				if err != nil {
					t.Fatalf("GetLog: %v", err)
				}
				if !complete || !strings.Contains(string(logBytes), "world") {
					t.Fatalf("GetLog = %q (complete=%v), want greeting from tree", string(logBytes), complete)
				}
				if strings.Contains(string(logBytes), "super-secret-value-123") || !strings.Contains(string(logBytes), "[REDACTED]") {
					t.Fatalf("secret leaked in job log: %q", string(logBytes))
				}

				// Artifact must be retrievable.
				art, err := h.backend.GetArtifact(ctx, run1.ID, "go", "report")
				if err != nil || string(art) != "artifact-bytes" {
					t.Fatalf("GetArtifact = (%q, %v), want artifact-bytes", string(art), err)
				}

				// Signed DSSE attestation must verify against the KeySet and match commit/tree.
				env, err := h.backend.GetAttestation(ctx, run1.ID)
				if err != nil {
					t.Fatalf("GetAttestation: %v", err)
				}
				stmt, err := attest.Verify(env, ks)
				if err != nil {
					t.Fatalf("attest.Verify: %v", err)
				}
				if err := stmt.CheckSubject(attest.Tree{SHA256: treeID.SHA256, GitTree: treeID.GitTree}, commitSHA); err != nil {
					t.Fatalf("stmt.CheckSubject: %v", err)
				}
				if len(stmt.Predicate.Jobs) != 2 || stmt.Predicate.Jobs[0].Result.Conclusion != "success" {
					t.Fatalf("unexpected attestation predicate: %+v", stmt.Predicate)
				}

				// Event stream must contain run.queued, job.started, job.completed, run.attested, and run.completed.
				batch, err := h.backend.Events(ctx, 0, 0, 100)
				if err != nil {
					t.Fatalf("Events: %v", err)
				}
				kinds := map[gritapi.EventKind]int{}
				for _, ev := range batch.Events {
					kinds[ev.Kind]++
				}
				for _, wantKind := range []gritapi.EventKind{
					gritapi.EventRunQueued,
					gritapi.EventRunPlanned,
					gritapi.EventJobStarted,
					gritapi.EventJobCompleted,
					gritapi.EventRunAttested,
					gritapi.EventRunCompleted,
				} {
					if kinds[wantKind] == 0 {
						t.Errorf("event stream missing kind %s (got %v)", wantKind, kinds)
					}
				}
			})

			t.Run("KillAndRestartGritMidRunLosesNoVerdict", func(t *testing.T) {
				signer, ks := newTestSigner(t)
				dataDir := t.TempDir()

				jobStarted := make(chan struct{})
				var once sync.Once
				blockFirstInstance := make(chan struct{})

				h1 := startHarness(t, mode, dataDir, signer, ks, func(ctx context.Context, env JobExecEnv) (JobExecResult, error) {
					once.Do(func() { close(jobStarted) })
					select {
					case <-ctx.Done():
						return JobExecResult{Conclusion: "cancelled"}, ctx.Err()
					case <-blockFirstInstance:
						return JobExecResult{Conclusion: "success", Log: []byte("finished")}, nil
					}
				}, nil, "")

				ctx := context.Background()
				treeID, archive := makeSampleTree(t, map[string]string{"a.txt": "1"})
				if err := h1.backend.UploadTree(ctx, treeID.SHA256, archive); err != nil {
					t.Fatal(err)
				}
				req := gritapi.StartRun{
					Repo:   "bwsalmon/grain",
					Ref:    "main",
					Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					Tree:   treeID,
					Event:  "push",
					Jobs:   []string{"go"},
				}
				run, err := h1.backend.StartRun(ctx, req)
				if err != nil {
					t.Fatal(err)
				}

				// Wait until the job is actively running on h1, then kill h1 mid-run.
				<-jobStarted
				savedToken := h1.token
				savedCursor := h1.cursor
				h1.close()

				// Start a second grit instance over the same dataDir; it must resume
				// the incomplete run from SQLite, complete it, and emit the verdict.
				h2 := startHarness(t, mode, dataDir, signer, ks, func(ctx context.Context, env JobExecEnv) (JobExecResult, error) {
					return JobExecResult{
						Conclusion: "success",
						Log:        []byte("resumed and passed"),
					}, nil
				}, savedCursor, savedToken)
				defer h2.close()

				done := waitForRunCompleted(t, h2.backend, run.ID)
				if done.Conclusion != "success" || done.AttestationSHA256 == "" {
					t.Fatalf("resumed run after grit restart = %+v, want success with attestation", done)
				}
				env, err := h2.backend.GetAttestation(ctx, run.ID)
				if err != nil {
					t.Fatalf("GetAttestation after restart: %v", err)
				}
				if _, err := attest.Verify(env, ks); err != nil {
					t.Fatalf("Verify after restart: %v", err)
				}
			})

			t.Run("KillAndRestartClientAnd410CursorGoneResync", func(t *testing.T) {
				signer, ks := newTestSigner(t)
				dataDir := t.TempDir()
				h := startHarness(t, mode, dataDir, signer, ks, func(ctx context.Context, env JobExecEnv) (JobExecResult, error) {
					return JobExecResult{Conclusion: "success", Log: []byte("ok")}, nil
				}, nil, "")
				defer h.close()

				ctx := context.Background()
				treeID, archive := makeSampleTree(t, map[string]string{"a.txt": "1"})
				if err := h.backend.UploadTree(ctx, treeID.SHA256, archive); err != nil {
					t.Fatal(err)
				}
				req := gritapi.StartRun{
					Repo:   "bwsalmon/grain",
					Ref:    "main",
					Commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					Tree:   treeID,
					Event:  "push",
					Jobs:   []string{"go"},
				}
				run, err := h.backend.StartRun(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				waitForRunCompleted(t, h.backend, run.ID)

				// Client reads just the first event (seq=1), then "restarts" and reads the rest.
				batch1, err := h.backend.Events(ctx, 0, 0, 1)
				if err != nil || len(batch1.Events) != 1 {
					t.Fatalf("first event read = (%+v, %v), want 1 event", batch1, err)
				}
				savedSeq := batch1.Events[0].Seq
				batchRest, err := h.backend.Events(ctx, savedSeq, 0, 100)
				if err != nil || len(batchRest.Events) == 0 {
					t.Fatalf("resumed client event read = (%+v, %v), want remaining events", batchRest, err)
				}
				lastEv := batchRest.Events[len(batchRest.Events)-1]
				if lastEv.Kind != gritapi.EventRunCompleted || lastEv.Conclusion != "success" {
					t.Fatalf("last event after client restart = %+v, want run.completed success", lastEv)
				}

				// Now prune events <= 3 and verify that requesting `after=1` returns 410 cursor_gone.
				if err := h.local.Store().PruneEventsUpTo(3); err != nil {
					t.Fatalf("PruneEventsUpTo: %v", err)
				}
				_, err = h.backend.Events(ctx, 1, 0, 10)
				goneErr, ok := gritapi.IsCursorGone(err)
				if !ok || goneErr.OldestSeq != 4 {
					t.Fatalf("Events(after=1) after pruning <=3 = %v, want 410 cursor_gone with oldestSeq=4", err)
				}

				// Verify gritapi.Client.SyncEvents automatically reconciles in-flight
				// runs via GET /v1/runs/{id} upon receiving 410 cursor_gone.
				if h.client != nil {
					_ = h.cursor.SaveCursor(goneErr.Epoch, 1)
					var resynced []gritapi.RunStatus
					err := h.client.SyncEvents(ctx, 0, 50, []string{run.ID}, nil, func(rs gritapi.RunStatus) error {
						resynced = append(resynced, rs)
						return nil
					})
					if err != nil {
						t.Fatalf("SyncEvents on 410: %v", err)
					}
					if len(resynced) != 1 || resynced[0].ID != run.ID || resynced[0].Conclusion != "success" {
						t.Fatalf("resynced runs = %+v, want [%s success]", resynced, run.ID)
					}
					_, newSeq, _ := h.cursor.LoadCursor()
					if newSeq < 4 {
						t.Fatalf("cursor after 410 resync = %d, want >= 4", newSeq)
					}
				}
			})
		})
	}
}

func TestServerScopesQuotasAndRevocation(t *testing.T) {
	signer, ks := newTestSigner(t)
	dataDir := t.TempDir()
	h := startHarness(t, "remote", dataDir, signer, ks, nil, nil, "")
	defer h.close()

	ctx := context.Background()
	treeID, archive := makeSampleTree(t, map[string]string{"f": "x"})
	if err := h.backend.UploadTree(ctx, treeID.SHA256, archive); err != nil {
		t.Fatal(err)
	}

	// Create a restricted client scoped only to submit+read on `bwsalmon/grain`
	// (no `release`, no `cancel`, no `cache-admin`, and max 2 runs/min).
	restrictedTok, restrictedHash, err := GenerateClientToken("restricted")
	if err != nil {
		t.Fatal(err)
	}
	err = h.local.Store().CreateOrUpdateClient(ClientRecord{
		ID:            "restricted",
		SecretSHA256:  restrictedHash,
		RunsPerMinute: 2,
		Scopes: []ClientScope{
			{Scope: gritapi.ScopeSubmit, Repo: "bwsalmon/grain"},
			{Scope: gritapi.ScopeRead, Repo: "bwsalmon/grain"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	rc := gritapi.NewClient(gritapi.ClientConfig{
		BaseURL:     h.srv.URL,
		ClientToken: restrictedTok,
	})

	// 1. Submitting to another repo is forbidden (403).
	_, err = rc.StartRun(ctx, gritapi.StartRun{
		Repo:   "other/repo",
		Ref:    "main",
		Commit: "1111111111111111111111111111111111111111",
		Tree:   treeID,
		Event:  "push",
	})
	if apiErr, ok := err.(*gritapi.APIError); !ok || apiErr.StatusCode != 403 {
		t.Fatalf("StartRun on unauthorized repo = %v, want 403", err)
	}

	// 2. Submitting trustedRelease: true without `release` scope is forbidden (403).
	_, err = rc.StartRun(ctx, gritapi.StartRun{
		Repo:           "bwsalmon/grain",
		Ref:            "main",
		Commit:         "2222222222222222222222222222222222222222",
		Tree:           treeID,
		Event:          "release",
		TrustedRelease: true,
	})
	if apiErr, ok := err.(*gritapi.APIError); !ok || apiErr.StatusCode != 403 {
		t.Fatalf("StartRun with trustedRelease without release scope = %v, want 403", err)
	}

	// 3. `/pkg/v1/stats` without `cache-admin` is forbidden (403).
	if _, err := rc.PkgStats(ctx); err == nil {
		t.Fatal("PkgStats without cache-admin succeeded, want 403")
	}
	// Admin client with `cache-admin` succeeds on `/pkg/v1/stats` and `/pkg/v1/flush`.
	if st, err := h.client.PkgStats(ctx); err != nil || !st.Enabled {
		t.Fatalf("admin PkgStats = (%+v, %v), want enabled", st, err)
	}
	if _, err := h.client.PkgFlush(ctx, ""); err != nil {
		t.Fatalf("admin PkgFlush = %v", err)
	}

	// 4. Rate limit: 2 runs/min allowed, 3rd run in the same minute returns 429.
	for i := 1; i <= 2; i++ {
		_, err := rc.StartRun(ctx, gritapi.StartRun{
			Repo:    "bwsalmon/grain",
			Ref:     "main",
			Commit:  "3333333333333333333333333333333333333333",
			Tree:    treeID,
			Event:   "push",
			Attempt: i,
		})
		if err != nil {
			t.Fatalf("run %d within rate limit failed: %v", i, err)
		}
	}
	_, err = rc.StartRun(ctx, gritapi.StartRun{
		Repo:    "bwsalmon/grain",
		Ref:     "main",
		Commit:  "3333333333333333333333333333333333333333",
		Tree:    treeID,
		Event:   "push",
		Attempt: 3,
	})
	if apiErr, ok := err.(*gritapi.APIError); !ok || apiErr.StatusCode != 429 {
		t.Fatalf("3rd run exceeding rate limit = %v, want 429", err)
	}

	// 5. Revoking the client immediately turns its token into 401 Unauthorized.
	if err := h.local.Store().RevokeClient("restricted"); err != nil {
		t.Fatal(err)
	}
	_, err = rc.Info(ctx)
	if apiErr, ok := err.(*gritapi.APIError); !ok || apiErr.StatusCode != 401 {
		t.Fatalf("Info with revoked token = %v, want 401", err)
	}
}
