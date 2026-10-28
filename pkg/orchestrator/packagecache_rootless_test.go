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
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

// rootlessSandbox is a commandedSandbox that answers SandboxRootless the
// way a Kubernetes sandbox pod does: reason when the pod has no route to
// root, "" when its image carries sudo after all.
type rootlessSandbox struct {
	commandedSandbox
	reason string
}

func (s *rootlessSandbox) RootUnavailable(context.Context) string { return s.reason }

// resetNoRootNotedForTest clears recorded rootless reasons before the test
// and registers a cleanup to clear them after the test.
func resetNoRootNotedForTest(t *testing.T) {
	t.Helper()
	noRootNoted.Clear()
	t.Cleanup(func() {
		noRootNoted.Clear()
	})
}

// captureLog redirects the standard logger for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	resetNoRootNotedForTest(t)
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prev)
		log.SetFlags(flags)
	})
	return &buf
}

// The walkthrough on kind (task 1836) found every k8s-container dispatch
// logging four "sudo: not found" failures: the CA, update-ca-certificates,
// resolv.conf and the apt file. A sandbox with no root gets none of those
// attempted, still gets the per-tool settings its own user can make, and
// is not reported as configured.
func TestConfigurePackageCacheTransparentSkipsWhatARootlessSandboxCannotDo(t *testing.T) {
	resetNoRootNotedForTest(t)
	logged := captureLog(t)
	sandbox := &rootlessSandbox{reason: "it runs as uid 1000 and has no sudo (test skips)"}
	configured, err := ConfigurePackageCacheTransparent(context.Background(), sandbox,
		"http://10.9.0.1:41234", []byte("-----BEGIN CERTIFICATE-----\n"), "10.244.0.32")
	if err != nil {
		t.Fatalf("a sandbox without root is not an error: %v", err)
	}
	if configured {
		t.Error("reported as configured, though nothing went into its /etc")
	}
	commands, err := pkgcache.SandboxCommands("http://10.9.0.1:41234")
	if err != nil {
		t.Fatalf("SandboxCommands: %v", err)
	}
	if len(sandbox.commands) != len(commands) {
		t.Fatalf("ran %d commands, want only the %d per-tool ones: %v", len(sandbox.commands), len(commands), sandbox.commands)
	}
	for i, argv := range sandbox.commands {
		if got := argv[4:]; strings.Join(got, " ") != strings.Join(commands[i].Argv, " ") {
			t.Errorf("command %d = %v, want %v", i, got, commands[i].Argv)
		}
	}
	if writes := resolvConfWrites(&sandbox.commandedSandbox); len(writes) != 0 {
		t.Errorf("pointed the resolver at the cache without the CA that makes its answers trusted: %q", writes)
	}
	if n := strings.Count(logged.String(), "cannot write as root"); n != 1 {
		t.Errorf("logged the missing root %d times, want once:\n%s", n, logged.String())
	}
	if !strings.Contains(logged.String(), "test skips") {
		t.Errorf("the log line does not say why:\n%s", logged.String())
	}
}

// "Say so once": every k8s-container dispatch lacks root for the same
// reason, so the second and later ones are quiet.
func TestConfigurePackageCacheNotesAMissingRootOnce(t *testing.T) {
	resetNoRootNotedForTest(t)
	logged := captureLog(t)
	for i := 0; i < 3; i++ {
		sandbox := &rootlessSandbox{reason: "it runs as uid 1000 and has no sudo (test once)"}
		if _, err := ConfigurePackageCacheTransparent(context.Background(), sandbox,
			"http://10.9.0.1:41234", []byte("ca"), "10.244.0.32"); err != nil {
			t.Fatalf("ConfigurePackageCacheTransparent: %v", err)
		}
	}
	if n := strings.Count(logged.String(), "test once"); n != 1 {
		t.Errorf("logged the missing root %d times over three dispatches, want once:\n%s", n, logged.String())
	}
}

// A deployment that built its own sandbox image with passwordless sudo
// answers "" and gets the whole configuration, exactly as before.
func TestConfigurePackageCacheTransparentConfiguresARootlessSandboxThatFoundRoot(t *testing.T) {
	resetNoRootNotedForTest(t)
	sandbox := &rootlessSandbox{}
	configured, err := ConfigurePackageCacheTransparent(context.Background(), sandbox,
		"http://10.9.0.1:41234", nil, "10.244.0.32")
	if err != nil {
		t.Fatalf("ConfigurePackageCacheTransparent: %v", err)
	}
	if !configured {
		t.Error("a sandbox with root was not configured")
	}
	if writes := resolvConfWrites(&sandbox.commandedSandbox); len(writes) != 1 {
		t.Errorf("wrote %s %d times, want once", pkgcache.ResolvConfPath, len(writes))
	}
}

func TestK8sSandboxAsksItsPodForRoot(t *testing.T) {
	owner := NewK8sSandboxes(K8sConfig{Image: "ghcr.io/example/k8s-sandbox:main"})

	found := &scriptedRunner{}
	if reason := (&k8sSandbox{owner: owner, runner: found}).RootUnavailable(context.Background()); reason != "" {
		t.Errorf("a pod whose probe succeeded was reported rootless: %q", reason)
	}
	if len(found.ran) != 1 || found.ran[0] != rootProbeScript {
		t.Errorf("ran %q, want the root probe once", found.ran)
	}

	missing := &scriptedRunner{replies: []runReply{{stderr: "it runs as uid 1000 and has no sudo\n", exitCode: 1}}}
	reason := (&k8sSandbox{owner: owner, runner: missing}).RootUnavailable(context.Background())
	if !strings.Contains(reason, "has no sudo") || !strings.Contains(reason, "ghcr.io/example/k8s-sandbox:main") {
		t.Errorf("reason %q does not say what the pod said and which image it runs", reason)
	}
}
