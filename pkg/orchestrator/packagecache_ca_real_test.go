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

package orchestrator_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

// TestPackageCacheCASurvivesARestartWithAnAdoptedContainer walks the
// package-cache Root CA's life on real Docker containers, the way the
// daemon drives it: LoadOrCreateCA at <data-dir>/package-cache-ca.pem on
// start, PrepareSandbox on a new sandbox, and Readopt +
// RefreshAdoptedSandbox on one a previous process left running. A TLS
// server issuing from the CA stands in for the cache's interception
// listener, and the container verifies it with curl and the system
// bundle -- no -k anywhere.
//
//  1. A fresh sandbox trusts the cache.
//  2. A "restart" (the CA reloaded from the file, a new
//     ContainerSandboxes readopting the container) keeps the same CA, so
//     the adopted sandbox still verifies.
//  3. Deleting the file rotates the CA on the next start; the adopted
//     sandbox verifies again once refreshed, and a new sandbox trusts the
//     new CA from the start.
//
// Opt-in (GRAIN_REAL_DOCKER_TESTS=1): it needs a Docker daemon and the
// buildpack-deps:bookworm-curl image the real-kubernetes job loads.
func TestPackageCacheCASurvivesARestartWithAnAdoptedContainer(t *testing.T) {
	if os.Getenv("GRAIN_REAL_DOCKER_TESTS") == "" {
		t.Skip("GRAIN_REAL_DOCKER_TESTS not set; see docs/test-runs/2026-11-03-sandbox-resources.md")
	}
	const image = "buildpack-deps:bookworm-curl"
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Skipf("%s is not in the local docker daemon: %v", image, err)
	}
	ctx := context.Background()
	caPath := filepath.Join(t.TempDir(), "package-cache-ca.pem")

	// Start 1.
	ca1, err := pkgcache.LoadOrCreateCA(caPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(caPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%s after the first start: %v, %v; want mode 0600", caPath, fi, err)
	}
	port := serveTLS(t, ca1)
	cfg := orchestrator.ContainerConfig{Image: image, Workspace: "/tmp"}
	first := orchestrator.NewContainerSandboxes(cfg)
	sb, err := first.Acquire(ctx, "ca-adopted", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = sb.Release(context.Background()) })
	orchestrator.PrepareSandbox(ctx, caSource(ca1), sb)
	mustVerify(t, sb, port, "a new sandbox on the first start")

	// Restart: the CA comes back from the file, and a new process
	// readopts the container rather than building one.
	ca2, err := pkgcache.LoadOrCreateCA(caPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ca1.RootCertPEM(), ca2.RootCertPEM()) {
		t.Fatal("the CA changed across a restart with its file in place")
	}
	port = serveTLS(t, ca2)
	second := orchestrator.NewContainerSandboxes(cfg)
	adopted, err := second.Readopt(ctx, "ca-adopted")
	if err != nil {
		t.Fatalf("Readopt: %v", err)
	}
	if err := orchestrator.RefreshAdoptedSandbox(ctx, caSource(ca2), adopted); err != nil {
		t.Fatalf("RefreshAdoptedSandbox: %v", err)
	}
	mustVerify(t, adopted, port, "the adopted sandbox after a restart")

	// Delete the file: the next start mints and persists a new CA.
	if err := os.Remove(caPath); err != nil {
		t.Fatal(err)
	}
	ca3, err := pkgcache.LoadOrCreateCA(caPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ca2.RootCertPEM(), ca3.RootCertPEM()) {
		t.Fatal("deleting package-cache-ca.pem did not rotate the CA")
	}
	if fi, err := os.Stat(caPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%s after rotation: %v, %v; want mode 0600", caPath, fi, err)
	}
	port = serveTLS(t, ca3)
	if ok, out := curlVerifies(t, adopted, port); ok {
		t.Fatalf("the adopted sandbox verified a rotated CA before being refreshed, so this test proves nothing: %s", out)
	}
	third := orchestrator.NewContainerSandboxes(cfg)
	adopted, err = third.Readopt(ctx, "ca-adopted")
	if err != nil {
		t.Fatalf("Readopt: %v", err)
	}
	if err := orchestrator.RefreshAdoptedSandbox(ctx, caSource(ca3), adopted); err != nil {
		t.Fatalf("RefreshAdoptedSandbox after rotation: %v", err)
	}
	mustVerify(t, adopted, port, "the adopted sandbox after the CA rotated")
	fresh, err := third.Acquire(ctx, "ca-fresh", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Release(context.Background()) })
	orchestrator.PrepareSandbox(ctx, caSource(ca3), fresh)
	mustVerify(t, fresh, port, "a new sandbox after the CA rotated")
}

func caSource(ca *pkgcache.CA) orchestrator.PackageCacheSource {
	return orchestrator.PackageCacheSource{CA: func(context.Context) []byte { return ca.RootCertPEM() }}
}

// serveTLS serves 200s on every host address, with certificates issued
// by ca for whatever name the client asks for, and returns the port.
func serveTLS(t *testing.T, ca *pkgcache.CA) int {
	t.Helper()
	ln, err := tls.Listen("tcp", "0.0.0.0:0", ca.TLSConfig())
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "cache ok") })}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// curlVerifies fetches https://cache.grain.test from inside sb, resolved
// to the container's gateway (this host), with certificate verification.
func curlVerifies(t *testing.T, sb orchestrator.Sandbox, port int) (bool, string) {
	t.Helper()
	script := fmt.Sprintf(`gw=$(awk '$2=="00000000"{h=$3; printf "%%d.%%d.%%d.%%d", "0x" substr(h,7,2), "0x" substr(h,5,2), "0x" substr(h,3,2), "0x" substr(h,1,2)}' /proc/net/route)
curl -fsS -m 10 --resolve cache.grain.test:%d:$gw https://cache.grain.test:%d/`, port, port)
	out, code, err := orchestrator.RunSandboxCommand(context.Background(), sb, []string{"sh", "-c", script}, "")
	if err != nil {
		t.Fatalf("running curl in %s: %v", sb.Name(), err)
	}
	return code == 0 && strings.Contains(out, "cache ok"), out
}

func mustVerify(t *testing.T, sb orchestrator.Sandbox, port int, what string) {
	t.Helper()
	if ok, out := curlVerifies(t, sb, port); !ok {
		t.Fatalf("%s does not verify the package cache: %s", what, out)
	}
	t.Logf("%s verifies the package cache", what)
}
