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

//go:build ignore

// ociregistry serves an in-memory OCI Distribution Spec v2 HTTP registry for
// §9.2 so docker push, docker pull, and docker buildx imagetools inspect/create
// run end-to-end against real manifests and content-addressed blobs, with an
// optional /_corrupt_next_manifest toggle to test fail-closed digest checks.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

type manifestRecord struct {
	mediaType string
	digest    string
	raw       []byte
}

func computeDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func main() {
	addr := flag.String("addr", "127.0.0.1:4281", "listen address")
	name := flag.String("name", "hub", "registry label")
	flag.Parse()

	var (
		mu             sync.Mutex
		blobs          = map[string][]byte{}
		uploads        = map[string][]byte{}
		manifestsByRef = map[string]manifestRecord{}
		events         []string
		uploadSeq      atomic.Int64
		corruptNextPut bool
	)

	recordEvent := func(ev string) {
		events = append(events, ev)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/_log", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodDelete {
			events = nil
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"name": *name, "events": events})
	})
	mux.HandleFunc("/_corrupt_next_manifest", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		corruptNextPut = r.URL.Query().Get("enable") != "false"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"corruptNextPut": corruptNextPut})
	})
	mux.HandleFunc("/_delete_manifest", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		delete(manifestsByRef, r.URL.Query().Get("repo")+"@"+r.URL.Query().Get("ref"))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
		if r.URL.Path == "/v2/" || r.URL.Path == "/v2" {
			w.WriteHeader(http.StatusOK)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/v2/")

		if idx := strings.Index(rest, "/blobs/uploads"); idx > 0 {
			repo := rest[:idx]
			tail := strings.TrimPrefix(strings.TrimPrefix(rest[idx:], "/blobs/uploads"), "/")

			if r.Method == http.MethodPost && tail == "" {
				if mount := r.URL.Query().Get("mount"); mount != "" {
					mu.Lock()
					_, ok := blobs[mount]
					if ok {
						recordEvent(fmt.Sprintf("MOUNT %s %s", repo, mount))
					}
					mu.Unlock()
					if ok {
						w.Header().Set("Docker-Content-Digest", mount)
						w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", repo, mount))
						w.WriteHeader(http.StatusCreated)
						return
					}
				}
				if d := r.URL.Query().Get("digest"); d != "" {
					data, _ := io.ReadAll(r.Body)
					got := computeDigest(data)
					mu.Lock()
					blobs[got] = data
					recordEvent(fmt.Sprintf("BLOB_PUT %s %s (%dB)", repo, got, len(data)))
					mu.Unlock()
					w.Header().Set("Docker-Content-Digest", got)
					w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", repo, got))
					w.WriteHeader(http.StatusCreated)
					return
				}
				id := fmt.Sprintf("up-%d", uploadSeq.Add(1))
				mu.Lock()
				uploads[id] = nil
				mu.Unlock()
				w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, id))
				w.Header().Set("Docker-Upload-UUID", id)
				w.Header().Set("Range", "0-0")
				w.WriteHeader(http.StatusAccepted)
				return
			}

			if tail != "" && r.Method == http.MethodPatch {
				chunk, _ := io.ReadAll(r.Body)
				mu.Lock()
				uploads[tail] = append(uploads[tail], chunk...)
				end := len(uploads[tail]) - 1
				if end < 0 {
					end = 0
				}
				mu.Unlock()
				w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, tail))
				w.Header().Set("Docker-Upload-UUID", tail)
				w.Header().Set("Range", fmt.Sprintf("0-%d", end))
				w.WriteHeader(http.StatusAccepted)
				return
			}

			if tail != "" && r.Method == http.MethodPut {
				chunk, _ := io.ReadAll(r.Body)
				mu.Lock()
				buf := append(uploads[tail], chunk...)
				delete(uploads, tail)
				got := computeDigest(buf)
				blobs[got] = buf
				recordEvent(fmt.Sprintf("BLOB_PUT %s %s (%dB)", repo, got, len(buf)))
				mu.Unlock()
				w.Header().Set("Docker-Content-Digest", got)
				w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", repo, got))
				w.WriteHeader(http.StatusCreated)
				return
			}
		}

		if idx := strings.Index(rest, "/blobs/"); idx > 0 {
			digest := rest[idx+len("/blobs/"):]
			mu.Lock()
			data, ok := blobs[digest]
			mu.Unlock()
			if !ok {
				http.Error(w, `{"errors":[{"code":"BLOB_UNKNOWN"}]}`, http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			_, _ = w.Write(data)
			return
		}

		if idx := strings.Index(rest, "/tags/list"); idx > 0 {
			repo := rest[:idx]
			prefix := repo + "@"
			var tags []string
			mu.Lock()
			for k := range manifestsByRef {
				if strings.HasPrefix(k, prefix) {
					ref := strings.TrimPrefix(k, prefix)
					if !strings.HasPrefix(ref, "sha256:") {
						tags = append(tags, ref)
					}
				}
			}
			mu.Unlock()
			sort.Strings(tags)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": tags})
			return
		}

		if idx := strings.Index(rest, "/manifests/"); idx > 0 {
			repo := rest[:idx]
			ref := rest[idx+len("/manifests/"):]
			switch r.Method {
			case http.MethodPut:
				raw, _ := io.ReadAll(r.Body)
				ct := r.Header.Get("Content-Type")
				if ct == "" {
					ct = "application/vnd.oci.image.manifest.v1+json"
				}
				putDigest := computeDigest(raw)
				storedRaw := raw
				mu.Lock()
				if corruptNextPut && !strings.HasPrefix(ref, "sha256:") {
					corruptNextPut = false
					storedRaw = append(append([]byte(nil), raw...), '\n')
				}
				dgst := computeDigest(storedRaw)
				rec := manifestRecord{mediaType: ct, digest: dgst, raw: storedRaw}
				manifestsByRef[repo+"@"+ref] = rec
				manifestsByRef[repo+"@"+dgst] = rec
				blobs[dgst] = storedRaw
				recordEvent(fmt.Sprintf("MANIFEST_PUT %s:%s (%s)", repo, ref, dgst))
				mu.Unlock()
				w.Header().Set("Docker-Content-Digest", putDigest)
				w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", repo, putDigest))
				w.WriteHeader(http.StatusCreated)
				return
			case http.MethodGet, http.MethodHead:
				mu.Lock()
				rec, ok := manifestsByRef[repo+"@"+ref]
				if ok {
					recordEvent(fmt.Sprintf("MANIFEST_%s %s:%s -> %s", r.Method, repo, ref, rec.digest))
				}
				mu.Unlock()
				if !ok {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`))
					return
				}
				w.Header().Set("Content-Type", rec.mediaType)
				w.Header().Set("Docker-Content-Digest", rec.digest)
				w.Header().Set("Content-Length", strconv.Itoa(len(rec.raw)))
				if r.Method == http.MethodHead {
					w.WriteHeader(http.StatusOK)
					return
				}
				_, _ = w.Write(rec.raw)
				return
			}
		}
		http.NotFound(w, r)
	})

	log.Printf("ociregistry (%s) listening on http://%s", *name, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
