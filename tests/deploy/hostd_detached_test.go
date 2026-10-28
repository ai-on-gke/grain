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

package deploy

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent/claude"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

// detachedEnv is what hostd_allowlist_content reads for a core-track
// allowlist with a data directory.
var detachedEnv = []string{"GRAIN_INSTALL_TRACK=oss", "HOSTD_AGY_PATH=", "GRAIN_DATA_DIR=/d", "GRAIN_SANDBOX_DIR=/s",
	"HOSTD_PROXY_VARS=HTTPS_PROXY", "HOST_GRAIN_BIN=/usr/local/bin/grain", "HOSTD_VERSIONS_DIR=/usr/local/lib/grain/hostd"}

// The allowlist offers claude-detached: the image's claude, extracted
// beside hostd's grain, with the container's HOME and exactly the
// variables pkg/agent/claude sends (docs/non-disruptive-upgrade.md,
// "Which substrate carries which CLI"). hostd accepts a run's request
// for it, and refuses one that would move HOME or carry anything else.
func TestTheAllowlistOffersAContainerEquivalentClaude(t *testing.T) {
	content := runBash(t, shellFunction(t, "hostd_allowlist_content")+"hostd_allowlist_content", detachedEnv...)
	a, err := hostd.ParseAllowlist([]byte(content))
	if err != nil {
		t.Fatalf("hostd refuses the allowlist: %v\n%s", err, content)
	}
	e := a.Lookup(hostd.KindCLI, "claude-detached")
	if e == nil {
		t.Fatalf("no claude-detached entry:\n%s", content)
	}
	if e.Path != "/usr/local/lib/grain/hostd/current/claude" {
		t.Errorf("path = %q, want the extracted claude behind the current link", e.Path)
	}
	if strings.Join(e.PathsUnder, ",") != "/d,/s" || strings.Join(e.CwdUnder, ",") != "/d,/s" {
		t.Errorf("paths_under = %v, cwd_under = %v, want the data and sandbox directories", e.PathsUnder, e.CwdUnder)
	}
	if e.SetEnv["HOME"] != "/d/home" || e.SetEnv["GRAIN_LOCAL_API_TOKEN_FILE"] != "/d/local-api-token" || len(e.SetEnv) != 2 {
		t.Errorf("set_env = %v, want the container's HOME and the local API token file", e.SetEnv)
	}
	if strings.Join(e.PassEnv, ",") != "HTTPS_PROXY" {
		t.Errorf("pass_env = %v, want the proxy", e.PassEnv)
	}
	got, want := slices.Sorted(slices.Values(e.Env)), slices.Sorted(slices.Values(claude.HostdEnv()))
	if !slices.Equal(got, want) {
		t.Errorf("env = %v, want exactly what pkg/agent/claude sends: %v", got, want)
	}

	req := &hostd.Request{Kind: hostd.KindCLI, Name: "claude-detached", Args: []string{"-p", "--output-format", "stream-json"},
		Env: []string{"CLAUDE_CODE_OAUTH_TOKEN=tok", "MCP_TOOL_TIMEOUT=1", "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1"}}
	spec, err := a.Check(req, []string{"HOME=/home/grain", "PATH=/usr/bin", "HTTPS_PROXY=http://proxy"})
	if err != nil {
		t.Fatalf("hostd refuses a claude run's request: %v", err)
	}
	if !slices.Contains(spec.Env, "HOME=/d/home") || !slices.Contains(spec.Env, "CLAUDE_CODE_OAUTH_TOKEN=tok") {
		t.Errorf("child environment = %v, want the container's HOME and the token", spec.Env)
	}
	for _, bad := range []string{"HOME=/elsewhere", "ANTHROPIC_API_KEY=k", "GRAIN_LOCAL_API_TOKEN_FILE=/d/x"} {
		req.Env = []string{bad}
		if _, err := a.Check(req, nil); err == nil {
			t.Errorf("hostd accepted %s for claude-detached", bad)
		}
	}

	c := a.Lookup(hostd.KindCLI, "codex-detached")
	if c == nil || c.Path != "/usr/local/lib/grain/hostd/current/codex" || c.SetEnv["HOME"] != "/d/home" {
		t.Errorf("codex-detached = %+v", c)
	}
	// The operator's own entries keep their names: nothing called claude.
	if a.Lookup(hostd.KindCLI, "claude") != nil {
		t.Error("the default allowlist names a claude entry, whose meaning (the host's own claude) is the operator's")
	}
	// hostd_detached_cli_entries, which a re-run adds, is the same text.
	entries := runBash(t, shellFunction(t, "hostd_allowlist_content")+shellFunction(t, "hostd_detached_cli_entries")+
		"hostd_detached_cli_entries", detachedEnv...)
	if !strings.Contains(content, entries) || !strings.HasPrefix(entries, "  claude-detached:\n") || !strings.Contains(entries, "  codex-detached:\n") {
		t.Errorf("hostd_detached_cli_entries =\n%s\nnot the allowlist's own entries:\n%s", entries, content)
	}
}

// A re-run adds the detached entries to an allowlist an earlier run
// wrote, under its clis: key, and changes nothing the second time; one of
// another shape is told what to add.
func TestARerunAddsTheDetachedEntriesToAnOlderAllowlist(t *testing.T) {
	dir := t.TempDir()
	allow := filepath.Join(dir, "hostd.yaml")
	fns := logFn + "chown() { :; }\n" + shellFunction(t, "hostd_allowlist_content") + shellFunction(t, "hostd_detached_cli_entries") +
		shellFunction(t, "ensure_hostd_detached_clis")
	env := append([]string{"HOSTD_ALLOWLIST=" + allow}, detachedEnv...)

	for _, before := range []string{
		"clis:\n  agy:\n    path: /opt/acme/bin/agy\ncapabilities:\n  host-git:\n    command: /usr/local/bin/grain\n",
		"clis: {}\ncapabilities:\n  host-git:\n    command: /usr/local/bin/grain\n",
		// One an operator already gave a claude-detached of their own.
		"clis:\n  claude-detached:\n    path: /opt/mine/claude\ncapabilities: {}\n",
	} {
		if err := os.WriteFile(allow, []byte(before), 0o644); err != nil {
			t.Fatal(err)
		}
		out := runBash(t, fns+"ensure_hostd_detached_clis", env...)
		contains(t, out, "added the container-equivalent cli entries")
		data, _ := os.ReadFile(allow)
		a, err := hostd.ParseAllowlist(data)
		if err != nil || a.Lookup(hostd.KindCLI, "claude-detached") == nil || a.Lookup(hostd.KindCLI, "codex-detached") == nil {
			t.Fatalf("after the re-run (%v):\n%s", err, data)
		}
		if strings.Contains(before, "/opt/mine/claude") && a.Lookup(hostd.KindCLI, "claude-detached").Path != "/opt/mine/claude" {
			t.Errorf("the operator's own claude-detached was replaced:\n%s", data)
		}
		if strings.Contains(before, "agy") && a.Lookup(hostd.KindCLI, "agy") == nil {
			t.Errorf("agy was lost:\n%s", data)
		}
		runBash(t, fns+"ensure_hostd_detached_clis", env...)
		if again, _ := os.ReadFile(allow); string(again) != string(data) {
			t.Errorf("a second re-run changed the allowlist:\n%s", again)
		}
	}

	edited := "capabilities: {}\n"
	if err := os.WriteFile(allow, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runBash(t, fns+"ensure_hostd_detached_clis", env...)
	contains(t, out, "has no cli entries claude-detached, codex-detached")
	contains(t, out, "path: /usr/local/lib/grain/hostd/current/claude")
	if data, _ := os.ReadFile(allow); string(data) != edited {
		t.Errorf("an allowlist of another shape was edited:\n%s", data)
	}
	contains(t, body(t, setupCode(t), "write_hostd_allowlist() {"), "ensure_hostd_detached_clis")
}

// With hostd, claude defaults to detached; an explicit setting, an
// operator's own claude and an install without hostd keep what they had.
func TestClaudeDefaultsToDetachedWithHostd(t *testing.T) {
	out, _ := resolveHostd(t, "GRAIN_HOSTD=1", "GRAIN_CLAUDE_PATH=")
	contains(t, out, "claude runs detached through hostd")
	got := func(env ...string) string {
		out := runBash(t, logFn+shellFunction(t, "resolve_hostd")+`resolve_hostd; echo "RUN=$GRAIN_CLAUDE_RUN"`,
			append([]string{"GRAIN_USER=grain", "GRAIN_AGY_PATH=", "HOSTD_AGY_PATH=",
				"GRAIN_CLAUDE_RUN=", "GRAIN_CODEX_RUN=", "GRAIN_AGY_RUN=", "GRAIN_CLAUDE_PATH="}, env...)...)
		for _, line := range strings.Split(out, "\n") {
			if v, ok := strings.CutPrefix(line, "RUN="); ok {
				return v
			}
		}
		t.Fatalf("no RUN= in\n%s", out)
		return ""
	}
	for _, tc := range []struct {
		env  []string
		want string
	}{
		{[]string{"GRAIN_HOSTD=1"}, "detached"},
		{[]string{"GRAIN_HOSTD=1", "GRAIN_CLAUDE_RUN=container"}, "container"},
		{[]string{"GRAIN_HOSTD=1", "GRAIN_CLAUDE_RUN=host"}, "host"},
		{[]string{"GRAIN_HOSTD=1", "GRAIN_CLAUDE_PATH=/opt/claude"}, ""},
		{[]string{"GRAIN_HOSTD=0"}, ""},
		{[]string{"GRAIN_HOSTD=0", "GRAIN_CLAUDE_RUN=detached"}, ""},
	} {
		if v := got(tc.env...); v != tc.want {
			t.Errorf("%v: GRAIN_CLAUDE_RUN = %q, want %q", tc.env, v, tc.want)
		}
	}
	// detached is claude's alone, for now.
	text := setupText(t)
	contains(t, text, `only claude has a detached run location yet`)
}

// The extractor takes the image's claude, codex and kubectl out beside
// hostd's grain, dereferenced, into the image's own version directory:
// never over a copy a version already has (a detached claude adopted
// across an upgrade may be running it), skipped when nothing changed, and
// re-run for a version an older extractor installed without them. The
// image's kubectl is linked onto PATH only for a host with none.
func TestTheExtractorTakesTheImagesCLIs(t *testing.T) {
	dir := t.TempDir()
	extractor := filepath.Join(dir, "lib", "extract-host-binaries.sh")
	idFile := filepath.Join(dir, "lib", ".extracted-image-id")
	hostGrain := filepath.Join(dir, "bin", "grain")
	versions := filepath.Join(dir, "lib", "hostd")
	kubectlLink := filepath.Join(dir, "bin", "kubectl")
	docker := filepath.Join(dir, "bin", "docker")
	cpLog := filepath.Join(dir, "cp.log")
	for _, d := range []string{filepath.Dir(extractor), filepath.Dir(hostGrain)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// docker cp writes "<name> from <image id>" for each binary; -L is
	// required for the CLIs (claude's is a symlink in the image).
	stub := `#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  image) echo "${STUB_IMAGE_ID}" ;;
  create) echo cid ;;
  cp)
    shift
    deref=0
    if [ "$1" = -L ]; then deref=1; shift; fi
    name="${1##*/}"
    echo "$name deref=$deref" >> "` + cpLog + `"
    case "$name" in
      grain) ;;
      claude|codex|kubectl) [ "$deref" = 1 ] || exit 1 ;;
      *) exit 1 ;;
    esac
    printf '#!/bin/sh\necho %s %s\n' "$name" "$STUB_IMAGE_ID" > "$2"
    ;;
  rm) ;;
esac
`
	if err := os.WriteFile(docker, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(id string) {
		t.Helper()
		runBash(t, shellFunction(t, "image_ref_from")+shellFunction(t, "write_host_extractor")+
			"write_host_extractor\n\"$HOST_EXTRACTOR\" "+channelImage+":main\n",
			"GRAIN_IMAGE="+channelImage, "GRAIN_IMAGE_REF="+channelImage+":main", "GRAIN_USER=grain",
			"IMAGE_REF_FILE="+filepath.Join(dir, "image.env"), "UNIT_IMAGE_REF_DIR="+dir, "UNIT_IMAGE_REF_FILE="+filepath.Join(dir, "unit.env"),
			"HOST_EXTRACTOR="+extractor, "EXTRACTED_ID_FILE="+idFile, "HOSTD_EXTRACTED_ID_FILE="+idFile,
			"HOST_GRAIN_BIN="+hostGrain, "HOSTD_VERSIONS_DIR="+versions, "DOCKER_BIN="+docker,
			"HOST_KUBECTL_BIN="+kubectlLink, "STUB_IMAGE_ID="+id, "PATH=/usr/bin:/bin")
	}
	read := func(p string) string {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	run("sha256:aaaaaaaaaaaa1")
	for _, name := range []string{"grain", "claude", "codex", "kubectl"} {
		p := filepath.Join(versions, "current", name)
		if fi, err := os.Stat(p); err != nil || fi.Mode()&0o111 == 0 {
			t.Fatalf("%s: %v (mode %v)", p, err, fi)
		}
	}
	contains(t, read(filepath.Join(versions, "aaaaaaaaaaaa", "claude")), "claude sha256:aaaaaaaaaaaa1")
	// No stage is left behind.
	if left, _ := filepath.Glob(filepath.Join(dir, "lib", ".extract-stage.*")); len(left) != 0 {
		t.Errorf("stage left behind: %v", left)
	}
	// The host has no kubectl on the PATH the extractor looks on only if
	// this machine lacks one; either way, a link is only ever grain's.
	if target, err := os.Readlink(kubectlLink); err == nil && target != filepath.Join(versions, "current", "kubectl") {
		t.Errorf("kubectl link = %q", target)
	}

	// Same image: the fast path copies nothing.
	os.Remove(cpLog)
	run("sha256:aaaaaaaaaaaa1")
	if _, err := os.Stat(cpLog); err == nil {
		t.Errorf("the same image was extracted again:\n%s", read(cpLog))
	}

	// A version an older extractor installed (no marker) is not skipped,
	// and a CLI already in a version is never written over.
	if err := os.Remove(filepath.Join(versions, "aaaaaaaaaaaa", ".clis")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versions, "aaaaaaaaaaaa", "claude"), []byte("#!/bin/sh\necho running\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run("sha256:aaaaaaaaaaaa1")
	contains(t, read(cpLog), "claude deref=1")
	if got := read(filepath.Join(versions, "aaaaaaaaaaaa", "claude")); got != "#!/bin/sh\necho running\n" {
		t.Errorf("a version's claude was written over: %q", got)
	}

	// An upgrade is a new directory and a moved link: the old version's
	// claude is still there for the run that is executing it.
	run("sha256:bbbbbbbbbbbb2")
	if target, _ := os.Readlink(filepath.Join(versions, "current")); target != "bbbbbbbbbbbb" {
		t.Fatalf("current = %q after the upgrade", target)
	}
	contains(t, read(filepath.Join(versions, "current", "claude")), "claude sha256:bbbbbbbbbbbb2")
	if _, err := os.Stat(filepath.Join(versions, "aaaaaaaaaaaa", "claude")); err != nil {
		t.Errorf("the previous version's claude went with the upgrade: %v", err)
	}
}
