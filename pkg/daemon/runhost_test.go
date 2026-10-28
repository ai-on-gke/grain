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
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent/claude"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// TestNewRunHostGivesDockerRunHostsTheCredentialDirectoryAsHome: a docker
// run host runs as the daemon's uid, which has no passwd entry in the
// image, so its CLI got HOME=/ and could not write ~/.claude
// (docs/test-runs/2026-10-14-run-hosts-daemon.md). The credential
// directory is where the design puts ~/.claude, and is mounted
// read-write, so it is HOME -- as it already was for run pods.
func TestNewRunHostGivesDockerRunHostsTheCredentialDirectoryAsHome(t *testing.T) {
	dataDir := t.TempDir()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	credDir := filepath.Join(t.TempDir(), "agent-home")
	h, err := NewRunHost(context.Background(), Config{
		DataDir: dataDir, RunHostImage: "grain:test", RunHostCredentialDir: credDir,
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := h.Driver.(runhost.Docker)
	if !ok {
		t.Fatalf("driver = %T, want runhost.Docker", h.Driver)
	}
	if d.Env["HOME"] != credDir {
		t.Fatalf("docker run host HOME = %q, want the credential directory %q", d.Env["HOME"], credDir)
	}
	mounted := false
	for _, m := range h.Mounts {
		mounted = mounted || (m.Path == credDir && !m.ReadOnly)
	}
	if !mounted {
		t.Fatalf("HOME %s is not mounted read-write into the run host: %+v", credDir, h.Mounts)
	}
}

// TestClaudeUnderARunHostStartsTheRunHostsOwnMCPServer: the --mcp-config
// a claude run is handed is read, and its mcpserver started, inside the
// run host. The hand run (docs/test-runs/2026-10-15-upgrade-adopts-live-claude-run.md)
// had the daemon installed somewhere other than the image's
// /usr/local/bin/grain, the config named the daemon's own path, and every
// run started with no tools and failed "the agent made no tool calls".
func TestClaudeUnderARunHostStartsTheRunHostsOwnMCPServer(t *testing.T) {
	dataDir := t.TempDir()
	cfg := Config{
		DataDir: dataDir, ClaudePath: "/opt/elsewhere/claude",
		ClaudeAuthMode: model.AntigravityAuthAmbient,
		RunHost:        &runhost.Host{Grain: runHostGrain},
	}
	fw, err := BuildClaudeFramework(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := fw.(*claude.Framework)
	if !ok {
		t.Fatalf("framework = %T, want *claude.Framework", fw)
	}
	if got := c.GrainBinaryPath(); got != runHostGrain {
		t.Fatalf("a run host's --mcp-config starts %q, want the run host image's own %q", got, runHostGrain)
	}
}
