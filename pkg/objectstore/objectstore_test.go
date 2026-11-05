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

package objectstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/option"
)

func runStoreSuite(t *testing.T, newStore func(t *testing.T) Store) {
	t.Helper()
	ctx := context.Background()

	t.Run("PutGetOverwriteDeleteList", func(t *testing.T) {
		s := newStore(t)

		if _, _, err := s.Get(ctx, "transcripts/run-1.jsonl"); !errors.Is(err, ErrNotExist) {
			t.Fatalf("Get missing = %v, want ErrNotExist", err)
		}
		if _, err := s.Stat(ctx, "transcripts/run-1.jsonl"); !errors.Is(err, ErrNotExist) {
			t.Fatalf("Stat missing = %v, want ErrNotExist", err)
		}

		if err := s.Put(ctx, "transcripts/run-1.jsonl", strings.NewReader("line1\n")); err != nil {
			t.Fatalf("Put initial: %v", err)
		}
		if err := s.Put(ctx, "transcripts/run-1.jsonl", strings.NewReader("line1\nline2\n")); err != nil {
			t.Fatalf("Put overwrite: %v", err)
		}
		if err := s.Put(ctx, "ci-artifacts/r1/build.logs.tar.gz", bytes.NewReader([]byte("tgzdata"))); err != nil {
			t.Fatalf("Put artifact: %v", err)
		}

		info, err := s.Stat(ctx, "transcripts/run-1.jsonl")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if info.Key != "transcripts/run-1.jsonl" || info.Size != int64(len("line1\nline2\n")) {
			t.Fatalf("Stat = %+v, want size %d", info, len("line1\nline2\n"))
		}

		rc, getInfo, err := s.Get(ctx, "transcripts/run-1.jsonl")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if string(got) != "line1\nline2\n" {
			t.Fatalf("Get body = %q, want %q", got, "line1\nline2\n")
		}
		if getInfo.Size != int64(len(got)) {
			t.Fatalf("Get size = %d, want %d", getInfo.Size, len(got))
		}

		listed, err := s.List(ctx, "transcripts/")
		if err != nil {
			t.Fatalf("List transcripts/: %v", err)
		}
		if len(listed) != 1 || listed[0].Key != "transcripts/run-1.jsonl" {
			t.Fatalf("List transcripts/ = %+v, want [transcripts/run-1.jsonl]", listed)
		}

		all, err := s.List(ctx, "")
		if err != nil {
			t.Fatalf("List all: %v", err)
		}
		if len(all) != 2 || all[0].Key != "ci-artifacts/r1/build.logs.tar.gz" || all[1].Key != "transcripts/run-1.jsonl" {
			t.Fatalf("List all = %+v", all)
		}

		if err := s.Delete(ctx, "transcripts/run-1.jsonl"); err != nil {
			t.Fatalf("Delete existing: %v", err)
		}
		if err := s.Delete(ctx, "transcripts/run-1.jsonl"); err != nil {
			t.Fatalf("Delete idempotent missing: %v", err)
		}
		if _, err := s.Stat(ctx, "transcripts/run-1.jsonl"); !errors.Is(err, ErrNotExist) {
			t.Fatalf("Stat after Delete = %v, want ErrNotExist", err)
		}
	})

	t.Run("InvalidKeysRejected", func(t *testing.T) {
		s := newStore(t)
		for _, bad := range []string{"", "/", "/leading", "trailing/", "a//b", "../escape", "a/../b", `a\b`} {
			if err := s.Put(ctx, bad, strings.NewReader("x")); err == nil {
				t.Errorf("Put(%q) succeeded, want error", bad)
			}
			if _, _, err := s.Get(ctx, bad); err == nil {
				t.Errorf("Get(%q) succeeded, want error", bad)
			}
		}
		for _, badPrefix := range []string{"/", "/leading", "../escape", "a//b", `a\b`} {
			if _, err := s.List(ctx, badPrefix); err == nil {
				t.Errorf("List(%q) succeeded, want error", badPrefix)
			}
		}
	})
}

func TestMemoryStore(t *testing.T) {
	runStoreSuite(t, func(*testing.T) Store {
		return NewMemory()
	})
}

func TestDirStore(t *testing.T) {
	runStoreSuite(t, func(t *testing.T) Store {
		s, err := NewDir(filepath.Join(t.TempDir(), "objects"))
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestWithPrefix(t *testing.T) {
	runStoreSuite(t, func(*testing.T) Store {
		base := NewMemory()
		// Seed a sibling prefix to ensure List and Delete never leak across prefixes.
		_ = base.Put(context.Background(), "instances/other-vm/transcripts/leak.jsonl", strings.NewReader("other"))
		return WithPrefix(base, "instances/calm-otter-7f3a/")
	})
}

func TestParseAndValidateURI(t *testing.T) {
	valid := []struct {
		in         string
		wantScheme string
		wantBucket string
		wantPrefix string
		wantDir    string
	}{
		{
			in:         "gs://grain-instances-prod-state/instances/calm-otter-7f3a/",
			wantScheme: "gs",
			wantBucket: "grain-instances-prod-state",
			wantPrefix: "instances/calm-otter-7f3a/",
		},
		{
			in:         "gs://grain-instances-prod-state/instances/calm-otter-7f3a",
			wantScheme: "gs",
			wantBucket: "grain-instances-prod-state",
			wantPrefix: "instances/calm-otter-7f3a/",
		},
		{
			in:         "gs://my-bucket",
			wantScheme: "gs",
			wantBucket: "my-bucket",
			wantPrefix: "",
		},
		{
			in:         "file:///var/lib/grain/object-store",
			wantScheme: "file",
			wantDir:    "/var/lib/grain/object-store",
		},
	}
	for _, tc := range valid {
		got, err := ParseURI(tc.in)
		if err != nil {
			t.Fatalf("ParseURI(%q): %v", tc.in, err)
		}
		if got.Scheme != tc.wantScheme || got.Bucket != tc.wantBucket || got.Prefix != tc.wantPrefix || got.Dir != tc.wantDir {
			t.Errorf("ParseURI(%q) = %+v, want scheme=%q bucket=%q prefix=%q dir=%q",
				tc.in, got, tc.wantScheme, tc.wantBucket, tc.wantPrefix, tc.wantDir)
		}
	}

	invalid := []string{
		"",
		"s3://bucket/key",
		"http://example.com/bucket",
		"gs://",
		"gs:///only-path",
		"gs://bucket/instances/../escape",
		"gs://bucket/instances//double",
		"gs://bucket/prefix?foo=bar",
		"gs://bucket/prefix#frag",
		"file://relative/path",
		"file:///tmp/../escape",
	}
	for _, raw := range invalid {
		if err := ValidateURI(raw); err == nil {
			t.Errorf("ValidateURI(%q) = nil, want error", raw)
		}
	}
}

func TestReadThroughCache(t *testing.T) {
	ctx := context.Background()
	mem := NewMemory()
	cacheDir := filepath.Join(t.TempDir(), "cache")
	cache, err := NewReadThroughCache(WithPrefix(mem, "transcripts"), cacheDir)
	if err != nil {
		t.Fatal(err)
	}

	// PutBytes populates both remote and local cache; subsequent reads never hit remote Get.
	if err := cache.PutBytes(ctx, "run-1.md", []byte("# Run 1\nhello")); err != nil {
		t.Fatal(err)
	}
	if mem.PutCount() != 1 {
		t.Fatalf("PutCount = %d, want 1", mem.PutCount())
	}
	for i := 0; i < 3; i++ {
		got, err := cache.GetBytes(ctx, "run-1.md")
		if err != nil {
			t.Fatalf("GetBytes #%d: %v", i, err)
		}
		if string(got) != "# Run 1\nhello" {
			t.Fatalf("GetBytes = %q", got)
		}
	}
	if mem.GetCount() != 0 {
		t.Fatalf("GetCount after cached PutBytes reads = %d, want 0", mem.GetCount())
	}

	// Simulate VM replacement / local eviction: ClearLocal wipes local disk while keeping remote intact.
	files, freed, err := cache.ClearLocal()
	if err != nil {
		t.Fatalf("ClearLocal: %v", err)
	}
	if files != 1 || freed != int64(len("# Run 1\nhello")) {
		t.Fatalf("ClearLocal = (%d, %d), want (1, %d)", files, freed, len("# Run 1\nhello"))
	}

	// First read after ClearLocal fetches from MemoryStore once and re-populates local disk;
	// second read hits local disk with zero extra remote Gets.
	for i := 0; i < 2; i++ {
		got, err := cache.GetBytes(ctx, "run-1.md")
		if err != nil {
			t.Fatalf("GetBytes post-clear #%d: %v", i, err)
		}
		if string(got) != "# Run 1\nhello" {
			t.Fatalf("GetBytes post-clear = %q", got)
		}
	}
	if mem.GetCount() != 1 {
		t.Fatalf("GetCount after 2 reads post-clear = %d, want 1", mem.GetCount())
	}
	info, err := os.Stat(filepath.Join(cacheDir, "run-1.md"))
	if err != nil {
		t.Fatalf("expected cached file on disk: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("cached file perm = %04o, want 0600", perm)
	}
}

func TestGCSStoreFakeServer(t *testing.T) {
	const (
		wantBucket = "grain-instances-prod-state"
		wantPrefix = "instances/calm-otter-7f3a/"
	)

	type storedObj struct {
		body    []byte
		updated string
	}
	var (
		mu      sync.Mutex
		objects = make(map[string]storedObj)
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Managed Folder roles/storage.objectUser grants NO bucket metadata access.
		if r.URL.Path == "/storage/v1/b/"+wantBucket && r.Method == http.MethodGet {
			t.Errorf("GCSStore called Buckets.Get (%s), violating Managed Folder IAM", r.URL.String())
			http.Error(w, "forbidden bucket get", http.StatusForbidden)
			return
		}

		// Upload endpoint: POST /upload/storage/v1/b/{bucket}/o
		if r.Method == http.MethodPost && r.URL.Path == "/upload/storage/v1/b/"+wantBucket+"/o" {
			if got := r.URL.Query().Get("ifGenerationMatch"); got != "" {
				t.Errorf("unexpected ifGenerationMatch=%q on upload; overwrites must be allowed", got)
			}
			var meta struct {
				Name string `json:"name"`
			}
			var payload []byte
			ct, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err == nil && strings.HasPrefix(ct, "multipart/") {
				mr := multipart.NewReader(r.Body, params["boundary"])
				p1, err := mr.NextPart()
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				metaBytes, _ := io.ReadAll(p1)
				_ = json.Unmarshal(metaBytes, &meta)
				p2, err := mr.NextPart()
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				payload, _ = io.ReadAll(p2)
			} else {
				meta.Name = r.URL.Query().Get("name")
				payload, _ = io.ReadAll(r.Body)
			}
			if !strings.HasPrefix(meta.Name, wantPrefix) {
				http.Error(w, "outside managed folder prefix", http.StatusForbidden)
				return
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			mu.Lock()
			objects[meta.Name] = storedObj{body: payload, updated: now}
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"bucket":  wantBucket,
				"name":    meta.Name,
				"size":    strconv.Itoa(len(payload)),
				"updated": now,
			})
			return
		}

		// List endpoint: GET /storage/v1/b/{bucket}/o
		if r.Method == http.MethodGet && r.URL.Path == "/storage/v1/b/"+wantBucket+"/o" {
			qPrefix := r.URL.Query().Get("prefix")
			if !strings.HasPrefix(qPrefix, wantPrefix) {
				t.Errorf("List prefix = %q, must start with %q for Managed Folder IAM", qPrefix, wantPrefix)
				http.Error(w, "forbidden list prefix", http.StatusForbidden)
				return
			}
			mu.Lock()
			var names []string
			for k := range objects {
				if strings.HasPrefix(k, qPrefix) {
					names = append(names, k)
				}
			}
			sort.Strings(names)
			// Exercise pagination when more than 1 item matches.
			pageToken := r.URL.Query().Get("pageToken")
			nextToken := ""
			if len(names) > 1 {
				if pageToken == "" {
					names = names[:1]
					nextToken = "page-2"
				} else if pageToken == "page-2" {
					names = names[1:]
				}
			}
			items := make([]map[string]any, 0, len(names))
			for _, k := range names {
				o := objects[k]
				items = append(items, map[string]any{
					"name":    k,
					"size":    strconv.Itoa(len(o.body)),
					"updated": o.updated,
				})
			}
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"nextPageToken": nextToken,
				"items":         items,
			})
			return
		}

		// Object Get / Download / Delete: /storage/v1/b/{bucket}/o/{object} or /download/storage/v1/b/{bucket}/o/{object}
		const (
			apiPrefix = "/storage/v1/b/" + wantBucket + "/o/"
			dlPrefix  = "/download/storage/v1/b/" + wantBucket + "/o/"
		)
		rawPath := r.URL.EscapedPath()
		var encodedObj string
		isDownload := r.URL.Query().Get("alt") == "media"
		switch {
		case strings.HasPrefix(rawPath, apiPrefix):
			encodedObj = strings.TrimPrefix(rawPath, apiPrefix)
		case strings.HasPrefix(rawPath, dlPrefix):
			encodedObj = strings.TrimPrefix(rawPath, dlPrefix)
			isDownload = true
		default:
			http.Error(w, "unexpected path "+rawPath, http.StatusNotFound)
			return
		}
		objName, err := url.PathUnescape(encodedObj)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(objName, wantPrefix) {
			http.Error(w, "outside managed folder prefix", http.StatusForbidden)
			return
		}

		mu.Lock()
		obj, exists := objects[objName]
		if r.Method == http.MethodDelete {
			delete(objects, objName)
			mu.Unlock()
			if !exists {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 404, "message": "not found"}})
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		mu.Unlock()

		if !exists {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 404, "message": "not found"}})
			return
		}
		if isDownload {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(obj.body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"bucket":  wantBucket,
			"name":    objName,
			"size":    strconv.Itoa(len(obj.body)),
			"updated": obj.updated,
		})
	}))
	defer srv.Close()

	runStoreSuite(t, func(t *testing.T) Store {
		mu.Lock()
		objects = make(map[string]storedObj)
		mu.Unlock()

		s, err := Open(
			context.Background(),
			"gs://"+wantBucket+"/"+wantPrefix,
			WithClientOptions(
				option.WithEndpoint(srv.URL+"/storage/v1/"),
				option.WithHTTPClient(srv.Client()),
				option.WithoutAuthentication(),
			),
		)
		if err != nil {
			t.Fatalf("Open gs://: %v", err)
		}
		return s
	})
}
