//go:build ignore

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

// ghsim serves pkg/github/githubsim's REST API plus git smart-HTTP
// (git-http-backend) over plain HTTP for acme/widgets and acme/other so a
// real `grain daemon` resolves both repositories and syncs acme/widgets's
// .grain/templates/review.yml over git ls-remote + git fetch.
package main

import (
	"flag"
	"log"
	"net/http"
	"net/http/cgi"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

var gitHTTPPath = regexp.MustCompile(`^/[^/]+/[^/]+/(info/refs|git-upload-pack|git-receive-pack)$`)

func main() {
	addr := flag.String("addr", "127.0.0.1:4294", "listen address")
	gitRoot := flag.String("git-root", "", "directory containing acme/widgets.git and acme/other.git")
	def := flag.String("default", "main", "default branch name")
	flag.Parse()

	widgetsBare := filepath.Join(*gitRoot, "acme", "widgets.git")
	otherBare := filepath.Join(*gitRoot, "acme", "other.git")
	widgetsSim := githubsim.New("acme", "widgets", widgetsBare, *def)
	otherSim := githubsim.New("acme", "other", otherBare, *def)
	widgetsH := widgetsSim.Handler()
	otherH := otherSim.Handler()
	gitCGI := &cgi.Handler{
		Path: "/usr/lib/git-core/git-http-backend",
		Env: []string{
			"GIT_PROJECT_ROOT=" + *gitRoot,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}

	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gitHTTPPath.MatchString(r.URL.Path) {
			r2 := r.Clone(r.Context())
			r2.Header.Del("Authorization")
			gitCGI.ServeHTTP(w, r2)
			return
		}
		p := strings.ToLower(r.URL.Path)
		if strings.Contains(p, "/acme/other") {
			otherH.ServeHTTP(w, r)
			return
		}
		widgetsH.ServeHTTP(w, r)
	})
	log.Printf("ghsim: acme/widgets + acme/other (default %s, git-root %s) on %s", *def, *gitRoot, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
