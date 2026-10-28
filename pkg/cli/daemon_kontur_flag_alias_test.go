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

package cli

import (
	"bytes"
	"log"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestKonturFlagAliasesBindToGranuleConfig(t *testing.T) {
	t.Parallel()

	args := []string{
		"-data-dir", "/var/lib/grain/data",
		"-kontur-sandboxes",
		"-kontur-ssh-user", "debian",
		"-kontur-workspace", "/home/debian/work",
		"-kontur-net", "nat",
		"-kontur-git-proxy-host", "172.17.0.1",
		"-kontur-create-arg", "-disk",
		"-kontur-create-arg", "/images/disk.img",
		"-kontur-base-port", "30080",
		"-kontur-state-dir", "/var/lib/custom-granule",
		"-kontur-exec-key", "/images/key",
	}

	cfg, _, unknown, err := parseDaemonArgs(args)
	if err != nil {
		t.Fatalf("parseDaemonArgs failed: %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("expected 0 unknown flags, got: %v", unknown)
	}

	if !cfg.GranuleSandboxes {
		t.Errorf("cfg.granuleSandboxes = false, want true")
	}
	if cfg.GranuleSSHUser != "debian" {
		t.Errorf("cfg.granuleSSHUser = %q, want %q", cfg.GranuleSSHUser, "debian")
	}
	if cfg.GranuleWorkspace != "/home/debian/work" {
		t.Errorf("cfg.granuleWorkspace = %q, want %q", cfg.GranuleWorkspace, "/home/debian/work")
	}
	if cfg.GranuleNet != "nat" {
		t.Errorf("cfg.granuleNet = %q, want %q", cfg.GranuleNet, "nat")
	}
	if cfg.GranuleGitProxyHost != "172.17.0.1" {
		t.Errorf("cfg.granuleGitProxyHost = %q, want %q", cfg.GranuleGitProxyHost, "172.17.0.1")
	}
	wantCreateArgs := []string{"-disk", "/images/disk.img"}
	if !slices.Equal(cfg.GranuleCreateArgs, wantCreateArgs) {
		t.Errorf("cfg.granuleCreateArgs = %v, want %v", cfg.GranuleCreateArgs, wantCreateArgs)
	}
	if cfg.GranuleBasePort != 30080 {
		t.Errorf("cfg.granuleBasePort = %d, want 30080", cfg.GranuleBasePort)
	}
	if cfg.GranuleStateDir != "/var/lib/custom-granule" {
		t.Errorf("cfg.granuleStateDir = %q, want %q", cfg.GranuleStateDir, "/var/lib/custom-granule")
	}
	if cfg.GranuleExecKey != "/images/key" {
		t.Errorf("cfg.granuleExecKey = %q, want %q", cfg.GranuleExecKey, "/images/key")
	}

	if err := checkConfig(cfg); err != nil {
		t.Fatalf("checkConfig failed on legacy kontur flags: %v", err)
	}
}

func TestCanonicalGranuleFlagsPrecedeKonturAliases(t *testing.T) {
	t.Parallel()

	args := []string{
		"-data-dir", "/var/lib/grain/data",
		"-kontur-sandboxes=false",
		"-granule-sandboxes=true",
		"-kontur-ssh-user", "legacy-user",
		"-granule-ssh-user", "canonical-user",
		"-kontur-workspace", "/legacy/work",
		"-granule-workspace", "/canonical/work",
		"-kontur-net", "nat",
		"-granule-net", "flat",
		"-kontur-git-proxy-host", "10.0.0.1",
		"-granule-git-proxy-host", "172.17.0.1",
		"-kontur-create-arg", "-legacy",
		"-granule-create-arg", "-canonical",
		"-kontur-base-port", "1111",
		"-granule-base-port", "2222",
		"-kontur-state-dir", "/legacy/state",
		"-granule-state-dir", "/canonical/state",
		"-kontur-exec-key", "/legacy/key",
		"-granule-exec-key", "/canonical/key",
	}

	cfg, _, unknown, err := parseDaemonArgs(args)
	if err != nil {
		t.Fatalf("parseDaemonArgs failed: %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("expected 0 unknown flags, got: %v", unknown)
	}

	if !cfg.GranuleSandboxes {
		t.Errorf("cfg.granuleSandboxes = false, want true")
	}
	if cfg.GranuleSSHUser != "canonical-user" {
		t.Errorf("cfg.granuleSSHUser = %q, want %q", cfg.GranuleSSHUser, "canonical-user")
	}
	if cfg.GranuleWorkspace != "/canonical/work" {
		t.Errorf("cfg.granuleWorkspace = %q, want %q", cfg.GranuleWorkspace, "/canonical/work")
	}
	if cfg.GranuleNet != "flat" {
		t.Errorf("cfg.granuleNet = %q, want %q", cfg.GranuleNet, "flat")
	}
	if cfg.GranuleGitProxyHost != "172.17.0.1" {
		t.Errorf("cfg.granuleGitProxyHost = %q, want %q", cfg.GranuleGitProxyHost, "172.17.0.1")
	}
	if !slices.Contains(cfg.GranuleCreateArgs, "-canonical") || !slices.Contains(cfg.GranuleCreateArgs, "-legacy") {
		t.Errorf("cfg.granuleCreateArgs = %v, want both canonical and legacy args", cfg.GranuleCreateArgs)
	}
	if cfg.GranuleBasePort != 2222 {
		t.Errorf("cfg.granuleBasePort = %d, want 2222", cfg.GranuleBasePort)
	}
	if cfg.GranuleStateDir != "/canonical/state" {
		t.Errorf("cfg.granuleStateDir = %q, want %q", cfg.GranuleStateDir, "/canonical/state")
	}
	if cfg.GranuleExecKey != "/canonical/key" {
		t.Errorf("cfg.granuleExecKey = %q, want %q", cfg.GranuleExecKey, "/canonical/key")
	}
}

func TestKonturFlagsIncludedInFlagNames(t *testing.T) {
	t.Parallel()

	_, fs, _, err := parseDaemonArgs([]string{"-flag-names"})
	if err != nil {
		t.Fatalf("parseDaemonArgs failed: %v", err)
	}

	var buf bytes.Buffer
	printDaemonFlagNames(fs, &buf)
	names := buf.String()

	expectedFlags := []string{
		"kontur-sandboxes bool",
		"kontur-ssh-user value",
		"kontur-workspace value",
		"kontur-net value",
		"kontur-git-proxy-host value",
		"kontur-create-arg value",
		"kontur-base-port value",
		"kontur-state-dir value",
		"kontur-exec-key value",
	}

	for _, flagName := range expectedFlags {
		if !strings.Contains(names, flagName) {
			t.Errorf("printDaemonFlagNames missing %q in output:\n%s", flagName, names)
		}
	}
}

func TestKonturFlagAliasesDeprecationLogging(t *testing.T) {
	// Not parallel because log.SetOutput modifies global logger state
	var logBuf bytes.Buffer
	origOutput := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(origOutput)
		log.SetFlags(origFlags)
	}()

	args := []string{
		"-data-dir", "/var/lib/grain/data",
		"-kontur-sandboxes",
		"-kontur-ssh-user", "debian",
		"-kontur-workspace", "/home/debian/work",
		"-kontur-net", "nat",
		"-kontur-git-proxy-host", "172.17.0.1",
		"-kontur-create-arg", "-disk",
		"-kontur-base-port", "30080",
		"-kontur-state-dir", "/var/lib/custom-granule",
		"-kontur-exec-key", "/images/key",
	}

	cfg, _, _, err := parseDaemonArgs(args)
	if err != nil {
		t.Fatalf("parseDaemonArgs failed: %v", err)
	}
	if !cfg.GranuleSandboxes {
		t.Fatalf("cfg.granuleSandboxes = false, want true")
	}

	logged := logBuf.String()
	expectedNotices := []string{
		"grain daemon: -kontur-sandboxes is legacy alias for -granule-sandboxes; using -granule-sandboxes=true",
		"grain daemon: -kontur-state-dir is legacy alias for -granule-state-dir; using -granule-state-dir=/var/lib/custom-granule",
		"grain daemon: -kontur-ssh-user is legacy alias for -granule-ssh-user; using -granule-ssh-user=debian",
		"grain daemon: -kontur-exec-key is legacy alias for -granule-exec-key; using -granule-exec-key=/images/key",
		"grain daemon: -kontur-workspace is legacy alias for -granule-workspace; using -granule-workspace=/home/debian/work",
		"grain daemon: -kontur-create-arg is legacy alias for -granule-create-arg; forwarded 1 argument(s)",
		"grain daemon: -kontur-net is legacy alias for -granule-net; using -granule-net=nat",
		"grain daemon: -kontur-base-port is legacy alias for -granule-base-port; using -granule-base-port=30080",
		"grain daemon: -kontur-git-proxy-host is legacy alias for -granule-git-proxy-host; using -granule-git-proxy-host=172.17.0.1",
	}

	for _, want := range expectedNotices {
		if !strings.Contains(logged, want) {
			t.Errorf("missing expected deprecation warning in log:\nwant: %s\ngot:\n%s", want, logged)
		}
	}
}

func TestKonturSandboxesSelectsGranuleBackendWithoutHostFallback(t *testing.T) {
	t.Parallel()

	args := []string{
		"-data-dir", "/var/lib/grain/data",
		"-kontur-sandboxes",
		"-kontur-ssh-user", "debian",
		"-kontur-workspace", "/home/debian/work",
		"-kontur-git-proxy-host", "172.17.0.1",
	}

	cfg, _, _, err := parseDaemonArgs(args)
	if err != nil {
		t.Fatalf("parseDaemonArgs failed: %v", err)
	}

	if cfg.SandboxBackend() != model.SandboxBackendGranule {
		t.Errorf("cfg.sandboxBackend() = %v, want %v", cfg.SandboxBackend(), model.SandboxBackendGranule)
	}
	selected := daemon.SelectedSandboxBackends(cfg)
	if len(selected) != 1 || selected[0] != "-granule-sandboxes" {
		t.Errorf("selectedSandboxBackends = %v, want [-granule-sandboxes]", selected)
	}
	if err := checkConfig(cfg); err != nil {
		t.Errorf("checkConfig failed: %v", err)
	}
}

func TestKonturSandboxesMissingRequiredFieldsRefused(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name: "missing ssh user",
			args: []string{
				"-data-dir", "/var/lib/grain/data",
				"-kontur-sandboxes",
				"-kontur-workspace", "/home/debian/work",
				"-kontur-git-proxy-host", "172.17.0.1",
			},
			wantErr: "-granule-ssh-user is required with -granule-sandboxes",
		},
		{
			name: "missing workspace",
			args: []string{
				"-data-dir", "/var/lib/grain/data",
				"-kontur-sandboxes",
				"-kontur-ssh-user", "debian",
				"-kontur-git-proxy-host", "172.17.0.1",
			},
			wantErr: "-granule-workspace is required with -granule-sandboxes",
		},
		{
			name: "missing git proxy host",
			args: []string{
				"-data-dir", "/var/lib/grain/data",
				"-kontur-sandboxes",
				"-kontur-ssh-user", "debian",
				"-kontur-workspace", "/home/debian/work",
			},
			wantErr: "-granule-git-proxy-host is required with -granule-sandboxes",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _, _, err := parseDaemonArgs(tc.args)
			if err != nil {
				t.Fatalf("parseDaemonArgs failed: %v", err)
			}
			err = checkConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("checkConfig error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}
