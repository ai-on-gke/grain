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

// ghsim serves acme/widgets for the repo-skills walkthrough: the GitHub
// simulator's REST API (pkg/github/githubsim) plus smart-HTTP git, with
// receive-pack enabled so a dispatched run can push its working branch.
package main

import (
	"flag"
	"log"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4283", "listen address")
	bare := flag.String("bare", "", "bare repository backing acme/widgets")
	flag.Parse()

	sim := githubsim.New("acme", "widgets", *bare, "main")
	simHandler := sim.Handler()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		log.Fatal(err)
	}
	gitBackend := &cgi.Handler{
		Path: gitPath,
		Root: "/acme",
		Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + filepath.Dir(*bare), "GIT_HTTP_EXPORT_ALL=1",
			"REMOTE_USER=walkthrough"},
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/acme/widgets.git/") || strings.HasPrefix(r.URL.Path, "/acme/widgets/") {
			r.URL.Path = strings.Replace(r.URL.Path, "/acme/widgets/", "/acme/widgets.git/", 1)
			gitBackend.ServeHTTP(w, r)
			return
		}
		simHandler.ServeHTTP(w, r)
	})
	log.Printf("ghsim: acme/widgets on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
