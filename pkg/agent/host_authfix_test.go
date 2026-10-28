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
	"os/exec"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/authfix"
	"github.com/ai-on-gke/grain/pkg/authfix/authfixtest"
)

// A CLI that is itself a wrapper exec'ing something the host refuses
// reports the refusal on its stderr rather than to exec: LocalHost.Run
// says that as the registered fix too, so orchestrator reads it the same
// way.
func TestLocalHostRunSaysExpiredHostCredentialsAsTheirFix(t *testing.T) {
	authfixtest.Register(t)
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell to run: %v", err)
	}
	_, err = LocalHost{}.Run(context.Background(), Command{
		Path: sh,
		Args: []string{"-c", "echo 'exec: /opt/tools/cli: host credential lapsed' >&2; exit 126"},
	}, nil)
	expired, ok := authfix.As(err)
	if !ok {
		t.Fatalf("Run err = %v, want an authfix.Error", err)
	}
	if !strings.Contains(expired.Error(), authfixtest.Action) {
		t.Errorf("err = %q, want it to say what fixes it", expired.Error())
	}
	if !strings.Contains(err.Error(), "(stderr: exec: /opt/tools/cli") {
		t.Errorf("err = %q, want what the CLI said kept beside it", err)
	}
}

func TestLocalHostRunLeavesOtherFailuresAlone(t *testing.T) {
	authfixtest.Register(t)
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell to run: %v", err)
	}
	_, err = LocalHost{}.Run(context.Background(), Command{Path: sh, Args: []string{"-c", "echo nope >&2; exit 1"}}, nil)
	if err == nil {
		t.Fatal("Run err = nil, want the failure")
	}
	if _, ok := authfix.As(err); ok {
		t.Errorf("Run err = %v, read as expired host credentials", err)
	}
}
