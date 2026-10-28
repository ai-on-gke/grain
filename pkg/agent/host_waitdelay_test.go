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

package agent

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// An agent CLI that is gone while something it started still holds its
// stdout -- the forked mcpserver, or a tool call's background process --
// must not hold Run, and with it the run and any daemon draining at
// shutdown, until that descendant exits too. Before LocalHost.Run set a
// WaitDelay, both of these blocked for the full sleep.
func TestLocalHostRunReturnsOnceTheCLIIsGoneEvenIfAChildHoldsItsOutput(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell to run: %v", err)
	}
	for _, tc := range []struct {
		name    string
		script  string
		wantErr bool
	}{
		// The shape the 10-12 walkthrough hit: the CLI killed outright.
		{name: "killed", script: "echo started; sleep 60 & kill -9 $$", wantErr: true},
		// The CLI finishing normally is still a success.
		{name: "exited", script: "echo started; sleep 60 & exit 0", wantErr: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			start := time.Now()
			out, err := LocalHost{Logf: func(string, ...any) {}}.Run(context.Background(),
				Command{Path: sh, Args: []string{"-c", tc.script}}, io.Discard)
			if elapsed := time.Since(start); elapsed > 30*time.Second {
				t.Fatalf("Run took %s, want it bounded by localWaitDelay (%s), not by the child's sleep", elapsed, localWaitDelay)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("Run err = %v, want error: %v", err, tc.wantErr)
			}
			if !strings.Contains(out, "started") {
				t.Errorf("Run output = %q, want what the CLI wrote before it went", out)
			}
		})
	}
}
