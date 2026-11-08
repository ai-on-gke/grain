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

	"github.com/ai-on-gke/grain/pkg/objectstore"
	"github.com/ai-on-gke/grain/pkg/redact"
	"github.com/ai-on-gke/grain/pkg/secrets"
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
