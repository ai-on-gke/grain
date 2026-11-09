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
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/objectstore"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/redact"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// failingPutStore refuses every Put while fail is set.
type failingPutStore struct {
	objectstore.Store
	fail atomic.Bool
}

func (s *failingPutStore) Put(ctx context.Context, key string, r io.Reader) error {
	if s.fail.Load() {
		return errors.New("object store unavailable")
	}
	return s.Store.Put(ctx, key, r)
}

// One catalogued secret deleted out of band in Secret Manager must not stop
// transcripts being uploaded, redacted of every secret that is still there
// (task 3577, the 2026-11-07 walkthrough's F7).
func TestTranscriptUploadSurvivesASecretGoneFromSecretManager(t *testing.T) {
	ctx := context.Background()
	const prefix = "projects/123456789/secrets/grain-inst-alice-"
	const liveSecret = "multi-a-still-there-0123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only multi/a's secret is left; multi/b's was deleted out of band.
		if r.Method == http.MethodGet && r.URL.Path == "/v1/"+prefix+"multi-a/versions/latest:access" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":    prefix + "multi-a/versions/1",
				"payload": map[string]string{"data": base64.StdEncoding.EncodeToString([]byte(liveSecret))},
			})
			return
		}
		http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	cat := secrets.NewMemoryCatalogue()
	for _, e := range []secrets.CatalogueEntry{
		{SecretName: "multi", Key: "a", SMSecret: "grain-inst-alice-multi-a"},
		{SecretName: "multi", Key: "b", SMSecret: "grain-inst-alice-multi-b"},
	} {
		if err := cat.PutSecretEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	secStore := secrets.OpenSecretManager(secrets.SecretManagerConfig{
		Prefix:     prefix,
		Catalogue:  cat,
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Token:      func(context.Context) (string, error) { return "tok", nil },
	})

	mem := objectstore.NewMemory()
	dataDir := t.TempDir()
	liveDir := filepath.Join(dataDir, "state", "transcripts")
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tStore, err := NewTranscriptObjectStore(mem, filepath.Join(dataDir, "cache"), liveDir,
		transcriptObjectRedactor(Config{DataDir: dataDir}, secStore))
	if err != nil {
		t.Fatal(err)
	}
	rawPath := filepath.Join(liveDir, "run-gone")
	if err := os.WriteFile(rawPath, []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"saw `+liveSecret+`"}]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	saved, err := tStore.FlushRunEnd(ctx, "run-gone", rawPath, "Final report: "+liveSecret)
	if err != nil || !saved {
		t.Fatalf("FlushRunEnd with multi/b gone upstream = (%v, %v), want (true, nil)", saved, err)
	}
	for _, key := range []string{"transcripts/run-gone.jsonl", "transcripts/run-gone.md"} {
		data, ok := mem.Bytes(key)
		if !ok {
			t.Fatalf("expected %s in the object store", key)
		}
		if strings.Contains(string(data), liveSecret) {
			t.Fatalf("%s leaked multi/a's value: %s", key, data)
		}
	}
}

// A finished run whose run-end upload failed keeps its raw stream, and the
// periodic sync uploads it once it can: the orchestrator removes the live file
// as soon as FlushRunEnd returns, so syncLiveDir alone would never see it
// again (the 2026-11-07 walkthrough's O1).
func TestFinishedRunsRawStreamIsRetriedAfterAFailedUpload(t *testing.T) {
	ctx := context.Background()
	const secret = "deploy-secret-abcdef0123456789"
	for _, tc := range []struct {
		name string
		// break makes the store fail, mend makes it work again.
		breakIt, mendIt func(redactorFails *atomic.Bool, store *failingPutStore)
	}{
		{
			name:    "redactor unavailable",
			breakIt: func(r *atomic.Bool, _ *failingPutStore) { r.Store(true) },
			mendIt:  func(r *atomic.Bool, _ *failingPutStore) { r.Store(false) },
		},
		{
			name:    "object store put fails",
			breakIt: func(_ *atomic.Bool, s *failingPutStore) { s.fail.Store(true) },
			mendIt:  func(_ *atomic.Bool, s *failingPutStore) { s.fail.Store(false) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem := objectstore.NewMemory()
			store := &failingPutStore{Store: mem}
			var redactorFails atomic.Bool
			dataDir := t.TempDir()
			liveDir := filepath.Join(dataDir, "state", "transcripts")
			if err := os.MkdirAll(liveDir, 0o700); err != nil {
				t.Fatal(err)
			}
			tStore, err := NewTranscriptObjectStore(store, filepath.Join(dataDir, "cache"), liveDir, func() (*redact.Redactor, error) {
				if redactorFails.Load() {
					return nil, errors.New("secret store unavailable")
				}
				return redact.New([]redact.KnownValue{{Label: "secret deploy/key", Value: secret}}), nil
			})
			if err != nil {
				t.Fatal(err)
			}

			rawPath := filepath.Join(liveDir, "run-1")
			raw := `{"type":"assistant","message":{"content":[{"type":"text","text":"used ` + secret + `"}]}}` + "\n"
			if err := os.WriteFile(rawPath, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			tc.breakIt(&redactorFails, store)
			if _, err := tStore.FlushRunEnd(ctx, "run-1", rawPath, ""); err == nil {
				t.Fatal("FlushRunEnd = nil error, want the failure reported")
			}
			// What the orchestrator does next, whatever FlushRunEnd said.
			if err := os.Remove(rawPath); err != nil {
				t.Fatal(err)
			}
			if _, ok := mem.Bytes("transcripts/run-1.jsonl"); ok {
				t.Fatal("raw stream uploaded despite the failure")
			}
			kept := filepath.Join(liveDir+".unflushed", "run-1")
			if _, err := os.Stat(kept); err != nil {
				t.Fatalf("raw stream not kept for a retry: %v", err)
			}

			// Still failing: kept, not uploaded, and never unredacted.
			if err := tStore.SyncChanged(ctx); err == nil {
				t.Fatal("SyncChanged while still failing = nil, want an error")
			}
			if _, err := os.Stat(kept); err != nil {
				t.Fatalf("kept raw stream lost on a failed retry: %v", err)
			}

			tc.mendIt(&redactorFails, store)
			if err := tStore.SyncChanged(ctx); err != nil {
				t.Fatalf("SyncChanged once mended: %v", err)
			}
			stored, ok := mem.Bytes("transcripts/run-1.jsonl")
			if !ok {
				t.Fatal("raw stream not uploaded on retry")
			}
			if strings.Contains(string(stored), secret) {
				t.Fatalf("retried upload leaked the secret: %s", stored)
			}
			if !strings.Contains(string(stored), "[redacted:") {
				t.Fatalf("retried upload missing redaction marker: %s", stored)
			}
			if _, err := os.Stat(kept); !os.IsNotExist(err) {
				t.Fatalf("kept raw stream not removed after upload: %v", err)
			}
		})
	}
}

// A kept stream for a run whose live file is back (adopted again after a
// restart) is dropped rather than let it overwrite the newer stream.
func TestUnflushedRawStreamDefersToALiveFile(t *testing.T) {
	ctx := context.Background()
	mem := objectstore.NewMemory()
	dataDir := t.TempDir()
	liveDir := filepath.Join(dataDir, "state", "transcripts")
	unflushed := liveDir + ".unflushed"
	for _, d := range []string{liveDir, unflushed} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tStore, err := NewTranscriptObjectStore(mem, filepath.Join(dataDir, "cache"), liveDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unflushed, "run-2"), []byte(`{"type":"old"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, "run-2"), []byte(`{"type":"old"}`+"\n"+`{"type":"new"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tStore.SyncChanged(ctx); err != nil {
		t.Fatal(err)
	}
	stored, _ := mem.Bytes("transcripts/run-2.jsonl")
	if !strings.Contains(string(stored), `"new"`) {
		t.Fatalf("stored stream = %q, want the live file's", stored)
	}
	if _, err := os.Stat(filepath.Join(unflushed, "run-2")); !os.IsNotExist(err) {
		t.Fatalf("stale kept stream not dropped: %v", err)
	}
}

// When FlushRunEnd fails closed (because Secret Manager returns 503/403 or the
// object store returns 503), RunDispatch must NOT fall back to writing the
// unredacted narrative into SQLite task_run.transcript; instead both raw
// stream and narrative are staged on disk and uploaded redacted once healthy.
func TestRunDispatchWithFailedTranscriptFlushNeverLeaksSecretsToSQLiteAndRetriesNarrative(t *testing.T) {
	ctx := context.Background()
	const secretVal = "sm-active-token-value-9988776655"
	for _, tc := range []struct {
		name            string
		smStatus        int
		failObjectStore bool
	}{
		{name: "secretmanager 503", smStatus: http.StatusServiceUnavailable},
		{name: "secretmanager 403", smStatus: http.StatusForbidden},
		{name: "object store 503", smStatus: http.StatusOK, failObjectStore: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var smCode atomic.Int32
			smCode.Store(int32(tc.smStatus))
			const prefix = "projects/123456789/secrets/grain-inst-alice-"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				code := int(smCode.Load())
				if code != http.StatusOK {
					http.Error(w, `{"error":{"message":"upstream unavailable"}}`, code)
					return
				}
				if r.Method == http.MethodGet && r.URL.Path == "/v1/"+prefix+"active-token/versions/latest:access" {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"name":    prefix + "active-token/versions/1",
						"payload": map[string]string{"data": base64.StdEncoding.EncodeToString([]byte(secretVal))},
					})
					return
				}
				http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`, http.StatusNotFound)
			}))
			t.Cleanup(srv.Close)

			cat := secrets.NewMemoryCatalogue()
			if err := cat.PutSecretEntry(ctx, secrets.CatalogueEntry{
				SecretName: "active-token", Key: "value", SMSecret: "grain-inst-alice-active-token",
			}); err != nil {
				t.Fatal(err)
			}
			secStore := secrets.OpenSecretManager(secrets.SecretManagerConfig{
				Prefix:     prefix,
				Catalogue:  cat,
				Endpoint:   srv.URL,
				HTTPClient: srv.Client(),
				Token:      func(context.Context) (string, error) { return "tok", nil },
			})

			db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "db")))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			modelStore := model.New(db)
			if err := modelStore.Init(ctx); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
			if err := modelStore.PutTask(ctx, model.Task{
				ID: "task-sec", Title: "Check fail-closed redaction", Body: "Run", CreatedAt: &now,
			}); err != nil {
				t.Fatal(err)
			}
			if err := modelStore.StartRun(ctx, model.Run{
				ID: "run-sec-1", TaskID: "task-sec", Sandbox: "run-sec-1", Attempt: 1, StartedAt: now,
			}, model.Limits{}); err != nil {
				t.Fatal(err)
			}
			task, err := modelStore.GetTask(ctx, "task-sec")
			if err != nil || task == nil {
				t.Fatalf("GetTask: %v", err)
			}

			mem := objectstore.NewMemory()
			objStore := &failingPutStore{Store: mem}
			objStore.fail.Store(tc.failObjectStore)

			dataDir := t.TempDir()
			liveDir := filepath.Join(dataDir, "state", "transcripts")
			if err := os.MkdirAll(liveDir, 0o700); err != nil {
				t.Fatal(err)
			}
			cacheDir := filepath.Join(dataDir, "cache", "transcripts")
			tStore, err := NewTranscriptObjectStore(objStore, cacheDir, liveDir,
				transcriptObjectRedactor(Config{DataDir: dataDir}, secStore))
			if err != nil {
				t.Fatal(err)
			}

			fw := testAgentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
				line := `{"type":"assistant","message":{"content":[{"type":"text","text":"printed ` + secretVal + `"}]}}` + "\n"
				if err := os.WriteFile(cfg.TranscriptPath, []byte(line), 0o600); err != nil {
					return nil, err
				}
				return &agent.Result{
					FinalText:  "Done",
					Transcript: "Final narrative echoed " + secretVal,
					ToolCalls:  []agent.ToolCall{{Name: "nothing_needed", Arguments: map[string]any{"reason": "done"}}},
				}, nil
			})
			orchCfg := orchestrator.Config{
				TranscriptDir:  liveDir,
				TranscriptSink: tStore,
				Now:            func() time.Time { return now.Add(time.Minute) },
			}
			if _, err := orchestrator.RunDispatch(ctx, modelStore, fw, orchCfg, *task,
				dispatch.Dispatch{TaskID: "task-sec", RunID: "run-sec-1", Attempt: 1},
				nil, t.TempDir(), "", nil, now); err != nil {
				t.Fatalf("RunDispatch: %v", err)
			}

			dbTranscript, exists, err := modelStore.RunTranscript(ctx, "task-sec", 1)
			if err != nil || !exists {
				t.Fatalf("RunTranscript = (%q, %v, %v)", dbTranscript, exists, err)
			}
			if dbTranscript != "" {
				t.Fatalf("SQLite task_run.transcript stored unredacted fallback %q, want empty", dbTranscript)
			}
			if _, ok := mem.Bytes("transcripts/run-sec-1.jsonl"); ok {
				t.Fatal("uploaded .jsonl despite failure")
			}
			if _, ok := mem.Bytes("transcripts/run-sec-1.md"); ok {
				t.Fatal("uploaded .md despite failure")
			}
			uiClient := ui.NewClient(ui.Config{
				LiveTranscripts:     LiveTranscriptDirWithStore(liveDir, tStore),
				ArchivedTranscripts: tStore,
			}, modelStore)
			duringOutage, err := uiClient.AttemptTranscript(ctx, "task-sec", 1)
			if err != nil || strings.Contains(duringOutage, secretVal) {
				t.Fatalf("AttemptTranscript during outage = (%q, %v), must never leak secret", duringOutage, err)
			}
			for _, staged := range []string{"run-sec-1", "run-sec-1.md"} {
				if _, err := os.Stat(filepath.Join(liveDir+".unflushed", staged)); err != nil {
					t.Fatalf("expected %s staged in unflushedDir: %v", staged, err)
				}
			}

			// Restore upstream health and verify a fresh TranscriptObjectStore
			// (as after a daemon restart) redacts and uploads both files.
			smCode.Store(http.StatusOK)
			objStore.fail.Store(false)
			restartedStore, err := NewTranscriptObjectStore(objStore, cacheDir, liveDir,
				transcriptObjectRedactor(Config{DataDir: dataDir}, secStore))
			if err != nil {
				t.Fatal(err)
			}
			if err := restartedStore.SyncChanged(ctx); err != nil {
				t.Fatalf("SyncChanged after recovery: %v", err)
			}
			for _, key := range []string{"transcripts/run-sec-1.jsonl", "transcripts/run-sec-1.md"} {
				b, ok := mem.Bytes(key)
				if !ok {
					t.Fatalf("expected %s uploaded on retry", key)
				}
				if strings.Contains(string(b), secretVal) {
					t.Fatalf("%s leaked secret: %s", key, b)
				}
				if !strings.Contains(string(b), "[redacted:") {
					t.Fatalf("%s missing redaction marker: %s", key, b)
				}
			}
			for _, staged := range []string{"run-sec-1", "run-sec-1.md"} {
				if _, err := os.Stat(filepath.Join(liveDir+".unflushed", staged)); !os.IsNotExist(err) {
					t.Fatalf("expected %s removed after retry: %v", staged, err)
				}
			}
			recoveredUI := ui.NewClient(ui.Config{
				LiveTranscripts:     LiveTranscriptDirWithStore(liveDir, restartedStore),
				ArchivedTranscripts: restartedStore,
			}, modelStore)
			gotAfter, err := recoveredUI.AttemptTranscript(ctx, "task-sec", 1)
			if err != nil || strings.Contains(gotAfter, secretVal) || !strings.Contains(gotAfter, "[redacted:") {
				t.Fatalf("AttemptTranscript after recovery = (%q, %v), want redacted narrative", gotAfter, err)
			}
		})
	}
}
