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

// githost <addr> <root>: bare repositories under <root>/<owner>/<name>.git
// served over smart HTTP as the deployment's GitHub, every push accepted
// and every request logged (method, path, the auth user).
package main

import (
	"log"
	"net/http"
	"net/http/cgi"
	"os"
)

func main() {
	addr, root := os.Args[1], os.Args[2]
	h := &cgi.Handler{Path: "/usr/bin/git", Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=walk"}}
	log.Fatal(http.ListenAndServe(addr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _, _ := r.BasicAuth()
		log.Printf("%s %s?%s user=%q", r.Method, r.URL.Path, r.URL.RawQuery, u)
		h.ServeHTTP(w, r)
	})))
}
