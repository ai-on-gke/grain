package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/k8s"
	"github.com/bwsalmon/grain/pkg/pkgcache"
)

const testCAPEM = "-----BEGIN CERTIFICATE-----\ngrain\n-----END CERTIFICATE-----\n"

// The regression test for the k8s platform's package cache. A sandbox pod
// is reached only over `kubectl exec`, as the image's own uid 1000 with
// no sudo -- noSudoRunner as its one runner, and no root route beside it
// -- so nothing under /etc can be written once it is running. A pod built
// with the cache's configuration already mounted has to come through
// ConfigurePackageCacheTransparent configured, having run nothing that
// needs root, and with the per-tool half still done as the agent.
func TestK8sSandboxConfiguresThePackageCacheWithoutSudo(t *testing.T) {
	dir := noSudoImage(t)
	agent := newNoSudoRunner(t, dir, "1000")
	base := "http://10.241.0.1:9729"
	sb := &k8sSandbox{owner: NewK8sSandboxes(K8sConfig{Image: "img"}), name: "3-1", podName: PodNamePrefix + "3-1",
		runner: agent, cache: podPackageCache{base: base, caPEM: []byte(testCAPEM)}}

	configured, err := ConfigurePackageCacheTransparent(context.Background(), sb, base, []byte(testCAPEM), "10.241.0.1")
	if err != nil {
		t.Fatalf("ConfigurePackageCacheTransparent: %v", err)
	}
	if !configured {
		t.Fatal("ConfigurePackageCacheTransparent reported doing nothing")
	}
	if log, err := os.ReadFile(filepath.Join(dir, "log")); err == nil {
		t.Errorf("something was written into a pod that has no root to write it with:\n%s", log)
	}
	var tools int
	for _, script := range agent.scripts {
		if script == configureToolScript {
			tools++
		} else {
			t.Errorf("a root script ran in a pod where it can only fail:\n%s", script)
		}
	}
	commands, _ := pkgcache.SandboxCommands(base)
	if tools != len(commands) {
		t.Errorf("the agent ran %d tool commands, want %d", tools, len(commands))
	}
}

// A pod built before the cache was switched on, or for another CA, has
// nothing under /etc for this dispatch and no root to write it with: it
// falls back to the rootless path (SandboxRootless), which writes nothing
// into /etc, still configures the per-user tools, and does not claim the
// sandbox was configured.
func TestK8sSandboxBuiltForAnotherCacheFallsBackToTheRootlessPath(t *testing.T) {
	resetNoRootNotedForTest(t)
	dir := noSudoImage(t)
	base := "http://10.241.0.1:9729"
	commands, _ := pkgcache.SandboxCommands(base)
	for _, cache := range []podPackageCache{{}, {base: "http://10.241.0.9:9729"}, {base: base, caPEM: []byte("old")}} {
		agent := newNoSudoRunner(t, dir, "1000")
		sb := &k8sSandbox{owner: NewK8sSandboxes(K8sConfig{Image: "img"}), name: "3-2", runner: agent, cache: cache}
		configured, err := ConfigurePackageCacheTransparent(context.Background(), sb, base, []byte(testCAPEM), "")
		if err != nil || configured {
			t.Errorf("pod built for %+v: ConfigurePackageCacheTransparent = %v, %v; want false, nil", cache, configured, err)
		}
		var tools int
		for _, script := range agent.scripts {
			switch script {
			case configureToolScript:
				tools++
			case rootProbeScript:
			default:
				t.Errorf("pod built for %+v ran a root script:\n%s", cache, script)
			}
		}
		if tools != len(commands) {
			t.Errorf("pod built for %+v: the agent ran %d tool commands, want %d", cache, tools, len(commands))
		}
	}
	if log, err := os.ReadFile(filepath.Join(dir, "log")); err == nil {
		t.Errorf("something was written into a pod that has no root to write it with:\n%s", log)
	}
}

// packageCacheSetup's script, run the way the init container runs it --
// `sh -c`, the emptyDir as $1, as an ordinary user -- has to leave every
// file the sandbox container mounts as a regular file holding what
// ConfigurePackageCacheTransparent would have written there as root.
func TestK8sPackageCacheSetupLeavesEveryMountedFile(t *testing.T) {
	base := "http://10.241.0.1:9729"
	setup, err := packageCacheSetup(podPackageCache{base: base, caPEM: []byte(testCAPEM)})
	if err != nil {
		t.Fatal(err)
	}
	image := t.TempDir()
	bundle := filepath.Join(image, "ca-certificates.crt")
	if err := os.WriteFile(bundle, []byte("-----BEGIN CERTIFICATE-----\npublic\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	daemonJSON := filepath.Join(image, "daemon.json") // the stock image has none

	want := map[string]string{}
	files, _ := pkgcache.SandboxFiles(base, nil)
	for _, file := range files {
		want[file.Path] = file.Content
	}
	want[pkgcache.DockerConfigPath], _ = pkgcache.DockerDaemonJSON(nil, base)
	want[pkgcache.CACertPath] = testCAPEM
	want[CABundlePath] = "-----BEGIN CERTIFICATE-----\npublic\n-----END CERTIFICATE-----\n\n" + testCAPEM

	out := runSetup(t, setup, bundle, daemonJSON)
	if len(setup.Files) != len(want) {
		t.Errorf("setup mounts %d files, want %d: %+v", len(setup.Files), len(want), setup.Files)
	}
	for _, file := range setup.Files {
		content, err := os.ReadFile(filepath.Join(out, file.SubPath))
		if err != nil {
			t.Errorf("%s: the script left no %s for it: %v", file.MountPath, file.SubPath, err)
			continue
		}
		if string(content) != want[file.MountPath] {
			t.Errorf("%s = %q, want %q", file.MountPath, content, want[file.MountPath])
		}
	}

	// An image with a daemon.json of its own keeps it: there is no
	// merging JSON in the shell, and its dockerd is configured as it was.
	if err := os.WriteFile(daemonJSON, []byte(`{"debug":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out = runSetup(t, setup, bundle, daemonJSON)
	if got, _ := os.ReadFile(filepath.Join(out, "docker")); string(got) != `{"debug":true}` {
		t.Errorf("an image's own daemon.json became %q", got)
	}
}

// With no cache, only a CA, a pod gets the CA and nothing else.
func TestK8sPackageCacheSetupWithOnlyACA(t *testing.T) {
	setup, err := packageCacheSetup(podPackageCache{caPEM: []byte(testCAPEM)})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, file := range setup.Files {
		paths = append(paths, file.MountPath)
	}
	if got, want := strings.Join(paths, " "), pkgcache.CACertPath+" "+CABundlePath; got != want {
		t.Errorf("mounts %s, want %s", got, want)
	}
	out := runSetup(t, setup, filepath.Join(t.TempDir(), "absent"), filepath.Join(t.TempDir(), "absent"))
	if got, _ := os.ReadFile(filepath.Join(out, "ca-bundle")); string(got) != "\n"+testCAPEM {
		t.Errorf("an image with no bundle got %q", got)
	}
}

func runSetup(t *testing.T, setup *k8s.Setup, args ...string) string {
	t.Helper()
	out := t.TempDir()
	cmd := exec.Command("sh", append([]string{"-c", setup.Script, "sh", out}, args...)...)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup script: %v\n%s\n%s", err, combined, setup.Script)
	}
	return out
}

// Every pod K8sSandboxes builds asks for the cache afresh, carries its
// configuration as a Setup, and reports what it was built with.
func TestK8sSandboxesBuildsEachPodWithThePackageCache(t *testing.T) {
	dir := t.TempDir()
	kubectl := filepath.Join(dir, "kubectl")
	manifest := filepath.Join(dir, "manifest")
	writePATHStub(t, dir, "kubectl", "#!/bin/sh\ncase \"$*\" in\n"+
		"*apply*) cat >"+manifest+" ;;\n"+
		"*get*) echo 'Running|True|sha |' ;;\nesac\n")
	k := NewK8sSandboxes(K8sConfig{Image: "img", Kubectl: kubectl, Workspace: "/home/agent"})
	base := "http://10.241.0.1:9729"
	k.SetPackageCache(func(context.Context) string { return base }, func(context.Context) []byte { return []byte(testCAPEM) })

	sb, err := k.Acquire(context.Background(), "4-1", Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	gotBase, gotCA := sb.(SandboxPackageCacheProvisioner).ProvisionedPackageCache()
	if gotBase != base || string(gotCA) != testCAPEM {
		t.Errorf("ProvisionedPackageCache = %q, %q", gotBase, gotCA)
	}
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("no pod was applied: %v", err)
	}
	for _, want := range []string{`"initContainers"`, `"mountPath":"` + pkgcache.PipConfigPath + `"`, `"mountPath":"` + CABundlePath + `"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the pod applied has no %s:\n%s", want, raw)
		}
	}

	// Switched off: the next pod is built exactly as one always was.
	base = ""
	k.SetPackageCache(func(context.Context) string { return base }, nil)
	sb, err = k.Acquire(context.Background(), "4-2", Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if gotBase, gotCA := sb.(SandboxPackageCacheProvisioner).ProvisionedPackageCache(); gotBase != "" || gotCA != nil {
		t.Errorf("a pod built with no cache reports %q, %q", gotBase, gotCA)
	}
	if raw, _ := os.ReadFile(manifest); strings.Contains(string(raw), "initContainers") {
		t.Errorf("a pod built with no cache has an init container:\n%s", raw)
	}
}
