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

package sandboxaccess

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/sandbox"
)

// A CI job's shape -- files placed in one batch, then a run of steps that
// build on each other's workspace, streamed -- goes green over the fake
// wrapper's dial-out channel, including across a channel dropped between
// two steps (design/design/grain-on-gke.design.md#GKE-3).
func TestRunnerRunsACIJobGreenOverTheFakeWrapper(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	a, live := newTestAcceptor(t, ctx, nil)
	const name = "grain-granule-ci-job-1"
	live.set(name, true)
	creds, err := a.Mint(name, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	startWrapper(t, ctx, a, name, creds.Token, writeCreds(t, creds))
	if _, err := a.WaitClient(ctx, name); err != nil {
		t.Fatalf("WaitClient: %v", err)
	}
	runner := &Runner{Acceptor: a, Sandbox: name, User: currentUser(t), ReattachTimeout: 5 * time.Second}

	ws := t.TempDir()
	// Checkout: the tree and the git credentials, in one batch.
	if err := runner.PutFiles(ctx, []sandbox.FileUpload{
		{Path: filepath.Join(ws, "src", "main.sh"), Content: []byte("#!/bin/sh\necho built > \"$1\"\n"), Mode: 0o755},
		{Path: filepath.Join(ws, ".git-credentials"), Content: []byte("http://x:tok@127.0.0.1:9728\n"), Mode: 0o600},
	}); err != nil {
		t.Fatalf("PutFiles: %v", err)
	}

	steps := []struct {
		name, script, want string
	}{
		{"build", "cd " + ws + " && ./src/main.sh out.txt && echo build-ok", "build-ok"},
		{"test", "cd " + ws + " && grep -q built out.txt && echo test-ok", "test-ok"},
		{"credentials", "stat -c %a " + filepath.Join(ws, ".git-credentials"), "600"},
	}
	for i, step := range steps {
		if i == 1 {
			// The channel drops between two steps; the next step reattaches.
			_ = a.DropActiveSession(name)
		}
		var streamed strings.Builder
		stdout, stderr, code, err := runner.Exec(ctx, []string{"sh", "-c", step.script}, "", &streamed)
		if err != nil || code != 0 {
			t.Fatalf("step %s: code=%d err=%v stderr=%q", step.name, code, err, stderr)
		}
		if got := strings.TrimSpace(stdout); got != step.want {
			t.Fatalf("step %s: stdout = %q, want %q", step.name, got, step.want)
		}
		if streamed.String() != stdout {
			t.Fatalf("step %s: streamed %q, returned %q", step.name, streamed.String(), stdout)
		}
	}
}
