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

// idlegate runs the `idle-gate` step of .github/workflows/nightly.yml,
// exactly as committed, against a fake Actions API on a loopback listener,
// for walkthrough 2026-11-11 §7 item 4. Each scenario sets the event, the
// runs the API answers with (or an HTTP error / garbage), and checks the
// step's `run=` output.
//
//	go run docs/test-runs/2026-11-11-release-supply-chain/idlegate.go [repo-root]
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type run struct {
	ID         int    `json:"id"`
	HeadBranch string `json:"head_branch"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	UpdatedAt  string `json:"updated_at"`
	HTMLURL    string `json:"html_url"`
}

type scenario struct {
	name   string
	event  string
	status int           // HTTP status the API answers with; 0 = 200
	body   string        // raw body instead of pages
	pages  map[int][]run // page number -> runs
	want   string        // expected run= output
	expect string        // substring expected in the log
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	script := gateScript(filepath.Join(root, ".github/workflows/nightly.yml"))
	now := time.Date(2026, 11, 11, 2, 11, 0, 0, time.UTC)
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	mains := func(n int, d time.Duration) []run {
		var rs []run
		for i := 0; i < n; i++ {
			rs = append(rs, run{ID: 1000 + i, HeadBranch: "main", Status: "completed", Conclusion: "success", UpdatedAt: ago(d), HTMLURL: "https://example.test/r/" + strconv.Itoa(1000+i)})
		}
		return rs
	}
	rc := func(tag, concl string, d time.Duration) run {
		return run{ID: 7, HeadBranch: tag, Status: "completed", Conclusion: concl, UpdatedAt: ago(d), HTMLURL: "https://example.test/r/7"}
	}
	scenarios := []scenario{
		{name: "tag push always runs", event: "push", want: "true", expect: "event is push"},
		{name: "workflow_dispatch always runs", event: "workflow_dispatch", want: "true", expect: "event is workflow_dispatch"},
		{name: "schedule, rc green 3h ago -> skip", event: "schedule", pages: map[int][]run{1: {rc("v3.7.0-rc.2", "success", 3*time.Hour)}}, want: "false", expect: "v3.7.0-rc.2's Stage 1 concluded success"},
		{name: "schedule, rc red 23h ago -> skip (any verdict counts)", event: "schedule", pages: map[int][]run{1: {rc("v3.7.0-rc.2", "failure", 23*time.Hour)}}, want: "false", expect: "concluded failure"},
		{name: "schedule, rc cancelled 1h ago -> run", event: "schedule", pages: map[int][]run{1: {rc("v3.7.0-rc.2", "cancelled", time.Hour)}}, want: "true", expect: "no vX.Y.Z-rc.N Stage 1 run"},
		{name: "schedule, rc 25h ago -> run", event: "schedule", pages: map[int][]run{1: {rc("v3.7.0-rc.2", "success", 25*time.Hour)}}, want: "true", expect: "no vX.Y.Z-rc.N Stage 1 run"},
		{name: "schedule, only main runs -> run", event: "schedule", pages: map[int][]run{1: mains(3, time.Hour)}, want: "true", expect: "no vX.Y.Z-rc.N Stage 1 run"},
		{name: "schedule, refs/tags/ prefix -> skip", event: "schedule", pages: map[int][]run{1: {rc("refs/tags/v3.7.0-rc.3", "success", time.Hour)}}, want: "false", expect: "skipping"},
		{name: "schedule, rc on page 2 behind 100 recent main runs -> skip", event: "schedule", pages: map[int][]run{1: mains(100, time.Hour), 2: {rc("v3.7.0-rc.4", "success", 2*time.Hour)}}, want: "false", expect: "v3.7.0-rc.4"},
		{name: "schedule, fractional-second timestamps", event: "schedule", pages: map[int][]run{1: {{ID: 8, HeadBranch: "v3.7.0-rc.5", Status: "completed", Conclusion: "success", UpdatedAt: now.Add(-time.Hour).Format("2006-01-02T15:04:05.000Z")}}}, want: "false", expect: "v3.7.0-rc.5"},
		{name: "schedule, API 500 -> fail open", event: "schedule", status: 500, want: "true", expect: "could not list nightly.yml runs"},
		{name: "schedule, API 403 -> fail open", event: "schedule", status: 403, want: "true", expect: "could not list nightly.yml runs"},
		{name: "schedule, API answers HTML -> fail open", event: "schedule", body: "<html>rate limited</html>", want: "true", expect: "could not read the Actions API's answer"},
		{name: "schedule, API answers {} -> run", event: "schedule", body: "{}", want: "true", expect: "no vX.Y.Z-rc.N Stage 1 run"},
	}
	failed := 0
	for _, sc := range scenarios {
		got, log := runOne(script, sc, now)
		verdict := "PASS"
		if got != sc.want || !strings.Contains(log, sc.expect) {
			verdict = "FAIL"
			failed++
		}
		fmt.Printf("=== %s: %s\n    run=%s (want %s)\n", verdict, sc.name, got, sc.want)
		for _, l := range strings.Split(strings.TrimSpace(log), "\n") {
			fmt.Printf("    | %s\n", l)
		}
	}
	fmt.Printf("%d scenario(s), %d failed\n", len(scenarios), failed)
	if failed > 0 {
		os.Exit(1)
	}
}

func gateScript(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				ID  string `yaml:"id"`
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		panic(err)
	}
	for _, s := range wf.Jobs["idle-gate"].Steps {
		if s.ID == "gate" {
			return s.Run
		}
	}
	panic("no idle-gate/gate step")
}

func runOne(script string, sc scenario, now time.Time) (string, string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repos/acme/widgets/actions/workflows/nightly.yml/runs") {
			http.NotFound(w, r)
			return
		}
		if sc.status != 0 {
			http.Error(w, "boom", sc.status)
			return
		}
		if sc.body != "" {
			fmt.Fprint(w, sc.body)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		rs := sc.pages[page]
		if rs == nil {
			rs = []run{}
		}
		json.NewEncoder(w).Encode(map[string]any{"total_count": len(rs), "workflow_runs": rs})
	})}
	go srv.Serve(ln)
	defer srv.Close()
	dir, _ := os.MkdirTemp("", "idlegate")
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "out")
	os.WriteFile(out, nil, 0o644)
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(),
		"EVENT_NAME="+sc.event, "GITHUB_TOKEN=fake", "GITHUB_REPOSITORY=acme/widgets",
		"GITHUB_API_URL=http://"+ln.Addr().String(), "GITHUB_OUTPUT="+out,
		"IDLE_GATE_NOW_EPOCH="+strconv.FormatInt(now.Unix(), 10))
	b, err := cmd.CombinedOutput()
	log := string(b)
	if err != nil {
		log += "\n(step exited: " + err.Error() + ")"
	}
	o, _ := os.ReadFile(out)
	got := ""
	for _, l := range strings.Split(string(o), "\n") {
		if v, ok := strings.CutPrefix(l, "run="); ok {
			got = v
		}
	}
	return got, log
}
