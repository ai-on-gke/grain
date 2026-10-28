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

package repodecl_test

// `.grain/lifecycle.yml`: how a repository says its own tasks move
// (docs/task-lifecycle-sandbox.md). What a well-formed declaration
// produces, what the defaults are, and what each refusal actually says
// -- the namespace refusal most of all, since it and package-publish's
// own scoping are the two halves of the load-bearing security property
// of the whole design.

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

const wellFormed = `
name: grain task lifecycle
image: ghcr.io/bwsalmon/grain/grain-lifecycle
serve:
  run: python3 -m lifecycle.server
  working-directory: /opt/grain-lifecycle
  listen: /run/grain/lifecycle.sock
  env:
    LIFECYCLE_LOG_LEVEL: info
  ready-timeout-seconds: 45
  request-timeout-ms: 8000
  sandbox:
    cpus: 1
    memory-mb: 1024
`

func lifecycle(t *testing.T, text string) model.LifecycleDeclaration {
	t.Helper()
	declared, path, err := repodecl.LoadLifecycle(
		fstest.MapFS{model.LifecycleDeclarationPath: &fstest.MapFile{Data: []byte(text)}}, repo)
	if err != nil {
		t.Fatalf("LoadLifecycle: %v", err)
	}
	if path != model.LifecycleDeclarationPath {
		t.Fatalf("read from %q, want %q", path, model.LifecycleDeclarationPath)
	}
	return declared
}

func lifecycleErr(t *testing.T, text string) string {
	t.Helper()
	declared, _, err := repodecl.LoadLifecycle(
		fstest.MapFS{model.LifecycleDeclarationPath: &fstest.MapFile{Data: []byte(text)}}, repo)
	if err == nil {
		t.Fatalf("LoadLifecycle: no error, and it declared %+v", declared)
	}
	return err.Error()
}

func TestLifecycleDeclarationReadsAWellFormedFile(t *testing.T) {
	declared := lifecycle(t, wellFormed)
	if !declared.Declared() {
		t.Fatal("a file with a serve.run in it declares a lifecycle")
	}
	if declared.Name != "grain task lifecycle" {
		t.Errorf("name %q", declared.Name)
	}
	if declared.Image != "ghcr.io/bwsalmon/grain/grain-lifecycle" {
		t.Errorf("image %q", declared.Image)
	}
	if declared.Serve.Run != "python3 -m lifecycle.server" {
		t.Errorf("run %q", declared.Serve.Run)
	}
	if declared.Serve.WorkingDirectory != "/opt/grain-lifecycle" {
		t.Errorf("working-directory %q", declared.Serve.WorkingDirectory)
	}
	if declared.Serve.ReadyTimeout != 45*time.Second {
		t.Errorf("ready timeout %s", declared.Serve.ReadyTimeout)
	}
	if declared.Serve.RequestTimeout != 8*time.Second {
		t.Errorf("request timeout %s", declared.Serve.RequestTimeout)
	}
	if declared.Serve.Sandbox != (model.LifecycleSandbox{CPUs: 1, MemoryMB: 1024}) {
		t.Errorf("sandbox %+v", declared.Serve.Sandbox)
	}
	if declared.Serve.Env["LIFECYCLE_LOG_LEVEL"] != "info" {
		t.Errorf("env %v", declared.Serve.Env)
	}
}

// A repository with nothing to say gets the deployment's default, which
// is what every repository gets today. Not an error, and not a
// declaration.
func TestNoLifecycleFileDeclaresNoLifecycle(t *testing.T) {
	declared, path, err := repodecl.LoadLifecycle(fstest.MapFS{
		".grain/templates/nightly.yml": &fstest.MapFile{Data: []byte("title: nightly\n")},
	}, repo)
	if err != nil {
		t.Fatalf("LoadLifecycle: %v", err)
	}
	if path != "" {
		t.Errorf("read from %q, want nothing", path)
	}
	if declared.Declared() {
		t.Errorf("declared %+v", declared)
	}
}

// image: is optional, and its absence is the zero-config path: the stock
// guest with the repository checked out, no registry and no release job.
func TestLifecycleWithoutAnImageServesFromTheStockGuest(t *testing.T) {
	declared := lifecycle(t, "serve:\n  run: python3 -m lifecycle.server\n")
	if declared.Image != "" {
		t.Errorf("image %q", declared.Image)
	}
	if declared.Serve.Listen != model.DefaultLifecycleListen {
		t.Errorf("listen %q, want the default %q", declared.Serve.Listen, model.DefaultLifecycleListen)
	}
	if declared.Serve.ReadyTimeout != model.DefaultLifecycleReadyTimeout {
		t.Errorf("ready timeout %s", declared.Serve.ReadyTimeout)
	}
	if declared.Serve.RequestTimeout != model.DefaultLifecycleRequestTimeout {
		t.Errorf("request timeout %s", declared.Serve.RequestTimeout)
	}
	if !declared.Serve.Sandbox.Commitment().IsZero() {
		t.Errorf("a declaration that asked for no particular shape got %+v", declared.Serve.Sandbox)
	}
}

// The one load-bearing security property: a repository may only serve
// its lifecycle from an image it can itself publish. Its other half is
// package-publish resolving the credential for the job's own repository,
// so these two together are what keeps repository A out of repository
// B's lifecycle.
func TestLifecycleImageIsConfinedToTheRepositorysOwnNamespace(t *testing.T) {
	for _, image := range []string{
		"ghcr.io/attacker/evil/grain-lifecycle",
		"ghcr.io/bwsalmon/other-repo/grain-lifecycle",
		"ghcr.io/bwsalmon/grain-lifecycle",
		"grain-lifecycle",
		"bwsalmon/grain/grain-lifecycle",
	} {
		err := model.ValidateLifecycleImage(image, repo)
		if err == nil {
			t.Errorf("%q: accepted, and it is not this repository's to publish", image)
			continue
		}
		got := lifecycleErr(t, "image: "+image+"\nserve:\n  run: serve\n")
		if !strings.Contains(got, model.LifecycleDeclarationPath) || !strings.Contains(got, "image:") {
			t.Errorf("%q: %q does not name the file and the key", image, got)
		}
	}
	for _, image := range []string{
		"ghcr.io/bwsalmon/grain/grain-lifecycle",
		"ghcr.io/BWSalmon/Grain/grain-lifecycle/deep",
		"registry.example.com:5000/bwsalmon/grain/lifecycle",
		"localhost/bwsalmon/grain/lifecycle",
	} {
		if err := model.ValidateLifecycleImage(strings.ToLower(image), repo); err != nil {
			t.Errorf("%q: %v", image, err)
		}
	}
}

// `image:` is a name. The version is the digest grain resolves it to
// after a release, and a declaration pinning its own tag would be a
// second, staler answer to "which version is live".
func TestLifecycleImageRefusesATagOrDigest(t *testing.T) {
	for _, image := range []string{
		"ghcr.io/bwsalmon/grain/lifecycle:v4",
		"ghcr.io/bwsalmon/grain/lifecycle@sha256:" + strings.Repeat("a", 64),
	} {
		err := model.ValidateLifecycleImage(image, repo)
		if err == nil {
			t.Fatalf("%q: accepted", image)
		}
		if !strings.Contains(err.Error(), "digest grain resolves it to") {
			t.Errorf("%q: %v", image, err)
		}
	}
}

// There is no build: block, and the refusal is the design's build story
// in one error: the build is a release job.
func TestLifecycleRefusesABuildBlock(t *testing.T) {
	got := lifecycleErr(t, "serve:\n  run: serve\nbuild:\n  dockerfile: Dockerfile\n")
	for _, want := range []string{"build:", "release job", "docs/task-lifecycle-sandbox.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q does not mention %q", got, want)
		}
	}
}

// The reserved keys: every one of them is something a repository would
// naturally write here and is exactly what a file an agent can rewrite
// does not get to say.
func TestLifecycleRefusesWhatTheSandboxMayTouch(t *testing.T) {
	for key, text := range map[string]string{
		"capabilities":          "serve:\n  run: serve\ncapabilities: [package-publish]\n",
		"secrets":               "serve:\n  run: serve\nsecrets:\n  TOKEN: x\n",
		"repo":                  "serve:\n  run: serve\nrepo: other/thing\n",
		"serve.network":         "serve:\n  run: serve\n  network: open\n",
		"serve.checkout":        "serve:\n  run: serve\n  checkout: true\n",
		"serve.sandbox.network": "serve:\n  run: serve\n  sandbox:\n    network: open\n",
	} {
		got := lifecycleErr(t, text)
		if !strings.Contains(got, key) {
			t.Errorf("%s: %q does not name the key", key, got)
		}
		if !strings.Contains(got, "no network") {
			t.Errorf("%s: %q does not say what the serving sandbox gets", key, got)
		}
	}
}

// KnownFields, like every other declaration grain reads: a misspelt key
// is an error naming it rather than a setting that quietly did nothing.
func TestLifecycleRefusesAnUnknownKey(t *testing.T) {
	got := lifecycleErr(t, "serve:\n  run: serve\n  reqeust-timeout-ms: 100\n")
	if !strings.Contains(got, "reqeust-timeout-ms") {
		t.Errorf("%q does not name the misspelt key", got)
	}
}

func TestLifecycleNeedsAServeCommand(t *testing.T) {
	got := lifecycleErr(t, "name: mine\nimage: ghcr.io/bwsalmon/grain/lifecycle\n")
	if !strings.Contains(got, "serve.run") {
		t.Errorf("%q does not name what is missing", got)
	}
}

func TestLifecycleRequestTimeoutHasACeiling(t *testing.T) {
	got := lifecycleErr(t, "serve:\n  run: serve\n  request-timeout-ms: 60000\n")
	if !strings.Contains(got, "ceiling") || !strings.Contains(got, "30000") {
		t.Errorf("%q does not say what the ceiling is", got)
	}
	if declared := lifecycle(t, "serve:\n  run: serve\n  request-timeout-ms: 30000\n"); declared.Serve.RequestTimeout != model.MaxLifecycleRequestTimeout {
		t.Errorf("the ceiling itself was refused: %s", declared.Serve.RequestTimeout)
	}
}

// Nothing outside the guest can reach this socket, so there is no
// address worth binding but its own.
func TestLifecycleListenIsALoopbackOrASocket(t *testing.T) {
	for _, listen := range []string{"/run/grain/lifecycle.sock", "127.0.0.1:8080", "[::1]:8080"} {
		if err := model.ValidateLifecycleListen(listen); err != nil {
			t.Errorf("%q: %v", listen, err)
		}
	}
	for _, listen := range []string{"0.0.0.0:8080", ":8080", "lifecycle.sock", "127.0.0.1:", "127.0.0.1:http"} {
		if err := model.ValidateLifecycleListen(listen); err == nil {
			t.Errorf("%q: accepted", listen)
		}
	}
	if got := lifecycleErr(t, "serve:\n  run: serve\n  listen: 0.0.0.0:80\n"); !strings.Contains(got, "serve.listen") {
		t.Errorf("%q does not name the key", got)
	}
}

// The token is the only thing authenticating the reducer, so a
// declaration that set it would be overwriting its own boundary.
func TestLifecycleEnvCannotShadowGrainsOwnVariables(t *testing.T) {
	got := lifecycleErr(t, "serve:\n  run: serve\n  env:\n    "+model.LifecycleTokenEnv+": mine\n")
	if !strings.Contains(got, model.LifecycleTokenEnv) {
		t.Errorf("%q does not name the variable", got)
	}
}

// Two files, one identity, with whichever was read last deciding how a
// repository's tasks behave.
func TestLifecycleRefusesBothSpellings(t *testing.T) {
	_, _, err := repodecl.LoadLifecycle(fstest.MapFS{
		model.LifecycleDeclarationPath:    &fstest.MapFile{Data: []byte("serve:\n  run: a\n")},
		model.LifecycleDeclarationAltPath: &fstest.MapFile{Data: []byte("serve:\n  run: b\n")},
	}, repo)
	if err == nil {
		t.Fatal("two declarations, no error")
	}
	if !strings.Contains(err.Error(), "exactly one answer") {
		t.Errorf("%v", err)
	}
}

// The .yaml spelling is read, since both are what people type.
func TestLifecycleReadsTheYamlSpelling(t *testing.T) {
	declared, path, err := repodecl.LoadLifecycle(fstest.MapFS{
		model.LifecycleDeclarationAltPath: &fstest.MapFile{Data: []byte("serve:\n  run: serve\n")},
	}, repo)
	if err != nil {
		t.Fatalf("LoadLifecycle: %v", err)
	}
	if path != model.LifecycleDeclarationAltPath || !declared.Declared() {
		t.Errorf("path %q, declared %v", path, declared.Declared())
	}
}
