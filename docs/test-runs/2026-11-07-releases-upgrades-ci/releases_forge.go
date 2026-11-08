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

// releases_forge serves a local GitHub Releases REST API for walkthrough §9.1:
// schema v2 channel pointers, tagged-not-published v3.2.1 (#749), mid-update
// images.json.next-only states (#661), dedicated edition bare vX.Y.Z tags (#772),
// slash-branch channel tags (#799), asset-only mode without images.json, and
// swapped-tarball signature/image-ID mismatch modes (#661 a2681b21).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

type ghAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	URL                string `json:"url"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	ID         int64     `json:"id"`
	TagName    string    `json:"tag_name"`
	Name       string    `json:"name"`
	Body       string    `json:"body"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Assets     []ghAsset `json:"assets"`
}

type assetBlob struct {
	name string
	data []byte
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func mustRead(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("reading %s: %v", path, err)
	}
	return b
}

func main() {
	addr := flag.String("addr", "127.0.0.1:4280", "listen address")
	tarballPath := flag.String("tarball", "/tmp/item1-build/grain-image.tar.gz", "primary v3.2.0 OCI tarball")
	imageID := flag.String("image-id", "", "primary v3.2.0 docker image ID (sha256:...)")
	swappedPath := flag.String("swapped-tarball", "/tmp/item1-build/grain-image-swapped.tar.gz", "swapped OCI tarball")
	swappedID := flag.String("swapped-image-id", "", "swapped docker image ID (sha256:...)")
	commitSHA := flag.String("commit", "3200320032003200320032003200320032003200", "40-hex commit in images.json")
	initialMode := flag.String("mode", "normal", "initial mode: normal|mid-update|asset-only|swapped-sig|swapped-id")
	flag.Parse()

	tar1 := mustRead(*tarballPath)
	sha1 := sha256Hex(tar1)
	sidecar1 := []byte(fmt.Sprintf("%s  grain-image.tar.gz\n", sha1))

	tar2 := mustRead(*swappedPath)
	sha2 := sha256Hex(tar2)
	sidecar2 := []byte(fmt.Sprintf("%s  grain-image.tar.gz\n", sha2))

	var mu sync.Mutex
	mode := *initialMode
	var reqLog []string

	record := func(r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		reqLog = append(reqLog, fmt.Sprintf("%s %s (Accept=%s)", r.Method, r.URL.RequestURI(), r.Header.Get("Accept")))
	}

	buildState := func(baseURL, curMode, repoSlug string) ([]ghRelease, map[int64]assetBlob) {
		blobs := map[int64]assetBlob{}
		nextID := int64(100)
		mkAsset := func(name string, content []byte) ghAsset {
			nextID++
			id := nextID
			blobs[id] = assetBlob{name: name, data: content}
			u := fmt.Sprintf("%s/api/v3/repos/%s/releases/assets/%d", baseURL, repoSlug, id)
			return ghAsset{
				ID:                 id,
				Name:               name,
				Size:               int64(len(content)),
				URL:                u,
				BrowserDownloadURL: u,
			}
		}

		mkManifest := func(schema int, ver, ref, sha, id, relPtr string) []byte {
			role := map[string]any{
				"asset":   "grain-image.tar.gz",
				"sha256":  sha,
				"imageId": id,
				"ref":     ref,
			}
			if schema == 2 {
				role["release"] = relPtr
			}
			doc := map[string]any{
				"schema":     schema,
				"repository": repoSlug,
				"commit":     *commitSHA,
				"version":    ver,
				"createdAt":  "2026-11-07T22:00:00Z",
				"images": map[string]any{
					"grain": role,
				},
			}
			raw, _ := json.MarshalIndent(doc, "", "  ")
			return append(raw, '\n')
		}

		ref := "docker.io/grainai/grain:v3.2.0"
		if strings.HasSuffix(repoSlug, "-enterprise") {
			ref = "us-docker.pkg.dev/example-prod/grain/grain:v3.2.0-enterprise"
		}

		useTar := tar1
		useSidecar := sidecar1
		useSHA := sha1
		useID := *imageID
		manifestID := *imageID

		switch curMode {
		case "swapped-sig":
			// Attacker replaced both tarball, sidecar, and images.json imageId
			// with swapped image #2, while operator VerifyCmd only trusts image #1.
			useTar = tar2
			useSidecar = sidecar2
			useSHA = sha2
			useID = *swappedID
			manifestID = *swappedID
		case "swapped-id":
			// Attacker replaced tarball and sidecar with swapped image #2, while
			// images.json still pins image #1's imageId.
			useTar = tar2
			useSidecar = sidecar2
			useSHA = sha2
			useID = *swappedID
			manifestID = *imageID
		}
		_ = useID

		var v320Assets []ghAsset
		v320Assets = append(v320Assets,
			mkAsset("grain-image.tar.gz", useTar),
			mkAsset("grain-image.tar.gz.sha256", useSidecar),
		)
		if curMode != "asset-only" {
			v320Assets = append(v320Assets, mkAsset("images.json", mkManifest(1, "v3.2.0", ref, useSHA, manifestID, "")))
		}

		v2Ptr := mkManifest(2, "v3.2.0", ref, useSHA, manifestID, "v3.2.0")
		channelAssets := func() []ghAsset {
			if curMode == "mid-update" {
				return []ghAsset{mkAsset("images.json.next", v2Ptr)}
			}
			if curMode == "asset-only" {
				return nil
			}
			return []ghAsset{mkAsset("images.json", v2Ptr)}
		}

		releases := []ghRelease{
			{
				ID:      1,
				TagName: "v3.2.1",
				Name:    "v3.2.1",
				Body:    "tagged, not published",
				Assets:  []ghAsset{},
			},
			{
				ID:      2,
				TagName: "v3.2.0",
				Name:    "v3.2.0",
				Body:    "Release v3.2.0",
				Assets:  v320Assets,
			},
			{
				ID:      3,
				TagName: "channel-latest",
				Name:    "channel-latest",
				Body:    "Moving channel latest",
				Assets:  channelAssets(),
			},
			{
				ID:      4,
				TagName: "channel-main",
				Name:    "channel-main",
				Body:    "Moving channel main",
				Assets:  channelAssets(),
			},
			{
				ID:      5,
				TagName: "channel-release-v3.2.0",
				Name:    "channel-release-v3.2.0",
				Body:    "Moving branch channel release/v3.2.0",
				Assets:  channelAssets(),
			},
		}
		return releases, blobs
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/_mode", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost {
			m := strings.TrimSpace(r.URL.Query().Get("set"))
			if m != "" {
				mode = m
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": mode})
	})
	mux.HandleFunc("/_log", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodDelete {
			reqLog = nil
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"requests": reqLog})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		mu.Lock()
		curMode := mode
		mu.Unlock()

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		baseURL := scheme + "://" + r.Host
		p := strings.TrimPrefix(r.URL.Path, "/api/v3")
		if !strings.HasPrefix(p, "/repos/") {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(strings.TrimPrefix(p, "/repos/"), "/")
		if len(parts) < 3 || parts[2] != "releases" {
			http.NotFound(w, r)
			return
		}
		repoSlug := parts[0] + "/" + parts[1]
		if repoSlug != "ai-on-gke/grain" && repoSlug != "example/grain-enterprise" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		releases, blobs := buildState(baseURL, curMode, repoSlug)

		// GET /repos/{owner}/{repo}/releases
		if len(parts) == 3 {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(releases)
			return
		}
		// GET /repos/{owner}/{repo}/releases/latest
		if len(parts) == 4 && parts[3] == "latest" {
			for _, rel := range releases {
				if rel.TagName == "v3.2.0" {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(rel)
					return
				}
			}
		}
		// GET /repos/{owner}/{repo}/releases/tags/{tag}
		if len(parts) >= 5 && parts[3] == "tags" {
			tag := strings.Join(parts[4:], "/")
			for _, rel := range releases {
				if rel.TagName == tag {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(rel)
					return
				}
			}
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		// GET /repos/{owner}/{repo}/releases/assets/{id}
		if len(parts) == 5 && parts[3] == "assets" {
			var id int64
			_, _ = fmt.Sscanf(parts[4], "%d", &id)
			blob, ok := blobs[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			if strings.Contains(r.Header.Get("Accept"), "application/octet-stream") {
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write(blob.data)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ghAsset{
				ID:   id,
				Name: blob.name,
				Size: int64(len(blob.data)),
			})
			return
		}
		http.NotFound(w, r)
	})

	log.Printf("releases_forge listening on http://%s (mode=%s)", *addr, mode)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
