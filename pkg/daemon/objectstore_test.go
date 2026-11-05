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
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/objectstore"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/redact"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type testAgentFunc func(context.Context, agent.RunConfig) (*agent.Result, error)

func (f testAgentFunc) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	return f(ctx, cfg)
}

func TestObjectStoreEndToEndTranscriptsArtifactsAndSessions(t *testing.T) {
	ctx := context.Background()
	mem := objectstore.NewMemory()
	instanceStore := objectstore.WithPrefix(mem, "instances/calm-otter-7f3a")

	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "db")))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("store.Init: %v", err)
	}

	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	if err := store.PutTask(ctx, model.Task{
		ID:        "task-1",
		Title:     "Verify GCS object store",
		Body:      "Test run",
		CreatedAt: &now,
	}); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID:        "run-1",
		TaskID:    "task-1",
		Sandbox:   "run-1",
		Attempt:   1,
		StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	task, err := store.GetTask(ctx, "task-1")
	if err != nil || task == nil {
		t.Fatalf("GetTask: %v", err)
	}

	vm1DataDir := filepath.Join(t.TempDir(), "vm1-data")
	vm1LiveDir := filepath.Join(vm1DataDir, "state", "transcripts")
	if err := os.MkdirAll(vm1LiveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	vm1TranscriptCache := filepath.Join(vm1DataDir, "cache", "object-store", "transcripts")
	tStore1, err := NewTranscriptObjectStore(instanceStore, vm1TranscriptCache, vm1LiveDir, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Start periodic sync with a short interval to exercise mid-run live flushing.
	stopSync := tStore1.StartPeriodicSync(ctx, 15*time.Millisecond)
	defer stopSync()

	claudeLine := `{"type":"assistant","message":{"content":[{"type":"text","text":"Streaming live progress"}]}}` + "\n"
	finalNarrative := "## Turn 1\nCompleted object store verification."

	fw := testAgentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		if err := os.WriteFile(cfg.TranscriptPath, []byte(claudeLine), 0o600); err != nil {
			return nil, err
		}
		// Wait briefly so periodic sync flushes <runID>.jsonl to MemoryStore while still live,
		// and verify a remote viewer (with an empty local liveDir) can tail it!
		deadline := time.Now().Add(2 * time.Second)
		remoteViewer := LiveTranscriptDirWithStore(t.TempDir(), tStore1)
		for time.Now().Before(deadline) {
			if text, ok, _ := remoteViewer.Tail("run-1"); ok && strings.Contains(text, "Streaming live progress") {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		text, ok, err := remoteViewer.Tail("run-1")
		if err != nil || !ok || !strings.Contains(text, "Streaming live progress") {
			t.Errorf("remote live Tail during run = (%q, %v, %v), want live progress", text, ok, err)
		}
		return &agent.Result{
			FinalText:  "Done",
			Transcript: finalNarrative,
			ToolCalls: []agent.ToolCall{
				{Name: "nothing_needed", Arguments: map[string]any{"reason": "verified"}},
			},
		}, nil
	})

	orchCfg := orchestrator.Config{
		TranscriptDir:  vm1LiveDir,
		TranscriptSink: tStore1,
		Now:            func() time.Time { return now.Add(time.Minute) },
	}
	d := dispatch.Dispatch{TaskID: "task-1", RunID: "run-1", Attempt: 1}
	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchCfg, *task, d, nil, t.TempDir(), "", nil, now); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	stopSync()

	// Both raw .jsonl and redacted .md must exist in MemoryStore under instances/<name>/transcripts/,
	// while SQLite's task_run.transcript column remains empty.
	if _, ok := mem.Bytes("instances/calm-otter-7f3a/transcripts/run-1.jsonl"); !ok {
		t.Fatalf("expected transcripts/run-1.jsonl in MemoryStore, keys = %v", mem.Keys())
	}
	mdBytes, ok := mem.Bytes("instances/calm-otter-7f3a/transcripts/run-1.md")
	if !ok || string(mdBytes) != finalNarrative {
		t.Fatalf("transcripts/run-1.md = %q (ok=%v), want %q", mdBytes, ok, finalNarrative)
	}
	dbTranscript, exists, err := store.RunTranscript(ctx, "task-1", 1)
	if err != nil || !exists {
		t.Fatalf("store.RunTranscript = (%q, %v, %v)", dbTranscript, exists, err)
	}
	if dbTranscript != "" {
		t.Fatalf("expected DB transcript column to remain empty when saved to object store, got %q", dbTranscript)
	}

	// UI Client on VM1 hits local read-through cache without calling MemoryStore.Get.
	mem.ResetCounts()
	uiClient1 := ui.NewClient(ui.Config{
		LiveTranscripts:     LiveTranscriptDirWithStore(vm1LiveDir, tStore1),
		ArchivedTranscripts: tStore1,
	}, store)
	got1, err := uiClient1.AttemptTranscript(ctx, "task-1", 1)
	if err != nil || got1 != finalNarrative {
		t.Fatalf("AttemptTranscript on VM1 = (%q, %v), want %q", got1, err, finalNarrative)
	}
	if mem.GetCount() != 0 {
		t.Fatalf("MemoryStore.GetCount on VM1 local cache hit = %d, want 0", mem.GetCount())
	}

	// Simulate VM replacement (VM2 has an empty dataDir, pointing at the same instanceStore + shared DB).
	vm2DataDir := filepath.Join(t.TempDir(), "vm2-data")
	vm2LiveDir := filepath.Join(vm2DataDir, "state", "transcripts")
	_ = os.MkdirAll(vm2LiveDir, 0o700)
	tStore2, err := NewTranscriptObjectStore(instanceStore, filepath.Join(vm2DataDir, "cache", "object-store", "transcripts"), vm2LiveDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	uiClient2 := ui.NewClient(ui.Config{
		LiveTranscripts:     LiveTranscriptDirWithStore(vm2LiveDir, tStore2),
		ArchivedTranscripts: tStore2,
	}, store)

	mem.ResetCounts()
	for i := 0; i < 2; i++ {
		got2, err := uiClient2.AttemptTranscript(ctx, "task-1", 1)
		if err != nil || got2 != finalNarrative {
			t.Fatalf("AttemptTranscript #%d on VM2 = (%q, %v), want %q", i, got2, err, finalNarrative)
		}
	}
	if mem.GetCount() != 1 {
		t.Fatalf("MemoryStore.GetCount after 2 reads on VM2 = %d, want 1 (read-through cached)", mem.GetCount())
	}

	// Verify Sizes() reports the object-stored transcript bytes for metrics.
	sizes, err := tStore2.Sizes(ctx)
	if err != nil {
		t.Fatalf("Sizes: %v", err)
	}
	if len(sizes) != 1 || sizes[0].RunID != "run-1" || sizes[0].Bytes != len(finalNarrative) {
		t.Fatalf("Sizes = %+v, want [{RunID:run-1 Bytes:%d}]", sizes, len(finalNarrative))
	}

	// Verify CiArtifactStore + agentSessionStoreFor + flushObjectStoreState under the same instanceStore.
	cfg1 := Config{DataDir: vm1DataDir, ObjectStore: instanceStore}
	artStore1 := CiArtifactStore(cfg1, nil)
	ref := ci.ArtifactRef{Run: "cirun1", Job: "e2e", Name: "report"}
	if rec, err := artStore1.Save(ctx, ref, []byte("artifact-tar-gz")); err != nil || !rec.Stored {
		t.Fatalf("CiArtifactStore.Save: %+v, %v", rec, err)
	}

	sessStore1 := agentSessionStoreFor(cfg1)
	home1, release1, ok := sessStore1.Open("codex", "task-1")
	if !ok {
		t.Fatal("sessStore1.Open failed")
	}
	if err := os.WriteFile(filepath.Join(home1, "rollout.jsonl"), []byte(`{"turn":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	release1()

	// Write an un-ended live transcript on VM1 and trigger drain flushObjectStoreState -> verify VM2 recovers all 3!
	if err := os.WriteFile(filepath.Join(vm1LiveDir, "run-drain"), []byte(claudeLine), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg1.transcriptObjects = tStore1
	flushObjectStoreState(ctx, cfg1)

	// Check VM2 reads crashed/drained run-drain transcript via fallback from .jsonl,
	// CI artifact via read-through cache, and codex session via archive extraction.
	drainedText, found, err := tStore2.Read(ctx, "run-drain")
	if err != nil || !found || !strings.Contains(drainedText, "Streaming live progress") {
		t.Fatalf("drained transcript Read on VM2 = (%q, %v, %v)", drainedText, found, err)
	}
	restoredRaw := filepath.Join(t.TempDir(), "restored-raw")
	if err := tStore2.RestoreRaw(ctx, "run-drain", restoredRaw); err != nil {
		t.Fatalf("RestoreRaw: %v", err)
	}
	rawBytes, err := os.ReadFile(restoredRaw)
	if err != nil || string(rawBytes) != claudeLine {
		t.Fatalf("restored raw = %q (err=%v)", rawBytes, err)
	}

	cfg2 := Config{DataDir: vm2DataDir, ObjectStore: instanceStore}
	artStore2 := CiArtifactStore(cfg2, nil)
	rc, _, err := artStore2.Open(ctx, ref)
	if err != nil {
		t.Fatalf("artStore2.Open on VM2: %v", err)
	}
	artBytes, _ := io.ReadAll(rc)
	rc.Close()
	if string(artBytes) != "artifact-tar-gz" {
		t.Fatalf("artStore2 bytes = %q", artBytes)
	}

	sessStore2 := agentSessionStoreFor(cfg2)
	home2, release2, ok := sessStore2.Open("codex", "task-1")
	if !ok {
		t.Fatal("sessStore2.Open failed on VM2")
	}
	defer release2()
	rollout, err := os.ReadFile(filepath.Join(home2, "rollout.jsonl"))
	if err != nil || string(rollout) != `{"turn":1}`+"\n" {
		t.Fatalf("restored codex session on VM2 = %q (err=%v)", rollout, err)
	}
}

func TestTranscriptObjectStoreRedactsJSONLAndNarrativeAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	mem := objectstore.NewMemory()

	dataDir := t.TempDir()
	liveDir := filepath.Join(dataDir, "state", "transcripts")
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(dataDir, "cache", "object-store", "transcripts")

	secretsDir := SecretsDir(dataDir)
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	secStore := secrets.Open(secrets.Config{
		File:    filepath.Join(secretsDir, secrets.DefaultFileName),
		KeyFile: filepath.Join(secretsDir, secrets.DefaultKeyFileName),
	})
	const deploySecret = "deploy-webhook-secret-9876543210"
	if err := secStore.Set("webhook-hmac", "key", []byte(deploySecret)); err != nil {
		t.Fatal(err)
	}
	const hostEnvSecret = "operator-proxy-secret-1122334455"
	t.Setenv("ACME_OPERATOR_TOKEN", hostEnvSecret)
	const ghShapeToken = "ghp_1234567890abcdef1234567890abcdef1234"
	const pemBodyLine = "MIIEvgIBADANBgkqhkiG9w0BAQEFAASCBKgwggSkAgEAAoIBAQC7"

	cfg := Config{
		DataDir:        dataDir,
		HostSandboxEnv: []string{"ACME_OPERATOR_TOKEN"},
	}
	tStore, err := NewTranscriptObjectStore(mem, cacheDir, liveDir, transcriptObjectRedactor(cfg, secStore))
	if err != nil {
		t.Fatal(err)
	}

	rawJSONL := `{"type":"assistant","message":{"content":[{"type":"text","text":"found ` +
		deploySecret + ` and ` + hostEnvSecret + ` and ` + ghShapeToken +
		` and key:\n-----BEGIN PRIVATE KEY-----\n` + pemBodyLine + `\n-----END PRIVATE KEY-----\ndone"}]}}` + "\n"
	rawFile := filepath.Join(liveDir, "run-sec")
	if err := os.WriteFile(rawFile, []byte(rawJSONL), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tStore.SyncChanged(ctx); err != nil {
		t.Fatalf("SyncChanged: %v", err)
	}

	storedJSONL, ok := mem.Bytes("transcripts/run-sec.jsonl")
	if !ok {
		t.Fatal("expected transcripts/run-sec.jsonl in object store")
	}
	for _, secretVal := range []string{deploySecret, hostEnvSecret, ghShapeToken, pemBodyLine} {
		if strings.Contains(string(storedJSONL), secretVal) {
			t.Fatalf("stored .jsonl leaked %q: %s", secretVal, storedJSONL)
		}
	}

	tailed, ok, err := tStore.TailRemoteRaw(ctx, "run-sec")
	if err != nil || !ok {
		t.Fatalf("TailRemoteRaw = (%q, %v, %v)", tailed, ok, err)
	}
	if !strings.Contains(tailed, "[redacted:") {
		t.Fatalf("TailRemoteRaw missing redaction marker: %q", tailed)
	}

	narrative := "Final report: " + deploySecret + " / " + hostEnvSecret + " / " + ghShapeToken
	saved, err := tStore.FlushRunEnd(ctx, "run-sec", rawFile, narrative)
	if err != nil || !saved {
		t.Fatalf("FlushRunEnd = (%v, %v), want (true, nil)", saved, err)
	}
	storedMD, ok := mem.Bytes("transcripts/run-sec.md")
	if !ok {
		t.Fatal("expected transcripts/run-sec.md in object store")
	}
	for _, secretVal := range []string{deploySecret, hostEnvSecret, ghShapeToken} {
		if strings.Contains(string(storedMD), secretVal) {
			t.Fatalf("stored .md leaked %q: %s", secretVal, storedMD)
		}
	}

	// Verify fail-closed behavior when the secret store cannot be read.
	brokenStore, err := NewTranscriptObjectStore(mem, t.TempDir(), liveDir, func() (*redact.Redactor, error) {
		return nil, errors.New("secret store unavailable")
	})
	if err != nil {
		t.Fatal(err)
	}
	unredactedPath := filepath.Join(liveDir, "run-failclosed")
	if err := os.WriteFile(unredactedPath, []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"`+deploySecret+`"}]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := brokenStore.SyncChanged(ctx); err == nil {
		t.Fatal("expected SyncChanged to fail closed when redactor errors")
	}
	if _, exists := mem.Bytes("transcripts/run-failclosed.jsonl"); exists {
		t.Fatal("brokenStore uploaded unredacted .jsonl despite redactor error")
	}
	if saved, err := brokenStore.FlushRunEnd(ctx, "run-failclosed", unredactedPath, deploySecret); err == nil || saved {
		t.Fatalf("FlushRunEnd on broken redactor = (%v, %v), want (false, err)", saved, err)
	}
	if _, exists := mem.Bytes("transcripts/run-failclosed.md"); exists {
		t.Fatal("brokenStore uploaded unredacted .md despite redactor error")
	}

	// Seed a raw stream and ensure Read/TailRemoteRaw also fail closed without caching.
	_ = mem.Put(ctx, "transcripts/run-crashed.jsonl", strings.NewReader(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`+"\n"))
	if text, ok, err := brokenStore.Read(ctx, "run-crashed"); err == nil || ok || text != "" {
		t.Fatalf("brokenStore.Read = (%q, %v, %v), want fail-closed error", text, ok, err)
	}
	if _, statErr := os.Stat(filepath.Join(brokenStore.Cache().Dir(), "run-crashed.md")); !os.IsNotExist(statErr) {
		t.Fatalf("brokenStore cached fallback .md despite redactor failure: %v", statErr)
	}
}

func TestHostSandboxHiddenPathsIncludesFileObjectStore(t *testing.T) {
	stubHostConfinement(t, nil)
	dataDir := t.TempDir()
	extObjDir := t.TempDir()
	nestedObjDir := filepath.Join(dataDir, "nested-obj")

	got, err := hostSandboxHiddenPaths(context.Background(), Config{
		DataDir:        dataDir,
		ObjectStoreURI: "file://" + extObjDir,
	})
	if err != nil {
		t.Fatalf("hostSandboxHiddenPaths external file store: %v", err)
	}
	if !slices.Equal(got, []string{dataDir, extObjDir}) {
		t.Fatalf("hostSandboxHiddenPaths = %v, want [%s %s]", got, dataDir, extObjDir)
	}

	gotNested, err := hostSandboxHiddenPaths(context.Background(), Config{
		DataDir:        dataDir,
		ObjectStoreURI: "file://" + nestedObjDir,
	})
	if err != nil {
		t.Fatalf("hostSandboxHiddenPaths nested file store: %v", err)
	}
	if !slices.Equal(gotNested, []string{dataDir}) {
		t.Fatalf("hostSandboxHiddenPaths nested = %v, want [%s]", gotNested, dataDir)
	}
}
