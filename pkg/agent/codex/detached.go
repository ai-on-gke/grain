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

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ai-on-gke/grain/pkg/mcp"
)

// DetachedSubcommand is the grain subcommand name (`grain codex-detached`)
// that starts a detached codex run's managed HTTP MCP adapter on the host
// before running codex.
const DetachedSubcommand = "codex-detached"

// detachedMCPSpec is the JSON payload writeConfig writes to
// $CODEX_HOME/mcpserver.json when WithDetachedMCP is set. The host-side
// `grain codex-detached` wrapper reads it, starts `grain mcpserver` behind
// mcp.StartCommandHTTP in its own process outside the daemon, and writes
// BaseConfig + the endpoint's [mcp_servers.grain-sandbox] stanza into
// $CODEX_HOME/config.toml before starting codex.
type detachedMCPSpec struct {
	GrainBinary string   `json:"grain_binary"`
	Args        []string `json:"args"`
	BaseConfig  string   `json:"base_config"`
}

// RunDetached implements `grain codex-detached <codex-bin> [args...]`.
//
// When $CODEX_HOME/mcpserver.json is present (and the invocation is not a
// --version/--help probe), it starts `grain mcpserver` behind
// mcp.StartCommandHTTP -- preserving docs/codex-isolation.md's per-run
// 256-bit bearer token, loopback-only listener, Host/Origin checks, and
// startup tools snapshot -- writes the resulting [mcp_servers.grain-sandbox]
// block into $CODEX_HOME/config.toml, and runs <codex-bin> with the
// remaining arguments until it exits.
func RunDetached(ctx context.Context, args []string) (int, error) {
	if len(args) == 0 || args[0] == "" {
		return 2, errors.New("usage: grain codex-detached <codex-bin> [args...]")
	}
	codexBin := args[0]
	codexArgs := args[1:]

	runCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	if !isProbeArgs(codexArgs) {
		if home := os.Getenv("CODEX_HOME"); home != "" {
			closeMCP, err := startDetachedMCP(runCtx, home)
			if err != nil {
				return 1, err
			}
			defer closeMCP()
		}
	}

	cmd := exec.CommandContext(runCtx, codexBin, codexArgs...)
	cmd.Env = append([]string{}, os.Environ()...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.WaitDelay = localWaitDelay
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		return 0, nil
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

const localWaitDelay = 5 * mcpToolTimeoutSlack / 24 // 5s

func isProbeArgs(args []string) bool {
	if len(args) == 1 {
		switch args[0] {
		case "--version", "-V", "--help", "-h":
			return true
		}
	}
	return false
}

func startDetachedMCP(ctx context.Context, home string) (func(), error) {
	specPath := filepath.Join(home, mcpServerFileName)
	data, err := os.ReadFile(specPath)
	if errors.Is(err, os.ErrNotExist) {
		return func() {}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", specPath, err)
	}
	var spec detachedMCPSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", specPath, err)
	}
	grainBin := spec.GrainBinary
	if grainBin == "" {
		if self, err := os.Executable(); err == nil {
			grainBin = self
		}
	} else if _, err := os.Stat(grainBin); err != nil {
		if self, selfErr := os.Executable(); selfErr == nil {
			grainBin = self
		}
	}
	endpoint, err := mcp.StartCommandHTTP(ctx, grainBin, spec.Args)
	if err != nil {
		return nil, err
	}
	cfg := spec.BaseConfig + mcpServerTOML(endpoint)
	configPath := filepath.Join(home, configFileName)
	if err := os.WriteFile(configPath, []byte(cfg), 0o600); err != nil {
		endpoint.Close()
		return nil, fmt.Errorf("writing %s: %w", configPath, err)
	}
	return endpoint.Close, nil
}
