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

// releases_fake is a stateful, writable fake of the GitHub Releases and git
// refs REST API for walkthrough 2026-11-08 §5 item 1: scripts/release-assets.sh
// creates releases, uploads, deletes and renames assets and moves tags against
// it, and a live `grain daemon -github-host 127.0.0.1:PORT -github-insecure-http`
// reads the result through the same /api/v3 paths. Every request is appended,
// in order, to the log GET /_log returns (DELETE /_log clears it).
//
//	go run releases_fake.go -addr 127.0.0.1:4380 -repo ai-on-gke/grain
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type asset struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Size        int    `json:"size"`
	ContentType string `json:"content_type"`
	State       string `json:"state"`
	URL         string `json:"url"`
	Download    string `json:"browser_download_url"`
	data        []byte
}

type release struct {
	ID         int64    `json:"id"`
	TagName    string   `json:"tag_name"`
	Target     string   `json:"target_commitish"`
	Name       string   `json:"name"`
	Body       string   `json:"body"`
	Draft      bool     `json:"draft"`
	Prerelease bool     `json:"prerelease"`
	CreatedAt  string   `json:"created_at"`
	Published  string   `json:"published_at"`
	UploadURL  string   `json:"upload_url"`
	URL        string   `json:"url"`
	Assets     []*asset `json:"assets"`
}

var (
	mu       sync.Mutex
	nextID   int64 = 1000
	releases       = map[int64]*release{}
	tags           = map[string]string{} // tag -> commit
	latestID int64
	reqLog   []string
	base     string
	repo     string
)

func newID() int64 { nextID++; return nextID }

func byTag(tag string) *release {
	for _, r := range releases {
		if r.TagName == tag {
			return r
		}
	}
	return nil
}

func assetByID(id int64) (*release, *asset, int) {
	for _, r := range releases {
		for i, a := range r.Assets {
			if a.ID == id {
				return r, a, i
			}
		}
	}
	return nil, nil, -1
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

var (
	reReleases  = regexp.MustCompile(`^/api/v3/repos/([^/]+/[^/]+)/releases$`)
	reByTag     = regexp.MustCompile(`^/api/v3/repos/([^/]+/[^/]+)/releases/tags/(.+)$`)
	reLatest    = regexp.MustCompile(`^/api/v3/repos/([^/]+/[^/]+)/releases/latest$`)
	reRelease   = regexp.MustCompile(`^/api/v3/repos/([^/]+/[^/]+)/releases/(\d+)$`)
	reAssets    = regexp.MustCompile(`^/api/v3/repos/([^/]+/[^/]+)/releases/(\d+)/assets$`)
	reAsset     = regexp.MustCompile(`^/api/v3/repos/([^/]+/[^/]+)/releases/assets/(\d+)$`)
	reUpload    = regexp.MustCompile(`^/api/uploads/repos/([^/]+/[^/]+)/releases/(\d+)/assets$`)
	reRefTag    = regexp.MustCompile(`^/api/v3/repos/([^/]+/[^/]+)/git/refs/tags/(.+)$`)
	reRefs      = regexp.MustCompile(`^/api/v3/repos/([^/]+/[^/]+)/git/refs$`)
	reDownload  = regexp.MustCompile(`^/([^/]+/[^/]+)/releases/download/([^/]+)/(.+)$`)
	reRepoRoot  = regexp.MustCompile(`^/api/v3/repos/([^/]+/[^/]+)$`)
	reMatchRefs = regexp.MustCompile(`^/api/v3/repos/([^/]+/[^/]+)/git/matching-refs/tags/?(.*)$`)
)

func serveAsset(w http.ResponseWriter, a *asset) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(a.data)))
	_, _ = w.Write(a.data)
}

func sortedReleases() []*release {
	out := make([]*release, 0, len(releases))
	for _, r := range releases {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

func handler(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	p := r.URL.Path
	if p == "/_log" {
		if r.Method == http.MethodDelete {
			reqLog = nil
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, strings.Join(reqLog, "\n"))
		return
	}
	if p == "/_state" {
		writeJSON(w, 200, map[string]any{"releases": sortedReleases(), "tags": tags, "latest": latestID})
		return
	}
	if p == "/_tag" && r.Method == http.MethodPost {
		// /_tag?name=latest&sha=...: what a person pushing a git tag does.
		tags[r.URL.Query().Get("name")] = r.URL.Query().Get("sha")
		return
	}
	entry := r.Method + " " + p
	if q := r.URL.Query().Get("name"); q != "" {
		entry += "?name=" + q
	}
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	if r.Method == http.MethodPatch && len(body) > 0 && len(body) < 300 {
		entry += " " + strings.TrimSpace(string(body))
	}
	reqLog = append(reqLog, entry)

	switch {
	case reRepoRoot.MatchString(p):
		writeJSON(w, 200, map[string]any{"full_name": repo, "default_branch": "main"})
	case reLatest.MatchString(p):
		if rel := releases[latestID]; rel != nil {
			writeJSON(w, 200, rel)
			return
		}
		writeJSON(w, 404, map[string]string{"message": "Not Found"})
	case reByTag.MatchString(p):
		tag := reByTag.FindStringSubmatch(p)[2]
		if rel := byTag(tag); rel != nil {
			writeJSON(w, 200, rel)
			return
		}
		writeJSON(w, 404, map[string]string{"message": "Not Found"})
	case reReleases.MatchString(p) && r.Method == http.MethodGet:
		writeJSON(w, 200, sortedReleases())
	case reReleases.MatchString(p) && r.Method == http.MethodPost:
		var in struct {
			TagName    string `json:"tag_name"`
			Target     string `json:"target_commitish"`
			Name       string `json:"name"`
			Body       string `json:"body"`
			Prerelease bool   `json:"prerelease"`
			MakeLatest string `json:"make_latest"`
		}
		_ = json.Unmarshal(body, &in)
		if byTag(in.TagName) != nil {
			writeJSON(w, 422, map[string]string{"message": "Validation Failed: already_exists"})
			return
		}
		id := newID()
		now := time.Now().UTC().Format(time.RFC3339)
		rel := &release{ID: id, TagName: in.TagName, Target: in.Target, Name: in.Name, Body: in.Body,
			Prerelease: in.Prerelease, CreatedAt: now, Published: now, Assets: []*asset{},
			UploadURL: fmt.Sprintf("%s/api/uploads/repos/%s/releases/%d/assets{?name,label}", base, repo, id),
			URL:       fmt.Sprintf("%s/api/v3/repos/%s/releases/%d", base, repo, id)}
		releases[id] = rel
		if _, ok := tags[in.TagName]; !ok {
			tags[in.TagName] = in.Target
		}
		if in.MakeLatest == "true" {
			latestID = id
		}
		writeJSON(w, 201, rel)
	case reRelease.MatchString(p):
		id, _ := strconv.ParseInt(reRelease.FindStringSubmatch(p)[2], 10, 64)
		rel := releases[id]
		if rel == nil {
			writeJSON(w, 404, map[string]string{"message": "Not Found"})
			return
		}
		switch r.Method {
		case http.MethodDelete:
			delete(releases, id)
			w.WriteHeader(204)
		case http.MethodPatch:
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			if in["make_latest"] == "true" {
				latestID = id
			}
			writeJSON(w, 200, rel)
		default:
			writeJSON(w, 200, rel)
		}
	case reAssets.MatchString(p):
		id, _ := strconv.ParseInt(reAssets.FindStringSubmatch(p)[2], 10, 64)
		rel := releases[id]
		if rel == nil {
			writeJSON(w, 404, map[string]string{"message": "Not Found"})
			return
		}
		writeJSON(w, 200, rel.Assets)
	case reUpload.MatchString(p) && r.Method == http.MethodPost:
		id, _ := strconv.ParseInt(reUpload.FindStringSubmatch(p)[2], 10, 64)
		rel := releases[id]
		name := r.URL.Query().Get("name")
		if rel == nil {
			writeJSON(w, 404, map[string]string{"message": "Not Found"})
			return
		}
		for _, a := range rel.Assets {
			if a.Name == name {
				writeJSON(w, 422, map[string]string{"message": "Validation Failed: already_exists"})
				return
			}
		}
		aid := newID()
		u := fmt.Sprintf("%s/api/v3/repos/%s/releases/assets/%d", base, repo, aid)
		a := &asset{ID: aid, Name: name, Size: len(body), ContentType: r.Header.Get("Content-Type"), State: "uploaded",
			URL: u, Download: fmt.Sprintf("%s/%s/releases/download/%s/%s", base, repo, rel.TagName, name), data: body}
		rel.Assets = append(rel.Assets, a)
		writeJSON(w, 201, a)
	case reAsset.MatchString(p):
		id, _ := strconv.ParseInt(reAsset.FindStringSubmatch(p)[2], 10, 64)
		rel, a, i := assetByID(id)
		if a == nil {
			writeJSON(w, 404, map[string]string{"message": "Not Found"})
			return
		}
		switch r.Method {
		case http.MethodDelete:
			rel.Assets = append(rel.Assets[:i], rel.Assets[i+1:]...)
			w.WriteHeader(204)
		case http.MethodPatch:
			var in struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(body, &in)
			if in.Name != "" {
				for _, o := range rel.Assets {
					if o.Name == in.Name && o.ID != a.ID {
						writeJSON(w, 422, map[string]string{"message": "Validation Failed: already_exists"})
						return
					}
				}
				a.Name = in.Name
				a.Download = fmt.Sprintf("%s/%s/releases/download/%s/%s", base, repo, rel.TagName, in.Name)
			}
			writeJSON(w, 200, a)
		default:
			if strings.Contains(r.Header.Get("Accept"), "octet-stream") {
				serveAsset(w, a)
				return
			}
			writeJSON(w, 200, a)
		}
	case reDownload.MatchString(p):
		m := reDownload.FindStringSubmatch(p)
		if rel := byTag(m[2]); rel != nil {
			for _, a := range rel.Assets {
				if a.Name == m[3] {
					serveAsset(w, a)
					return
				}
			}
		}
		http.NotFound(w, r)
	case reRefTag.MatchString(p):
		tag := reRefTag.FindStringSubmatch(p)[2]
		var in struct {
			SHA string `json:"sha"`
		}
		_ = json.Unmarshal(body, &in)
		switch r.Method {
		case http.MethodPatch:
			if _, ok := tags[tag]; !ok {
				writeJSON(w, 422, map[string]string{"message": "Reference does not exist"})
				return
			}
			tags[tag] = in.SHA
			writeJSON(w, 200, map[string]any{"ref": "refs/tags/" + tag, "object": map[string]string{"sha": in.SHA}})
		case http.MethodGet:
			if c, ok := tags[tag]; ok {
				writeJSON(w, 200, map[string]any{"ref": "refs/tags/" + tag, "object": map[string]string{"sha": c, "type": "commit"}})
				return
			}
			writeJSON(w, 404, map[string]string{"message": "Not Found"})
		case http.MethodDelete:
			delete(tags, tag)
			w.WriteHeader(204)
		}
	case reRefs.MatchString(p) && r.Method == http.MethodPost:
		var in struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		}
		_ = json.Unmarshal(body, &in)
		tag := strings.TrimPrefix(in.Ref, "refs/tags/")
		if _, ok := tags[tag]; ok {
			writeJSON(w, 422, map[string]string{"message": "Reference already exists"})
			return
		}
		tags[tag] = in.SHA
		writeJSON(w, 201, map[string]any{"ref": in.Ref, "object": map[string]string{"sha": in.SHA}})
	case reMatchRefs.MatchString(p):
		var out []any
		for t, c := range tags {
			out = append(out, map[string]any{"ref": "refs/tags/" + t, "object": map[string]string{"sha": c, "type": "commit"}})
		}
		writeJSON(w, 200, out)
	default:
		writeJSON(w, 404, map[string]string{"message": "Not Found: " + p})
	}
}

func main() {
	addr := flag.String("addr", "", "listen address (required; the run scripts pass one)")
	flag.StringVar(&repo, "repo", "ai-on-gke/grain", "owner/name")
	flag.Parse()
	if *addr == "" {
		log.Fatal("-addr is required")
	}
	base = "http://" + *addr
	log.Printf("releases_fake on %s for %s", base, repo)
	log.Fatal(http.ListenAndServe(*addr, http.HandlerFunc(handler)))
}
