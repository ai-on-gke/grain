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

// This file is docs/test-plans/2026-11-03.md §6 item 4: a method a build
// outside core contributes with capability.Register, walked end to end in
// a test-only build of grain -- this test binary, which registers the
// made-up method (capabilitytest.RegisterMethod) the way an edition's
// init() would. It is listed by a real daemon API, granted to a task by
// the bare name it retires (and recorded as the method grant), and the
// run's `grain mcpserver`, fetching that task's grants from the daemon as
// a dispatched run does, takes the grant as one this build serves rather
// than warning that it serves nothing for it.
package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/capabilitytest"
	"github.com/ai-on-gke/grain/pkg/capability/grainserver"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestRegisteredMethodIsListedGrantedAndServedToTheRun(t *testing.T) {
	// capabilitytest.RegisterMethod's method, registered here so the test
	// can take it away again at the end.
	unregister := capability.Register(capability.Method{
		Server: capabilitytest.MethodServer, Spec: capabilitytest.MethodSpec(capabilitytest.MethodName),
		Name: capabilitytest.MethodLabel, RunGrant: true, Aliases: []string{capabilitytest.MethodAlias},
	})
	registered := true
	t.Cleanup(func() {
		if registered {
			unregister()
		}
	})
	w := newWorld(t)
	w.newRepo("acme", "widgets")
	url := newTestServer(t, ui.NewServer(withAccessCode(ui.Config{
		Actor:        ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities(),
	}), w.store))
	api := func(method, path string, body, out any) int {
		t.Helper()
		var in io.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			in = bytes.NewReader(data)
		}
		req, _ := http.NewRequest(method, url+path, in)
		req.Header.Set("Content-Type", "application/json")
		presentAccessCode(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if out != nil && resp.StatusCode < 300 {
			if err := json.Unmarshal(data, out); err != nil {
				t.Fatalf("%s %s: decoding %s: %v", method, path, data, err)
			}
		}
		if resp.StatusCode >= 300 {
			t.Logf("%s %s: %d %s", method, path, resp.StatusCode, data)
		}
		return resp.StatusCode
	}

	// Listed, beside the grain server's own methods.
	var methods []ui.CapabilityMethod
	if code := api("GET", "/api/capability-methods", nil, &methods); code != http.StatusOK {
		t.Fatalf("GET /api/capability-methods: %d", code)
	}
	if !slices.ContainsFunc(methods, func(m ui.CapabilityMethod) bool {
		return m.Ref == capabilitytest.MethodRef && m.Capability == capabilitytest.MethodServer
	}) {
		t.Fatalf("GET /api/capability-methods lists no %s: %+v", capabilitytest.MethodRef, methods)
	}

	// Granted to a task by its alias, and recorded as the method grant.
	var task struct {
		ID           string   `json:"id"`
		Capabilities []string `json:"capabilities"`
	}
	if code := api("POST", "/api/tasks", map[string]any{
		"title": "look something up", "description": "d", "repo": "acme/widgets",
		"capabilities": []string{capabilitytest.MethodAlias},
	}, &task); code >= 300 {
		t.Fatalf("filing a task holding %s: %d", capabilitytest.MethodAlias, code)
	}
	if !slices.Contains(task.Capabilities, capabilitytest.MethodGrant) || slices.Contains(task.Capabilities, capabilitytest.MethodAlias) {
		t.Fatalf("a task filed holding %s holds %v, want %s in its place", capabilitytest.MethodAlias, task.Capabilities, capabilitytest.MethodGrant)
	}

	// Served to the run: the run's mcpserver reads the task's grants from
	// the daemon (-server/-task) and holds them against what this build
	// serves; an unknown one is named on stderr. The method's server is
	// the in-process one built from what is registered.
	if srv, err := grainserver.Lookup(nil)(capabilitytest.MethodServer); err != nil || srv.Methods[capabilitytest.MethodName] == nil {
		t.Fatalf("the in-process lookup of %s = %+v, %v", capabilitytest.MethodServer, srv, err)
	}
	t.Setenv("GRAIN_TOKEN", testAccessCode)
	stderr := serveMCPOnce(t, "-sandbox-root", t.TempDir(), "-server", url, "-task", task.ID)
	if strings.Contains(stderr, capabilitytest.MethodGrant) {
		t.Errorf("the run's mcpserver does not serve the registered method's grant:\n%s", stderr)
	}
	// Not vacuous: with the method unregistered, the same task's run is
	// told its grant serves nothing -- so the run above did read the
	// task's grants, and found the method among what this build serves.
	unregister()
	registered = false
	stderr = serveMCPOnce(t, "-sandbox-root", t.TempDir(), "-server", url, "-task", task.ID)
	if !strings.Contains(stderr, `"`+capabilitytest.MethodGrant+`" names no capability`) {
		t.Errorf("with the method unregistered the task's grant drew no warning, so the check above checks nothing:\n%s", stderr)
	}
}

// serveMCPOnce runs this build's mcpserver in-process with args and its
// stdin already closed -- it reads its grants, warns of any it does not
// serve, and returns -- and gives back what it wrote to stderr.
func serveMCPOnce(t *testing.T, args ...string) string {
	t.Helper()
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = wr
	done := make(chan string)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	serveErr := daemon.ServeMCPServer(strings.NewReader(""), io.Discard, args)
	os.Stderr = saved
	wr.Close()
	out := <-done
	r.Close()
	// Its stdin closing is how mcp.Serve ends: io.EOF, which the
	// subcommand treats as an ordinary close.
	if serveErr != nil && !errors.Is(serveErr, io.EOF) {
		t.Fatalf("grain mcpserver %v: %v\nstderr:\n%s", args, serveErr, out)
	}
	return out
}
