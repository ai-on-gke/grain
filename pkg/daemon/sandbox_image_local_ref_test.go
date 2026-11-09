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

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalOnlyImageRef(t *testing.T) {
	for ref, want := range map[string]bool{
		"grain/k8s-sandbox:sha-0123456789ab":                       true,
		"grain/guest:sha-0123456789ab":                             true,
		"someone/image:v1":                                         true,
		"docker.io/grainai/sandbox-k8s:main":                       false,
		"us-docker.pkg.dev/p/r/guest:sha-0123456789ab@sha256:abcd": false,
		"localhost/grain/guest:sha-0123456789ab":                   false,
		"registry.example:5000/grain/guest:x":                      false,
		"ubuntu:24.04":                                             false,
		"":                                                         false,
	} {
		if got := localOnlyImageRef(ref); got != want {
			t.Errorf("localOnlyImageRef(%q) = %v, want %v", ref, got, want)
		}
	}
}

// A Kubernetes backend refuses a release-asset local ref outright: kubelet
// would resolve grain/k8s-sandbox:sha-... on Docker Hub.
func TestRefuseLocalOnlyPodImage(t *testing.T) {
	err := refuseLocalOnlyPodImage("grain/k8s-sandbox:sha-0123456789ab", "-k8s-sandbox-image")
	if err == nil {
		t.Fatal("refuseLocalOnlyPodImage(grain/k8s-sandbox:sha-...) = nil, want a refusal")
	}
	for _, want := range []string{"Docker Hub", "-k8s-sandbox-image", "release-assets.sh mirror"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}
	if err := refuseLocalOnlyPodImage("docker.io/grainai/sandbox-k8s:main", "-k8s-sandbox-image"); err != nil {
		t.Errorf("refuseLocalOnlyPodImage(registry ref) = %v, want nil", err)
	}
}

// Mirroring a release-asset ref that is no longer in the local image store
// fails instead of pulling it (which would ask Docker Hub for it).
func TestDefaultMirrorSandboxImageRefusesToPullLocalOnlyRef(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "docker.log")
	script := "#!/bin/sh\necho \"$@\" >> \"" + logFile + "\"\n" +
		"if [ \"$1\" = image ] && [ \"$2\" = inspect ]; then exit 1; fi\nexit 0\n"
	writeExecutableStub(t, filepath.Join(dir, "docker"), script)
	savedDocker, savedToken := sandboxMirrorDockerCmd, sandboxMirrorTokenSource
	sandboxMirrorDockerCmd = []string{filepath.Join(dir, "docker")}
	sandboxMirrorTokenSource = nil
	t.Cleanup(func() { sandboxMirrorDockerCmd, sandboxMirrorTokenSource = savedDocker, savedToken })
	savedLocal := LocalSandboxes
	LocalSandboxes = nil
	t.Cleanup(func() { LocalSandboxes = savedLocal })

	source := "grain/guest:sha-0123456789ab"
	mirror := "us-docker.pkg.dev/cluster-project/grain/guest"
	_, err := defaultMirrorSandboxImage(context.Background(), source, mirror, mirror+":sha-0123456789ab")
	if err == nil || !strings.Contains(err.Error(), "release-asset") {
		t.Fatalf("defaultMirrorSandboxImage(%s) error = %v, want a release-asset refusal", source, err)
	}
	logged, _ := os.ReadFile(logFile)
	if strings.Contains(string(logged), "pull ") {
		t.Fatalf("defaultMirrorSandboxImage pulled a release-asset ref:\n%s", logged)
	}
}
