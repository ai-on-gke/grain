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

// ghesim serves pkg/github/githubsim as a GitHub Enterprise Server: REST
// under /api/v3 over HTTPS, for walkthrough 2026-11-13 §9 item 4
// (reviewed-merge against ghe.example.com). It seeds acme/widgets with
//
//   - an approved merge: alice's pull request, approved by bob, merged by alice;
//   - a self-merge on top of it: alice's pull request merged by alice, unreviewed;
//
// and writes their shas (base, approved, self) as JSON to -shas. On the
// plain-HTTP -ctl address:
//
//	POST /_fail?n=N&status=502   the next N API requests answer status
//	GET  /_calls                 every API request, in order (method path status)
//
//	sudo go run ghesim.go -addr 127.0.0.1:443 -ctl 127.0.0.1:4297 \
//	  -cert ghe.pem -key ghe.key -dir /tmp/s9-ghe -shas /tmp/s9-ghe/shas.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

type repo struct {
	sim        *githubsim.Sim
	bare, work string
	n          int
}

func (r *repo) git(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *repo) commit(msg string) {
	r.n++
	if err := os.WriteFile(filepath.Join(r.work, "f.txt"), []byte(fmt.Sprintf("%d %s\n", r.n, msg)), 0o644); err != nil {
		log.Fatal(err)
	}
	r.git(r.work, "add", "f.txt")
	r.git(r.work, "commit", "-q", "-m", msg)
}

func (r *repo) main() string { return r.git(r.work, "--git-dir", r.bare, "rev-parse", "main") }

// pull opens branch's pull request by author and merges it as merger,
// with approvers' approvals of its head; it returns main afterwards.
func (r *repo) pull(branch, author, merger string, approvers ...string) string {
	r.git(r.work, "fetch", "-q", "origin")
	r.git(r.work, "checkout", "-q", "-B", branch, "origin/main")
	r.commit(branch)
	r.git(r.work, "push", "-q", "-f", "origin", branch)
	body, _ := json.Marshal(map[string]string{"title": branch, "head": branch, "base": "main"})
	resp, err := r.sim.Request("POST", "/repos/acme/widgets/pulls", nil, body)
	if err != nil || resp.Status >= 300 {
		log.Fatalf("open pull: %v %d %s", err, resp.Status, resp.Body)
	}
	var pr struct{ Number int }
	_ = json.Unmarshal(resp.Body, &pr)
	for i := range r.sim.PullRequests {
		if r.sim.PullRequests[i].Number == pr.Number {
			r.sim.PullRequests[i].Author = author
		}
	}
	if len(approvers) > 0 {
		if r.sim.Approvals == nil {
			r.sim.Approvals = map[int][]string{}
		}
		r.sim.Approvals[pr.Number] = approvers
	}
	r.sim.MergeAs = merger
	resp, err = r.sim.Request("PUT", fmt.Sprintf("/repos/acme/widgets/pulls/%d/merge", pr.Number), nil, []byte(`{"merge_method":"merge"}`))
	if err != nil || resp.Status >= 300 {
		log.Fatalf("merge #%d: %v %d %s", pr.Number, err, resp.Status, resp.Body)
	}
	return r.main()
}

func main() {
	addr := flag.String("addr", "127.0.0.1:443", "HTTPS address ghe.example.com resolves to")
	ctl := flag.String("ctl", "127.0.0.1:4297", "plain-HTTP control address")
	cert := flag.String("cert", "", "TLS certificate for ghe.example.com")
	key := flag.String("key", "", "TLS key")
	dir := flag.String("dir", "/tmp/s9-ghe", "work directory")
	shas := flag.String("shas", "", "where to write the seeded shas")
	flag.Parse()

	_ = os.RemoveAll(filepath.Join(*dir, "repo"))
	r := &repo{bare: filepath.Join(*dir, "repo", "widgets.git"), work: filepath.Join(*dir, "repo", "work")}
	_ = os.MkdirAll(filepath.Dir(r.bare), 0o755)
	r.git(*dir, "init", "-q", "--bare", "-b", "main", r.bare)
	r.git(*dir, "clone", "-q", r.bare, r.work)
	r.git(r.work, "checkout", "-q", "-b", "main")
	r.commit("seed")
	r.git(r.work, "push", "-q", "origin", "main")
	r.sim = githubsim.New("acme", "widgets", r.bare, "main")
	r.sim.RecordMergeCommits = true
	seeded := map[string]string{"base": r.main()}
	seeded["approved"] = r.pull("approved", "alice", "alice", "bob")
	seeded["self"] = r.pull("self", "alice", "alice")
	if *shas != "" {
		b, _ := json.Marshal(seeded)
		if err := os.WriteFile(*shas, b, 0o644); err != nil {
			log.Fatal(err)
		}
	}

	var mu sync.Mutex
	failN, failStatus := 0, 502
	var calls []string
	api := r.sim.Handler()
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/_fail", func(w http.ResponseWriter, req *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			failN, _ = strconv.Atoi(req.URL.Query().Get("n"))
			if s, err := strconv.Atoi(req.URL.Query().Get("status")); err == nil {
				failStatus = s
			}
			w.WriteHeader(204)
		})
		mux.HandleFunc("/_calls", func(w http.ResponseWriter, req *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			fmt.Fprintln(w, strings.Join(calls, "\n"))
		})
		log.Fatal(http.ListenAndServe(*ctl, mux))
	}()
	log.Printf("ghesim: https://%s/api/v3 (control http://%s), shas %v", *addr, *ctl, seeded)
	log.Fatal(http.ListenAndServeTLS(*addr, *cert, *key, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		line := req.Method + " " + req.Host + req.URL.RequestURI()
		if failN > 0 {
			failN--
			calls = append(calls, line+" -> "+strconv.Itoa(failStatus)+" (injected)")
			http.Error(w, `{"message":"Bad Gateway"}`, failStatus)
			return
		}
		if !strings.HasPrefix(req.URL.Path, "/api/v3/") {
			calls = append(calls, line+" -> 404 (not under /api/v3)")
			http.NotFound(w, req)
			return
		}
		calls = append(calls, line)
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/api/v3")
		req.RequestURI = req.URL.RequestURI()
		api.ServeHTTP(w, req)
	})))
}
