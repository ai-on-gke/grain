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

package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/granule"
	granulesandbox "github.com/bwsalmon/granule/pkg/sandbox"
)

// neverConnects is a ChannelProvider whose wrapper never dials in.
type neverConnects struct{}

func (neverConnects) Client(string) (*granulesandbox.Client, bool) { return nil, false }
func (neverConnects) WaitClient(ctx context.Context, _ string) (*granulesandbox.Client, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A sandbox whose wrapper never establishes its dial-out channel fails
// Acquire -- no silent fallback to kubectl exec -- with the wrapper's own
// dial-out log in the error, read by one diagnostic exec; its credentials
// are revoked and its pod deleted (docs/design/grain-on-gke.md#GKE-3).
func TestK8sGranuleDialoutAcquireFailsWithTheWrapperLogWhenTheChannelNeverConnects(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(t.TempDir(), "kubectl")
	script := `#!/bin/sh
n=$(ls "` + dir + `" | wc -l)
printf '%s\n' "$*" > "` + dir + `/$n.args"
case "$*" in
	*"granule-dialout.log"*) printf 'dial grain-access.grain.svc:4244: connect: connection refused\n'; exit 0 ;;
	*" -f -"*) cat > "` + dir + `/$n.json"; exit 0 ;;
	*"jsonpath={.metadata.uid}"*) printf 'uid-1'; exit 0 ;;
	*" delete "*) exit 0 ;;
esac
printf 'Running|True|docker-pullable://example/granule@sha256:abc|\n'
`
	if err := os.WriteFile(bin+".src", []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("install", "-m", "0755", bin+".src", bin).CombinedOutput(); err != nil {
		t.Fatalf("installing fake kubectl: %v: %s", err, out)
	}
	minter := &fakeMinter{}
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Namespace:         "grain",
		Kubectl:           bin,
		Image:             "example/granule:latest",
		ReadyPollInterval: 5 * time.Millisecond,
		ForwardRunner: func(ctx context.Context, cfg granule.KubectlForwardConfig) error {
			t.Error("granule forward over kubectl exec was started for a dial-out sandbox")
			if cfg.OnReady != nil {
				cfg.OnReady()
			}
			<-ctx.Done()
			return ctx.Err()
		},
		Access: &K8sGranuleAccess{
			Acceptor:      minter,
			Channels:      neverConnects{},
			Dial:          "grain-access.grain.svc:4244",
			Mode:          K8sGranuleAccessDialout,
			AccessTimeout: 50 * time.Millisecond,
		},
	})

	_, err := k.Acquire(context.Background(), "901-1", Shape{})
	if err == nil {
		t.Fatal("Acquire succeeded with a channel that never connected")
	}
	for _, want := range []string{"dial-out channel", "connection refused"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Acquire error does not carry %q: %v", want, err)
		}
	}
	if k.Live("901-1") {
		t.Error("the sandbox is still live after a failed Acquire")
	}
	minter.mu.Lock()
	revoked := slices.Contains(minter.revoked, "901-1")
	minter.mu.Unlock()
	if !revoked {
		t.Error("the sandbox's credentials were not revoked")
	}
	args, _ := filepath.Glob(filepath.Join(dir, "*.args"))
	var deleted bool
	for _, f := range args {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), " delete ") {
			deleted = true
		}
	}
	if !deleted {
		t.Error("the pod was not deleted")
	}
}
