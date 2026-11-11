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

// forge.go is the external config repository's forge for the
// 2026-11-11 §11 walkthrough: one loopback HTTP server that is both
// halves of a GitHub host for acme/grain-config --
//
//   - git smart HTTP for the bare repository (git http-backend), so the
//     daemon clones and fetches it as https://github.com/... would be;
//   - the three REST calls MergedPullRequestReview makes
//     (GET /repos/{o}/{r}/commits/{sha}/pulls, /pulls/{n},
//     /pulls/{n}/reviews), answered from a JSON file the walkthrough
//     rewrites as it "merges" pull requests.
//
// pkg/github/githubsim does not model merge_commit_sha on the commit's
// pull list, merged_by, or a review's commit_id/author_association, so
// this double serves exactly those fields and logs every other request.
//
//	go run forge.go -addr 127.0.0.1:4291 -root /tmp/forge -pulls /tmp/forge/pulls.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type review struct {
	User              string `json:"user"`
	State             string `json:"state"`
	CommitID          string `json:"commit_id"`
	AuthorAssociation string `json:"author_association"`
}

type pull struct {
	Number         int      `json:"number"`
	Base           string   `json:"base"`
	MergeCommitSHA string   `json:"merge_commit_sha"`
	HeadSHA        string   `json:"head_sha"`
	Author         string   `json:"author"`
	MergedBy       string   `json:"merged_by"`
	Reviews        []review `json:"reviews"`
}

var (
	commitPulls = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/commits/([0-9a-f]+)/pulls$`)
	onePull     = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/(\d+)$`)
	pullReviews = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/(\d+)/reviews$`)
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4291", "listen address")
	root := flag.String("root", "", "GIT_PROJECT_ROOT: directory holding <owner>/<repo>.git")
	pullsFile := flag.String("pulls", "", "JSON array of merged pull requests")
	flag.Parse()
	backend, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		log.Fatal(err)
	}
	git := &cgi.Handler{
		Path: strings.TrimSpace(string(backend)) + "/git-http-backend",
		Env:  []string{"GIT_PROJECT_ROOT=" + *root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	load := func() []pull {
		var out []pull
		data, err := os.ReadFile(*pullsFile)
		if err == nil {
			_ = json.Unmarshal(data, &out)
		}
		return out
	}
	find := func(n int) *pull {
		for _, p := range load() {
			if p.Number == n {
				return &p
			}
		}
		return nil
	}
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		log.Printf("%s %s auth=%t", r.Method, r.URL.RequestURI(), r.Header.Get("Authorization") != "")
		if strings.Contains(p, ".git/") {
			git.ServeHTTP(w, r)
			return
		}
		if m := commitPulls.FindStringSubmatch(p); m != nil {
			items := []map[string]any{}
			for _, pr := range load() {
				if pr.MergeCommitSHA == m[3] {
					items = append(items, map[string]any{"number": pr.Number, "merged_at": "2026-11-11T20:00:00Z",
						"merge_commit_sha": pr.MergeCommitSHA, "base": map[string]string{"ref": pr.Base}})
				}
			}
			reply(w, items)
			return
		}
		if m := onePull.FindStringSubmatch(p); m != nil {
			n, _ := strconv.Atoi(m[3])
			pr := find(n)
			if pr == nil {
				http.Error(w, `{"message":"Not Found"}`, 404)
				return
			}
			out := map[string]any{"number": n, "merged": true, "merge_commit_sha": pr.MergeCommitSHA,
				"html_url": fmt.Sprintf("http://%s/%s/%s/pull/%d", r.Host, m[1], m[2], n),
				"user":     map[string]string{"login": pr.Author}, "head": map[string]string{"sha": pr.HeadSHA}}
			if pr.MergedBy != "" {
				out["merged_by"] = map[string]string{"login": pr.MergedBy}
			}
			reply(w, out)
			return
		}
		if m := pullReviews.FindStringSubmatch(p); m != nil {
			n, _ := strconv.Atoi(m[3])
			pr := find(n)
			if pr == nil {
				http.Error(w, `{"message":"Not Found"}`, 404)
				return
			}
			items := []map[string]any{}
			for _, rv := range pr.Reviews {
				items = append(items, map[string]any{"state": rv.State, "commit_id": rv.CommitID,
					"author_association": rv.AuthorAssociation, "user": map[string]string{"login": rv.User}})
			}
			reply(w, items)
			return
		}
		http.Error(w, `{"message":"Not Found"}`, 404)
	})
	log.Printf("forge for %s on http://%s", *root, *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
