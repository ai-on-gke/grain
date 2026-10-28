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
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fakeDocker is a docker binary that records each call, one line per
// call, and succeeds -- swallowing whatever it is fed on stdin, as a
// volume fill is.
func fakeDocker(t *testing.T) (docker, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	docker = filepath.Join(dir, "docker")
	script := "#!/bin/sh\necho \"$*\" >> " + calls + "\ncat >/dev/null\nexit 0\n"
	if err := os.WriteFile(docker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return docker, calls
}

func readCalls(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// Only a self-debug run's container gets grain's source mounted: from
// a volume named for the build revision, read-only, and filled once for
// every sandbox that revision serves rather than once per run.
func TestContainerMountsGrainSourceOnlyForSelfDebug(t *testing.T) {
	src, head := bakeSource(t, 2, 1)
	docker, callLog := fakeDocker(t)
	c := NewContainerSandboxes(ContainerConfig{Docker: docker, Image: "img", Workspace: "/w", Network: "mine", GrainSourceDir: src})
	mount := "--mount type=volume,src=" + GrainSourceVolumePrefix + head + ",dst=" + containerGrainSourceMount + ",readonly"

	plain, err := c.Acquire(t.Context(), "1-1", Shape{})
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range readCalls(t, callLog) {
		if strings.Contains(call, "--mount") || strings.HasPrefix(call, "volume") {
			t.Errorf("a sandbox without self-debug ran docker %q: it gets no grain source", call)
		}
	}
	if _, _, ok := plain.(GrainSourceMounter).MountedGrainSource(); ok {
		t.Error("a sandbox without self-debug reports grain's source mounted")
	}

	for _, name := range []string{"2-1", "3-1"} {
		sb, err := c.Acquire(t.Context(), name, Shape{GrainSource: true})
		if err != nil {
			t.Fatal(err)
		}
		path, rev, ok := sb.(GrainSourceMounter).MountedGrainSource()
		if !ok || rev != head || path != containerGrainSourceMount+"/"+grainSourceVolumeTree {
			t.Errorf("sandbox %s reports %q at %s (%v), want %s's tree", name, path, rev, ok, head)
		}
	}
	var fills, mounted int
	for _, call := range readCalls(t, callLog) {
		if strings.HasPrefix(call, "run --rm") {
			fills++
		}
		if strings.HasPrefix(call, "run -d") && strings.Contains(call, mount) {
			mounted++
		}
	}
	if fills != 1 || mounted != 2 {
		t.Errorf("docker calls %q: want the volume filled once and mounted (%s) into both self-debug sandboxes", readCalls(t, callLog), mount)
	}
}

// A self-debug container whose grain-src/ comes from the mounted volume
// is at the build revision -- `git log -1` says so -- and placeGrainSource
// linked it rather than streaming a copy. Against a real Docker, with an
// image assembled from this machine's own binaries so nothing is pulled.
func TestContainerGrainSourceIsTheBuildRevision(t *testing.T) {
	if testing.Short() {
		t.Skip("starts docker containers")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("no usable docker: %v", err)
	}
	src, head := bakeSource(t, 3, 2)
	image := grainSourceProbeImage(t)
	c := NewContainerSandboxes(ContainerConfig{Image: image, Workspace: "/home/agent", Network: "none", GrainSourceDir: src})
	volume := GrainSourceVolumePrefix + head
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", volume).Run() })

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	sb, err := c.Acquire(ctx, fmt.Sprintf("grainsrc-%d", time.Now().UnixNano()), Shape{GrainSource: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Release(context.Background()) })
	if _, _, ok := sb.(GrainSourceMounter).MountedGrainSource(); !ok {
		t.Fatal("the self-debug sandbox has no grain source mounted")
	}

	out := checkout{Root: "/home/agent"}
	placeGrainSource(ctx, selfDebugTask, src, "", sb.(SandboxPlacer), nil, &out)
	if out.GrainSource != grainSourceReady {
		t.Fatalf("GrainSource = %q (%s), want ready", out.GrainSource, out.GrainSourceWhy)
	}
	run := sb.(SandboxCommander)
	if target, stderr, code := run.RunInSandbox(ctx, []string{"readlink", "/home/agent/grain-src"}, ""); code != 0 || strings.TrimSpace(target) != containerGrainSourceMount+"/"+grainSourceVolumeTree {
		t.Errorf("grain-src links to %q (exit %d: %s), want the mounted tree", target, code, stderr)
	}
	got, stderr, code := run.RunInSandbox(ctx, []string{"git", "-c", "safe.directory=*", "-C", "/home/agent/grain-src", "log", "-1", "--format=%H"}, "")
	if code != 0 || strings.TrimSpace(got) != head {
		t.Errorf("git log -1 in grain-src = %q (exit %d: %s), want the build revision %s", got, code, stderr, head)
	}
	if _, _, code := run.RunInSandbox(ctx, []string{"sh", "-c", "echo x > /home/agent/grain-src/README.md"}, ""); code == 0 {
		t.Error("the sandbox user could write grain-src/README.md, want the mount read-only")
	}
}

// grainSourceProbeImage imports an image holding just the commands the
// fill script and the test need, with their shared libraries, running
// as an unprivileged user whose workspace is /home/agent.
func grainSourceProbeImage(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	seen := map[string]bool{}
	dir := func(name string, uid int) {
		if !seen[name] {
			seen[name] = true
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name, Mode: 0o755, Uid: uid, Gid: uid}); err != nil {
				t.Fatal(err)
			}
		}
	}
	files := map[string]string{}
	libRE := regexp.MustCompile(`(/\S+) \(0x`)
	for _, name := range []string{"sh", "tar", "gzip", "find", "chmod", "mv", "rm", "mkdir", "ln", "sleep", "git", "cat", "readlink"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("no %s for the probe image: %v", name, err)
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		files["/bin/"+name] = real
		ldd, _ := exec.Command("ldd", real).Output()
		for _, m := range libRE.FindAllStringSubmatch(string(ldd), -1) {
			lib, err := filepath.EvalSymlinks(m[1])
			if err != nil {
				t.Fatal(err)
			}
			files[m[1]] = lib
		}
	}
	for _, d := range []string{"bin/", "tmp/", "home/"} {
		dir(d, 0)
	}
	dir("home/agent/", 1000)
	for name, from := range files {
		var parents []string
		for d := filepath.Dir(name); d != "/"; d = filepath.Dir(d) {
			parents = append([]string{strings.TrimPrefix(d, "/") + "/"}, parents...)
		}
		for _, d := range parents {
			dir(d, 0)
		}
		data, err := os.ReadFile(from)
		if err != nil {
			t.Skipf("reading %s for the probe image: %v", from, err)
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: strings.TrimPrefix(name, "/"), Mode: 0o755, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	image := fmt.Sprintf("grain-src-probe:%d", time.Now().UnixNano())
	cmd := exec.Command("docker", "import", "--change", "USER 1000", "--change", "ENV PATH=/bin", "--change", "ENV HOME=/home/agent", "-", image)
	cmd.Stdin = &buf
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("importing the probe image: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })
	return image
}
